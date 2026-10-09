package api

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/tailscale/tailcat"
)

// The provider routes are the configuration half of renting machines: where
// they come from, what shape they are, and how many may exist. The machines
// themselves are handlers_machines.go.
//
// One rule runs through the whole file: the credential goes in and never comes
// back. It is sealed with the instance key before the row is written, it has no
// field on the view, and providerResponse reports credentials_configured so a
// form can say "a credential is set" without holding one.

// providerResponse is the shape GET /providers returns, rendered by the
// controller so the event stream's provider.updated frames are the same JSON.
type providerResponse = controller.ProviderView

// providerKindResponse is one driver this build ships, with the questions its
// form has to ask.
type providerKindResponse = controller.ProviderKindView

// providerSettingResponse is one of those questions.
type providerSettingResponse = controller.ProviderSettingView

// providerCheckResponse is what the live preflight found.
type providerCheckResponse = controller.ProviderCheckView

// providerDiscoveryResponse is what a provider's credential can actually see.
type providerDiscoveryResponse = controller.ProviderDiscoveryView

// providerOrphansResponse is the review page: rows and resources that disagree.
type providerOrphansResponse = controller.OrphanReportView

// ---------------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------------

// handleListProviders answers GET /api/v1/providers.
//
// Each provider's machines are counted through the same query the detail page
// uses rather than one listing of every machine in the fleet: providers are
// counted in single figures, and a page of machines would be capped while the
// count on a card must not be.
// loopbackHost reports whether an address names this machine, which is the one
// case where plain HTTP carries nothing off the box.
func loopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	rows, err := s.ctrl.Store().ListProviders(r.Context())
	if err != nil {
		s.internal(w, r, "listing providers", err)
		return
	}
	out := make([]providerResponse, 0, len(rows))
	for _, row := range rows {
		machines, err := s.ctrl.Store().ListMachinesForProvider(r.Context(), row.ID)
		if err != nil {
			s.internal(w, r, "counting a provider's machines", err)
			return
		}
		out = append(out, s.ctrl.ProviderView(row, machines))
	}
	writeJSON(w, http.StatusOK, newList(out))
}

// handleProviderPairings answers GET /api/v1/providers/pairings: for every
// provider and pool, whether the provider would rent a machine for the pool and,
// if not, whose setting says so.
func (s *Server) handleProviderPairings(w http.ResponseWriter, r *http.Request) {
	items, err := s.ctrl.ProviderPairings(r.Context())
	if err != nil {
		s.internal(w, r, "working out which providers serve which pools", err)
		return
	}
	writeJSON(w, http.StatusOK, newList(items))
}

// handleGetProvider answers GET /api/v1/providers/{id}.
func (s *Server) handleGetProvider(w http.ResponseWriter, r *http.Request) {
	row, machines, ok := s.providerWithMachines(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.ctrl.ProviderView(row, machines))
}

// handleProviderKinds answers GET /api/v1/providers/kinds: what this build can
// rent from, and what each driver needs to be told. The wizard renders its form
// from this, so a setting a driver gained cannot be missing from the form.
func (s *Server) handleProviderKinds(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, newList(s.ctrl.ProviderKinds()))
}

// providerWithMachines reads a provider and its machines, answering the client
// itself when either read fails.
func (s *Server) providerWithMachines(w http.ResponseWriter, r *http.Request) (*store.Provider, []*store.Machine, bool) {
	row, err := s.ctrl.Store().GetProvider(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the provider", err)
		return nil, nil, false
	}
	machines, err := s.ctrl.Store().ListMachinesForProvider(r.Context(), row.ID)
	if err != nil {
		s.internal(w, r, "counting the provider's machines", err)
		return nil, nil, false
	}
	return row, machines, true
}

// ---------------------------------------------------------------------------
// Creating and editing
// ---------------------------------------------------------------------------

