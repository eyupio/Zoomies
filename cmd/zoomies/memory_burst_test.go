package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The memory valve's policy is one object, like the CPU one: an edit that types
// only the swap has to carry the mode and the ceiling forward, or "allow a little
// swap" would quietly switch the valve off.
func TestPoolsEditCarriesTheMemoryValveForward(t *testing.T) {
	var sent map[string]any
	srv := patchServer(t, `{"id":"pool_1","name":"zoomies-p","resources":{"cpus":2,"memory_mb":4096},"sizing":"fixed",
		"memory_burst":{"mode":"automatic","max_memory_mb":6144,"spill_mb":1024}}`, &sent)

	runCLI(t, "pools", "edit", "pool_1", "--memory-burst-spill", "2048", "--url", srv.URL)

	burst, _ := sent["memory_burst"].(map[string]any)
	if burst["mode"] != "automatic" || burst["max_memory_mb"] != 6144.0 || burst["spill_mb"] != 2048.0 {
		t.Errorf("the PATCH must keep the mode and the ceiling and set the swap, got %v", sent["memory_burst"])
	}
	if _, ok := sent["resources"]; ok {
		t.Errorf("an edit that touches no part of the size must not send one: %v", sent)
	}
	if _, ok := sent["cpu_burst"]; ok {
		t.Errorf("an edit that touches no part of the CPU policy must not send one: %v", sent)
	}
}

// Naming the mode changes the mode and leaves what was typed beside it.
func TestPoolsEditCanTurnTheMemoryValveOffAndOn(t *testing.T) {
	var sent map[string]any
	srv := patchServer(t, `{"id":"pool_1","name":"zoomies-p","memory_burst":{"mode":"automatic","max_memory_mb":6144}}`, &sent)

	runCLI(t, "pools", "edit", "pool_1", "--memory-burst", "observe", "--url", srv.URL)

	burst, _ := sent["memory_burst"].(map[string]any)
	if burst["mode"] != "observe" || burst["max_memory_mb"] != 6144.0 {
		t.Errorf("moving to observe must keep the ceiling, got %v", sent["memory_burst"])
	}
}

// Swap is the valve's last resort and the API refuses it while the valve is off,
// so turning the valve off has to let go of the allowance with it: asking an
// operator for a second flag to say what the first implies is a 422 for nothing.
func TestPoolsEditTurnsTheMemoryValveOffWithoutNamingTheSwapItHad(t *testing.T) {
	const pool = `{"id":"pool_1","name":"zoomies-p","memory_burst":{"mode":"automatic","max_memory_mb":6144,"spill_mb":2048}}`

	var sent map[string]any
	srv := patchServer(t, pool, &sent)
	runCLI(t, "pools", "edit", "pool_1", "--memory-burst", "off", "--url", srv.URL)
	burst, _ := sent["memory_burst"].(map[string]any)
	if burst["mode"] != "off" || burst["spill_mb"] != 0.0 || burst["max_memory_mb"] != 6144.0 {
		t.Errorf("turning the valve off must drop the swap and keep the ceiling, got %v", sent["memory_burst"])
	}

	// Moving between the two modes that use it keeps it.
	sent = nil
	srv = patchServer(t, pool, &sent)
	runCLI(t, "pools", "edit", "pool_1", "--memory-burst", "observe", "--url", srv.URL)
	burst, _ = sent["memory_burst"].(map[string]any)
	if burst["mode"] != "observe" || burst["spill_mb"] != 2048.0 {
		t.Errorf("moving to observe must keep the swap, got %v", sent["memory_burst"])
	}
}

// A ceiling or an allowance of swap typed on a create without a mode would be
// sent with an empty mode, which the API reads as off: the figures would bind
// nothing and the pool would miss the observe default it would otherwise get.
func TestPoolsCreateRefusesAMemoryCeilingWithoutAMode(t *testing.T) {
	for _, flag := range []string{"--memory-burst-max", "--memory-burst-spill"} {
		t.Run(flag, func(t *testing.T) {
			e, _, errOut := newTestEnv(t)
			code := dispatch(context.Background(), e, []string{"pools", "create", "--name", "p", "--labels", "p",
				flag, "4096", "--url", "http://127.0.0.1:1"})
			if code != exitUsage {
				t.Fatalf("exit code = %d, want %d: a figure without a mode must be refused as a usage error", code, exitUsage)
			}
			if !strings.Contains(errOut.String(), "--memory-burst") {
				t.Errorf("the refusal must name the flag to add:\n%s", errOut.String())
			}
		})
	}
}

