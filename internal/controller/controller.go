// Package controller is the wiring between everything else: it owns the
// reconcile loop, the GitHub client cache, webhook ingest, the agent task
// queue, the log relay and the background housekeeping.
//
// Nothing in here decides how many runners a pool should have -- that is
// internal/scheduler, and it is a pure function -- and nothing in here writes
// SQL, which is internal/store. What this package does is turn a decision into
// GitHub calls, database rows and tasks for an agent, in an order that leaves
// the fleet in a defensible state when any single step fails.
//
// The controller never dials an agent. Agents connect outbound, long-poll for
// tasks and POST their results, which is why the task queue and the log relay
// are shaped the way they are.
package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// SeedEnvVar names the environment variable that turns demo seeding on. The
// Playwright suite sets it (see web/tests/support/serve.mjs) so the UI has a
// fixture fleet to render.
const SeedEnvVar = "ZOOMIES_SEED_DEMO"

// Options are the collaborators the controller needs. Everything except the
// store and the configuration has a defensible default, because a caller that
// forgets the event bus should get a working controller rather than a nil
// dereference three loops later.
type Options struct {
	// Store is the only writer of persistent state.
	Store *store.Store
	// Config is the validated configuration; the controller reads the
	// scheduler tunables, the GitHub settings and the retention windows.
	Config *config.Config
	// Key unseals the GitHub App private keys and webhook secrets held in the
	// database. Without it no installation can be used.
	Key *cryptox.Key
	// Auth mints agent tokens, redeems join tokens and records audit rows.
	Auth *auth.Service
	// Events carries every state change to the UI's SSE stream.
	Events *events.Bus
	// GitHub builds a client per installation; nil uses the real GitHub App
	// factory, and tests pass one backed by github.NewFake.
	GitHub github.Factory
	// Backends is only needed when this process also runs the embedded agent.
	Backends *backend.Registry
	Logger   *slog.Logger
	// Clock is injectable so tests can freeze time.
	Clock func() time.Time
}

// Controller owns the control plane's moving parts and their lifecycles.
type Controller struct {
	st       *store.Store
	cfg      *config.Config
	key      *cryptox.Key
	authsvc  *auth.Service
	bus      *events.Bus
	factory  github.Factory
	backends *backend.Registry
	log      *slog.Logger
	clock    func() time.Time

	metrics *metrics
	clients *clientCache
	queues  *taskQueues
	relay   *logRelay

	// nudges is capacity 1 on purpose: it is a "there is work to look at"
	// flag, not a queue. Fifty webhooks in a second leave one token behind and
	// therefore cause one reconcile pass, not fifty.
	nudges chan struct{}
	// reconcileMu makes a pass mutually exclusive with itself, so a timer tick
	// landing on top of a nudge cannot double-create runners.
	reconcileMu sync.Mutex
	// passes counts completed reconciles; tests assert on coalescing with it.
	passes atomic.Uint64

	// pollingOnly records that no webhook has ever arrived, which the Overview
	// says out loud because a fleet scaling on the poller looks healthy until
	// somebody wonders why it is slow.
	pollingOnly atomic.Bool
	// pollPausedUntil is a rate-limit backoff, as Unix nanoseconds.
	pollPausedUntil atomic.Int64

	mu sync.Mutex
	// lastPlan is the most recent scheduler decision, kept so that Problems
	// can report the queued jobs no pool claimed without deciding again.
	lastPlan   *scheduler.Plan
	lastPlanAt time.Time
	// blocked remembers the reason each pool could not place a runner, so that
	// a fleet that cannot scale says so once rather than every tick.
	blocked map[string]string
	// hostHealthy remembers each host's last known health so that only a flip
	// publishes an event.
	hostHealthy map[string]bool
	// resynced records the hosts that have heartbeat since this process
	// started; the first heartbeat from each asks for a full runner report.
	resynced map[string]bool
	embedded *agent.Agent
	// embeddedCancel stops the in-process agent, which may have been started
	// with a context the controller does not otherwise control.
	embeddedCancel context.CancelFunc

	startMu sync.Mutex
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// New validates the options and builds a controller that is not yet running.
func New(opts Options) (*Controller, error) {
	if opts.Store == nil {
		return nil, errors.New("controller: no store; open one with store.Open before building the controller")
	}
	if opts.Config == nil {
		return nil, errors.New("controller: no configuration; pass the result of config.Load, or config.Default for an in-process controller")
	}
	if opts.Key == nil {
		return nil, errors.New("controller: no encryption key; GitHub App private keys and webhook secrets are sealed with it, " +
			"so set security.encryption_key_file (or ZOOMIES_ENCRYPTION_KEY) and pass the parsed key")
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "controller")

	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}

	bus := opts.Events
	if bus == nil {
		bus = events.New()
	}

	authsvc := opts.Auth
	if authsvc == nil {
		authsvc = auth.New(opts.Store, opts.Config, bus, auth.WithLogger(log), auth.WithClock(clock))
	}

	factory := opts.GitHub
	if factory == nil {
		factory = github.NewAppFactory(nil)
	}

	c := &Controller{
		st:          opts.Store,
		cfg:         opts.Config,
		key:         opts.Key,
		authsvc:     authsvc,
		bus:         bus,
		factory:     factory,
		backends:    opts.Backends,
		log:         log,
		clock:       clock,
		nudges:      make(chan struct{}, 1),
		hostHealthy: map[string]bool{},
		resynced:    map[string]bool{},
	}
	c.metrics = newMetrics(c)
	c.clients = newClientCache(c)
	c.queues = newTaskQueues()
	c.relay = newLogRelay(c)
	return c, nil
}