// providerInput is ProviderCreate and ProviderUpdate in one type.
//
// Every field is a pointer for the reason poolInput's are: a PATCH that renamed
// a provider must not silently reset max_machines to zero, which on a provider
// with machines is a fleet-wide change nobody asked for. The credential is the
// one field where an empty string means "leave it alone" rather than "clear it"
// -- a form that renders a blank password box and then saves would otherwise
// erase the credential every time somebody fixed a typo elsewhere.
type providerInput struct {
	SetupID            *string             `json:"setup_id"`
	Kind               *store.ProviderKind `json:"kind"`
	Name               *string             `json:"name"`
	Endpoint           *string             `json:"endpoint"`
	CAPEM              *string             `json:"ca_pem"`
	InsecureSkipVerify *bool               `json:"insecure_skip_verify"`
	Settings           *map[string]string  `json:"settings"`
	Credential         *string             `json:"credential"`
	// Connection is "direct" or "tailcat". TailcatAddress is the gateway's
	// address, handled exactly as the credential is: sealed, never returned,
	// and an empty string on a PATCH leaves the stored one alone. Switching
	// back to a direct connection is what clears it, so a form that cannot
	// read the address back cannot erase it by accident either.
	Connection     *string `json:"connection"`
	TailcatAddress *string `json:"tailcat_address"`

	MachineLabels   *map[string]string `json:"machine_labels"`
	MachineCapacity *int               `json:"machine_capacity"`
	MachineBackend  *string            `json:"machine_backend"`
	MachinePlatform *store.Platform    `json:"machine_platform"`
	MachineCPUs     *float64           `json:"machine_cpus"`
	MachineMemoryMB *int64             `json:"machine_memory_mb"`
	MachineDiskMB   *int64             `json:"machine_disk_mb"`

	PoolSelector       *map[string]string `json:"pool_selector"`
	MaxMachines        *int               `json:"max_machines"`
	MaxCreatesInFlight *int               `json:"max_creates_in_flight"`
	IdleTimeout        *string            `json:"idle_timeout"`
	CostPerMachineHour *float64           `json:"cost_per_machine_hour"`
	Enabled            *bool              `json:"enabled"`
}

// defaultProvider is a new provider before the request is applied.
//
// MaxMachines is deliberately absent from it. Zero rents nothing, which is the
// safe answer for a row nobody has finished configuring: a provider that bought
// machines the moment its credential was accepted would spend money on the
// strength of a connection test.
func defaultProvider() *store.Provider {
	return &store.Provider{
		MachineCapacity:    2,
		MachineBackend:     store.BackendDocker,
		MaxCreatesInFlight: 1,
		IdleTimeout:        store.Duration(15 * time.Minute),
		Enabled:            true,
	}
}

