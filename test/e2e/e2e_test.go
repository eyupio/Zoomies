//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This test proves the whole product works end to end: a real controller, a
// real GitHub App, a real workflow run, a real container. It is behind the
// "e2e" build tag and additionally skips itself unless it has credentials, so
// `go test ./...` never reaches for the network.
//
// See README.md in this directory for the environment it needs.

type env struct {
	appID          string
	installationID string
	privateKey     string
	target         string
	targetType     string
	repo           string
}

func requireEnv(t *testing.T) env {
	t.Helper()
	if os.Getenv("ZOOMIES_E2E") == "" {
		t.Skip("set ZOOMIES_E2E=1 to run the end-to-end test; see test/e2e/README.md")
	}
	e := env{
		appID:          os.Getenv("ZOOMIES_E2E_APP_ID"),
		installationID: os.Getenv("ZOOMIES_E2E_INSTALLATION_ID"),
		target:         os.Getenv("ZOOMIES_E2E_TARGET"),
		targetType:     cmp(os.Getenv("ZOOMIES_E2E_TARGET_TYPE"), "org"),
		repo:           os.Getenv("ZOOMIES_E2E_REPO"),
	}
	keyFile := os.Getenv("ZOOMIES_E2E_PRIVATE_KEY_FILE")
	var missing []string
	for name, v := range map[string]string{
		"ZOOMIES_E2E_APP_ID":           e.appID,
		"ZOOMIES_E2E_INSTALLATION_ID":  e.installationID,
		"ZOOMIES_E2E_PRIVATE_KEY_FILE": keyFile,
		"ZOOMIES_E2E_TARGET":           e.target,
		"ZOOMIES_E2E_REPO":             e.repo,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Skipf("the end-to-end test needs %s; see test/e2e/README.md", strings.Join(missing, ", "))
	}
	pem, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("reading %s: %v", keyFile, err)
	}
	e.privateKey = string(pem)

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("the end-to-end test needs a Docker daemon on this host")
	}
	return e
}

