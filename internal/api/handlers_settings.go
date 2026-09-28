package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// The settings surface.
//
// Every configuration key except the three that get the database open lives in
// the database, so this is where an administrator changes the fleet rather than
// where they are told to go and edit a file on the controller's host. Two
// things follow from that, and both are the point:
//
// A change is kept. It used to be that a PATCH altered the running process and
// nothing else, so a setting that was also in zoomies.yaml quietly went back to
// the file's value at the next restart -- the API said so in as many words, and
// it was still a surprise every time. Now the value is written, and the file is
// a layer underneath it.
//
// A change that cannot be applied now is still accepted. Rebinding a listener
// under live connections is not something a process can do to itself, so
// server.bind is stored and reported in pending_restart instead of refused.
// Refusing it never stopped anybody wanting it changed; it only moved the work
// to a text editor and left no record that anyone had asked.
//
// What is refused is a change that would not survive: a key an environment
// variable is pinning would be written, overridden at the next start, and
// appear to have been forgotten. The refusal names the variable.

// ---------------------------------------------------------------------------
// The shapes
// ---------------------------------------------------------------------------

// settingView is one configuration key as the settings page sees it: what it
// is, what it is set to, where that value came from, and whether this
// administrator can change it here.
//
// It is generated from the registry in internal/config rather than written out
// by hand. The lists it replaces -- the fourteen keys the API would write, the
// forty-seven it would refuse, the nested map it rendered from, and the two
// constants the browser kept -- were four hand-maintained copies of one fact,
// and all four had drifted.
type settingView struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Section string   `json:"section"`
	Kind    string   `json:"kind"`
	Choices []string `json:"choices,omitempty"`
	Summary string   `json:"summary"`

	// Value is what this controller is running. A secret's value is never
	// here; Configured says whether one is set, which is the useful fact.
	Value      any  `json:"value"`
	Secret     bool `json:"secret"`
	Configured bool `json:"configured"`
	// Default is what Zoomies would use if nothing said otherwise, so the UI
	// can offer "put it back" and show what that means.
	Default any `json:"default"`

	// Source is which layer won: default, file, database or environment.
	Source string `json:"source"`
	// Env is the variable that overrides this key, named so an operator can
	// find the one that is pinning it.
	Env string `json:"env"`
	// Stored says a value for this key is in the database.
	Stored bool `json:"stored"`

	// Editable says an administrator may change it here. Scope and Source are
	// between them the reason when it is false, and Reason says it in a
	// sentence.
	Editable bool   `json:"editable"`
	Scope    string `json:"scope"`
	Reason   string `json:"reason,omitempty"`

	// Live says a change is in force by the time the response is written.
	// False means it is stored and applies at the next restart.
	Live          bool   `json:"live"`
	RestartReason string `json:"restart_reason,omitempty"`
	// Pending says a stored value is waiting for that restart.
	Pending bool `json:"pending"`

	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	UpdatedBy string     `json:"updated_by,omitempty"`
}