// apply folds the request into a provider, returning the field errors it could
// not. Parsing and validation are one pass, as they are for a pool: an
// unparseable idle_timeout is a validation failure with a field name, not a 400
// about JSON.
func (in *providerInput) apply(p *store.Provider) []fieldError {
	var errs []fieldError
	add := func(field, msg string) { errs = append(errs, fieldError{field, msg}) }

	if in.Kind != nil {
		p.Kind = store.ProviderKind(strings.TrimSpace(string(*in.Kind)))
	}
	if in.Name != nil {
		p.Name = strings.TrimSpace(*in.Name)
	}
	if in.Endpoint != nil {
		p.Endpoint = strings.TrimSpace(*in.Endpoint)
	}
	if in.CAPEM != nil {
		p.CAPEM = strings.TrimSpace(*in.CAPEM)
	}
	if in.InsecureSkipVerify != nil {
		p.InsecureSkipVerify = *in.InsecureSkipVerify
	}
	if in.Settings != nil {
		p.Settings = store.StringMap(*in.Settings)
	}
	if in.MachineLabels != nil {
		p.MachineLabels = store.StringMap(*in.MachineLabels)
	}
	if in.MachineCapacity != nil {
		p.MachineCapacity = *in.MachineCapacity
	}
	if in.MachineBackend != nil {
		p.MachineBackend = store.BackendKind(strings.TrimSpace(*in.MachineBackend))
	}
	if in.MachinePlatform != nil {
		p.MachinePlatform = in.MachinePlatform.Normalized()
	}
	if in.MachineCPUs != nil {
		p.MachineCPUs = *in.MachineCPUs
	}
	if in.MachineMemoryMB != nil {
		p.MachineMemoryMB = *in.MachineMemoryMB
	}
	if in.MachineDiskMB != nil {
		p.MachineDiskMB = *in.MachineDiskMB
	}
	if in.PoolSelector != nil {
		p.PoolSelector = store.StringMap(*in.PoolSelector)
	}
	if in.MaxMachines != nil {
		p.MaxMachines = *in.MaxMachines
	}
	if in.MaxCreatesInFlight != nil {
		p.MaxCreatesInFlight = *in.MaxCreatesInFlight
	}
	if in.CostPerMachineHour != nil {
		p.CostPerMachineHour = *in.CostPerMachineHour
	}
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}
	if in.IdleTimeout != nil {
		d, err := time.ParseDuration(strings.TrimSpace(*in.IdleTimeout))
		switch {
		case err != nil:
			add("idle_timeout", fmt.Sprintf("%q is not a duration; write it as 15m or 1h", *in.IdleTimeout))
		case d < 0:
			add("idle_timeout", "an idle timeout cannot be negative; use 0 to delete a machine as soon as nothing needs it")
		default:
			p.IdleTimeout = store.Duration(d)
		}
	}

	if p.Name == "" {
		add("name", "give this provider a name; it is what the machines page, the audit log and every problem about it will call it")
	}
	if p.Endpoint != "" {
		u, err := url.Parse(p.Endpoint)
		switch {
		case err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https"):
			add("endpoint", fmt.Sprintf("%q is not an address this controller can reach; write it as https://pve.example.com:8006", p.Endpoint))
		case u.Scheme == "http" && !loopbackHost(u.Hostname()):
			// Refused here rather than left to the driver, because the driver
			// refuses it at the first call and by then the row is saved: the
			// check answers 500 with the reason only in the log, the machine
			// loop files the same error under a kind the problems drawer has no
			// case for, and machines sit in planned with nothing on any page
			// saying why. Certificate verification is a different question and
			// switching it off does not answer this one -- the credential on
			// this row can create and destroy machines.
			add("endpoint", fmt.Sprintf("%q would send this provider's credential across the network in the clear, "+
				"and that credential can create and destroy machines. Use https://. "+
				"Turning certificate verification off does not permit this.", p.Endpoint))
		}
	}
	if !p.MachineBackend.Valid() {
		add("machine_backend", fmt.Sprintf("%q is not a backend; use docker, podman or process", p.MachineBackend))
	}
	if p.MachineCapacity < 1 {
		add("machine_capacity", "a machine that can run no runners would be bought and never used; give it at least one")
	}
	if p.MaxMachines < 0 {
		add("max_machines", "a ceiling cannot be negative; use 0 to rent nothing")
	}
	if p.MaxCreatesInFlight < 0 {
		add("max_creates_in_flight", "a ceiling cannot be negative; use 1 to build one machine at a time")
	}
	if p.MachineCPUs < 0 {
		add("machine_cpus", "a machine cannot have fewer than no CPUs")
	}
	if p.MachineMemoryMB < 0 {
		add("machine_memory_mb", "a machine cannot have less than no memory")
	}
	if p.MachineDiskMB < 0 {
		add("machine_disk_mb", "a machine cannot have less than no disk")
	}
	if p.CostPerMachineHour < 0 {
		add("cost_per_machine_hour", "a machine cannot cost less than nothing an hour")
	}
	return errs
}

// validateProvider is the half of the check that needs the rest of the
// database and the driver: the kind has to be one this build can speak, the
// name has to be free, and the driver's own offline validator has to be happy
// with the answers it was given. existingID excludes the row being edited from
// the uniqueness check, so opening a provider and saving it is not refused
// about itself.
func (s *Server) validateProvider(r *http.Request, p *store.Provider, existingID string) []fieldError {
	var errs []fieldError
	add := func(field, msg string) { errs = append(errs, fieldError{field, msg}) }

	switch {
	case p.Kind == "":
		add("kind", "say what this provider is; GET /api/v1/providers/kinds lists what this build can rent from")
	case !p.Kind.Valid():
		add("kind", fmt.Sprintf("%q is not a provider kind this build knows", p.Kind))
	}

	existing, err := s.ctrl.Store().ListProviders(r.Context())
	if err != nil {
		// A uniqueness check that could not run must not pass silently: the
		// store's own constraint is the backstop and answers 409.
		s.logger(r).Warn("could not check provider names for uniqueness", "error", err)
	}
	for _, other := range existing {
		if other.ID != existingID && strings.EqualFold(other.Name, p.Name) {
			add("name", fmt.Sprintf("a provider called %s already exists; names are how machines and audit rows say where they came from, so they have to be distinct", other.Name))
			break
		}
	}

	if p.Kind.Valid() {
		findings, verr := s.ctrl.ValidateProviderSettings(p.Kind, p.Settings)
		switch {
		case errors.Is(verr, controller.ErrProviderKindUnknown):
			add("kind", fmt.Sprintf("this build has no driver for %s providers, so nothing here could ever create a machine; upgrade to a build that ships one, or choose another kind", p.Kind))
		case verr != nil:
			s.logger(r).Warn("could not validate a provider's settings", "kind", p.Kind, "error", verr)
		default:
			for _, f := range findings {
				if f.Severity != config.SeverityError {
					continue
				}
				add(settingField(f.Setting), strings.TrimSpace(f.Title+". "+f.Fix))
			}
		}
	}
	return errs
}

