package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/store"
)

func TestStartupQueuePreservesForegroundOrderAheadOfRefreshes(t *testing.T) {
	var q startupQueue
	active := q.enqueue(true)
	warm := q.enqueue(false)
	first := q.enqueue(true)
	second := q.enqueue(true)
	assertReady := func(ticket *startupTicket, want bool) {
		t.Helper()
		select {
		case <-ticket.ready:
			if !want {
				t.Fatal("task admitted ahead of its turn")
			}
		default:
			if want {
				t.Fatal("next task was not admitted")
			}
		}
	}
	q.done(active)
	assertReady(first, true)
	assertReady(second, false)
	assertReady(warm, false)
	q.done(first)
	assertReady(second, true)
	q.done(second)
	assertReady(warm, true)
	q.done(warm)
}

func TestCancellingAQueuedStartupDoesNotReleaseTheActiveOne(t *testing.T) {
	var q startupQueue
	active := q.enqueue(true)
	cancelled := q.enqueue(true)
	next := q.enqueue(true)
	q.done(cancelled)
	select {
	case <-next.ready:
		t.Fatal("cancelling a waiter released the running operation")
	default:
	}
	q.done(active)
	select {
	case <-next.ready:
	default:
		t.Fatal("cancelled waiter blocked the next operation")
	}
	q.done(next)
}

// A redelivered create -- or one from a controller too old to say which
// delivery it is -- may be for a runner that is mid-job behind a daemon that
// is not answering. Silence is the only safe answer.
func TestUnknownRunnerInventoryNeverRecreatesOrFailsTheRunner(t *testing.T) {
	a, tr, be, _ := newAgent(t, 2)
	a.resolveRetry = nil
	be.listErr = backend.ErrUnavailable
	released := false
	task := createTask("uncertain", "existing")
	a.handleCreate(context.Background(), task, func() { released = true })
	if !released {
		t.Fatal("uncertain task retained its runner claim")
	}
	if created, _, removed := be.counts(); created != 0 || removed != 0 {
		t.Fatal("unknown inventory changed workloads")
	}
	select {
	case result := <-tr.results:
		t.Fatalf("uncertain existence was reported as a lifecycle outcome: %+v", result)
	default:
	}
	// The same task can recover on redelivery once inventory answers.
	be.listErr = nil
	a.handleCreate(context.Background(), task, func() {})
	if result := <-tr.results; !result.OK {
		t.Fatalf("redelivery did not recover: %+v", result)
	}
}

// On a first delivery no workload of the runner's can exist, so an inventory
// the host will not give is a create that failed, and the controller should
// hear so now rather than when the provision timeout notices five minutes on.
func TestFirstCreateDeliveryFailsPromptlyWhenTheHostCannotBeAsked(t *testing.T) {
	a, tr, be, _ := newAgent(t, 2)
	a.resolveRetry = []time.Duration{0, 0}
	be.listErr = backend.ErrUnavailable
	task := createTask("first", "new")
	task.Attempt = 1
	a.handleCreate(context.Background(), task, func() {})
	result := <-tr.results
	if result.OK || result.RunnerID != "new" {
		t.Fatalf("first delivery was not failed: %+v", result)
	}
	if !strings.Contains(result.Error, "nothing was created") {
		t.Fatalf("failure does not say the host was left alone: %q", result.Error)
	}
	if created, _, removed := be.counts(); created != 0 || removed != 0 {
		t.Fatal("an unanswered inventory changed workloads")
	}
	if a.runtimeRetryAt.IsZero() {
		t.Fatal("an unavailable daemon did not open the runtime hold")
	}
}

// A daemon that missed one call answers the next; a create should ask again
// before deciding anything, because every other outcome costs a runner.
func TestCreateRetriesTheInventoryBeforeGivingUp(t *testing.T) {
	a, tr, be, _ := newAgent(t, 2)
	a.resolveRetry = []time.Duration{0, 0, 0}
	be.listErr = backend.ErrUnavailable
	be.listErrAfter = 2
	task := createTask("retry", "new")
	task.Attempt = 1
	a.handleCreate(context.Background(), task, func() {})
	result := <-tr.results
	if !result.OK {
		t.Fatalf("create did not recover once the daemon answered: %+v", result)
	}
	if created, _, _ := be.counts(); created != 1 {
		t.Fatalf("created %d runners, want 1", created)
	}
	if !a.runtimeRetryAt.IsZero() {
		t.Fatal("a create that succeeded left the runtime hold open")
	}
}

