package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// jsonRoutes serves a fixed body per path, which is all these commands need:
// what is being tested is what the CLI renders, not what the controller
// decides.
func jsonRoutes(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"no"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runCLI(t *testing.T, args ...string) (string, string) {
	t.Helper()
	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, args); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	return out.String(), errOut.String()
}

// `pools get` is where an operator looks before changing anything, so it has to
// show the settings that weaken the defaults rather than only the safe ones.
func TestPoolsGetShowsTheDangerousSettings(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/pools/pool_1": `{
			"id":"pool_1","name":"zoomies-4vcpu","installation_id":"inst_1",
			"installation_target":"acme","labels":["zoomies-4vcpu"],"backend":"docker",
			"platform":{"os":"ubuntu","os_version":"24.04","arch":"amd64"},
			"effective_image":"ghcr.io/eyupio/zoomies-runner:ubuntu-24.04",
			"min_runners":1,"max_runners":8,"priority":2,"idle_timeout":"5m",
			"ephemeral":true,"docker_mode":"host-socket","run_as_root":true,
			"host_selector":{"zone":"eu"},"enabled":true,
			"counts":{"live":3,"idle":1,"busy":2},"queued_jobs":4,"utilisation":0.66,
			"warnings":[
				{"code":"pool.docker_socket","severity":"warning","title":"Jobs can reach the Docker socket",
				 "detail":"A job can start a container outside the fleet.","fix":"Set docker mode to none or dind."},
				{"code":"pool.root","severity":"error","title":"Runners run as root"}
			]}`,
	})

	out, _ := runCLI(t, "pools", "get", "pool_1", "--url", srv.URL)

	for _, want := range []string{"zoomies-4vcpu", "host-socket", "ubuntu 24.04", "acme"} {
		if !strings.Contains(out, want) {
			t.Errorf("pools get must show %q:\n%s", want, out)
		}
	}
	// A pool that pins no image still has to say what its runners will boot,
	// and where that came from, rather than leaving a dash.
	if !strings.Contains(out, "from the pool's platform") {
		t.Errorf("the derived image must say where it came from:\n%s", out)
	}
	for _, want := range []string{"Docker socket", "run as root", "fix: Set docker mode"} {
		if !strings.Contains(out, want) {
			t.Errorf("the warnings must be shown, including %q:\n%s", want, out)
		}
	}
}

// A pool that pins an image shows exactly that, with nothing about platforms:
// the pin is the answer.
func TestPoolsGetShowsAPinnedImageAsItIs(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/pools/pool_1": `{"id":"pool_1","name":"p","image":"my.registry/runner:v3",
			"effective_image":"ignored","enabled":true,"counts":{}}`,
	})

	out, _ := runCLI(t, "pools", "get", "pool_1", "--url", srv.URL)

	if !strings.Contains(out, "my.registry/runner:v3") {
		t.Errorf("the pinned image must be shown:\n%s", out)
	}
	if strings.Contains(out, "from the pool's platform") {
		t.Errorf("a pinned image must not be attributed to the platform:\n%s", out)
	}
	// Silence is the right output for "nothing is wrong".
	if strings.Contains(out, "weaken the defaults") {
		t.Errorf("a pool with no warnings printed a warnings heading:\n%s", out)
	}
}

// Disabling says what happens next, because "disabled" on its own reads as
// "the runners are gone" and they are not: they finish what they are doing.
func TestPoolsDisableSaysWhatHappensToTheRunners(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/pools/pool_1/disable": `{"id":"pool_1","name":"zoomies-4vcpu","enabled":false}`,
	})

	out, _ := runCLI(t, "pools", "disable", "pool_1", "--url", srv.URL)

	if !strings.Contains(out, "disabled") || !strings.Contains(out, "drain") {
		t.Errorf("disable must say the runners drain:\n%s", out)
	}
}

func TestPoolsEnableConfirmsByName(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/pools/pool_1/enable": `{"id":"pool_1","name":"zoomies-4vcpu","enabled":true}`,
	})

	out, _ := runCLI(t, "pools", "enable", "pool_1", "--url", srv.URL)

	if !strings.Contains(out, "zoomies-4vcpu") || !strings.Contains(out, "enabled") {
		t.Errorf("enable must confirm which pool:\n%s", out)
	}
}

// Prewarming is per host, and the per-host outcome is the point: one host
// failing to pull is exactly what this command exists to surface.
func TestPoolsPrewarmReportsEachHost(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/pools/pool_1/prewarm": `{"queued":2,"hosts":[
			{"host_name":"vm-1","state":"succeeded","digest":"sha256:abc"},
			{"host_name":"vm-2","state":"failed","error":"no space left on device"}]}`,
	})

	out, _ := runCLI(t, "pools", "prewarm", "pool_1", "--url", srv.URL)

	for _, want := range []string{"2 host", "vm-1", "sha256:abc", "vm-2", "no space left"} {
		if !strings.Contains(out, want) {
			t.Errorf("prewarm must report %q:\n%s", want, out)
		}
	}
}

// `runners get` is the page an operator opens when one runner is wrong, so it
// has to carry the job it is on and how it got to the state it is in.
func TestRunnersGetShowsTheCurrentJobAndTimeline(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/runners/run_1": `{
			"id":"run_1","name":"zoomies-abc","state":"busy","pool_name":"zoomies-4vcpu",
			"host_name":"vm-1","ephemeral":true,"labels":["zoomies-4vcpu"],
			"image":"ghcr.io/eyupio/zoomies-runner:ubuntu-24.04","container_id":"ctr-abc",
			"jobs_handled":0,"logs_available":true,"message":"pulled in 3s",
			"created_at":"2025-01-01T00:00:00Z","started_at":"2025-01-01T00:00:05Z",
			"current_job":{"repo":"acme/widgets","workflow":"CI","job_name":"build"},
			"timeline":[
				{"state":"provisioning","at":"2025-01-01T00:00:00Z","duration_ms":5000},
				{"state":"busy","at":"2025-01-01T00:00:05Z","message":"picked up build"}]}`,
	})

	out, _ := runCLI(t, "runners", "get", "run_1", "--url", srv.URL)

	for _, want := range []string{"zoomies-abc", "vm-1", "ctr-abc", "pulled in 3s"} {
		if !strings.Contains(out, want) {
			t.Errorf("runners get must show %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "acme/widgets") || !strings.Contains(out, "build") {
		t.Errorf("the job the runner is on must be shown:\n%s", out)
	}
	if !strings.Contains(out, "Timeline") || !strings.Contains(out, "provisioning") {
		t.Errorf("how it got here must be shown:\n%s", out)
	}
}

// A runner with no job and nothing to say prints neither, rather than empty
// rows an operator has to read past.
func TestRunnersGetOmitsWhatIsNotThere(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/runners/run_1": `{"id":"run_1","name":"zoomies-abc","state":"idle",
			"created_at":"2025-01-01T00:00:00Z"}`,
	})

	out, _ := runCLI(t, "runners", "get", "run_1", "--url", srv.URL)

	if strings.Contains(out, "current job") {
		t.Errorf("an idle runner was given a current job row:\n%s", out)
	}
	if strings.Contains(out, "Timeline") {
		t.Errorf("a runner with no history printed an empty timeline:\n%s", out)
	}
}

// Uncordoning says how much room the host has, because that is the question
// the operator is actually asking: is it taking work again, and how much.
func TestHostsUncordonSaysHowMuchRoomThereIs(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/hosts/hst_1/cordon": `{"id":"hst_1","name":"vm-1","cordoned":false,"free":3}`,
	})

	out, _ := runCLI(t, "hosts", "uncordon", "hst_1", "--url", srv.URL)

	if !strings.Contains(out, "vm-1") || !strings.Contains(out, "3 more runner") {
		t.Errorf("uncordon must say how much room there is:\n%s", out)
	}
}

// "Why is this host full" is answered by what the machine is and how big it
// is, so a listing that shows neither is a listing an operator cannot act on.
func TestHostsListShowsWhatEachMachineIs(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/hosts": `{"items":[
			{"id":"hst_1","name":"vm-1","platform_label":"ubuntu 24.04, amd64","cpus":8,
			 "memory_mb":16384,"capacity":4,"active_runners":2,"free":2,"healthy":true,
			 "embedded":true,"backends":["docker"],"version":"1.0.0"},
			{"id":"hst_2","name":"vm-2","os":"linux","arch":"arm64","capacity":2,
			 "active_runners":0,"free":2,"healthy":false,"backends":["podman"]}],
			"total":2}`,
	})

	out, _ := runCLI(t, "hosts", "list", "--url", srv.URL)

	if !strings.Contains(out, "ubuntu 24.04, amd64") {
		t.Errorf("the platform the controller rendered must be used as-is:\n%s", out)
	}
	// An agent too old to report a distribution still says what it is, from
	// the kernel and the architecture.
	if !strings.Contains(out, "linux/arm64") {
		t.Errorf("an old agent's host must still say what it is:\n%s", out)
	}
	if !strings.Contains(out, "8 vCPU") || !strings.Contains(out, "16 GB") {
		t.Errorf("how much machine it is must be shown:\n%s", out)
	}
}

func TestUsersListShowsRolesAndWhoMustChangeTheirPassword(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/users": `{"items":[
			{"id":"usr_1","username":"ada","role":"admin","display_name":"Ada L",
			 "email":"ada@example.com","must_change_password":true},
			{"id":"usr_2","username":"mel","role":"viewer","disabled":true}],"total":2}`,
	})

	out, _ := runCLI(t, "users", "list", "--url", srv.URL)

	for _, want := range []string{"ada", "admin", "Ada L", "must change password", "mel", "disabled"} {
		if !strings.Contains(out, want) {
			t.Errorf("users list must show %q:\n%s", want, out)
		}
	}
}

// An instance with no users cannot be signed into at all, so the empty listing
// has to say what to do about it rather than print an empty table.
func TestUsersListSaysWhatAnEmptyInstanceNeeds(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{"/api/v1/users": `{"items":[],"total":0}`})

	out, _ := runCLI(t, "users", "list", "--url", srv.URL)

	if !strings.Contains(out, "first administrator") {
		t.Errorf("an empty user list must say what to do:\n%s", out)
	}
}

// An audit row's actor is a person or it is not, and a token acting on its own
// must not be rendered as though somebody was at the keyboard.
func TestAuditListNamesNonHumanActors(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/audit": `{"items":[
			{"id":"aud_1","created_at":"2025-01-01T00:00:00Z","actor_name":"ada",
			 "actor_kind":"user","action":"pool.create","target_kind":"pool",
			 "target_id":"pool_1","ip":"10.0.0.1"},
			{"id":"aud_2","created_at":"2025-01-01T00:01:00Z","actor_name":"ci-token",
			 "actor_kind":"token","action":"runner.delete","target_kind":"runner",
			 "target_id":"run_9"}],
			"total":2,"limit":50,"offset":0}`,
	})

	out, _ := runCLI(t, "audit", "list", "--url", srv.URL)

	if !strings.Contains(out, "ci-token (token)") {
		t.Errorf("a token acting on its own must be marked as one:\n%s", out)
	}
	if !strings.Contains(out, "ada") || strings.Contains(out, "ada (user)") {
		t.Errorf("a person is just their name:\n%s", out)
	}
	for _, want := range []string{"pool.create", "pool pool_1", "10.0.0.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("audit list must show %q:\n%s", want, out)
		}
	}
}

func TestAuditListSaysWhenNothingMatches(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/audit": `{"items":[],"total":0,"limit":50,"offset":0}`,
	})

	out, _ := runCLI(t, "audit", "list", "--url", srv.URL, "--action", "pool.create", "--since", "24h")

	if !strings.Contains(out, "No audit rows match") {
		t.Errorf("an empty result must say so:\n%s", out)
	}
}

// A bad --since is the operator's typo, not a server error, so it is reported
// as usage before a request is made.
func TestAuditListRejectsAnUnparseableSince(t *testing.T) {
	e, _, errOut := newTestEnv(t)

	code := dispatch(context.Background(), e, []string{"audit", "list", "--url", "http://127.0.0.1:1", "--since", "last tuesday"})

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d\n%s", code, exitUsage, errOut)
	}
	if !strings.Contains(errOut.String(), "--since") {
		t.Errorf("the complaint must name the flag:\n%s", errOut)
	}
}

// The elastic CPU policy is one object, like the size: an edit that types only
// the ceiling has to carry the mode forward, or "raise the ceiling" would
// quietly switch elasticity off.
func TestPoolsEditCarriesTheElasticCPUModeForward(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"id":"pool_1","name":"zoomies-4vcpu","resources":{},"sizing":"automatic",
				"cpu_burst":{"mode":"automatic","max_cpus":0}}`))
		case http.MethodPatch:
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Errorf("decoding the PATCH body: %v", err)
			}
			_, _ = w.Write([]byte(`{"id":"pool_1","name":"zoomies-4vcpu","enabled":true,
				"cpu_burst":{"mode":"automatic","max_cpus":6}}`))
		}
	}))
	t.Cleanup(srv.Close)

	out, _ := runCLI(t, "pools", "edit", "pool_1", "--cpu-burst-max", "6", "--url", srv.URL)

	burst, _ := sent["cpu_burst"].(map[string]any)
	if burst["mode"] != "automatic" || burst["max_cpus"] != 6.0 {
		t.Errorf("the PATCH must keep the mode and set the ceiling, got %v", sent["cpu_burst"])
	}
	if _, ok := sent["resources"]; ok {
		t.Errorf("an edit that touches no part of the size must not send one: %v", sent)
	}
	if !strings.Contains(out, "zoomies-4vcpu") {
		t.Errorf("the edited pool must be confirmed by name:\n%s", out)
	}
}