// settingField turns a driver finding's setting name into the field a form can
// highlight.
//
// A driver names the answer it is complaining about in the terms an operator
// reads in a log line -- "template_node", or "provider.settings.zone" -- while
// the form knows it as a key of the settings object. Everything that is not one
// of the provider's own top-level fields is one of those keys.
func settingField(setting string) string {
	setting = strings.TrimSpace(setting)
	switch setting {
	case "":
		return "settings"
	case "kind", "name", "endpoint", "ca_pem", "insecure_skip_verify", "credential":
		return setting
	}
	if key := strings.TrimPrefix(setting, "provider.settings."); key != setting {
		return "settings." + key
	}
	if strings.Contains(setting, ".") {
		// Something fully qualified that is not a setting of ours -- a
		// configuration key, say. Naming a field the form does not have would
		// highlight nothing, so it stays as the message's own words.
		return "settings"
	}
	return "settings." + setting
}

// handleCreateProvider answers POST /api/v1/providers.
func (s *Server) handleCreateProvider(w http.ResponseWriter, r *http.Request) {
	if s.key == nil {
		s.noEncryptionKey(w)
		return
	}
	var in providerInput
	if !decode(w, r, &in) {
		return
	}
	if !s.resolveProviderSetup(w, r, &in) {
		return
	}
	p := defaultProvider()
	errs := in.apply(p)
	errs = append(errs, s.validateProvider(r, p, "")...)
	errs = append(errs, s.connectionErrors(&in, p)...)
	if len(errs) > 0 {
		unprocessable(w, "this provider cannot be created as described", errs)
		return
	}

	if err := s.ctrl.Store().CreateProvider(r.Context(), p); err != nil {
		s.fail(w, r, "creating the provider", err)
		return
	}
	if in.Credential != nil && strings.TrimSpace(*in.Credential) != "" {
		if !s.sealProviderCredential(w, r, p.ID, *in.Credential) {
			return
		}
	}
	if !s.applyConnection(w, r, &in, p) {
		return
	}
	// Read back, because the credential was written by a second statement and
	// the response says whether one is configured.
	fresh, err := s.ctrl.Store().GetProvider(r.Context(), p.ID)
	if err != nil {
		s.internal(w, r, "reading the provider back", err)
		return
	}
	// The row itself is the audit document: the credential is sealed bytes
	// behind json:"-", so there is nothing here to blank first.
	s.auth.Auditor().Created(r.Context(), Identity(r.Context()), "provider", fresh.ID, fresh)
	s.ctrl.PublishProvider(r.Context(), fresh)
	// A new provider may already be the answer to work that is queued now.
	s.ctrl.NudgeMachines()
	if in.SetupID != nil {
		if err := s.ctrl.Store().DeleteProviderSetup(r.Context(), *in.SetupID); err != nil {
			s.logger(r).Warn("could not remove completed provider setup", "error", err)
		}
	}
	writeJSON(w, http.StatusCreated, s.ctrl.ProviderView(fresh, nil))
}

// handleUpdateProvider answers PATCH /api/v1/providers/{id}.
func (s *Server) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	row, err := s.ctrl.Store().GetProvider(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the provider", err)
		return
	}
	var in providerInput
	if !decode(w, r, &in) {
		return
	}
	if !s.resolveProviderSetup(w, r, &in) {
		return
	}
	if s.key == nil && (in.Credential != nil && strings.TrimSpace(*in.Credential) != "" ||
		in.TailcatAddress != nil && strings.TrimSpace(*in.TailcatAddress) != "") {
		s.noEncryptionKey(w)
		return
	}

	before := *row
	p := *row
	errs := in.apply(&p)
	if in.Kind != nil && p.Kind != before.Kind {
		// Changing a kind would leave every machine this provider owns being
		// managed by a driver that has never heard of them.
		errs = append(errs, fieldError{"kind", fmt.Sprintf(
			"a provider's kind cannot be changed after it is created: the machines it already owns are %s machines. Create a second provider instead.", before.Kind)})
	}
	errs = append(errs, s.validateProvider(r, &p, row.ID)...)
	errs = append(errs, s.connectionErrors(&in, &p)...)
	if len(errs) > 0 {
		unprocessable(w, "this provider cannot be changed as described", errs)
		return
	}

	if err := s.ctrl.Store().UpdateProvider(r.Context(), &p); err != nil {
		s.fail(w, r, "saving the provider", err)
		return
	}
	if in.Credential != nil && strings.TrimSpace(*in.Credential) != "" {
		if !s.sealProviderCredential(w, r, p.ID, *in.Credential) {
			return
		}
	}
	if !s.applyConnection(w, r, &in, &p) {
		return
	}
	fresh, err := s.ctrl.Store().GetProvider(r.Context(), p.ID)
	if err != nil {
		s.internal(w, r, "reading the provider back", err)
		return
	}
	s.auth.Auditor().Updated(r.Context(), Identity(r.Context()), "provider", fresh.ID, &before, fresh)
	s.ctrl.PublishProvider(r.Context(), fresh)
	// A ceiling that went up, or a provider that was just enabled, changes
	// what the next machine pass may do.
	s.ctrl.NudgeMachines()
	machines, err := s.ctrl.Store().ListMachinesForProvider(r.Context(), fresh.ID)
	if err != nil {
		s.internal(w, r, "counting the provider's machines", err)
		return
	}
	writeJSON(w, http.StatusOK, s.ctrl.ProviderView(fresh, machines))
}

