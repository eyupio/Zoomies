package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// How long a task may stay in flight before it is offered to the host again.
//
// A create has to cover a cold image pull on a slow link, which is minutes;
// the others are quick. Log tasks are never re-queued -- see requeueAfter.
const (
	createLease = 20 * time.Minute
	stopLease   = 10 * time.Minute
	removeLease = 5 * time.Minute
	// maxTaskAttempts stops a task from being redelivered forever to a host
	// whose agent is gone. After this the runner's provision timeout is what
	// notices, and it says so on the Runners page.
	maxTaskAttempts = 3
	// maxTasksPerPoll bounds one response so a host that has been offline does
	// not receive a hundred tasks in one batch.
	maxTasksPerPoll = 20
)

// taskQueues holds one queue per host.
//
// The queues live in memory only. That is a deliberate choice rather than an
// omission: every task is derived from state the database already holds, so
// the next reconcile re-derives whatever a restart dropped. Persisting them
// would add a second source of truth that could disagree with the runners
// table, which is exactly the failure this design avoids.
type taskQueues struct {
	mu sync.Mutex
	qs map[string]*taskQueue
}

func newTaskQueues() *taskQueues { return &taskQueues{qs: map[string]*taskQueue{}} }

func (t *taskQueues) get(hostID string) *taskQueue {
	t.mu.Lock()
	defer t.mu.Unlock()
	q, ok := t.qs[hostID]
	if !ok {
		q = &taskQueue{wake: make(chan struct{}, 1), inflight: map[string]*leasedTask{}}
		t.qs[hostID] = q
	}
	return q
}

// all returns every queue, for the sweep that re-queues expired leases.
func (t *taskQueues) all() map[string]*taskQueue {
	t.mu.Lock()
	defer t.mu.Unlock()
	return maps.Clone(t.qs)
}

// taskQueue is one host's pending and in-flight work.
type taskQueue struct {
	mu       sync.Mutex
	pending  []*leasedTask
	inflight map[string]*leasedTask
	// wake carries a single token so a poll that is blocked returns as soon as
	// a task is enqueued, without the enqueuer ever blocking.
	wake chan struct{}
}

type leasedTask struct {
	task     agent.Task
	key      string
	attempts int
	expires  time.Time
}

// taskKey is what makes enqueueing idempotent: one outstanding task per kind
// per runner. A reconcile that decides "remove this failed runner" on every
// pass therefore leaves one remove task, not one every ten seconds.
func taskKey(t agent.Task) string {
	if t.Kind == agent.TaskPrewarmImage {
		return string(t.Kind) + ":" + t.PoolID + ":" + t.Image
	}
	if t.StreamID != "" {
		return string(t.Kind) + "|stream:" + t.StreamID
	}
	return string(t.Kind) + "|" + t.RunnerID
}

// requeueAfter is how long a task of this kind may be in flight before the
// controller assumes the agent died with it.
func requeueAfter(kind agent.TaskKind) time.Duration {
	switch kind {
	case agent.TaskCreateRunner:
		return createLease
	case agent.TaskStopRunner:
		return stopLease
	case agent.TaskRemoveRunner:
		return removeLease
	default:
		// Log relays are tied to a browser that has since gone away, so
		// redelivering one would open a stream nobody is reading.
		return 0
	}
}

// enqueue adds a task unless an equivalent one is already outstanding.
func (q *taskQueue) enqueue(t agent.Task) bool {
	key := taskKey(t)
	q.mu.Lock()
	for _, p := range q.pending {
		if p.key == key {
			q.mu.Unlock()
			return false
		}
	}
	for _, f := range q.inflight {
		if f.key == key {
			q.mu.Unlock()
			return false
		}
	}
	q.pending = append(q.pending, &leasedTask{task: t, key: key})
	q.mu.Unlock()

	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

// take moves up to n pending tasks into flight and returns them.
func (q *taskQueue) take(n int, now time.Time) []agent.Task {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return nil
	}
	n = min(n, len(q.pending))
	out := make([]agent.Task, 0, n)
	for _, lt := range q.pending[:n] {
		lt.attempts++
		lt.task.IssuedAt = now
		if lease := requeueAfter(lt.task.Kind); lease > 0 {
			lt.expires = now.Add(lease)
		} else {
			lt.expires = time.Time{}
		}
		q.inflight[lt.task.ID] = lt
		out = append(out, lt.task)
	}
	q.pending = slices.Delete(q.pending, 0, n)
	return out
}