// `pools get` has to say what the runner page says: a pool sized by its host
// and lent spare CPU is not "cpus 0", which reads as unlimited.
func TestPoolsGetShowsSizingAndTheElasticCPUPolicy(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/pools/pool_1": `{"id":"pool_1","name":"zoomies-4vcpu","backend":"docker",
			"resources":{},"sizing":"automatic","cpu_burst":{"mode":"automatic","max_cpus":6},
			"counts":{}}`,
	})

	out, _ := runCLI(t, "pools", "get", "pool_1", "--url", srv.URL)

	for _, want := range []string{"one slot's share", "automatic, up to 6 CPUs per runner"} {
		if !strings.Contains(out, want) {
			t.Errorf("pools get must show %q:\n%s", want, out)
		}
	}
}

// A ceiling typed on a create without a mode would be sent as an explicit
// empty mode, which the API reads as off, so the ceiling would bind nothing
// and the pool would quietly miss the observe default it would otherwise get.
func TestPoolsCreateRefusesACeilingWithoutAMode(t *testing.T) {
	e, _, errOut := newTestEnv(t)
	code := dispatch(context.Background(), e, []string{"pools", "create", "--name", "p", "--labels", "p",
		"--cpu-burst-max", "4", "--url", "http://127.0.0.1:1"})
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d: a ceiling without a mode must be refused as a usage error", code, exitUsage)
	}
	if !strings.Contains(errOut.String(), "--cpu-burst") {
		t.Errorf("the refusal must name the flag to add:\n%s", errOut.String())
	}
}

// patchServer answers a pool's GET with `existing` and records the body of the
// PATCH or POST that follows, which is what these tests are about.
func patchServer(t *testing.T, existing string, sent *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(existing))
			return
		}
		if err := json.NewDecoder(r.Body).Decode(sent); err != nil {
			t.Errorf("decoding the %s body: %v", r.Method, err)
		}
		_, _ = w.Write([]byte(`{"id":"pool_1","name":"zoomies-p","enabled":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A pool that takes its size from the host sends the choice and states no
// size: the API refuses both together, and a create that carried the sliders'
// figures along would be refused for a thing the operator never typed.
func TestPoolsCreateCanTakeItsSizeFromTheHost(t *testing.T) {
	var sent map[string]any
	srv := patchServer(t, `{}`, &sent)

	runCLI(t, "pools", "create", "--name", "p", "--labels", "p", "--installation", "ins_1",
		"--size-from-host", "--url", srv.URL)

	if sent["size_from_profile"] != true {
		t.Errorf("the create must say the pool takes its size from the host: %v", sent)
	}
	if res, _ := sent["resources"].(map[string]any); res["cpus"] != nil || res["memory_mb"] != nil {
		t.Errorf("a pool that takes its size from the host states none: %v", sent["resources"])
	}
}

// Moving a pool from a stated size to the host's clears the figures in the same
// request, because the two are refused together. Disk is independent of the
// choice, so the edit carries it forward rather than quietly dropping a limit
// the operator set.
func TestPoolsEditMovingToTheHostClearsTheStatedSizeAndKeepsTheDisk(t *testing.T) {
	var sent map[string]any
	srv := patchServer(t, `{"id":"pool_1","name":"zoomies-p","sizing":"fixed",
		"resources":{"cpus":4,"memory_mb":8192,"disk_gb":40}}`, &sent)

	runCLI(t, "pools", "edit", "pool_1", "--size-from-host", "--url", srv.URL)

	if sent["size_from_profile"] != true {
		t.Errorf("the PATCH must carry the choice: %v", sent)
	}
	res, ok := sent["resources"].(map[string]any)
	if !ok {
		t.Fatalf("the PATCH must send the size it clears, got %v", sent)
	}
	if _, has := res["cpus"]; has {
		t.Errorf("the stated CPU must be cleared: %v", res)
	}
	if _, has := res["memory_mb"]; has {
		t.Errorf("the stated memory must be cleared: %v", res)
	}
	if res["disk_gb"] != 40.0 {
		t.Errorf("the disk limit is independent of the choice and must be kept: %v", res)
	}
}

// A PATCH reads an absent key as "leave it alone", so handing a pool back to the
// share of its host has to send false -- and must not touch the size.
func TestPoolsEditHandsAPoolBackToTheShareOfItsHost(t *testing.T) {
	var sent map[string]any
	srv := patchServer(t, `{"id":"pool_1","name":"zoomies-p","sizing":"profile","size_from_profile":true,"resources":{}}`, &sent)

	runCLI(t, "pools", "edit", "pool_1", "--size-from-host=false", "--url", srv.URL)

	if v, ok := sent["size_from_profile"]; !ok || v != false {
		t.Errorf("the PATCH must say false, not leave the key out: %v", sent)
	}
	if _, ok := sent["resources"]; ok {
		t.Errorf("handing a pool back to its host's share touches no stated size: %v", sent)
	}
}

func TestPoolsRefuseASizeFromTheHostAndAStatedSizeTogether(t *testing.T) {
	for _, args := range [][]string{
		{"pools", "create", "--name", "p", "--labels", "p", "--installation", "ins_1", "--size-from-host", "--cpus", "4"},
		{"pools", "edit", "pool_1", "--size-from-host", "--memory-mb", "4096"},
	} {
		e, _, errOut := newTestEnv(t)
		code := dispatch(context.Background(), e, append(args, "--url", "http://127.0.0.1:1"))
		if code != exitUsage {
			t.Fatalf("%v: exit code = %d, want %d", args, code, exitUsage)
		}
		if !strings.Contains(errOut.String(), "--size-from-host") || !strings.Contains(errOut.String(), "--cpus or --memory-mb") {
			t.Errorf("%v: the refusal must name both flags:\n%s", args, errOut.String())
		}
	}
}

// `pools get` has to say where a profile pool's size comes from, and what stands
// in for a host that sets none: "automatic" would send an operator to the slot
// share, which is not what this pool does.
func TestPoolsGetSaysAPoolTakesItsSizeFromItsHosts(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/pools/pool_1": `{"id":"pool_1","name":"zoomies-p","backend":"docker","resources":{},
			"sizing":"profile","size_from_profile":true,"fleet_standard":{"cpus":2,"memory_mb":4096},
			"cpu_burst":{"mode":"off"},"counts":{}}`,
	})

	out, _ := runCLI(t, "pools", "get", "pool_1", "--url", srv.URL)

	if !strings.Contains(out, "profile: the standard size each host sets") ||
		!strings.Contains(out, "the fleet's default of 2 CPUs and 4096 MB where a host sets none") {
		t.Errorf("pools get must say where the size comes from and what stands in:\n%s", out)
	}
	if strings.Contains(out, "one slot's share") {
		t.Errorf("a profile pool is not sized by a slot's share:\n%s", out)
	}
}

// The in-memory folders are one object, like the size: an edit that types only
// a size for /tmp has to carry the work folder's mode forward from the pool as
// it stands, or "make /tmp bigger" would quietly put _work back on disk.
func TestPoolsEditCarriesTheOtherInMemoryFolderForward(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"id":"pool_1","name":"zoomies-4vcpu","resources":{},"sizing":"automatic",
				"tmpfs":{"work":{"enabled":true,"size_mb":6144},"tmp":{"enabled":true}}}`))
		case http.MethodPatch:
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Errorf("decoding the PATCH body: %v", err)
			}
			_, _ = w.Write([]byte(`{"id":"pool_1","name":"zoomies-4vcpu","enabled":true}`))
		}
	}))
	t.Cleanup(srv.Close)

	runCLI(t, "pools", "edit", "pool_1", "--tmpfs-tmp-size", "2048", "--url", srv.URL)

	tmpfs, _ := sent["tmpfs"].(map[string]any)
	work, _ := tmpfs["work"].(map[string]any)
	tmp, _ := tmpfs["tmp"].(map[string]any)
	if work["enabled"] != true || work["size_mb"] != 6144.0 {
		t.Errorf("the work folder must be carried forward untouched, got %v", work)
	}
	if tmp["enabled"] != true || tmp["size_mb"] != 2048.0 {
		t.Errorf("/tmp must keep its mode and take the new size, got %v", tmp)
	}
}