// Start launches the background loops and returns as soon as they are running.
// It does not block until they finish; Stop does that.
func (c *Controller) Start(ctx context.Context) error {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	if c.started {
		return errors.New("controller: already started")
	}

	if seedRequested() {
		if err := c.SeedDemo(ctx); err != nil {
			// Seeding is a development and test convenience; refusing to start
			// because of it would be worse than saying so and carrying on.
			c.log.Warn("demo seeding was requested but did not run", "env", SeedEnvVar, "error", err)
		}
	}

	// Knowing up front whether a webhook has ever arrived means the Overview
	// can answer "are we event-driven?" without waiting for the first poll.
	if last, err := c.st.LastDeliveryAt(ctx); err == nil {
		c.pollingOnly.Store(last.IsZero())
	}

	loopCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.started = true

	c.spawn("reconcile", loopCtx, c.reconcileLoop)
	c.spawn("reap", loopCtx, c.reapLoop)
	c.spawn("poller", loopCtx, c.pollLoop)
	c.spawn("installations", loopCtx, c.probeLoop)
	c.spawn("background", loopCtx, c.backgroundLoop)

	c.log.Info("controller started",
		"interval", c.schedulerInterval(),
		"webhook_path", c.cfg.GitHub.WebhookPath,
		"poll_fallback", c.cfg.GitHub.PollFallback,
		"version", version.Short())
	return nil
}

// spawn runs one named loop until its context is cancelled, tracking it so
// Stop can wait for it.
func (c *Controller) spawn(name string, ctx context.Context, fn func(context.Context)) {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() {
			// A panic in one loop must not take the whole controller with it:
			// the fleet keeps running, and the operator gets a stack.
			if r := recover(); r != nil {
				c.log.Error("controller loop panicked", "loop", name, "panic", r)
			}
		}()
		fn(ctx)
	}()
}

// Stop shuts the loops down gracefully and flushes what is worth keeping.
//
// It deliberately does not tear down runners. Restarting a controller must not
// kill jobs: the runners keep working, their agents keep reporting, and the
// next reconcile picks up where this one left off.
func (c *Controller) Stop(ctx context.Context) error {
	c.startMu.Lock()
	if !c.started {
		c.startMu.Unlock()
		return nil
	}
	c.started = false
	cancel := c.cancel
	c.startMu.Unlock()

	if cancel != nil {
		cancel()
	}
	c.stopEmbeddedAgent()

	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		c.log.Warn("controller shutdown timed out waiting for its loops; exiting anyway, runners are unaffected")
	}

	// One last sample so the Overview's sparkline does not show a gap that
	// looks like an outage when it was a restart.
	if err := c.sample(context.WithoutCancel(ctx)); err != nil {
		c.log.Debug("could not write a final fleet sample", "error", err)
	}
	c.relay.closeAll()
	c.log.Info("controller stopped; runners on every host are still running")
	return nil
}

// Store returns the database handle the API reads through.
func (c *Controller) Store() *store.Store { return c.st }

// Config returns the configuration this controller was built with.
func (c *Controller) Config() *config.Config { return c.cfg }

// Auth returns the authentication and audit service.
func (c *Controller) Auth() *auth.Service { return c.authsvc }