// complete clears a task's lease once its result has arrived, returning the
// task if it was still on record -- it is not after a controller restart.
func (q *taskQueue) complete(taskID string) (agent.Task, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	lt, ok := q.inflight[taskID]
	if ok {
		delete(q.inflight, taskID)
		return lt.task, true
	}
	return agent.Task{}, false
}

// lifecycleTask reports whether a task's failure leaves its runner unusable.
//
// A create, stop or remove that fails does; a log relay that could not be
// opened says nothing about the runner at all -- the container is where it
// was, doing what it was doing. An unknown kind is treated as lifecycle,
// which is the only safe reading of a failure whose task nobody can name.
func lifecycleTask(kind agent.TaskKind) bool {
	switch kind {
	case agent.TaskStreamLogs, agent.TaskCancelLogs, agent.TaskPrewarmImage:
		return false
	}
	return true
}

// sweep re-queues tasks whose lease has expired and drops the ones that have
// been tried too often. It returns how many of each happened.
func (q *taskQueue) sweep(now time.Time) (requeued, dropped int) {
	q.mu.Lock()
	for id, lt := range q.inflight {
		if lt.expires.IsZero() || now.Before(lt.expires) {
			continue
		}
		delete(q.inflight, id)
		if lt.attempts >= maxTaskAttempts {
			dropped++
			continue
		}
		q.pending = append(q.pending, lt)
		requeued++
	}
	q.mu.Unlock()
	if requeued > 0 {
		select {
		case q.wake <- struct{}{}:
		default:
		}
	}
	return requeued, dropped
}

func (q *taskQueue) depth() (pending, inflight int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending), len(q.inflight)
}

// enqueue puts a task on a host's queue, stamping the fields every task needs.
func (c *Controller) enqueue(hostID string, t agent.Task) bool {
	if hostID == "" {
		c.log.Error("dropping a task with no host to run it on", "kind", t.Kind, "runner", t.RunnerID)
		return false
	}
	if t.ID == "" {
		t.ID = "task_" + store.NewSecret(8)
	}
	if t.IssuedAt.IsZero() {
		t.IssuedAt = c.Now()
	}
	return c.queues.get(hostID).enqueue(t)
}

// PrewarmPool queues one idempotent image preparation task on every matching
// healthy host. A failure is recorded per host and never affects scheduling.
func (c *Controller) PrewarmPool(ctx context.Context, p *store.Pool) (int, error) {
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return 0, err
	}
	if p.Backend == store.BackendProcess {
		return 0, fmt.Errorf("the process backend does not support image prewarming")
	}
	n := 0
	for _, h := range hosts {
		if !h.Healthy(c.Now()) || h.Cordoned || !slices.Contains(h.Backends, string(p.Backend)) || !matchesSelector(p.HostSelector, h.Labels) {
			continue
		}
		_ = c.st.SetPoolPrewarm(ctx, p.ID, h.ID, p.Image, "pending", "", "")
		if c.enqueue(h.ID, agent.Task{Kind: agent.TaskPrewarmImage, PoolID: p.ID, Backend: p.Backend, Image: p.Image, PullPolicy: p.PullPolicy, IssuedAt: c.Now()}) {
			n++
		}
	}
	return n, nil
}