// An edit that touches nothing about the folders must not send them, or a PATCH
// for a label would reset them to whatever this CLI's flag defaults say.
func TestPoolsEditLeavesTheInMemoryFoldersAloneWhenNotNamed(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPatch {
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Errorf("decoding the PATCH body: %v", err)
			}
		}
		_, _ = w.Write([]byte(`{"id":"pool_1","name":"zoomies-4vcpu","enabled":true}`))
	}))
	t.Cleanup(srv.Close)

	runCLI(t, "pools", "edit", "pool_1", "--max", "9", "--url", srv.URL)

	if _, ok := sent["tmpfs"]; ok {
		t.Errorf("an edit that names no in-memory folder must not send them: %v", sent)
	}
}

// A size for a folder that is off would be sent as nothing to size, and the pool
// would quietly keep the folder on disk; it is refused as a usage error instead.
func TestPoolsCreateRefusesAFolderSizeWithoutTheFolder(t *testing.T) {
	for flag, want := range map[string]string{
		"--tmpfs-work-size": "--tmpfs-work",
		"--tmpfs-tmp-size":  "--tmpfs-tmp",
	} {
		e, _, errOut := newTestEnv(t)
		code := dispatch(context.Background(), e, []string{"pools", "create", "--name", "p", "--labels", "p",
			flag, "2048", "--url", "http://127.0.0.1:1"})
		if code != exitUsage {
			t.Fatalf("%s: exit code = %d, want %d", flag, code, exitUsage)
		}
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("%s: the refusal must name %s:\n%s", flag, want, errOut.String())
		}
	}
}

