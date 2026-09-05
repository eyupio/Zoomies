package api

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
)

// poolResponse is the shape GET /pools returns, rendered by the controller so
// the event stream's pool.* frames are the same JSON. See controller/views.go
// for why the renderer lives there.
type poolResponse = controller.PoolView

// handleListPools answers GET /api/v1/pools.
func (s *Server) handleListPools(w http.ResponseWriter, r *http.Request) {
	pools, err := s.ctrl.Store().ListPools(r.Context())
	if err != nil {
		s.internal(w, r, "listing pools", err)
		return
	}
	view, err := s.ctrl.PoolRenderer(r.Context())
	if err != nil {
		s.internal(w, r, "listing pools", err)
		return
	}
	out := make([]poolResponse, 0, len(pools))
	for _, p := range pools {
		out = append(out, view.View(p))
	}
	writeJSON(w, http.StatusOK, newList(out))
}

// handleGetPool answers GET /api/v1/pools/{id}.
func (s *Server) handleGetPool(w http.ResponseWriter, r *http.Request) {
	p, err := s.ctrl.Store().GetPool(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the pool", err)
		return
	}
	view, err := s.ctrl.PoolRenderer(r.Context())
	if err != nil {
		s.internal(w, r, "reading the pool", err)
		return
	}
	writeJSON(w, http.StatusOK, view.View(p))
}

// ---------------------------------------------------------------------------
// Creating and editing
// ---------------------------------------------------------------------------

// poolInput is PoolCreate and PoolUpdate in one type.
//
// Every field is a pointer so that a PATCH can tell "leave this alone" from
// "set it to zero": without that, editing a pool's name would silently reset
// min_runners to 0, which on a pool with warm runners is a fleet-wide change
// nobody asked for.
type poolInput struct {
	Name                   *string            `json:"name"`
	InstallationID         *string            `json:"installation_id"`
	Labels                 *[]string          `json:"labels"`
	RunnerGroup            *string            `json:"runner_group"`
	Backend                *string            `json:"backend"`
	Image                  *string            `json:"image"`
	PullPolicy             *string            `json:"pull_policy"`
	RunnerVersion          *string            `json:"runner_version"`
	MinRunners             *int               `json:"min_runners"`
	MaxRunners             *int               `json:"max_runners"`
	RepositoryScaleUpLimit *int               `json:"repository_scale_up_limit"`
	CostPerRunnerHour      *float64           `json:"cost_per_runner_hour"`
	Priority               *int               `json:"priority"`
	IdleTimeout            *string            `json:"idle_timeout"`
	Ephemeral              *bool              `json:"ephemeral"`
	DockerMode             *string            `json:"docker_mode"`
	Resources              *store.Resources   `json:"resources"`
	Cache                  *store.CacheConfig `json:"cache"`
	HostSelector           *map[string]string `json:"host_selector"`
	Env                    *map[string]string `json:"env"`
	RunAsRoot              *bool              `json:"run_as_root"`
	Enabled                *bool              `json:"enabled"`
}

// defaultPool is a new pool before the request is applied: the defaults the
// OpenAPI document states, so that a minimal create produces the same pool the
// wizard's review step showed.
func (s *Server) defaultPool() *store.Pool {
	return &store.Pool{
		Backend:     store.BackendDocker,
		Image:       s.cfg.GitHub.RunnerImage,
		PullPolicy:  store.PullIfNotPresent,
		MinRunners:  0,
		MaxRunners:  4,
		IdleTimeout: store.Duration(5 * time.Minute),
		Ephemeral:   true,
		DockerMode:  store.DockerNone,
		Cache:       store.CacheConfig{Scope: store.CacheScopePool},
		Enabled:     true,
	}
}