func matchesSelector(want, got store.StringMap) bool {
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// The agent-facing API
// ---------------------------------------------------------------------------

// Join enrols an agent: it redeems the single-use join token, creates or
// updates the host row, and mints the long-lived agent token that every later
// request carries. The plaintext token is returned exactly once.
func (c *Controller) Join(ctx context.Context, req agent.JoinRequest, ip string) (*agent.JoinResponse, error) {
	return c.join(ctx, req, ip, false)
}

func (c *Controller) join(ctx context.Context, req agent.JoinRequest, ip string, embedded bool) (*agent.JoinResponse, error) {
	if req.ProtocolVersion != 0 && req.ProtocolVersion != agent.ProtocolVersion {
		return nil, fmt.Errorf("this agent speaks protocol version %d and this controller speaks %d; "+
			"upgrade whichever is older so the two match", req.ProtocolVersion, agent.ProtocolVersion)
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, errors.New("the join request carried no host name; start the agent with --name or set agent.name, since it is how this host appears in the UI")
	}

	// Reuse the row when a host of this name already exists, so re-joining a
	// rebuilt machine does not leave a duplicate behind. The join token has to
	// be redeemed against the final ID, so this lookup comes first.
	existing, err := c.st.GetHostByName(ctx, name)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("looking up host %q: %w", name, err)
	}
	hostID := store.NewID(store.PrefixHost)
	if existing != nil {
		hostID = existing.ID
	}

	var tokenLabels store.StringMap
	tokenCapacity := 0
	if !embedded {
		tok, err := c.authsvc.RedeemJoinToken(ctx, req.JoinToken, hostID)
		if err != nil {
			return nil, err
		}
		tokenLabels, tokenCapacity = tok.Labels, tok.Capacity
	}

	capacity := req.Capacity
	if tokenCapacity > 0 {
		// The token was minted by an operator who said how much of this host
		// they were prepared to give Zoomies; that wins over the agent's guess.
		capacity = tokenCapacity
	}
	if capacity < 1 {
		capacity = 1
	}

	labels := store.StringMap{}
	maps.Copy(labels, tokenLabels)
	for k, v := range req.Labels {
		labels[k] = v
	}

	probed := hostBackends(req.Backends)
	plaintext, hash := auth.NewAgentToken()
	now := c.Now()
	h := &store.Host{
		ID:            hostID,
		Name:          name,
		Address:       firstNonEmpty(req.Address, ip),
		Embedded:      embedded,
		Capacity:      capacity,
		Backends:      probed.Kinds(),
		BackendInfo:   probed,
		Labels:        labels,
		OS:            req.OS,
		Arch:          req.Arch,
		Version:       req.Version,
		TokenHash:     hash,
		LastHeartbeat: now,
	}
	if existing != nil {
		// A host that is joining again is, to the fleet, a new machine: it has
		// a new token and no idea what the runners recorded against the old row
		// were. Those rows go with the row (ON DELETE CASCADE) rather than
		// lingering as runners nobody will ever report on again; their GitHub
		// registrations are tidied by the reaper and their workloads, if any
		// survive, by the agent's own orphan sweep. The ID is kept so that
		// audit rows and bookmarks still resolve.
		if stale, err := c.st.ListRunnersForHost(ctx, existing.ID); err == nil && len(stale) > 0 {
			c.log.Warn("a host joined again while runners were still recorded against it; those rows are being dropped",
				"host", existing.ID, "name", name, "runners", len(stale))
		}
		if err := c.st.DeleteHost(ctx, existing.ID); err != nil {
			return nil, fmt.Errorf("replacing the previous registration of host %s: %w", name, err)
		}
		h.Embedded = existing.Embedded || embedded
		h.Cordoned = existing.Cordoned
	}
	if err := c.st.CreateHost(ctx, h); err != nil {
		return nil, fmt.Errorf("registering host %s: %w", name, err)
	}

	c.markHostSeen(h.ID, true)
	c.publishHost(h)
	c.authsvc.Auditor().Act(ctx, auth.AgentIdentity(h, ip), "host.join", "host", h.ID, map[string]any{
		"name":     h.Name,
		"capacity": h.Capacity,
		"backends": []string(h.Backends),
		"embedded": h.Embedded,
	})
	c.log.Info("a host joined", "host", h.ID, "name", h.Name, "capacity", h.Capacity,
		"backends", strings.Join(h.Backends, ","), "embedded", h.Embedded)

	// A new host changes where runners can be placed.
	c.Nudge()

	return &agent.JoinResponse{
		HostID:            h.ID,
		AgentToken:        plaintext,
		ControllerVersion: version.Short(),
		HeartbeatInterval: c.heartbeatInterval().String(),
	}, nil
}

