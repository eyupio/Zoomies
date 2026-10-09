package controller

import (
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// The constants that govern reconciliation live in four packages, and each
// was chosen against one in another: the host-lost reclaim against the
// heartbeat timeout, the heartbeat timeout against the interval the controller
// hands the agent, every task lease against the time the agent gives itself to
// do that task. Until now nothing but a comment held those pairs together, so
// a change to one side did not have to say what it did to the other. These
// tests are the section of the same name in docs/architecture.md, made to
// fail.

// A host passes through two judgements as it goes quiet, and they have to come
// in this order: late (one missed heartbeat is nothing), then unhealthy (the
// scheduler stops placing runners on it), then lost (the runners already on
// it are failed so a pool can replace them). Unhealthy before late would fail
// healthy hosts on every hiccup; lost before unhealthy would destroy runners
// on a host the scheduler was still placing new ones on.
func TestAHostIsLateThenUnhealthyThenLostInThatOrder(t *testing.T) {
	interval := config.Default().Agent.HeartbeatInterval
	if interval <= 0 {
		t.Fatalf("the default heartbeat interval is %s; the ladder needs a positive one", interval)
	}
	// Three missed heartbeats, not one: a single late POST is a busy host or
	// a slow link, and marking it unhealthy would bounce placement on and off.
	if store.HeartbeatTimeout < 3*interval {
		t.Fatalf("store.HeartbeatTimeout is %s, less than three default heartbeat intervals of %s; one slow heartbeat would make a host unhealthy", store.HeartbeatTimeout, interval)
	}
	if hostLostAfter <= store.HeartbeatTimeout {
		t.Fatalf("hostLostAfter is %s, not later than store.HeartbeatTimeout at %s; runners would be failed on a host the scheduler had not yet stopped using", hostLostAfter, store.HeartbeatTimeout)
	}
}

// A task's lease is how long the controller waits before offering the task to
// the host again. If it were shorter than the work, a create still pulling its
// image would be redelivered while the first attempt was running, and the
// agent's adopt-before-create is a backstop for a lost result, not a licence
// to race it. So each lease exceeds the longest the agent will spend on that
// kind of task, with room for the result to travel.
func TestEveryTaskLeaseOutlastsTheWorkItCovers(t *testing.T) {
	cases := []struct {
		kind  agent.TaskKind
		lease time.Duration
		work  time.Duration
	}{
		{agent.TaskCreateRunner, createLease, agent.CreateTimeout},
		// The controller hands the agent DefaultStopTimeout on every stop
		// (reconcile.go), and the agent adds StopMargin to it.
		{agent.TaskStopRunner, stopLease, agent.DefaultStopTimeout + agent.StopMargin},
		{agent.TaskRemoveRunner, removeLease, agent.RemoveTimeout},
		// A check is bounded by the monitor's own run timeout on the agent.
		{agent.TaskCheckHost, hostCheckLease, hosttune.MonitorRunTimeout},
		// An update task is one small file written before the answer; the
		// update it asks for restarts the agent and reports on a heartbeat, so it
		// is not the task's work. A minute is far beyond the write.
		{agent.TaskUpdateAgent, updateLease, time.Minute},
	}
	for _, c := range cases {
		if got := requeueAfter(c.kind); got != c.lease {
			t.Errorf("%s: requeueAfter gives %s, want the %s lease this test pins", c.kind, got, c.lease)
		}
		if c.lease <= c.work {
			t.Errorf("%s: the lease is %s and the agent may spend %s on the task; the controller would redeliver it while the first attempt was still running", c.kind, c.lease, c.work)
		}
	}
}

// A machine's life is a chain of deadlines, and each one has to outlast the
// thing it bounds. Set the wrong way round they do not merely misbehave: an
// ambiguity timeout shorter than a create quarantines machines that are simply
// still being built, and a delete grace shorter than the silence that loses a
// host destroys a machine in the middle of a job.
func TestEveryMachineDeadlineOutlastsTheWorkItCovers(t *testing.T) {
	p := config.Default().Provider
	cases := []struct {
		shorter, longer string
		short, long     time.Duration
		because         string
	}{
		{"provider.call_timeout", "provider.create_timeout", p.CallTimeout, p.CreateTimeout,
			"one request has to fit inside the whole asynchronous create it starts"},
		{"provider.create_timeout", "provider.ambiguity_timeout", p.CreateTimeout, p.AmbiguityTimeout,
			"a create that is merely slow would otherwise be treated as one nobody can account for"},
		{"the heartbeat timeout", "provider.enrol_timeout", store.HeartbeatTimeout, p.EnrolTimeout,
			"a machine that joined and went briefly quiet would otherwise be given up on"},
		{"hostLostAfter plus a housekeeping tick", "provider.delete_grace",
			hostLostAfter + housekeepingTick, p.DeleteGrace,
			"a network blip would otherwise destroy a machine that is in the middle of a job"},
		{"hostLostAfter", "provider.idle_timeout", hostLostAfter, p.IdleTimeout,
			"a machine whose host had merely gone quiet would otherwise be drained as idle"},
		{"provider.enrol_timeout", "a machine join token's life",
			p.EnrolTimeout, p.EnrolTimeout + machineTokenGrace,
			"the credential has to outlast the window the machine is given to use it"},
	}
	for _, c := range cases {
		if c.long <= c.short {
			t.Errorf("%s is %s and %s is %s: %s", c.longer, c.long, c.shorter, c.short, c.because)
		}
	}
}

// A runner's start is bounded in three places, and the three have to agree.
// The agent gives itself a budget for the create because a cold image pull is
// minutes; the controller holds the task that long again before re-offering it;
// the scheduler fails the row once it has been starting too long. The scheduler
// is the only one of the three that gives up, so it has to be the most patient
// of them -- and it did not used to be. With a five-minute provision timeout
// against a fifteen-minute create budget, a cold host had its runners condemned
// while the agent was still pulling, and the pool replaced each one, which put a
// second pull of the same image on the link that was slow to begin with.
func TestARunnerIsGivenLongEnoughToStartByEveryHalfThatBoundsIt(t *testing.T) {
	cfg := config.Default()
	// What a start actually costs: the agent's create, then the wait the runner
	// image does for its Docker daemon before it registers.
	start := agent.CreateTimeout + cfg.Runners.EffectiveDockerWait()
	if cfg.Scheduler.ProvisionTimeout <= start {
		t.Errorf("scheduler.provision_timeout is %s and a runner may legitimately take %s to start (agent.CreateTimeout %s plus a %s Docker wait); runners still coming up would be failed and replaced",
			cfg.Scheduler.ProvisionTimeout, start, agent.CreateTimeout, cfg.Runners.EffectiveDockerWait())
	}
	// The lease is how long the controller waits before offering the create to
	// the host again. A runner failed while its create is still leased is one
	// nothing will retry and nothing will finish.
	if createLease < cfg.Scheduler.ProvisionTimeout {
		t.Errorf("the create lease is %s and scheduler.provision_timeout is %s; a runner would be failed with its create neither redelivered nor abandoned",
			createLease, cfg.Scheduler.ProvisionTimeout)
	}
	// The fleet says a runner is not progressing at half the timeout, and that
	// has to land after the Docker wait rather than during it: a runner doing
	// exactly what it was configured to do must not be reported as stuck.
	if half := cfg.Scheduler.ProvisionTimeout / 2; half <= cfg.Runners.EffectiveDockerWait() {
		t.Errorf("runners are reported as not progressing after %s, within the %s a Docker pool's runner is configured to wait for its daemon; a healthy runner would be raised as a problem",
			half, cfg.Runners.EffectiveDockerWait())
	}
}

// The throttle ladder's four constants were each chosen against one in
// another package, and a change to any of them has to say what it does to
// the other side.
func TestTheThrottleLadderIsPacedAgainstTheMeasurementsItClimbsOn(t *testing.T) {
	// A step down waits for a stretch of calm, and calm is only ever judged
	// from a fresh sample. If the recovery were shorter than a sample's life,
	// one calm reading could take a host down a rung before a second reading
	// had a chance to disagree, and a host under pressure that lulled for a
	// moment would bounce between rungs.
	if scheduler.ThrottleRecovery <= store.HostUsageMaxAge {
		t.Fatalf("scheduler.ThrottleRecovery is %s, not longer than store.HostUsageMaxAge at %s; a single calm sample could step a host down", scheduler.ThrottleRecovery, store.HostUsageMaxAge)
	}
	// A rung may not be climbed faster than the CPU hold that feeds the
	// ladder can trip: the hold needs CPUHoldWindow of saturation, and a step
	// shorter than that would let the ladder climb on measurements the hold
	// had not yet judged sustained.
	if scheduler.ThrottleStep < store.CPUHoldWindow {
		t.Fatalf("scheduler.ThrottleStep is %s, shorter than the %s CPU hold that feeds it; the ladder would climb on pressure the hold had not called sustained", scheduler.ThrottleStep, store.CPUHoldWindow)
	}
	// A host that stops measuring is lifted after StaleThrottleReset, and a
	// host that stops heartbeating is given up on after hostLostAfter. The
	// reset has to be the later of the two: a host whose runners were just
	// reclaimed as lost is a host that is about to come back, and it should
	// come back on no rung -- but a host lifted before it was lost would have
	// its full capacity handed back while it was merely late.
	if scheduler.StaleThrottleReset <= hostLostAfter {
		t.Fatalf("scheduler.StaleThrottleReset is %s, not later than hostLostAfter at %s; a late host would be handed its slots back before it was even given up on", scheduler.StaleThrottleReset, hostLostAfter)
	}
	// Half speed doubles a job's time, which the timeout-minutes most
	// workflows set survives; a quarter turns "slow" into "timed out", and a
	// throttle that made jobs fail would be doing the thing it exists to
	// prevent. The top rung takes slots and nothing else.
	if store.MinCPUFactor < 0.5 {
		t.Fatalf("store.MinCPUFactor is %v, below the half speed a running job is promised; a throttled job would time out rather than finish late", store.MinCPUFactor)
	}
}
