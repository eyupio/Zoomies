package controller

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/store"
)

// checkHost seeds a connected host whose agent can answer a check.
func (h *harness) checkHost(name string) *store.Host {
	h.t.Helper()
	return h.hostWith(name, agent.FeatureHostCheck)
}

// takeChecks hands the host's pending tasks to its "agent", as a poll would.
func (h *harness) takeChecks(hostID string) []agent.Task {
	return h.c.queues.get(hostID).take(maxTasksPerPoll, h.c.Now())
}

func (h *harness) view(hostID string) HostView {
	h.t.Helper()
	host, err := h.st.GetHost(h.ctx, hostID)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.c.HostView(host)
}

// answer reports a successful check carrying rep, the way the agent does.
func (h *harness) answer(hostID string, task agent.Task, rep *hosttune.Report) {
	h.t.Helper()
	res := agent.TaskResult{TaskID: task.ID, Kind: agent.TaskCheckHost, OK: true, Doctor: rep}
	if err := h.c.ReportResult(h.ctx, hostID, res); err != nil {
		h.t.Fatal(err)
	}
}

// A refusal must queue nothing: the button is for an agent that is there and
// can answer, and a task left behind for one that is not would run hours later.
func TestAHostThatCannotAnswerIsRefusedWithAReasonAndNothingIsQueued(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *harness) *store.Host
		code  string
		want  string
	}{
		{"an agent that has gone quiet", func(h *harness) *store.Host {
			host := h.checkHost("quiet")
			h.advance(store.HeartbeatTimeout + time.Second)
			return host
		}, HostCheckNotConnected, "not connected"},
		{"an agent on another protocol", func(h *harness) *store.Host {
			host := h.checkHost("skewed")
			if err := h.st.SetHostProtocol(h.ctx, host.ID, 9, true); err != nil {
				t.Fatal(err)
			}
			return host
		}, HostCheckIncompatible, "protocol version 9"},
		{"an agent that does not advertise the feature", func(h *harness) *store.Host {
			return h.hostWith("container")
		}, HostCheckUnsupported, "container"},
		{"a controller behind the fence", func(h *harness) *store.Host {
			host := h.checkHost("fenced")
			h.fence("restored")
			return host
		}, HostCheckFenced, "recovering"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			host := tc.setup(h)
			_, queued, err := h.c.RequestHostCheck(h.ctx, host.ID)
			var he *HostCheckError
			if !errors.As(err, &he) || he.Code != tc.code || !strings.Contains(he.Message, tc.want) {
				t.Fatalf("err = %v, want a %s refusal mentioning %q", err, tc.code, tc.want)
			}
			if queued || len(h.tasksFor(host.ID)) != 0 {
				t.Fatal("a refused request queued a task")
			}
			if v := h.view(host.ID); v.HealthCheck != nil {
				t.Fatalf("a refusal left %+v on the host view", v.HealthCheck)
			}
		})
	}
}

