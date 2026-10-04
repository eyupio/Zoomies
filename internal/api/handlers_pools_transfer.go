package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// Moving a fleet's pools between instances, or keeping them in version control.
//
// This is the settings export's other half, built the same way on purpose: a
// versioned document, a plan shown before anything is written, and one change
// or none. What differs is what a pool refers to. A pool belongs to an
// installation, and an installation's ins_ id means nothing on another
// instance, so the document names the installation by the organisation or
// repository it covers -- the one fact that is the same on both sides. And a
// pool's environment is the one place a secret can hide in it, so the
// document carries the variable names and never their values.

// poolsExportVersion is the pools document's own shape number. It moves when
// a field is removed or renamed, not when one is added.
const poolsExportVersion = 1

// poolsExport is the JSON form of a pools export.
type poolsExport struct {
	ExportVersion int       `json:"export_version"`
	ExportedAt    time.Time `json:"exported_at"`
	// ExportedFrom names the instance, so a file found in a repository a year
	// later says where it came from.
	ExportedFrom string         `json:"exported_from,omitempty"`
	Version      string         `json:"version"`
	Pools        []poolDocument `json:"pools"`
}

// poolDocument is one pool as the document carries it: the pool's own
// settings and nothing the instance made up about it -- no id, no counts, no
// timestamps -- because those are what differ between two instances running
// the same pool.
type poolDocument struct {
	Name string `json:"name"`
	// Installation is the organisation ("acme") or repository ("acme/widgets")
	// the pool's installation covers.
	Installation           string               `json:"installation"`
	Labels                 []string             `json:"labels"`
	RunnerGroup            string               `json:"runner_group"`
	Backend                store.BackendKind    `json:"backend"`
	Platform               store.Platform       `json:"platform"`
	Image                  string               `json:"image"`
	PullPolicy             store.PullPolicy     `json:"pull_policy"`
	RunnerVersion          string               `json:"runner_version"`
	MinRunners             int                  `json:"min_runners"`
	MaxRunners             int                  `json:"max_runners"`
	RepositoryScaleUpLimit int                  `json:"repository_scale_up_limit"`
	CostPerRunnerHour      *float64             `json:"cost_per_runner_hour"`
	Priority               int                  `json:"priority"`
	IdleTimeout            string               `json:"idle_timeout"`
	Ephemeral              bool                 `json:"ephemeral"`
	DockerMode             store.DockerMode     `json:"docker_mode"`
	Resources              store.Resources      `json:"resources"`
	CPUBurst               store.CPUBurstPolicy `json:"cpu_burst"`
	// RunnerSettings writes every override, with null for one the pool does
	// not make, so that importing the document hands a setting back to the
	// fleet on a pool that overrides it rather than leaving it alone.
	RunnerSettings poolDocumentTimings `json:"runner_settings"`
	Cache          store.CacheConfig   `json:"cache"`
	Tmpfs          store.TmpfsConfig   `json:"tmpfs"`
	HostSelector   map[string]string   `json:"host_selector"`
	// EnvKeys names the variables the pool injects. Their values stay behind:
	// a pool's environment is where a registry password ends up, and a file
	// meant for version control cannot be a place a password ends up.
	EnvKeys   []string `json:"env_keys"`
	RunAsRoot bool     `json:"run_as_root"`
	Enabled   bool     `json:"enabled"`
	// NoDefaultLabels travels with the labels it qualifies: a pool that
	// registers with its own labels only, imported without this, would start
	// answering to self-hosted and linux again on the other instance.
	NoDefaultLabels bool `json:"no_default_labels"`
}

type poolDocumentTimings struct {
	ProvisionTimeout  *string `json:"provision_timeout"`
	DrainTimeout      *string `json:"drain_timeout"`
	MaxRunnerLifetime *string `json:"max_runner_lifetime"`
	ScaleUpDelay      *string `json:"scale_up_delay"`
	DockerWait        *string `json:"docker_wait"`
}

