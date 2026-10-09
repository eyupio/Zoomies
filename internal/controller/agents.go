package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/naming"
	"github.com/eyupio/zoomies/internal/scheduler"
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
	// A prewarm is a cold image pull like a create, and gets the same room. It
	// needs a lease at all because it is the one task with no runner behind it
	// to notice: without one it stayed in flight for ever, and since enqueue
	// refuses a key that is already outstanding, that pool was never prewarmed
	// on that host again -- not by the hourly refresh, not by editing the pool,
	// and not by asking for one.
	prewarmLease = 20 * time.Minute
	// toolFillLease outlasts the agent's own bound on a fill, so a fill is
	// only offered again when its agent went away, not while it downloads.
	toolFillLease = 50 * time.Minute
	// maxTaskAttempts stops a task from being redelivered forever to a host
	// whose agent is gone. After this the runner's provision timeout is what
	// notices, and it says so on the Runners page.
	maxTaskAttempts = 3
	// hostCheckLease sits above the agent's own bound on one run of its checks
	// (hosttune.MonitorRunTimeout, 45 s) with room for the poll and the result.
	// A check_host has no runner behind it to notice a lost task, so without a
	// lease it would stay in flight for ever, enqueue would refuse its key, and
	// the button would be dead for that host -- the prewarm failure above.
	hostCheckLease = 90 * time.Second
	// hostCheckPendingTTL is how long a check may sit unclaimed. An agent holds
	// a long poll and takes a task at once, so a task still pending after this
	// means nobody is listening, and delivering it hours later when the agent
	// comes back would run a check nobody is waiting for.
	hostCheckPendingTTL = 20 * time.Second
	// updateLease covers an update task, which the agent answers once it has
	// written one small request for the helper: seconds. Without a lease the task
	// would stay in flight for ever and enqueue would refuse it again, so an
	// agent that went away holding it would leave its host's attempt to time out.
	// The update itself is not covered: it restarts the agent, and its outcome
	// comes on a heartbeat.
	updateLease = 5 * time.Minute
	// maxTasksPerPoll bounds one response so a host that has been offline does
	// not receive a hundred tasks in one batch.
	maxTasksPerPoll = 20
	// pollShedThreshold is how many task polls may be held at once before the
	// controller starts asking agents to come back less often.
	//
	// An idle fleet costs one held connection and one goroutine per host for
	// up to DefaultPollWait, so the cost of the poll channel is the fleet's
	// size rather than its workload. The number is far above any single-VM
	// deployment and below what a controller of the documented shape holds
	// comfortably, so a fleet that grows past it is asked to spread out
	// instead of being refused.
	pollShedThreshold = 256
	// maxPollShed caps the wait a shed poll is asked for. It is deliberately
	// short: a task queued the instant after a shed answer waits this long
	// before the host hears about it, and a runner that starts fifteen seconds
	// late is a slower fleet, whereas one that starts a minute late is a
	// broken one.
	maxPollShed = 15 * time.Second
)

// shedFor says how long an agent that found no work should wait before polling
// again, given how many polls the controller is holding. Zero means "come
// straight back", which is the answer for every fleet below the threshold.
//
// The wait rises with the excess rather than switching on, because a step
// would move the whole fleet between two duty cycles at once and oscillate
// around the threshold.
func shedFor(inFlight int64) time.Duration {
	over := inFlight - pollShedThreshold
	if over <= 0 {
		return 0
	}
	return min(time.Duration(over)*maxPollShed/pollShedThreshold, maxPollShed)
}

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

// forget drops a host's queue entirely. Called when the host itself is
// deleted, so its entry does not linger in this map for the rest of the
// process's life -- every other exit a host takes (going unhealthy,
// cordoning) leaves the queue in place because the host might come back.
func (t *taskQueues) forget(hostID string) {
	t.mu.Lock()
	delete(t.qs, hostID)
	t.mu.Unlock()
}

// taskQueue is one host's pending and in-flight work.
type taskQueue struct {
	mu       sync.Mutex
	pending  []*leasedTask
	inflight map[string]*leasedTask
	// wake carries a single token so a poll that is blocked returns as soon as
	// a task is enqueued, without the enqueuer ever blocking.
	wake chan struct{}
	// polling is set while one of this host's task polls is held. A second
	// poll is answered at once and empty: a host has one agent, and an agent
	// polls one at a time, so a second held poll is a bug or an abuse, and
	// either way it would count against the fleet-wide threshold every other
	// host's polls are shed against.
	polling atomic.Bool
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
	// One fill per pool per host: a scan that finishes while the last fill
	// is still downloading leaves that one to finish rather than queueing a
	// second behind it.
	if t.Kind == agent.TaskFillToolCache {
		return string(t.Kind) + ":" + t.PoolID
	}
	// One per attempt, so a task left in flight by an attempt that has ended
	// does not hold back the next attempt's.
	if t.Kind == agent.TaskUpdateAgent {
		return string(t.Kind) + ":" + t.UpdateID
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
	case agent.TaskPrewarmImage:
		return prewarmLease
	case agent.TaskFillToolCache:
		return toolFillLease
	case agent.TaskCheckHost:
		return hostCheckLease
	case agent.TaskUpdateAgent:
		return updateLease
	default:
		// Log relays are tied to a browser that has since gone away, so
		// redelivering one would open a stream nobody is reading.
		return 0
	}
}

// maxAttempts is how many times a task of this kind may be delivered. A host
// check is read-only and the operator who pressed the button has given up long
// before a third delivery, so it gets one; every other kind keeps the shared
// bound.
func maxAttempts(kind agent.TaskKind) int {
	if kind == agent.TaskCheckHost {
		return 1
	}
	return maxTaskAttempts
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
		lt.task.Attempt = lt.attempts
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

// peek leaves the lease recoverable until all required result writes commit.
func (q *taskQueue) peek(taskID string) (agent.Task, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	lt, ok := q.inflight[taskID]
	if ok {
		return lt.task, true
	}
	return agent.Task{}, false
}

// complete clears the lease after durable result application.
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

type redelivery int

const (
	redelivered redelivery = iota
	notInFlight
	outOfAttempts
)

// redeliver moves a task the agent gave back unstarted from flight to the
// front of the queue, unless it is not in flight or has used its attempts.
func (q *taskQueue) redeliver(taskID string) redelivery {
	q.mu.Lock()
	lt, ok := q.inflight[taskID]
	if !ok {
		q.mu.Unlock()
		return notInFlight
	}
	if lt.attempts >= maxAttempts(lt.task.Kind) {
		q.mu.Unlock()
		return outOfAttempts
	}
	delete(q.inflight, taskID)
	lt.expires = time.Time{}
	q.pending = append([]*leasedTask{lt}, q.pending...)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return redelivered
}

// withdraw drops the pending tasks match picks, and with inFlight the ones an
// agent holds as well, and says how many went. A task taken out of flight is
// never offered again when its lease runs out, and its answer, if one comes,
// matches no task and changes nothing.
func (q *taskQueue) withdraw(match func(agent.Task) bool, inFlight bool) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	kept := q.pending[:0]
	for _, lt := range q.pending {
		if !match(lt.task) {
			kept = append(kept, lt)
		}
	}
	n := len(q.pending) - len(kept)
	clear(q.pending[len(kept):])
	q.pending = kept
	if inFlight {
		for id, lt := range q.inflight {
			if match(lt.task) {
				delete(q.inflight, id)
				n++
			}
		}
	}
	return n
}

// lifecycleTask reports whether a task's failure leaves its runner unusable.
//
// A create, stop or remove that fails does; a log relay that could not be
// opened says nothing about the runner at all -- the container is where it
// was, doing what it was doing.
//
// It is an allowlist, and that is the whole point of the shape. It used to
// name the exceptions and return true for everything else, so an unknown kind
// counted as lifecycle -- reasonable-sounding, and wrong in the one case it
// actually met: an agent one release behind does not ignore a task kind it has
// never heard of, it reports the task failed, and this controller then marked a
// perfectly healthy runner failed for it. An unknown kind now leaves the runner
// alone, which is the only conclusion available about a task neither side can
// name.
func lifecycleTask(kind agent.TaskKind) bool {
	switch kind {
	case agent.TaskCreateRunner, agent.TaskStopRunner, agent.TaskRemoveRunner:
		return true
	}
	return false
}