// Heartbeat records that a host is alive, merges anything its agent observed,
// and tells the agent whether the controller still recognises it.
func (c *Controller) Heartbeat(ctx context.Context, hostID string, req agent.HeartbeatRequest) (*agent.HeartbeatResponse, error) {
	h, err := c.st.GetHost(ctx, hostID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Wrapping agent.ErrHostGone is what makes the embedded agent
			// behave like a remote one. A standalone agent learns this from a
			// 404 and its transport wraps the sentinel itself; in-process
			// there is no status code, so the sentinel has to come from here
			// or the daemon's "stop and tell the operator to re-join" path
			// never fires and it retries a host that will never exist again.
			return nil, fmt.Errorf("%w: this controller has no record of host %s; delete the agent's credentials file and join again with a fresh join token",
				agent.ErrHostGone, hostID)
		}
		return nil, err
	}

	now := c.Now()
	wasHealthy := h.Healthy(now)
	// The heartbeat's capacity is deliberately not written. Capacity is set
	// at join -- from the join token when it carries one, else from the
	// agent -- and from then on the host row is what an operator edits: the
	// Hosts API says "use 0 to stop this host taking new runners", and a
	// heartbeat writing the agent's configured number back thirty seconds
	// later would undo exactly that.
	if err := c.st.Heartbeat(ctx, hostID, now); err != nil {
		return nil, err
	}

	// Only write the row back when the agent is telling us something new: a
	// heartbeat every 30 seconds per host is not worth an UPDATE each time.
	//
	// An agent re-probes its backends as it runs, so this is also how a host
	// that started before its Docker daemon -- or before its user was in the
	// docker group -- stops advertising nothing and becomes schedulable. That
	// recovery is worth a log line and a scheduling pass: until it happens,
	// every pool on that backend looks healthy and quietly starts no runner.
	probed := hostBackends(req.Backends)
	kinds := probed.Kinds()
	backendsChanged := len(probed) > 0 && !slices.Equal(kinds, h.Backends)
	changed := backendsChanged ||
		(len(probed) > 0 && !slices.Equal(probed, h.BackendInfo)) ||
		(req.Version != "" && req.Version != h.Version)
	if changed {
		was := h.Backends
		if len(probed) > 0 {
			h.Backends = kinds
			h.BackendInfo = probed
		}
		h.Version = firstNonEmpty(req.Version, h.Version)
		h.LastHeartbeat = now
		if err := c.st.UpdateHost(ctx, h); err != nil {
			c.log.Warn("could not record what a host reported about itself", "host", hostID, "error", err)
		} else if backendsChanged {
			c.log.Info("a host's backends changed", "host", hostID, "name", h.Name,
				"was", strings.Join(was, ","), "now", strings.Join(h.Backends, ","),
				"detail", unavailableDetail(probed))
			c.publishHost(h)
			// A host that has just gained a backend may be the one a stalled
			// pool has been waiting for.
			c.Nudge()
		}
	}

	if len(req.Runners) > 0 {
		c.applyReports(ctx, hostID, req.Runners)
	}

	h.LastHeartbeat = now
	if !wasHealthy {
		// The host was over its heartbeat window and has come back; the Hosts
		// page should say so without waiting for the health sweep.
		c.publishHost(h)
	}
	c.setHostHealth(hostID, true)

	return &agent.HeartbeatResponse{
		OK:                true,
		Cordoned:          h.Cordoned,
		ControllerVersion: version.Short(),
		ResyncRequested:   c.markHostSeen(hostID, false),
	}, nil
}

// PollTasks blocks until this host has work or wait elapses.
//
// It waits on a per-host channel rather than sleeping in a loop, so a task
// enqueued by a reconcile reaches the agent in the same instant rather than up
// to a poll interval later.
func (c *Controller) PollTasks(ctx context.Context, hostID string, wait time.Duration) (*agent.TaskBatch, error) {
	if hostID == "" {
		return nil, errors.New("a task poll carried no host ID; the agent must send the identity it was given at join")
	}
	if wait <= 0 || wait > agent.DefaultPollWait {
		wait = agent.DefaultPollWait
	}
	q := c.queues.get(hostID)

	if tasks := q.take(maxTasksPerPoll, c.Now()); len(tasks) > 0 {
		return &agent.TaskBatch{Tasks: tasks}, nil
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		// A cancelled long poll is the client hanging up, not an error worth
		// reporting; the tasks are still queued for the next one.
		return &agent.TaskBatch{}, nil
	case <-timer.C:
		return &agent.TaskBatch{}, nil
	case <-q.wake:
		return &agent.TaskBatch{Tasks: q.take(maxTasksPerPoll, c.Now())}, nil
	}
}

