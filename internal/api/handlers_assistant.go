package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/eyupio/zoomies/internal/assistant"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
)

// assistantProviderInput is the body of a create, a patch and a draft check.
// On a patch every field is optional; APIKey empty leaves the sealed key
// alone, so a form with a blank key box does not erase one.
type assistantProviderInput struct {
	// ID names the saved provider a draft check is about, so a blank key box
	// borrows the sealed key the row holds rather than testing with none.
	ID      *string `json:"id"`
	Name    *string `json:"name"`
	Kind    *string `json:"kind"`
	BaseURL *string `json:"base_url"`
	Model   *string `json:"model"`
	Enabled *bool   `json:"enabled"`
	APIKey  *string `json:"api_key"`
	// FleetAccess lets the assistant read this fleet through the provider. It
	// is the administrator's decision per provider, and absent leaves it as it was.
	FleetAccess *bool `json:"fleet_access"`
}

func (in assistantProviderInput) apply(p *store.AssistantProvider) {
	if in.Name != nil {
		p.Name = strings.TrimSpace(*in.Name)
	}
	if in.Kind != nil {
		p.Kind = strings.TrimSpace(*in.Kind)
	}
	if in.BaseURL != nil {
		p.BaseURL = strings.TrimSpace(*in.BaseURL)
	}
	if in.Model != nil {
		p.Model = strings.TrimSpace(*in.Model)
	}
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}
	if in.FleetAccess != nil {
		p.FleetAccess = *in.FleetAccess
	}
}

// validateAssistantProvider says what is wrong with a row as it would be
// saved. The fake kind is admitted only where the demo seeded one, because
// a person creating "a model that answers without a network" on a real
// fleet has misunderstood what it is for.
//
// The egress check runs only when the address is being saved (checkAddress):
// a row that holds a private address with the switch since turned off can
// still be disabled, renamed or re-keyed, and only re-saving the address is
// refused, which is what the switch's own text says.
func (s *Server) validateAssistantProvider(r *http.Request, p *store.AssistantProvider, checkAddress bool, apiKey *string) []fieldError {
	var errs []fieldError
	if p.Name == "" || len(p.Name) > 80 {
		errs = append(errs, fieldError{"name", "give the provider a name of 1 to 80 characters; it is how the cards tell two apart"})
	}
	kind := assistant.Kind(p.Kind)
	known := kind == assistant.KindFake && controller.DemoRequested()
	for _, k := range assistant.Kinds {
		if k == kind {
			known = true
		}
	}
	if !known {
		names := make([]string, len(assistant.Kinds))
		for i, k := range assistant.Kinds {
			names[i] = string(k)
		}
		errs = append(errs, fieldError{"kind", "choose one of " + strings.Join(names, ", ")})
	}
	// A subscription's tool has a model of its own when none is named.
	if p.Model == "" && !assistant.Subscription(kind) {
		errs = append(errs, fieldError{"model", "name the model, as the provider calls it (llama3.1, claude-sonnet-4-5, gpt-4o)"})
	}
	switch {
	case p.BaseURL == "" && kind == assistant.KindOpenAICompatible:
		errs = append(errs, fieldError{"base_url", "an OpenAI-compatible provider needs the address its server listens on, such as http://localhost:11434/v1"})
	case p.BaseURL != "":
		u, err := url.Parse(p.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fieldError{"base_url", "that is not an HTTP URL; the scheme is what decides whether the connection is encrypted, so it has to be there"})
		} else if f := config.CheckProviderURL(p.BaseURL, s.cfg().Assistant.AllowPrivateProvider); checkAddress && f != nil {
			errs = append(errs, fieldError{"base_url", f.Title + ". " + f.Fix})
		}
	}
	if assistant.Subscription(kind) && p.ID != "" && p.OwnerID == "" {
		errs = append(errs, fieldError{"kind", "Add a new personal subscription provider rather than converting an installation provider."})
	}
	if assistant.Subscription(kind) && !s.isAssistantAdmin(r) {
		errs = append(errs, fieldError{"kind", "Subscription tools on the controller require an administrator; use your personal API provider instead."})
	}
	if assistant.Subscription(kind) {
		errs = append(errs, subscriptionErrors(p, apiKey)...)
	}
	return errs
}

