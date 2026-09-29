package api

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"slices"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// bundleVersion is the shape's own number, carried in the document so that a
// reader given a bundle out of context knows what it is looking at. It moves
// when a section is removed or renamed, not when one is added: a reader that
// ignores fields it does not know keeps working across an addition.
const bundleVersion = 1

// The per-section caps. They are counts rather than bytes because a count is
// what a reader can reason about -- "the newest 500 runners" is a sentence --
// and because a section truncated at a row boundary is still valid JSON. The
// byte cap below is the backstop for the fleet that makes 500 rows enormous.
const (
	bundleMaxRunners       = 500
	bundleMaxJobs          = 300
	bundleMaxExplanations  = 50
	bundleMaxScalingEvents = 200
	bundleMaxMachines      = 200
	bundleMaxLogPointers   = 50

	// bundleMaxBytes is what an operator can paste into a support case
	// without a file-sharing service. A bundle over it sheds sections rather
	// than being refused: a short bundle answers some questions, and a 500
	// answers none.
	bundleMaxBytes = 8 << 20
)

// supportBundle is one document describing this instance, for a bug report.
//
// It is assembled from the same renderings the API already serves rather than
// from the store directly, which is the property that makes it trustworthy:
// every section is something the operator could have fetched themselves, so a
// field that is secret-free on its own route is secret-free here, and a view
// that gains a field gains it in both places. The configuration in particular
// comes from settingsConfig, which is built key by key -- a secret added to
// config.Config tomorrow cannot appear in a bundle by default.
//
// The one exception to "what the operator could have fetched" is a pool's
// environment: its values are the operator's to read on the pool's own route,
// but the bundle is a document made to be handed on, so it keeps the keys and
// drops the values.
//
// What it never carries is workflow log bodies. There is no redaction pass for
// them and there cannot be a reliable one -- a log holds whatever a workflow
// printed -- so the bundle carries runner ids and the download route instead,
// and an operator decides log by log what to attach.
type supportBundle struct {
	BundleVersion int                      `json:"bundle_version"`
	GeneratedAt   time.Time                `json:"generated_at"`
	Instance      bundleInstance           `json:"instance"`
	Config        map[string]any           `json:"config,omitempty"`
	Findings      []config.Finding         `json:"findings,omitempty"`
	Problems      *controller.ProblemsView `json:"problems,omitempty"`
	Stats         *controller.Stats        `json:"stats,omitempty"`
	Installations []installationResponse   `json:"installations,omitempty"`
	Pools         []poolResponse           `json:"pools,omitempty"`
	Hosts         []hostResponse           `json:"hosts,omitempty"`
	// Providers and Machines are the evidence a pending cleanup or an orphan
	// review needs, and they cost nothing: both sections are the renderings
	// the API already serves, so a machine stuck mid-create arrives in a bug
	// report with its operation handle and its provider's own words.
	Providers []providerResponse `json:"providers,omitempty"`
	Machines  []machineResponse  `json:"machines,omitempty"`
	Runners   []runnerResponse   `json:"runners,omitempty"`
	// Jobs is the work in flight rather than the history. A bundle is taken
	// because something is stuck, and finished jobs would be most of the bytes
	// while answering none of it; the history is a query away on the same
	// instance.
	Jobs          []jobResponse                `json:"jobs,omitempty"`
	Explanations  []*controller.JobExplanation `json:"explanations,omitempty"`
	ScalingEvents []*store.ScalingEvent        `json:"scaling_events,omitempty"`
	Logs          *bundleLogs                  `json:"logs,omitempty"`

	// Errors is why a section is missing. A bundle assembles section by
	// section and a failing one costs its own contents: a support case built
	// on nine sections and a named failure is worth more than a 500, and the
	// failure itself is often the bug being reported.
	Errors []bundleError `json:"errors"`
	// Truncated names each section that was capped, and why.
	Truncated []bundleTruncation `json:"truncated,omitempty"`
}

// bundleInstance is what this process is and how it is doing, which is the
// half of a bug report that is never in the fleet's own rows.
type bundleInstance struct {
	Version          string     `json:"version"`
	Commit           string     `json:"commit,omitempty"`
	BuildDate        string     `json:"build_date,omitempty"`
	Go               string     `json:"go"`
	OS               string     `json:"os"`
	Arch             string     `json:"arch"`
	CPUs             int        `json:"cpus"`
	Goroutines       int        `json:"goroutines"`
	HeapInUseBytes   uint64     `json:"heap_in_use_bytes"`
	ConfigPath       string     `json:"config_path,omitempty"`
	DatabasePath     string     `json:"database_path,omitempty"`
	SchemaApplied    int        `json:"schema_applied,omitempty"`
	SchemaLatest     string     `json:"schema_latest,omitempty"`
	EventSubscribers int        `json:"event_subscribers"`
	PollingOnly      bool       `json:"polling_only"`
	PollerEnabled    bool       `json:"poller_enabled"`
	PollerLastPollAt *time.Time `json:"poller_last_poll_at,omitempty"`
	// LoopPanics is the one fact here that is always a bug in Zoomies. A loop
	// that panicked was restarted and the fleet carried on, so nothing else in
	// a bundle would show it.
	LoopPanics []bundleLoopPanic `json:"loop_panics,omitempty"`
}