// apply folds the request into a pool, returning the field errors it could not.
//
// Parsing and validation are the same pass on purpose: "idle_timeout: 5 munutes"
// is a validation failure with a field name, not a 400 about JSON.
func (in *poolInput) apply(p *store.Pool) []fieldError {
	var errs []fieldError
	add := func(field, msg string) { errs = append(errs, fieldError{field, msg}) }

	if in.Name != nil {
		// Branded here as well as in the store, so that the uniqueness check
		// below and the error messages that quote the name are talking about
		// the name the pool will actually have.
		p.Name = store.BrandedName(*in.Name)
	}
	if in.InstallationID != nil {
		p.InstallationID = strings.TrimSpace(*in.InstallationID)
	}
	if in.Labels != nil {
		// Every pool answers to the brand as well as to whatever it was given,
		// so that "runs-on: zoomies" reaches this fleet without naming one of
		// its pools. That is the label the migration wizard writes into a
		// repository that has not yet been assigned to a pool, and a pool that
		// quietly dropped it would take no work from those repositories.
		p.Labels = store.BrandLabels(*in.Labels)
	}
	if in.RunnerGroup != nil {
		p.RunnerGroup = strings.TrimSpace(*in.RunnerGroup)
	}
	if in.Backend != nil {
		p.Backend = store.BackendKind(strings.ToLower(strings.TrimSpace(*in.Backend)))
	}
	if in.Image != nil {
		p.Image = strings.TrimSpace(*in.Image)
	}
	if in.PullPolicy != nil {
		p.PullPolicy = store.PullPolicy(strings.ToLower(strings.TrimSpace(*in.PullPolicy)))
	}
	if in.RunnerVersion != nil {
		p.RunnerVersion = strings.TrimSpace(*in.RunnerVersion)
	}
	if in.MinRunners != nil {
		p.MinRunners = *in.MinRunners
	}
	if in.MaxRunners != nil {
		p.MaxRunners = *in.MaxRunners
	}
	if in.RepositoryScaleUpLimit != nil {
		p.RepositoryScaleUpLimit = *in.RepositoryScaleUpLimit
	}
	if in.CostPerRunnerHour != nil {
		p.CostPerRunnerHour = in.CostPerRunnerHour
	}
	if in.Priority != nil {
		p.Priority = *in.Priority
	}
	if in.IdleTimeout != nil {
		raw := strings.TrimSpace(*in.IdleTimeout)
		switch {
		case raw == "":
			p.IdleTimeout = 0
		default:
			d, err := time.ParseDuration(raw)
			switch {
			case err != nil:
				add("idle_timeout", fmt.Sprintf("%q is not a duration; write it like 5m, 30s or 1h30m", raw))
			case d < 0:
				add("idle_timeout", "an idle timeout cannot be negative; use 0 to keep idle runners until something else removes them")
			default:
				p.IdleTimeout = store.Duration(d)
			}
		}
	}
	if in.Ephemeral != nil {
		p.Ephemeral = *in.Ephemeral
	}
	if in.DockerMode != nil {
		p.DockerMode = store.DockerMode(strings.ToLower(strings.TrimSpace(*in.DockerMode)))
	}
	if in.Resources != nil {
		p.Resources = *in.Resources
	}
	if in.Cache != nil {
		p.Cache = *in.Cache
		p.Cache.Source = strings.TrimSpace(p.Cache.Source)
	}
	if in.HostSelector != nil {
		p.HostSelector = store.StringMap(*in.HostSelector)
	}
	if in.Env != nil {
		p.Env = store.StringMap(*in.Env)
	}
	if in.RunAsRoot != nil {
		p.RunAsRoot = *in.RunAsRoot
	}
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}
	return errs
}

