package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// settings turns demoSettings into something a test can ask questions of.
func settings(t *testing.T, dir, addr string) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, s := range demoSettings(dir, addr) {
		if _, dup := got[s[0]]; dup {
			t.Errorf("%s is set twice, so which one wins depends on the order", s[0])
		}
		got[s[0]] = s[1]
	}
	return got
}

// The demo switches authentication off, which the validator permits only where
// nobody else can reach the listener. Whatever else changes, this must not.
func TestTheDemoListensOnLoopbackAndNowhereElse(t *testing.T) {
	got := settings(t, t.TempDir(), "127.0.0.1:8080")
	host, _, err := net.SplitHostPort(got["ZOOMIES_BIND"])
	if err != nil {
		t.Fatalf("the demo's bind address %q is not host:port: %v", got["ZOOMIES_BIND"], err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Errorf("the demo listens on %q, and authentication is off", host)
	}
	if got["ZOOMIES_DISABLE_AUTH"] != "true" {
		t.Error("the demo asks people to sign in, which is the one thing it exists to avoid")
	}
}

// "Nothing leaves this machine" is a claim the announcement makes, and it holds
// only while nothing in the configuration asks the outside world a question.
// The release check is the one a controller makes unprompted, once a day, to
// github.com whatever its own GitHub is.
func TestTheDemoAsksNothingOutsideThisMachine(t *testing.T) {
	got := settings(t, t.TempDir(), "127.0.0.1:8080")
	if got["ZOOMIES_UPDATE_CHECK_INTERVAL"] != "0" {
		t.Errorf("the demo still checks github.com for a new release (interval %q)", got["ZOOMIES_UPDATE_CHECK_INTERVAL"])
	}
	if got["ZOOMIES_AGENT_EMBEDDED"] != "false" {
		t.Error("the demo starts an agent, which wants a container runtime that most people looking around do not have")
	}
	if got["ZOOMIES_SEED_DEMO"] != "true" {
		t.Error("the demo does not seed its fleet, so it opens on an empty controller")
	}
}

// Everything the demo writes is under the directory it deletes. A path that
// escaped it would be data left on somebody's machine by a command that said it
// would leave none.
func TestEverythingTheDemoWritesIsInsideItsOwnDirectory(t *testing.T) {
	dir := t.TempDir()
	for name, value := range settings(t, dir, "127.0.0.1:8080") {
		if !strings.HasSuffix(name, "_PATH") && !strings.HasSuffix(name, "_DIR") {
			continue
		}
		rel, err := filepath.Rel(dir, value)
		if err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("%s is %q, which is outside the demo's directory %q", name, value, dir)
		}
	}
}

// A machine that already runs Zoomies has some of these set, and any of them
// would change what the demo is: an external URL alone turns "authentication is
// off" from a warning into a refusal to start.
func TestTheDemoIgnoresAndRestoresTheEnvironmentItWasStartedIn(t *testing.T) {
	t.Setenv("ZOOMIES_EXTERNAL_URL", "https://zoomies.example.com")
	t.Setenv("ZOOMIES_BIND", "0.0.0.0:9999")

	var inside, bindInside string
	err := withEnvironment([][2]string{{"ZOOMIES_BIND", "127.0.0.1:8080"}}, func() error {
		inside = os.Getenv("ZOOMIES_EXTERNAL_URL")
		bindInside = os.Getenv("ZOOMIES_BIND")
		return errors.New("passed through")
	})
	if err == nil || err.Error() != "passed through" {
		t.Errorf("what the function returned was not handed back: %v", err)
	}
	if inside != "" {
		t.Errorf("the external URL %q reached the demo's controller", inside)
	}
	if bindInside != "127.0.0.1:8080" {
		t.Errorf("the demo's own bind address was %q while it ran", bindInside)
	}
	if got := os.Getenv("ZOOMIES_EXTERNAL_URL"); got != "https://zoomies.example.com" {
		t.Errorf("the external URL was %q afterwards, so the demo changed this shell's environment", got)
	}
	if got := os.Getenv("ZOOMIES_BIND"); got != "0.0.0.0:9999" {
		t.Errorf("the bind address was %q afterwards", got)
	}
}

