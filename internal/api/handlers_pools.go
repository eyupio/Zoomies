package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/naming"
	"github.com/eyupio/zoomies/internal/store"
)

// poolResponse is the shape GET /pools returns, rendered by the controller so
// the event stream's pool.* frames are the same JSON. See controller/views.go
// for why the renderer lives there.
type poolResponse = controller.PoolView

// poolFor renders a pool for the caller in front of it.
//
// Everything on the view is a viewer's to see except the environment a pool
// injects into its runners, which is where a registry password ends up if one
// is anywhere. Setting a pool's env is an operator action, so reading the
// values back is one too; a viewer gets the keys, which is all the pool page
// draws.
func poolFor(r *http.Request, v controller.PoolView) controller.PoolView {
	if auth.Allowed(Identity(r.Context()), auth.ActionPoolsWrite) {
		return v
	}
	return v.WithoutEnvValues()
}

// poolForAudit returns the pool as an audit row should record it: every env key
// kept, every env value gone.
//
// auth.Redact cannot do this on its own. It matches names, so it blanks
// GITHUB_TOKEN and leaves DEPLOY_KEY, NEXUS_PW and REGISTRY_AUTH untouched --
// an operator names their own variables, and a name is not a reliable signal of
// what stands behind it. So the values come out here, before the row is
// written, rather than being trusted to a word list.
//
// Unlike poolFor this is not role-based, and that is the point. A response is
// rendered for one caller and gone; an audit row is kept for the life of the
// database, is never pruned, and is readable at viewer -- so a secret written
// into one is readable by every viewer from then on, and no later fix can
// retract it. The keys stay, because which variable changed is what the row is
// read for.
func poolForAudit(p *store.Pool) *store.Pool {
	if p == nil || len(p.Env) == 0 {
		return p
	}
	blanked := *p
	blanked.Env = make(store.StringMap, len(p.Env))
	for k := range p.Env {
		blanked.Env[k] = ""
	}
	return &blanked
}

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
		out = append(out, poolFor(r, view.View(p)))
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
	writeJSON(w, http.StatusOK, poolFor(r, view.View(p)))
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
	Name                   *string               `json:"name"`
	InstallationID         *string               `json:"installation_id"`
	Labels                 *[]string             `json:"labels"`
	RunnerGroup            *string               `json:"runner_group"`
	Backend                *string               `json:"backend"`
	Platform               *store.Platform       `json:"platform"`
	Image                  *string               `json:"image"`
	PullPolicy             *string               `json:"pull_policy"`
	RunnerVersion          *string               `json:"runner_version"`
	MinRunners             *int                  `json:"min_runners"`
	MaxRunners             *int                  `json:"max_runners"`
	RepositoryScaleUpLimit *int                  `json:"repository_scale_up_limit"`
	CostPerRunnerHour      *float64              `json:"cost_per_runner_hour"`
	Priority               *int                  `json:"priority"`
	IdleTimeout            *string               `json:"idle_timeout"`
	Ephemeral              *bool                 `json:"ephemeral"`
	DockerMode             *string               `json:"docker_mode"`
	Resources              *store.Resources      `json:"resources"`
	CPUBurst               *store.CPUBurstPolicy `json:"cpu_burst"`
	RunnerSettings         *runnerSettingsIn     `json:"runner_settings"`
	Cache                  *store.CacheConfig    `json:"cache"`
	Tmpfs                  *store.TmpfsConfig    `json:"tmpfs"`
	HostSelector           *map[string]string    `json:"host_selector"`
	Env                    *map[string]string    `json:"env"`
	RunAsRoot              *bool                 `json:"run_as_root"`
	NoDefaultLabels        *bool                 `json:"no_default_labels"`
	SizeFromProfile        *bool                 `json:"size_from_profile"`
	Enabled                *bool                 `json:"enabled"`
	// Auto is what an operator asks of a pool the controller keeps, and is
	// refused on any other.
	Auto *autoPoolInput `json:"auto"`
}

// autoPoolInput is the operator's side of a pool the controller keeps: runners to
// keep ready, the most runners to allow however many slots the hosts give, and a
// pause. A warm count above the cap is held to it.
type autoPoolInput struct {
	Warm   *int  `json:"warm"`
	Cap    *int  `json:"cap"`
	Paused *bool `json:"paused"`
}

// optionalDuration is one duration field of a PATCH body, with the three
// states an operator can express kept apart.
//
// encoding/json cannot tell an absent field from an explicit null on its own:
// both leave a pointer field nil, whatever depth of pointer it is given. So
// presence is recorded here, where UnmarshalJSON is called for a null and is
// not called at all for an absent field -- which is what makes "stop
// overriding this" expressible without also re-sending every other setting.
type optionalDuration struct {
	// present is true when the key was in the body at all.
	present bool
	// value is nil for an explicit null, which hands the setting back to the
	// fleet.
	value *string
}

func (o *optionalDuration) UnmarshalJSON(b []byte) error {
	o.present = true
	if string(b) == "null" {
		o.value = nil
		return nil
	}
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.value = &v
	return nil
}

// runnerSettingsIn is a pool's runner overrides as they arrive: a duration
// string per field, and a null or absent field meaning two different things.
//
// Absent leaves whatever the pool already had; an explicit null clears the
// override and hands the setting back to the fleet; a string sets it,
// including "0", which is a real answer -- "never fail a runner of this pool
// for taking too long to register" is what a pool on a slow air-gapped
// registry means.
type runnerSettingsIn struct {
	ProvisionTimeout  optionalDuration `json:"provision_timeout"`
	DrainTimeout      optionalDuration `json:"drain_timeout"`
	MaxRunnerLifetime optionalDuration `json:"max_runner_lifetime"`
	ScaleUpDelay      optionalDuration `json:"scale_up_delay"`
	DockerWait        optionalDuration `json:"docker_wait"`
}

