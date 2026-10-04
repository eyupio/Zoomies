package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// hostServer answers a host's GET with the profile it has now and records the
// PATCH it is sent.
func hostServer(t *testing.T, current string, patched *map[string]any, query *string, status int, reply string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(current))
		case http.MethodPatch:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			*patched, *query = body, r.URL.RawQuery
			w.WriteHeader(status)
			_, _ = w.Write([]byte(reply))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
}

const profiledHost = `{"id":"hst_a","name":"big","capacity":8,"slots":3,"slots_limited_by":"cpu",
	"runner_profile":{"minimum":{"cpus":1},"standard":{"cpus":3,"memory_mb":8192,"burst_max_cpus":6}},
	"effective_profile":{"standard":{"cpus":3,"memory_mb":8192,"cpus_source":"host","memory_mb_source":"host"}}}`

// The API replaces a runner profile whole, so naming one figure has to carry
// the rest of the profile forward: `--standard-cpus 4` must not clear the memory
// and the ceiling the host already had.
func TestEditingOneRunnerSizeKeepsTheRestOfTheProfile(t *testing.T) {
	var patched map[string]any
	var query string
	srv := hostServer(t, profiledHost, &patched, &query, http.StatusOK, profiledHost)
	defer srv.Close()

	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"hosts", "edit", "hst_a", "--standard-cpus", "4", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	profile, _ := patched["runner_profile"].(map[string]any)
	standard, _ := profile["standard"].(map[string]any)
	minimum, _ := profile["minimum"].(map[string]any)
	if standard["cpus"] != 4.0 || standard["memory_mb"] != 8192.0 || standard["burst_max_cpus"] != 6.0 || minimum["cpus"] != 1.0 {
		t.Fatalf("the PATCH did not carry the unnamed figures forward: %v", patched)
	}
	if _, sent := patched["capacity"]; sent {
		t.Errorf("a capacity nobody named was sent: %v", patched)
	}
	if got := out.String() + errOut.String(); !strings.Contains(got, "Updated host big") || !strings.Contains(got, "3 slots") {
		t.Errorf("the summary does not say what happened:\n%s", got)
	}
}

// Zero is a figure the operator can name: it hands that field back to the fleet,
// which is how a ceiling is removed without clearing the standard beside it.
func TestZeroHandsAProfileFieldBackToTheFleet(t *testing.T) {
	var patched map[string]any
	var query string
	srv := hostServer(t, profiledHost, &patched, &query, http.StatusOK, profiledHost)
	defer srv.Close()

	e, _, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"hosts", "edit", "hst_a", "--burst-max-cpus", "0", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	standard := patched["runner_profile"].(map[string]any)["standard"].(map[string]any)
	if _, kept := standard["burst_max_cpus"]; kept {
		t.Errorf("the ceiling was sent as %v, want it left out so the fleet decides", standard["burst_max_cpus"])
	}
	if standard["cpus"] != 3.0 {
		t.Errorf("clearing the ceiling changed the standard: %v", standard)
	}
}

// A capacity or a reserve is independent of the profile, and costs no read of
// the host: nothing in the request depends on what it has.
func TestEditingACapacityDoesNotReadOrSendTheProfile(t *testing.T) {
	var patched map[string]any
	var query string
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			reads++
		}
		if r.Method == http.MethodPatch {
			_ = json.NewDecoder(r.Body).Decode(&patched)
			query = r.URL.RawQuery
		}
		_, _ = w.Write([]byte(`{"id":"hst_a","name":"big","capacity":6}`))
	}))
	defer srv.Close()

	e, _, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"hosts", "edit", "hst_a", "--capacity", "6", "--reserve-memory-mb", "4096", "--confirm", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	if reads != 0 {
		t.Errorf("the host was read %d times for an edit that does not touch its profile", reads)
	}
	if _, sent := patched["runner_profile"]; sent {
		t.Errorf("the profile was sent with an edit that did not name it: %v", patched)
	}
	if patched["capacity"] != 6.0 || patched["reserve_memory_mb"] != 4096.0 {
		t.Errorf("the named figures were not sent: %v", patched)
	}
	if query != "confirm=true" {
		t.Errorf("--confirm sent %q, want confirm=true", query)
	}
}

func TestClearingTheProfileSendsAnEmptyOne(t *testing.T) {
	var patched map[string]any
	var query string
	srv := hostServer(t, profiledHost, &patched, &query, http.StatusOK, `{"id":"hst_a","name":"big","capacity":8}`)
	defer srv.Close()

	e, _, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"hosts", "edit", "hst_a", "--clear-profile", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	profile, present := patched["runner_profile"].(map[string]any)
	if !present || len(profile) != 0 {
		t.Fatalf("the PATCH sent %v, want an empty runner_profile that clears it", patched["runner_profile"])
	}
}