func TestRuntimeCooldownIsBoundedAndClearsOnSuccess(t *testing.T) {
	a, _, _, clock := newAgent(t, 2)
	a.runtimeResult(errors.New("bad pool configuration"))
	if !a.runtimeRetryAt.IsZero() {
		t.Fatal("configuration failure held the whole host")
	}
	// A pull that outran the create budget is slow, not a broken runtime;
	// holding every start behind it would turn one slow image into a stalled
	// host.
	a.runtimeResult(fmt.Errorf("backend: pulling image: %w", context.DeadlineExceeded))
	a.runtimeResult(context.Canceled)
	if !a.runtimeRetryAt.IsZero() {
		t.Fatal("an expired caller budget held the whole host")
	}
	for range 10 {
		a.runtimeResult(backend.ErrUnavailable)
		wait := a.runtimeRetryAt.Sub(clock.Now())
		if wait < 5*time.Second || wait > 75*time.Second {
			t.Fatalf("runtime cooldown outside bounds: %s", wait)
		}
	}
	a.runtimeResult(nil)
	if !a.runtimeRetryAt.IsZero() || a.runtimeFailures != 0 {
		t.Fatal("successful operation did not clear runtime hold")
	}
}

type dependencyBackend struct {
	*fakeBackend
	dindCalls int
	dindErr   error
}

func (b *dependencyBackend) PrewarmDinD(context.Context) error {
	b.dindCalls++
	return b.dindErr
}

func TestPrewarmingCoalescesSharedImagesAndIncludesDockerDependencies(t *testing.T) {
	a, _, be, clock := newAgent(t, 2)
	b := &dependencyBackend{fakeBackend: be}
	task := Task{PoolID: "one", Image: "runner", PullPolicy: store.PullAlways,
		Spec: &backend.Spec{DockerMode: store.DockerDinD}}
	for _, pool := range []string{"one", "two"} {
		task.PoolID = pool
		result, err := a.prewarm(context.Background(), b, b, task)
		if err != nil {
			t.Fatal(err)
		}
		if pool == "two" && !result.cached {
			t.Fatal("shared preparation was reused but not reported as a cache hit")
		}
	}
	if len(be.pulls()) != 1 || b.dindCalls != 1 {
		t.Fatalf("shared preparation repeated: pulls=%v, dependencies=%d", be.pulls(), b.dindCalls)
	}
	clock.advance(time.Minute)
	result, err := a.prewarm(context.Background(), b, b, task)
	if err != nil {
		t.Fatal(err)
	}
	if result.cached {
		t.Fatal("expired preparation was reported as a cache hit")
	}
	if len(be.pulls()) != 2 || b.dindCalls != 2 {
		t.Fatal("successful prewarm was reused past its freshness window")
	}
}

func TestFailedSidecarPreparationIsNotCached(t *testing.T) {
	a, _, be, _ := newAgent(t, 2)
	b := &dependencyBackend{fakeBackend: be, dindErr: errors.New("pull failed")}
	task := Task{Image: "runner", PullPolicy: store.PullIfNotPresent,
		Spec: &backend.Spec{DockerMode: store.DockerDinD}}
	if _, err := a.prewarm(context.Background(), b, b, task); err == nil {
		t.Fatal("sidecar preparation failure was ignored")
	}
	b.dindErr = nil
	if _, err := a.prewarm(context.Background(), b, b, task); err != nil {
		t.Fatal(err)
	}
	if b.dindCalls != 2 {
		t.Fatal("failed preparation suppressed its retry")
	}
}

type blockedStatsBackend struct {
	*fakeBackend
	entered chan struct{}
}

func (b *blockedStatsBackend) Stats(ctx context.Context, _ backend.Handle) (backend.Stats, error) {
	b.entered <- struct{}{}
	<-ctx.Done()
	return backend.Stats{}, ctx.Err()
}