func durationText(d *store.Duration) *string {
	if d == nil {
		return nil
	}
	s := d.String()
	return &s
}

// documentPool renders a pool as the document carries it.
func documentPool(p *store.Pool, installation string) poolDocument {
	envKeys := make([]string, 0, len(p.Env))
	for k := range p.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	return poolDocument{
		Name:                   p.Name,
		Installation:           installation,
		Labels:                 emptySlice([]string(p.Labels)),
		RunnerGroup:            p.RunnerGroup,
		Backend:                p.Backend,
		Platform:               p.Platform,
		Image:                  p.Image,
		PullPolicy:             p.PullPolicy,
		RunnerVersion:          p.RunnerVersion,
		MinRunners:             p.MinRunners,
		MaxRunners:             p.MaxRunners,
		RepositoryScaleUpLimit: p.RepositoryScaleUpLimit,
		CostPerRunnerHour:      p.CostPerRunnerHour,
		Priority:               p.Priority,
		IdleTimeout:            p.IdleTimeout.String(),
		Ephemeral:              p.Ephemeral,
		DockerMode:             p.DockerMode,
		Resources:              p.Resources,
		CPUBurst:               p.CPUBurst,
		RunnerSettings: poolDocumentTimings{
			ProvisionTimeout:  durationText(p.RunnerSettings.ProvisionTimeout),
			DrainTimeout:      durationText(p.RunnerSettings.DrainTimeout),
			MaxRunnerLifetime: durationText(p.RunnerSettings.MaxRunnerLifetime),
			ScaleUpDelay:      durationText(p.RunnerSettings.ScaleUpDelay),
			DockerWait:        durationText(p.RunnerSettings.DockerWait),
		},
		Cache:           p.Cache,
		Tmpfs:           p.Tmpfs,
		HostSelector:    emptyMap(p.HostSelector),
		EnvKeys:         envKeys,
		RunAsRoot:       p.RunAsRoot,
		Enabled:         p.Enabled,
		NoDefaultLabels: p.NoDefaultLabels,
	}
}

// handleExportPools answers GET /api/v1/pools/export.
func (s *Server) handleExportPools(w http.ResponseWriter, r *http.Request) {
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "yaml" {
		badRequestField(w, "format", "the export is written as json or yaml")
		return
	}
	doc, err := s.exportPools(r)
	if err != nil {
		s.internal(w, r, "reading the pools to export", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "pools.export", "pool", "pools", map[string]any{
		"format": format, "pools": len(doc.Pools),
	})

	stamp := doc.ExportedAt.Format("20060102-150405")
	w.Header().Set("Cache-Control", "no-store")
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		s.internal(w, r, "rendering the export", err)
		return
	}
	if format == "yaml" {
		// Through JSON, so the YAML has the field names the API has rather
		// than a second set of tags to keep in step with them.
		var tree map[string]any
		if err := json.Unmarshal(raw, &tree); err != nil {
			s.internal(w, r, "rendering the export", err)
			return
		}
		body, err := yaml.Marshal(map[string]any{"export_version": tree["export_version"], "pools": tree["pools"]})
		if err != nil {
			s.internal(w, r, "rendering the export", err)
			return
		}
		header := fmt.Sprintf("# Zoomies pools, exported %s from %s (Zoomies %s).\n"+
			"# Installations are named by the organisation or repository they cover, so this file can be imported\n"+
			"# on another instance with the same GitHub App installed. Environment values are never in here;\n"+
			"# env_keys names the variables to set by hand. Import it on the Pools page or with zoomies pools import.\n",
			doc.ExportedAt.Format(time.RFC3339), orPhrase(doc.ExportedFrom, "an unnamed instance"), doc.Version)
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="zoomies-pools-`+stamp+`.yaml"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(header))
		_, _ = w.Write(body)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="zoomies-pools-`+stamp+`.json"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(raw, '\n'))
}