func TestAskingAnUnknownHostIsNotFound(t *testing.T) {
	h := newHarness(t)
	if _, _, err := h.c.RequestHostCheck(h.ctx, "host_nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}
}

// Pressing twice is one request: the second press restates the first rather
// than queueing a second check or reporting an error for a double click.
func TestAskingAHostTwiceQueuesOneTaskAndTheSecondPressChangesNothing(t *testing.T) {
	h := newHarness(t)
	host := h.checkHost("twice")
	got, queued, err := h.c.RequestHostCheck(h.ctx, host.ID)
	if err != nil || !queued || got.ID != host.ID {
		t.Fatalf("first press: queued=%v err=%v", queued, err)
	}
	if v := h.view(host.ID); v.HealthCheck == nil || v.HealthCheck.State != "asked" || v.HealthCheck.NextAt == nil {
		t.Fatalf("view after asking = %+v, want asked with a next_at", v.HealthCheck)
	}
	if _, queued, err := h.c.RequestHostCheck(h.ctx, host.ID); err != nil || queued {
		t.Fatalf("second press: queued=%v err=%v, want an idempotent no-op", queued, err)
	}
	if n := len(h.tasksFor(host.ID)); n != 1 {
		t.Fatalf("%d tasks queued, want 1", n)
	}
}

// A cordoned host is still checkable: cordoning stops new work, and a check is
// read-only and takes no slot.
func TestACordonedHostCanStillBeChecked(t *testing.T) {
	h := newHarness(t)
	host := h.checkHost("cordoned")
	if err := h.st.SetHostCordoned(h.ctx, host.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, queued, err := h.c.RequestHostCheck(h.ctx, host.ID); err != nil || !queued {
		t.Fatalf("queued=%v err=%v", queued, err)
	}
}

// The cooldown runs from the request and a failure does not clear it. Clearing
// it would let a loop with a token hammer an agent that fails at once.
func TestTheCooldownRunsFromTheRequestAndAFailureDoesNotClearIt(t *testing.T) {
	h := newHarness(t)
	host := h.checkHost("cool")
	_, _, err := h.c.RequestHostCheck(h.ctx, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	task := h.takeChecks(host.ID)[0]
	if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{TaskID: task.ID, Kind: agent.TaskCheckHost, OK: false, Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	h.advance(5 * time.Second)
	_, queued, err := h.c.RequestHostCheck(h.ctx, host.ID)
	var cd *HostCheckCooldownError
	if !errors.As(err, &cd) || queued {
		t.Fatalf("err = %v queued=%v, want the cooldown", err, queued)
	}
	if !strings.Contains(err.Error(), "Ask again in 10 s") {
		t.Fatalf("message = %q, want the seconds left", err.Error())
	}
	h.advance(HostCheckCooldown)
	if _, queued, err := h.c.RequestHostCheck(h.ctx, host.ID); err != nil || !queued {
		t.Fatalf("after the cooldown: queued=%v err=%v", queued, err)
	}
}

// Whatever the agent does, a check is delivered once: a third delivery to an
// operator who gave up long ago would be a check nobody is waiting for.
func TestACheckGetsOneAttemptAndEveryOtherKindKeepsThree(t *testing.T) {
	if maxAttempts(agent.TaskCheckHost) != 1 {
		t.Fatalf("check_host attempts = %d, want 1", maxAttempts(agent.TaskCheckHost))
	}
	for _, k := range []agent.TaskKind{agent.TaskCreateRunner, agent.TaskStopRunner, agent.TaskRemoveRunner, agent.TaskPrewarmImage, agent.TaskFillToolCache} {
		if maxAttempts(k) != maxTaskAttempts {
			t.Errorf("%s attempts = %d, want %d", k, maxAttempts(k), maxTaskAttempts)
		}
	}
	h := newHarness(t)
	host := h.checkHost("once")
	if _, _, err := h.c.RequestHostCheck(h.ctx, host.ID); err != nil {
		t.Fatal(err)
	}
	task := h.takeChecks(host.ID)[0]
	// The agent gives it back unstarted: out of attempts, so it is not offered again.
	err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{TaskID: task.ID, Kind: agent.TaskCheckHost, NotStarted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.tasksFor(host.ID)) != 0 {
		t.Fatal("a check given back was queued for a second delivery")
	}
	if v := h.view(host.ID); v.HealthCheck == nil || v.HealthCheck.State != "failed" {
		t.Fatalf("view = %+v, want failed", v.HealthCheck)
	}
}

// A check with no runner behind it needs a lease or its key stays outstanding
// for ever and the button is dead on that host.
func TestALostCheckExpiresAndTheHostCanBeAskedAgain(t *testing.T) {
	h := newHarness(t)
	host := h.checkHost("lost")
	if _, _, err := h.c.RequestHostCheck(h.ctx, host.ID); err != nil {
		t.Fatal(err)
	}
	h.takeChecks(host.ID)
	h.advance(hostCheckLease + time.Second)
	// Keep the host connected so the second request is not refused for silence.
	if _, err := h.c.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Features: []string{agent.FeatureHostCheck}}); err != nil {
		t.Fatal(err)
	}
	h.c.sweepTasks(h.ctx, h.c.Now())
	if v := h.view(host.ID); v.HealthCheck == nil || v.HealthCheck.State != "failed" {
		t.Fatalf("view = %+v, want failed after the lease", v.HealthCheck)
	}
	if h.c.queues.get(host.ID).depthTotal() != 0 {
		t.Fatal("the lost check is still outstanding")
	}
	if _, queued, err := h.c.RequestHostCheck(h.ctx, host.ID); err != nil || !queued {
		t.Fatalf("asking again: queued=%v err=%v", queued, err)
	}
}

func (q *taskQueue) depthTotal() int { p, f := q.depth(); return p + f }

// Nobody listening means the task would run when the agent next appears, long
// after the press. It is dropped and the page is told why.
func TestACheckNobodyPickedUpIsDroppedAfterTwentySeconds(t *testing.T) {
	h := newHarness(t)
	host := h.checkHost("deaf")
	if _, _, err := h.c.RequestHostCheck(h.ctx, host.ID); err != nil {
		t.Fatal(err)
	}
	h.advance(hostCheckPendingTTL - time.Second)
	h.c.sweepTasks(h.ctx, h.c.Now())
	if len(h.tasksFor(host.ID)) != 1 {
		t.Fatal("a check was dropped before its twenty seconds")
	}
	h.advance(2 * time.Second)
	h.c.sweepTasks(h.ctx, h.c.Now())
	if len(h.tasksFor(host.ID)) != 0 {
		t.Fatal("a stale pending check was kept")
	}
	v := h.view(host.ID)
	if v.HealthCheck == nil || v.HealthCheck.State != "failed" || !strings.Contains(v.HealthCheck.Message, "did not pick the request up") {
		t.Fatalf("view = %+v, want a failure saying the request was not picked up", v.HealthCheck)
	}
}

// What the page does with the answer depends on how it compares with the last
// report, and the first frame carrying a changed report must already say so.
func TestAnAnsweredCheckIsIngestedAndSaysHowItComparedWithTheLastReport(t *testing.T) {
	cases := []struct {
		name  string
		prior func(h *harness, host *store.Host, base time.Time)
		rep   func(base time.Time) *hosttune.Report
		want  string
	}{
		{"the first report", func(*harness, *store.Host, time.Time) {}, func(b time.Time) *hosttune.Report {
			return reportAt(b.Add(time.Second), "61% free", hosttune.Warn)
		}, "first"},
		{"a report that changes a finding", func(h *harness, host *store.Host, b time.Time) {
			h.sendCheckableReport(host.ID, reportAt(b, "61% free", hosttune.Warn))
		}, func(b time.Time) *hosttune.Report { return reportAt(b.Add(time.Second), "61% free", hosttune.OK) }, "changed"},
		{"a report that says the same", func(h *harness, host *store.Host, b time.Time) {
			h.sendCheckableReport(host.ID, reportAt(b, "61% free", hosttune.Warn))
		}, func(b time.Time) *hosttune.Report { return reportAt(b.Add(time.Second), "60% free", hosttune.Warn) }, "unchanged"},
		{"a report from a clock that is behind", func(h *harness, host *store.Host, b time.Time) {
			h.sendCheckableReport(host.ID, reportAt(b, "61% free", hosttune.Warn))
		}, func(b time.Time) *hosttune.Report { return reportAt(b.Add(-time.Second), "61% free", hosttune.OK) }, "not_newer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			host := h.checkHost("answers")
			base := h.c.Now().Truncate(time.Millisecond)
			tc.prior(h, host, base)
			if _, _, err := h.c.RequestHostCheck(h.ctx, host.ID); err != nil {
				t.Fatal(err)
			}
			task := h.takeChecks(host.ID)[0]
			beatBefore := h.mustHost(host.ID).LastHeartbeat
			sub := h.c.Events().Subscribe(h.ctx, events.SubscribeOptions{Kinds: []events.Kind{events.KindHostUpdated}})
			defer sub.Close()
			rep := tc.rep(base)
			h.answer(host.ID, task, rep)

			var last HostView
			frames := 0
			for {
				select {
				case ev := <-sub.C:
					frames++
					if err := json.Unmarshal(ev.Data, &last); err != nil {
						t.Fatal(err)
					}
				case <-time.After(200 * time.Millisecond):
					goto done
				}
			}
		done:
			if frames == 0 {
				t.Fatal("an answered check published no host frame")
			}
			if last.HealthCheck == nil || last.HealthCheck.State != "done" || last.HealthCheck.Outcome != tc.want {
				t.Fatalf("last frame health_check = %+v, want done/%s", last.HealthCheck, tc.want)
			}
			got := h.mustHost(host.ID)
			if !got.LastHeartbeat.Equal(beatBefore) {
				t.Fatal("a task result moved last_heartbeat; it must not pose as a heartbeat")
			}
			if tc.want == "unchanged" && !got.Doctor.CheckedAt.Equal(rep.CheckedAt) {
				t.Fatalf("freshness = %v, want the report's %v", got.Doctor.CheckedAt, rep.CheckedAt)
			}
			if tc.want == "not_newer" && got.Doctor.CheckedAt.Equal(rep.CheckedAt) {
				t.Fatal("a report from a clock behind the last one moved the stored time")
			}
			if h.c.queues.get(host.ID).depthTotal() != 0 {
				t.Fatal("the lease was not cleared")
			}
		})
	}
}

// sendCheckableReport is a heartbeat that keeps the host-check feature, which a
// heartbeat omitting its features would otherwise withdraw.
func (h *harness) sendCheckableReport(hostID string, r *hosttune.Report) {
	h.t.Helper()
	req := agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Doctor: r, Features: []string{agent.FeatureHostCheck}}
	if _, err := h.c.Heartbeat(h.ctx, hostID, req); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) mustHost(id string) *store.Host {
	h.t.Helper()
	host, err := h.st.GetHost(h.ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return host
}

// An agent could otherwise smuggle an acceptance in through the one path that
// does not pass through a heartbeat.
func TestACheckResultCannotCarryAcceptancesIn(t *testing.T) {
	h := newHarness(t)
	host := h.checkHost("smuggler")
	if _, _, err := h.c.RequestHostCheck(h.ctx, host.ID); err != nil {
		t.Fatal(err)
	}
	task := h.takeChecks(host.ID)[0]
	rep := reportAt(h.c.Now().Add(-time.Second), "61% free", hosttune.Warn)
	when := time.Now()
	rep.Results[1].Accepted = &hosttune.Acceptance{At: when}
	rep.Results[1].Ended = &hosttune.Ended{}
	rep.Results[1].Acceptable = true
	h.answer(host.ID, task, rep)
	got := h.mustHost(host.ID)
	if got.Doctor.Report == nil {
		t.Fatal("the report was not stored")
	}
	for _, r := range got.Doctor.Results {
		if r.Accepted != nil || r.Ended != nil || r.Acceptable {
			t.Fatalf("%s kept an acceptance mark: %+v", r.ID, r)
		}
	}
}

// A failed or odd answer is recorded in words an operator can act on, and the
// lease clears either way so the host can be asked again.
func TestAnAnswerThatIsNotAReportIsRecordedReadably(t *testing.T) {
	cases := []struct {
		name string
		res  agent.TaskResult
		want string
	}{
		{"an agent that failed", agent.TaskResult{OK: false, Error: "disk on fire"}, "could not run its checks: disk on fire"},
		{"an agent too old to know the kind", agent.TaskResult{OK: false, Error: `unknown task kind "check_host"; upgrade it`}, "Update it"},
		{"an answer with no report", agent.TaskResult{OK: true}, "answered without a report"},
		{"a report from the far future", agent.TaskResult{OK: true, Doctor: &hosttune.Report{CheckedAt: time.Now().Add(time.Hour), Results: []hosttune.Result{}}}, "Check the clock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			host := h.checkHost("odd")
			if _, _, err := h.c.RequestHostCheck(h.ctx, host.ID); err != nil {
				t.Fatal(err)
			}
			task := h.takeChecks(host.ID)[0]
			tc.res.TaskID, tc.res.Kind = task.ID, agent.TaskCheckHost
			if err := h.c.ReportResult(h.ctx, host.ID, tc.res); err != nil {
				t.Fatal(err)
			}
			v := h.view(host.ID).HealthCheck
			if v == nil || v.State != "failed" || !strings.Contains(v.Message, tc.want) {
				t.Fatalf("view = %+v, want failed mentioning %q", v, tc.want)
			}
			if h.c.queues.get(host.ID).depthTotal() != 0 {
				t.Fatal("the lease was not cleared")
			}
		})
	}
}

