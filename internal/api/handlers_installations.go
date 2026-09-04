package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// installationResponse is a GitHub App installation as the API returns it.
//
// The private key and the webhook secret are absent, and there is no field they
// could be put in: the store keeps them sealed and tagged `json:"-"`, and this
// type names every field explicitly so that adding one to the domain model
// cannot leak it here by accident.
type installationResponse struct {
	ID             string           `json:"id"`
	AppID          int64            `json:"app_id"`
	InstallationID int64            `json:"installation_id"`
	Target         string           `json:"target"`
	TargetType     store.TargetType `json:"target_type"`
	APIBaseURL     string           `json:"api_base_url"`
	AppSlug        string           `json:"app_slug,omitempty"`
	WebURL         string           `json:"web_url,omitempty"`
	Enterprise     bool             `json:"enterprise"`
	Healthy        bool             `json:"healthy"`
	LastError      string           `json:"last_error,omitempty"`
	LastCheckedAt  *time.Time       `json:"last_checked_at"`
	PoolCount      int              `json:"pool_count"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

func installationResponseOf(i *store.Installation, pools int) installationResponse {
	return installationResponse{
		ID:             i.ID,
		AppID:          i.AppID,
		InstallationID: i.InstallationID,
		Target:         i.Target,
		TargetType:     i.TargetType,
		APIBaseURL:     i.APIBaseURL,
		AppSlug:        i.AppSlug,
		WebURL:         github.WebURLForAPI(i.APIBaseURL),
		Enterprise:     github.IsEnterprise(i.APIBaseURL),
		Healthy:        i.Healthy(),
		LastError:      i.LastError,
		LastCheckedAt:  i.LastCheckedAt,
		PoolCount:      pools,
		CreatedAt:      i.CreatedAt,
		UpdatedAt:      i.UpdatedAt,
	}
}

// poolCountsByInstallation answers "how much depends on this installation?",
// which is what makes the delete confirmation honest.
func (s *Server) poolCountsByInstallation(ctx context.Context) (map[string]int, error) {
	pools, err := s.ctrl.Store().ListPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing pools: %w", err)
	}
	out := map[string]int{}
	for _, p := range pools {
		out[p.InstallationID]++
	}
	return out, nil
}

// handleListInstallations answers GET /api/v1/installations.
func (s *Server) handleListInstallations(w http.ResponseWriter, r *http.Request) {
	insts, err := s.ctrl.Store().ListInstallations(r.Context())
	if err != nil {
		s.internal(w, r, "listing installations", err)
		return
	}
	counts, err := s.poolCountsByInstallation(r.Context())
	if err != nil {
		s.internal(w, r, "listing installations", err)
		return
	}
	out := make([]installationResponse, 0, len(insts))
	for _, i := range insts {
		out = append(out, installationResponseOf(i, counts[i.ID]))
	}
	writeJSON(w, http.StatusOK, newList(out))
}

// handleGetInstallation answers GET /api/v1/installations/{id}.
func (s *Server) handleGetInstallation(w http.ResponseWriter, r *http.Request) {
	i, err := s.ctrl.Store().GetInstallation(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the installation", err)
		return
	}
	counts, err := s.poolCountsByInstallation(r.Context())
	if err != nil {
		s.internal(w, r, "reading the installation", err)
		return
	}
	writeJSON(w, http.StatusOK, installationResponseOf(i, counts[i.ID]))
}

type installationCreateRequest struct {
	AppID          int64  `json:"app_id"`
	InstallationID int64  `json:"installation_id"`
	Target         string `json:"target"`
	TargetType     string `json:"target_type"`
	APIBaseURL     string `json:"api_base_url"`
	PrivateKey     string `json:"private_key"`
	WebhookSecret  string `json:"webhook_secret"`
}

// handleCreateInstallation connects a GitHub App installation.
//
// The private key is sealed with the instance key before it reaches the
// database and is never returned by anything. A request may leave private_key
// empty when the App was just created through the manifest flow: the
// credentials from that exchange are held sealed, and this is the call that
// completes it once the operator has installed the App and GitHub has told them
// the installation ID.
func (s *Server) handleCreateInstallation(w http.ResponseWriter, r *http.Request) {
	var req installationCreateRequest
	if !decode(w, r, &req) {
		return
	}
	if s.key == nil {
		s.noEncryptionKey(w)
		return
	}

	pending := s.manifests.credentialsFor(req.AppID)
	privateKey := strings.TrimSpace(req.PrivateKey)
	webhookSecret := req.WebhookSecret
	if privateKey == "" && pending != nil {
		privateKey, webhookSecret = pending.pem, pending.webhookSecret
	}

	target := strings.TrimSpace(req.Target)
	targetType := store.TargetType(strings.ToLower(strings.TrimSpace(req.TargetType)))
	apiBase := strings.TrimSpace(req.APIBaseURL)
	// A browser that finished the manifest flow in the tab GitHub redirected
	// back to never saw the form the flow started from, so it may not know the
	// target. The pending handshake does; it is the same App either way.
	if pending != nil {
		if target == "" {
			target = pending.target
		}
		if targetType == "" {
			targetType = pending.targetType
		}
		if apiBase == "" {
			apiBase = pending.apiBaseURL
		}
	}
	if apiBase == "" {
		apiBase = s.cfg.GitHub.APIBaseURL
	}

	var fields []fieldError
	if req.AppID <= 0 {
		fields = append(fields, fieldError{"app_id", "the App ID is on the GitHub App's settings page, next to its name"})
	}
	if req.InstallationID <= 0 {
		fields = append(fields, fieldError{"installation_id", "the installation ID is the number at the end of the App's install URL, e.g. .../installations/12345678"})
	}
	if target == "" {
		fields = append(fields, fieldError{"target", "name the organisation, or the owner/repo, whose runners this App manages"})
	}
	if targetType == "" {
		// Derive it rather than refuse: "acme/widgets" can only be a repo.
		if strings.Contains(target, "/") {
			targetType = store.TargetRepo
		} else {
			targetType = store.TargetOrg
		}
	}
	if !targetType.Valid() {
		fields = append(fields, fieldError{"target_type", fmt.Sprintf("%q is not a target type; use org or repo", targetType)})
	}
	if privateKey == "" {
		fields = append(fields, fieldError{"private_key", "paste the App's PEM private key; GitHub shows it once, when you generate it"})
	} else if !strings.Contains(privateKey, "PRIVATE KEY") {
		fields = append(fields, fieldError{"private_key", "that does not look like a PEM private key; it should start with -----BEGIN RSA PRIVATE KEY-----"})
	}
	normalised, err := github.NormalizeAPIBaseURL(apiBase)
	if err != nil {
		fields = append(fields, fieldError{"api_base_url", err.Error()})
	}
	if len(fields) > 0 {
		unprocessable(w, "this installation could not be connected", fields)
		return
	}

	sealedKey, err := s.key.SealString(privateKey)
	if err != nil {
		s.internal(w, r, "sealing the App's private key", err)
		return
	}
	sealedSecret, err := s.key.SealString(webhookSecret)
	if err != nil {
		s.internal(w, r, "sealing the webhook secret", err)
		return
	}

	inst := &store.Installation{
		AppID:            req.AppID,
		InstallationID:   req.InstallationID,
		Target:           target,
		TargetType:       targetType,
		APIBaseURL:       normalised,
		PrivateKeyEnc:    sealedKey,
		WebhookSecretEnc: sealedSecret,
	}
	if pending != nil {
		inst.AppSlug = pending.slug
	}
	if err := s.ctrl.Store().CreateInstallation(r.Context(), inst); err != nil {
		s.fail(w, r, "recording the installation", err)
		return
	}
	s.manifests.forget(req.AppID)

	s.auth.Auditor().Created(r.Context(), Identity(r.Context()), "installation", inst.ID, inst)
	// A fresh installation may already own queued jobs.
	s.ctrl.Nudge()
	writeJSON(w, http.StatusCreated, installationResponseOf(inst, 0))
}

type installationUpdateRequest struct {
	Target        *string `json:"target"`
	APIBaseURL    *string `json:"api_base_url"`
	PrivateKey    *string `json:"private_key"`
	WebhookSecret *string `json:"webhook_secret"`
}

// handleUpdateInstallation answers PATCH /api/v1/installations/{id}.
func (s *Server) handleUpdateInstallation(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	inst, err := s.ctrl.Store().GetInstallation(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the installation", err)
		return
	}

	var req installationUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	before := *inst

	var fields []fieldError
	if req.Target != nil {
		t := strings.TrimSpace(*req.Target)
		if t == "" {
			fields = append(fields, fieldError{"target", "an installation has to name the organisation or repository it manages"})
		} else {
			inst.Target = t
		}
	}
	if req.APIBaseURL != nil {
		normalised, nerr := github.NormalizeAPIBaseURL(strings.TrimSpace(*req.APIBaseURL))
		if nerr != nil {
			fields = append(fields, fieldError{"api_base_url", nerr.Error()})
		} else {
			inst.APIBaseURL = normalised
		}
	}
	if (req.PrivateKey != nil || req.WebhookSecret != nil) && s.key == nil {
		s.noEncryptionKey(w)
		return
	}
	if req.PrivateKey != nil && strings.TrimSpace(*req.PrivateKey) != "" {
		if !strings.Contains(*req.PrivateKey, "PRIVATE KEY") {
			fields = append(fields, fieldError{"private_key", "that does not look like a PEM private key; it should start with -----BEGIN RSA PRIVATE KEY-----"})
		} else {
			sealed, serr := s.key.SealString(strings.TrimSpace(*req.PrivateKey))
			if serr != nil {
				s.internal(w, r, "sealing the App's private key", serr)
				return
			}
			inst.PrivateKeyEnc = sealed
		}
	}
	if req.WebhookSecret != nil {
		sealed, serr := s.key.SealString(*req.WebhookSecret)
		if serr != nil {
			s.internal(w, r, "sealing the webhook secret", serr)
			return
		}
		inst.WebhookSecretEnc = sealed
	}
	if len(fields) > 0 {
		unprocessable(w, "this installation could not be changed", fields)
		return
	}

	if err := s.ctrl.Store().UpdateInstallation(r.Context(), inst); err != nil {
		s.fail(w, r, "saving the installation", err)
		return
	}
	// The cached client holds the old credentials, so it has to go or the next
	// call would still use them.
	s.ctrl.Forget(id)
	s.auth.Auditor().Updated(r.Context(), Identity(r.Context()), "installation", id, &before, inst)

	counts, cerr := s.poolCountsByInstallation(r.Context())
	if cerr != nil {
		s.internal(w, r, "reading the installation back", cerr)
		return
	}
	writeJSON(w, http.StatusOK, installationResponseOf(inst, counts[id]))
}

type deleteInstallationResponse struct {
	PoolsDeleted    int `json:"pools_deleted"`
	RunnersAffected int `json:"runners_affected"`
}

// handleDeleteInstallation removes an installation and everything that depended
// on it, and says how much that was.
func (s *Server) handleDeleteInstallation(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	inst, err := s.ctrl.Store().GetInstallation(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the installation", err)
		return
	}

	pools, err := s.ctrl.Store().ListPools(r.Context())
	if err != nil {
		s.internal(w, r, "listing the installation's pools", err)
		return
	}
	deleted, affected := 0, 0
	for _, p := range pools {
		if p.InstallationID != id {
			continue
		}
		deleted++
		runners, rerr := s.ctrl.Store().ListRunnersForPool(r.Context(), p.ID)
		if rerr != nil {
			s.internal(w, r, "listing a pool's runners", rerr)
			return
		}
		for _, run := range runners {
			if run.State.Terminal() {
				continue
			}
			// Drained rather than killed: removing an installation is an
			// administrative act, not a reason to interrupt somebody's build.
			if _, derr := s.ctrl.DrainRunner(r.Context(), run.ID, "installation "+inst.Target+" was removed"); derr != nil {
				s.logger(r).Warn("could not drain a runner while removing its installation",
					"installation", id, "runner", run.ID, "error", derr)
				continue
			}
			affected++
		}
	}

	if err := s.ctrl.Store().DeleteInstallation(r.Context(), id); err != nil {
		s.fail(w, r, "deleting the installation", err)
		return
	}
	s.ctrl.Forget(id)
	s.auth.Auditor().Deleted(r.Context(), Identity(r.Context()), "installation", id, inst)
	s.ctrl.Nudge()
	writeJSON(w, http.StatusOK, deleteInstallationResponse{PoolsDeleted: deleted, RunnersAffected: affected})
}

// installationHealthResponse is what a credential probe found.
type installationHealthResponse struct {
	OK                 bool              `json:"ok"`
	AppSlug            string            `json:"app_slug,omitempty"`
	AppName            string            `json:"app_name,omitempty"`
	Message            string            `json:"message"`
	Permissions        map[string]string `json:"permissions,omitempty"`
	Events             []string          `json:"events,omitempty"`
	MissingPermissions []string          `json:"missing_permissions,omitempty"`
	MissingEvents      []string          `json:"missing_events,omitempty"`
	RateLimitRemaining int               `json:"rate_limit_remaining,omitempty"`
}

// handleVerifyInstallation probes an installation's credentials end to end.
//
// On failure the message is GitHub's own complaint, translated by the github
// package into the permission that is probably missing, because "403" tells an
// operator nothing they can act on.
func (s *Server) handleVerifyInstallation(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	inst, err := s.ctrl.Store().GetInstallation(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the installation", err)
		return
	}

	info, perr := s.ctrl.ProbeInstallation(r.Context(), id)
	out := installationHealthResponse{}
	if perr != nil {
		out.OK = false
		out.Message = perr.Error()
	} else {
		out.OK = true
		out.AppSlug, out.AppName = info.Slug, info.Name
		out.Permissions, out.Events = info.Permissions, info.Events
		out.MissingPermissions = info.MissingRequirements(inst.TargetType)
		out.MissingEvents = missingEvents(info.Events)
		switch {
		case len(out.MissingPermissions) > 0:
			out.OK = false
			out.Message = "the App is reachable but is missing: " + strings.Join(out.MissingPermissions, "; ")
		case len(out.MissingEvents) > 0:
			out.Message = fmt.Sprintf("the credentials work, but the App is not subscribed to %s, so scaling will run on the fallback poller",
				strings.Join(out.MissingEvents, ", "))
		default:
			out.Message = fmt.Sprintf("the credentials work and %s has everything Zoomies needs", inst.Target)
		}
		if rl, rerr := s.rateLimitFor(r.Context(), id); rerr == nil && rl != nil {
			out.RateLimitRemaining = rl.Remaining
		}
	}

	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "installation.verify", "installation", id, map[string]any{
		"target": inst.Target, "ok": out.OK, "message": out.Message,
	})
	writeJSON(w, http.StatusOK, out)
}

// missingEvents reports the webhook events Zoomies needs and the App is not
// subscribed to. Only workflow_job matters: it is the only event acted on.
func missingEvents(events []string) []string {
	for _, e := range events {
		if strings.EqualFold(e, "workflow_job") {
			return nil
		}
	}
	return []string{"workflow_job"}
}

// handleRunnerGroups lists the target's runner groups, for the pool wizard.
func (s *Server) handleRunnerGroups(w http.ResponseWriter, r *http.Request) {
	client, err := s.ctrl.ClientFor(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.githubFail(w, r, "reading the installation's runner groups", err)
		return
	}
	groups, err := client.ListRunnerGroups(r.Context())
	if err != nil {
		s.githubFail(w, r, "listing runner groups", err)
		return
	}
	type groupResponse struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	out := make([]groupResponse, 0, len(groups))
	for _, g := range groups {
		out = append(out, groupResponse{ID: g.ID, Name: g.Name})
	}
	writeJSON(w, http.StatusOK, newList(out))
}

type rateLimitResponse struct {
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"reset_at"`
}

