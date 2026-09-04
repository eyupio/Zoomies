package backend

import (
	"bytes"
	"context"
	"encoding/binary"
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

// ---------------------------------------------------------------------------
// Endpoint parsing
// ---------------------------------------------------------------------------

func TestNewAPIClient(t *testing.T) {
	t.Run("unix url", func(t *testing.T) {
		c, err := NewAPIClient("unix:///var/run/docker.sock")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c.SocketPath() != "/var/run/docker.sock" {
			t.Fatalf("socket = %q", c.SocketPath())
		}
		if got := c.urlFor("/_ping", nil); got != "http://docker/"+APIVersion+"/_ping" {
			t.Fatalf("url = %q", got)
		}
	})

	t.Run("bare path", func(t *testing.T) {
		c, err := NewAPIClient("/run/user/1000/podman/podman.sock")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c.SocketPath() != "/run/user/1000/podman/podman.sock" {
			t.Fatalf("socket = %q", c.SocketPath())
		}
	})

	t.Run("tcp", func(t *testing.T) {
		c, err := NewAPIClient("tcp://192.0.2.10:2375")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c.SocketPath() != "" {
			t.Fatalf("a tcp endpoint must have no socket, got %q", c.SocketPath())
		}
		if got := c.urlFor("/version", nil); got != "http://192.0.2.10:2375/"+APIVersion+"/version" {
			t.Fatalf("url = %q", got)
		}
	})

	t.Run("windows pipe is refused with advice", func(t *testing.T) {
		_, err := NewAPIClient(`npipe:////./pipe/docker_engine`)
		if err == nil {
			t.Fatal("npipe accepted")
		}
		if !strings.Contains(err.Error(), "Linux or macOS") {
			t.Fatalf("unhelpful message: %v", err)
		}
	})

	t.Run("nonsense is refused with the accepted forms", func(t *testing.T) {
		_, err := NewAPIClient("docker.sock")
		if err == nil || !strings.Contains(err.Error(), "unix:///var/run/docker.sock") {
			t.Fatalf("unhelpful message: %v", err)
		}
	})

	t.Run("empty", func(t *testing.T) {
		if _, err := NewAPIClient("   "); err == nil {
			t.Fatal("empty host accepted")
		}
	})
}

// ---------------------------------------------------------------------------
// Log demultiplexing
// ---------------------------------------------------------------------------

// frame builds one Docker log frame.
func frame(stream byte, payload string) []byte {
	b := make([]byte, logFrameHeader+len(payload))
	b[0] = stream
	binary.BigEndian.PutUint32(b[4:], uint32(len(payload)))
	copy(b[logFrameHeader:], payload)
	return b
}

func TestLogDemuxer(t *testing.T) {
	t.Run("interleaved streams are merged in order", func(t *testing.T) {
		var in bytes.Buffer
		in.Write(frame(1, "step one\n"))
		in.Write(frame(2, "warning: slow\n"))
		in.Write(frame(1, "step two\n"))

		got, err := io.ReadAll(NewLogDemuxer(io.NopCloser(&in)))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		want := "step one\nwarning: slow\nstep two\n"
		if string(got) != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("frames split across reads", func(t *testing.T) {
		var in bytes.Buffer
		in.Write(frame(1, "hello world, this is a long line\n"))
		in.Write(frame(1, "and another\n"))

		// A reader that hands back three bytes at a time cuts every header and
		// every payload in the middle, which is what a slow socket does.
		got, err := io.ReadAll(NewLogDemuxer(io.NopCloser(&trickleReader{data: in.Bytes(), n: 3})))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		want := "hello world, this is a long line\nand another\n"
		if string(got) != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("empty frames are skipped", func(t *testing.T) {
		var in bytes.Buffer
		in.Write(frame(1, ""))
		in.Write(frame(1, "after the empty frame\n"))

		got, err := io.ReadAll(NewLogDemuxer(io.NopCloser(&in)))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(got) != "after the empty frame\n" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("a truncated frame ends the stream instead of corrupting it", func(t *testing.T) {
		var in bytes.Buffer
		in.Write(frame(1, "complete\n"))
		in.Write([]byte{1, 0, 0}) // header cut off mid-way

		got, err := io.ReadAll(NewLogDemuxer(io.NopCloser(&in)))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(got) != "complete\n" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("a payload longer than the read buffer", func(t *testing.T) {
		payload := strings.Repeat("x", 5000) + "\n"
		d := NewLogDemuxer(io.NopCloser(bytes.NewReader(frame(1, payload))))
		got, err := io.ReadAll(d)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(got) != payload {
			t.Fatalf("got %d bytes, want %d", len(got), len(payload))
		}
	})
}

// trickleReader returns at most n bytes per Read, so that frame boundaries fall
// in awkward places.
type trickleReader struct {
	data []byte
	n    int
	off  int
}

func (r *trickleReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := min(min(r.n, len(p)), len(r.data)-r.off)
	copy(p, r.data[r.off:r.off+n])
	r.off += n
	return n, nil
}

// ---------------------------------------------------------------------------
// A fake Engine API
// ---------------------------------------------------------------------------

// fakeEngine is enough of the Docker API to exercise the client. It records
// every request so a test can assert on what was actually sent.
type fakeEngine struct {
	*httptest.Server
	mu   sync.Mutex
	seen []*http.Request
}

func newFakeEngine(t *testing.T, routes map[string]http.HandlerFunc) *fakeEngine {
	t.Helper()
	f := &fakeEngine{}
	mux := http.NewServeMux()
	for pattern, h := range routes {
		mux.HandleFunc(pattern, h)
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.seen = append(f.seen, r)
		f.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeEngine) client(t *testing.T) *APIClient {
	t.Helper()
	c, err := NewAPIClient("tcp://" + f.Listener.Addr().String())
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return c
}

// request returns the first recorded request for "METHOD /path".
func (f *fakeEngine) request(method, path string) *http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.seen {
		if r.Method == method && r.URL.Path == path {
			return r
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

const v = "/" + APIVersion

func TestAPIClientContainerLifecycle(t *testing.T) {
	var createBody ContainerCreateRequest
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/_ping": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Api-Version", "1.41")
			_, _ = w.Write([]byte("OK"))
		},
		"POST " + v + "/containers/create": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&createBody)
			writeJSON(w, http.StatusCreated, map[string]any{"Id": "container-1", "Warnings": []string{}})
		},
		"POST " + v + "/containers/container-1/start": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
		"GET " + v + "/containers/container-1/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, ContainerInspect{
				ID:     "container-1",
				Name:   "/runner-1",
				Config: &ContainerConfig{Image: "runner:1", Labels: map[string]string{LabelName: "runner-1"}},
				State: &ContainerState{
					Status: "exited", ExitCode: 3,
					StartedAt: "2026-01-02T03:04:05Z", FinishedAt: "2026-01-02T03:09:05Z",
				},
			})
		},
		"GET " + v + "/containers/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, []ContainerSummary{{
				ID: "container-1", Names: []string{"/runner-1"}, State: "running",
				Labels: map[string]string{LabelManaged: "true"},
			}})
		},
		"DELETE " + v + "/containers/container-1": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	})
	c := f.client(t)
	ctx := context.Background()

	if err := c.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	id, err := c.ContainerCreate(ctx, "runner-1", ContainerCreateRequest{Image: "runner:1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id != "container-1" {
		t.Fatalf("id = %q", id)
	}
	if createBody.Image != "runner:1" {
		t.Fatalf("the create body did not reach the daemon: %+v", createBody)
	}
	if got := f.request(http.MethodPost, v+"/containers/create").Form.Get("name"); got != "runner-1" {
		t.Fatalf("name query = %q", got)
	}

	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatalf("start: %v", err)
	}

	insp, err := c.ContainerInspect(ctx, id)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if insp.State.ExitCode != 3 || insp.State.Status != "exited" {
		t.Fatalf("state = %+v", insp.State)
	}

	list, err := c.ContainerList(ctx, map[string][]string{"label": {LabelManaged + "=true"}})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != "container-1" {
		t.Fatalf("list = %+v", list)
	}
	q := f.request(http.MethodGet, v+"/containers/json").Form
	if q.Get("all") != "1" {
		t.Fatalf("list must include stopped containers, query was %v", q)
	}
	var filters map[string][]string
	if err := json.Unmarshal([]byte(q.Get("filters")), &filters); err != nil {
		t.Fatalf("filters not JSON: %v", err)
	}
	if len(filters["label"]) != 1 || filters["label"][0] != LabelManaged+"=true" {
		t.Fatalf("filters = %v", filters)
	}

	if err := c.ContainerRemove(ctx, id, true); err != nil {
		t.Fatalf("remove: %v", err)
	}
	rq := f.request(http.MethodDelete, v+"/containers/container-1").Form
	if rq.Get("force") != "1" || rq.Get("v") != "1" {
		t.Fatalf("remove query = %v", rq)
	}
}

