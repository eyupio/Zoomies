package installer

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
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