// sweep re-queues tasks whose lease has expired and drops the ones that have
// been tried too often. It returns how many were re-queued and which were
// dropped, because a dropped stop is a runner nothing will ever stop.
func (q *taskQueue) sweep(now time.Time) (requeued int, dropped []agent.Task) {
	q.mu.Lock()
	for id, lt := range q.inflight {
		if lt.expires.IsZero() || now.Before(lt.expires) {
			continue
		}
		delete(q.inflight, id)
		if lt.attempts >= maxAttempts(lt.task.Kind) {
			dropped = append(dropped, lt.task)
			continue
		}
		q.pending = append(q.pending, lt)
		requeued++
	}
	// Pending checks expire too: take overwrites IssuedAt on delivery, so this
	// only ever sees one that has not been delivered.
	kept := q.pending[:0]
	for _, lt := range q.pending {
		if lt.task.Kind == agent.TaskCheckHost && now.Sub(lt.task.IssuedAt) > hostCheckPendingTTL {
			dropped = append(dropped, lt.task)
			continue
		}
		kept = append(kept, lt)
	}
	clear(q.pending[len(kept):])
	q.pending = kept
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

// enqueueLifecycle queues a task that decides a runner's fate and records on
// the runner's own row when it was issued.
//
// Only lifecycle tasks are stamped. A log relay says nothing about whether a
// runner is progressing -- the container is where it was, doing what it was
// doing -- and a prewarm has no runner to stamp.
func (c *Controller) enqueueLifecycle(ctx context.Context, hostID string, t agent.Task) bool {
	if !c.mayAct() {
		return false
	}
	queued := c.enqueue(hostID, t)
	if queued {
		c.stampTaskIssued(ctx, t)
	}
	return queued
}

// stampTaskIssued records on a runner's row that its task has been issued.
//
// A failure is logged and dropped. The stamp is diagnostic -- it tells an
// operator, and a controller that has just restarted, how long this runner has
// been waiting on a task -- and losing it must never stop the task itself
// being handed to the host.
func (c *Controller) stampTaskIssued(ctx context.Context, t agent.Task) {
	if t.RunnerID == "" || !lifecycleTask(t.Kind) {
		return
	}
	issued := t.IssuedAt
	if issued.IsZero() {
		issued = c.Now()
	}
	if err := c.st.SetRunnerTaskIssued(ctx, t.RunnerID, issued); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		c.log.Debug("could not record when a runner's task was issued",
			"runner", t.RunnerID, "kind", t.Kind, "error", err)
	}
}

// PrewarmPool queues one idempotent image preparation task on every matching
// healthy host. A failure is recorded per host and never affects scheduling.
// ErrPrewarmUnsupported is returned for a pool whose backend has no image to
// pull ahead of time. It is a refusal the caller can act on, kept apart from a
// failure to list hosts so the API can answer the two differently.
var ErrPrewarmUnsupported = errors.New("the process backend does not support image prewarming; its runners install the runner archive themselves")