// Events returns the bus the API's SSE endpoint subscribes to.
func (c *Controller) Events() *events.Bus { return c.bus }

// Backends returns the backend registry, or nil in a controller that runs no
// embedded agent.
func (c *Controller) Backends() *backend.Registry { return c.backends }

// Now returns the controller's clock, always in UTC.
func (c *Controller) Now() time.Time { return c.clock().UTC() }

// Nudge wakes the reconcile loop immediately.
//
// It never blocks and never queues: the channel holds a single token, so a
// burst of webhooks costs one reconcile pass rather than one per delivery.
func (c *Controller) Nudge() {
	select {
	case c.nudges <- struct{}{}:
	default:
	}
}

// PollingOnly reports whether scaling is running on the fallback poller
// because no webhook has ever been received.
func (c *Controller) PollingOnly() bool { return c.pollingOnly.Load() }

// schedulerInterval is the reconcile period, with a floor so that a
// misconfigured zero does not spin the loop.
func (c *Controller) schedulerInterval() time.Duration {
	if d := c.cfg.Scheduler.Interval; d > 0 {
		return d
	}
	return 10 * time.Second
}

// policy converts the configured tunables into the scheduler's Policy.
func (c *Controller) policy() scheduler.Policy {
	return scheduler.Policy{
		ScaleUpDelay:      c.cfg.Scheduler.ScaleUpDelay,
		MaxRunnerLifetime: c.cfg.Scheduler.MaxRunnerLifetime,
		ProvisionTimeout:  c.cfg.Scheduler.ProvisionTimeout,
		MaxCreatesPerTick: c.cfg.Scheduler.MaxCreatesPerTick,
	}
}

// publish sends one event to the UI, ignoring the absence of a bus.
func (c *Controller) publish(kind events.Kind, topic string, payload any) {
	if c.bus == nil {
		return
	}
	c.bus.Publish(kind, topic, payload)
}

// publishRunner announces a runner change so the Runners page updates live.
func (c *Controller) publishRunner(kind events.Kind, r *store.Runner) {
	if r == nil {
		return
	}
	c.publish(kind, "runner:"+r.ID, r)
}

// publishHost announces a host change, which is how the Hosts page shows an
// agent going quiet without the operator refreshing.
func (c *Controller) publishHost(h *store.Host) {
	if h == nil {
		return
	}
	c.publish(events.KindHostUpdated, "host:"+h.ID, h)
}

// setLastPlan records the most recent decision for Problems to report on.
func (c *Controller) setLastPlan(p scheduler.Plan) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastPlan = &p
	c.lastPlanAt = c.Now()
}

// noteBlocked logs a pool that wanted runners and could not place any, and the
// moment it recovers. Both are logged once per change: the reconcile loop runs
// every few seconds, and a fleet that is one host short would otherwise fill
// the log with the same sentence until somebody noticed it.
func (c *Controller) noteBlocked(pp scheduler.PoolPlan) {
	c.mu.Lock()
	if c.blocked == nil {
		c.blocked = map[string]string{}
	}
	was, had := c.blocked[pp.PoolID]
	switch {
	case pp.Blocked == "":
		delete(c.blocked, pp.PoolID)
	default:
		c.blocked[pp.PoolID] = pp.Blocked
	}
	c.mu.Unlock()

	switch {
	case pp.Blocked != "" && pp.Blocked != was:
		c.log.Warn("a pool cannot place the runners it wants",
			"pool", pp.PoolName, "current", pp.Current, "desired", pp.Desired,
			"queued", pp.QueuedMatched, "reason", pp.Blocked, "fix", pp.BlockedFix)
	case pp.Blocked == "" && had:
		c.log.Info("a pool can place runners again", "pool", pp.PoolName)
	}
}

func (c *Controller) getLastPlan() (*scheduler.Plan, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastPlan, c.lastPlanAt
}

// seedRequested reports whether the demo fixtures were asked for.
func seedRequested() bool {
	v, ok := os.LookupEnv(SeedEnvVar)
	if !ok {
		return false
	}
	b, err := strconv.ParseBool(v)
	return err == nil && b
}

// unsealString opens a sealed column, turning a key mismatch into a message
// that names the situation rather than "cipher: message authentication failed".
func (c *Controller) unsealString(sealed []byte, what string) (string, error) {
	if len(sealed) == 0 {
		return "", nil
	}
	s, err := c.key.OpenString(sealed)
	if err != nil {
		return "", fmt.Errorf("%s: %w", what, err)
	}
	return s, nil
}