// bundleLoopPanic is one background loop's crash record.
type bundleLoopPanic struct {
	Loop  string    `json:"loop"`
	Count int       `json:"count"`
	Last  string    `json:"last"`
	At    time.Time `json:"at"`
}

// bundleLogs points at logs rather than carrying them.
type bundleLogs struct {
	Note    string             `json:"note"`
	Runners []bundleLogPointer `json:"runners"`
}

// bundleLogPointer is one runner whose log a support case is likely to want,
// with the route that fetches it.
type bundleLogPointer struct {
	RunnerID string            `json:"runner_id"`
	Name     string            `json:"name,omitempty"`
	State    store.RunnerState `json:"state"`
	PoolID   string            `json:"pool_id,omitempty"`
	Download string            `json:"download"`
}

type bundleError struct {
	Section string `json:"section"`
	Error   string `json:"error"`
}

type bundleTruncation struct {
	Section string `json:"section"`
	Kept    int    `json:"kept"`
	Reason  string `json:"reason"`
}

// handleDiagnosticsBundle answers GET /api/v1/diagnostics/bundle.
func (s *Server) handleDiagnosticsBundle(w http.ResponseWriter, r *http.Request) {
	b := s.supportBundle(r.Context())
	raw, err := marshalBundle(&b)
	if err != nil {
		s.internal(w, r, "rendering the support bundle", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "diagnostics.bundle", "instance", "", map[string]any{
		"bytes": len(raw), "sections_failed": len(b.Errors),
	})
	// no-store because a bundle is a snapshot of the whole instance and has no
	// business in a shared cache, and because it is stale the moment it is
	// taken.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// supportBundle assembles the document.
//
// Every section is gathered through gather, so that one failure is recorded
// and the rest of the bundle still arrives. That is the same rule the problems
// aggregator follows and for the same reason: the moment an operator most
// needs this is the moment a query is most likely to fail.
func (s *Server) supportBundle(ctx context.Context) supportBundle {
	b := supportBundle{
		BundleVersion: bundleVersion,
		GeneratedAt:   s.ctrl.Now(),
		Errors:        []bundleError{},
	}
	gather := func(section string, fn func() error) {
		if err := fn(); err != nil {
			b.Errors = append(b.Errors, bundleError{Section: section, Error: err.Error()})
		}
	}

	b.Instance = s.bundleInstance(ctx, gather)
	if !callerRoleCtx(ctx).AtLeast(store.RolePlatform) {
		// "Send me a bundle" is the platform's first support question, so the
		// fleet has to be able to answer it -- the route stays theirs rather
		// than moving behind the platform's. What they send is the fleet's
		// half: their pools, hosts, runners and jobs, and of the instance
		// only what identifies the build it all ran on. The machine the
		// process is on is not theirs to describe.
		b.Instance = bundleInstance{
			Version:   b.Instance.Version,
			Commit:    b.Instance.Commit,
			BuildDate: b.Instance.BuildDate,
			Go:        b.Instance.Go,
		}
	}

	gather("config", func() error {
		b.Config = s.settingsConfig(callerRoleCtx(ctx))
		b.Findings = s.cfg().Validate().ForUI()
		return nil
	})
	gather("problems", func() error {
		items, err := s.ctrl.Problems(ctx)
		if err != nil {
			return err
		}
		// The same audience split as the problems drawer: a failed backup
		// names the directory it could not write, and that directory is the
		// machine's, not the fleet's.
		view := controller.NewProblemsViewFor(items, callerRoleCtx(ctx).AtLeast(store.RolePlatform))
		b.Problems = &view
		return nil
	})
	gather("stats", func() error {
		stats, err := s.ctrl.Stats(ctx, defaultStatsWindow)
		if err != nil {
			return err
		}
		b.Stats = stats
		return nil
	})
	gather("installations", func() error {
		insts, err := s.ctrl.Store().ListInstallations(ctx)
		if err != nil {
			return err
		}
		counts, err := s.ctrl.PoolCountsByInstallation(ctx)
		if err != nil {
			return err
		}
		for _, i := range insts {
			b.Installations = append(b.Installations, controller.NewInstallationView(i, counts[i.ID]))
		}
		return nil
	})
	gather("pools", func() error {
		pools, err := s.ctrl.Store().ListPools(ctx)
		if err != nil {
			return err
		}
		view, err := s.ctrl.PoolRenderer(ctx)
		if err != nil {
			return err
		}
		for _, p := range pools {
			// Not poolFor: that is role-based, and an administrator who may read
			// the values on the pool's own route is not the reader of this file,
			// which is attached to an issue or sent to whoever runs the platform.
			b.Pools = append(b.Pools, view.View(p).WithoutEnvValues())
		}
		return nil
	})
	gather("hosts", func() error {
		hosts, err := s.ctrl.Store().ListHosts(ctx)
		if err != nil {
			return err
		}
		for _, h := range hosts {
			b.Hosts = append(b.Hosts, s.ctrl.HostView(h))
		}
		return nil
	})

	gather("providers", func() error {
		rows, err := s.ctrl.Store().ListProviders(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			machines, err := s.ctrl.Store().ListMachinesForProvider(ctx, row.ID)
			if err != nil {
				return err
			}
			b.Providers = append(b.Providers, s.ctrl.ProviderView(row, machines))
		}
		return nil
	})
	gather("machines", func() error {
		// Deleted machines are left out for the reason finished jobs are: a
		// fleet that has been renting for a month has far more history than
		// present, and a bundle is taken because something is stuck now.
		found, total, err := s.ctrl.Store().ListMachines(ctx, store.MachineFilter{}, store.Page{Limit: bundleMaxMachines})
		if err != nil {
			return err
		}
		view, err := s.ctrl.MachineRenderer(ctx)
		if err != nil {
			return err
		}
		for _, m := range found {
			b.Machines = append(b.Machines, view.View(m))
		}
		b.note(total > len(found), "machines", len(found), fmt.Sprintf("the fleet has %d; a bundle carries the newest %d", total, bundleMaxMachines))
		return nil
	})

	var runners []*store.Runner
	gather("runners", func() error {
		found, total, err := s.ctrl.Store().ListRunners(ctx, store.RunnerFilter{}, store.Page{Limit: bundleMaxRunners})
		if err != nil {
			return err
		}
		runners = found
		view, err := s.ctrl.RunnerRenderer(ctx, found)
		if err != nil {
			return err
		}
		for _, run := range found {
			b.Runners = append(b.Runners, view.View(run))
		}
		b.note(total > len(found), "runners", len(found), fmt.Sprintf("the fleet has %d; a bundle carries the newest %d", total, bundleMaxRunners))
		return nil
	})
	gather("logs", func() error {
		b.Logs = bundleLogPointers(runners)
		return nil
	})

	gather("jobs", func() error {
		return s.bundleJobs(ctx, &b)
	})
	gather("scaling_events", func() error {
		events, err := s.ctrl.Store().ListScalingEvents(ctx, "", bundleMaxScalingEvents)
		if err != nil {
			return err
		}
		b.ScalingEvents = events
		b.note(len(events) == bundleMaxScalingEvents, "scaling_events", len(events), "the newest decisions only")
		return nil
	})
	return b
}

// note records a cap that was reached, so a reader can tell a short section
// from a complete one. A section that fitted says nothing, because a bundle
// full of "not truncated" lines buries the one line that matters.
func (b *supportBundle) note(truncated bool, section string, kept int, reason string) {
	if truncated {
		b.Truncated = append(b.Truncated, bundleTruncation{Section: section, Kept: kept, Reason: reason})
	}
}

// bundleInstance describes the process. Its two store reads are gathered
// individually: a database that will not answer is exactly when the rest of
// this section -- the build, the goroutine count -- is worth having.
func (s *Server) bundleInstance(ctx context.Context, gather func(string, func() error)) bundleInstance {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	out := bundleInstance{
		Version:          version.Version,
		Commit:           version.Commit,
		BuildDate:        version.Date,
		Go:               runtime.Version(),
		OS:               runtime.GOOS,
		Arch:             runtime.GOARCH,
		CPUs:             runtime.NumCPU(),
		Goroutines:       runtime.NumGoroutine(),
		HeapInUseBytes:   mem.HeapInuse,
		ConfigPath:       s.cfg().Path(),
		DatabasePath:     s.ctrl.Store().Path(),
		EventSubscribers: s.ctrl.Events().Subscribers(),
		PollingOnly:      s.ctrl.PollingOnly(),
		PollerEnabled:    s.ctrl.PollerEnabled(),
	}
	if last := s.ctrl.LastPollAt(); !last.IsZero() {
		out.PollerLastPollAt = &last
	}
	for name, p := range s.ctrl.LoopPanics() {
		out.LoopPanics = append(out.LoopPanics, bundleLoopPanic{Loop: name, Count: p.Count, Last: p.Last, At: p.At})
	}
	slices.SortFunc(out.LoopPanics, func(a, b bundleLoopPanic) int {
		return cmp.Compare(a.Loop, b.Loop)
	})
	gather("instance.schema", func() error {
		applied, err := s.ctrl.Store().AppliedMigrations(ctx)
		if err != nil {
			return err
		}
		out.SchemaApplied = len(applied)
		if n := len(applied); n > 0 {
			out.SchemaLatest = applied[n-1].Name
		}
		return nil
	})
	return out
}

// bundleJobs carries the work in flight and the controller's own answer for
// each of it -- the same answer the drawer and the CLI render, so a bundle
// cannot disagree with the page the operator was looking at when they took it.
func (s *Server) bundleJobs(ctx context.Context, b *supportBundle) error {
	filter := store.JobFilter{States: []store.JobState{store.JobWaiting, store.JobQueued, store.JobInProgress}}
	jobs, total, err := s.ctrl.Store().ListJobs(ctx, filter, store.Page{Limit: bundleMaxJobs})
	if err != nil {
		return err
	}
	pools, err := s.ctrl.Store().ListPools(ctx)
	if err != nil {
		return err
	}
	names := poolNames(pools)
	for _, j := range jobs {
		b.Jobs = append(b.Jobs, controller.NewJobView(j, names[j.PoolID]))
	}
	b.note(total > len(jobs), "jobs", len(jobs), fmt.Sprintf("%d jobs are unfinished; a bundle carries the newest %d", total, bundleMaxJobs))

	for _, j := range jobs {
		if len(b.Explanations) >= bundleMaxExplanations {
			b.note(true, "explanations", len(b.Explanations), "one per unfinished job, oldest first, up to the cap")
			break
		}
		ex, err := s.ctrl.ExplainJob(ctx, j.ID)
		if err != nil {
			// One job that cannot be explained is not the bundle's failure,
			// and the job itself is already in the section above.
			continue
		}
		b.Explanations = append(b.Explanations, ex)
	}
	return nil
}

// bundleLogPointers names the runners whose logs a support case is likely to
// want: the ones that failed, and the ones still trying to start. It is a list
// of routes rather than of bodies -- see the note it carries.
func bundleLogPointers(runners []*store.Runner) *bundleLogs {
	out := &bundleLogs{
		Note: "Workflow logs are never in a bundle: there is no redaction pass for them, and a log holds whatever a workflow printed. " +
			"Fetch the ones a support case needs from the routes below and attach them deliberately.",
		Runners: []bundleLogPointer{},
	}
	for _, r := range runners {
		switch r.State {
		case store.RunnerFailed, store.RunnerProvisioning, store.RunnerRegistering:
		default:
			continue
		}
		if len(out.Runners) >= bundleMaxLogPointers {
			break
		}
		out.Runners = append(out.Runners, bundleLogPointer{
			RunnerID: r.ID,
			Name:     r.Name,
			State:    r.State,
			PoolID:   r.PoolID,
			Download: "/api/v1/runners/" + r.ID + "/logs/download",
		})
	}
	return out
}

// shedOrder is what a bundle gives up when it is over the byte cap, weakest
// evidence first. Explanations are recomputable from the jobs beside them,
// scaling history is the least specific to a fault, and the fleet's own shape
// -- pools, hosts, configuration, problems -- is what a bundle is for and is
// never shed.
var shedOrder = []struct {
	section string
	drop    func(*supportBundle)
}{
	{"explanations", func(b *supportBundle) { b.Explanations = nil }},
	{"scaling_events", func(b *supportBundle) { b.ScalingEvents = nil }},
	{"jobs", func(b *supportBundle) { b.Jobs = nil }},
	{"runners", func(b *supportBundle) { b.Runners = nil }},
}

// marshalBundle renders the document, shedding sections until it is under the
// cap.
//
// The cap is checked on the rendered bytes rather than guessed from row counts
// because what makes a bundle huge is not usually the number of rows: it is one
// fleet's very long pool names, or a hundred scheduler reason strings. A count
// cap cannot see that and a byte cap can.
func marshalBundle(b *supportBundle) ([]byte, error) {
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	for _, shed := range shedOrder {
		if len(raw) <= bundleMaxBytes {
			break
		}
		shed.drop(b)
		b.Truncated = append(b.Truncated, bundleTruncation{
			Section: shed.section,
			Reason:  fmt.Sprintf("the bundle was over its %d-byte cap, and this section is the most recomputable thing in it", bundleMaxBytes),
		})
		if raw, err = json.Marshal(b); err != nil {
			return nil, err
		}
	}
	return raw, nil
}