// connection is what the request asked for, resolved against what the row
// already has: the connection named, the address to seal (empty for none), and
// whether the stored address is to be cleared.
//
// An address with no connection named means a private one -- the address is
// the whole of the answer -- and an address beside "direct" is a contradiction
// rather than a choice, because one of the two would have to be ignored and
// the form could not tell which.
func (in *providerInput) connection(p *store.Provider) (kind, address string, drop bool, errs []fieldError) {
	if in.TailcatAddress != nil {
		address = strings.TrimSpace(*in.TailcatAddress)
	}
	switch {
	case in.Connection != nil:
		kind = strings.ToLower(strings.TrimSpace(*in.Connection))
	case address != "":
		kind = "tailcat"
	case len(p.TailcatAddressEnc) > 0:
		kind = "tailcat"
	default:
		kind = "direct"
	}
	switch kind {
	case "direct":
		if address != "" {
			errs = append(errs, fieldError{"connection", "a direct connection has no private address; choose tailcat to use it, or leave it out"})
		}
		drop = len(p.TailcatAddressEnc) > 0
	case "tailcat":
		if address == "" && len(p.TailcatAddressEnc) == 0 {
			errs = append(errs, fieldError{"tailcat_address", "a private connection needs the gateway's address; run `zoomies gateway --target <hypervisor-api>` beside the provider and paste the address it prints"})
		}
		if address != "" {
			// Refused here, offline, rather than at the first clone. The
			// message never quotes the address: it is a capability, and a
			// 422 body is copied into bug reports.
			info, err := tailcat.ParseAddr(tailcat.Addr(address))
			if err != nil || info.PresharedKey.IsZero() {
				errs = append(errs, fieldError{"tailcat_address", "that is not a complete Tailcat address; copy the whole address the gateway printed, beginning with tc"})
			}
		}
	default:
		errs = append(errs, fieldError{"connection", "choose direct or tailcat"})
	}
	return kind, address, drop, errs
}

// tailcatAvailable says private connections can be used at all, on the same
// terms the hosts' enrolment and GET /meta apply.
func (s *Server) tailcatAvailable() bool {
	return s.cfg().Server.TailcatEnabled && !s.cfg().Security.DisableAuth && s.key != nil
}

// connectionErrors is the connection half of validating a request, shared by
// create, update and the dry run. The availability check sits here rather
// than in apply because it is a fact about this deployment, not the draft.
func (s *Server) connectionErrors(in *providerInput, p *store.Provider) []fieldError {
	kind, address, _, errs := in.connection(p)
	if kind == "tailcat" && (address != "" || len(p.TailcatAddressEnc) == 0) && !s.tailcatAvailable() {
		errs = append(errs, fieldError{"connection", "private connections are disabled; enable authentication, set server.tailcat_enabled to true and configure a controller encryption key, then restart the controller"})
	}
	// Only a direct connection is dialled from this machine. Through a
	// gateway the endpoint is reached from the gateway's own network, which
	// is the private network it was installed on purpose to reach, and
	// nothing near this controller is.
	if kind == "direct" {
		if f := config.CheckOutboundURL("endpoint", p.Endpoint, s.cfg().Security.AllowPrivateEgress); f != nil {
			errs = append(errs, fieldError{"endpoint", f.Title + ". " + f.Fix})
		}
	}
	return errs
}