// settingsResponse is the whole settings page in one document.
type settingsResponse struct {
	// Config is the effective configuration as a nested object, every secret
	// absent. It is what `zoomies config print` shows and what the diagnostics
	// bundle carries, kept because a person reading a configuration wants it
	// shaped like the file rather than as a list of rows.
	Config   map[string]any   `json:"config"`
	Settings []settingView    `json:"settings"`
	Findings []config.Finding `json:"findings"`

	// PendingRestart names the settings stored since this process started that
	// it cannot apply to itself.
	PendingRestart []string `json:"pending_restart"`
	// PinnedByEnvironment names the settings an environment variable is
	// holding, which is why the page will not let them be changed.
	PinnedByEnvironment []string `json:"pinned_by_environment"`
	// RestartRequiredKeys is every setting whose change waits for a restart,
	// whether or not one is waiting now. The UI labels them before an
	// administrator edits one rather than after.
	RestartRequiredKeys []string `json:"restart_required_keys"`

	ConfigPath       string `json:"config_path,omitempty"`
	Version          string `json:"version"`
	DatabasePath     string `json:"database_path"`
	EventSubscribers int    `json:"event_subscribers"`
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// dropAnsweredElsewhere removes the findings the configuration file cannot
// answer on its own.
//
// There is one: "backups never leave this host", which config.Validate raises
// from backup.remotes and cannot know about a destination an administrator
// added on the Backups page, because that is a database row. Telling somebody
// to do a thing they have already done is how a page teaches them to stop
// reading it.
func (s *Server) dropAnsweredElsewhere(r *http.Request, findings []config.Finding) []config.Finding {
	answered := false
	for _, remote := range s.ctrl.BackupRemotes(r.Context()) {
		if !remote.Disabled && !remote.Shadowed && remote.Problem == "" {
			answered = true
			break
		}
	}
	if !answered {
		return findings
	}
	out := findings[:0]
	for _, f := range findings {
		if f.Code != config.NoRemoteFinding {
			out = append(out, f)
		}
	}
	return out
}

// settingsConfig renders the effective configuration as a nested object.
//
// The registry-driven rendering lives in config.Redacted, shared with the
// support bundle and a backup's manifest; what is added here is the handful of
// values that are derived rather than configured, and are what an operator
// actually wants to see next to the settings they came from.
func (s *Server) settingsConfig(who store.Role) map[string]any {
	c := s.cfg()
	out := config.Redacted(c)

	// The nested object says the same thing the key-by-key list does, so it
	// has to answer the same way: an administrator who is not shown
	// backup.directory in the list must not find it here, or in the support
	// bundle that renders this same tree.
	if !who.AtLeast(store.RolePlatform) {
		for _, st := range config.Settings() {
			if platformsOwn(st) {
				deleteNested(out, st.Key)
			}
		}
	}

	// The encryption key's presence is the one thing here that is not read off
	// the configuration: it may have come from a file this process read at
	// startup rather than from a key any layer holds.
	setNested(out, "security.encryption_key_configured", s.key != nil)

	setNested(out, "github.webhook_url", c.WebhookURL())
	setNested(out, "github.polling_only", s.ctrl.PollingOnly())
	setNested(out, "oidc.redirect_url", s.oidcRedirectURL())
	return out
}

// setNested writes a dotted key into a tree of maps.
func setNested(into map[string]any, key string, value any) { config.SetNested(into, key, value) }

// deleteNested removes a dotted key from a tree of maps, and any section the
// removal leaves empty -- so a caller shown none of `backup.*` is not handed a
// bare `backup: {}` that says one exists and is being withheld.
func deleteNested(from map[string]any, key string) {
	parts := strings.Split(key, ".")
	node := from
	parents := make([]map[string]any, 0, len(parts))
	names := make([]string, 0, len(parts))
	for _, part := range parts[:len(parts)-1] {
		child, ok := node[part].(map[string]any)
		if !ok {
			return
		}
		parents = append(parents, node)
		names = append(names, part)
		node = child
	}
	delete(node, parts[len(parts)-1])
	for i := len(parents) - 1; i >= 0; i-- {
		child, _ := parents[i][names[i]].(map[string]any)
		if len(child) > 0 {
			return
		}
		delete(parents[i], names[i])
	}
}

func (s *Server) oidcRedirectURL() string {
	if s.oidcProvider().Enabled() {
		return s.oidcProvider().RedirectURL()
	}
	return s.cfg().OIDC.RedirectURL
}

// settingViews renders every key, in the order the documentation lists them.
func (s *Server) settingViews(rows []store.InstanceSetting, pending []string, who store.Role) []settingView {
	c := s.cfg()
	defaults := config.Default()
	stored := map[string]store.InstanceSetting{}
	for _, row := range rows {
		stored[row.Key] = row
	}

	out := make([]settingView, 0, len(config.Settings()))
	for _, st := range config.Settings() {
		// A fleet's administrator is not shown the platform's keys at all,
		// rather than shown them locked. A locked field still says what the
		// instance binds and where it ships its backups, and on an instance
		// one team operates for another that is the platform's business and
		// not the fleet's.
		if platformsOwn(st) && !who.AtLeast(store.RolePlatform) {
			continue
		}
		value, err := c.Value(st.Key)
		if err != nil {
			continue
		}
		row, isStored := stored[st.Key]
		source := string(c.Source(st.Key))

		v := settingView{
			Key:           st.Key,
			Label:         st.Label,
			Section:       st.Section(),
			Kind:          string(st.Kind),
			Choices:       st.Choices,
			Summary:       st.Summary,
			Secret:        st.Secret,
			Configured:    config.Text(st, value) != "",
			Source:        source,
			Env:           st.Env,
			Stored:        isStored,
			Scope:         string(st.Scope),
			Live:          st.Live,
			RestartReason: st.RestartReason,
			Pending:       slices.Contains(pending, st.Key),
		}
		if !st.Secret {
			v.Value = value
			if d, derr := defaults.Value(st.Key); derr == nil {
				v.Default = d
			}
		}
		if isStored {
			at := row.UpdatedAt
			v.UpdatedAt, v.UpdatedBy = &at, row.UpdatedBy
		}
		v.Editable, v.Reason = s.editable(st, c, who)
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return config.CompareKeys(out[i].Key, out[j].Key) < 0 })
	return out
}