func TestAHostEditThatNamesNothingOrContradictsItselfIsAUsageError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a request was made for an edit that cannot be sent: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"nothing named", []string{"hosts", "edit", "hst_a"}, "nothing to change"},
		{"clearing and setting", []string{"hosts", "edit", "hst_a", "--clear-profile", "--standard-cpus", "2"}, "cannot be combined with --standard-cpus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _, errOut := newTestEnv(t)
			if code := dispatch(context.Background(), e, append(tc.args, "--url", srv.URL)); code != exitUsage {
				t.Fatalf("exit code = %d, want the usage code\n%s", code, errOut)
			}
			if !strings.Contains(errOut.String(), tc.want) {
				t.Errorf("the error does not say %q:\n%s", tc.want, errOut)
			}
		})
	}
}

// The API's refusal is the answer, in the words it wrote, and the exit says it
// failed: an edit that would strand a pool must not read as saved.
func TestARefusedHostEditShowsTheRefusal(t *testing.T) {
	var patched map[string]any
	var query string
	srv := hostServer(t, profiledHost, &patched, &query, http.StatusConflict,
		`{"error":{"code":"conflict","message":"saving big as described would leave pool small with nowhere to run: its minimum runner size is 3 CPU, above the 2 CPU this pool gives each runner"}}`)
	defer srv.Close()

	e, _, errOut := newTestEnv(t)
	code := dispatch(context.Background(), e, []string{"hosts", "edit", "hst_a", "--min-cpus", "3", "--url", srv.URL})
	if code == exitOK {
		t.Fatal("a refused edit exited zero")
	}
	if !strings.Contains(errOut.String(), "would leave pool small with nowhere to run") {
		t.Errorf("the refusal is not shown:\n%s", errOut)
	}
}

// The in-memory policy is part of the profile the API replaces whole, so naming
// it has to carry the sizes forward, and naming a size has to carry the policy.
func TestTheInMemoryPolicyIsEditedWithoutLosingTheRestOfTheProfile(t *testing.T) {
	const withPolicy = `{"id":"hst_a","name":"big","capacity":8,"slots":3,
		"runner_profile":{"standard":{"cpus":3,"memory_mb":8192},"tmpfs":{"max_mb":2048}},
		"effective_profile":{"standard":{"cpus":3,"memory_mb":8192,"cpus_source":"host","memory_mb_source":"host"}}}`
	cases := []struct {
		name         string
		args         []string
		wantDisabled bool
		wantMax      float64
	}{
		{"a ceiling alone keeps the sizes", []string{"--tmpfs-max-mb", "1024"}, false, 1024},
		{"off keeps the sizes", []string{"--tmpfs-off"}, true, 2048},
		{"a size keeps the policy", []string{"--standard-cpus", "4"}, false, 2048},
		{"a zero ceiling hands it back to the pools", []string{"--tmpfs-max-mb", "0"}, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var patched map[string]any
			var query string
			srv := hostServer(t, withPolicy, &patched, &query, http.StatusOK, withPolicy)
			defer srv.Close()

			e, _, errOut := newTestEnv(t)
			args := append([]string{"hosts", "edit", "hst_a"}, tc.args...)
			args = append(args, "--url", srv.URL)
			if code := dispatch(context.Background(), e, args); code != exitOK {
				t.Fatalf("exit code = %d\n%s", code, errOut)
			}
			profile, _ := patched["runner_profile"].(map[string]any)
			tmpfs, _ := profile["tmpfs"].(map[string]any)
			standard, _ := profile["standard"].(map[string]any)
			disabled, _ := tmpfs["disabled"].(bool)
			max, _ := tmpfs["max_mb"].(float64)
			if disabled != tc.wantDisabled || max != tc.wantMax {
				t.Errorf("tmpfs = %v, want disabled %v max %v", tmpfs, tc.wantDisabled, tc.wantMax)
			}
			if standard["memory_mb"] != 8192.0 {
				t.Errorf("the sizes were not carried forward: %v", profile)
			}
		})
	}
}

// Clearing the profile clears the policy with it, so naming both is a
// contradiction the CLI refuses before sending anything.
func TestClearingTheProfileCannotBeCombinedWithTheInMemoryPolicy(t *testing.T) {
	e, _, errOut := newTestEnv(t)
	code := dispatch(context.Background(), e, []string{"hosts", "edit", "hst_a", "--clear-profile", "--tmpfs-off", "--url", "http://127.0.0.1:1"})
	if code != exitUsage || !strings.Contains(errOut.String(), "--tmpfs-off") {
		t.Fatalf("exit code = %d, want a usage error naming --tmpfs-off:\n%s", code, errOut)
	}
}