// ReportResult applies the outcome of one task and clears its lease.
func (c *Controller) ReportResult(ctx context.Context, hostID string, res agent.TaskResult) error {
	task, known := c.queues.get(hostID).complete(res.TaskID)
	if known && task.Kind == agent.TaskPrewarmImage {
		state := "succeeded"
		if !res.OK {
			state = "failed"
		}
		return c.st.SetPoolPrewarm(ctx, task.PoolID, hostID, task.Image, state, res.Digest, res.Error)
	}
	kind := res.Kind
	if kind == "" && known {
		kind = task.Kind
	}
	if res.RunnerID == "" {
		return nil
	}

	r, err := c.st.GetRunner(ctx, res.RunnerID)
	if err != nil {
		// The runner has been pruned, or belongs to another controller's
		// database. Nothing to apply; the agent will reap the workload.
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if r.HostID != hostID {
		return fmt.Errorf("host %s reported on runner %s, which belongs to host %s", hostID, r.ID, r.HostID)
	}

	if res.Handle != "" && string(res.Handle) != r.ContainerID {
		if err := c.st.SetRunnerContainer(ctx, r.ID, string(res.Handle)); err != nil {
			c.log.Warn("could not record a runner's workload handle", "runner", r.ID, "error", err)
		}
	}
	if kind == agent.TaskCreateRunner && res.OK && res.ContainerStartedAt != nil {
		_ = c.st.SetRunnerStartup(ctx, r.ID, res.ImagePullDuration, res.ContainerStartedAt)
		if p, err := c.st.GetPool(ctx, r.PoolID); err == nil {
			observeDuration(c.metrics.createToContainer, p.Name, string(p.Backend), r.CreatedAt, *res.ContainerStartedAt)
		}
	}
	if res.Digest != "" {
		_ = c.st.SetRunnerImageDigest(ctx, r.ID, res.Digest)
	}

	state := res.State
	message := res.Error
	if !res.OK {
		if !lifecycleTask(kind) {
			// The relay could not be opened, which the viewer has been told;
			// the runner itself is untouched, and failing it here would have
			// the next reconcile tear down a container that may be mid-job.
			c.log.Info("a log task failed; the runner is left as it is",
				"runner", r.ID, "task", res.TaskID, "kind", kind, "error", res.Error)
			return nil
		}
		// A lifecycle task that failed leaves the runner unusable; saying so
		// on the Runners page is the whole point of reporting it.
		state = store.RunnerFailed
		if message == "" {
			message = "the agent could not complete task " + res.TaskID
		}
	}
	c.applyRunnerState(ctx, r, state, message)
	return nil
}

// ReportRunners merges an agent's observations outside the heartbeat cycle, so
// a runner coming up is visible immediately rather than up to a beat later.
func (c *Controller) ReportRunners(ctx context.Context, hostID string, reports []agent.RunnerReport) error {
	if hostID == "" {
		return errors.New("a runner report carried no host ID; the agent must send the identity it was given at join")
	}
	c.applyReports(ctx, hostID, reports)
	return nil
}

// applyReports folds each observation into the runner's authoritative state.
func (c *Controller) applyReports(ctx context.Context, hostID string, reports []agent.RunnerReport) {
	for _, rep := range reports {
		if rep.RunnerID == "" {
			continue
		}
		r, err := c.st.GetRunner(ctx, rep.RunnerID)
		if err != nil {
			continue
		}
		if r.HostID != hostID {
			c.log.Warn("a host reported on a runner it does not own",
				"host", hostID, "runner", r.ID, "owner", r.HostID)
			continue
		}
		if rep.Handle != "" && string(rep.Handle) != r.ContainerID {
			_ = c.st.SetRunnerContainer(ctx, r.ID, string(rep.Handle))
		}
		if rep.GitHubRunnerID != 0 && r.GitHubRunnerID == 0 {
			_ = c.st.SetRunnerGitHubID(ctx, r.ID, rep.GitHubRunnerID)
		}
		if rep.Stats.CPUPercent != 0 || rep.Stats.MemoryBytes != 0 {
			_ = c.st.SetRunnerResourceUsage(ctx, r.ID, rep.Stats.CPUPercent, rep.Stats.MemoryBytes)
		}

		state := rep.State
		if state == "" && rep.Phase == backend.PhaseRunning && r.State == store.RunnerRegistering {
			// The agent stops asserting a state once the workload is up,
			// because whether GitHub has handed it a job is not the agent's
			// call. A registered runner with nothing to do is idle, and the
			// webhook is what moves it to busy.
			state = store.RunnerIdle
		}
		c.applyRunnerState(ctx, r, state, rep.Message)
	}
}

// applyRunnerState performs a reported transition when it is legal, and
// publishes it. An illegal one is dropped rather than forced: the store's
// state machine is what stops a confused agent corrupting the accounting.
func (c *Controller) applyRunnerState(ctx context.Context, r *store.Runner, state store.RunnerState, message string) {
	if state == "" || state == r.State {
		return
	}
	if !state.Valid() || !store.CanTransition(r.State, state) {
		c.log.Debug("ignoring a runner state an agent reported out of order",
			"runner", r.ID, "from", r.State, "to", state)
		return
	}
	updated, err := c.st.TransitionRunner(ctx, r.ID, state, message)
	if err != nil {
		c.log.Warn("could not apply a runner state an agent reported", "runner", r.ID, "state", state, "error", err)
		return
	}
	if (state == store.RunnerIdle || state == store.RunnerBusy) && updated.RegisteredAt != nil {
		if p, e := c.st.GetPool(ctx, r.PoolID); e == nil {
			if r.ContainerStartedAt != nil {
				observeDuration(c.metrics.containerToRegistered, p.Name, string(p.Backend), *r.ContainerStartedAt, *updated.RegisteredAt)
			}
			observeDuration(c.metrics.registeredToReady, p.Name, string(p.Backend), *updated.RegisteredAt, c.Now())
		}
	}
	c.publishRunner(ctx, events.KindRunnerUpdated, updated)
	if state == store.RunnerFailed {
		// A clean exit under a job is the ordinary race between GitHub's
		// completed delivery and the agent noticing the container has gone;
		// a failure is not, and the job it was running needs to say so.
		c.noteRunnerLost(ctx, r, message)
	}
	if state.Terminal() {
		// A runner that has gone frees host capacity, so the next placement
		// decision should happen now rather than on the next tick.
		c.Nudge()
	}
}

// ---------------------------------------------------------------------------
// Host health
// ---------------------------------------------------------------------------

// checkHostHealth publishes a host event whenever a host's health flips, so
// the UI shows an agent going quiet without anyone refreshing.
func (c *Controller) checkHostHealth(ctx context.Context) {
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return
	}
	now := c.Now()
	for _, h := range hosts {
		healthy := h.Healthy(now)
		if c.setHostHealth(h.ID, healthy) {
			c.publishHost(h)
			if healthy {
				c.log.Info("a host is healthy again", "host", h.ID, "name", h.Name)
			} else {
				c.log.Warn("a host has stopped sending heartbeats; its runners are unreachable",
					"host", h.ID, "name", h.Name, "last_heartbeat", h.LastHeartbeat,
					"timeout", store.HeartbeatTimeout)
				// Runners on a silent host no longer count as usable capacity.
				c.Nudge()
			}
		}
	}
}