func TestAPIClientErrors(t *testing.T) {
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/missing/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such container: missing"})
		},
		"POST " + v + "/containers/busy/stop": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotModified)
		},
		"POST " + v + "/containers/busy/kill": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusConflict, map[string]string{"message": "container is not running"})
		},
		"POST " + v + "/containers/broken/start": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "driver failed programming external connectivity"})
		},
	})
	c := f.client(t)
	ctx := context.Background()

	t.Run("404 is ErrNotFound and keeps the daemon's message", func(t *testing.T) {
		_, err := c.ContainerInspect(ctx, "missing")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("got %v, want ErrNotFound", err)
		}
		if !strings.Contains(err.Error(), "No such container") {
			t.Fatalf("the daemon's explanation was lost: %v", err)
		}
		if StatusCode(err) != http.StatusNotFound {
			t.Fatalf("StatusCode = %d", StatusCode(err))
		}
	})

	t.Run("stopping an already-stopped container succeeds", func(t *testing.T) {
		if err := c.ContainerStop(ctx, "busy", 5*time.Second); err != nil {
			t.Fatalf("304 should not be an error: %v", err)
		}
		if got := f.request(http.MethodPost, v+"/containers/busy/stop").Form.Get("t"); got != "5" {
			t.Fatalf("stop timeout = %q, want 5", got)
		}
	})

	t.Run("killing a stopped container succeeds", func(t *testing.T) {
		if err := c.ContainerKill(ctx, "busy", "SIGKILL"); err != nil {
			t.Fatalf("409 should not be an error: %v", err)
		}
	})

	t.Run("a 500 surfaces the daemon's message", func(t *testing.T) {
		err := c.ContainerStart(ctx, "broken")
		if err == nil || !strings.Contains(err.Error(), "external connectivity") {
			t.Fatalf("got %v", err)
		}
		if errors.Is(err, ErrNotFound) {
			t.Fatal("a 500 must not look like a missing container")
		}
	})
}

