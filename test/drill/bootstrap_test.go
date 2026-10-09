//go:build drill

package drill

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/github"
)

// The unattended drill: a controller comes up from its environment alone, with
// authentication on, and a provisioner drives it to a joined agent without a
// browser and without reading a line of its log.
//
// This is what a compose file or a Terraform module has to be able to do:
// nobody is watching, so the setup token printed to the log is a step nobody
// takes. The provisioner writes a token it generated into a 0600 file, starts
// the controller with ZOOMIES_BOOTSTRAP_ADMIN and ZOOMIES_BOOTSTRAP_TOKEN_FILE,
// waits on /readyz for bootstrap_required to go false, and then uses the token
// it already holds -- as a platform account -- to mint a join token and enrol
// an agent. It runs the real binary as the two processes a compose file would
// start; it is not a `docker compose up`.
func TestAnUnattendedControllerIsUsableWithNoHumanStep(t *testing.T) {
	requireBinary(t)
	rec := newRecord(t, "unattended-bootstrap")
	defer rec.write()

	gh := github.NewFake()
	t.Cleanup(gh.Close)
	f := &fleet{t: t, gh: gh, port: freePort(t), stateDir: t.TempDir()}
	f.baseURL = fmt.Sprintf("http://127.0.0.1:%d", f.port)

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("generating the provisioner's token: %v", err)
	}
	token := hex.EncodeToString(secret)
	tokenFile := filepath.Join(t.TempDir(), "bootstrap-token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatalf("writing the token file: %v", err)
	}

	f.controller = f.spawn(builtBinary(), "controller", []string{"controller"}, append(baseEnv(f.stateDir),
		fmt.Sprintf("ZOOMIES_BIND=127.0.0.1:%d", f.port),
		"ZOOMIES_DB_PATH="+filepath.Join(f.stateDir, "zoomies.db"),
		"ZOOMIES_GITHUB_API_BASE_URL="+gh.URL(),
		"ZOOMIES_AGENT_EMBEDDED=false",
		"ZOOMIES_BOOTSTRAP_ADMIN=provisioner",
		"ZOOMIES_BOOTSTRAP_TOKEN_FILE="+tokenFile,
	))

	// One endpoint answers "can I use this yet", and it is the only thing the
	// provisioner watches.
	waitFor(t, waitProcessUp, "/readyz to report the instance usable", func() bool {
		resp, err := http.Get(f.baseURL + "/readyz")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var body struct {
			OK                bool  `json:"ok"`
			BootstrapRequired *bool `json:"bootstrap_required"`
		}
		if json.NewDecoder(resp.Body).Decode(&body) != nil {
			return false
		}
		return body.OK && body.BootstrapRequired != nil && !*body.BootstrapRequired
	})
	rec.note("ready", "/readyz reported bootstrap_required false")

	// Without the token the API is closed: authentication is on, which is
	// what makes the rest of this worth anything.
	resp, err := http.Get(f.baseURL + "/api/v1/hosts")
	if err != nil {
		t.Fatalf("GET /hosts: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an anonymous GET /hosts answered %s; the instance must not be open", resp.Status)
	}

	f.api = &apiClient{t: t, base: f.baseURL + "/api/v1", token: token}
	var me struct {
		Role string `json:"role"`
	}
	f.api.get("/auth/session", &me)
	if me.Role != "platform" {
		t.Fatalf("the provisioner's token is %q; want platform", me.Role)
	}

	// The platform account mints a join token through the API, and an agent
	// joins with it.
	var join struct {
		Token string `json:"token"`
	}
	f.api.post("/join-tokens", map[string]any{"capacity": 2}, &join)
	if join.Token == "" {
		t.Fatal("the join token came back empty")
	}
	agentDir := t.TempDir()
	work := filepath.Join(agentDir, "work")
	if err := os.MkdirAll(work, 0o750); err != nil {
		t.Fatalf("creating the agent work directory: %v", err)
	}
	f.agent = f.spawn(builtBinary(), "agent", []string{"agent"}, append(baseEnv(agentDir),
		"ZOOMIES_CONTROLLER_URL="+f.baseURL,
		"ZOOMIES_JOIN_TOKEN="+join.Token,
		"ZOOMIES_AGENT_BACKEND=process",
		"ZOOMIES_AGENT_CAPACITY=2",
		"ZOOMIES_WORK_DIR="+work,
		"ZOOMIES_AGENT_ALLOW_INSECURE_HTTP=true",
		"ZOOMIES_AGENT_RUNNER_DOWNLOAD_URL=http://127.0.0.1:1/never",
	))

	waitFor(t, waitProcessUp, "the agent to join and report healthy", func() bool {
		var out struct {
			Items []struct {
				Healthy bool `json:"healthy"`
			} `json:"items"`
		}
		f.api.get("/hosts", &out)
		return len(out.Items) == 1 && out.Items[0].Healthy
	})
	rec.note("joined", "one agent joined with a join token the bootstrap account minted, and is healthy")

	// Who bootstrapped and how is in the audit log, not only in a log line.
	var audit struct {
		Items []struct {
			Action    string `json:"action"`
			ActorKind string `json:"actor_kind"`
			After     any    `json:"after"`
		} `json:"items"`
	}
	f.api.get("/audit?action=auth.bootstrap", &audit)
	if len(audit.Items) != 1 || audit.Items[0].ActorKind != "system" {
		t.Fatalf("the audit log has %+v; want one auth.bootstrap row by the system", audit.Items)
	}
	if after, _ := json.Marshal(audit.Items[0].After); !strings.Contains(string(after), "environment_token") {
		t.Fatalf("the auth.bootstrap row says %s; want the environment token method", after)
	}

	// The negative half, and the only look at the output: an instance that
	// bootstrapped itself has no setup token to print, and a secret it never
	// minted is never written out.
	out := f.controller.output()
	if strings.Contains(out, "setup token") {
		t.Error("the controller printed a setup token although the environment had already created the first account")
	}
	if strings.Contains(out, token) {
		t.Error("the controller wrote the provisioner's token to its output")
	}

	rec.pass("a controller started with ZOOMIES_BOOTSTRAP_ADMIN and ZOOMIES_BOOTSTRAP_TOKEN_FILE reports bootstrap_required false on /readyz, " +
		"the provisioner's own token is a platform identity that mints a join token, and the agent it enrols is healthy -- " +
		"with no browser, no setup token and no log read")
}
