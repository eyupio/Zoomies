package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestTransport(t *testing.T, h http.Handler) (*HTTPTransport, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	tr, err := NewHTTPTransport(HTTPOptions{ControllerURL: srv.URL, Logger: testLogger()})
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}
	tr.SetCredentials("host-1", "agent-token")
	return tr, srv
}

func TestHTTPTransportSendsCredentialsAndIdentifiesItself(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]http.Header{}
	mux := http.NewServeMux()
	record := func(path string, body any) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen[path] = r.Header.Clone()
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(body)
		})
	}
	record(PathJoin, JoinResponse{HostID: "host-1", AgentToken: "agent-token"})
	record(PathHeartbeat, HeartbeatResponse{OK: true})
	record(PathResults, struct{}{})

	tr, _ := newTestTransport(t, mux)
	ctx := context.Background()
	if _, err := tr.Join(ctx, JoinRequest{Name: "h"}); err != nil {
		t.Fatalf("Join: %v", err)
	}
	if _, err := tr.Heartbeat(ctx, HeartbeatRequest{Capacity: 2}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if err := tr.ReportResult(ctx, TaskResult{TaskID: "t1", OK: true}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// Join is the one call made without credentials, because it is what mints
	// them.
	if got := seen[PathJoin].Get("Authorization"); got != "" {
		t.Fatalf("join sent Authorization %q", got)
	}
	for _, path := range []string{PathHeartbeat, PathResults} {
		if got := seen[path].Get("Authorization"); got != "Bearer agent-token" {
			t.Fatalf("%s Authorization = %q", path, got)
		}
		if got := seen[path].Get(HeaderHostID); got != "host-1" {
			t.Fatalf("%s %s = %q", path, HeaderHostID, got)
		}
	}
	for path, h := range seen {
		if !strings.HasPrefix(h.Get("User-Agent"), "zoomies-agent/") {
			t.Fatalf("%s User-Agent = %q", path, h.Get("User-Agent"))
		}
	}
	if tr.Describe() == "" {
		t.Fatal("Describe returned nothing to print in the banner")
	}
}

func TestHTTPTransportHasNoGlobalTimeout(t *testing.T) {
	tr, err := NewHTTPTransport(HTTPOptions{ControllerURL: "https://controller.example:8080", Logger: testLogger()})
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}
	// A client-wide timeout would abort long polls and followed log streams,
	// which is the classic way long-polling turns into a tight retry loop.
	if tr.http.Timeout != 0 {
		t.Fatalf("http.Client.Timeout = %s, want 0 with per-request deadlines instead", tr.http.Timeout)
	}
}

func TestHTTPTransportLongPollOutlivesTheRequestedWait(t *testing.T) {
	var gotWait string
	mux := http.NewServeMux()
	mux.HandleFunc(PathTasks, func(w http.ResponseWriter, r *http.Request) {
		gotWait = r.URL.Query().Get("wait")
		// The controller answers after the wait the agent asked for, which is
		// exactly the case a wait-length deadline would break.
		time.Sleep(300 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(TaskBatch{Tasks: []Task{{ID: "t1", Kind: TaskCreateRunner}}})
	})

	tr, _ := newTestTransport(t, mux)
	batch, err := tr.PollTasks(context.Background(), 200*time.Millisecond)
	if err != nil {
		t.Fatalf("PollTasks: %v", err)
	}
	if len(batch.Tasks) != 1 {
		t.Fatalf("batch = %+v, want one task", batch)
	}
	if gotWait != "1" {
		t.Fatalf("wait query = %q, want the wait in whole seconds", gotWait)
	}
}

func TestHTTPTransportPollTreats204AsIdle(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(PathTasks, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	tr, _ := newTestTransport(t, mux)
	batch, err := tr.PollTasks(context.Background(), time.Second)
	if err != nil {
		t.Fatalf("PollTasks: %v", err)
	}
	if len(batch.Tasks) != 0 {
		t.Fatalf("batch = %+v, want empty", batch)
	}
}

func TestHTTPTransportUnauthorizedIsTerminal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(PathTasks, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unknown agent token", http.StatusUnauthorized)
	})

	tr, _ := newTestTransport(t, mux)
	_, err := tr.PollTasks(context.Background(), time.Second)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("error = %v, want ErrUnauthorized", err)
	}
	if Retryable(err) {
		t.Fatal("a rejected token was reported as retryable, which would hammer the controller forever")
	}
	if !strings.Contains(err.Error(), "zoomies agent join") {
		t.Fatalf("error does not say how to recover: %v", err)
	}
}