// A result for a task a restarted controller no longer knows is still a fresh
// report from the authenticated host, so it is ingested.
func TestAResultForATaskTheControllerForgotIsStillIngested(t *testing.T) {
	h := newHarness(t)
	host := h.checkHost("restarted")
	rep := reportAt(h.c.Now().Add(-time.Second), "61% free", hosttune.Warn)
	if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{TaskID: "task_gone", Kind: agent.TaskCheckHost, OK: true, Doctor: rep}); err != nil {
		t.Fatal(err)
	}
	if h.mustHost(host.ID).Doctor.Report == nil {
		t.Fatal("the report was dropped")
	}
}

// The page gives up before the agent's own bound would, and a late answer still
// lands: it is fresh, read-only data.
func TestAnAskedCheckRendersAsFailedPastPatienceAndALateAnswerFlipsItToDone(t *testing.T) {
	h := newHarness(t)
	host := h.checkHost("slow")
	if _, _, err := h.c.RequestHostCheck(h.ctx, host.ID); err != nil {
		t.Fatal(err)
	}
	task := h.takeChecks(host.ID)[0]
	h.advance(hostCheckPatience - time.Second)
	if v := h.view(host.ID).HealthCheck; v == nil || v.State != "asked" {
		t.Fatalf("view = %+v, want still asked", v)
	}
	h.advance(2 * time.Second)
	v := h.view(host.ID).HealthCheck
	if v == nil || v.State != "failed" || !strings.Contains(v.Message, "did not answer in time") {
		t.Fatalf("view = %+v, want failed for want of an answer", v)
	}
	h.answer(host.ID, task, reportAt(h.c.Now().Add(-time.Second), "61% free", hosttune.Warn))
	if v := h.view(host.ID).HealthCheck; v == nil || v.State != "done" {
		t.Fatalf("view = %+v, want done after the late answer", v)
	}
}