// applyConnection writes the connection half of a request after the row has
// been saved: the sealed address, or its removal. It answers the client itself
// on failure and reports whether the caller may carry on.
func (s *Server) applyConnection(w http.ResponseWriter, r *http.Request, in *providerInput, p *store.Provider) bool {
	_, address, drop, _ := in.connection(p)
	switch {
	case address != "":
		sealed, err := s.key.SealString(address)
		if err != nil {
			s.internal(w, r, "sealing the provider's private connection address", err)
			return false
		}
		if err := s.ctrl.Store().SetProviderTailcatAddress(r.Context(), p.ID, sealed); err != nil {
			s.fail(w, r, "saving the provider's private connection address", err)
			return false
		}
	case drop:
		if err := s.ctrl.Store().SetProviderTailcatAddress(r.Context(), p.ID, nil); err != nil {
			s.fail(w, r, "switching the provider to a direct connection", err)
			return false
		}
	}
	return true
}

// sealProviderCredential seals a credential with the instance key and writes
// it. It answers the client itself on failure and reports whether the caller
// may carry on.
func (s *Server) sealProviderCredential(w http.ResponseWriter, r *http.Request, id, credential string) bool {
	sealed, err := s.key.SealString(strings.TrimSpace(credential))
	if err != nil {
		s.internal(w, r, "sealing the provider's credential", err)
		return false
	}
	if err := s.ctrl.Store().SetProviderCredentials(r.Context(), id, sealed); err != nil {
		s.fail(w, r, "saving the provider's credential", err)
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Validating a draft
// ---------------------------------------------------------------------------

// validateProviderResponse is the wizard's review step: what would stop this
// provider being saved, and what saving it would weaken. It always answers 200
// -- an invalid draft is a successful answer to "is this valid" -- which is the
// POST /pools/validate shape.
type validateProviderResponse struct {
	Valid  bool         `json:"valid"`
	Errors []fieldError `json:"errors"`
	// Warnings are the driver's own findings below error severity: the
	// answers that are legal and still cost something, in the same type the
	// startup validator and the problems drawer use.
	Warnings []config.Finding `json:"warnings"`
}

// handleValidateProvider answers POST /api/v1/providers/validate. It writes
// nothing and dials nothing: a driver's Validate is offline by contract, so the
// form can run this as somebody types. POST /providers/{id}/check is the call
// that talks to the hypervisor.
//
// `?id=` names the provider this is a dry run of an edit to, so that opening a
// provider and pressing on is not refused with "a provider called pve-lab
// already exists" -- about itself.
func (s *Server) handleValidateProvider(w http.ResponseWriter, r *http.Request) {
	var in providerInput
	if !decode(w, r, &in) {
		return
	}
	if !s.resolveProviderSetup(w, r, &in) {
		return
	}
	p := defaultProvider()
	errs := in.apply(p)
	existingID := strings.TrimSpace(r.URL.Query().Get("id"))
	errs = append(errs, s.validateProvider(r, p, existingID)...)
	if existingID != "" {
		// The dry run of an edit is judged against the row it edits, so a
		// form that leaves the address box blank on a provider that already
		// has one is told nothing is wrong, exactly as the PATCH will.
		if row, err := s.ctrl.Store().GetProvider(r.Context(), existingID); err == nil {
			p.TailcatAddressEnc = row.TailcatAddressEnc
		}
	}
	errs = append(errs, s.connectionErrors(&in, p)...)

	// Only the driver's own findings. This endpoint is offline, so the
	// warnings that need the hypervisor -- a certificate nobody checks, a
	// privilege the token is missing -- belong to the preflight, which has a
	// documented code for each and is one button away in the same wizard.
	warnings := []config.Finding{}
	if p.Kind.Valid() {
		if findings, err := s.ctrl.ValidateProviderSettings(p.Kind, p.Settings); err == nil {
			for _, f := range findings {
				if f.Severity != config.SeverityError {
					warnings = append(warnings, f)
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, validateProviderResponse{
		Valid:    len(errs) == 0,
		Errors:   emptySlice(errs),
		Warnings: warnings,
	})
}

// ---------------------------------------------------------------------------
// Deleting
// ---------------------------------------------------------------------------

// handleDeleteProvider answers DELETE /api/v1/providers/{id}.
//
// It refuses while any machine still believes it holds a resource, and says how
// many. The rows are the only record of what was rented: deleting them is how a
// fleet stops knowing about VMs it is still paying for, so the machines go
// first and the provider goes after them.
func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	row, machines, ok := s.providerWithMachines(w, r)
	if !ok {
		return
	}
	owned := 0
	for _, m := range machines {
		if m.Owns() {
			owned++
		}
	}
	if owned > 0 {
		conflict(w, fmt.Sprintf("provider %s still has %s behind it, so it was not deleted: this row is the only record "+
			"of what was rented, and deleting it would leave those machines running with nothing tracking them. "+
			"Delete each machine (which removes its VM too), or release the ones whose resource is already gone, then delete this provider.",
			row.Name, machineCount(owned)))
		return
	}
	if err := s.ctrl.Store().DeleteProvider(r.Context(), row.ID); err != nil {
		s.fail(w, r, "deleting the provider", err)
		return
	}
	s.auth.Auditor().Deleted(r.Context(), Identity(r.Context()), "provider", row.ID, row)
	s.ctrl.PublishProviderDeleted(row.ID)
	noContent(w)
}

// ---------------------------------------------------------------------------
// The kill switch
// ---------------------------------------------------------------------------

// pauseProviderRequest carries why, which is the whole value of the row in the
// audit log six weeks later.
type pauseProviderRequest struct {
	Reason string `json:"reason"`
}

// handlePauseProvider and handleResumeProvider are the two halves of the switch
// on the Hosts page. Pausing blocks new machines only: draining, deleting,
// recovering and verifying ownership all continue, because a switch that also
// stopped those would strand running VMs nobody is watching.
func (s *Server) handlePauseProvider(w http.ResponseWriter, r *http.Request) {
	s.setProviderPaused(w, r, true)
}

func (s *Server) handleResumeProvider(w http.ResponseWriter, r *http.Request) {
	s.setProviderPaused(w, r, false)
}

// setProviderPaused presses the switch. Pressing it twice is not an error: the
// person doing it at three in the morning wants the fleet in a known state, not
// an argument about whether it was already in one.
func (s *Server) setProviderPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	row, err := s.ctrl.Store().GetProvider(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the provider", err)
		return
	}
	var req pauseProviderRequest
	if !decodeOptional(w, r, &req) {
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if !paused {
		reason = ""
	}
	if row.Paused != paused || row.PausedReason != reason {
		if err := s.ctrl.Store().SetProviderPaused(r.Context(), row.ID, paused, reason); err != nil {
			s.fail(w, r, "saving the provider", err)
			return
		}
		action := "provider.resume"
		if paused {
			action = "provider.pause"
		}
		s.auth.Auditor().Act(r.Context(), Identity(r.Context()), action, "provider", row.ID, map[string]any{
			"name": row.Name, "paused": paused, "was": row.Paused, "reason": reason,
		})
	}
	fresh, machines, ok := s.providerWithMachines(w, r)
	if !ok {
		return
	}
	s.ctrl.PublishProvider(r.Context(), fresh)
	// Resuming is the half that has work to start; pausing nudges too, because
	// a pass that runs now is the one that stops planning machines.
	s.ctrl.NudgeMachines()
	writeJSON(w, http.StatusOK, s.ctrl.ProviderView(fresh, machines))
}

// ---------------------------------------------------------------------------
// Asking the provider itself
// ---------------------------------------------------------------------------

// handleCheckProvider answers POST /api/v1/providers/{id}/check: the live
// preflight. It changes nothing on the provider -- it is the one call an
// operator can make against a hypervisor without risking anything -- and is
// audited because it uses the credential.
func (s *Server) handleCheckProvider(w http.ResponseWriter, r *http.Request) {
	row, err := s.ctrl.Store().GetProvider(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the provider", err)
		return
	}
	report, err := s.ctrl.CheckProvider(r.Context(), row)
	if err != nil {
		s.providerFailed(w, r, "checking the provider", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "provider.check", "provider", row.ID, map[string]any{
		"name": row.Name, "reachable": report.Reachable, "ok": report.OK, "findings": len(report.Findings),
	})
	writeJSON(w, http.StatusOK, report)
}

// handleProviderDiscovery answers GET /api/v1/providers/{id}/discovery: the
// nodes, storages, bridges and templates this credential can actually see, so
// the form offers a menu rather than asking somebody to go and look an
// identifier up.
func (s *Server) handleProviderDiscovery(w http.ResponseWriter, r *http.Request) {
	row, err := s.ctrl.Store().GetProvider(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the provider", err)
		return
	}
	found, err := s.ctrl.DiscoverProvider(r.Context(), row)
	if err != nil {
		s.providerFailed(w, r, "asking the provider what it can see", err)
		return
	}
	writeJSON(w, http.StatusOK, found)
}

// handleDiscoverDraft answers POST /api/v1/providers/discover: what a draft's
// credential can see, before the draft is saved.
//
// It always answers 200 for a draft that is well formed: a hypervisor that
// does not answer, or refuses the token, is the normal state of a wizard
// halfway through, and comes back as a sentence in `unavailable` for the form
// to show beside the boxes it then asks to be typed into. What is refused
// here is a draft the create would refuse too, with the same field errors, so
// the wizard never asks a provider about a body it could not save.
func (s *Server) handleDiscoverDraft(w http.ResponseWriter, r *http.Request) {
	var in providerInput
	if !decode(w, r, &in) {
		return
	}
	if !s.resolveProviderSetup(w, r, &in) {
		return
	}
	p := defaultProvider()
	// Only the connect step's answers are judged. The placement settings a
	// create insists on are the very ones the menu is being asked for, and a
	// draft's name may well still be the placeholder.
	var errs []fieldError
	for _, e := range in.apply(p) {
		switch e.Field {
		case "endpoint", "ca_pem", "connection", "tailcat_address":
			errs = append(errs, e)
		}
	}
	switch {
	case p.Kind == "":
		errs = append(errs, fieldError{"kind", "say what this provider is; GET /api/v1/providers/kinds lists what this build can rent from"})
	case !p.Kind.Valid():
		errs = append(errs, fieldError{"kind", fmt.Sprintf("%q is not a provider kind this build knows", p.Kind)})
	}
	if p.Endpoint == "" {
		errs = append(errs, fieldError{"endpoint", "give the address this controller reaches the provider on"})
	}
	errs = append(errs, s.connectionErrors(&in, p)...)
	credential := ""
	if in.Credential != nil {
		credential = strings.TrimSpace(*in.Credential)
	}
	if credential == "" {
		errs = append(errs, fieldError{"credential", "the provider can only be asked what it can see with a credential to ask with"})
	}
	if len(errs) > 0 {
		unprocessable(w, "this draft cannot be asked about as described", errs)
		return
	}
	address := ""
	if in.TailcatAddress != nil {
		address = strings.TrimSpace(*in.TailcatAddress)
	}
	if p.Name == "" {
		p.Name = "draft"
	}
	found, err := s.ctrl.DiscoverDraft(r.Context(), p, credential, address)
	switch {
	case err == nil:
	case errors.Is(err, controller.ErrProviderKindUnknown), errors.Is(err, controller.ErrDiscoveryUnsupported):
		s.providerFailed(w, r, "asking the draft what it can see", err)
		return
	default:
		found = providerDiscoveryResponse{
			Nodes: []controller.ProviderChoiceView{}, Storages: []controller.ProviderChoiceView{},
			Bridges: []controller.ProviderChoiceView{}, Templates: []controller.ProviderChoiceView{},
			Unavailable: err.Error(),
		}
	}
	writeJSON(w, http.StatusOK, found)
}

// handleProviderOrphans answers GET /api/v1/providers/{id}/orphans: the three
// ways a row and a resource can disagree. Nothing on it is acted on
// automatically, which is why it is a page rather than a background job.
func (s *Server) handleProviderOrphans(w http.ResponseWriter, r *http.Request) {
	row, err := s.ctrl.Store().GetProvider(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the provider", err)
		return
	}
	report, err := s.ctrl.OrphanReport(r.Context(), row)
	if err != nil {
		s.internal(w, r, "reviewing the provider's machines", err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// providerFailed maps the two refusals that are about this deployment rather
// than about the request: a build with no driver for the row's kind, and a
// driver that cannot do what was asked. Both are 409s naming what to do, and
// everything else is a 500 with a request ID.
func (s *Server) providerFailed(w http.ResponseWriter, r *http.Request, doing string, err error) {
	switch {
	case errors.Is(err, controller.ErrProviderKindUnknown):
		conflict(w, err.Error()+". Upgrade to a build that ships that driver, or delete this provider.")
	case errors.Is(err, controller.ErrDiscoveryUnsupported):
		conflict(w, err.Error()+". Type the identifiers into the provider's settings instead.")
	default:
		s.fail(w, r, doing, err)
	}
}

// machineCount renders a count of machines for a sentence an operator reads.
func machineCount(n int) string {
	if n == 1 {
		return "1 machine"
	}
	return fmt.Sprintf("%d machines", n)
}