func TestHTTPTransportHeartbeat404IsHostGone(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(PathHeartbeat, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no such host", http.StatusNotFound)
	})

	tr, _ := newTestTransport(t, mux)
	_, err := tr.Heartbeat(context.Background(), HeartbeatRequest{})
	if !errors.Is(err, ErrHostGone) {
		t.Fatalf("error = %v, want ErrHostGone", err)
	}
	if Retryable(err) {
		t.Fatal("a deleted host was reported as retryable")
	}
}

func TestHTTPTransportServerErrorsAreRetryable(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(PathHeartbeat, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "database is locked", http.StatusServiceUnavailable)
	})

	tr, _ := newTestTransport(t, mux)
	_, err := tr.Heartbeat(context.Background(), HeartbeatRequest{})
	if err == nil {
		t.Fatal("a 503 was reported as success")
	}
	if !Retryable(err) || !errors.Is(err, ErrRetryable) {
		t.Fatalf("error = %v, want a retryable one", err)
	}
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusServiceUnavailable {
		t.Fatalf("error does not carry the status: %v", err)
	}
}

func TestHTTPTransportNetworkFailureIsRetryable(t *testing.T) {
	tr, srv := newTestTransport(t, http.NewServeMux())
	srv.Close() // nothing is listening any more

	_, err := tr.Heartbeat(context.Background(), HeartbeatRequest{})
	if !Retryable(err) {
		t.Fatalf("error = %v, want a retryable one", err)
	}
}

func TestHTTPTransportLogStreamRoundTrip(t *testing.T) {
	type delivery struct {
		path string
		auth string
		body string
	}
	got := make(chan delivery, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(PathLogs+"/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- delivery{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: string(body)}
		w.WriteHeader(http.StatusOK)
	})

	tr, _ := newTestTransport(t, mux)
	w, err := tr.OpenLogStream(context.Background(), "stream-1")
	if err != nil {
		t.Fatalf("OpenLogStream: %v", err)
	}
	if _, err := io.WriteString(w, "first line\n"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := io.WriteString(w, "second line\n"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case d := <-got:
		if d.body != "first line\nsecond line\n" {
			t.Fatalf("controller received %q", d.body)
		}
		if d.path != PathLogs+"/stream-1" {
			t.Fatalf("stream posted to %q", d.path)
		}
		if d.auth != "Bearer agent-token" {
			t.Fatalf("stream Authorization = %q", d.auth)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the controller never received the log stream")
	}
}

func TestHTTPTransportLogStreamSurfacesARejection(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(PathLogs+"/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		http.Error(w, "no such stream", http.StatusGone)
	})

	tr, _ := newTestTransport(t, mux)
	w, err := tr.OpenLogStream(context.Background(), "stream-1")
	if err != nil {
		t.Fatalf("OpenLogStream: %v", err)
	}
	if _, err := io.WriteString(w, "output\n"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// A relay that quietly stopped delivering looks exactly like a quiet
	// runner, so the failure has to come back out of Close.
	if err := w.Close(); err == nil {
		t.Fatal("Close hid the controller's rejection")
	}
}

func TestNewHTTPTransportValidatesItsOptions(t *testing.T) {
	cases := map[string]HTTPOptions{
		"no url":       {},
		"bad scheme":   {ControllerURL: "ftp://controller"},
		"no host":      {ControllerURL: "https://"},
		"cert only":    {ControllerURL: "https://c:8080", ClientCertFile: "/tmp/cert.pem"},
		"key only":     {ControllerURL: "https://c:8080", ClientKeyFile: "/tmp/key.pem"},
		"missing ca":   {ControllerURL: "https://c:8080", CAFile: "/nonexistent/ca.pem"},
		"unparsed url": {ControllerURL: "://controller"},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			opts.Logger = testLogger()
			if _, err := NewHTTPTransport(opts); err == nil {
				t.Fatal("NewHTTPTransport accepted unusable options")
			}
		})
	}
}
