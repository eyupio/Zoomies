package installer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

func newTestCallbackServer(t *testing.T) (*callbackServer, *httptest.Server) {
	t.Helper()
	c := &callbackServer{
		state:     "state-from-this-session",
		results:   make(chan callbackResult, 4),
		stateErrs: make(chan error, 4),
	}
	srv := httptest.NewServer(c.Handler())
	t.Cleanup(srv.Close)
	return c, srv
}

func TestCallbackServerAcceptsAGoodCode(t *testing.T) {
	c, srv := newTestCallbackServer(t)

	resp, err := srv.Client().Get(srv.URL + "/callback?code=abc123&state=" + c.State())
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "App created") {
		t.Errorf("the operator should be told to go back to the terminal:\n%s", body)
	}

	res, err := c.WaitFor(context.Background(), 2*time.Second, nil, nil)
	if err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	if res.Code != "abc123" {
		t.Fatalf("code = %q, want abc123", res.Code)
	}
}

func TestCallbackServerAcceptsAnInstallationID(t *testing.T) {
	c, srv := newTestCallbackServer(t)

	// GitHub's post-installation redirect carries no state of its own.
	resp, err := srv.Client().Get(srv.URL + "/callback?installation_id=987654")
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	res, err := c.WaitFor(context.Background(), 2*time.Second, nil, nil)
	if err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	if res.InstallationID != 987654 {
		t.Fatalf("installation id = %d", res.InstallationID)
	}
}

func TestCallbackServerRejectsTheWrongState(t *testing.T) {
	c, srv := newTestCallbackServer(t)

	resp, err := srv.Client().Get(srv.URL + "/callback?code=abc123&state=somebody-elses")
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}

	_, err = c.WaitFor(context.Background(), 2*time.Second, nil, nil)
	if err == nil {
		t.Fatal("a mismatched state must be reported, not silently ignored")
	}
	if !strings.Contains(err.Error(), "wrong state") {
		t.Fatalf("the error should say what happened, got: %v", err)
	}
	if len(c.results) != 0 {
		t.Fatal("a rejected callback must not deliver a code")
	}
}

func TestCallbackServerRejectsAnEmptyCallback(t *testing.T) {
	_, srv := newTestCallbackServer(t)
	resp, err := srv.Client().Get(srv.URL + "/callback")
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestCallbackServerTimesOutWithSomethingToDoNext(t *testing.T) {
	c, _ := newTestCallbackServer(t)

	start := time.Now()
	_, err := c.WaitFor(context.Background(), 30*time.Millisecond, nil, nil)
	if err == nil {
		t.Fatal("want a timeout")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("the wait should end near its deadline, took %s", time.Since(start))
	}
	if !strings.Contains(err.Error(), "paste") {
		t.Fatalf("the timeout must offer the paste fallback, got: %v", err)
	}
}

func TestCallbackServerTakesAPastedValue(t *testing.T) {
	c, _ := newTestCallbackServer(t)
	paste := make(chan string, 1)
	paste <- "https://zoomies.example.com/callback?code=pasted-code&state=whatever"

	res, err := c.WaitFor(context.Background(), 2*time.Second, paste, nil)
	if err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	if res.Code != "pasted-code" {
		t.Fatalf("code = %q", res.Code)
	}
}

func TestCallbackServerStopsWithTheContext(t *testing.T) {
	c, _ := newTestCallbackServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.WaitFor(ctx, time.Minute, nil, nil); err == nil {
		t.Fatal("a cancelled context must end the wait")
	}
}

func TestCallbackStartPageAutoPostsTheManifest(t *testing.T) {
	c, srv := newTestCallbackServer(t)
	c.Configure([]byte(`{"name":"zoomies-acme"}`), "https://github.com/organizations/acme/settings/apps/new")

	resp, err := srv.Client().Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	page := string(body)

	if !strings.Contains(page, `action="https://github.com/organizations/acme/settings/apps/new?state=`) {
		t.Errorf("the form must post to GitHub with the state:\n%s", page)
	}
	if !strings.Contains(page, `name="manifest"`) {
		t.Errorf("the manifest must be in the form:\n%s", page)
	}
	if !strings.Contains(page, "zoomies-acme") {
		t.Errorf("the manifest body should be there:\n%s", page)
	}
	// A browser with JavaScript off must still be able to continue.
	if !strings.Contains(page, "<button type=\"submit\">") {
		t.Errorf("the form needs a visible button:\n%s", page)
	}
}

func TestParsePasted(t *testing.T) {
	cases := []struct {
		in       string
		wantCode string
		wantID   int64
		wantOK   bool
	}{
		{"abc123", "abc123", 0, true},
		{"  abc123  ", "abc123", 0, true},
		{"http://127.0.0.1:1234/callback?code=xyz&state=s", "xyz", 0, true},
		{"http://127.0.0.1:1234/callback?installation_id=42", "", 42, true},
		{"42", "", 42, true},
		{"", "", 0, false},
	}
	for _, tc := range cases {
		got, ok := parsePasted(tc.in)
		if ok != tc.wantOK {
			t.Errorf("parsePasted(%q) ok = %v, want %v", tc.in, ok, tc.wantOK)
			continue
		}
		if got.Code != tc.wantCode || got.InstallationID != tc.wantID {
			t.Errorf("parsePasted(%q) = %+v, want code %q id %d", tc.in, got, tc.wantCode, tc.wantID)
		}
	}
}

func TestLineReader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lines := lineReader(ctx, strings.NewReader("first\nsecond\n"))
	if got := <-lines; got != "first" {
		t.Fatalf("first line = %q", got)
	}
	if got := <-lines; got != "second" {
		t.Fatalf("second line = %q", got)
	}
}

func TestStoreInstallationSealsAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Path: ":memory:"})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	key, err := cryptox.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	const pem = "-----BEGIN RSA PRIVATE KEY-----\nsecret\n-----END RSA PRIVATE KEY-----"
	in := installationInput{
		AppID: 1, InstallationID: 2, Target: "acme", TargetType: store.TargetOrg,
		PrivateKeyPEM: pem, WebhookSecret: "hunter2hunter2",
	}

	inst, err := storeInstallation(ctx, st, key, in)
	if err != nil {
		t.Fatalf("storeInstallation: %v", err)
	}
	if inst.APIBaseURL != "https://api.github.com" {
		t.Fatalf("api base url = %q", inst.APIBaseURL)
	}
	if strings.Contains(string(inst.PrivateKeyEnc), "secret") {
		t.Fatal("the private key must be sealed, not stored in the clear")
	}
	if got, err := key.OpenString(inst.PrivateKeyEnc); err != nil || got != pem {
		t.Fatalf("unsealed key = %q, %v", got, err)
	}
	if got, err := key.OpenString(inst.WebhookSecretEnc); err != nil || got != "hunter2hunter2" {
		t.Fatalf("unsealed webhook secret = %q, %v", got, err)
	}

	// Re-running setup for the same target updates the row rather than
	// creating a second installation that the controller would then have to
	// choose between.
	in.InstallationID = 3
	again, err := storeInstallation(ctx, st, key, in)
	if err != nil {
		t.Fatalf("storeInstallation again: %v", err)
	}
	if again.ID != inst.ID {
		t.Fatalf("a second run created a new row %s, want %s updated", again.ID, inst.ID)
	}
	all, err := st.ListInstallations(ctx)
	if err != nil {
		t.Fatalf("ListInstallations: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("want exactly one installation, got %d", len(all))
	}
	if all[0].InstallationID != 3 {
		t.Fatalf("installation id = %d, want the updated 3", all[0].InstallationID)
	}
}

func TestStoreInstallationNeedsAKey(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Path: ":memory:"})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	if _, err := storeInstallation(ctx, st, nil, installationInput{Target: "acme"}); err == nil {
		t.Fatal("without a key the private key cannot be sealed, and that must be an error")
	}
}

// The manifest the installer sends is the security boundary of the whole
// integration, so it is checked here rather than trusted.
func TestManifestAsksForTheLeastItCan(t *testing.T) {
	raw, err := github.Manifest(github.ManifestOptions{
		Name:         "zoomies-acme",
		URL:          "https://zoomies.example.com",
		WebhookURL:   "https://zoomies.example.com/webhooks/github",
		Organization: "acme",
		SetupURL:     "http://127.0.0.1:4321/callback",
	})
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	var m struct {
		DefaultEvents      []string          `json:"default_events"`
		DefaultPermissions map[string]string `json:"default_permissions"`
		Public             bool              `json:"public"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(m.DefaultEvents) != 1 || m.DefaultEvents[0] != "workflow_job" {
		t.Errorf("events = %v, want only workflow_job", m.DefaultEvents)
	}
	if m.Public {
		t.Error("a fleet controller's App has no business being installable by strangers")
	}
	if m.DefaultPermissions["organization_self_hosted_runners"] != "write" {
		t.Errorf("an org App needs the runner permission: %v", m.DefaultPermissions)
	}
}