func (s *Server) rateLimitFor(ctx context.Context, id string) (*github.RateLimit, error) {
	client, err := s.ctrl.ClientFor(ctx, id)
	if err != nil {
		return nil, err
	}
	return client.RateLimit(ctx)
}

// handleRateLimit reports the installation's remaining GitHub quota.
func (s *Server) handleRateLimit(w http.ResponseWriter, r *http.Request) {
	rl, err := s.rateLimitFor(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.githubFail(w, r, "reading the GitHub rate limit", err)
		return
	}
	writeJSON(w, http.StatusOK, rateLimitResponse{Limit: rl.Limit, Remaining: rl.Remaining, ResetAt: rl.ResetAt})
}

// githubFail maps a GitHub failure onto a status code without hiding what
// GitHub said, since that is usually the actionable half.
func (s *Server) githubFail(w http.ResponseWriter, r *http.Request, doing string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, github.ErrNotFound):
		notFound(w, err.Error())
	case errors.Is(err, github.ErrForbidden):
		writeError(w, http.StatusBadGateway, errorEnvelope{Error: errorBody{
			Code:    codeInternal,
			Message: "GitHub refused this request, which usually means the App is missing a permission: " + err.Error(),
		}})
	case errors.Is(err, github.ErrRateLimited):
		rateLimited(w, "this installation has used up its GitHub API quota; the counters reset within the hour", 0)
	default:
		s.internal(w, r, doing, err)
	}
}