func (s *Server) exportPools(r *http.Request) (poolsExport, error) {
	doc := poolsExport{
		ExportVersion: poolsExportVersion,
		ExportedAt:    s.ctrl.Now(),
		ExportedFrom:  s.cfg().Server.ExternalURL,
		Version:       version.Short(),
		Pools:         []poolDocument{},
	}
	pools, err := s.ctrl.Store().ListPools(r.Context())
	if err != nil {
		return doc, err
	}
	targets, err := s.installationTargets(r)
	if err != nil {
		return doc, err
	}
	for _, p := range pools {
		doc.Pools = append(doc.Pools, documentPool(p, targets[p.InstallationID]))
	}
	return doc, nil
}

// installationTargets maps each installation's id to the target a document
// names it by.
func (s *Server) installationTargets(r *http.Request) (map[string]string, error) {
	insts, err := s.ctrl.Store().ListInstallations(r.Context())
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(insts))
	for _, i := range insts {
		out[i.ID] = i.Target
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Import
// ---------------------------------------------------------------------------

// poolsImportRequest is what the page sends: the file's text, whether this
// is only a look, and the pools to leave out.
type poolsImportRequest struct {
	// Document is a pools export, as JSON or YAML text.
	Document string `json:"document"`
	// DryRun plans and reports without writing anything.
	DryRun bool `json:"dry_run"`
	// Skip names pools in the document to ignore -- the ones the preview
	// refused, or the ones the operator unticked.
	Skip []string `json:"skip"`
}

// poolImportField is one setting of one pool that applying would change.
type poolImportField struct {
	Field string `json:"field"`
	// Current is nil for a pool that does not exist yet.
	Current  any `json:"current"`
	Incoming any `json:"incoming"`
}

// poolImportChange is one pool's fate.
type poolImportChange struct {
	Pool string `json:"pool"`
	// Action is what applying would do: create, change, unchanged, or refused.
	Action string `json:"action"`
	// Fields is what differs, setting by setting: every setting for a create,
	// the ones that move for a change, none for the rest.
	Fields []poolImportField `json:"fields"`
	// Reason says why it is refused, in a sentence.
	Reason string `json:"reason,omitempty"`
	// Warnings are things that would not stop the import but that the
	// operator should know before it happens -- chiefly that no host in this
	// fleet could run a pool about to be created.
	Warnings []string `json:"warnings"`
	// EnvKeys names the environment variables the source pool injected, whose
	// values the document could not carry.
	EnvKeys []string `json:"env_keys"`
}

// The actions a planned pool can have.
const (
	poolCreateAction    = "create"
	poolChangeAction    = "change"
	poolUnchangedAction = "unchanged"
	poolRefusedAction   = "refused"
)

type poolsImportResponse struct {
	Applied bool               `json:"applied"`
	Changes []poolImportChange `json:"changes"`
	Summary poolsImportSummary `json:"summary"`
}

type poolsImportSummary struct {
	Create    int `json:"create"`
	Change    int `json:"change"`
	Unchanged int `json:"unchanged"`
	Refused   int `json:"refused"`
	Skipped   int `json:"skipped"`
}

// The document fields an import reads for itself rather than as a pool
// setting, or ignores: an id and an installation id belong to the instance
// that wrote them, and env is never read, because a document that could set
// an environment could be carrying a credential into a file.
var poolDocumentOwnFields = []string{"installation", "installation_id", "id", "env", "env_keys"}

// plannedPool is one pool the import would write.
type plannedPool struct {
	before *store.Pool // nil for a create
	after  *store.Pool
}

// handleImportPools answers POST /api/v1/pools/import.
//
// A dry run plans and reports; a real run refuses the whole document while
// any pool in it is refused, as the settings import does, so an import is one
// change rather than the pools that happened to validate. Pools the document
// does not name are left alone: an import adds and changes, and never deletes.
func (s *Server) handleImportPools(w http.ResponseWriter, r *http.Request) {
	var req poolsImportRequest
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Document) == "" {
		unprocessable(w, "there is nothing to import", []fieldError{{"document", "paste or upload a pools export"}})
		return
	}
	entries, err := parsePoolsImport(req.Document)
	if err != nil {
		unprocessable(w, "that is not a pools document", []fieldError{{"document", err.Error()}})
		return
	}
	insts, err := s.ctrl.Store().ListInstallations(r.Context())
	if err != nil {
		s.internal(w, r, "reading the installations", err)
		return
	}
	targets := map[string]string{}
	for _, i := range insts {
		targets[i.ID] = i.Target
	}

	out := poolsImportResponse{Changes: []poolImportChange{}}
	skip := map[string]bool{}
	for _, name := range req.Skip {
		skip[store.BrandedName(name)] = true
	}
	seen := map[string]bool{}
	var planned []plannedPool
	for i, entry := range entries {
		name, _ := entry["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			out.Changes = append(out.Changes, poolImportChange{Pool: fmt.Sprintf("pool %d", i+1),
				Action: poolRefusedAction, Reason: "every pool in the document needs a name; it is what the import matches pools by",
				Fields: []poolImportField{}, Warnings: []string{}, EnvKeys: []string{}})
			out.Summary.Refused++
			continue
		}
		name = store.BrandedName(name)
		if skip[name] {
			out.Summary.Skipped++
			continue
		}
		ch, plan := s.planPool(r, name, entry, insts, targets)
		if seen[name] {
			ch.Action, ch.Reason, ch.Fields = poolRefusedAction,
				fmt.Sprintf("the document names %s more than once; keep one of them", name), []poolImportField{}
			plan = nil
		}
		seen[name] = true
		switch ch.Action {
		case poolCreateAction:
			out.Summary.Create++
		case poolChangeAction:
			out.Summary.Change++
		case poolUnchangedAction:
			out.Summary.Unchanged++
		case poolRefusedAction:
			out.Summary.Refused++
		}
		if plan != nil && ch.Action != poolUnchangedAction {
			planned = append(planned, *plan)
		}
		out.Changes = append(out.Changes, ch)
	}

	// Before the dry-run return, so a preview says what the real run will:
	// otherwise an operator is shown "create" for pools the ceiling then
	// refuses. It waits for a document with nothing refused in it, so the
	// fixable problems are reported first, as a create reports validation
	// before room.
	if out.Summary.Refused == 0 {
		if err := s.ctrl.AdmitPools(r.Context(), out.Summary.Create); err != nil {
			if !asLimit(w, err) {
				s.internal(w, r, "checking limits.pools", err)
			}
			return
		}
	}
	if req.DryRun {
		writeJSON(w, http.StatusOK, out)
		return
	}
	if out.Summary.Refused > 0 {
		var fields []fieldError
		for _, ch := range out.Changes {
			if ch.Action == poolRefusedAction {
				fields = append(fields, fieldError{ch.Pool, ch.Reason})
			}
		}
		unprocessable(w, "some of the document was refused; skip those pools or fix the document", fields)
		return
	}
	var create, update []*store.Pool
	for _, p := range planned {
		if p.before == nil {
			create = append(create, p.after)
		} else {
			update = append(update, p.after)
		}
	}
	if len(planned) > 0 {
		if err := s.ctrl.Store().ApplyPools(r.Context(), create, update); err != nil {
			s.fail(w, r, "storing the imported pools", err)
			return
		}
	}
	id := Identity(r.Context())
	for _, p := range planned {
		if p.before == nil {
			s.auth.Auditor().Created(r.Context(), id, "pool", p.after.ID, poolForAudit(p.after))
			s.ctrl.PublishPool(r.Context(), events.KindPoolCreated, p.after)
			_, _ = s.ctrl.PrewarmPool(r.Context(), p.after)
			continue
		}
		s.auth.Auditor().Updated(r.Context(), id, "pool", p.after.ID, poolForAudit(p.before), poolForAudit(p.after))
		s.ctrl.PublishPool(r.Context(), events.KindPoolUpdated, p.after)
		// The same test a PATCH makes: only a change to what the runners are
		// made from, or where, is worth pulling an image for.
		b, a := p.before, p.after
		if b.Image != a.Image || b.DockerMode.GivesDaemon() != a.DockerMode.GivesDaemon() ||
			b.PullPolicy != a.PullPolicy || b.Backend != a.Backend || !maps.Equal(b.HostSelector, a.HostSelector) {
			_, _ = s.ctrl.PrewarmPool(r.Context(), a)
		}
	}
	if len(planned) > 0 {
		s.ctrl.Nudge()
	}
	s.auth.Auditor().Act(r.Context(), id, "pools.import", "pool", "pools", map[string]any{
		"created": out.Summary.Create, "changed": out.Summary.Change, "unchanged": out.Summary.Unchanged,
		"skipped": out.Summary.Skipped,
	})
	out.Applied = true
	writeJSON(w, http.StatusOK, out)
}

