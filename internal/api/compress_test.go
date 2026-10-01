package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("the body is not gzip: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("reading the gzip body: %v", err)
	}
	return out
}

func TestAcceptEncodingIsReadByItsQValues(t *testing.T) {
	for header, want := range map[string]bool{
		"":                         false,
		"gzip":                     true,
		"br, gzip;q=0.8":           true,
		"deflate, GZIP":            true,
		"gzip;q=0":                 false,
		"identity":                 false,
		"br":                       false,
		"x-gzip-not-really, other": false,
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			r.Header.Set("Accept-Encoding", header)
		}
		if got := acceptsGzip(r); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}

// The UI's files are the bulk of a first visit, and they are served from the
// binary as they were embedded -- uncompressed until this. A client that asks
// gets a gzip body that decodes to the file; one that does not, or one that
// refuses it by q-value, or one that asks for a byte range, gets the file.
func TestStaticFilesAreServedGzippedToClientsThatAsk(t *testing.T) {
	script := strings.Repeat("export const answer = 42; // padding that compresses well\n", 200)
	h := &spaHandler{
		files: fstest.MapFS{
			"index.html":        {Data: []byte("<html></html>")},
			"assets/app.abc.js": {Data: []byte(script)},
			"assets/font.woff2": {Data: []byte(strings.Repeat("x", 4000))},
			"assets/tiny.js":    {Data: []byte("1")},
		},
		index:   []byte("<html></html>"),
		modTime: buildTime(),
		gz:      &sync.Map{},
	}
	get := func(path string, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	gz := get("/assets/app.abc.js", map[string]string{"Accept-Encoding": "gzip"})
	if gz.Code != http.StatusOK || gz.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("status %d, Content-Encoding %q; want a gzip body", gz.Code, gz.Header().Get("Content-Encoding"))
	}
	if got := string(gunzip(t, gz.Body.Bytes())); got != script {
		t.Fatal("the gzip body does not decode to the file")
	}
	if gz.Body.Len() >= len(script)/4 {
		t.Errorf("the body is %d bytes for a %d-byte script; it was not compressed", gz.Body.Len(), len(script))
	}
	if !strings.Contains(gz.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("Cache-Control = %q; the hashed asset lost its year-long cache", gz.Header().Get("Cache-Control"))
	}
	if n, _ := strconv.Atoi(gz.Header().Get("Content-Length")); n != gz.Body.Len() {
		t.Errorf("Content-Length %d does not describe the %d bytes sent", n, gz.Body.Len())
	}
	if !strings.Contains(gz.Header().Get("Vary"), "Accept-Encoding") {
		t.Errorf("Vary = %q; a shared cache would serve one encoding to everyone", gz.Header().Get("Vary"))
	}

	for name, headers := range map[string]map[string]string{
		"no Accept-Encoding": nil,
		"gzip;q=0":           {"Accept-Encoding": "gzip;q=0"},
		"a byte range":       {"Accept-Encoding": "gzip", "Range": "bytes=0-9"},
	} {
		w := get("/assets/app.abc.js", headers)
		if w.Header().Get("Content-Encoding") != "" {
			t.Errorf("%s was answered with Content-Encoding %q", name, w.Header().Get("Content-Encoding"))
		}
		if name != "a byte range" && w.Body.String() != script {
			t.Errorf("%s did not get the file as it is", name)
		}
		if !strings.Contains(w.Header().Get("Vary"), "Accept-Encoding") {
			t.Errorf("%s: the identity answer must say Vary too, or a cache will keep it for everybody", name)
		}
	}

	// Fonts are already compressed and a one-byte file is not worth it.
	for _, name := range []string{"/assets/font.woff2", "/assets/tiny.js"} {
		w := get(name, map[string]string{"Accept-Encoding": "gzip"})
		if w.Header().Get("Content-Encoding") != "" {
			t.Errorf("%s was compressed", name)
		}
	}
}