func TestAPIClientUnreachableSocket(t *testing.T) {
	c, err := NewAPIClient("unix:///nonexistent/zoomies-test/docker.sock")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	err = c.Ping(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v, want ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), "/nonexistent/zoomies-test/docker.sock") {
		t.Fatalf("the message must name the socket: %v", err)
	}
}

func TestAPIClientLogs(t *testing.T) {
	var framed bytes.Buffer
	framed.Write(frame(1, "job started\n"))
	framed.Write(frame(2, "npm WARN\n"))

	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/framed/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, ContainerInspect{ID: "framed", Config: &ContainerConfig{Tty: false}, State: &ContainerState{Running: true}})
		},
		"GET " + v + "/containers/framed/logs": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(framed.Bytes())
		},
		"GET " + v + "/containers/tty/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, ContainerInspect{ID: "tty", Config: &ContainerConfig{Tty: true}, State: &ContainerState{Running: true}})
		},
		"GET " + v + "/containers/tty/logs": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("raw output\n"))
		},
	})
	c := f.client(t)
	ctx := context.Background()

	t.Run("framed output is demultiplexed", func(t *testing.T) {
		rc, err := c.ContainerLogs(ctx, "framed", LogQuery{Follow: true, Tail: 20, Since: time.Unix(1700000000, 0)})
		if err != nil {
			t.Fatalf("logs: %v", err)
		}
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(got) != "job started\nnpm WARN\n" {
			t.Fatalf("got %q", got)
		}
		q := f.request(http.MethodGet, v+"/containers/framed/logs").Form
		for k, want := range map[string]string{"stdout": "1", "stderr": "1", "follow": "1", "tail": "20", "since": "1700000000"} {
			if q.Get(k) != want {
				t.Fatalf("%s = %q, want %q (query %v)", k, q.Get(k), want, q)
			}
		}
	})

	t.Run("tty output passes through", func(t *testing.T) {
		rc, err := c.ContainerLogs(ctx, "tty", LogQuery{})
		if err != nil {
			t.Fatalf("logs: %v", err)
		}
		defer rc.Close()
		got, _ := io.ReadAll(rc)
		if string(got) != "raw output\n" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("logs for a container that is gone", func(t *testing.T) {
		_, err := c.ContainerLogs(ctx, "nope", LogQuery{})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("got %v, want ErrNotFound", err)
		}
	})
}