func (s *Server) noEncryptionKey(w http.ResponseWriter) {
	msg := "this controller has no usable encryption key, so it cannot seal a GitHub App private key. " +
		"Set security.encryption_key_file (or ZOOMIES_ENCRYPTION_KEY) and restart."
	if s.keyErr != nil {
		msg += " The key it tried to load reported: " + s.keyErr.Error()
	}
	writeError(w, http.StatusInternalServerError, errorEnvelope{Error: errorBody{Code: codeInternal, Message: msg}})
}

// ---------------------------------------------------------------------------
// The App manifest flow
// ---------------------------------------------------------------------------

// manifestTTL is how long a manifest handshake may take. GitHub's own code is
// valid for an hour; the state that ties the exchange back to what the operator
// asked for does not need to outlive the browser tab it was opened in.
const manifestTTL = time.Hour

// pendingApp is what the manifest flow remembers between the two calls.
//
// It is held in memory rather than in the database because it is a credential
// in flight: the App exists but has not been installed anywhere yet, so there
// is no installation row to hang it on, and a controller restart in the middle
// of the handshake should leave nothing behind. The operator simply starts the
// flow again.
type pendingApp struct {
	state         string
	target        string
	targetType    store.TargetType
	apiBaseURL    string
	webhookSecret string
	appID         int64
	slug          string
	pem           string
	createdAt     time.Time
}