// planPool works out one document entry's fate through the same fold and the
// same validation a POST or PATCH of that pool would go through, so that the
// import can accept nothing the pools API would refuse.
func (s *Server) planPool(r *http.Request, name string, entry map[string]any,
	insts []*store.Installation, targets map[string]string) (poolImportChange, *plannedPool) {
	ctx := r.Context()
	ch := poolImportChange{Pool: name, Fields: []poolImportField{}, Warnings: []string{}, EnvKeys: []string{}}
	refuse := func(reason string) (poolImportChange, *plannedPool) {
		ch.Action, ch.Reason, ch.Fields = poolRefusedAction, reason, []poolImportField{}
		return ch, nil
	}
	if raw, ok := entry["env_keys"].([]any); ok {
		for _, k := range raw {
			if str, ok := k.(string); ok {
				ch.EnvKeys = append(ch.EnvKeys, str)
			}
		}
	}

	target, _ := entry["installation"].(string)
	target = strings.TrimSpace(target)
	if target == "" {
		return refuse("name the installation this pool belongs to by what it covers, as installation: acme or installation: acme/widgets")
	}
	var inst *store.Installation
	for _, i := range insts {
		if strings.EqualFold(i.Target, target) {
			inst = i
			break
		}
	}
	if inst == nil {
		return refuse(fmt.Sprintf("no installation on this instance covers %s; install the GitHub App there and connect it on the Installations page, or skip this pool", target))
	}

	settings := map[string]any{}
	for k, v := range entry {
		if !slices.Contains(poolDocumentOwnFields, k) {
			settings[k] = v
		}
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return refuse("its settings could not be read: " + err.Error())
	}
	var in poolInput
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return refuse("its settings could not be read: " + err.Error())
	}
	in.InstallationID = &inst.ID
	in.Env = nil

	existing, err := s.ctrl.Store().GetPoolByName(ctx, name)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return refuse("the pool of that name here could not be read: " + err.Error())
	}
	var candidate *store.Pool
	var errs []fieldError
	if existing == nil {
		candidate = s.defaultPool()
		errs = in.apply(candidate)
		defaultNewPoolBurst(&in, candidate)
		// The same default a create through the API gets: an organisation's
		// pool goes in Zoomies' own runner group unless the document says.
		if in.RunnerGroup == nil && inst.TargetType == store.TargetOrg {
			candidate.RunnerGroup = controller.ManagedRunnerGroupName
		}
		errs = append(errs, s.validatePool(ctx, candidate, "")...)
	} else {
		copied := *existing
		candidate = &copied
		errs = in.apply(candidate)
		errs = append(errs, s.validatePool(ctx, candidate, existing.ID)...)
	}
	if len(errs) > 0 {
		parts := make([]string, 0, len(errs))
		for _, e := range errs {
			parts = append(parts, e.Field+": "+e.Message)
		}
		return refuse(strings.Join(parts, "; "))
	}
	// What the store would make of it, so an unchanged pool compares equal to
	// itself rather than to its own un-normalised spelling.
	candidate.Name = store.BrandedName(candidate.Name)
	candidate.Labels = store.NormalizeLabels(candidate.Labels)
	candidate.Platform = candidate.Platform.Normalized()
	if candidate.PullPolicy == "" {
		candidate.PullPolicy = store.PullIfNotPresent
	}

	incoming := documentFields(documentPool(candidate, inst.Target))
	if existing == nil {
		ch.Action = poolCreateAction
		for _, k := range sortedKeys(incoming) {
			ch.Fields = append(ch.Fields, poolImportField{Field: k, Incoming: incoming[k]})
		}
		fit, ferr := s.ctrl.HostFit(ctx, candidate)
		if ferr == nil && fit.Count == 0 && candidate.Enabled {
			// A warning, not a refusal, exactly as the wizard treats a new
			// pool: the host that runs it may be the next thing joined.
			why, _ := noHostWarning(candidate, fit)
			ch.Warnings = append(ch.Warnings, why)
		}
		return ch, &plannedPool{after: candidate}
	}

	current := documentFields(documentPool(existing, targets[existing.InstallationID]))
	for _, k := range sortedKeys(incoming) {
		if !reflect.DeepEqual(current[k], incoming[k]) {
			ch.Fields = append(ch.Fields, poolImportField{Field: k, Current: current[k], Incoming: incoming[k]})
		}
	}
	if len(ch.Fields) == 0 {
		ch.Action = poolUnchangedAction
		return ch, nil
	}
	// The pools API refuses an edit that takes a pool's last host away, and
	// an import is an edit: it gets the same refusal, with skip in place of
	// confirm as the way to go on without it.
	stranded, serr := s.ctrl.PoolStranding(ctx, existing, candidate)
	if serr != nil {
		return refuse("whether any host could still run it could not be checked: " + serr.Error())
	}
	if len(stranded) > 0 {
		st := stranded[0]
		fields := ch.Fields
		ch, _ = refuse(fmt.Sprintf("applying this would leave %s with nowhere to run: %s %s, and no other host in "+
			"this fleet could run it either. Change the document, adjust %s to match, or skip this pool.",
			st.Pool, st.Host, st.Reason, st.Host))
		ch.Fields = fields
		return ch, nil
	}
	ch.Action = poolChangeAction
	return ch, &plannedPool{before: existing, after: candidate}
}