// `pools get` says which folders are in memory and how much memory that spends,
// because "yes" alone does not.
func TestPoolsGetShowsWhichFoldersAreInMemory(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/pools/pool_1": `{"id":"pool_1","name":"zoomies-4vcpu","backend":"docker",
			"resources":{},"sizing":"automatic","counts":{},
			"tmpfs":{"work":{"enabled":true,"size_mb":4096},"tmp":{"enabled":true}}}`,
		"/api/v1/pools/pool_2": `{"id":"pool_2","name":"zoomies-plain","backend":"docker",
			"resources":{},"sizing":"automatic","counts":{}}`,
	})

	out, _ := runCLI(t, "pools", "get", "pool_1", "--url", srv.URL)
	for _, want := range []string{"in memory", "_work (4096 MB)", "/tmp (sized from the memory limit)"} {
		if !strings.Contains(out, want) {
			t.Errorf("pools get must show %q:\n%s", want, out)
		}
	}
	out, _ = runCLI(t, "pools", "get", "pool_2", "--url", srv.URL)
	if !strings.Contains(out, "in memory") || strings.Contains(out, "_work (") {
		t.Errorf("a pool with nothing in memory must say no:\n%s", out)
	}
}

// The Docker image store is one more folder in the same object: naming it must
// carry the other two forward from the pool as it stands, as naming either of
// them carries it.
func TestPoolsEditCarriesTheOtherFoldersForwardWhenOnlyTheImageStoreIsNamed(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"id":"pool_1","name":"zoomies-4vcpu","resources":{},"sizing":"automatic",
				"tmpfs":{"work":{"enabled":true,"size_mb":6144},"tmp":{"enabled":false}}}`))
		case http.MethodPatch:
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Errorf("decoding the PATCH body: %v", err)
			}
			_, _ = w.Write([]byte(`{"id":"pool_1","name":"zoomies-4vcpu","enabled":true}`))
		}
	}))
	t.Cleanup(srv.Close)

	runCLI(t, "pools", "edit", "pool_1", "--tmpfs-docker", "--tmpfs-docker-size", "4096", "--url", srv.URL)

	tmpfs, _ := sent["tmpfs"].(map[string]any)
	work, _ := tmpfs["work"].(map[string]any)
	daemon, _ := tmpfs["daemon"].(map[string]any)
	if work["enabled"] != true || work["size_mb"] != 6144.0 {
		t.Errorf("the work folder must be carried forward untouched, got %v", work)
	}
	if daemon["enabled"] != true || daemon["size_mb"] != 4096.0 {
		t.Errorf("the image store must be on at the size typed, got %v", daemon)
	}
}