func TestAPIClientImages(t *testing.T) {
	pulls := 0
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/images/runner:1/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"Id": "sha256:abc"})
		},
		"GET " + v + "/images/absent:1/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such image: absent:1"})
		},
		"POST " + v + "/images/create": func(w http.ResponseWriter, r *http.Request) {
			pulls++
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			// Progress arrives in pieces; the client must drain all of it.
			for i := range 3 {
				_, _ = w.Write([]byte(`{"status":"Downloading","progressDetail":{"current":` + string(rune('1'+i)) + `}}` + "\n"))
				if flusher != nil {
					flusher.Flush()
				}
			}
			if r.Form.Get("fromImage") == "broken" {
				_, _ = w.Write([]byte(`{"errorDetail":{"message":"manifest unknown"},"error":"manifest unknown"}` + "\n"))
			}
		},
	})
	c := f.client(t)
	ctx := context.Background()

	t.Run("present", func(t *testing.T) {
		ok, err := c.ImageInspect(ctx, "runner:1")
		if err != nil || !ok {
			t.Fatalf("got %v %v", ok, err)
		}
	})

	t.Run("absent is not an error", func(t *testing.T) {
		ok, err := c.ImageInspect(ctx, "absent:1")
		if err != nil || ok {
			t.Fatalf("got %v %v", ok, err)
		}
	})

	t.Run("pull drains the progress stream", func(t *testing.T) {
		if err := c.ImagePull(ctx, "ghcr.io/acme/runner:2", ""); err != nil {
			t.Fatalf("pull: %v", err)
		}
		q := f.request(http.MethodPost, v+"/images/create").Form
		if q.Get("fromImage") != "ghcr.io/acme/runner" || q.Get("tag") != "2" {
			t.Fatalf("pull query = %v", q)
		}
		if pulls != 1 {
			t.Fatalf("pulls = %d", pulls)
		}
	})

	t.Run("an error inside the stream fails the pull", func(t *testing.T) {
		err := c.ImagePull(ctx, "broken", "")
		if err == nil || !strings.Contains(err.Error(), "manifest unknown") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestAPIClientNetworkEnsure(t *testing.T) {
	created := 0
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/networks/existing": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"Name": "existing"})
		},
		"GET " + v + "/networks/fresh": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "network fresh not found"})
		},
		"GET " + v + "/networks/raced": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "network raced not found"})
		},
		"POST " + v + "/networks/create": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["Name"] == "raced" {
				writeJSON(w, http.StatusConflict, map[string]string{"message": "network with name raced already exists"})
				return
			}
			created++
			writeJSON(w, http.StatusCreated, map[string]string{"Id": "net-1"})
		},
	})
	c := f.client(t)
	ctx := context.Background()

	if err := c.NetworkEnsure(ctx, "existing"); err != nil {
		t.Fatalf("existing: %v", err)
	}
	if created != 0 {
		t.Fatal("an existing network must not be recreated")
	}
	if err := c.NetworkEnsure(ctx, "fresh"); err != nil {
		t.Fatalf("fresh: %v", err)
	}
	if created != 1 {
		t.Fatalf("created = %d", created)
	}
	// Two agents on one host may create the same network at the same moment.
	if err := c.NetworkEnsure(ctx, "raced"); err != nil {
		t.Fatalf("a lost race must not fail: %v", err)
	}
}