type manifestStates struct {
	mu    sync.Mutex
	now   func() time.Time
	items map[string]*pendingApp
}

func newManifestStates(now func() time.Time) *manifestStates {
	if now == nil {
		now = time.Now
	}
	return &manifestStates{now: now, items: map[string]*pendingApp{}}
}

func (m *manifestStates) put(p *pendingApp) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked()
	m.items[p.state] = p
}

func (m *manifestStates) take(state string) *pendingApp {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked()
	p := m.items[state]
	if p != nil {
		// Single use: the state ties one browser's exchange to one manifest,
		// and reusing it is either a replay or a mistake.
		delete(m.items, state)
	}
	return p
}

// credentialsFor finds the credentials of an App created in this process that
// has not been recorded as an installation yet.
func (m *manifestStates) credentialsFor(appID int64) *pendingApp {
	if appID == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked()
	for _, p := range m.items {
		if p.appID == appID && p.pem != "" {
			return p
		}
	}
	return nil
}

func (m *manifestStates) forget(appID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, p := range m.items {
		if p.appID == appID {
			delete(m.items, k)
		}
	}
}

func (m *manifestStates) sweepLocked() {
	cutoff := m.now().Add(-manifestTTL)
	for k, p := range m.items {
		if p.createdAt.Before(cutoff) {
			delete(m.items, k)
		}
	}
}