// apply folds the overrides that arrived into the pool's, reporting the
// durations it could not read. The field names it reports are the ones the
// request used, so the wizard can put the message under the input that
// carried it.
func (in *runnerSettingsIn) apply(rs *store.RunnerSettings, add func(field, msg string)) {
	each := []struct {
		name string
		in   optionalDuration
		out  **store.Duration
		what string
	}{
		{"provision_timeout", in.ProvisionTimeout, &rs.ProvisionTimeout,
			"use 0 to let a runner of this pool take as long as it needs to register"},
		{"drain_timeout", in.DrainTimeout, &rs.DrainTimeout,
			"use 0 to leave a drain of this pool unbounded"},
		{"max_runner_lifetime", in.MaxRunnerLifetime, &rs.MaxRunnerLifetime,
			"use 0 to let a runner of this pool live until something else removes it"},
		{"scale_up_delay", in.ScaleUpDelay, &rs.ScaleUpDelay,
			"use 0 to scale this pool the moment a job is queued"},
		{"docker_wait", in.DockerWait, &rs.DockerWait,
			"use 0 to leave the runner image's own wait in place"},
	}
	for _, f := range each {
		if !f.in.present {
			continue // absent: whatever the pool already said stands
		}
		if f.in.value == nil {
			*f.out = nil // null: hand this setting back to the fleet
			continue
		}
		raw := strings.TrimSpace(*f.in.value)
		// An empty string is the HTML form's way of saying null: an operator
		// who clears the input has stopped overriding the setting.
		if raw == "" {
			*f.out = nil
			continue
		}
		d, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			add("runner_settings."+f.name, fmt.Sprintf("%q is not a duration; write it like 5m, 30s or 1h30m", raw))
		case d < 0:
			add("runner_settings."+f.name, "this cannot be negative; "+f.what)
		default:
			v := store.Duration(d)
			*f.out = &v
		}
	}
}

// defaultPool is a new pool before the request is applied: the defaults the
// OpenAPI document states, so that a minimal create produces the same pool the
// wizard's review step showed.
func (s *Server) defaultPool() *store.Pool {
	return &store.Pool{
		Backend: store.BackendDocker,
		// No image: a pool that names none follows its platform, and a pool
		// with no platform follows the instance default. Pinning the default
		// here would freeze every pool on whatever the default was the day it
		// was created.
		Image:       "",
		PullPolicy:  store.PullIfNotPresent,
		MinRunners:  0,
		MaxRunners:  4,
		IdleTimeout: store.Duration(5 * time.Minute),
		Ephemeral:   true,
		DockerMode:  store.DockerNone,
		Cache:       store.CacheConfig{Scope: store.CacheScopePool},
		Enabled:     true,
		// No size: a pool that names none is sized by the host it lands on,
		// one slot's share of that machine, charged and enforced as such.
		// That is the shape a pool has unless somebody chooses otherwise --
		// see automaticSizing -- and it is the one that keeps fitting when a
		// bigger machine joins the fleet.
		Resources: store.Resources{},
		CPUBurst:  store.CPUBurstPolicy{Mode: store.CPUBurstOff},
	}
}

// defaultNewPoolBurst enables the safe, metrics-only stage only when a new
// pool actually kept automatic sizing. A create request that supplies fixed
// resources must remain valid without also knowing to turn elasticity off.
func defaultNewPoolBurst(in *poolInput, p *store.Pool) {
	if in.CPUBurst == nil && p.Automatic() && (p.Backend == store.BackendDocker || p.Backend == store.BackendPodman) {
		p.CPUBurst.Mode = store.CPUBurstObserve
	}
}