func TestAPIClientStats(t *testing.T) {
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/c/stats": func(w http.ResponseWriter, r *http.Request) {
			if r.Form.Get("stream") != "false" {
				t.Errorf("stats must not stream, query %v", r.Form)
			}
			_, _ = w.Write([]byte(`{
			  "cpu_stats":{"cpu_usage":{"total_usage":200000000},"system_cpu_usage":2000000000,"online_cpus":2},
			  "precpu_stats":{"cpu_usage":{"total_usage":100000000},"system_cpu_usage":1000000000},
			  "memory_stats":{"usage":600,"limit":1000,"stats":{"inactive_file":100}}
			}`))
		},
	})
	c := f.client(t)

	got, err := c.ContainerStats(context.Background(), "c")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	// 0.1s of CPU over 1s of system time on 2 CPUs is 20%.
	if got.CPUPercent < 19.9 || got.CPUPercent > 20.1 {
		t.Fatalf("cpu = %v, want ~20", got.CPUPercent)
	}
	if got.MemoryBytes != 500 {
		t.Fatalf("memory = %d, want 500 (page cache excluded)", got.MemoryBytes)
	}
	if got.MemoryLimit != 1000 {
		t.Fatalf("limit = %d", got.MemoryLimit)
	}
}

func TestSplitImageRef(t *testing.T) {
	cases := []struct{ ref, name, tag string }{
		{"runner", "runner", "latest"},
		{"runner:2.1", "runner", "2.1"},
		{"ghcr.io/acme/runner:2.1", "ghcr.io/acme/runner", "2.1"},
		{"registry:5000/acme/runner", "registry:5000/acme/runner", "latest"},
		{"registry:5000/acme/runner:dev", "registry:5000/acme/runner", "dev"},
		{"runner@sha256:abc", "runner", "sha256:abc"},
	}
	for _, c := range cases {
		name, tag := splitImageRef(c.ref)
		if name != c.name || tag != c.tag {
			t.Errorf("%s -> (%q, %q), want (%q, %q)", c.ref, name, tag, c.name, c.tag)
		}
	}
}

func TestParseDockerTime(t *testing.T) {
	if got := parseDockerTime("0001-01-01T00:00:00Z"); !got.IsZero() {
		t.Fatalf("docker's never should be the zero time, got %v", got)
	}
	if got := parseDockerTime(""); !got.IsZero() {
		t.Fatalf("empty should be the zero time, got %v", got)
	}
	if got := parseDockerTime("nonsense"); !got.IsZero() {
		t.Fatalf("unparseable should be the zero time, got %v", got)
	}
	got := parseDockerTime("2026-01-02T03:04:05.123456789Z")
	if got.Year() != 2026 || got.Minute() != 4 {
		t.Fatalf("got %v", got)
	}
}