type manifestRequest struct {
	Name       string `json:"name"`
	Target     string `json:"target"`
	TargetType string `json:"target_type"`
	APIBaseURL string `json:"api_base_url"`
}

type manifestResponse struct {
	PostURL  string `json:"post_url"`
	Manifest string `json:"manifest"`
	State    string `json:"state"`
}

// handleCreateManifest builds the GitHub App manifest the browser posts.
//
// The manifest asks for exactly the permissions Zoomies needs. It cannot carry
// a webhook secret -- GitHub rejects a manifest that names one -- so the secret
// is GitHub's, and it arrives with the rest of the credentials when the code is
// exchanged.
func (s *Server) handleCreateManifest(w http.ResponseWriter, r *http.Request) {
	var req manifestRequest
	if !decode(w, r, &req) {
		return
	}

	target := strings.TrimSpace(req.Target)
	targetType := store.TargetType(strings.ToLower(strings.TrimSpace(req.TargetType)))
	if targetType == "" && target != "" {
		if strings.Contains(target, "/") {
			targetType = store.TargetRepo
		} else {
			targetType = store.TargetOrg
		}
	}

	var fields []fieldError
	if target == "" {
		fields = append(fields, fieldError{"target", "name the organisation, or the owner/repo, this App will manage runners for"})
	}
	if !targetType.Valid() {
		fields = append(fields, fieldError{"target_type", fmt.Sprintf("%q is not a target type; use org or repo", targetType)})
	}
	if s.cfg.Server.ExternalURL == "" {
		fields = append(fields, fieldError{"target", "server.external_url is not set, so Zoomies cannot tell GitHub where to deliver webhooks; set it and restart before creating the App"})
	}
	if len(fields) > 0 {
		unprocessable(w, "the App manifest could not be built", fields)
		return
	}

	apiBase := strings.TrimSpace(req.APIBaseURL)
	if apiBase == "" {
		apiBase = s.cfg.GitHub.APIBaseURL
	}
	normalised, err := github.NormalizeAPIBaseURL(apiBase)
	if err != nil {
		unprocessable(w, "the App manifest could not be built", []fieldError{{"api_base_url", err.Error()}})
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Zoomies (" + strings.ReplaceAll(target, "/", "-") + ")"
	}
	org := ""
	if targetType == store.TargetOrg {
		org = target
	}

	manifest, err := github.Manifest(github.ManifestOptions{
		Name:         name,
		URL:          s.cfg.Server.ExternalURL,
		WebhookURL:   s.cfg.WebhookURL(),
		Organization: org,
		SetupURL:     s.cfg.Server.ExternalURL + "/settings/github/setup",
	})
	if err != nil {
		unprocessable(w, err.Error(), []fieldError{{"name", err.Error()}})
		return
	}

	state := store.NewSecret(16)
	s.manifests.put(&pendingApp{
		state:      state,
		target:     target,
		targetType: targetType,
		apiBaseURL: normalised,
		createdAt:  s.ctrl.Now(),
	})

	writeJSON(w, http.StatusOK, manifestResponse{
		PostURL:  github.ManifestURL(normalised, org),
		Manifest: string(manifest),
		State:    state,
	})
}