// documentFields is a document pool as the plan compares it: field by field,
// in the JSON the document is written in, less the name it is matched by and
// the environment names it cannot apply.
func documentFields(d poolDocument) map[string]any {
	raw, _ := json.Marshal(d)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	delete(out, "name")
	delete(out, "env_keys")
	return out
}

// parsePoolsImport reads a pools document and returns its entries.
//
// YAML is the parser, because JSON is YAML and a file kept in a repository is
// more likely to be the former. The version field is optional, so a file
// somebody wrote by hand with a pools list and nothing else is read too.
func parsePoolsImport(text string) ([]map[string]any, error) {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, fmt.Errorf("it does not parse as YAML or JSON: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("it is empty")
	}
	if v, ok := doc["export_version"]; ok {
		n, isNum := v.(int)
		if !isNum || n > poolsExportVersion {
			return nil, fmt.Errorf("it is a pools export of version %v, and this build reads version %d", v, poolsExportVersion)
		}
	}
	raw, ok := doc["pools"]
	if !ok {
		if _, settings := doc["settings"]; settings {
			return nil, fmt.Errorf("it is a settings export; import it on the Settings page")
		}
		return nil, fmt.Errorf("it has no pools list")
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("its pools are not a list")
	}
	out := make([]map[string]any, 0, len(list))
	for i, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("pool %d is not an object", i+1)
		}
		out = append(out, entry)
	}
	return out, nil
}