// The view is in memory and is gone once it has nothing to say.
func TestTheHealthCheckLeavesTheViewWhenItIsIdleAndWhenTheHostIsDeleted(t *testing.T) {
	h := newHarness(t)
	host := h.checkHost("tidy")
	raw, _ := json.Marshal(h.view(host.ID))
	if strings.Contains(string(raw), "health_check") {
		t.Fatal("an idle host rendered a health_check key")
	}
	if _, _, err := h.c.RequestHostCheck(h.ctx, host.ID); err != nil {
		t.Fatal(err)
	}
	task := h.takeChecks(host.ID)[0]
	h.answer(host.ID, task, reportAt(h.c.Now().Add(-time.Second), "61% free", hosttune.Warn))
	v := h.view(host.ID).HealthCheck
	if v == nil || v.State != "done" || v.NextAt == nil {
		t.Fatalf("view = %+v, want done inside its cooldown", v)
	}
	h.advance(HostCheckCooldown + time.Second)
	if v := h.view(host.ID).HealthCheck; v == nil || v.NextAt != nil {
		t.Fatalf("view = %+v, want done with the cooldown over", v)
	}
	h.advance(hostCheckKeep)
	if v := h.view(host.ID).HealthCheck; v != nil {
		t.Fatalf("view = %+v, want nothing once the result is old", v)
	}
	if _, _, err := h.c.RequestHostCheck(h.ctx, host.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.c.DeleteHost(h.ctx, host.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.c.hostChecks.get(host.ID); ok {
		t.Fatal("a deleted host's request was remembered")
	}
}

// The taskKey and lifecycleTask rows pin that a check is its own key per host
// and never mistaken for work on a runner.
func TestACheckIsOneKeyPerHostAndNeverLifecycleWork(t *testing.T) {
	if got := taskKey(agent.Task{Kind: agent.TaskCheckHost}); got != "check_host|" {
		t.Fatalf("taskKey = %q", got)
	}
	if lifecycleTask(agent.TaskCheckHost) {
		t.Fatal("a check counted as lifecycle work")
	}
	if requeueAfter(agent.TaskCheckHost) <= hosttune.MonitorRunTimeout {
		t.Fatal("the lease does not outlast the agent's bound on one run")
	}
}