func TestAPIAnswersAreGzippedWhenTheyAreWorthIt(t *testing.T) {
	h := newHarness(t)
	token := h.token("reader", store.RoleViewer)
	inst := h.installation()

	small := h.do(request{method: http.MethodGet, path: "/api/v1/pools", token: token,
		headers: map[string]string{"Accept-Encoding": "gzip"}})
	small.mustStatus(t, http.StatusOK, "GET /pools with none")
	if small.header.Get("Content-Encoding") != "" {
		t.Error("an answer under a kilobyte was compressed")
	}

	for i := 0; i < 20; i++ {
		h.pool(inst, "pool-"+strconv.Itoa(i))
	}
	plain := h.do(request{method: http.MethodGet, path: "/api/v1/pools", token: token})
	plain.mustStatus(t, http.StatusOK, "GET /pools")
	if plain.header.Get("Content-Encoding") != "" || len(plain.body) < gzipMinSize {
		t.Fatalf("the fixture is wrong: encoding %q, %d bytes", plain.header.Get("Content-Encoding"), len(plain.body))
	}

	gz := h.do(request{method: http.MethodGet, path: "/api/v1/pools", token: token,
		headers: map[string]string{"Accept-Encoding": "gzip"}})
	gz.mustStatus(t, http.StatusOK, "GET /pools, gzip")
	if gz.header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", gz.header.Get("Content-Encoding"))
	}
	if !strings.Contains(gz.header.Get("Vary"), "Accept-Encoding") {
		t.Errorf("Vary = %q", gz.header.Get("Vary"))
	}
	var a, b map[string]any
	if err := json.Unmarshal(gunzip(t, gz.body), &a); err != nil {
		t.Fatalf("the gzip body is not JSON: %v", err)
	}
	if err := json.Unmarshal(plain.body, &b); err != nil {
		t.Fatal(err)
	}
	if len(a["items"].([]any)) != 20 || len(b["items"].([]any)) != 20 {
		t.Fatal("compression changed what the list says")
	}
	if len(gz.body) >= len(plain.body) {
		t.Errorf("the gzip body (%d bytes) is not smaller than the plain one (%d)", len(gz.body), len(plain.body))
	}
}

// The three kinds of body that must never be compressed: anything that carries
// a credential (BREACH), anything that is not a GET, and a stream, which has to
// reach the client as it is written rather than when the encoder's buffer fills.
func TestCompressionLeavesCredentialsAndStreamsAlone(t *testing.T) {
	big := []byte(`{"csrf":"` + strings.Repeat("a", 2000) + `"}`)
	json200 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(big)))
		_, _ = w.Write(big)
	})
	h := compressResponses(json200)
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/api/v1/pools", true},
		{http.MethodGet, "/api/v1/auth/session", false},
		{http.MethodGet, "/api/v1/tokens", false},
		{http.MethodGet, "/api/v1/join-tokens", false},
		{http.MethodGet, "/api/v1/backups", false},
		{http.MethodPost, "/api/v1/pools", false},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if got := w.Header().Get("Content-Encoding") == "gzip"; got != tc.want {
			t.Errorf("%s %s compressed = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}

	// An export written without a declared length -- the usage CSV is one -- is
	// the biggest kind of answer there is, and has to be compressed too.
	csv := compressResponses(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		_, _ = io.WriteString(w, "pool,jobs\n")
		_, _ = w.Write(bytes.Repeat([]byte("pool-a,12\n"), 500))
	}))
	r := httptest.NewRequest(http.MethodGet, "/api/v1/usage.csv", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	csv.ServeHTTP(w, r)
	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Error("a CSV written without a Content-Length was not compressed")
	} else if got := gunzip(t, w.Body.Bytes()); !bytes.HasPrefix(got, []byte("pool,jobs\npool-a,12\n")) || len(got) != len("pool,jobs\n")+500*len("pool-a,12\n") {
		t.Errorf("the compressed CSV does not decode to what was written (%d bytes)", len(got))
	}

	// A stream: the first event must be readable while the handler is still
	// holding the connection open.
	release := make(chan struct{})
	stream := compressResponses(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: ping\n\n")
		w.(http.Flusher).Flush()
		<-release
	}))
	srv := httptest.NewServer(stream)
	defer srv.Close()
	defer close(release)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/logs", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "" {
		t.Fatal("a stream was compressed")
	}
	buf := make([]byte, 13)
	if _, err := io.ReadFull(resp.Body, buf); err != nil || string(buf) != "event: ping\n\n" {
		t.Fatalf("the first event did not arrive while the stream was open: %q, %v", buf, err)
	}
}

// onlyUnwrap is a ResponseWriter like the access log's: it reaches the
// connection by Unwrap and does not implement http.Flusher itself.
type onlyUnwrap struct{ inner http.ResponseWriter }

func (o onlyUnwrap) Header() http.Header         { return o.inner.Header() }
func (o onlyUnwrap) Write(p []byte) (int, error) { return o.inner.Write(p) }
func (o onlyUnwrap) WriteHeader(code int)        { o.inner.WriteHeader(code) }
func (o onlyUnwrap) Unwrap() http.ResponseWriter { return o.inner }

// A stream flushed through a writer that only unwraps used to hang: the
// compression layer looked for http.Flusher underneath it, did not find one,
// and held the first frame back for ever. Every live view in the UI is such a
// stream.
func TestAStreamFlushesThroughWritersThatOnlyUnwrap(t *testing.T) {
	release := make(chan struct{})
	stream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush: %v", err)
		}
		<-release
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		compressResponses(stream).ServeHTTP(onlyUnwrap{w}, r)
	}))
	defer srv.Close()
	defer close(release)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/runners/run_1/logs", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	done := make(chan error, 1)
	go func() {
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err == nil {
			resp.Body.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the response headers never arrived: the flush did not reach the connection")
	}
}