func TestPoolsCreateRefusesAnImageStoreSizeWithoutTheImageStore(t *testing.T) {
	e, _, errOut := newTestEnv(t)
	code := dispatch(context.Background(), e, []string{"pools", "create", "--name", "p", "--labels", "p",
		"--tmpfs-docker-size", "4096", "--url", "http://127.0.0.1:1"})
	if code != exitUsage || !strings.Contains(errOut.String(), "--tmpfs-docker") {
		t.Fatalf("exit code = %d, want a usage error naming --tmpfs-docker:\n%s", code, errOut.String())
	}
}

func TestPoolsGetShowsTheImageStoreWhenItIsInMemory(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/pools/pool_1": `{"id":"pool_1","name":"zoomies-dind","backend":"docker","docker_mode":"dind",
			"resources":{},"sizing":"automatic","counts":{},
			"tmpfs":{"daemon":{"enabled":true,"size_mb":6144}}}`,
	})
	out, _ := runCLI(t, "pools", "get", "pool_1", "--url", srv.URL)
	if !strings.Contains(out, "Docker image store (6144 MB)") {
		t.Errorf("pools get must show the image store and its size:\n%s", out)
	}
}

// Auto is a property of the folders that are on, said once for them all: an edit
// that only names --tmpfs-auto keeps each folder's size and which are on, marks
// the enabled ones automatic and leaves the disabled ones alone.
func TestPoolsEditTmpfsAutoMarksOnlyTheFoldersThatAreOn(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"id":"pool_1","name":"zoomies-4vcpu","resources":{},"sizing":"automatic",
				"tmpfs":{"work":{"enabled":true,"size_mb":6144},"tmp":{"enabled":false}}}`))
		case http.MethodPatch:
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Errorf("decoding the PATCH body: %v", err)
			}
			_, _ = w.Write([]byte(`{"id":"pool_1","name":"zoomies-4vcpu","enabled":true}`))
		}
	}))
	t.Cleanup(srv.Close)

	runCLI(t, "pools", "edit", "pool_1", "--tmpfs-auto", "--url", srv.URL)

	tmpfs, _ := sent["tmpfs"].(map[string]any)
	work, _ := tmpfs["work"].(map[string]any)
	tmp, _ := tmpfs["tmp"].(map[string]any)
	if work["enabled"] != true || work["auto"] != true || work["size_mb"] != 6144.0 {
		t.Errorf("the work folder should stay on, keep its size and become automatic, got %v", work)
	}
	if tmp["enabled"] != false || tmp["auto"] != nil {
		t.Errorf("a folder that is off must not be marked automatic, got %v", tmp)
	}

	// --tmpfs-auto=false hands the folders back to always-in-memory.
	runCLI(t, "pools", "edit", "pool_1", "--tmpfs-auto=false", "--url", srv.URL)
	tmpfs, _ = sent["tmpfs"].(map[string]any)
	work, _ = tmpfs["work"].(map[string]any)
	if work["auto"] != nil {
		t.Errorf("--tmpfs-auto=false should clear auto, got %v", work)
	}
}