// editable says whether this administrator may change a key here, and when they
// may not, why -- in a sentence written for the person reading it, because
// "not editable" with no reason is the thing that sends somebody to the source.
func (s *Server) editable(st config.Setting, c *config.Config, who store.Role) (bool, string) {
	if st.Platform() && !who.AtLeast(store.RolePlatform) {
		return false, "This belongs to whoever runs this controller rather than to the fleet: it changes what the process binds, trusts, stores or logs on its own machine. Ask them to change it."
	}
	switch st.Scope {
	case config.ScopeBootstrap:
		return false, fmt.Sprintf(
			"This is read before the database is open, so it cannot live in it. Set it in %s or as %s, and restart.",
			s.configFileName(), st.Env)
	case config.ScopeLocal:
		return false, fmt.Sprintf(
			"This belongs to a standalone agent's own host, not to the fleet. Set it there, in that agent's configuration or as %s.",
			st.Env)
	}
	if c.Source(st.Key) == config.SourceEnvironment {
		return false, fmt.Sprintf(
			"%s is setting this in the environment, and the environment is the last word. A change made here would be stored and then overridden at the next restart, so unset the variable first \u2014 or change it where it is set.",
			st.Env)
	}
	return true, ""
}

// callerRole is the asking identity's role, or the empty role when there is
// none. The empty role is at least nothing, so an unauthenticated caller that
// somehow reaches here is treated as the fleet rather than as the platform.
func callerRole(r *http.Request) store.Role { return callerRoleCtx(r.Context()) }

// isPlatform reports whether this caller runs the process rather than the
// fleet. It is the one question every audience filter asks.
func isPlatform(r *http.Request) bool { return callerRole(r).AtLeast(store.RolePlatform) }

// callerRoleCtx is callerRole for the handlers that carry a context rather
// than the request -- the support bundle assembles itself from several.
func callerRoleCtx(ctx context.Context) store.Role {
	if id := Identity(ctx); id != nil {
		return id.Role
	}
	return ""
}

// platformsOwn reports whether a setting describes the process's own machine,
// and so is shown to the platform alone. The bootstrap keys count: they are
// not platform-scoped, because they are not stored at all, but their values
// are the database file and the key file -- the two paths on the host that
// matter most -- and hiding database_path from an administrator while
// database.path sat in the same response hid nothing.
func platformsOwn(st config.Setting) bool {
	return st.Platform() || st.Scope == config.ScopeBootstrap
}

// platformOnly blanks a string that describes the process's own machine --
// where its configuration file is, where its database is -- for anybody but
// the platform. A fleet's administrator has no use for a path on somebody
// else's host, and on an operated instance it is not theirs to know.
func platformOnly(who store.Role, v string) string {
	if who.AtLeast(store.RolePlatform) {
		return v
	}
	return ""
}

// platformOnlyInt is platformOnly for a count. Zero rather than absent: the
// field is not optional in the schema, and "no subscribers" is the honest
// reading of a number the caller is not being told.
func platformOnlyInt(who store.Role, v int) int {
	if who.AtLeast(store.RolePlatform) {
		return v
	}
	return 0
}

// visibleKeys drops the platform's keys from a list of key names for anybody
// but the platform. The settings themselves are already filtered; a key that
// survived only in the pending-restart or restart-required list would name a
// setting the caller was never shown, which reads as a bug in the page.
func visibleKeys(keys []string, who store.Role) []string {
	if who.AtLeast(store.RolePlatform) {
		return keys
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if st, ok := config.LookupSetting(k); ok && platformsOwn(st) {
			continue
		}
		out = append(out, k)
	}
	return out
}