// setHostHealth records a host's health and reports whether it changed.
func (c *Controller) setHostHealth(id string, healthy bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	prev, known := c.hostHealthy[id]
	c.hostHealthy[id] = healthy
	return known && prev != healthy
}

// markHostSeen records that a host has been in touch since this process
// started, returning true the first time -- which is when the agent is asked
// to send a full runner report, because the controller's in-memory view of
// that host is empty.
func (c *Controller) markHostSeen(id string, force bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resynced[id] && !force {
		return false
	}
	c.resynced[id] = true
	return true
}

func (c *Controller) heartbeatInterval() time.Duration {
	if d := c.cfg.Agent.HeartbeatInterval; d > 0 {
		return d
	}
	return 30 * time.Second
}

// hostBackends converts an agent's probe into the form the store keeps. The
// whole probe is persisted, not just the kinds that answered: "this host has no
// docker" and "this host has docker but the agent cannot read its socket" are
// the same row to the scheduler and completely different to an operator.
func hostBackends(infos []backend.Info) store.HostBackends {
	if len(infos) == 0 {
		return nil
	}
	out := make(store.HostBackends, 0, len(infos))
	for _, i := range infos {
		out = append(out, store.HostBackend{
			Kind:         i.Kind,
			Available:    i.Available,
			Version:      i.Version,
			Rootless:     i.Rootless,
			Endpoint:     i.Endpoint,
			Detail:       i.Detail,
			SupportsDinD: i.SupportsDinD,
		})
	}
	slices.SortFunc(out, func(a, b store.HostBackend) int {
		return strings.Compare(string(a.Kind), string(b.Kind))
	})
	return out
}