// defaultResources is the size the wizard's sliders open on when an operator
// chooses to set one: the fleet's own figures (runners.default_cpus,
// runners.default_memory_mb), so that a fleet of small boxes or of compilers
// says so once rather than on every pool it creates.
//
// It is an opening value and no longer a floor. A pool that names no size is
// sized by the host it lands on instead, which is both charged
// (scheduler.Reserve) and enforced as a real cgroup limit
// (scheduler.Allocation) -- so "no limit" is not what an unsized pool means,
// and has not been since default runner limits existed.
func (s *Server) defaultResources() store.Resources {
	r := s.cfg().Runners
	cpus, memoryMB := r.DefaultRunnerSize()
	out := store.Resources{CPUs: cpus, MemoryMB: memoryMB}
	// A minimum only opens the slider where it sits under the standard; one
	// at or above it would be refused the moment the pool was saved.
	if r.MinimumCPUs > 0 && r.MinimumCPUs < cpus {
		out.MinCPUs = r.MinimumCPUs
	}
	if r.MinimumMemoryMB > 0 && r.MinimumMemoryMB < memoryMB {
		out.MinMemoryMB = r.MinimumMemoryMB
	}
	return out
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
	if in.Platform != nil {
		// Keep what the operator sent rather than the normalised form: an
		// operating system Zoomies does not know has to survive as far as the
		// validator, which can then say so by name.
		p.Platform = store.Platform{
			OS:        strings.ToLower(strings.TrimSpace(in.Platform.OS)),
			OSVersion: strings.TrimSpace(in.Platform.OSVersion),
			Arch:      strings.ToLower(strings.TrimSpace(in.Platform.Arch)),
		}
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
	if in.CPUBurst != nil {
		p.CPUBurst = *in.CPUBurst
		p.CPUBurst.Mode = store.CPUBurstMode(strings.ToLower(strings.TrimSpace(string(p.CPUBurst.Mode))))
	}
	if in.RunnerSettings != nil {
		in.RunnerSettings.apply(&p.RunnerSettings, add)
	}
	if in.Cache != nil {
		p.Cache = *in.Cache
		p.Cache.Source = strings.TrimSpace(p.Cache.Source)
	}
	if in.Tmpfs != nil {
		p.Tmpfs = *in.Tmpfs
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
	if in.NoDefaultLabels != nil {
		p.NoDefaultLabels = *in.NoDefaultLabels
	}
	if in.SizeFromProfile != nil {
		p.SizeFromProfile = *in.SizeFromProfile
	}
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}
	// The image the pool will actually run. A pool that gives its jobs a daemon
	// cannot use the stock image, which has no client for it, so the stock
	// image is swapped for its Docker variant here, as the request is folded
	// in, rather than left for the operator to remember: the response, the
	// audit row and the pool's page then all show the image that runs, and the
	// wizard's dry run agrees with the create it precedes. What is and is not
	// swapped is config.RunnerImageFor's to say.
	p.Image = config.RunnerImageFor(p.Image, p.DockerMode.GivesDaemon())
	return errs
}

// platformOption is one row of the runner image catalogue, as the pool wizard
// needs it: what to select, what to call it, and which architectures it can be
// asked for.
type platformOption struct {
	OS        string   `json:"os"`
	OSVersion string   `json:"os_version"`
	Label     string   `json:"label"`
	Image     string   `json:"image"`
	Arches    []string `json:"arches"`
	Default   bool     `json:"default"`
}

// poolDefaultsResponse is what a pool that says nothing is, so that a form can
// open on the same figures the server would have applied.
//
// It carries two sizes because there are two questions, and answering both
// with one number is what used to make the wizard show an operator a pool
// they were not creating. Resources is what a new pool actually is -- empty,
// meaning its runners are sized by the host they land on. SuggestedResources
// is where the sliders open for an operator who chooses to set a size
// instead: the fleet's own figures, so a fleet of small boxes or of compilers
// says so once rather than on every pool. The same figures answer "how big is
// a runner in this fleet" for the Hosts page, which sizes a machine against
// them before any pool has been created at all.
type poolDefaultsResponse struct {
	Resources store.Resources `json:"resources"`
	// SuggestedResources is the size the sliders open on, never what an
	// unspecified pool becomes.
	SuggestedResources store.Resources `json:"suggested_resources"`
	// MinRunners, MaxRunners and IdleTimeout are the rest of what a new pool
	// starts as, so the wizard has one source for its starting point.
	MinRunners  int    `json:"min_runners"`
	MaxRunners  int    `json:"max_runners"`
	IdleTimeout string `json:"idle_timeout"`
	// RunnerSettings is what the fleet's own runner timings are, so a form
	// offering to override one can say what it is overriding. They are here
	// rather than read from /settings because creating a pool is an operator
	// action and reading the settings page is an administrator's: a wizard
	// that had to ask for both would show the operator who may create pools
	// an empty box where the fleet's answer should be.
	RunnerSettings poolDefaultTimings `json:"runner_settings"`
}

// poolDefaultTimings is the fleet's answer for each setting a pool may
// override, as the text an operator would write.
type poolDefaultTimings struct {
	ProvisionTimeout  string `json:"provision_timeout"`
	DrainTimeout      string `json:"drain_timeout"`
	MaxRunnerLifetime string `json:"max_runner_lifetime"`
	ScaleUpDelay      string `json:"scale_up_delay"`
	// DockerWait is the wait that actually happens rather than the one that
	// was configured: zero in the setting means the runner image chooses, and
	// the image waits two minutes.
	DockerWait string `json:"docker_wait"`
}

// handlePoolDefaults answers GET /api/v1/pools/defaults.
func (s *Server) handlePoolDefaults(w http.ResponseWriter, _ *http.Request) {
	p := s.defaultPool()
	cfg := s.cfg()
	writeJSON(w, http.StatusOK, poolDefaultsResponse{
		Resources:          p.Resources,
		SuggestedResources: s.defaultResources(),
		MinRunners:         p.MinRunners,
		MaxRunners:         p.MaxRunners,
		IdleTimeout:        p.IdleTimeout.String(),
		RunnerSettings: poolDefaultTimings{
			ProvisionTimeout:  cfg.Scheduler.ProvisionTimeout.String(),
			DrainTimeout:      cfg.Scheduler.DrainTimeout.String(),
			MaxRunnerLifetime: cfg.Scheduler.MaxRunnerLifetime.String(),
			ScaleUpDelay:      cfg.Scheduler.ScaleUpDelay.String(),
			DockerWait:        cfg.Runners.EffectiveDockerWait().String(),
		},
	})
}

// handlePoolPlatforms answers GET /api/v1/pools/platforms.
//
// The catalogue is compiled into the binary, so this is a constant list. It is
// served rather than duplicated in the UI because a dropdown offering an
// operating system no image is published for is a pool that validates and then
// never starts a runner.
func (s *Server) handlePoolPlatforms(w http.ResponseWriter, _ *http.Request) {
	images := naming.Images()
	out := make([]platformOption, 0, len(images))
	for _, img := range images {
		out = append(out, platformOption{
			OS:        img.OS,
			OSVersion: img.Version,
			Label:     naming.PrettyOS(img.OS) + " " + img.Version,
			Image:     img.Ref(),
			Arches:    img.Arches,
			Default:   img.Default,
		})
	}
	writeJSON(w, http.StatusOK, newList(out))
}

// validatePlatform checks the machine a pool asks for.
//
// The rule it enforces is that a pool must be startable: either it names an
// image, or the platform it names is one Zoomies publishes an image for. A
// pool that asks for Ubuntu 20.04 and no image would otherwise be accepted and
// then fail at every create, which is a much later and much worse place to
// find out.
func validatePlatform(p *store.Pool) []fieldError {
	var errs []fieldError
	add := func(field, msg string) { errs = append(errs, fieldError{field, msg}) }

	if p.Platform.OS != "" && naming.NormalizeOS(p.Platform.OS) == "" {
		add("platform.os", fmt.Sprintf("%q is not an operating system Zoomies knows; use one of %s, or leave it blank and name an image instead",
			p.Platform.OS, naming.SupportedPlatforms()))
	}
	if p.Platform.Arch != "" && naming.NormalizeArch(p.Platform.Arch) == "" {
		add("platform.arch", fmt.Sprintf("%q is not an architecture Zoomies runs on; use amd64 or arm64", p.Platform.Arch))
	}
	if p.Backend == store.BackendProcess {
		// There is no image: the host itself is the environment, so nothing
		// below applies.
		return errs
	}

	os := naming.NormalizeOS(p.Platform.OS)
	if p.Image == "" && os != "" {
		img, ok := naming.FindImage(os, p.Platform.OSVersion)
		switch {
		case !ok:
			add("platform.os_version", fmt.Sprintf("no runner image is published for %s %s; Zoomies publishes %s, or name an image of your own",
				naming.PrettyOS(os), p.Platform.OSVersion, naming.SupportedPlatforms()))
		case !img.Supports(p.Platform.Arch):
			add("platform.arch", fmt.Sprintf("%s is not published for %s; it is built for %s",
				img.Ref(), p.Platform.Arch, strings.Join(img.Arches, " and ")))
		}
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
	// An empty image is only a problem when nothing else can supply one: a
	// pool that names its platform gets the variant that platform selects, and
	// one that names neither gets the instance default.
	if p.Image == "" && p.Platform.OS == "" && p.Backend != store.BackendProcess &&
		strings.TrimSpace(s.cfg().GitHub.RunnerImage) == "" {
		add("image", "a container backend needs a runner image; leave it blank only for the process backend, "+
			"or name an operating system and let its published image be used")
	}
	if !p.PullPolicy.Valid() {
		add("pull_policy", "use if-not-present, always, or pinned-only")
	}
	if p.PullPolicy == store.PullPinnedOnly && !digestReference(p.Image) {
		add("image", "pinned-only requires an immutable digest reference such as image@sha256:…; mutable tags are rejected")
	}
	errs = append(errs, validatePlatform(p)...)

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
	// GitHub adds self-hosted, the operating system and the architecture to
	// every just-in-time runner itself; the generate-jitconfig request has no
	// way to ask otherwise. Accepting the setting on an ephemeral pool would
	// have the scheduler refuse jobs asking for self-hosted that GitHub then
	// happily offers the runner -- a pool that says one thing and does another.
	if p.NoDefaultLabels && p.Ephemeral {
		add("no_default_labels", "GitHub adds the self-hosted, operating-system and architecture labels to every ephemeral (just-in-time) runner, "+
			"so only a non-ephemeral pool can leave them out; set ephemeral to false, or turn no_default_labels off")
	}
	if p.NoDefaultLabels && len(p.Labels) == 0 {
		add("labels", "a pool without the default labels advertises only the labels listed here; give it at least one")
	}
	if p.Ephemeral && p.MinRunners > 0 && p.IdleTimeout.Duration() == 0 {
		add("idle_timeout", "warm ephemeral runners with no idle timeout are replaced after every job and never reaped; give an idle timeout, or set min_runners to 0")
	}

	// Zero is not "no limit" here: an unsized pool is sized from the fleet's
	// default before it reaches this point, so a figure that survived is one
	// somebody typed. The floors are what a runner needs to be a runner at
	// all -- below them the agent process and the runner binary do not both
	// fit, and the job fails in a way that looks like a broken image.
	switch {
	case p.Resources.CPUs < 0:
		add("resources.cpus", "a CPU limit cannot be negative")
	case p.Resources.CPUs > 0 && p.Resources.CPUs < store.MinRunnerCPUs:
		add("resources.cpus", "a runner needs at least a quarter of a core; below that the runner binary cannot keep up with its own job")
	}
	switch {
	case p.Resources.MemoryMB < 0:
		add("resources.memory_mb", "a memory limit cannot be negative")
	case p.Resources.MemoryMB > 0 && p.Resources.MemoryMB < store.MinRunnerMemoryMB:
		add("resources.memory_mb", "a runner needs at least 512 MB; below that the runner binary is killed before it takes a job")
	}
	// A minimum is a floor under the size a runner is otherwise given: the
	// size the pool states, or -- where it leaves the field to the host -- a
	// slot's share of whichever host it lands on. Under a stated size it has
	// to be below it to say anything; under a share there is nothing to
	// compare it with here, because every host's share is different, and one
	// above a host's share simply does not apply there. Either way it is held
	// to the same floor any runner is.
	switch {
	case p.Resources.MinCPUs < 0:
		add("resources.min_cpus", "a minimum CPU cannot be negative; use 0 to follow the fleet's minimum")
	case p.Resources.CPUs > 0 && p.Resources.MinCPUs > p.Resources.CPUs:
		add("resources.min_cpus", fmt.Sprintf("the minimum (%g cores) is above the standard size (%g); a runner is never given less than the minimum, so it has to be the smaller of the two", p.Resources.MinCPUs, p.Resources.CPUs))
	case p.Resources.MinCPUs > 0 && p.Resources.MinCPUs < store.MinRunnerCPUs:
		add("resources.min_cpus", "a runner needs at least a quarter of a core, however short its host is")
	}
	switch {
	case p.Resources.MinMemoryMB < 0:
		add("resources.min_memory_mb", "a minimum memory cannot be negative; use 0 to follow the fleet's minimum")
	case p.Resources.MemoryMB > 0 && p.Resources.MinMemoryMB > p.Resources.MemoryMB:
		add("resources.min_memory_mb", fmt.Sprintf("the minimum (%d MB) is above the standard size (%d MB); a runner is never given less than the minimum, so it has to be the smaller of the two", p.Resources.MinMemoryMB, p.Resources.MemoryMB))
	case p.Resources.MinMemoryMB > 0 && p.Resources.MinMemoryMB < store.MinRunnerMemoryMB:
		add("resources.min_memory_mb", "a runner needs at least 512 MB, however short its host is; below that the runner binary is killed before it takes a job")
	}
	if share := p.Resources.DaemonSharePercent; share != 0 {
		switch {
		case p.DockerMode != store.DockerDinD:
			add("resources.daemon_share_percent", "the daemon's share only means something for a pool with docker_mode dind, which is the one that runs a daemon beside the runner")
		case share < store.MinDaemonSharePercent || share > store.MaxDaemonSharePercent:
			add("resources.daemon_share_percent", fmt.Sprintf("the daemon's share must be between %d and %d percent, or 0 for an even split; a container given less than a tenth of the slot is no limit in effect", store.MinDaemonSharePercent, store.MaxDaemonSharePercent))
		}
	}
	if p.Resources.DiskGB < 0 {
		add("resources.disk_gb", "a disk limit cannot be negative; use 0 for no limit")
	}
	if p.Resources.PidsLimit < 0 {
		add("resources.pids_limit", "a process limit cannot be negative; use 0 for no limit")
	}
	if !p.CPUBurst.Mode.Valid() {
		add("cpu_burst.mode", "use off, observe or automatic")
	}
	if p.CPUBurst.MaxCPUs < 0 {
		add("cpu_burst.max_cpus", "a burst ceiling cannot be negative; use 0 to let the host set the ceiling")
	} else if p.CPUBurst.MaxCPUs > 0 && p.CPUBurst.MaxCPUs < store.MinRunnerCPUs {
		add("cpu_burst.max_cpus", "a burst ceiling below a quarter of a core cannot run the runner itself")
	}
	// A pool takes its size from the host or states one, never both: the figure
	// it states would be the whole answer and the host's would never be read,
	// which is a setting that looks like it does something and does not.
	if p.SizeFromProfile && !p.Automatic() {
		add("size_from_profile", "a pool that takes its size from its host cannot also state one; clear the CPU and memory limits, or turn off size_from_profile and keep the figures")
	}
	if p.CPUBurst.Observes() && !p.Automatic() {
		add("cpu_burst.mode", "CPU elasticity currently requires automatic sizing, because the host share is its guaranteed base; clear the fixed CPU and memory size or turn elasticity off")
	}
	if p.CPUBurst.Observes() && p.Backend != store.BackendDocker && p.Backend != store.BackendPodman {
		add("cpu_burst.mode", "CPU elasticity needs the Docker or Podman backend, which can measure and move a live cgroup quota")
	}
	// The runner image refuses a Docker wait outside 1..3600 seconds with a
	// configuration exit, so a pool that overrides it past the hour starts no
	// runner at all. The fleet's own figure is a startup error for the same
	// reason; a pool's has to be refused in the same place its other figures
	// are, or the two disagree about the same limit.
	if d := p.RunnerSettings.DockerWait; d != nil && d.Duration() > config.MaxDockerWait {
		add("runner_settings.docker_wait", fmt.Sprintf(
			"the runner image refuses a wait longer than %s and starts no runner at all; keep it to %s or less, or clear it to follow the fleet",
			config.MaxDockerWait, config.MaxDockerWait))
	}
	// In-memory folders are a mount on a container; a process runner has no
	// container to mount them on, and its work folder is a directory the agent
	// makes, which an operator can already put on a tmpfs by pointing
	// agent.work_dir at one.
	if p.Tmpfs.Any() {
		if p.Backend == store.BackendProcess {
			add("tmpfs.work.enabled", "in-memory folders are mounted into a container, so they need the Docker or Podman backend; for a process runner point agent.work_dir at a tmpfs on the host instead")
		}
		// The image store is the docker-in-docker sidecar's, so a pool with no
		// sidecar has no container to mount it on.
		if p.Tmpfs.Daemon.Enabled && p.DockerMode != store.DockerDinD {
			add("tmpfs.daemon.enabled", "the image store is the Docker-in-Docker sidecar's, so it needs docker_mode dind; this pool has no sidecar to keep it in memory")
		}
	}
	// A pool with no limit is sized from its host's share when a runner is
	// created, and the folders are fitted to whatever that turns out to be; only a
	// limit typed here can be checked against sizes typed here. It runs whether or
	// not a folder is on: "auto" on a folder that is off is a mistake worth naming.
	if field, msg := p.Tmpfs.Validate(p.Resources.MemoryMB); field != "" {
		add(field, msg)
	}
	// The tool cache is kept beside the pool cache and shared with the same
	// runners, so it needs the cache to be on to have a scope at all; and it
	// is a folder bound into a container, so a process runner, which uses the
	// host's own tool cache, has nothing to bind it into.
	if p.Cache.Tools && !p.Cache.Enabled {
		add("cache.tools", "a tool cache is kept with the pool cache and shared with the same runners; turn the cache on, or turn the tool cache off")
	} else if p.Cache.Tools && p.Backend == store.BackendProcess {
		add("cache.tools", "a process runner uses the host's own tool cache already; a kept tool cache is for container runners")
	}
	if p.Cache.Enabled {
		if !p.Cache.Scope.Valid() {
			add("cache.scope", "use pool or repository")
		}
		if p.Cache.SizeLimit < 0 {
			add("cache.size_limit", "the cache size limit cannot be negative; use 0 for no limit")
		}
		if strings.Contains(p.Cache.Source, "..") {
			add("cache.source", "path traversal is not allowed")
		}
		// A size limit is kept by evicting entries from a directory on the
		// host. There is no directory to measure behind a named volume, so
		// accepting the number there would promise an enforcement that does
		// not exist -- which is worse than refusing it.
		if p.Cache.SizeLimit > 0 && !filepath.IsAbs(strings.TrimSpace(p.Cache.Source)) {
			add("cache.size_limit", "a size limit is enforced by evicting from a host directory, so set the cache source to an absolute host path, or leave the limit at 0")
		}
		if p.Cache.Scope == store.CacheScopeRepository {
			repo := strings.TrimSpace(p.Cache.Repository)
			inst, err := s.ctrl.Store().GetInstallation(ctx, p.InstallationID)
			switch {
			case err != nil:
				// The installation itself is already reported as invalid.
			case inst.TargetType == store.TargetRepo:
				if repo != "" && !strings.EqualFold(repo, inst.Target) {
					add("cache.repository", "this pool's installation is scoped to "+inst.Target+", so its repository cache can only be for that repository; leave it empty")
				}
			case repo == "":
				add("cache.repository", "this pool's installation covers all of "+inst.Target+", so a repository cache has to name the repository it is for, as "+inst.Target+"/name")
			case !validRepositoryPath(repo):
				add("cache.repository", "name the repository as owner/name, for example "+inst.Target+"/widgets")
			case !strings.EqualFold(strings.SplitN(repo, "/", 2)[0], inst.Target):
				add("cache.repository", "this pool's installation covers "+inst.Target+", so its cache repository has to be under that owner")
			}
		}
	}
	// A pool's env is applied last and wins, so a reserved name here replaces
	// what the controller gives each runner individually. Only a key being set
	// or changed is refused: a pool saved before this rule may already store
	// one, and refusing the whole pool on an unrelated edit would strand it.
	var stored map[string]string
	if existingID != "" {
		if old, err := s.ctrl.Store().GetPool(ctx, existingID); err == nil {
			stored = old.Env
		}
	}
	for _, k := range slices.Sorted(maps.Keys(p.Env)) {
		if strings.TrimSpace(k) == "" {
			add("env", "an environment variable needs a name")
			break
		}
		name := strings.ToUpper(strings.TrimSpace(k))
		if !slices.Contains(config.ReservedRunnerEnv, name) {
			continue
		}
		if was, ok := stored[k]; ok && was == p.Env[k] {
			continue
		}
		add("env", name+" is set for each runner individually, so a pool cannot set it: a runner's name, labels, group, ephemerality and credentials come from the pool's own settings; remove it from the environment")
		break
	}
	for k := range p.HostSelector {
		if strings.TrimSpace(k) == "" {
			add("host_selector", "a host selector key cannot be empty")
			break
		}
	}
	return errs
}

// validRepositoryPath accepts exactly "owner/name" with both halves present and
// nothing that could climb out of a cache directory built from it.
func validRepositoryPath(repo string) bool {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return false
	}
	return owner != "." && owner != ".." && name != "." && name != ".."
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

// handleCreatePool answers POST /api/v1/pools.
func (s *Server) handleCreatePool(w http.ResponseWriter, r *http.Request) {
	var in poolInput
	if !decode(w, r, &in) {
		return
	}
	if in.Auto != nil {
		unprocessable(w, "this pool cannot be created as described", []fieldError{{"auto", "automatic pools are made by the controller from the hosts it has, not created; turn on scheduler.auto_pools, or leave auto out to make a pool of your own"}})
		return
	}
	p := s.defaultPool()
	errs := in.apply(p)
	defaultNewPoolBurst(&in, p)
	// Organisation installations get the dedicated group provisioned by the
	// connection probe. An omitted group means "use the Zoomies isolation
	// boundary", while an explicit empty string still means GitHub Default.
	if in.RunnerGroup == nil && p.InstallationID != "" {
		if inst, err := s.ctrl.Store().GetInstallation(r.Context(), p.InstallationID); err == nil && inst.TargetType == store.TargetOrg {
			p.RunnerGroup = controller.ManagedRunnerGroupName
		}
	}
	errs = append(errs, s.validatePool(r.Context(), p, "")...)
	if len(errs) > 0 {
		unprocessable(w, "this pool cannot be created as described", errs)
		return
	}
	// After validation, so a pool that could never be created is told what is
	// wrong with it rather than that there is no room for it.
	if err := s.ctrl.AdmitPool(r.Context()); err != nil {
		if !asLimit(w, err) {
			s.internal(w, r, "checking limits.pools", err)
		}
		return
	}

	if err := s.ctrl.Store().CreatePool(r.Context(), p); err != nil {
		s.fail(w, r, "creating the pool", err)
		return
	}

	s.auth.Auditor().Created(r.Context(), Identity(r.Context()), "pool", p.ID, poolForAudit(p))
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
	writeJSON(w, http.StatusCreated, poolFor(r, view.View(p)))
}

// validatePoolResponse is the wizard's review step: the errors that would stop
// the pool being created, the warnings it would produce, and how many hosts
// could actually run it.
type validatePoolResponse struct {
	Valid         bool                 `json:"valid"`
	Errors        []fieldError         `json:"errors"`
	Warnings      []controller.Problem `json:"warnings"`
	MatchingHosts int                  `json:"matching_hosts"`
	// SelectedHosts is how many hosts this pool's host selector reaches, and
	// ExcludedHosts is every one of those the fleet could not run it on, with
	// the reason. The wizard's placement step counts hosts by the selector
	// alone -- it is asked before the backend and the size are known -- so
	// without these two the review step's smaller number reads as the wizard
	// contradicting itself two clicks later.
	SelectedHosts int                        `json:"selected_hosts"`
	ExcludedHosts []controller.HostExclusion `json:"excluded_hosts"`
	// ReducedHosts can run the pool, and are counted in MatchingHosts, but
	// give its runners less than a larger machine would: the pool's minimum
	// at work. It is information, not a warning.
	ReducedHosts []controller.HostExclusion `json:"reduced_hosts"`
	// Image is the image the pool would actually run, which is not always the
	// one the request named: a pool that gives its jobs a daemon runs the
	// stock image's Docker variant, and the review step should show that
	// rather than promise a pool the server will not make.
	Image string `json:"image"`
	// EffectiveImage is that answer for a pool that named no image at all,
	// where Image is empty because nothing would be stored. The wizard's
	// automatic path names none, so without this the one step that says what
	// will be made said nothing about the image -- including for the pool that
	// has just asked for a Docker daemon, where which image it lands on is the
	// whole of what makes the daemon usable.
	EffectiveImage string `json:"effective_image"`
	// Room is how many runners of this size the hosts it can land on have
	// between them, host by host. It is what turns "4 CPU per runner" and "a
	// maximum of 20" from two settings nobody can weigh against each other
	// into one sentence: the fleet has room for eleven.
	Room controller.PoolRoom `json:"room"`
	// Resources is the size the pool would run at on every host, and it is
	// empty for a pool that leaves the size to its host. Sizing says which of
	// the two this is, so a reader need not infer "the host decides" from an
	// absent number -- and for an automatic pool the per-host figures are in
	// Room.Hosts, where charge_cpus and charge_memory_mb are the share each
	// machine would actually give a runner.
	Resources store.Resources `json:"resources"`
	Sizing    string          `json:"sizing"`
}

// handleValidatePool answers POST /api/v1/pools/validate. It creates nothing.
//
// `?id=` names the pool this is a dry run of an edit to, and it matters: without
// it the name check compares against every pool including that one, so opening
// a pool, changing its image and pressing on was refused with "a pool called
// zoomies-linux-x64 already exists" -- about itself. The form could not be
// saved at all without also renaming the pool.
func (s *Server) handleValidatePool(w http.ResponseWriter, r *http.Request) {
	var in poolInput
	if !decode(w, r, &in) {
		return
	}
	p := s.defaultPool()
	errs := in.apply(p)
	defaultNewPoolBurst(&in, p)
	errs = append(errs, s.validatePool(r.Context(), p, r.URL.Query().Get("id"))...)

	fit, err := s.ctrl.HostFit(r.Context(), p)
	if err != nil {
		s.internal(w, r, "counting the hosts that could run this pool", err)
		return
	}
	excluded := fit.Excluded
	if excluded == nil {
		excluded = []controller.HostExclusion{}
	}
	reduced := fit.Reduced
	if reduced == nil {
		reduced = []controller.HostExclusion{}
	}
	// The installation is part of what a warning is about; when it does not
	// exist, validatePool has already said so in the errors.
	var inst *store.Installation
	if i, err := s.ctrl.Store().GetInstallation(r.Context(), p.InstallationID); err == nil {
		inst = i
	}
	room, err := s.ctrl.PoolRoom(r.Context(), p)
	if err != nil {
		s.internal(w, r, "counting the room this pool's hosts have for it", err)
		return
	}
	warnings := controller.PoolWarnings(p, inst, s.cfg())
	warnings = append(warnings, controller.PoolRoomWarnings(p, room)...)
	if fit.Count == 0 {
		why, fix := noHostWarning(p, fit)
		warnings = append(warnings, controller.Problem{
			Code:         "pool.no_matching_hosts",
			Severity:     config.SeverityWarning,
			Title:        "no host can run this pool as configured",
			Detail:       why,
			Fix:          fix,
			Alternatives: fit.Alternatives,
		})
	}
	if errs == nil {
		errs = []fieldError{}
	}
	if warnings == nil {
		warnings = []controller.Problem{}
	}
	writeJSON(w, http.StatusOK, validatePoolResponse{
		Valid:          len(errs) == 0,
		Errors:         errs,
		Warnings:       warnings,
		MatchingHosts:  fit.Count,
		SelectedHosts:  fit.Selected,
		ExcludedHosts:  excluded,
		ReducedHosts:   reduced,
		Image:          p.Image,
		EffectiveImage: s.ctrl.RunnerImage(p),
		Room:           room,
		Resources:      p.Resources,
		Sizing:         controller.PoolSizing(p),
	})
}

// noHostWarning says why nothing can run this pool and what to change, choosing
// the sentence from the rule the hosts actually failed rather than assuming the
// commonest one. An operator told to "make the docker backend usable" on a host
// whose only problem is that it has four CPUs goes and looks at a daemon that
// was working all along.
func noHostWarning(p *store.Pool, fit controller.HostFit) (why, fix string) {
	if fit.Selected == 0 {
		// A selector on what the agent reports about its machine is answered
		// by joining such a machine, not by labelling one: an operator told
		// to "label a host" for os=windows would put a Linux box in a Windows
		// pool, which is exactly the placement the selector exists to stop.
		if platform := platformSelector(p.HostSelector); platform != "" {
			return fmt.Sprintf("no host reports itself as %s, so every runner this pool asks for would wait for a host that does not exist.", platform),
				fmt.Sprintf("join a %s host to this fleet, or change the host selector to a platform it already has.", platform)
		}
		return "no host matches this pool's host selector, so every runner it asks for would wait for a host that does not exist.",
			"relax the host selector, or label a host to match it."
	}
	counts := map[string]int{}
	for _, ex := range fit.Excluded {
		counts[ex.Code]++
	}
	switch {
	case counts[controller.ExcludedSize] > 0 && counts[controller.ExcludedSize] == len(fit.Excluded):
		// The one refusal that is about a number an operator typed rather than
		// about the fleet, so the numbers go in the sentence.
		why = fmt.Sprintf("no host this pool's selector reaches is big enough for one of its runners, "+
			"so every runner it asks for would wait for a machine that does not exist. %s", hostDetail(fit))
		fix = "lower this pool's CPU, memory or disk request, turn off Docker in Docker if it is not needed, or add a host large enough to run one."
	case counts[controller.ExcludedPlatform] > 0 && p.Platform.Describe() != "":
		platform := p.Platform.Describe()
		why = fmt.Sprintf("no healthy, uncordoned host is running %s with the %s backend, "+
			"so every runner this pool asks for would wait for a host that does not exist.", platform, p.Backend)
		fix = fmt.Sprintf("add a %s host, or change this pool's platform to one your fleet already has.", platform)
	case counts[controller.ExcludedUnavailable] == len(fit.Excluded):
		why = fmt.Sprintf("every host this pool's selector reaches is cordoned, not heartbeating, or running an agent this "+
			"controller cannot talk to, so nothing would be placed there. %s", hostDetail(fit))
		fix = "uncordon a host, check that its agent is running and can reach this controller, or add one that can take work."
	default:
		why = fmt.Sprintf("no healthy, uncordoned host offers the %s backend and matches this pool's host selector, "+
			"so every runner it asks for would wait for a host that does not exist. %s", p.Backend, hostDetail(fit))
		fix = fmt.Sprintf("make the %s backend usable on that host%s.", p.Backend, switchTo(fit.Alternatives))
	}
	return strings.TrimSpace(why), fix
}

// platformSelector renders a host selector that asks only about the machine
// -- the os and arch keys every agent answers for -- as prose, e.g.
// "Windows, arm64". It is empty for a selector that asks about anything else,
// because a label an operator invented is theirs to explain.
func platformSelector(sel map[string]string) string {
	var parts []string
	for k, v := range sel {
		switch strings.ToLower(strings.TrimSpace(k)) {
		case store.LabelOS:
			if os := naming.PrettyOS(v); os != "" {
				parts = append([]string{os}, parts...)
				continue
			}
		case store.LabelArch:
			if arch := naming.NormalizeArch(v); arch != "" {
				parts = append(parts, arch)
				continue
			}
		}
		return ""
	}
	return strings.Join(parts, ", ")
}

// hostDetail names the hosts that were turned down and why, up to the point
// where a paragraph stops being read. The names matter: an operator who can see
// which machine was refused can check it, and a count alone sends them round
// the whole fleet.
func hostDetail(fit controller.HostFit) string {
	const most = 3
	parts := make([]string, 0, most)
	for _, ex := range fit.Excluded {
		if len(parts) == most {
			parts = append(parts, fmt.Sprintf("and %d more", len(fit.Excluded)-most))
			break
		}
		parts = append(parts, ex.Host+": "+ex.Reason)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ") + "."
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
	if existing.FromHosts() {
		s.updateAutoPool(w, r, existing, &in)
		return
	}
	if in.Auto != nil {
		unprocessable(w, "this pool cannot be changed as described", []fieldError{{"auto", "this pool was made by somebody, not kept by the controller, so it has no warm count, cap or pause of that kind; use min_runners, max_runners and enabled"}})
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

	// The other half of the host edit's guard. A pool that fits somewhere
	// today and would fit nowhere afterwards is a pool whose jobs queue for
	// ever, and the wizard's warning is shown only to the wizard: this is the
	// same fact refused at the route every client goes through.
	if !queryBool(r, "confirm", false) {
		stranded, serr := s.ctrl.PoolStranding(r.Context(), existing, &updated)
		if serr != nil {
			s.internal(w, r, "checking whether any host could still run this pool", serr)
			return
		}
		if len(stranded) > 0 {
			conflict(w, poolStrandingRefusal(stranded[0]))
			return
		}
	}

	if err := s.ctrl.Store().UpdatePool(r.Context(), &updated); err != nil {
		s.fail(w, r, "saving the pool", err)
		return
	}

	s.auth.Auditor().Updated(r.Context(), Identity(r.Context()), "pool", id, poolForAudit(&before), poolForAudit(&updated))
	s.ctrl.PublishPool(r.Context(), events.KindPoolUpdated, &updated)
	// The pool's shape decides how many runners should exist, so the scheduler
	// should look again rather than wait out its interval.
	s.ctrl.Nudge()
	// A daemon that comes or goes can change the image the runners are made
	// from without changing the row -- a pool on the controller's default
	// image has nothing in its own image field to differ -- so it counts as
	// an image change here too.
	if before.Image != updated.Image || before.DockerMode.GivesDaemon() != updated.DockerMode.GivesDaemon() ||
		before.PullPolicy != updated.PullPolicy || before.Backend != updated.Backend || !maps.Equal(before.HostSelector, updated.HostSelector) {
		_, _ = s.ctrl.PrewarmPool(r.Context(), &updated)
	}

	view, verr := s.ctrl.PoolRenderer(r.Context())
	if verr != nil {
		s.internal(w, r, "reading the pool back", verr)
		return
	}
	writeJSON(w, http.StatusOK, poolFor(r, view.View(&updated)))
}

func (s *Server) handlePrewarmPool(w http.ResponseWriter, r *http.Request) {
	p, err := s.ctrl.Store().GetPool(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the pool", err)
		return
	}
	n, err := s.ctrl.PrewarmPool(r.Context(), p)
	if err != nil {
		if errors.Is(err, controller.ErrPrewarmUnsupported) {
			unprocessable(w, err.Error(), nil)
			return
		}
		s.fail(w, r, "queueing the image pull", err)
		return
	}
	states, err := s.ctrl.Store().ListPoolPrewarms(r.Context(), p.ID)
	if err != nil {
		s.internal(w, r, "reading prewarm state", err)
		return
	}
	// Every mutating operator route writes a row, and this one pulls an image
	// onto every host that matches the pool: minutes of network on somebody
	// else's machines, and the one action of the set that left no trace.
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "pool.prewarm", "pool", p.ID, map[string]any{
		"image": s.ctrl.RunnerImage(p), "hosts": n,
	})
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
//
// A drain the pool does not outlive is not a drain. The pool row used to go in
// the same request that started one, and deleting it takes its runners' rows
// with it -- so the next heartbeat reported runners the controller had no
// record of, the agent stopped tracking them, and its orphan sweep destroyed
// the containers about two minutes later, killing the jobs the drain existed to
// protect. So the delete is refused while any runner is still going, and the
// operator calls again once they have finished. The drain has already been
// started by then, which is what makes the second call the short one.
//
// An idle pool still goes in one call: a runner that is not busy is removed
// outright rather than drained, so it is terminal by the time this looks. Only
// a pool with work on it needs the second call, and force skips the wait by
// killing the jobs.
func (s *Server) handleDeletePool(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	force := queryBool(r, "force", false)
	drain := queryBool(r, "drain", true)

	p, err := s.ctrl.Store().GetPool(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the pool", err)
		return
	}
	// The controller makes it again on its next pass, so deleting it only costs
	// the pool's history. Left alone when it is not keeping this pool -- the switch
	// is off or only watching, or the pool belongs to an installation the pools are
	// no longer kept for -- because then it is a leftover and nothing will remake it.
	if p.FromHosts() && s.ctrl.AutoPoolKept(p) {
		conflict(w, fmt.Sprintf("%s is kept by the controller from your hosts, so deleting it would only have it made again on the next pass. "+
			"Pause it to stop it taking work, or set scheduler.auto_pools to shadow or off to stop the controller keeping pools, and delete it then.", p.Name))
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
		_, rerr = s.ctrl.RemoveRunner(r.Context(), run.ID, "pool "+p.Name+" was deleted", force || !drain, true)
		if rerr != nil {
			// One runner that cannot be told to stop must not leave the pool
			// half-deleted; the row goes either way and the reaper cleans up.
			s.logger(r).Warn("could not stop a runner while deleting its pool",
				"pool", id, "runner", run.ID, "error", rerr)
			continue
		}
		affected++
	}

	// Read the runners back rather than trusting the loop above: a drain leaves
	// a runner in draining, which is not terminal, and only the agent reporting
	// the stop moves it on.
	remaining, err := s.ctrl.Store().ListRunnersForPool(r.Context(), id)
	if err != nil {
		s.internal(w, r, "checking whether the pool's runners have finished", err)
		return
	}
	live := 0
	for _, run := range remaining {
		if !run.State.Terminal() {
			live++
		}
	}
	if live > 0 {
		// The refusal has already removed or drained runners, and the pool.delete
		// row only comes with a later call that may be someone else's, so this
		// is the one place the actor who emptied the pool is recorded.
		if affected > 0 {
			s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "pool.drain", "pool", id, map[string]any{
				"name": p.Name, "runners": affected, "force": force, "drain": drain,
			})
		}
		conflict(w, fmt.Sprintf("%s still has %s finishing, so it was not deleted: deleting the pool now would "+
			"take their records with it and the jobs they are running would be killed a couple of minutes later. "+
			"They have been told to stop and will go as their jobs finish; delete the pool again once they have. "+
			"To stop them now and lose those jobs, delete it with force=true.",
			p.Name, runnerCount(live)))
		return
	}

	if err := s.ctrl.DeletePool(r.Context(), id); err != nil {
		s.fail(w, r, "deleting the pool", err)
		return
	}
	s.auth.Auditor().Deleted(r.Context(), Identity(r.Context()), "pool", id, poolForAudit(p))
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
	// Whether an automatic pool is in use is the controller's to work out from
	// its hosts, so the switch is the operator's half of it: a pause.
	if p.FromHosts() {
		s.setAutoPoolPaused(w, r, p, !enabled)
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
	writeJSON(w, http.StatusOK, poolFor(r, view.View(p)))
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

// runnerCount renders a count of runners for a sentence an operator reads.
func runnerCount(n int) string {
	if n == 1 {
		return "1 runner"
	}
	return fmt.Sprintf("%d runners", n)
}