func TestThePortIsTheDefaultWhereThatIsFreeAndAnyFreeOneWhereItIsNot(t *testing.T) {
	busy := func(addr string) (net.Listener, error) {
		if strings.HasSuffix(addr, ":8080") {
			return nil, errors.New("address already in use")
		}
		return net.Listen("tcp", addr)
	}
	port, err := chooseDemoPort(8080, false, busy)
	if err != nil {
		t.Fatal(err)
	}
	if port == 8080 || port == 0 {
		t.Errorf("a busy 8080 gave port %d", port)
	}

	free := func(addr string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
	if _, err := chooseDemoPort(8080, false, free); err != nil {
		t.Errorf("a free default was refused: %v", err)
	}
}

// Somebody who typed a port wants that port. Quietly giving them another one
// means the address in their bookmark, their tunnel or their notes is wrong.
func TestAPortThatWasAskedForIsUsedOrRefused(t *testing.T) {
	busy := func(string) (net.Listener, error) { return nil, errors.New("address already in use") }
	_, err := chooseDemoPort(9000, true, busy)
	if err == nil {
		t.Fatal("a port that was asked for and is taken was swapped for another")
	}
	for _, want := range []string{"9000", "--port"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
}

func TestTheDemoRefusesAPortThatIsNotOne(t *testing.T) {
	e, _, _ := newTestEnv(t)
	for _, port := range []string{"0", "70000", "-1"} {
		if code := dispatch(context.Background(), e, []string{"demo", "--port", port}); code != exitUsage {
			t.Errorf("--port %s exited %d, want %d", port, code, exitUsage)
		}
	}
}

// lockedBuffer is what the demo writes to while the test is reading it: the
// announcement comes from a goroutine, and the test has to wait for it to find
// out where the demo is.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// The whole thing, end to end: it starts, it says where it is, it answers with
// the seeded fleet in it, it stops when it is told to, and it leaves nothing on
// the machine.
func TestTheDemoServesASeededFleetAndLeavesNothingBehind(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a controller")
	}
	e, _, _ := newTestEnv(t)
	out := &lockedBuffer{}
	e.out = out
	// Where the demo's directory is made, so that "nothing is left behind" is
	// something this test can look at rather than hope for.
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)
	t.Setenv("TMP", scratch)
	t.Setenv("TEMP", scratch)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	exit := make(chan int, 1)
	go func() { exit <- dispatch(ctx, e, []string{"demo", "--no-browser"}) }()

	// It says where it is only once it answers, so this is the wait.
	where := regexp.MustCompile(`http://127\.0\.0\.1:\d+`)
	var base string
	for deadline := time.Now().Add(60 * time.Second); base == "" && time.Now().Before(deadline); {
		base = where.FindString(out.String())
		select {
		case code := <-exit:
			t.Fatalf("the demo stopped by itself with exit code %d before it said where it was:\n%s", code, out)
		case <-time.After(100 * time.Millisecond):
		}
	}
	if base == "" {
		t.Fatalf("the demo did not say where it was within a minute:\n%s", out)
	}

	get := func(path string) (int, string) {
		t.Helper()
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	// Signed in as nobody, because there is nobody to sign in as, and the fleet
	// on the page is the seeded one.
	if code, body := get("/api/v1/pools"); code != http.StatusOK || !strings.Contains(body, "zoomies-demo-linux-x64") {
		t.Errorf("the demo's pools: HTTP %d\n%s", code, body)
	}
	if code, _ := get("/api/v1/stats"); code != http.StatusOK {
		t.Errorf("the demo's stats asked for a sign-in: HTTP %d", code)
	}

	stop()
	select {
	case code := <-exit:
		if code != exitOK {
			t.Fatalf("stopping the demo exited %d, want %d", code, exitOK)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the demo did not stop within a minute of being asked to")
	}

	for _, want := range []string{"Ctrl-C", "deleted"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("what the demo said does not mention %q:\n%s", want, out)
		}
	}
	left, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		var names []string
		for _, f := range left {
			names = append(names, f.Name())
		}
		t.Errorf("the demo left %v behind in the temporary directory", names)
	}
}

// The demo says what it talks to, and the assistant is the one part of it a
// person might assume talks to a model somewhere else.
func TestTheDemoAnnouncementSaysTheAssistantAnswersFromABuiltInModel(t *testing.T) {
	var out bytes.Buffer
	announceDemo(&out, "http://127.0.0.1:8080")
	if s := out.String(); !strings.Contains(s, "assistant") || !strings.Contains(s, "built-in model") {
		t.Errorf("the announcement does not say where the assistant's answers come from:\n%s", s)
	}
}