// unavailableDetail summarises the backends a host reported it cannot use, for
// the log line that records a change. It is empty when everything answered.
func unavailableDetail(probed store.HostBackends) string {
	var parts []string
	for _, i := range probed {
		if !i.Available {
			parts = append(parts, string(i.Kind)+": "+firstNonEmpty(i.Detail, "unavailable"))
		}
	}
	return strings.Join(parts, "; ")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// The embedded agent
// ---------------------------------------------------------------------------

// EmbeddedTransport returns an agent.Transport that calls this controller
// directly. It is what makes the single-VM case one process: no listener, no
// TLS, no token on the wire.
func (c *Controller) EmbeddedTransport() agent.Transport { return &embeddedTransport{c: c} }

type embeddedTransport struct {
	c *Controller

	mu     sync.Mutex
	hostID string
}

func (t *embeddedTransport) host() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.hostID
}

// Join enrols the in-process agent. It carries no join token: the caller is
// this same process, so there is no identity to prove that the process itself
// does not already have.
func (t *embeddedTransport) Join(ctx context.Context, req agent.JoinRequest) (*agent.JoinResponse, error) {
	return t.c.join(ctx, req, "127.0.0.1", true)
}

func (t *embeddedTransport) Heartbeat(ctx context.Context, req agent.HeartbeatRequest) (*agent.HeartbeatResponse, error) {
	return t.c.Heartbeat(ctx, t.host(), req)
}

func (t *embeddedTransport) PollTasks(ctx context.Context, wait time.Duration) (*agent.TaskBatch, error) {
	return t.c.PollTasks(ctx, t.host(), wait)
}

func (t *embeddedTransport) ReportResult(ctx context.Context, res agent.TaskResult) error {
	return t.c.ReportResult(ctx, t.host(), res)
}

func (t *embeddedTransport) ReportRunners(ctx context.Context, reports []agent.RunnerReport) error {
	return t.c.ReportRunners(ctx, t.host(), reports)
}

// OpenLogStream hands the agent a pipe whose read half goes straight into the
// relay, which is the in-process equivalent of the chunked POST a standalone
// agent makes.
func (t *embeddedTransport) OpenLogStream(ctx context.Context, streamID string) (io.WriteCloser, error) {
	if strings.TrimSpace(streamID) == "" {
		return nil, errors.New("controller: cannot open a log stream without a stream ID")
	}
	pr, pw := io.Pipe()
	go func() {
		err := t.c.AcceptLogStream(streamID, pr)
		// Closing with the error makes the agent's next Write fail rather than
		// block on a pipe nobody is draining.
		_ = pr.CloseWithError(err)
	}()
	return pw, nil
}

func (t *embeddedTransport) SetCredentials(hostID, _ string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.hostID = hostID
}

func (t *embeddedTransport) Describe() string { return "embedded controller" }