// A create that names a mode sends the whole policy.
func TestPoolsCreateSendsTheMemoryValvePolicy(t *testing.T) {
	var sent map[string]any
	srv := patchServer(t, `{}`, &sent)

	runCLI(t, "pools", "create", "--name", "p", "--labels", "p", "--installation", "ins_1",
		"--memory-burst", "automatic", "--memory-burst-max", "12288", "--memory-burst-spill", "2048", "--url", srv.URL)

	burst, _ := sent["memory_burst"].(map[string]any)
	if burst["mode"] != "automatic" || burst["max_memory_mb"] != 12288.0 || burst["spill_mb"] != 2048.0 {
		t.Errorf("the create must carry the policy it was given, got %v", sent["memory_burst"])
	}
}

// `pools get` says what the runner page says: the mode, what a runner may reach,
// and whether swap is the last resort.
func TestPoolsGetShowsTheMemoryValve(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy string
		want   string
	}{
		{"off", `{"mode":"off"}`, "off"},
		{"a pool made before the valve", ``, "off"},
		{"observing, with the default ceiling", `{"mode":"observe"}`, "observe only, up to half as much again as a runner starts with"},
		{"lending, with a ceiling and swap", `{"mode":"automatic","max_memory_mb":6144,"spill_mb":2048}`,
			"automatic, up to 6 GB per runner, with up to 2 GB of swap as the last resort"},
		{"lending, with a ceiling that is not a whole gigabyte", `{"mode":"automatic","max_memory_mb":6656}`, "automatic, up to 6.5 GB per runner"},
		{"lending, with an awkward ceiling", `{"mode":"automatic","max_memory_mb":6100}`, "automatic, up to 6100 MB per runner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := ""
			if tc.policy != "" {
				policy = `"memory_burst":` + tc.policy + ","
			}
			srv := jsonRoutes(t, map[string]string{
				"/api/v1/pools/pool_1": `{"id":"pool_1","name":"zoomies-p","backend":"docker","resources":{},"sizing":"automatic",` +
					policy + `"counts":{}}`,
			})

			out, _ := runCLI(t, "pools", "get", "pool_1", "--url", srv.URL)

			var line string
			for _, l := range strings.Split(out, "\n") {
				if strings.Contains(l, "elastic memory") {
					line = l
				}
			}
			if !strings.Contains(line, tc.want) {
				t.Errorf("the elastic memory row is %q, want it to say %q:\n%s", line, tc.want, out)
			}
		})
	}
}

// The host's memory ceiling goes with the rest of its runner profile: naming it
// alone must not clear the sizes beside it.
func TestEditingAHostsMemoryCeilingKeepsTheRestOfTheProfile(t *testing.T) {
	var patched map[string]any
	var query string
	srv := hostServer(t, profiledHost, &patched, &query, http.StatusOK, profiledHost)
	defer srv.Close()

	e, _, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"hosts", "edit", "hst_a", "--burst-max-memory-mb", "16384", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	standard := patched["runner_profile"].(map[string]any)["standard"].(map[string]any)
	if standard["burst_max_memory_mb"] != 16384.0 {
		t.Errorf("the ceiling was not sent: %v", standard)
	}
	if standard["cpus"] != 3.0 || standard["memory_mb"] != 8192.0 || standard["burst_max_cpus"] != 6.0 {
		t.Errorf("naming the memory ceiling changed what the host already had: %v", standard)
	}
}

// Zero hands the ceiling back to the pool, which is how an operator removes a
// host's cap without touching its sizes.
func TestZeroHandsAHostsMemoryCeilingBackToThePool(t *testing.T) {
	var patched map[string]any
	var query string
	const capped = `{"id":"hst_a","name":"big","capacity":8,"slots":3,
		"runner_profile":{"standard":{"cpus":3,"memory_mb":8192,"burst_max_memory_mb":16384}}}`
	srv := hostServer(t, capped, &patched, &query, http.StatusOK, capped)
	defer srv.Close()

	e, _, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"hosts", "edit", "hst_a", "--burst-max-memory-mb", "0", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	standard := patched["runner_profile"].(map[string]any)["standard"].(map[string]any)
	if _, kept := standard["burst_max_memory_mb"]; kept {
		t.Errorf("the ceiling was sent as %v, want it left out so the pool decides", standard["burst_max_memory_mb"])
	}
	if standard["memory_mb"] != 8192.0 {
		t.Errorf("clearing the ceiling changed the standard: %v", standard)
	}
}
