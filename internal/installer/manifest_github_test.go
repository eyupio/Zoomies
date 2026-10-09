package installer

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

// appKey is generated once: 2048-bit RSA generation is the slowest thing these
// tests do, and every one of them needs a key GitHub's client will accept.
var appKey = sync.OnceValue(func() string {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))
})

func newManifestStore(t *testing.T) (*store.Store, *cryptox.Key) {
	t.Helper()
	st, err := store.Open(t.Context(), store.Options{Path: ":memory:"})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	key, err := cryptox.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return st, key
}

// newUnattendedInstaller is an installer with no terminal, which is the only
// shape the unattended GitHub paths can be exercised in.
func newUnattendedInstaller(t *testing.T, answers *Answers) (*Installer, *strings.Builder) {
	t.Helper()
	out := &strings.Builder{}
	i := &Installer{
		out:         out,
		in:          strings.NewReader(""),
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		ui:          newUI(out),
		answers:     answers,
		interactive: false,
	}
	return i, out
}

// Skipping GitHub is a supported way to finish setup, but it leaves a fleet
// that cannot create a runner -- so the step has to say so rather than pass
// silently.
func TestStepGitHubAppSaysWhatSkippingCosts(t *testing.T) {
	st, key := newManifestStore(t)
	i, out := newUnattendedInstaller(t, nil)

	p := &Plan{GitHub: GitHubPlan{Skip: true}}
	if err := i.stepGitHubApp(t.Context(), st, key, p); err != nil {
		t.Fatalf("stepGitHubApp: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, "Installations page") || !strings.Contains(body, "no runners") {
		t.Fatalf("skipping must say what it costs:\n%s", body)
	}
	if all, err := st.ListInstallations(t.Context()); err != nil || len(all) != 0 {
		t.Fatalf("a skipped step recorded an installation: %v, %v", all, err)
	}
}

// Creating an App is a browser handshake with a human in it, so an unattended
// run with no answers cannot do it -- and the refusal has to name the way out.
func TestStepGitHubAppRefusesToInventAnAppUnattended(t *testing.T) {
	st, key := newManifestStore(t)
	i, _ := newUnattendedInstaller(t, nil)

	err := i.stepGitHubApp(t.Context(), st, key, &Plan{})
	if err == nil {
		t.Fatal("an unattended run created a GitHub App out of nothing")
	}
	for _, want := range []string{"github.app_id", "github.private_key_file", "github.skip"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q:\n%v", want, err)
		}
	}
}

// An answer file that names half the credentials is a typo, not a decision, so
// it is reported as the missing answers rather than silently recording a
// half-built installation.
func TestAppFromAnswersReportsTheAnswersItIsMissing(t *testing.T) {
	st, key := newManifestStore(t)
	i, _ := newUnattendedInstaller(t, &Answers{Mode: "single"})

	p := &Plan{Mode: ModeSingle, GitHub: GitHubPlan{AppID: 1, Target: "acme", TargetType: store.TargetOrg}}
	err := i.appFromAnswers(t.Context(), st, key, p)
	if err == nil {
		t.Fatal("an installation was recorded from half a set of credentials")
	}
	if !strings.Contains(err.Error(), "cannot continue without these answers") {
		t.Fatalf("error = %v, want the missing-answers report", err)
	}
}