type exchangeRequest struct {
	Code       string `json:"code"`
	State      string `json:"state"`
	APIBaseURL string `json:"api_base_url"`
}

type exchangeResponse struct {
	AppID      int64  `json:"app_id"`
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	HTMLURL    string `json:"html_url"`
	InstallURL string `json:"install_url"`
	// SettingsURL is the App's own settings page. It is returned because the
	// one thing a manifest cannot do is set the App's logo: GitHub takes an
	// avatar as an upload and has no manifest field for it, so an App created
	// this way starts out anonymous unless the operator is sent to the page
	// that fixes it.
	SettingsURL string `json:"settings_url"`
	// Target and TargetType are echoed back because GitHub returns the
	// operator to a fresh tab, which knows nothing about the form the flow
	// started from. Without them the last step has no target to record.
	Target     string `json:"target"`
	TargetType string `json:"target_type"`
}

// handleExchangeManifest turns the code GitHub redirected back with into App
// credentials.
//
// The credentials are kept sealed in memory rather than returned: the operator
// still has to install the App before there is an installation ID to record,
// and POST /installations completes the flow by naming that ID. Nothing here
// puts a private key in a response.
func (s *Server) handleExchangeManifest(w http.ResponseWriter, r *http.Request) {
	var req exchangeRequest
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Code) == "" {
		unprocessable(w, "GitHub did not send a code back, so there is nothing to exchange", []fieldError{
			{"code", "start the App creation again from the Installations page"},
		})
		return
	}

	pending := s.manifests.take(strings.TrimSpace(req.State))
	apiBase := strings.TrimSpace(req.APIBaseURL)
	if apiBase == "" && pending != nil {
		apiBase = pending.apiBaseURL
	}
	if apiBase == "" {
		apiBase = s.cfg.GitHub.APIBaseURL
	}

	creds, err := github.ExchangeManifestCode(r.Context(), apiBase, req.Code)
	if err != nil {
		unprocessable(w, err.Error(), []fieldError{{"code", err.Error()}})
		return
	}

	if pending == nil {
		// The state expired or came from another process. The App exists and
		// the operator can still finish by hand, so say exactly that rather
		// than losing the credentials silently.
		unprocessable(w,
			"the App was created, but this controller no longer has the setup state that goes with it (it expires after an hour, and does not survive a restart). "+
				"Open the App's settings on GitHub, generate a private key, and add the installation by hand on the Installations page.",
			[]fieldError{{"state", "start the App creation again, or connect the App manually"}})
		return
	}

	pending.appID, pending.slug, pending.pem = creds.AppID, creds.Slug, creds.PEM
	// The manifest cannot ask for a particular secret, so the one GitHub
	// generated is the one its deliveries are signed with.
	pending.webhookSecret = creds.WebhookSecret
	pending.state = store.NewSecret(16)
	pending.createdAt = s.ctrl.Now()
	s.manifests.put(pending)

	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "installation.app_created", "installation", "", map[string]any{
		"app_id": creds.AppID, "slug": creds.Slug, "target": pending.target,
	})

	// The settings page lives under the organisation for an org App and under
	// the operator's own account for a repo App, and GitHub 404s the wrong one.
	settingsOrg := ""
	if pending.targetType == store.TargetOrg {
		settingsOrg = pending.target
	}
	writeJSON(w, http.StatusOK, exchangeResponse{
		AppID:       creds.AppID,
		Slug:        creds.Slug,
		Name:        creds.Name,
		HTMLURL:     creds.HTMLURL,
		InstallURL:  github.InstallURL(creds.HTMLURL),
		SettingsURL: github.SettingsURL(apiBase, creds.Slug, settingsOrg),
		Target:      pending.target,
		TargetType:  string(pending.targetType),
	})
}