func (s *Server) configFileName() string {
	if p := s.cfg().Path(); p != "" {
		return p
	}
	return "zoomies.yaml"
}

// restartRequiredKeys is every setting a change to which waits for a restart.
// A fresh slice each time: the old version returned one backing array to every
// response, which a caller that sorted it would have corrupted for all of them.
func restartRequiredKeys() []string {
	var out []string
	for _, st := range config.StoredSettings() {
		if !st.Live {
			out = append(out, st.Key)
		}
	}
	slices.Sort(out)
	return out
}

// ---------------------------------------------------------------------------
// GET
// ---------------------------------------------------------------------------

// handleGetSettings answers GET /api/v1/settings.
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	page, err := s.settingsPage(r)
	if err != nil {
		s.internal(w, r, "reading the stored settings", err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// settingsPage renders the whole page. The import route returns it too, so
// that a client which just changed forty keys can repaint without asking.
func (s *Server) settingsPage(r *http.Request) (settingsResponse, error) {
	c := s.cfg()
	who := callerRole(r)
	rows, err := s.ctrl.Store().ListInstanceSettings(r.Context())
	if err != nil {
		return settingsResponse{}, err
	}
	// PendingRestart needs the sealed values to compare a stored credential
	// against the running one, so it reads the unblanked rows.
	sealed, err := s.ctrl.Store().InstanceSettings(r.Context())
	if err != nil {
		return settingsResponse{}, err
	}
	pending := config.PendingRestart(c, sealed, s.key)

	var pinned []string
	for _, st := range c.PinnedByEnvironment() {
		if platformsOwn(st) && !who.AtLeast(store.RolePlatform) {
			continue
		}
		pinned = append(pinned, st.Key)
	}

	// The findings about the rows themselves -- a stored value that will not
	// parse, keys a newer version left behind -- are recomputed here rather
	// than kept from startup. config.Validate cannot produce them: it works on
	// an in-memory snapshot and has never seen the rows. Printing them once at
	// startup and dropping them left the one page where each is actionable as
	// the one place it did not appear.
	stale := *c
	stale.SetSources(c.Sources())
	findings := append(config.ApplyStored(&stale, sealed, s.key).ForUI(), c.Validate().ForUI()...)
	findings = s.dropAnsweredElsewhere(r, findings)

	return settingsResponse{
		Config:              s.settingsConfig(who),
		Settings:            s.settingViews(rows, pending, who),
		Findings:            findings,
		PendingRestart:      emptySlice(visibleKeys(pending, who)),
		PinnedByEnvironment: emptySlice(pinned),
		RestartRequiredKeys: visibleKeys(restartRequiredKeys(), who),
		ConfigPath:          platformOnly(who, c.Path()),
		Version:             version.Short(),
		DatabasePath:        platformOnly(who, s.ctrl.Store().Path()),
		EventSubscribers:    platformOnlyInt(who, s.ctrl.Events().Subscribers()),
	}, nil
}

// ---------------------------------------------------------------------------
// PATCH
// ---------------------------------------------------------------------------

// change is one staged edit, checked but not yet written.
type change struct {
	setting config.Setting
	// unset asks for the stored row to be removed, so the key goes back to
	// whatever the configuration file or the built-in default says. A JSON
	// null means this, which reads the same way for every kind: "say nothing
	// about it".
	unset bool
	value any
	// before is what the setting was, for the audit trail.
	before any
}

// handleUpdateSettings changes the fleet's settings.
//
// Every key is checked before any is written, and the whole request is refused
// if one fails. A request is one change: applying the keys that parsed and
// answering 422 for the one that did not would leave an operator told their
// change was refused while half of it was in effect, with an audit row for
// neither half.
//
// The order of what follows matters. The candidate configuration is validated
// first, so a value that would stop the next startup is refused now rather than
// discovered at the next restart by an operator who can no longer reach this
// page. Then the rows are written, because a stored change that is not yet in
// force is recoverable and an applied change that was never stored is not.
// Then the live keys are pushed into the running snapshot.
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var raw map[string]any
	if !decode(w, r, &raw) {
		return
	}
	flat := map[string]any{}
	flatten("", raw, flat)

	current := s.cfg()
	candidate, staged, fields := s.planSettings(current, flat, callerRole(r))
	if len(fields) > 0 {
		unprocessable(w, "these settings could not be changed", fields)
		return
	}
	if len(staged) == 0 {
		s.handleGetSettings(w, r)
		return
	}
	if fields := validatePlan(current, candidate, staged); len(fields) > 0 {
		unprocessable(w, "that would leave a controller that will not start", fields)
		return
	}
	if err := s.applyPlan(r.Context(), Identity(r.Context()), staged); err != nil {
		s.internal(w, r, "storing the settings", err)
		return
	}
	s.handleGetSettings(w, r)
}

// planSettings checks every key in flat against the running configuration and
// returns the candidate that results, the changes that would be made, and the
// refusals. It writes nothing. The import route plans the same way, which is
// how a preview of an import can be trusted to be what applying it would do.
func (s *Server) planSettings(current *config.Config, flat map[string]any, who store.Role) (*config.Config, []change, []fieldError) {
	// A scratch copy to try the whole request on. Validating the result is
	// what makes it safe to let a settings page write a listener address.
	candidate := *current
	candidate.SetSources(current.Sources())

	var fields []fieldError
	staged := make([]change, 0, len(flat))
	for _, key := range sortedKeys(flat) {
		value := flat[key]
		st, ok := config.LookupSetting(key)
		if !ok {
			fields = append(fields, fieldError{key, fmt.Sprintf("%q is not a setting this version has", key)})
			continue
		}
		if editable, reason := s.editable(st, current, who); !editable {
			fields = append(fields, fieldError{key, reason})
			continue
		}

		ch := change{setting: st}
		if value == nil && st.Kind != config.KindOptionalBool {
			ch.unset = true
			// Clearing a key means the layers underneath decide, and what they
			// say is only known once the row is gone -- so the candidate gets
			// the default, which is the floor of those layers and the only
			// value that is certainly no worse.
			def, _ := config.Default().Value(key)
			value = def
		}
		before, err := candidate.SetValue(key, value)
		if err != nil {
			var se *config.SettingError
			if errors.As(err, &se) {
				fields = append(fields, fieldError{key, se.Error()})
			} else {
				fields = append(fields, fieldError{key, err.Error()})
			}
			continue
		}
		// What gets stored and applied is what the configuration made of the
		// request, not the request itself. A list arrives from JSON as []any
		// and a duration as free text; reading the value back gives the typed,
		// normalised thing, so the row in the database and the value in the
		// running snapshot are the same value rendered the same way.
		parsed, err := candidate.Value(key)
		if err != nil {
			fields = append(fields, fieldError{key, err.Error()})
			continue
		}
		ch.before, ch.value = before, parsed
		staged = append(staged, ch)
	}
	return &candidate, staged, fields
}

// validatePlan asks whether the candidate would leave a controller that will
// not start, and returns the refusals as fields.
//
// Only the errors the request introduces count. An instance can already be
// running with one -- a deployment that disabled authentication and later
// gained an external URL is the common way -- and refusing every edit on that
// instance would lock the operator out of the settings page that is the one
// place they could fix it. So the validator is run twice and the difference is
// what is refused.
func validatePlan(current, candidate *config.Config, staged []change) []fieldError {
	candidate.Normalize()
	changed := map[string]bool{}
	for _, ch := range staged {
		changed[ch.setting.Key] = true
	}
	fields := egressRefusals(candidate, changed)
	introduced := newErrors(current, candidate)
	for _, f := range introduced {
		field := f.Setting
		if !changed[field] {
			// The error is about a setting this request did not touch, so it
			// belongs to the request rather than to a field: it is the
			// combination that is wrong.
			field = ""
		}
		fields = append(fields, fieldError{field, findingSentence(f)})
	}
	return fields
}

// egressRefusals refuses a write that points one of the settings the
// controller dials at this machine or a private network, unless the
// candidate allows it.
//
// The validator only warns about the same thing, because at startup the
// value came from whoever runs the process and an upgrade must not stop a
// working install. Here it is somebody with settings rights asking this
// controller to send requests somewhere on their say-so, which is the case
// the guard exists for -- so it is decided here, whatever the validator's
// severity. Turning single sign-on on is a write that starts dialling the
// issuer, so it counts as writing the issuer.
func egressRefusals(candidate *config.Config, changed map[string]bool) []fieldError {
	var fields []fieldError
	for _, o := range candidate.OutboundURLs() {
		if !changed[o.Setting] && (o.Setting != "oidc.issuer" || !changed["oidc.enabled"]) {
			continue
		}
		if f := config.CheckOutboundURL(o.Setting, o.Value, candidate.Security.AllowPrivateEgress); f != nil {
			// Blamed on the key the request changed, so an import preview
			// marks a row the operator can skip.
			field := o.Setting
			if !changed[field] {
				field = "oidc.enabled"
			}
			fields = append(fields, fieldError{field, findingSentence(*f)})
		}
	}
	return fields
}

// applyPlan stores the staged changes, pushes the live half into the running
// snapshot, and writes the audit row.
func (s *Server) applyPlan(ctx context.Context, id *auth.Identity, staged []change) error {
	if err := s.writeSettings(ctx, id.Name, staged); err != nil {
		return err
	}

	// Only the live half reaches the running snapshot. The rest is stored and
	// waiting, which is what pending_restart in the response says.
	applied, before := map[string]any{}, map[string]any{}
	var live []change
	for _, ch := range staged {
		before[ch.setting.Key] = ch.before
		if ch.unset {
			applied[ch.setting.Key] = nil
		} else {
			applied[ch.setting.Key] = ch.value
		}
		if ch.setting.Live {
			live = append(live, ch)
		}
	}
	if len(live) > 0 {
		// One update for the whole request, so two keys sent together land in
		// the same snapshot and no reader sees one without the other.
		s.ctrl.UpdateConfig(func(c *config.Config) {
			for _, ch := range live {
				if _, err := c.SetValue(ch.setting.Key, ch.value); err != nil {
					continue // Already parsed against the candidate; cannot fail here.
				}
				if ch.unset {
					c.Note(ch.setting.Key, config.SourceDefault)
				} else {
					c.Note(ch.setting.Key, config.SourceDatabase)
				}
			}
			c.Normalize()
		})
	}

	s.auth.Auditor().Updated(ctx, id, "settings", "settings", before, applied)
	return nil
}

// writeSettings puts the whole batch in the database in one transaction, so a
// request that both sets a key and clears another cannot half-apply -- which
// would leave an audit row claiming both halves took.
func (s *Server) writeSettings(ctx context.Context, by string, staged []change) error {
	var rows []store.InstanceSetting
	var clear []string
	for _, ch := range staged {
		if ch.unset {
			clear = append(clear, ch.setting.Key)
			continue
		}
		row, err := config.EncodeStored(ch.setting, ch.value, s.key)
		if err != nil {
			return err
		}
		rows = append(rows, row)
	}
	return s.ctrl.Store().ApplyInstanceSettings(ctx, by, rows, clear)
}

// newErrors returns the validation errors the candidate has and the running
// configuration does not, matched by code and setting so that the same error
// moving from one setting to another still counts as new.
func newErrors(running, candidate *config.Config) config.Findings {
	had := map[string]bool{}
	for _, f := range running.Validate().Errors() {
		had[f.Code+"\x00"+f.Setting] = true
	}
	var out config.Findings
	for _, f := range candidate.Validate().Errors() {
		if !had[f.Code+"\x00"+f.Setting] {
			out = append(out, f)
		}
	}
	return out
}

// findingSentence puts a validator finding into one line an operator can act
// on: what is true, then what to do about it.
func findingSentence(f config.Finding) string {
	title := strings.TrimRight(f.Title, ".")
	if f.Fix == "" {
		return title + "."
	}
	return title + ". " + f.Fix
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// flatten turns a nested settings object into dotted keys, so that a client may
// send either {"retention":{"jobs":"720h"}} or {"retention.jobs":"720h"} -- the
// UI's form produces one and a script written by hand produces the other.
//
// A key the registry knows is never descended into, so agent.labels -- the one
// setting whose value is itself a map -- arrives as one value rather than as a
// key per label.
func flatten(prefix string, in map[string]any, out map[string]any) {
	for k, v := range in {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if _, known := config.LookupSetting(key); !known {
			if nested, ok := v.(map[string]any); ok && len(nested) > 0 {
				flatten(key, nested, out)
				continue
			}
		}
		out[key] = v
	}
}