// subscriptionErrors are what is wrong with a provider that is somebody's own
// subscription, used through the vendor's own tool: it has no address, since the
// tool knows where it goes; no key, since Zoomies holds none of a subscription's
// credentials and the sign-in is made in the vendor's own tool; and no fleet
// access, since the tool is run with its own tools off and cannot be given Eli's.
func subscriptionErrors(p *store.AssistantProvider, apiKey *string) []fieldError {
	var errs []fieldError
	if p.BaseURL != "" {
		errs = append(errs, fieldError{"base_url", "this provider has no address: the tool it runs knows where it goes"})
	}
	if apiKey != nil && strings.TrimSpace(*apiKey) != "" {
		errs = append(errs, fieldError{"api_key", "Zoomies never holds a subscription's credentials: sign in with the vendor's own tool on the machine the controller runs on instead"})
	}
	if p.FleetAccess {
		errs = append(errs, fieldError{"fleet_access", "this provider cannot be given Eli's tools yet, so it cannot read the fleet"})
	}
	return errs
}

// ownerOnly refuses a person who is not the owner of a provider that is somebody's
// own subscription, and says so. Everyone else's use of one would be routing their
// requests through another person's plan, which its terms do not permit.
func (s *Server) ownerOnly(w http.ResponseWriter, r *http.Request, row *store.AssistantProvider) bool {
	if assistant.Subscription(assistant.Kind(row.Kind)) && !s.isAssistantAdmin(r) {
		forbidden(w, "Subscription tools on the controller require an administrator.")
		return false
	}
	if id := Identity(r.Context()); id != nil && controller.UsableBy(row, id.UserID) {
		return true
	}
	writeError(w, http.StatusForbidden, errorEnvelope{Error: errorBody{Code: codeAssistantNotYours, Message: controller.ErrAssistantNotYours.Error()}})
	return false
}

// assistantView renders a provider for the person asking: whose it is, and
// whether it is theirs to use.
func (s *Server) assistantView(r *http.Request, row *store.AssistantProvider) controller.AssistantProviderView {
	uid := ""
	if id := Identity(r.Context()); id != nil {
		uid = id.UserID
	}
	return s.ctrl.AssistantProviderViewFor(r.Context(), row, uid)
}