// ---------------------------------------------------------------------------
// Webhook health
// ---------------------------------------------------------------------------

type webhookDeliveriesResponse struct {
	Items          []*store.WebhookDelivery `json:"items"`
	LastReceivedAt *time.Time               `json:"last_received_at"`
}

// handleWebhookDeliveries lists recent deliveries.
//
// last_received_at is null rather than absent when nothing has ever arrived,
// because "quiet" and "broken" look identical without it.
func (s *Server) handleWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	switch status {
	case "", "accepted", "rejected", "error":
	default:
		badRequestField(w, "status", fmt.Sprintf("%q is not a delivery status; use accepted, rejected or error", status))
		return
	}

	limit := clamp(queryInt(r, "limit", defaultLimit), 1, maxLimit)
	items, err := s.ctrl.Store().ListDeliveries(r.Context(), status, limit)
	if err != nil {
		s.internal(w, r, "listing webhook deliveries", err)
		return
	}
	last, err := s.ctrl.Store().LastDeliveryAt(r.Context())
	if err != nil {
		s.internal(w, r, "reading the last delivery time", err)
		return
	}
	if items == nil {
		items = []*store.WebhookDelivery{}
	}
	writeJSON(w, http.StatusOK, webhookDeliveriesResponse{Items: items, LastReceivedAt: timePtr(last)})
}