func TestBlockedStatsDoNotBlockLifecycleReconciliation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, _, be, clock := newAgent(t, 2)
		b := &blockedStatsBackend{fakeBackend: be, entered: make(chan struct{}, 2)}
		a.opts.Backends = backend.NewRegistry(b)
		a.polled.Store(true)
		sampled := clock.Now().Add(-time.Minute)
		track(a, "runner-1", "wl-1", true).stats = backend.Stats{CPUPercent: 12.5, SampledAt: &sampled}
		be.setWorkloads(running("wl-1", "runner-1"))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() {
			a.sampleStats(ctx)
			close(done)
		}()
		synctest.Wait()
		select {
		case <-b.entered:
		default:
			t.Fatal("sampler did not start")
		}
		reports, err := a.ReconcileOnce(context.Background())
		if err != nil || len(reports) != 1 || reports[0].Phase != backend.PhaseRunning {
			t.Fatalf("lifecycle observation was blocked: %+v, %v", reports, err)
		}
		cancel()
		synctest.Wait()
		<-done
		if got := a.Runners(); len(got) != 1 || got[0].Stats.CPUPercent != 12.5 || got[0].Stats.SampledAt == nil || !got[0].Stats.SampledAt.Equal(sampled) {
			t.Fatalf("failed sampling replaced the last good reading: %+v", got)
		}
	})
}

func TestExpiredCreateDoesNotStartWorkload(t *testing.T) {
	for _, which := range []string{"provision", "token"} {
		t.Run(which, func(t *testing.T) {
			a, tr, be, _ := newAgent(t, 2)
			task := createTask("expired", "runner")
			if which == "provision" {
				task.Spec.StartBefore = a.now()
			} else {
				task.Spec.Credentials.ExpiresAt = a.now()
			}
			released := false
			a.handleCreate(context.Background(), task, func() { released = true })
			result := <-tr.results
			if result.OK || !strings.Contains(result.Error, "expired") || !released {
				t.Fatalf("expired create: %+v, released %v", result, released)
			}
			if created, _, removed := be.counts(); created != 0 || removed != 0 {
				t.Fatal("expired task changed workloads")
			}
		})
	}
}

func TestExpiredRedeliveryAdoptsExistingWorkload(t *testing.T) {
	a, tr, be, _ := newAgent(t, 2)
	task := createTask("first", "runner")
	a.handleCreate(context.Background(), task, func() {})
	first := <-tr.results
	if !first.OK {
		t.Fatalf("first create: %+v", first)
	}
	task.Spec.StartBefore = a.now().Add(-time.Minute)
	task.Spec.Credentials.ExpiresAt = a.now().Add(-time.Minute)
	a.handleCreate(context.Background(), task, func() {})
	again := <-tr.results
	if !again.OK || again.Handle != first.Handle {
		t.Fatalf("expired redelivery: %+v", again)
	}
	if created, _, removed := be.counts(); created != 1 || removed != 0 {
		t.Fatal("redelivery replaced existing workload")
	}
}

func TestOutdatedRunnerExplainsRequiredUpdate(t *testing.T) {
	if exitFault(7) != store.FaultConfig || !strings.Contains(entrypointExitHint(7), "outdated") {
		t.Fatal("outdated runner lacks actionable classification")
	}
}

type failingStatsBackend struct{ *fakeBackend }

func (failingStatsBackend) Stats(context.Context, backend.Handle) (backend.Stats, error) {
	return backend.Stats{}, errors.New("cgroup unavailable")
}

// Readings that stop coming leave the controller sizing from a stale picture, and the
// failure itself is only at Debug: the agent counts them in a row so the run can be said
// once, and a good sample ends it.
func TestStatsFailuresAreCountedInARowAndEndedByAGoodSample(t *testing.T) {
	a, _, be, _ := newAgent(t, 2)
	be.setWorkloads(running("wl-1", "runner-1"))
	tr := track(a, "runner-1", "wl-1", true)
	a.opts.Backends = backend.NewRegistry(failingStatsBackend{be})
	for range statsMissesSaid + 2 {
		a.sampleStats(context.Background())
	}
	a.mu.Lock()
	missed := tr.statsMisses
	a.mu.Unlock()
	if missed != statsMissesSaid+2 {
		t.Fatalf("misses = %d, want %d", missed, statsMissesSaid+2)
	}
	a.opts.Backends = backend.NewRegistry(be)
	a.sampleStats(context.Background())
	a.mu.Lock()
	missed = tr.statsMisses
	a.mu.Unlock()
	if missed != 0 {
		t.Errorf("misses = %d after a good sample, want 0", missed)
	}
}