// A private key file that is not a key is the commonest setup mistake -- an
// operator pointing at the wrong download -- and it has to be caught here
// rather than at the first API call weeks later.
func TestAppFromAnswersRejectsAFileThatIsNotAKey(t *testing.T) {
	st, key := newManifestStore(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "not-a-key.pem")
	if err := os.WriteFile(path, []byte("just some text\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	i, _ := newUnattendedInstaller(t, &Answers{GitHub: AnswersApp{PrivateKeyFile: path}})

	err := i.appFromAnswers(t.Context(), st, key, &Plan{})
	if err == nil || !strings.Contains(err.Error(), "does not look like a PEM private key") {
		t.Fatalf("error = %v, want a complaint about the key file", err)
	}
}

// The whole point of the unattended path is that credentials which already
// exist get sealed and recorded, and then proved against GitHub before setup
// claims success.
func TestAppFromAnswersSealsTheCredentialsAndVerifiesThem(t *testing.T) {
	st, key := newManifestStore(t)
	fake := github.NewFake()
	t.Cleanup(fake.Close)

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "app.pem")
	if err := os.WriteFile(keyPath, []byte(appKey()), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	i, out := newUnattendedInstaller(t, &Answers{GitHub: AnswersApp{
		PrivateKeyFile: keyPath,
		WebhookSecret:  "hunter2hunter2",
	}})

	p := &Plan{GitHub: GitHubPlan{
		AppID: fake.AppID(), InstallationID: fake.InstallationID(),
		Target: "acme", TargetType: store.TargetOrg, APIBaseURL: fake.URL(),
	}}
	if err := i.appFromAnswers(t.Context(), st, key, p); err != nil {
		t.Fatalf("appFromAnswers: %v", err)
	}

	all, err := st.ListInstallations(t.Context())
	if err != nil {
		t.Fatalf("ListInstallations: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("recorded %d installations, want 1", len(all))
	}
	if strings.Contains(string(all[0].PrivateKeyEnc), "PRIVATE KEY") {
		t.Fatal("the private key was stored in the clear")
	}
	if got, err := key.OpenString(all[0].WebhookSecretEnc); err != nil || got != "hunter2hunter2" {
		t.Fatalf("unsealed webhook secret = %q, %v", got, err)
	}

	// The run must say what the App actually got, because an App created by
	// hand with the wrong permissions otherwise fails much later in a way that
	// looks like a bug in Zoomies.
	body := out.String()
	if !strings.Contains(body, "verified against GitHub") {
		t.Fatalf("the credentials were not proved before setup claimed success:\n%s", body)
	}
	if !strings.Contains(body, "granted") {
		t.Fatalf("what the App was granted must be reported:\n%s", body)
	}
	if !strings.Contains(body, "recorded installation") {
		t.Fatalf("the ledger must record what was written:\n%s", body)
	}
	if len(i.Written()) == 0 {
		t.Fatal("recording an installation left nothing in the ledger")
	}
}

// Credentials GitHub rejects must not stop setup: the fleet is otherwise
// usable, and the Installations page is where this gets fixed. What must not
// happen is setup claiming the credentials work.
func TestVerifyInstallationWarnsWithoutFailingTheRun(t *testing.T) {
	i, out := newUnattendedInstaller(t, nil)

	// A key that is not a key cannot even build a client.
	i.verifyInstallation(t.Context(), &store.Installation{
		AppID: 1, InstallationID: 2, Target: "acme", TargetType: store.TargetOrg,
		APIBaseURL: "https://api.github.com",
	}, "not a key")
	if !strings.Contains(out.String(), "could not build a GitHub client") {
		t.Fatalf("a broken key must be reported:\n%s", out.String())
	}

	// A server that refuses every call is the credentials being rejected.
	out.Reset()
	refuser := newRefusingGitHub(t)
	i.verifyInstallation(t.Context(), &store.Installation{
		AppID: 1, InstallationID: 2, Target: "acme", TargetType: store.TargetOrg,
		APIBaseURL: refuser,
	}, appKey())
	body := out.String()
	if !strings.Contains(body, "GitHub rejected these credentials") {
		t.Fatalf("a rejection must be reported:\n%s", body)
	}
	if !strings.Contains(body, "Installations page") {
		t.Fatalf("the operator must be told where to fix it:\n%s", body)
	}
}

// The App needs different permissions depending on whether it manages an
// organisation's runners or one repository's, and naming the wrong one is a
// setup that fails at the first scale-up.
func TestRunnerPermissionDependsOnTheTarget(t *testing.T) {
	if got := runnerPermission(store.TargetRepo); got != "administration" {
		t.Fatalf("repo permission = %q, want administration", got)
	}
	if got := runnerPermission(store.TargetOrg); got != "organization_self_hosted_runners" {
		t.Fatalf("org permission = %q, want organization_self_hosted_runners", got)
	}
	// Anything else is treated as an organisation, which is the default target.
	if got := runnerPermission(""); got != "organization_self_hosted_runners" {
		t.Fatalf("default permission = %q", got)
	}
}

// Administration read reaches more than the checks that need it do, so the list
// names it when it is requested and not otherwise. A repository App already
// lists Administration write among its runner permissions, and a request for
// read on top of that would be a line that promises nothing the App lacks.
func TestNotePermissionsNamesAdministrationReadOnlyWhenKennelSettingsAreRequested(t *testing.T) {
	for _, tc := range []struct {
		name     string
		target   store.TargetType
		kennel   bool
		mentions bool
	}{
		{"an organisation App that did not ask", store.TargetOrg, false, false},
		{"an organisation App that asked", store.TargetOrg, true, true},
		{"a repository App that asked", store.TargetRepo, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i, out := newUnattendedInstaller(t, nil)
			i.notePermissions(&Plan{GitHub: GitHubPlan{TargetType: tc.target, KennelSettings: tc.kennel}})
			if got := strings.Contains(out.String(), "administration:read"); got != tc.mentions {
				t.Errorf("mentions administration:read = %v, want %v\n%s", got, tc.mentions, out.String())
			}
		})
	}
}

// Contents read is the permission that lets an App read code, so the list names it
// when it was requested, and says the App cannot read code only when it was not.
// With the migration wizard the App holds Contents write, which the list already
// names, so read is not said twice.
func TestNotePermissionsNamesContentsReadOnlyWhenKennelFilesAreRequested(t *testing.T) {
	for _, tc := range []struct {
		name      string
		files     bool
		migration bool
		reads     bool
		cannot    bool
	}{
		{"neither", false, false, false, true},
		{"files asked", true, false, true, false},
		{"files asked with the wizard", true, true, false, false},
		{"the wizard alone", false, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i, out := newUnattendedInstaller(t, nil)
			i.notePermissions(&Plan{GitHub: GitHubPlan{TargetType: store.TargetOrg, KennelFiles: tc.files, Migration: tc.migration}})
			body := out.String()
			if got := strings.Contains(body, "contents:read"); got != tc.reads {
				t.Errorf("mentions contents:read = %v, want %v\n%s", got, tc.reads, body)
			}
			if got := strings.Contains(body, "cannot read or change code"); got != tc.cannot {
				t.Errorf("says the App cannot read code = %v, want %v\n%s", got, tc.cannot, body)
			}
		})
	}
}

// A repository App holds Administration write already, so the question could
// change nothing for it. It is not asked, which a test can see because asking
// needs a terminal and this one has none, and the answer is recorded as no even
// if the plan arrived with a yes.
func TestTheKennelSettingsQuestionIsNotAskedOfARepositoryApp(t *testing.T) {
	i, out := newUnattendedInstaller(t, nil)
	p := &Plan{GitHub: GitHubPlan{TargetType: store.TargetRepo, KennelSettings: true}}
	if err := i.askKennelSettingsPermission(t.Context(), p); err != nil {
		t.Fatalf("a repository App was asked: %v", err)
	}
	if p.GitHub.KennelSettings {
		t.Error("a repository App was recorded as wanting a permission it already holds")
	}
	if out.Len() != 0 {
		t.Errorf("a repository App was told something: %q", out.String())
	}
}

// An operator reading GitHub's consent screen should find exactly what the
// installer said it would ask for. The migration wizard's three write
// permissions are the ones nobody expects a runner controller to want, so the
// list names them when they are requested, and says in so many words that the
// App cannot touch code when they are not.
func TestNotePermissionsNamesTheMigrationPermissionsOnlyWhenTheyAreRequested(t *testing.T) {
	for _, tc := range []struct {
		name      string
		migration bool
	}{{"without the migration wizard", false}, {"with the migration wizard", true}} {
		t.Run(tc.name, func(t *testing.T) {
			i, out := newUnattendedInstaller(t, nil)
			i.notePermissions(&Plan{GitHub: GitHubPlan{TargetType: store.TargetOrg, Migration: tc.migration}})
			body := out.String()

			if !strings.Contains(body, "organization_self_hosted_runners:write") {
				t.Errorf("the runner permission is missing:\n%s", body)
			}
			for _, perm := range []string{"contents:write", "pull_requests:write", "workflows:write"} {
				if got := strings.Contains(body, perm); got != tc.migration {
					t.Errorf("mentions %s = %v, want %v\n%s", perm, got, tc.migration, body)
				}
			}
			if said := strings.Contains(body, "cannot read or change code"); said == tc.migration {
				t.Errorf("says the App cannot touch code = %v, want %v\n%s", said, !tc.migration, body)
			}
		})
	}
}

// GitHub cannot deliver to loopback, and an App's webhook URL is fixed when it
// is created -- so an unattended run gets the warning and carries on, since
// there is nobody there to answer the question.
func TestCheckWebhookReachableWarnsAboutLoopbackUnattended(t *testing.T) {
	i, out := newUnattendedInstaller(t, nil)

	p := &Plan{ExternalURL: "http://127.0.0.1:8080"}
	if err := i.checkWebhookReachable(t.Context(), p); err != nil {
		t.Fatalf("checkWebhookReachable: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, "cannot deliver webhooks") {
		t.Fatalf("loopback must be warned about:\n%s", body)
	}
	if !strings.Contains(body, "poller") {
		t.Fatalf("the operator must be told what it falls back to:\n%s", body)
	}
	if p.GitHub.Skip {
		t.Fatal("an unattended run skipped GitHub on its own")
	}

	// A real address is not warned about at all.
	out.Reset()
	if err := i.checkWebhookReachable(t.Context(), &Plan{ExternalURL: "https://zoomies.example.com"}); err != nil {
		t.Fatalf("checkWebhookReachable: %v", err)
	}
	if out.String() != "" {
		t.Fatalf("a reachable address was warned about:\n%s", out.String())
	}
}

// The countdown is there so an operator can see the installer waiting rather
// than hung, which means it has no business writing anything when there is no
// terminal to rewrite.
func TestCountdownIsSilentWithoutATerminal(t *testing.T) {
	i, out := newUnattendedInstaller(t, nil)
	if tick := i.countdown("waiting"); tick != nil {
		t.Fatal("an unattended run got a countdown to draw")
	}
	i.clearLine()
	if out.String() != "" {
		t.Fatalf("an unattended run wrote a countdown:\n%q", out.String())
	}
}

func TestCountdownRewritesOneLine(t *testing.T) {
	out := &strings.Builder{}
	i := &Installer{out: out, ui: newUI(out), interactive: true}

	tick := i.countdown("waiting for GitHub")
	if tick == nil {
		t.Fatal("an interactive run got no countdown")
	}
	tick(30 * time.Second)
	// A clock that has run out shows zero rather than a negative duration.
	tick(-time.Second)
	body := out.String()
	if !strings.Contains(body, "waiting for GitHub") || !strings.Contains(body, "30s left") {
		t.Fatalf("countdown = %q", body)
	}
	if strings.Contains(body, "-1s") {
		t.Fatalf("a countdown past zero showed a negative duration: %q", body)
	}
	if !strings.HasPrefix(body, "\r") {
		t.Fatalf("the countdown must rewrite its line rather than scroll: %q", body)
	}

	i.clearLine()
	if !strings.HasSuffix(out.String(), "\r") {
		t.Fatal("clearLine must leave the cursor at the start of the wiped line")
	}
}

// The handshake's listener is loopback on a free port, because nothing off this
// host has any business in it and 8080 is very often the controller's own.
func TestCallbackServerBindsLoopbackAndClosesTwice(t *testing.T) {
	c, err := newCallbackServer()
	if err != nil {
		t.Fatalf("newCallbackServer: %v", err)
	}
	if !strings.HasPrefix(c.URL(), "http://127.0.0.1:") {
		t.Fatalf("URL = %q, want a loopback address", c.URL())
	}
	if !strings.HasSuffix(c.CallbackURL(), "/callback") {
		t.Fatalf("CallbackURL = %q", c.CallbackURL())
	}
	if c.State() == "" {
		t.Fatal("the handshake has no anti-forgery state")
	}

	c.Configure([]byte(`{"name":"zoomies-acme"}`), "https://github.com/organizations/acme/settings/apps/new")
	c.Start()

	resp, err := http.Get(c.URL())
	if err != nil {
		t.Fatalf("get start page: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "zoomies-acme") {
		t.Fatalf("the start page must carry the manifest:\n%s", body)
	}

	// Closing twice matters, because the flow closes on both the success and
	// the failure path.
	c.Close()
	c.Close()
	if _, err := http.Get(c.URL()); err == nil {
		t.Fatal("the listener survived Close")
	}
}

// A server that was never started still has a listener holding a port, and
// closing it has to give that port back.
func TestCallbackServerCloseReleasesAnUnstartedListener(t *testing.T) {
	c, err := newCallbackServer()
	if err != nil {
		t.Fatalf("newCallbackServer: %v", err)
	}
	addr := c.URL()
	c.Close()
	if _, err := http.Get(addr); err == nil {
		t.Fatal("an unstarted listener survived Close")
	}
}

// newRefusingGitHub is an API endpoint that rejects everything, which is what
// wrong credentials look like from here.
func newRefusingGitHub(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// The prompts cannot be driven from a test, so the order they are asked in is
// pinned by reading the function that asks them.
//
// On the default loopback install the reachability question ends in "connect it
// later in the browser" often enough that asking the four questions about the
// App first threw their answers away: the operator typed an organisation, an
// API address and a name, was told GitHub could not reach them, chose to skip,
// and met the same four questions again on a blank form in the browser. The
// check reads only the external URL, so nothing is lost by asking it first.
func TestTheReachabilityQuestionIsAskedBeforeTheQuestionsAboutTheApp(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "manifest.go", nil, 0)
	if err != nil {
		t.Fatalf("parse manifest.go: %v", err)
	}
	var calls []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "appFromManifest" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					calls = append(calls, sel.Sel.Name)
				}
			}
			return true
		})
	}
	position := func(name string) int {
		for index, called := range calls {
			if called == name {
				return index
			}
		}
		t.Fatalf("appFromManifest never calls %s", name)
		return -1
	}
	reachable := position("checkWebhookReachable")
	for _, later := range []string{"askGitHubTarget", "askMigrationPermissions", "askKennelSettingsPermission", "askKennelFilesPermission"} {
		if at := position(later); at < reachable {
			t.Errorf("%s is asked before checkWebhookReachable: a Skip would throw its answers away", later)
		}
	}
}