// validatePool checks a pool the way the creation wizard does, and for the same
// reasons, so that the review step and the server never disagree.
func (s *Server) validatePool(ctx context.Context, p *store.Pool, existingID string) []fieldError {
	var errs []fieldError
	add := func(field, msg string) { errs = append(errs, fieldError{field, msg}) }

	switch {
	case p.Name == "":
		add("name", "a pool needs a name; it is how you will refer to it in the UI and on the CLI")
	case len([]rune(p.Name)) > 64:
		add("name", "a pool name must be 64 characters or fewer")
	default:
		if existing, err := s.ctrl.Store().GetPoolByName(ctx, p.Name); err == nil && existing.ID != existingID {
			add("name", fmt.Sprintf("a pool called %q already exists; pick another name", p.Name))
		}
	}

	if p.InstallationID == "" {
		add("installation_id", "a pool has to belong to a GitHub App installation; connect one on the Installations page first")
	} else if _, err := s.ctrl.Store().GetInstallation(ctx, p.InstallationID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			add("installation_id", fmt.Sprintf("there is no installation %s; it may have been removed since this form was opened", p.InstallationID))
		} else {
			add("installation_id", "the installation could not be read: "+err.Error())
		}
	}

	switch {
	case len(p.Labels) == 0:
		add("labels", "a pool needs at least one label your workflows can ask for")
	case !hasDistinctiveLabel(p.Labels):
		add("labels", fmt.Sprintf("a pool needs at least one label of its own: %q is on every Zoomies pool and the rest are labels every runner advertises anyway, so nothing would ever select this pool in particular. Try %q.",
			store.BrandLabel, store.BrandedLabel(p.Name)))
	}
	for _, l := range p.Labels {
		if strings.ContainsAny(l, " ,") {
			add("labels", fmt.Sprintf("%q contains a space or a comma; GitHub labels may not", l))
			break
		}
	}

	if !p.Backend.Valid() {
		add("backend", fmt.Sprintf("%q is not a backend; use docker, podman or process", p.Backend))
	}
	if !p.DockerMode.Valid() {
		add("docker_mode", fmt.Sprintf("%q is not a docker mode; use none, dind or host-socket", p.DockerMode))
	}
	if p.Backend == store.BackendProcess && p.DockerMode != store.DockerNone && p.DockerMode != "" {
		add("docker_mode", "the process backend runs jobs directly on the host, so it cannot give them a Docker daemon of their own; use the docker or podman backend, or set docker_mode to none")
	}
	if p.Image == "" && p.Backend != store.BackendProcess {
		add("image", "a container backend needs a runner image; leave it blank only for the process backend")
	}
	if !p.PullPolicy.Valid() {
		add("pull_policy", "use if-not-present, always, or pinned-only")
	}
	if p.PullPolicy == store.PullPinnedOnly && !digestReference(p.Image) {
		add("image", "pinned-only requires an immutable digest reference such as image@sha256:…; mutable tags are rejected")
	}

	if p.MinRunners < 0 {
		add("min_runners", "the minimum cannot be negative")
	}
	if p.RepositoryScaleUpLimit < 0 {
		add("repository_scale_up_limit", "must be zero or greater")
	}
	if p.CostPerRunnerHour != nil && *p.CostPerRunnerHour < 0 {
		add("cost_per_runner_hour", "must be zero or greater")
	}
	if p.MaxRunners < 1 {
		add("max_runners", "a pool that may have no runners can never run a job; set at least 1")
	}
	if p.MinRunners > p.MaxRunners {
		add("min_runners", fmt.Sprintf("the minimum (%d) is above the maximum (%d); warm runners cannot exceed the cap", p.MinRunners, p.MaxRunners))
	}
	if p.Ephemeral && p.MinRunners > 0 && p.IdleTimeout.Duration() == 0 {
		add("idle_timeout", "warm ephemeral runners with no idle timeout are replaced after every job and never reaped; give an idle timeout, or set min_runners to 0")
	}

	if p.Resources.CPUs < 0 {
		add("resources.cpus", "a CPU limit cannot be negative; use 0 for no limit")
	}
	if p.Resources.MemoryMB < 0 {
		add("resources.memory_mb", "a memory limit cannot be negative; use 0 for no limit")
	}
	if p.Resources.DiskGB < 0 {
		add("resources.disk_gb", "a disk limit cannot be negative; use 0 for no limit")
	}
	if p.Resources.PidsLimit < 0 {
		add("resources.pids_limit", "a process limit cannot be negative; use 0 for no limit")
	}
	if p.Cache.Enabled {
		if !p.Cache.Scope.Valid() {
			add("cache.scope", "use pool or repository")
		}
		if p.Cache.SizeLimit < 0 {
			add("cache.size_limit", "the advisory cache size cannot be negative; use 0 to give no figure")
		}
		if strings.Contains(p.Cache.Source, "..") {
			add("cache.source", "path traversal is not allowed")
		}
		if p.Cache.Scope == store.CacheScopeRepository {
			if inst, err := s.ctrl.Store().GetInstallation(ctx, p.InstallationID); err == nil && inst.TargetType != store.TargetRepo {
				add("cache.scope", "repository scope needs a repository-targeted installation: a runner registered to an organisation can be given any repository's job, so a repository cache would be shared between repositories; use pool scope, or install the App on the repository itself")
			}
		}
	}
	for k := range p.Env {
		if strings.TrimSpace(k) == "" {
			add("env", "an environment variable needs a name")
			break
		}
	}
	for k := range p.HostSelector {
		if strings.TrimSpace(k) == "" {
			add("host_selector", "a host selector key cannot be empty")
			break
		}
	}
	return errs
}