func (s *Server) handleListAssistantProviders(w http.ResponseWriter, r *http.Request) {
	rows, err := s.ctrl.Store().ListAssistantProviders(r.Context())
	if err != nil {
		s.internal(w, r, "listing the assistant's providers", err)
		return
	}
	items := make([]controller.AssistantProviderView, 0, len(rows))
	for _, row := range rows {
		if !strings.Contains(r.URL.Path, "/assistant/personal/") && row.OwnerID != "" && !assistant.Subscription(assistant.Kind(row.Kind)) {
			continue
		}
		if !s.cfg().Security.DisableAuth && strings.Contains(r.URL.Path, "/assistant/personal/") && row.OwnerID != s.assistantOwner(r) {
			continue
		}
		items = append(items, s.assistantView(r, row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleAssistantProviderKinds(w http.ResponseWriter, r *http.Request) {
	kinds := make([]map[string]string, 0, len(assistant.Kinds))
	for _, k := range assistant.Kinds {
		if assistant.Subscription(k) && !s.isAssistantAdmin(r) {
			continue
		}
		kinds = append(kinds, map[string]string{"kind": string(k), "default_base_url": assistant.DefaultBaseURL(k)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": kinds})
}

func (s *Server) handleGetAssistantProvider(w http.ResponseWriter, r *http.Request) {
	row, err := s.ownedAssistantProvider(r, chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the assistant provider", err)
		return
	}
	writeJSON(w, http.StatusOK, s.assistantView(r, row))
}

func (s *Server) handleCreateAssistantProvider(w http.ResponseWriter, r *http.Request) {
	var in assistantProviderInput
	if !decode(w, r, &in) {
		return
	}
	p := &store.AssistantProvider{Enabled: true, OwnerID: s.assistantOwner(r)}
	in.apply(p)
	if assistant.Subscription(assistant.Kind(p.Kind)) && !s.isAssistantAdmin(r) {
		forbidden(w, "Subscription tools on the controller require an administrator; use your personal API provider instead.")
		return
	}
	if errs := s.validateAssistantProvider(r, p, true, in.APIKey); len(errs) > 0 {
		unprocessable(w, "this provider cannot be created as described", errs)
		return
	}
	if assistant.Subscription(assistant.Kind(p.Kind)) {
		// It belongs to the person who adds it. A caller that is not a person, or a
		// token that has no owner, has nobody for it to belong to.
		id := Identity(r.Context())
		if id == nil || id.UserID == "" {
			forbidden(w, "a subscription belongs to a person: add it signed in as yourself, and not with a token that has no owner")
			return
		}
		p.OwnerID = id.UserID
	}
	if err := s.ctrl.Store().CreateAssistantProvider(r.Context(), p); err != nil {
		s.fail(w, r, "creating the assistant provider", err)
		return
	}
	if in.APIKey != nil && strings.TrimSpace(*in.APIKey) != "" {
		if !s.sealAssistantKey(w, r, p.ID, *in.APIKey) {
			return
		}
	}
	fresh, err := s.ctrl.Store().GetAssistantProvider(r.Context(), p.ID)
	if err != nil {
		s.internal(w, r, "reading the assistant provider back", err)
		return
	}
	view := s.ctrl.AssistantProviderView(fresh)
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "assistant.provider.create", "assistant_provider", p.ID, view)
	writeJSON(w, http.StatusCreated, s.assistantView(r, fresh))
}

func (s *Server) handleUpdateAssistantProvider(w http.ResponseWriter, r *http.Request) {
	row, err := s.ownedAssistantProvider(r, chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the assistant provider", err)
		return
	}
	if !s.ownerOnly(w, r, row) {
		return
	}
	var in assistantProviderInput
	if !decode(w, r, &in) {
		return
	}
	before := s.ctrl.AssistantProviderView(row)
	in.apply(row)
	if errs := s.validateAssistantProvider(r, row, in.BaseURL != nil, in.APIKey); len(errs) > 0 {
		unprocessable(w, "this provider cannot be changed as described", errs)
		return
	}
	if err := s.ctrl.Store().UpdateAssistantProvider(r.Context(), row); err != nil {
		s.fail(w, r, "saving the assistant provider", err)
		return
	}
	if in.APIKey != nil && strings.TrimSpace(*in.APIKey) != "" {
		if !s.sealAssistantKey(w, r, row.ID, *in.APIKey) {
			return
		}
	}
	fresh, err := s.ctrl.Store().GetAssistantProvider(r.Context(), row.ID)
	if err != nil {
		s.internal(w, r, "reading the assistant provider back", err)
		return
	}
	view := s.ctrl.AssistantProviderView(fresh)
	s.auth.Auditor().Updated(r.Context(), Identity(r.Context()), "assistant_provider", row.ID, before, view)
	if before.FleetAccess != view.FleetAccess {
		// Its own row, because it is the one setting that decides whether a
		// stranger's model is shown this fleet, and it should be findable as that.
		s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "assistant.provider.fleet_access", "assistant_provider", row.ID,
			map[string]any{"name": view.Name, "fleet_access": view.FleetAccess, "local": view.Local})
	}
	writeJSON(w, http.StatusOK, s.assistantView(r, fresh))
}

// sealAssistantKey seals a key with the instance key and writes it through
// the row's own setter, auditing that a key changed and never what it is.
func (s *Server) sealAssistantKey(w http.ResponseWriter, r *http.Request, id, key string) bool {
	sealed, err := s.key.SealString(strings.TrimSpace(key))
	if err != nil {
		s.internal(w, r, "sealing the provider's key", err)
		return false
	}
	if err := s.ctrl.Store().SetAssistantProviderKey(r.Context(), id, sealed); err != nil {
		s.fail(w, r, "saving the provider's key", err)
		return false
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "assistant.provider.key", "assistant_provider", id, nil)
	return true
}

// deleteAssistantProviderRequest carries the name typed to confirm: a
// delete that could be sent by a stray click on the wrong card is not one.
type deleteAssistantProviderRequest struct {
	Name string `json:"name"`
}

func (s *Server) handleDeleteAssistantProvider(w http.ResponseWriter, r *http.Request) {
	row, err := s.ownedAssistantProvider(r, chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the assistant provider", err)
		return
	}
	var in deleteAssistantProviderRequest
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) != row.Name {
		conflict(w, fmt.Sprintf("the name typed does not match %q, so the provider was not removed; type its name exactly to confirm", row.Name))
		return
	}
	if err := s.ctrl.Store().DeleteAssistantProvider(r.Context(), row.ID); err != nil {
		s.fail(w, r, "removing the assistant provider", err)
		return
	}
	s.auth.Auditor().Deleted(r.Context(), Identity(r.Context()), "assistant_provider", row.ID, s.ctrl.AssistantProviderView(row))
	noContent(w)
}

func (s *Server) handleCheckAssistantProvider(w http.ResponseWriter, r *http.Request) {
	row, err := s.ownedAssistantProvider(r, chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the assistant provider", err)
		return
	}
	if !s.ownerOnly(w, r, row) {
		return
	}
	check := s.ctrl.CheckAssistantProvider(r.Context(), row)
	if err := s.ctrl.RecordAssistantProviderCheck(r.Context(), row.ID, check); err != nil {
		s.internal(w, r, "recording the check", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "assistant.provider.check", "assistant_provider", row.ID,
		map[string]any{"name": row.Name, "ok": check.OK, "model": check.Model})
	writeJSON(w, http.StatusOK, check)
}

// handleCheckAssistantDraft checks a provider as the form has it, with the
// key the form holds, so a key need not be saved to be tested. Nothing is
// written and nothing is audited as a change; the check is still the
// administrator's and is audited as one.
func (s *Server) handleCheckAssistantDraft(w http.ResponseWriter, r *http.Request) {
	var in assistantProviderInput
	if !decode(w, r, &in) {
		return
	}
	draft := &store.AssistantProvider{Enabled: true}
	if in.ID != nil && *in.ID != "" {
		// The Edit dialog's Test: the draft is the saved row with the form's
		// fields over it, so a blank key box means the key the row holds.
		row, err := s.ownedAssistantProvider(r, *in.ID)
		if err != nil {
			s.fail(w, r, "reading the assistant provider", err)
			return
		}
		if !s.ownerOnly(w, r, row) {
			return
		}
		draft = row
	}
	in.apply(draft)
	if draft.Name == "" {
		draft.Name = "draft"
	}
	if errs := s.validateAssistantProvider(r, draft, in.BaseURL != nil, in.APIKey); len(errs) > 0 {
		unprocessable(w, "this provider cannot be checked as described", errs)
		return
	}
	key := ""
	if in.APIKey != nil {
		key = *in.APIKey
	}
	check := s.ctrl.CheckAssistantDraft(r.Context(), draft, key)
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "assistant.provider.check", "assistant_provider", "",
		map[string]any{"draft": true, "kind": draft.Kind, "ok": check.OK, "model": check.Model})
	writeJSON(w, http.StatusOK, check)
}

// handleAssistantModels lists the models a provider, as the form has it, says it
// serves. It spends the form's key the way a draft check does, so it is the same
// role, and writes nothing. A provider that would not answer is a 502 with the
// adapter's words, which never carry the request or the key.
func (s *Server) handleAssistantModels(w http.ResponseWriter, r *http.Request) {
	var in assistantProviderInput
	if !decode(w, r, &in) {
		return
	}
	draft := &store.AssistantProvider{Enabled: true}
	if in.ID != nil && *in.ID != "" {
		row, err := s.ownedAssistantProvider(r, *in.ID)
		if err != nil {
			s.fail(w, r, "reading the assistant provider", err)
			return
		}
		if !s.ownerOnly(w, r, row) {
			return
		}
		draft = row
	}
	in.apply(draft)
	if draft.Name == "" {
		draft.Name = "draft"
	}
	if draft.Model == "" {
		// The list is how a model is chosen, so asking for it before one is
		// chosen must not be refused for want of one.
		draft.Model = "unset"
	}
	if errs := s.validateAssistantProvider(r, draft, in.BaseURL != nil, in.APIKey); len(errs) > 0 {
		unprocessable(w, "the models of this provider cannot be listed as described", errs)
		return
	}
	key := ""
	if in.APIKey != nil {
		key = *in.APIKey
	}
	models, err := s.ctrl.ListAssistantModels(r.Context(), draft, key)
	switch {
	case errors.Is(err, controller.ErrAssistantCannotList):
		conflict(w, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusBadGateway, errorEnvelope{Error: errorBody{Code: codeAssistantProviderFailed, Message: err.Error()}})
		return
	}
	if models == nil {
		models = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": models})
}

func (s *Server) handleDefaultAssistantProvider(w http.ResponseWriter, r *http.Request) {
	row, err := s.ownedAssistantProvider(r, chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the assistant provider", err)
		return
	}
	if !s.ownerOnly(w, r, row) {
		return
	}
	if err := s.ctrl.Store().SetDefaultAssistantProvider(r.Context(), row.ID); err != nil {
		s.fail(w, r, "choosing the default provider", err)
		return
	}
	fresh, _ := s.ctrl.Store().GetAssistantProvider(r.Context(), row.ID)
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "assistant.provider.default", "assistant_provider", row.ID, map[string]any{"name": row.Name})
	writeJSON(w, http.StatusOK, s.assistantView(r, fresh))
}

// A personal provider's scope comes from authentication and routing, never from its JSON body.
func (s *Server) assistantOwner(r *http.Request) string {
	if s.cfg().Security.DisableAuth {
		return ""
	}
	if strings.Contains(r.URL.Path, "/assistant/personal/") {
		return Identity(r.Context()).UserID
	}
	return ""
}
func (s *Server) requirePersonalAssistantOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if Identity(r.Context()).UserID == "" && !s.cfg().Security.DisableAuth {
			writeError(w, http.StatusForbidden, errorEnvelope{Error: errorBody{Code: codeForbidden, Message: "Personal Eli providers require a credential owned by a user."}})
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) ownedAssistantProvider(r *http.Request, id string) (*store.AssistantProvider, error) {
	row, err := s.ctrl.Store().GetAssistantProvider(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if !s.cfg().Security.DisableAuth && strings.Contains(r.URL.Path, "/assistant/personal/") && row.OwnerID != s.assistantOwner(r) {
		return nil, store.ErrNotFound
	}
	if !strings.Contains(r.URL.Path, "/assistant/personal/") && row.OwnerID != "" && !assistant.Subscription(assistant.Kind(row.Kind)) {
		return nil, store.ErrNotFound
	}
	return row, nil
}

func (s *Server) isAssistantAdmin(r *http.Request) bool {
	id := Identity(r.Context())
	return s.cfg().Security.DisableAuth || id != nil && id.Role == "admin"
}