type webhookCheckResponse struct {
	Reachable        bool       `json:"reachable"`
	URL              string     `json:"url"`
	Message          string     `json:"message"`
	Fix              string     `json:"fix,omitempty"`
	PollingAvailable bool       `json:"polling_available"`
	LastDeliveryAt   *time.Time `json:"last_delivery_at"`
}

// webhookProbeTimeout bounds the self-probe. It is short because the answer to
// "can this be reached?" arrives quickly or not at all, and an operator is
// watching a spinner.
const webhookProbeTimeout = 5 * time.Second

// handleWebhookTest reports whether GitHub can reach this controller.
//
// A delivery that has actually arrived is proof and is used first. Failing
// that, this asks the configured external URL for the webhook path from here,
// which proves the name resolves and something answers -- not that GitHub can
// reach it -- and the message says so rather than overclaiming.
func (s *Server) handleWebhookTest(w http.ResponseWriter, r *http.Request) {
	out := webhookCheckResponse{
		URL:              s.cfg.WebhookURL(),
		PollingAvailable: s.cfg.GitHub.PollFallback,
	}
	last, err := s.ctrl.Store().LastDeliveryAt(r.Context())
	if err != nil {
		s.internal(w, r, "reading the last delivery time", err)
		return
	}
	out.LastDeliveryAt = timePtr(last)

	switch {
	case s.cfg.Server.ExternalURL == "":
		out.Message = "server.external_url is not set, so Zoomies cannot tell GitHub where to deliver webhooks and cannot test the address."
		out.Fix = "set server.external_url to the address GitHub should reach this controller on, then restart."
	case !last.IsZero():
		out.Reachable = true
		out.Message = fmt.Sprintf("GitHub last delivered a webhook at %s, so deliveries reach this controller.", last.Format(time.RFC3339))
	default:
		out.Reachable, out.Message, out.Fix = s.probeWebhookURL(r.Context(), out.URL)
	}

	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "webhook.test", "webhook", "", map[string]any{
		"url": out.URL, "reachable": out.Reachable, "message": out.Message,
	})
	writeJSON(w, http.StatusOK, out)
}

// probeWebhookURL asks the external URL for the webhook path and reports what
// it found, with the specific remedy when it found nothing.
func (s *Server) probeWebhookURL(ctx context.Context, url string) (bool, string, string) {
	ctx, cancel := context.WithTimeout(ctx, webhookProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false, "the webhook URL " + url + " cannot be requested: " + err.Error(),
			"check server.external_url and github.webhook_path."
	}
	req.Header.Set("User-Agent", "zoomies/"+version.Version+" (webhook reachability check)")

	client := &http.Client{Timeout: webhookProbeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return false,
			"nothing answered at " + url + ": " + err.Error(),
			"open the port on this host's firewall, put a reverse proxy in front of it, or use a tunnel. " +
				"Until then leave github.poll_fallback on: Zoomies will discover queued jobs by polling instead, within " +
				s.ctrl.Config().GitHub.PollInterval.String() + " rather than instantly."
	}
	defer resp.Body.Close()

	// Any HTTP response means something is listening at that address. The
	// webhook endpoint answers 405 to a HEAD, which is the expected result.
	return true,
		fmt.Sprintf("%s answered (HTTP %d) when asked from this host, so the address resolves and something is listening. "+
			"GitHub reaching it from outside is still worth confirming with a redelivery from the App's Advanced tab.",
			url, resp.StatusCode),
		""
}