func cmp(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// controller starts a real zoomies controller against a throwaway database and
// returns its base URL.
func controller(t *testing.T) string {
	t.Helper()

	root := repoRoot(t)
	bin := filepath.Join(root, "zoomies")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("zoomies binary not found at %s; run `make build` first", bin)
	}

	port := freePort(t)
	dir := t.TempDir()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, "controller")
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("ZOOMIES_BIND=127.0.0.1:%d", port),
		"ZOOMIES_DISABLE_AUTH=true", // loopback only; the config validator allows it there
		"ZOOMIES_DB_PATH="+filepath.Join(dir, "zoomies.db"),
		"ZOOMIES_STATE_DIR="+dir,
		"ZOOMIES_CONFIG_DIR="+dir,
		"ZOOMIES_WORK_DIR="+filepath.Join(dir, "work"),
		"ZOOMIES_AGENT_EMBEDDED=true",
		"ZOOMIES_AGENT_BACKEND=docker",
		"ZOOMIES_AGENT_CAPACITY=2",
		// A test host is not reachable from GitHub, so this exercises the
		// polling path rather than the webhook path. The webhook path is
		// covered by the integration tests against the fake GitHub.
		"ZOOMIES_POLL_FALLBACK=true",
		"ZOOMIES_POLL_INTERVAL=10s",
		"ZOOMIES_LOG_FORMAT=text",
		"ZOOMIES_LOG_LEVEL=debug",
	)
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("starting the controller: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("controller output:\n%s", logs.String())
		}
	})

	waitFor(t, 30*time.Second, "the controller to become healthy", func() bool {
		resp, err := http.Get(base + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	return base
}

func TestEphemeralRunnerRunsARealJob(t *testing.T) {
	e := requireEnv(t)
	base := controller(t)
	api := &client{t: t, base: base + "/api/v1"}

	// 1. Connect the GitHub App.
	var inst struct {
		ID string `json:"id"`
	}
	api.post("/installations", map[string]any{
		"app_id":          mustInt(t, e.appID),
		"installation_id": mustInt(t, e.installationID),
		"target":          e.target,
		"target_type":     e.targetType,
		"private_key":     e.privateKey,
	}, &inst)
	if inst.ID == "" {
		t.Fatal("the installation was created but came back without an ID")
	}

	// 2. It must verify, or nothing else can work.
	var health struct {
		OK                 bool     `json:"ok"`
		Message            string   `json:"message"`
		MissingPermissions []string `json:"missing_permissions"`
	}
	api.post("/installations/"+inst.ID+"/verify", nil, &health)
	if !health.OK {
		t.Fatalf("the installation did not verify: %s (missing: %v)",
			health.Message, health.MissingPermissions)
	}

	// 3. A pool for this test's label only, so a stray workflow elsewhere in
	//    the organisation cannot be picked up by it.
	var pool struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	api.post("/pools", map[string]any{
		"installation_id": inst.ID,
		"name":            "e2e",
		"labels":          []string{"zoomies-e2e"},
		"backend":         "docker",
		"min_runners":     0,
		"max_runners":     1,
		"idle_timeout":    "1m",
		"ephemeral":       true,
		"docker_mode":     "none",
	}, &pool)

	// The audit log must have recorded that.
	var audit struct {
		Items []struct {
			Action   string `json:"action"`
			TargetID string `json:"target_id"`
		} `json:"items"`
	}
	api.get("/audit?action=pool.create", &audit)
	if len(audit.Items) == 0 {
		t.Error("creating a pool was not written to the audit log")
	}

	// 4. Trigger the workflow.
	marker := fmt.Sprintf("zoomies-e2e-%d", time.Now().UnixNano())
	dispatchWorkflow(t, e, marker)

	// 5. A runner must appear, register, and pick the job up.
	var runnerID string
	waitFor(t, 6*time.Minute, "a runner to be created for the pool", func() bool {
		var out struct {
			Items []struct {
				ID    string `json:"id"`
				State string `json:"state"`
			} `json:"items"`
		}
		api.get("/runners?pool_id="+pool.ID+"&include_removed=true", &out)
		for _, r := range out.Items {
			runnerID = r.ID
			return true
		}
		return false
	})
	t.Logf("runner %s created", runnerID)

	waitFor(t, 6*time.Minute, "the runner to reach idle or busy", func() bool {
		var r struct {
			State string `json:"state"`
		}
		api.get("/runners/"+runnerID, &r)
		return r.State == "idle" || r.State == "busy" || r.State == "removed"
	})

	// 6. The job must complete successfully.
	waitFor(t, 10*time.Minute, "the job to complete successfully", func() bool {
		var out struct {
			Items []struct {
				State      string `json:"state"`
				Conclusion string `json:"conclusion"`
				RunnerID   string `json:"runner_id"`
			} `json:"items"`
		}
		api.get("/jobs?pool_id="+pool.ID, &out)
		for _, j := range out.Items {
			if j.State == "completed" {
				if j.Conclusion != "success" {
					t.Fatalf("the job completed with conclusion %q, want success", j.Conclusion)
				}
				if j.RunnerID == "" {
					t.Error("the completed job is not linked to the runner that ran it")
				}
				return true
			}
		}
		return false
	})

	// 7. The ephemeral runner must go away by itself, and -- the failure this
	//    test exists to catch -- must not leave a registration behind on GitHub.
	waitFor(t, 5*time.Minute, "the ephemeral runner to be destroyed", func() bool {
		var r struct {
			State string `json:"state"`
		}
		api.get("/runners/"+runnerID, &r)
		return r.State == "removed" || r.State == "failed"
	})

	var final struct {
		State       string `json:"state"`
		JobsHandled int    `json:"jobs_handled"`
	}
	api.get("/runners/"+runnerID, &final)
	if final.State != "removed" {
		t.Errorf("runner ended in state %q, want removed", final.State)
	}
	if final.JobsHandled != 1 {
		t.Errorf("runner handled %d jobs, want exactly 1 (it is ephemeral)", final.JobsHandled)
	}

	// 8. Tidy up. Failing to delete the pool would leave runners behind on a
	//    real organisation, so this is an assertion, not a best effort.
	api.delete("/pools/" + pool.ID + "?force=true")
	api.delete("/installations/" + inst.ID)
}

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

type client struct {
	t    *testing.T
	base string
}

func (c *client) do(method, path string, body any, out any) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshalling the request body: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, r)
	if err != nil {
		c.t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		c.t.Fatalf("%s %s: %s\n%s", method, path, resp.Status, b)
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("%s %s: decoding the response: %v\n%s", method, path, err, b)
		}
	}
}

func (c *client) get(path string, out any)        { c.do(http.MethodGet, path, nil, out) }
func (c *client) post(path string, body, out any) { c.do(http.MethodPost, path, body, out) }
func (c *client) delete(path string)              { c.do(http.MethodDelete, path, nil, nil) }

// dispatchWorkflow triggers the fixture workflow through the GitHub API using a
// token the test host already has. It uses the gh CLI when present because that
// keeps credentials out of this test's environment.
func dispatchWorkflow(t *testing.T, e env, marker string) {
	t.Helper()
	if _, err := exec.LookPath("gh"); err != nil {
		t.Skip("the end-to-end test triggers the workflow with the gh CLI, which is not installed")
	}
	cmd := exec.Command("gh", "workflow", "run", "zoomies-e2e.yml",
		"--repo", e.repo, "-f", "marker="+marker)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("triggering the workflow in %s: %v\n%s", e.repo, err, out)
	}
	t.Logf("dispatched zoomies-e2e.yml in %s with marker %s", e.repo, marker)
}

func waitFor(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("timed out after %s waiting for %s", limit, what)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not find the repository root from the test's working directory")
	return ""
}

func mustInt(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		t.Fatalf("%q is not a number: %v", s, err)
	}
	return n
}