func (c *Controller) PrewarmPool(ctx context.Context, p *store.Pool) (int, error) {
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return 0, err
	}
	if p.Backend == store.BackendProcess {
		return 0, ErrPrewarmUnsupported
	}
	// The image the runners will be created from, which for a pool that gives
	// its jobs a daemon is not always the one the row names: pulling the other
	// one would warm nothing.
	image := c.RunnerImage(p)
	n := 0
	for _, h := range hosts {
		if !scheduler.HostCanRun(h, p, c.Now()) {
			continue
		}
		_ = c.st.SetPoolPrewarm(ctx, p.ID, h.ID, image, "pending", "", "")
		if c.enqueue(h.ID, agent.Task{Kind: agent.TaskPrewarmImage, PoolID: p.ID, Backend: p.Backend, Image: image, PullPolicy: p.PullPolicy, Spec: &backend.Spec{DockerMode: p.DockerMode}, IssuedAt: c.Now()}) {
			n++
		}
	}
	return n, nil
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
	// Refusals the agent's operator can act on are auth.Invalid, so the API
	// answers them with the reason; anything else below is this controller
	// failing, which the API answers with a request ID and logs.
	if req.ProtocolVersion != 0 && req.ProtocolVersion != agent.ProtocolVersion {
		return nil, auth.Invalid("this agent speaks protocol version %d and this controller speaks %d; "+
			"upgrade whichever is older so the two match", req.ProtocolVersion, agent.ProtocolVersion)
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, auth.Invalid("the join request carried no host name; start the agent with --name or set agent.name, since it is how this host appears in the UI")
	}

	// Reuse the row this machine already owns, so re-joining a rebuilt machine
	// does not leave a duplicate behind. The join token has to be redeemed
	// against the final ID, so this lookup comes first.
	//
	// The id it says it had is tried before the name, because the name is not
	// stable. It is derived from what the machine is, so an agent that has
	// learnt to measure its own CPUs and memory computes a different one from
	// an agent that had not -- and upgrading such a host therefore enrolled it
	// twice: a new row with the correct figures, beside the old row still being
	// kept alive by the process that had not been restarted yet. The operator
	// saw one machine as two, one of them reporting nothing.
	//
	// It is a claim, not a credential: the ownership check below is what makes
	// it safe, and it is the same check either lookup leads to.
	var existing *store.Host
	var err error
	if req.PreviousHostID != "" {
		existing, err = c.st.GetHost(ctx, req.PreviousHostID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("looking up host %s: %w", req.PreviousHostID, err)
		}
	}
	if existing == nil {
		// No id, or an id for a row that is gone: an agent too old to send one,
		// or a host deleted on the controller since it last joined.
		existing, err = c.st.GetHostByName(ctx, name)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("looking up host %q: %w", name, err)
		}
	}
	// What the host may call itself. The name is the agent's to choose and is
	// written into the problems list, where the UI offers whatever sits between
	// a pair of backticks as a command to copy, so a name that could pass for
	// one is refused here -- before the join token is redeemed, so whoever typed
	// it keeps a token that still works.
	//
	// Two cases are exempt because refusing them would take a working host out
	// of the fleet over a name nobody asked it to change. A host re-joining
	// under the name its row already carries predates this rule, and every
	// sentence that names it goes through naming.ForSentence instead; it may
	// not use the occasion to take a different refused name, which is why the
	// comparison is with the stored name and not with the rule alone. The
	// embedded agent's name comes from this controller's own configuration, and
	// refusing it would leave a single-VM controller with no host at all.
	if err := naming.ValidateHostName(name); err != nil && !embedded && (existing == nil || existing.Name != name) {
		return nil, auth.Invalid("this agent's host name was refused: %v. Start the agent with --name, or set agent.name, "+
			"to a name that fits", err)
	}
	// Taking over an existing row is a privileged act: it destroys that host's
	// runner records and inherits its ID, its labels and its cordon state. A
	// join token alone must not be enough, or anyone an operator trusts to
	// enrol one machine can seize any other machine by naming itself after it.
	// The proof is the agent token the previous registration was issued, which
	// a re-joining agent still holds; the embedded agent is exempt because the
	// caller is this same process.
	if existing != nil && !embedded && !c.provesHost(existing, req.PreviousToken) {
		// auth.Invalid, like the two refusals above it and for the same reason:
		// this is the agent operator's to fix and the sentence says how. As a
		// plain error it went out as a 500 with "something went wrong while
		// enrolling a host. The cause is in the controller's log" -- so the one
		// message that names the two ways forward was replaced, at the moment
		// it was needed, by one that names none.
		return nil, auth.Invalid("a host named %q is already enrolled here. Re-join from the machine that holds its credentials, "+
			"or, if that machine is gone, delete the host first (`zoomies hosts delete %s`) and join again", name, existing.ID)
	}
	hostID := store.NewID(store.PrefixHost)
	if existing != nil {
		hostID = existing.ID
	}
	// Checked before the token is redeemed, so an agent refused by the
	// ceiling keeps a token it can use once there is room. A host joining
	// again replaces its own row and adds nothing to the count. The embedded
	// agent is exempt: it is this process's own machine, and refusing it
	// would leave a single-VM controller with no host at all.
	if existing == nil && !embedded {
		if err := c.admitHost(ctx); err != nil {
			return nil, err
		}
	}

	var tokenLabels store.StringMap
	tokenCapacity := 0
	// machineID is the machine this token was minted for, when it was minted
	// for one. The link is written after the host row exists, because a
	// machine's deletion authority over a host is the link -- so a link
	// written before the row would name a host that may never appear.
	machineID, joinTokenID := "", ""
	if !embedded {
		tok, err := c.authsvc.RedeemJoinToken(ctx, req.JoinToken, store.JoinClaim{HostID: hostID, Name: name})
		if err != nil {
			return nil, err
		}
		tokenLabels, tokenCapacity = tok.Labels, tok.Capacity
		machineID, joinTokenID = tok.MachineID, tok.ID
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

	// The agent describes itself, but the operator who minted the join token
	// decides what this host advertises: labels are what the scheduler matches
	// a pool's host selector against, so an agent that could overwrite them
	// could place itself in a pool it was never meant to serve -- and be handed
	// that pool's runner registrations. Token labels therefore win.
	labels := store.StringMap{}
	maps.Copy(labels, req.Labels)
	for k, v := range tokenLabels {
		if was, ok := labels[k]; ok && was != v {
			c.log.Warn("a joining agent declared a label the join token pins; the token's value stands",
				"host", name, "label", k, "declared", was, "token", v)
		}
		labels[k] = v
	}

	probed := hostBackends(req.Backends)
	plaintext, hash := auth.NewAgentToken()
	now := c.Now()
	h := &store.Host{
		ID:            hostID,
		Name:          name,
		Address:       firstNonEmpty(req.Address, ip),
		Connection:    req.Connection,
		Embedded:      embedded,
		Capacity:      capacity,
		Backends:      probed.Kinds(),
		BackendInfo:   probed,
		Labels:        labels,
		OS:            req.OS,
		Distro:        req.Distro,
		OSVersion:     req.OSVersion,
		Arch:          req.Arch,
		CPUs:          req.CPUs,
		MemoryMB:      req.MemoryMB,
		DiskTotalMB:   req.DiskTotalMB,
		DiskFreeMB:    req.DiskFreeMB,
		Version:       req.Version,
		Features:      req.Features,
		TokenHash:     hash,
		LastHeartbeat: now,
		// Recorded at join as well as at every heartbeat. The join above
		// refuses a mismatch outright -- there is no fleet to disrupt yet, and
		// an agent that cannot join has nothing running to strand -- so what
		// this stores is the version of an agent that matched, which is what
		// makes a *later* mismatch, after a controller upgrade, visible.
		ProtocolVersion: req.ProtocolVersion,
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
		dropped, err := c.st.DeleteHost(ctx, existing.ID)
		if err != nil {
			return nil, fmt.Errorf("replacing the previous registration of host %s: %w", name, err)
		}
		c.publishRunnersDeleted(dropped)
		// The delete cancelled the old registration's open update attempt. A task
		// for it still queued, or held by the agent that went away, would update
		// the machine joining now with nothing recording it.
		c.withdrawHostUpdates(existing.ID)
		h.Embedded = existing.Embedded || embedded
		h.Cordoned = existing.Cordoned
		// The reserve is the operator's, and a re-join is something the agent
		// does: a rebuilt machine reclaiming its row, or an embedded agent
		// whose credentials no longer match. Letting it drop here would be the
		// agent writing the reserve after all, by the back door, and silently
		// -- the row keeps its id, its cordon and its labels, so nothing looks
		// wrong until the controller places into the space that was held back.
		// Capacity needs no such line: the join token carries it, so an
		// operator re-decides it every time one is minted.
		h.ReserveCPUs = existing.ReserveCPUs
		h.ReserveMemoryMB = existing.ReserveMemoryMB
		h.ReserveDiskMB = existing.ReserveDiskMB
		// The runner profile is the operator's for the same reason, and costs
		// more to lose than the reserve: a re-joined host that forgot its
		// standard would be sized by the fleet's default again, with fewer or
		// more slots than its operator chose, and every pool that takes its
		// size from the host would change size with nothing to say why.
		h.RunnerProfile = existing.RunnerProfile
		// The labels an operator added are theirs as well, and the comment above
		// has been saying so while the row lost every one: the join builds a host
		// from what the agent declares and what the token pins, and neither knows
		// about a PATCH. A label the row has and the join does not name is carried
		// over, which is how a size tag or a rack survives a rebuilt machine.
		//
		// A key the agent declares, or the token pins, is not: the agent's
		// configuration is how it says what it is now, and carrying the stored
		// value over it would make an edit of that configuration do nothing for as
		// long as the row lived. So the order of authority is the token's, then the
		// agent's declaration, then what is stored. The price is on two sides: an
		// operator's edit of a label the agent also declares is put back at a
		// re-join, which is what "declares" means, and a label that was dropped
		// from the agent's configuration stays until it is removed with
		// `zoomies hosts edit --untag`, because nothing says it was the agent's.
		for k, v := range existing.Labels {
			if _, declared := req.Labels[k]; declared {
				continue
			}
			if _, pinned := tokenLabels[k]; !pinned {
				h.Labels[k] = v
			}
		}
		// The size class is the controller's, held back while a host's
		// measurements wobble. A rebuilt machine keeps the class it was in rather
		// than starting the reading again, so the pool it is in does not move
		// for the minutes it takes the first heartbeats to arrive.
		h.SizeClass = existing.SizeClass
		// The throttle is deliberately not carried over. It was decided from
		// measurements of a machine that has just been rebuilt or restarted,
		// and a rebuilt machine starts on no rung: if the pressure is still
		// there its first heartbeats put it straight back on one.
	}
	if err := c.st.CreateHost(ctx, h); err != nil {
		return nil, fmt.Errorf("registering host %s: %w", name, err)
	}

	// The machine that minted this token now has a host. Linking it here
	// rather than in the machine loop closes the window in which a machine has
	// enrolled and nothing in the fleet knows which host it became -- and the
	// machine loop still links on a later pass if this write is lost, because
	// the token carries the machine and the token names the host.
	if machineID != "" {
		if err := c.st.LinkMachineHost(ctx, machineID, h.ID, joinTokenID, now); err != nil {
			// Not fatal: the host is enrolled and working. The machine loop's
			// enrolling step retries the link, and quarantines the machine if
			// the host turns out to belong to something else.
			c.log.Warn("a machine's host enrolled but could not be linked to it",
				"machine", machineID, "host", h.ID, "error", err)
		}
		// The machine the fleet has been waiting minutes for has arrived, and
		// the pass that marks it ready is worth having now rather than at the
		// next tick.
		c.NudgeMachines()
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

	// A new host changes where runners can be placed, and which pools exist.
	c.Nudge()
	c.KickAutoPools()

	return &agent.JoinResponse{
		HostID:            h.ID,
		AgentToken:        plaintext,
		ControllerVersion: version.Short(),
		HeartbeatInterval: c.heartbeatInterval().String(),
	}, nil
}

// provesHost reports whether a join request carries the agent token the named
// host was last issued, which is what distinguishes a machine re-joining itself
// from a stranger claiming its name.
func (c *Controller) provesHost(h *store.Host, token string) bool {
	token = strings.TrimSpace(token)
	if token == "" || h.TokenHash == "" {
		return false
	}
	return cryptox.ConstantTimeEqual(h.TokenHash, cryptox.HashToken(token))
}

// Heartbeat records that a host is alive, merges anything its agent observed,
// and tells the agent whether the controller still recognises it.
func (c *Controller) Heartbeat(ctx context.Context, hostID string, req agent.HeartbeatRequest) (*agent.HeartbeatResponse, error) {
	if err := c.checkReportSize(len(req.Runners)); err != nil {
		return nil, err
	}
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
	var freshAt time.Time
	if req.Doctor != nil {
		if err := validateDoctor(req.Doctor, now); err != nil {
			return nil, err
		}
		if freshAt, err = c.ingestDoctor(ctx, h, req.Doctor); err != nil {
			return nil, err
		}
	}

	// The protocol, on every beat rather than only at join. An agent that
	// joined before a protocol bump kept polling and receiving tasks it could
	// not understand, because the check ran once and never again.
	//
	// The answer is exclusion, not refusal. Refusing the heartbeat would send
	// every agent in the fleet into its re-join path at the same moment, which
	// is the outage the upgrade was supposed to avoid; excluding the host from
	// placement leaves its runners working and its agent draining them, and
	// gives an operator a fleet that shrinks rather than one that falls over.
	if err := c.noteProtocol(ctx, h, req.ProtocolVersion); err != nil {
		c.log.Warn("could not record a host's agent protocol", "host", hostID, "error", err)
	}
	// The heartbeat's capacity is deliberately not written. Capacity is set
	// at join -- from the join token when it carries one, else from the
	// agent -- and from then on the host row is what an operator edits: the
	// Hosts API says "use 0 to stop this host taking new runners", and a
	// heartbeat writing the agent's configured number back thirty seconds
	// later would undo exactly that.
	//
	// A report that said nothing new reaches the database here, in the
	// heartbeat's own UPDATE, and reaches the page with the next frame that
	// carries last_heartbeat. If heartbeat frames are ever quietened, the
	// "Checked" line would freeze with them. A crash between ingest and this
	// write loses one freshness update, and the next report repairs it.
	if freshAt.IsZero() {
		err = c.st.Heartbeat(ctx, hostID, now)
	} else {
		err = c.st.HeartbeatWithReport(ctx, hostID, now, freshAt)
	}
	if err != nil {
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
		(req.Version != "" && req.Version != h.Version) ||
		// Written whenever the list differs, an empty one included: an agent
		// rolled back to a release that cannot move a live quota has to stop
		// being counted as one that can, or an elastic pool keeps reading its
		// runners there as boostable when they are held at their share.
		!slices.Equal(req.Features, h.Features) ||
		// A host resized in place -- a VM given more cores, a container's
		// cgroup limit raised -- has to stop describing itself as the machine
		// it used to be, or its name and every pool sized from it go stale.
		// This is a fact about the machine, not the operator's capacity
		// setting, which stays theirs.
		(req.CPUs > 0 && req.CPUs != h.CPUs) ||
		(req.MemoryMB > 0 && req.MemoryMB != h.MemoryMB) ||
		(req.DiskTotalMB > 0 && req.DiskTotalMB != h.DiskTotalMB) ||
		diskFreeMoved(h.DiskFreeMB, req.DiskFreeMB, req.DiskTotalMB > 0)
	if changed {
		was := h.Backends
		if len(probed) > 0 {
			h.Backends = kinds
			h.BackendInfo = probed
		}
		h.Version = firstNonEmpty(req.Version, h.Version)
		h.Features = req.Features
		if req.CPUs > 0 {
			h.CPUs = req.CPUs
		}
		if req.MemoryMB > 0 {
			h.MemoryMB = req.MemoryMB
		}
		if req.DiskTotalMB > 0 {
			h.DiskTotalMB = req.DiskTotalMB
			// Free is written whenever a measurement was taken, zero included:
			// a disk with nothing left on it is the state an operator most
			// needs to see, and the size is what says a measurement happened.
			h.DiskFreeMB = req.DiskFreeMB
		}
		h.LastHeartbeat = now
		// Only the columns an agent measures. h was read at the top of this
		// request, so writing the whole row here would put its pre-edit copy of
		// the operator's fields -- cordoned above all -- back over anything
		// changed in the interval.
		if err := c.st.SetHostReported(ctx, h); err != nil {
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
	// After the row has the version the agent reports: that version, and not the
	// task's answer, is what says an update arrived.
	c.noteHostUpdate(ctx, h, req.Update)
	c.noteHelperUnsupported(hostID, req.UpdateUnsupported)

	if req.Usage != nil {
		usage := store.ObserveHostUsage(h.Usage, *req.Usage, c.lentCPUPercent(ctx, h, req.Runners, now), h.MemoryMB, now)
		if err := c.st.SetHostUsage(ctx, hostID, usage); err != nil {
			return nil, err
		}
		if h.Usage.CPUHeld != usage.CPUHeld {
			c.log.Info("host CPU admission hold changed", "host", hostID, "held", usage.CPUHeld)
		}
		h.Usage = usage
		c.Nudge()
	}
	c.noteRuntime(ctx, h, req.Runtime, now)
	// The ladder runs on the measurements just recorded, and on every beat
	// rather than only the ones that carry a sample: a host whose agent has
	// stopped measuring is lifted here after StaleThrottleReset, not left on a
	// rung until housekeeping happens to look. With throttling off nothing is
	// decided, and housekeeping lifts whatever still stands.
	if c.cfg().Scheduler.HostThrottling {
		c.settleThrottle(ctx, h, now)
	}
	// Decide before applyReports replaces the previous per-runner sample: the
	// delta in cgroup throttling is one of the demand signals.
	elasticCPU := c.elasticCPUTargets(ctx, h, req, now)
	elasticMemory := c.elasticMemoryDirective(ctx, h, req, now)

	if len(req.Runners) > 0 {
		if err := c.applyReports(ctx, hostID, req.Runners); err != nil {
			return nil, err
		}
	}

	h.LastHeartbeat = now
	if !wasHealthy {
		// The host was over its heartbeat window and has come back; the Hosts
		// page should say so without waiting for the health sweep.
		c.publishHost(h)
	}
	c.setHostHealth(hostID, true)

	return &agent.HeartbeatResponse{
		MutationsPaused: !c.mayAct(),
		OK:              true,
		// An incompatible host is told it is cordoned as well, so an agent
		// that does understand the field stops asking for new work without
		// waiting to be told twice. The two are separate fields because they
		// are separate facts: one is an operator's decision and the other is
		// this controller's conclusion about the binary.
		Cordoned:           h.Cordoned || h.Incompatible,
		Incompatible:       h.Incompatible,
		IncompatibleReason: incompatibleReason(h),
		ProtocolVersion:    agent.ProtocolVersion,
		ControllerVersion:  version.Short(),
		ResyncRequested:    c.markHostSeen(hostID, false),
		UnknownRunners:     c.unknownRunners(ctx, hostID, req.Runners),
		Throttle:           c.throttleDirective(ctx, h),
		ElasticCPU:         elasticCPU,
		ElasticMemory:      elasticMemory,
	}, nil
}

// noteProtocol records what protocol the agent says it speaks and whether this
// controller can work with it.
//
// A zero is an agent old enough not to send one, and it is not incompatible:
// it is the one case this cannot judge, and guessing would exclude every host
// in a fleet mid-upgrade from a controller that had just learnt to ask.
//
// It writes through its own statement rather than the general host update, so
// that learning the protocol does not drag every other figure the heartbeat
// carried into the row with it -- the free-disk drift the tolerance exists to
// ignore, in particular.
func (c *Controller) noteProtocol(ctx context.Context, h *store.Host, reported int) error {
	if reported == 0 {
		return nil
	}
	incompatible := reported != agent.ProtocolVersion
	if h.ProtocolVersion == reported && h.Incompatible == incompatible {
		return nil
	}
	was := h.Incompatible
	h.ProtocolVersion, h.Incompatible = reported, incompatible
	if err := c.st.SetHostProtocol(ctx, h.ID, reported, incompatible); err != nil {
		return err
	}
	if incompatible != was {
		if incompatible {
			c.log.Warn("a host's agent speaks a protocol this controller does not",
				"host", h.ID, "name", h.Name, "agent_protocol", reported, "controller_protocol", agent.ProtocolVersion,
				"detail", "it is excluded from placement like a cordoned host; its existing runners keep working and are drained as normal",
				"fix", "upgrade the agent on that host to match this controller")
		} else {
			c.log.Info("a host's agent is compatible with this controller again",
				"host", h.ID, "name", h.Name, "agent_protocol", reported)
		}
		// Placement changes either way, and the Hosts page has to say so.
		c.publishHost(h)
		c.Nudge()
	}
	return nil
}

// incompatibleReason is the sentence the agent logs about itself, written here
// because this side is the one that knows both numbers.
func incompatibleReason(h *store.Host) string {
	if !h.Incompatible {
		return ""
	}
	return fmt.Sprintf("this agent speaks protocol version %d and the controller speaks %d; "+
		"no new runner will be placed here until they match. Existing runners keep working and will be drained as normal. "+
		"Upgrade this agent to the controller's release", h.ProtocolVersion, agent.ProtocolVersion)
}

// NoteAgentSession folds the session an agent identified itself with into its
// host's record, and warns when the host has seen that session before.
//
// Seeing a session a host has already moved on from is the duplicate-agent
// signal: an agent mints its session at start-up, so restarting moves the id
// forward and never back, and only two agents sharing one host's credentials
// -- a cloned VM, or a copied state directory -- hand the same pair back and
// forth. Counting plain changes instead would flag every ordinary restart.
//
// Detect only, by decision. Refusing the older session would be a coin toss
// over which of two live agents keeps the host, and both are running real
// work; the problems panel names it and an operator decides which machine
// should not be there.
func (c *Controller) NoteAgentSession(ctx context.Context, hostID, sessionID string) {
	if hostID == "" || sessionID == "" {
		return
	}
	alternated, err := c.st.RecordAgentSession(ctx, hostID, sessionID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			c.log.Debug("could not record an agent session", "host", hostID, "error", err)
		}
		return
	}
	if alternated {
		c.log.Warn("two agents appear to share this host's credentials; they are splitting its tasks between them",
			"host", hostID, "session", sessionID)
	}
}

// unknownRunners names the runners a host reported that this controller has no
// live row for, which are the ones whose workloads it may remove.
//
// An agent adopts what it finds running when it starts, so a restart no longer
// destroys the jobs on its host -- and that is also what stops it recognising
// genuine litter. This is the other half: the controller is the only party
// that knows a runner was deleted while the agent was down, so it says which,
// and the agent reaps only those.
//
// A runner belonging to another host is deliberately not named. That is a
// different fault, already logged where reports are applied, and answering
// "unknown" would invite one host to remove another's work.
func (c *Controller) unknownRunners(ctx context.Context, hostID string, reports []agent.RunnerReport) []string {
	if !c.mayAct() {
		return nil
	}
	if len(reports) == 0 {
		return nil
	}
	var unknown []string
	for _, rep := range reports {
		if rep.RunnerID == "" {
			continue
		}
		r, err := c.st.GetRunner(ctx, rep.RunnerID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			unknown = append(unknown, rep.RunnerID)
		case err != nil:
			// A read that failed is not evidence the runner is gone, and the
			// answer to this question deletes containers.
			c.log.Warn("could not tell whether a reported runner still exists",
				"host", hostID, "runner", rep.RunnerID, "error", err)
		case r.HostID != hostID:
			// Somebody else's, and not this host's to remove.
		case r.State == store.RunnerRemoved:
			unknown = append(unknown, rep.RunnerID)
		}
	}
	slices.Sort(unknown)
	return unknown
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
	if !q.polling.CompareAndSwap(false, true) {
		c.metrics.agentLimited.WithLabelValues(limitPoll).Inc()
		return &agent.TaskBatch{}, nil
	}
	defer q.polling.Store(false)

	c.pollsInFlight.Add(1)
	defer c.pollsInFlight.Add(-1)

	if !c.mayAct() {
		return &agent.TaskBatch{Backoff: time.Second}, nil
	}
	if tasks := q.take(maxTasksPerPoll, c.Now()); len(tasks) > 0 {
		c.stampIssued(ctx, tasks)
		return &agent.TaskBatch{Tasks: tasks}, nil
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		// A cancelled long poll is the client hanging up, not an error worth
		// reporting; the tasks are still queued for the next one. It is also
		// not evidence about load, so it is never shed.
		return &agent.TaskBatch{}, nil
	case <-timer.C:
		// Only a poll that found nothing is ever asked to wait. Work is never
		// delayed to shed load: the whole point of the long poll is that a
		// task reaches its host in the instant it is queued.
		return &agent.TaskBatch{Backoff: c.shed()}, nil
	case <-q.wake:
		if !c.mayAct() {
			return &agent.TaskBatch{Backoff: time.Second}, nil
		}
		tasks := q.take(maxTasksPerPoll, c.Now())
		c.stampIssued(ctx, tasks)
		return &agent.TaskBatch{Tasks: tasks}, nil
	}
}

// shed asks for a wait if the controller is holding more polls than it wants
// to, and counts the ones it asks for so the pressure is visible in a scrape
// rather than only in a connection count nobody is watching.
func (c *Controller) shed() time.Duration {
	wait := shedFor(c.pollsInFlight.Load())
	if wait > 0 {
		c.metrics.pollsShed.Inc()
	}
	return wait
}

// stampIssued records the issue time of every lifecycle task in a batch. This
// is the redelivery half: take stamps a fresh IssuedAt on each attempt, so a
// task the sweep re-queued moves the row's clock forward with it.
func (c *Controller) stampIssued(ctx context.Context, tasks []agent.Task) {
	for _, t := range tasks {
		c.stampTaskIssued(ctx, t)
		if t.Kind == agent.TaskCreateRunner {
			if err := c.st.SetRunnerCreateTaskIssued(ctx, t.RunnerID, t.IssuedAt); err != nil {
				c.log.Warn("could not record the first create delivery", "runner", t.RunnerID, "error", err)
			}
		}
	}
}

// ReportResult applies the outcome of one task and clears its lease.
func (c *Controller) ReportResult(ctx context.Context, hostID string, res agent.TaskResult) (resultErr error) {
	defer func() {
		if resultErr == nil {
			c.queues.get(hostID).complete(res.TaskID)
		}
	}()
	if res.NotStarted && !res.OK {
		// The agent was shutting down -- an upgrade, a restart -- and gave the
		// task back before touching the runner. Offer it again straight away
		// so the agent that comes back picks it up, instead of waiting out a
		// twenty-minute lease or failing a runner nothing happened to. The
		// attempt still counts, so a host that never stays up long enough
		// ends at the provision timeout rather than looping.
		switch c.queues.get(hostID).redeliver(res.TaskID) {
		case redelivered:
			c.log.Info("an agent gave a task back while shutting down; it will be offered again",
				"host", hostID, "task", res.TaskID, "kind", res.Kind, "runner", res.RunnerID)
			return nil
		case notInFlight:
			// A controller restart since, so the task is not on record. The
			// runner is untouched; the reconcile loop and the provision
			// timeout are what notice one that is never built.
			return nil
		}
		// Out of attempts: fall through and fail the runner, which is the
		// honest answer for a host that never stays up long enough.
	}
	task, known := c.queues.get(hostID).peek(res.TaskID)
	if known && task.Kind == agent.TaskFillToolCache {
		c.recordToolFill(hostID, task, res)
		return nil
	}
	if known && task.Kind == agent.TaskPrewarmImage {
		outcome := "failed"
		if res.OK {
			switch {
			case res.PrewarmCached == nil:
				outcome = "unknown"
			case *res.PrewarmCached:
				outcome = "cache_hit"
			default:
				outcome = "prepared"
			}
		}
		pool := c.poolLabel(task.PoolID)
		backendKind := string(task.Backend)
		c.metrics.imagePrewarms.WithLabelValues(pool, backendKind, outcome).Inc()
		if res.PrewarmDuration != nil && *res.PrewarmDuration >= 0 {
			c.metrics.imagePrewarmDuration.WithLabelValues(pool, backendKind, outcome).Observe(res.PrewarmDuration.Seconds())
		}
		state := "succeeded"
		if !res.OK {
			state = "failed"
		}
		c.noteImagePull(ctx, hostID, task.PoolID, task.Image, "prewarm", res.OK, res.Fault, res.Error)
		return c.st.SetPoolPrewarm(ctx, task.PoolID, hostID, task.Image, state, res.Digest, res.Error)
	}
	if known && task.Kind == agent.TaskStreamLogs && !res.OK {
		// Before the runner lookup, so a runner pruned in the meantime does not
		// leave its viewer waiting either.
		c.relay.fail(task.StreamID, res.Error)
	}
	kind := res.Kind
	if kind == "" && known {
		kind = task.Kind
	}
	if kind == agent.TaskCheckHost {
		// hostID is the authenticated agent's, never the body's. Returning nil
		// clears the lease on every outcome, a failed check included.
		c.applyHostCheck(ctx, hostID, res, task.IssuedAt)
		return nil
	}
	if kind == agent.TaskUpdateAgent {
		// The host's, and never a runner's: a failure here is not a reason to
		// fail anything but the attempt it was for.
		c.applyUpdateTaskResult(ctx, hostID, task, known, res)
		return nil
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
			return fmt.Errorf("recording runner workload handle: %w", err)
		}
	}
	if kind == agent.TaskCreateRunner && res.OK && res.ContainerStartedAt != nil {
		service := res.CreateDuration
		if res.ImagePullDuration != nil {
			service += *res.ImagePullDuration
		}
		if err := c.st.SetRunnerServiceTime(ctx, r.ID, service); err != nil {
			return err
		}
		if err := c.st.SetRunnerStartup(ctx, r.ID, res.ImagePullDuration, res.ContainerStartedAt); err != nil {
			return err
		}
		if p, err := c.st.GetPool(ctx, r.PoolID); err == nil {
			if res.StartupWait != nil && *res.StartupWait >= 0 {
				c.metrics.startupWait.WithLabelValues(p.Name, string(p.Backend)).Observe(res.StartupWait.Seconds())
			}
			if res.DinDReadyDuration != nil && *res.DinDReadyDuration >= 0 {
				c.metrics.dindReady.WithLabelValues(p.Name, string(p.Backend)).Observe(res.DinDReadyDuration.Seconds())
			}
			observeDuration(c.metrics.createToContainer, p.Name, string(p.Backend), r.CreatedAt, *res.ContainerStartedAt)
		}
	}
	if res.Digest != "" {
		if err := c.st.SetRunnerImageDigest(ctx, r.ID, res.Digest); err != nil {
			return err
		}
	}

	if kind == agent.TaskCreateRunner {
		image := ""
		if known && task.Spec != nil {
			image = task.Spec.Image
		}
		c.noteImagePull(ctx, hostID, r.PoolID, image, "start", res.OK, res.Fault, res.Error)
	}

	if !res.OK && res.CleanupPending && kind == agent.TaskRemoveRunner {
		// The daemon is still carrying out a removal of this runner's
		// workload, and finishes it without being asked again. Nothing is
		// recorded against the runner: the agent confirms the removal on
		// the first attempt that finds the workload gone, and reports it as
		// a failure itself once it has waited long enough to call it stuck.
		// Recording each of these answers as a failed cleanup is what put a
		// runner that was merely slow to delete on the Runners page as
		// something left behind, one "attempt" per scheduling pass.
		c.log.Info("a host is still removing a runner's workload; it will confirm the removal once the daemon has finished",
			"runner", r.ID, "name", r.Name, "host", hostID, "detail", res.Error)
		return nil
	}

	state := res.State
	message := res.Error
	if !res.OK {
		if !lifecycleTask(kind) {
			// The relay could not be opened, which the viewer has been told
			// (logRelay.fail, above);
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
		if r.State.Terminal() {
			// The row is already finished with, so there is no transition to
			// make: a removed runner cannot become failed, and the state
			// machine is right to refuse it. Before this the result was
			// dropped there at debug level -- and a remove that failed means
			// the container is still on the host, with nobody told. It is
			// recorded on the row instead.
			return c.noteCleanupFailure(ctx, r, kind, message)
		}
	}
	if err := c.applyRunnerState(ctx, r, state, message, res.Fault); err != nil {
		return err
	}
	if res.OK && cleansUp(kind) {
		return c.noteCleanupSucceeded(ctx, r, kind == agent.TaskRemoveRunner)
	}
	return nil
}

// publishRunnerByID re-reads a runner and puts it on the bus, for the places
// that changed a column rather than a state.
func (c *Controller) publishRunnerByID(ctx context.Context, id string) {
	if updated, err := c.st.GetRunner(ctx, id); err == nil {
		c.publishRunner(ctx, events.KindRunnerUpdated, updated)
	}
}

// cleansUp reports whether this task kind is one whose success means there is
// less of the runner left on the host than there was.
func cleansUp(kind agent.TaskKind) bool {
	return kind == agent.TaskStopRunner || kind == agent.TaskRemoveRunner
}

// noteCleanupFailure records on the runner's row that taking it away did not
// work, and says so where an operator will see it.
//
// It is not a state transition. The runner is terminal and its slot is already
// free; forcing it back through the state machine would make the fleet's
// capacity wrong in order to record a tidying problem. What is wrong is the
// host, and the row is where that belongs.
func (c *Controller) noteCleanupFailure(ctx context.Context, r *store.Runner, kind agent.TaskKind, reason string) error {
	if r.HostRemovedAt != nil {
		// The host has already confirmed this runner's workload gone -- its
		// sidecar included, from a fresh listing -- so a failure arriving
		// after that is about an attempt the confirmation overtook: a report
		// assembled before it, a redelivered task, a lease that ran out on a
		// task it made moot. Recording one re-opened a finished cleanup,
		// counted another attempt and raised the warning again for a runner
		// with nothing left behind. This copy of the row can be older than
		// the confirmation, so the store checks again as it writes.
		c.log.Debug("ignored a cleanup failure for a runner its host has already confirmed removed",
			"runner", r.ID, "kind", kind, "error", reason)
		return nil
	}
	detail := fmt.Sprintf("%s failed: %s", kind, reason)
	if err := c.st.RecordCleanupFailure(ctx, r.ID, detail); err != nil {
		if errors.Is(err, store.ErrConflict) {
			// The confirmation landed between this row being read and the
			// write: the same late failure as above, caught by the store.
			c.log.Debug("ignored a cleanup failure for a runner its host has already confirmed removed",
				"runner", r.ID, "kind", kind, "error", reason)
			return nil
		}
		c.log.Warn("could not record a failed cleanup on its runner",
			"runner", r.ID, "kind", kind, "error", err)
		return err
	}
	c.metrics.cleanups.WithLabelValues("failed").Inc()
	c.log.Warn("could not clean a runner up; it is recorded on the row",
		"runner", r.ID, "name", r.Name, "host", r.HostID, "kind", kind, "error", reason)
	if updated, err := c.st.GetRunner(ctx, r.ID); err == nil {
		c.publishRunner(ctx, events.KindRunnerUpdated, updated)
	}
	return nil
}

// noteCleanupSucceeded clears a recorded failure once the same work has since
// worked, and closes the runner's cleanup interval.
func (c *Controller) noteCleanupSucceeded(ctx context.Context, r *store.Runner, removed bool) error {
	if removed {
		if err := c.confirmCleanup(ctx, r.ID, true); err != nil {
			return err
		}
		if r.HostRemovedAt == nil {
			c.metrics.cleanups.WithLabelValues("succeeded").Inc()
		}
		if r.RegistrationDeletedAt == nil {
			return c.st.QueueEnrichment(ctx, "registration", r.ID)
		}
		return nil
	}
	if r.CleanupError == "" && r.CleanedUpAt != nil {
		return nil
	}
	// Counted here rather than at every call site: this is the point at which
	// the runner is known to be gone from its host, and the early return above
	// is a repeat report about one that already was.
	c.metrics.cleanups.WithLabelValues("succeeded").Inc()
	if err := c.st.ClearCleanupFailure(ctx, r.ID); err != nil {
		c.log.Warn("could not clear a recorded cleanup failure", "runner", r.ID, "error", err)
		return err
	}
	if r.CleanupError != "" {
		c.log.Info("a runner that would not clean up has now been cleaned up",
			"runner", r.ID, "name", r.Name, "attempts", r.CleanupAttempts+1)
	}
	if updated, err := c.st.GetRunner(ctx, r.ID); err == nil {
		c.publishRunner(ctx, events.KindRunnerUpdated, updated)
	}
	return nil
}

// ReportRunners merges an agent's observations outside the heartbeat cycle, so
// a runner coming up is visible immediately rather than up to a beat later.
func (c *Controller) ReportRunners(ctx context.Context, hostID string, reports []agent.RunnerReport) error {
	if hostID == "" {
		return errors.New("a runner report carried no host ID; the agent must send the identity it was given at join")
	}
	if err := c.checkReportSize(len(reports)); err != nil {
		return err
	}
	return c.applyReports(ctx, hostID, reports)
}

// applyReports folds each observation into the runner's authoritative state.
func (c *Controller) applyReports(ctx context.Context, hostID string, reports []agent.RunnerReport) error {
	var errs []error
	for _, rep := range reports {
		if rep.RunnerID == "" {
			continue
		}
		r, err := c.st.GetRunner(ctx, rep.RunnerID)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				errs = append(errs, err)
			}
			continue
		}
		if r.HostID != hostID {
			c.log.Warn("a host reported on a runner it does not own",
				"host", hostID, "runner", r.ID, "owner", r.HostID)
			continue
		}
		if rep.CleanupError != "" && r.State.Terminal() {
			if err := c.noteCleanupFailure(ctx, r, agent.TaskRemoveRunner, rep.CleanupError); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if r.State.Terminal() && rep.Phase.Live() {
			c.reconcileLateReport(ctx, r, rep)
			continue
		}
		if rep.Handle != "" && string(rep.Handle) != r.ContainerID {
			if err := c.st.SetRunnerContainer(ctx, r.ID, string(rep.Handle)); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if rep.GitHubRunnerID != 0 && r.GitHubRunnerID == 0 {
			if err := c.st.SetRunnerGitHubID(ctx, r.ID, rep.GitHubRunnerID); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		resourcesMoved := false
		if rep.Stats.SampledAt != nil || rep.Stats.CPUPercent != 0 || rep.Stats.MemoryBytes != 0 {
			sample, _ := json.Marshal(rep.Stats)
			// Read against the sample it is about to replace, which is why it is
			// before the write: the loan is written when it grows, and the view
			// is repainted when anything it shows moved.
			memoryMoved := c.noteMemoryValve(ctx, r, rep)
			if err := c.st.SetRunnerResourceSample(ctx, r.ID, rep.Stats.CPUPercent, rep.Stats.MemoryBytes, sample); err == nil {
				resourcesMoved = allocationFactorMoved(r.ResourceSample, rep.Stats) || memoryMoved
			}
			if r.State == store.RunnerBusy && rep.Stats.SampledAt != nil {
				c.observePair(r, rep.Stats)
				// The same sample raises the peaks of the job the runner is
				// running, which is what the next run of that job is placed
				// from. A failure here costs one sample of history, never
				// the report.
				if err := c.st.RecordJobUsage(ctx, r.ID, rep.Stats.CPUPercent/100, rep.Stats.MemoryBytes/(1<<20)); err != nil {
					c.log.Debug("could not record a job's usage", "runner", r.ID, "error", err)
				}
				// What each container of a pair used, which is what a pool's smallest
				// runner is sized to hold: the window in memory is the last few hours,
				// and this is every job of the week.
				if h := rep.Stats.Halves; h != nil {
					if err := c.st.RecordJobHalfPeaks(ctx, r.ID, h.Runner.MemoryBytes>>20, h.Daemon.MemoryBytes>>20); err != nil {
						c.log.Debug("could not record a pair's peaks", "runner", r.ID, "error", err)
					}
				}
				// How often the runner was held back by its CPU quota, which is
				// what tells a job that wants more CPU from one that is merely
				// busy. An agent that sends no counters leaves the job as it was.
				if t := rep.Stats.CPUThrottling; t != nil && c.sizeMode() != scheduler.SizeOff {
					if err := c.st.RecordJobThrottle(ctx, r.ID, int64(t.Periods), int64(t.ThrottledPeriods)); err != nil {
						c.log.Debug("could not record a job's CPU throttling", "runner", r.ID, "error", err)
					}
				}
			}
		}

		state := rep.State
		if state == "" && rep.Phase == backend.PhaseRunning && r.State == store.RunnerRegistering {
			// Container liveness is not GitHub readiness. Keep the provision
			// timeout active until GitHub confirms the listener is online.
			if err := c.st.QueueEnrichment(ctx, "ready", r.ID); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if err := c.applyRunnerState(ctx, r, state, rep.Message, rep.Fault); err != nil {
			errs = append(errs, err)
			continue
		}
		if len(rep.OutputTail) > 0 {
			// The runner's last lines, kept on the job it was running: the
			// container goes next, and the explanation quotes from here.
			if _, err := c.st.SetJobOutputTail(ctx, r.ID, rep.OutputTail); err != nil {
				c.log.Warn("could not keep a runner's last lines on its job", "runner", r.ID, "error", err)
			}
		}
		if rep.Fault == store.FaultOutOfMemory {
			// Whatever the lifecycle says -- a runner killed outright, or one
			// that finished its job after a step was killed -- the job it ran
			// was the fleet's failure, and its history has to say it needed
			// more than it had.
			c.noteOOMKilled(ctx, r, rep.Message)
		}
		if resourcesMoved {
			// A boost given, taken back or tightened by a throttle is the
			// runner's cpu_resource changing, and memory lent or swap allowed
			// is its memory_resource changing; the UI repaints a runner only
			// from a runner.updated frame. Without this the Squirrel spotted
			// status appeared on a reload and never live. The frame is the
			// runner's GET shape, read after every write above.
			c.publishRunnerByID(ctx, r.ID)
		}
		if rep.HostRemoved && !rep.Phase.Live() && (r.State.Terminal() || state.Terminal()) {
			if err := c.noteCleanupSucceeded(ctx, r, true); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// allocationFactorMoved reports whether a new sample changes the CPU
// allocation factor the previous one recorded, which is the only input to a
// runner's cpu_resource view that a heartbeat moves. A runner never sampled
// before counts as at its guarantee.
func allocationFactorMoved(previous json.RawMessage, current backend.Stats) bool {
	was := 1.0
	var prev backend.Stats
	if len(previous) > 0 && json.Unmarshal(previous, &prev) == nil && prev.CPUAllocationFactor > 0 {
		was = prev.CPUAllocationFactor
	}
	now := current.CPUAllocationFactor
	if now <= 0 {
		now = 1
	}
	return math.Abs(now-was) > 0.005
}

// reconcileLateReport settles a runner this controller has already written
// off, whose host has come back with the workload still running.
//
// It happens for real: a host silent past hostLostAfter has its runners failed
// as lost, and "lost" is a guess. A network partition, a long agent restart or
// a paused VM all end with the host returning and the container exactly where
// it was, still executing its job. Before this, the report was dropped as an
// illegal transition and nothing else looked again: the row stayed failed, the
// agent kept the workload tracked because the controller never disowned it,
// and the container ran for ever on a host nobody was accounting for.
//
// The row is not resurrected. failed and removed are terminal because the
// fleet has already told an operator, and a job's events, that this runner was
// gone; walking that back would make the timeline a worse record than no
// record. What is repaired instead is the truth of the message and the fate of
// the workload.
//
// The workload outlives the row while a job is still running on it. A job in
// flight is worth more than a tidy row -- the runner is failed, so its slot is
// already free and the scheduler has replaced it, and killing the container
// now would fail a job that is about to succeed for no reason but neatness.
// Once nothing is running on it, it is removed properly, through the same path
// as any other removal.
func (c *Controller) reconcileLateReport(ctx context.Context, r *store.Runner, rep agent.RunnerReport) {
	if r.State == store.RunnerRemoved {
		// Already the answer. The heartbeat names a removed runner as unknown,
		// so the agent releases it and the reconciler collects the workload.
		return
	}
	running, err := c.st.ListRunningJobsForRunner(ctx, r.ID)
	if err != nil {
		// Removing a workload is not a thing to do on a failed read.
		c.log.Warn("could not tell whether a returned runner still has a job on it",
			"runner", r.ID, "host", r.HostID, "error", err)
		return
	}
	if len(running) > 0 {
		c.noteRunnerReturned(ctx, r, running)
		return
	}
	pool, err := c.st.GetPool(ctx, r.PoolID)
	if err != nil {
		pool = nil
	}
	if _, err := c.removeRunner(ctx, r, "the host returned with this runner still on it after it was given up as lost, and it has no job left to finish", pool); err != nil {
		c.log.Warn("could not remove a runner that outlived being declared lost",
			"runner", r.ID, "host", r.HostID, "error", err)
	}
}

// noteRunnerReturned records that a runner given up as lost is alive after all
// and still working, on the row and on every job it is running.
//
// It writes once per job. A host reports its runners on every heartbeat, so
// without the guard a runner that takes ten minutes to finish would write the
// same line into its job's timeline forty times.
func (c *Controller) noteRunnerReturned(ctx context.Context, r *store.Runner, running []*store.Job) {
	message := fmt.Sprintf("host %s returned with this runner still running; it was given up as lost, and its job is being left to finish", c.hostName(ctx, r.HostID))
	if r.Message != message {
		if updated, err := c.st.TransitionRunner(ctx, r.ID, r.State, message); err == nil {
			c.publishRunner(ctx, events.KindRunnerUpdated, updated)
		} else {
			c.log.Warn("could not correct the message on a returned runner", "runner", r.ID, "error", err)
		}
	}
	for _, j := range running {
		timeline, err := c.st.ListJobEvents(ctx, j.ID)
		if err != nil {
			continue
		}
		if slices.ContainsFunc(timeline, func(e *store.JobEvent) bool {
			return e.Kind == store.JobEventRunnerReturned && e.RunnerID == r.ID
		}) {
			continue
		}
		if err := c.st.AppendJobEvent(ctx, &store.JobEvent{
			JobID: j.ID, Kind: store.JobEventRunnerReturned, Source: sourceAgent,
			Message:  fmt.Sprintf("runner %s was reported lost with its host, but both came back and this job is still running on it", r.Name),
			RunnerID: r.ID, RunnerName: r.Name, At: c.Now(),
		}); err != nil {
			c.log.Warn("could not record a returned runner on its job", "job", j.ID, "runner", r.ID, "error", err)
			continue
		}
		if updated, err := c.st.GetJob(ctx, j.ID); err == nil {
			c.publishJob(ctx, updated)
		}
	}
	c.log.Info("a runner given up as lost came back still working",
		"runner", r.ID, "name", r.Name, "host", r.HostID, "jobs", len(running))
}

// hostName is the operator-facing name of a host, falling back to its id so a
// message is never left with a hole in it. It is for putting in a sentence, so
// it is the name as prose: see naming.ForSentence.
func (c *Controller) hostName(ctx context.Context, hostID string) string {
	if h, err := c.st.GetHost(ctx, hostID); err == nil && h.Name != "" {
		return naming.ForSentence(h.Name)
	}
	return hostID
}

// observeRunnerReady records how long a runner took to become usable, once it
// has. Both timings are only meaningful for a runner that reached idle or busy
// with a registration behind it, which is why every caller that can produce one
// goes through here rather than keeping its own copy of the rule.
//
// Every field is read off the row the store handed back, never off the copy the
// caller was holding. The create result is what stamps container_started_at, and
// it is also what races an in_progress delivery -- so a caller whose copy
// predates it would drop the container-to-registered timing for exactly the
// runners whose startup was interesting enough to race.
func (c *Controller) observeRunnerReady(ctx context.Context, updated *store.Runner) {
	if updated == nil || updated.RegisteredAt == nil {
		return
	}
	if updated.State != store.RunnerIdle && updated.State != store.RunnerBusy {
		return
	}
	p, err := c.st.GetPool(ctx, updated.PoolID)
	if err != nil {
		return
	}
	if updated.ContainerStartedAt != nil {
		observeDuration(c.metrics.containerToRegistered, p.Name, string(p.Backend), *updated.ContainerStartedAt, *updated.RegisteredAt)
	}
	observeDuration(c.metrics.registeredToReady, p.Name, string(p.Backend), *updated.RegisteredAt, c.Now())
}

// applyRunnerState performs a reported transition when it is legal, and
// publishes it. An illegal one is dropped rather than forced: the store's
// state machine is what stops a confused agent corrupting the accounting.
// fault is the agent's own classification, sent alongside the state. An agent
// older than that field sends none, which is recorded as the unclassified kind
// -- exactly what this controller knew before there was a taxonomy at all.
func (c *Controller) applyRunnerState(ctx context.Context, r *store.Runner, state store.RunnerState, message string, fault store.FaultKind) error {
	if state == "" || state == r.State {
		return nil
	}
	if !state.Valid() || !store.CanTransition(r.State, state) {
		c.log.Debug("ignoring a runner state an agent reported out of order",
			"runner", r.ID, "from", r.State, "to", state)
		return nil
	}
	var updated *store.Runner
	var err error
	if state == store.RunnerFailed {
		updated, err = c.st.FailRunner(ctx, r.ID, message, fault)
	} else {
		updated, err = c.st.TransitionRunner(ctx, r.ID, state, message)
	}
	if err != nil {
		c.log.Warn("could not apply a runner state an agent reported", "runner", r.ID, "state", state, "error", err)
		return err
	}
	c.observeRunnerReady(ctx, updated)
	c.publishRunner(ctx, events.KindRunnerUpdated, updated)
	if state == store.RunnerFailed {
		// A clean exit under a job is the ordinary race between GitHub's
		// completed delivery and the agent noticing the container has gone;
		// a failure is not, and the job it was running needs to say so.
		c.noteRunnerLost(ctx, r, sourceAgent, message, updated.FaultKind)
		// And a failure with no job under it is the other half: a runner that
		// died on the way up, which reaches the queue it was started for and
		// nothing else.
		c.noteRunnerStartFailure(ctx, r, updated)
	}
	if state.Terminal() {
		// A runner that has gone frees host capacity, so the next placement
		// decision should happen now rather than on the next tick.
		c.Nudge()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Host health
// ---------------------------------------------------------------------------

// hostLostAfter is how long a host may be silent before its runners are given
// up on. store.HeartbeatTimeout is the earlier, softer judgement: the host is
// unhealthy, so nothing new is placed on it. This is the later, harder one:
// the runners already there are not coming back. It is longer than an agent
// restart, a daemon upgrade or a network blip, and shorter than a pool pinned
// at its maximum by dead runners can be left creating nothing.
const hostLostAfter = 5 * time.Minute

// checkHostHealth publishes a host event whenever a host's health flips, so
// the UI shows an agent going quiet without anyone refreshing, and reclaims
// the runners of a host that has been quiet for long enough to be gone.
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
		if !healthy && now.Sub(h.LastHeartbeat) > hostLostAfter {
			c.reclaimLostRunners(ctx, h, now)
		}
	}
}

// reclaimLostRunners fails every runner still recorded as live on a host that
// has been silent past hostLostAfter.
//
// Until they are failed those rows count as capacity: a pool at its maximum
// with four runners on a dead host created nothing, for ever, while looking
// perfectly healthy, because nothing sends a task to a host that is gone and
// the rows never reached a terminal state on their own. Failing them lets the
// next reconcile pass replace them, and marks the job a busy one was running as
// the fleet's failure rather than the workflow's. The demo fleet's hosts are
// left alone: some are silent on purpose, so the UI can be looked at with an
// unhealthy host on it.
func (c *Controller) reclaimLostRunners(ctx context.Context, h *store.Host, now time.Time) {
	if IsDemoID(h.ID) {
		return
	}
	runners, err := c.st.ListRunnersForHost(ctx, h.ID,
		store.RunnerProvisioning, store.RunnerRegistering, store.RunnerIdle, store.RunnerBusy, store.RunnerDraining)
	if err != nil || len(runners) == 0 {
		return
	}
	silent := now.Sub(h.LastHeartbeat).Round(time.Second)
	reason := fmt.Sprintf("host %s has not sent a heartbeat for %s, so this runner cannot be reached or stopped; it is presumed gone with the host",
		h.Name, silent)
	failed := 0
	for _, r := range runners {
		if err := c.failRunnerID(ctx, r.ID, reason, store.FaultHostLost); err != nil {
			if !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrInvalidTransition) {
				c.log.Warn("could not fail a runner on a silent host", "runner", r.ID, "host", h.ID, "error", err)
			}
			continue
		}
		failed++
	}
	if failed > 0 {
		c.log.Warn("gave up on the runners of a silent host; the next pass replaces them",
			"host", h.ID, "name", h.Name, "runners", failed, "silent_for", silent)
		c.Nudge()
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
	if d := c.cfg().Agent.HeartbeatInterval; d > 0 {
		return d
	}
	return 30 * time.Second
}

// diskFreeTolerance is how far free disk may drift before the host row is
// rewritten.
//
// The fraction is the rule; the floor only ever binds below about five
// gigabytes, and it is there because a twentieth of a small volume is a few
// megabytes -- an amount that changes constantly and tells nobody anything, so
// tracking it would be the write-per-beat this exists to avoid. It does make a
// small volume coarser in proportion, which is the price: a host whose runners
// share a one-gigabyte scratch disk is not a host being placed on by disk.
const (
	diskFreeToleranceFraction = 0.05
	diskFreeToleranceFloorMB  = 256
)

// hostBackends converts an agent's probe into the form the store keeps. The
// whole probe is persisted, not just the kinds that answered: "this host has no
// docker" and "this host has docker but the agent cannot read its socket" are
// the same row to the scheduler and completely different to an operator.

// diskFreeMoved reports whether free disk has changed by enough to be worth a
// write. `measured` says whether the agent took a reading at all.
//
// Free space is the one thing an agent reports that moves on its own: a job
// unpacking a cache changes it, and so does the job next door. A heartbeat
// arrives every thirty seconds per host, and "different from last time" is true
// of this figure essentially always -- so recording it the way the others are
// recorded turns a fleet's heartbeats into one row write per host per beat, for
// a number that was never exactly the same twice and did not need to be.
//
// Whether a reading was taken is a separate question from what it said, and
// they cannot share an encoding. A disk with nothing left reads zero, and so
// does an agent too old to look; treating the two alike leaves a full host
// describing itself with the last comfortable figure it ever reported, which is
// the one state where being out of date does real harm. The size is what says a
// measurement happened -- no filesystem is zero bytes -- so free is free.
func diskFreeMoved(was, now int64, measured bool) bool {
	if !measured {
		// An agent that has stopped answering says nothing about the disk, and
		// its silence must not overwrite what was known.
		return false
	}
	if was == now {
		// Including a full disk that is still full: it is already on the row.
		return false
	}
	if was <= 0 {
		// The first reading, however small, because going from "not measured"
		// to a figure is the difference between a host that can be placed on
		// by disk and one that cannot.
		return true
	}
	tolerance := int64(float64(was) * diskFreeToleranceFraction)
	if tolerance < diskFreeToleranceFloorMB {
		tolerance = diskFreeToleranceFloorMB
	}
	delta := now - was
	if delta < 0 {
		delta = -delta
	}
	return delta >= tolerance
}

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
			CPUs:         i.CPUs,
			MemoryMB:     i.MemoryMB,
			Limits:       i.Limits,
			SharedFolder: i.SharedFolder,
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
		err := t.c.AcceptLogStream(t.host(), streamID, pr)
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
		cfg = c.cfg()
	}
	if c.backends == nil || len(c.backends.Kinds()) == 0 {
		return errors.New("controller: no runner backends are registered, so the embedded agent has no way to start runners; " +
			"install Docker or Podman, or set agent.backend to process")
	}

	tr := c.EmbeddedTransport()
	a, err := agent.New(agent.Options{
		Doctor:             c.doctor,
		Name:               cfg.Agent.Name,
		WorkDir:            cfg.Agent.WorkDir,
		Capacity:           cfg.Agent.Capacity,
		Labels:             cfg.Agent.Labels,
		Backends:           c.backends,
		DefaultBackend:     store.BackendKind(cfg.Agent.Backend),
		Transport:          tr,
		HeartbeatInterval:  cfg.Agent.HeartbeatInterval,
		FinishedRetention:  cfg.Agent.FinishedRetention,
		BootstrapCPUGrace:  cfg.Agent.BootstrapCPUGrace,
		PrewarmJitter:      cfg.Agent.PrewarmJitter,
		PrewarmTimeout:     cfg.Agent.PrewarmTimeout,
		DockerBuildCacheMB: cfg.Agent.DockerBuildCacheMB,
		Logger:             c.log,
		Clock:              c.clock,
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

// containerHostname is what Docker calls a container when nothing else does:
// the first twelve hex digits of its ID.
var containerHostname = regexp.MustCompile(`^[0-9a-f]{12}$`)

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
//
// Only a name Docker generated is replaced. Any other name may be one an
// operator chose through PATCH /hosts/{id}, and putting the configured name
// back at every restart would undo a rename each time the controller came up.
func (c *Controller) renameEmbeddedHost(ctx context.Context, h *store.Host, name string) {
	name = strings.TrimSpace(name)
	if name == "" || h.Name == name || !containerHostname.MatchString(h.Name) {
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