func digestReference(ref string) bool {
	parts := strings.Split(ref, "@sha256:")
	if len(parts) != 2 || parts[0] == "" || len(parts[1]) != 64 {
		return false
	}
	for _, c := range parts[1] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// hasDistinctiveLabel reports whether a pool advertises anything beyond the
// labels every actions/runner binary advertises anyway. A pool of only implicit
// labels would match every job in the organisation, which is never what an
// operator meant.
func hasDistinctiveLabel(labels []string) bool {
	return slices.ContainsFunc(labels, func(l string) bool {
		l = store.NormalizeLabel(l)
		// The brand is on every pool, so it distinguishes this one from the
		// others no better than "self-hosted" does. A fleet reached only by
		// "runs-on: zoomies" is a fleet where no workflow can say which pool
		// it meant.
		return !store.ImplicitLabels[l] && l != store.BrandLabel
	})
}

// hostFit is what the fleet says about a pool that does not exist yet: how many
// hosts could run it, why the ones that cannot say they cannot, and which
// backends they offer instead.
type hostFit struct {
	count  int
	detail string
	// alternatives are the backends offered by the hosts that match this pool
	// in every way except its backend, in the order a pool would move to them.
	// The wizard turns them into the second half of its warning, in the same
	// words the scheduler uses once the pool is real.
	alternatives []string
}

// matchingHosts counts the hosts that could actually run this pool, and returns
// the first host's explanation of why it cannot when one is to be had.
//
// Zero is worth saying out loud before a pool is created: a pool whose selector
// matches nothing looks completely healthy and never starts a runner. The
// explanation matters as much as the count, because the usual cause is not a
// missing machine but a daemon the agent on an existing one could not reach --
// and the backends those same hosts do offer are the other way out.
func (s *Server) matchingHosts(ctx context.Context, p *store.Pool) (hostFit, error) {
	hosts, err := s.ctrl.Store().ListHosts(ctx)
	if err != nil {
		return hostFit{}, err
	}
	now := s.ctrl.Now()
	fit := hostFit{}
	offered := map[string]int{}
	for _, h := range hosts {
		if !h.Healthy(now) || h.Cordoned || !selectorMatches(p.HostSelector, h.Labels) {
			continue
		}
		for _, kind := range h.Backends {
			if kind != string(p.Backend) {
				offered[kind]++
			}
		}
		if !slices.Contains(h.Backends, string(p.Backend)) {
			if fit.detail == "" {
				if info, ok := h.BackendInfo.Find(p.Backend); ok && !info.Available && info.Detail != "" {
					fit.detail = h.Name + " reports: " + info.Detail
				}
			}
			continue
		}
		fit.count++
	}
	for _, kind := range []store.BackendKind{store.BackendDocker, store.BackendPodman, store.BackendProcess} {
		if offered[string(kind)] > 0 {
			fit.alternatives = append(fit.alternatives, string(kind))
		}
	}
	return fit, nil
}

// selectorMatches is the scheduler's host-selector rule, which is deliberately
// simple: every key and value in the selector must be present on the host, and
// an empty selector matches everything.
func selectorMatches(selector, labels store.StringMap) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// handleCreatePool answers POST /api/v1/pools.
func (s *Server) handleCreatePool(w http.ResponseWriter, r *http.Request) {
	var in poolInput
	if !decode(w, r, &in) {
		return
	}
	p := s.defaultPool()
	errs := in.apply(p)
	errs = append(errs, s.validatePool(r.Context(), p, "")...)
	if len(errs) > 0 {
		unprocessable(w, "this pool cannot be created as described", errs)
		return
	}

	if err := s.ctrl.Store().CreatePool(r.Context(), p); err != nil {
		s.fail(w, r, "creating the pool", err)
		return
	}

	s.auth.Auditor().Created(r.Context(), Identity(r.Context()), "pool", p.ID, p)
	s.ctrl.PublishPool(r.Context(), events.KindPoolCreated, p)
	// A new pool with a minimum above zero has runners to create; a new pool
	// with none may still claim jobs that are queued right now.
	s.ctrl.Nudge()
	_, _ = s.ctrl.PrewarmPool(r.Context(), p)

	view, err := s.ctrl.PoolRenderer(r.Context())
	if err != nil {
		s.internal(w, r, "reading the pool back", err)
		return
	}
	writeJSON(w, http.StatusCreated, view.View(p))
}

// validatePoolResponse is the wizard's review step: the errors that would stop
// the pool being created, the warnings it would produce, and how many hosts
// could actually run it.
type validatePoolResponse struct {
	Valid         bool                 `json:"valid"`
	Errors        []fieldError         `json:"errors"`
	Warnings      []controller.Problem `json:"warnings"`
	MatchingHosts int                  `json:"matching_hosts"`
}

// handleValidatePool answers POST /api/v1/pools/validate. It creates nothing.
func (s *Server) handleValidatePool(w http.ResponseWriter, r *http.Request) {
	var in poolInput
	if !decode(w, r, &in) {
		return
	}
	p := s.defaultPool()
	errs := in.apply(p)
	errs = append(errs, s.validatePool(r.Context(), p, "")...)

	fit, err := s.matchingHosts(r.Context(), p)
	if err != nil {
		s.internal(w, r, "counting the hosts that could run this pool", err)
		return
	}
	warnings := controller.PoolWarnings(p)
	if fit.count == 0 {
		why := fmt.Sprintf("no healthy, uncordoned host offers the %s backend and matches this pool's host selector, "+
			"so every runner it asks for would wait for a host that does not exist.", p.Backend)
		fix := "add a host with that backend, uncordon one, or relax the host selector."
		if detail := fit.detail; detail != "" {
			// A host is there and its agent already said what is wrong with it,
			// which is a much shorter route to a working pool than adding a
			// machine.
			why += " " + detail
			fix = fmt.Sprintf("make the %s backend usable on that host%s.", p.Backend, switchTo(fit.alternatives))
		}
		warnings = append(warnings, controller.Problem{
			Code:         "pool.no_matching_hosts",
			Severity:     config.SeverityWarning,
			Title:        "no host can run this pool as configured",
			Detail:       why,
			Fix:          fix,
			Alternatives: fit.alternatives,
		})
	}
	if errs == nil {
		errs = []fieldError{}
	}
	if warnings == nil {
		warnings = []controller.Problem{}
	}
	writeJSON(w, http.StatusOK, validatePoolResponse{
		Valid:         len(errs) == 0,
		Errors:        errs,
		Warnings:      warnings,
		MatchingHosts: fit.count,
	})
}

// switchTo names the backends a pool could move to instead, or says plainly
// that there are none. It is the wizard's half of the sentence the scheduler
// writes for a pool that already exists, kept in the same words on purpose:
// the warning before creation and the problem after it are the same fact.
func switchTo(alternatives []string) string {
	switch len(alternatives) {
	case 0:
		return "; your hosts offer no other backend either"
	case 1:
		return ", or point this pool at " + alternatives[0] + ", which they already offer"
	default:
		return ", or point this pool at a backend they already offer: " + strings.Join(alternatives, ", ")
	}
}

// handleUpdatePool answers PATCH /api/v1/pools/{id}.
func (s *Server) handleUpdatePool(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	existing, err := s.ctrl.Store().GetPool(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the pool", err)
		return
	}

	var in poolInput
	if !decode(w, r, &in) {
		return
	}
	before := *existing
	updated := *existing
	errs := in.apply(&updated)
	errs = append(errs, s.validatePool(r.Context(), &updated, id)...)
	if len(errs) > 0 {
		unprocessable(w, "this pool cannot be changed as described", errs)
		return
	}

	if err := s.ctrl.Store().UpdatePool(r.Context(), &updated); err != nil {
		s.fail(w, r, "saving the pool", err)
		return
	}

	s.auth.Auditor().Updated(r.Context(), Identity(r.Context()), "pool", id, &before, &updated)
	s.ctrl.PublishPool(r.Context(), events.KindPoolUpdated, &updated)
	// The pool's shape decides how many runners should exist, so the scheduler
	// should look again rather than wait out its interval.
	s.ctrl.Nudge()
	if before.Image != updated.Image || before.PullPolicy != updated.PullPolicy || before.Backend != updated.Backend || !maps.Equal(before.HostSelector, updated.HostSelector) {
		_, _ = s.ctrl.PrewarmPool(r.Context(), &updated)
	}

	view, verr := s.ctrl.PoolRenderer(r.Context())
	if verr != nil {
		s.internal(w, r, "reading the pool back", verr)
		return
	}
	writeJSON(w, http.StatusOK, view.View(&updated))
}

func (s *Server) handlePrewarmPool(w http.ResponseWriter, r *http.Request) {
	p, err := s.ctrl.Store().GetPool(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the pool", err)
		return
	}
	n, err := s.ctrl.PrewarmPool(r.Context(), p)
	if err != nil {
		unprocessable(w, err.Error(), nil)
		return
	}
	states, err := s.ctrl.Store().ListPoolPrewarms(r.Context(), p.ID)
	if err != nil {
		s.internal(w, r, "reading prewarm state", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": n, "hosts": states})
}

// deletePoolResponse says how much of the fleet the deletion took with it.
type deletePoolResponse struct {
	RunnersAffected int `json:"runners_affected"`
}

// handleDeletePool answers DELETE /api/v1/pools/{id}.
//
// Draining is the default and the only safe choice while work is in flight: the
// runners finish their current job and then go. force tears them down now,
// interrupting whatever they were running, which is sometimes exactly what an
// operator wants and is never what they should get by accident.
func (s *Server) handleDeletePool(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	force := queryBool(r, "force", false)
	drain := queryBool(r, "drain", true)

	p, err := s.ctrl.Store().GetPool(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the pool", err)
		return
	}
	runners, err := s.ctrl.Store().ListRunnersForPool(r.Context(), id)
	if err != nil {
		s.internal(w, r, "listing the pool's runners", err)
		return
	}

	affected := 0
	for _, run := range runners {
		if run.State.Terminal() {
			continue
		}
		var rerr error
		_, rerr = s.ctrl.RemoveRunner(r.Context(), run.ID, "pool "+p.Name+" was deleted", force || !drain)
		if rerr != nil {
			// One runner that cannot be told to stop must not leave the pool
			// half-deleted; the row goes either way and the reaper cleans up.
			s.logger(r).Warn("could not stop a runner while deleting its pool",
				"pool", id, "runner", run.ID, "error", rerr)
			continue
		}
		affected++
	}

	if err := s.ctrl.Store().DeletePool(r.Context(), id); err != nil {
		s.fail(w, r, "deleting the pool", err)
		return
	}
	s.auth.Auditor().Deleted(r.Context(), Identity(r.Context()), "pool", id, p)
	s.ctrl.PublishPoolDeleted(id)
	s.ctrl.Nudge()
	writeJSON(w, http.StatusOK, deletePoolResponse{RunnersAffected: affected})
}

// handleEnablePool and handleDisablePool are the two halves of the switch on
// the pool page. Disabling never interrupts a running job: the scheduler stops
// creating runners and drains the idle ones.
func (s *Server) handleEnablePool(w http.ResponseWriter, r *http.Request) {
	s.setPoolEnabled(w, r, true)
}

func (s *Server) handleDisablePool(w http.ResponseWriter, r *http.Request) {
	s.setPoolEnabled(w, r, false)
}

func (s *Server) setPoolEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	id := chiURLParam(r, "id")
	p, err := s.ctrl.Store().GetPool(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the pool", err)
		return
	}
	if p.Enabled != enabled {
		before := *p
		p.Enabled = enabled
		if err := s.ctrl.Store().UpdatePool(r.Context(), p); err != nil {
			s.fail(w, r, "saving the pool", err)
			return
		}
		action := "pool.disable"
		if enabled {
			action = "pool.enable"
		}
		s.auth.Auditor().Act(r.Context(), Identity(r.Context()), action, "pool", id, map[string]any{
			"name": p.Name, "enabled": enabled, "was": before.Enabled,
		})
		s.ctrl.PublishPool(r.Context(), events.KindPoolUpdated, p)
		s.ctrl.Nudge()
	}

	view, verr := s.ctrl.PoolRenderer(r.Context())
	if verr != nil {
		s.internal(w, r, "reading the pool back", verr)
		return
	}
	writeJSON(w, http.StatusOK, view.View(p))
}

// emptySlice and emptyMap keep a JSON response from carrying null where the UI
// expects a collection.
func emptySlice[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}

func emptyMap(in store.StringMap) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	return in
}