// StartEmbeddedAgent runs an agent inside the controller process, so a single
// VM needs exactly one process and one systemd unit.
//
// It joins itself with a short-lived token it mints on the spot: the embedded
// agent is not a remote party, and requiring an operator to paste a token into
// their own controller would be ceremony without a threat model behind it.
func (c *Controller) StartEmbeddedAgent(ctx context.Context, cfg *config.Config) error {
	if cfg == nil {
		cfg = c.cfg
	}
	if c.backends == nil || len(c.backends.Kinds()) == 0 {
		return errors.New("controller: no runner backends are registered, so the embedded agent has no way to start runners; " +
			"install Docker or Podman, or set agent.backend to process")
	}

	tr := c.EmbeddedTransport()
	a, err := agent.New(agent.Options{
		Name:              cfg.Agent.Name,
		WorkDir:           cfg.Agent.WorkDir,
		Capacity:          cfg.Agent.Capacity,
		Labels:            cfg.Agent.Labels,
		Backends:          c.backends,
		DefaultBackend:    store.BackendKind(cfg.Agent.Backend),
		Transport:         tr,
		HeartbeatInterval: cfg.Agent.HeartbeatInterval,
		FinishedRetention: cfg.Agent.FinishedRetention,
		Logger:            c.log,
		Clock:             c.clock,
	})
	if err != nil {
		return err
	}

	if err := c.adoptEmbeddedCredentials(ctx, a, tr, cfg); err != nil {
		return err
	}

	// The agent gets its own cancellation so that Stop can shut it down even
	// when the caller passed a context it does not control. Cancelling it is a
	// graceful stop: the agent leaves this host's runners running.
	agentCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.embedded = a
	c.embeddedCancel = cancel
	c.mu.Unlock()

	c.spawn("embedded-agent", agentCtx, func(ctx context.Context) {
		if err := a.Run(ctx); err != nil && ctx.Err() == nil {
			c.log.Error("the embedded agent stopped", "error", err)
		}
	})
	return nil
}

// stopEmbeddedAgent asks the in-process agent to shut down, if there is one.
func (c *Controller) stopEmbeddedAgent() {
	c.mu.Lock()
	cancel := c.embeddedCancel
	c.embeddedCancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// adoptEmbeddedCredentials reuses the identity a previous run persisted when
// the controller still recognises it, and joins afresh when it does not --
// which is what happens the first time, and after the database is replaced.
func (c *Controller) adoptEmbeddedCredentials(ctx context.Context, a *agent.Agent, tr agent.Transport, cfg *config.Config) error {
	creds, err := agent.Load(agent.StatePath(cfg.Agent.WorkDir))
	if err == nil && creds.Valid() {
		if h, herr := c.st.GetHost(ctx, creds.HostID); herr == nil &&
			cryptox.ConstantTimeEqual(h.TokenHash, cryptox.HashToken(creds.AgentToken)) {
			tr.SetCredentials(creds.HostID, creds.AgentToken)
			c.renameEmbeddedHost(ctx, h, cfg.Agent.Name)
			c.markHostSeen(creds.HostID, true)
			return nil
		}
	}

	_, plaintext, err := c.authsvc.CreateJoinToken(ctx, time.Minute, cfg.Agent.Labels, cfg.Agent.Capacity, "system")
	if err != nil {
		return fmt.Errorf("minting a join token for the embedded agent: %w", err)
	}
	// The embedded transport ignores the token; it is passed because Join is
	// the same code path a remote agent takes, and one path is easier to trust
	// than two.
	return a.Join(ctx, plaintext)
}

// renameEmbeddedHost brings the host row's name in line with the agent's
// configured one when the two have drifted apart.
//
// The name is recorded once, at join, and a container deployment joined under
// whatever hostname Docker gave the first container -- a random twelve hex
// digits -- before its compose file set one. The identity is the persisted
// credential, not the name, so the row is kept and renamed rather than
// re-joined; and it is only renamed when nothing else already answers to the
// new name, since two hosts called the same thing would be worse than one
// called 7096d9a9b798.
func (c *Controller) renameEmbeddedHost(ctx context.Context, h *store.Host, name string) {
	name = strings.TrimSpace(name)
	if name == "" || h.Name == name {
		return
	}
	if _, err := c.st.GetHostByName(ctx, name); !errors.Is(err, store.ErrNotFound) {
		return
	}
	was := h.Name
	h.Name = name
	if err := c.st.UpdateHost(ctx, h); err != nil {
		c.log.Warn("could not rename the embedded host", "host", h.ID, "was", was, "want", name, "error", err)
		return
	}
	c.log.Info("renamed the embedded host to its configured name", "host", h.ID, "was", was, "now", name)
	c.publishHost(h)
}

// EmbeddedAgent returns the in-process agent, or nil when this controller runs
// without one.
func (c *Controller) EmbeddedAgent() *agent.Agent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.embedded
}
