package github

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// etagServer answers GET /thing with a body and an ETag derived from it, and
// answers 304 to a matching If-None-Match, as GitHub does. It records what it
// was sent.
type etagServer struct {
	*httptest.Server
	mu       sync.Mutex
	body     string
	status   int
	requests int
	sent     []string // the If-None-Match of each request
	accept   []string
}

func newETagServer(t *testing.T, body string) *etagServer {
	t.Helper()
	s := &etagServer{body: body, status: http.StatusOK}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.requests++
		inm := r.Header.Get("If-None-Match")
		s.sent = append(s.sent, inm)
		s.accept = append(s.accept, r.Header.Get("Accept"))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(5000-s.requests))
		if s.status != http.StatusOK {
			w.WriteHeader(s.status)
			_, _ = io.WriteString(w, `{"message":"no"}`)
			return
		}
		// A short hash, as GitHub's are: an ETag the length of the body would make
		// every entry twice the size the bound tests reason about.
		sum := sha256.Sum256([]byte(s.body))
		etag := `"` + hex.EncodeToString(sum[:8]) + `"`
		w.Header().Set("ETag", etag)
		w.Header().Set("Set-Cookie", "session=secret")
		if r.Method == http.MethodGet && inm == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = io.WriteString(w, s.body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *etagServer) set(body string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.body, s.status = body, status
}

func get(t *testing.T, c *http.Client, url string, hdr ...string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func client(cache *conditionalCache, scope string) *http.Client {
	return newConditionalClient(&http.Client{}, cache, scope)
}

// What the caller sees must not change: the point is the request count. A 304
// that surfaced as a 304 would be an error to every caller that never asked for
// a conditional read.
func TestARepeatedReadIsAnsweredFromMemoryAndLooksIdentical(t *testing.T) {
	srv := newETagServer(t, "hello")
	c := client(newConditionalCache(1<<20), "inst-1")
	code, body, _ := get(t, c, srv.URL+"/thing")
	if code != 200 || body != "hello" {
		t.Fatalf("first read = %d %q", code, body)
	}
	code, body, _ = get(t, c, srv.URL+"/thing")
	if code != 200 || body != "hello" {
		t.Fatalf("second read = %d %q, want the same as the first, not a 304", code, body)
	}
	if srv.sent[0] != "" || srv.sent[1] == "" {
		t.Errorf("If-None-Match sent = %q, want none on the first read and the ETag on the second", srv.sent)
	}
}

func TestAChangedResourceIsReadInFullAndRemembered(t *testing.T) {
	srv := newETagServer(t, "one")
	c := client(newConditionalCache(1<<20), "inst-1")
	get(t, c, srv.URL+"/thing")
	srv.set("two!", http.StatusOK)
	if _, body, _ := get(t, c, srv.URL+"/thing"); body != "two!" {
		t.Fatalf("body = %q, want the new one", body)
	}
	if _, body, _ := get(t, c, srv.URL+"/thing"); body != "two!" {
		t.Fatalf("body = %q after revalidating the new one", body)
	}
	if srv.sent[2] == "" || srv.sent[2] == srv.sent[1] {
		t.Errorf("third read sent %q, want the new ETag", srv.sent[2])
	}
}

// The 304 carries the quota as it is now. go-github reads the rate limit from
// the response, and a replayed header would show a quota that was true a minute
// ago.
func TestAReplayedResponseCarriesTheCurrentQuotaNotTheOldOne(t *testing.T) {
	srv := newETagServer(t, "x")
	c := client(newConditionalCache(1<<20), "inst-1")
	_, _, first := get(t, c, srv.URL+"/thing")
	_, _, second := get(t, c, srv.URL+"/thing")
	if first.Get("X-RateLimit-Remaining") != "4999" || second.Get("X-RateLimit-Remaining") != "4998" {
		t.Errorf("remaining = %q then %q, want 4999 then 4998", first.Get("X-RateLimit-Remaining"), second.Get("X-RateLimit-Remaining"))
	}
}

// Two installations asking for one URL are two questions. A response one was
// allowed to see must never be handed to the other, and the second must pay for
// its own first read.
func TestOneInstallationNeverGetsAnotherOnesResponse(t *testing.T) {
	srv := newETagServer(t, "private to one")
	shared := newConditionalCache(1 << 20)
	a, b := client(shared, "inst-A"), client(shared, "inst-B")
	get(t, a, srv.URL+"/thing")
	get(t, b, srv.URL+"/thing")
	if srv.sent[1] != "" {
		t.Errorf("installation B revalidated with %q: it was sent A's ETag, so it could have been handed A's body", srv.sent[1])
	}
	get(t, b, srv.URL+"/thing")
	if srv.sent[2] == "" {
		t.Error("installation B's own repeat read was not conditional")
	}
}

// GitHub varies a response on its media type, so the same URL asked for two
// ways is two answers.
func TestADifferentMediaTypeIsADifferentQuestion(t *testing.T) {
	srv := newETagServer(t, "x")
	c := client(newConditionalCache(1<<20), "i")
	get(t, c, srv.URL+"/thing", "Accept", "application/vnd.github+json")
	get(t, c, srv.URL+"/thing", "Accept", "application/vnd.github.raw+json")
	if srv.sent[1] != "" {
		t.Errorf("a read with another Accept was conditional on the first's ETag: %q", srv.sent)
	}
}

func TestAQueryStringIsPartOfWhatIsRemembered(t *testing.T) {
	srv := newETagServer(t, "x")
	c := client(newConditionalCache(1<<20), "i")
	get(t, c, srv.URL+"/thing?page=1")
	get(t, c, srv.URL+"/thing?page=2")
	if srv.sent[1] != "" {
		t.Errorf("page 2 was conditional on page 1's ETag: %q", srv.sent)
	}
}

// A refusal is not worth keeping, and what was kept for the same request may
// no longer be true: a permission taken away, a repository deleted.
func TestARefusalIsNotKeptAndForgetsWhatWasKeptBefore(t *testing.T) {
	srv := newETagServer(t, "fine")
	c := client(newConditionalCache(1<<20), "i")
	get(t, c, srv.URL+"/thing")
	srv.set("", http.StatusForbidden)
	if code, _, _ := get(t, c, srv.URL+"/thing"); code != http.StatusForbidden {
		t.Fatalf("code = %d, want the refusal passed through", code)
	}
	srv.set("fine", http.StatusOK)
	get(t, c, srv.URL+"/thing")
	if last := srv.sent[len(srv.sent)-1]; last != "" {
		t.Errorf("after a refusal the next read was still conditional (%q): the old entry should be gone", last)
	}
}

func TestAnErrorResponseIsNeverCached(t *testing.T) {
	srv := newETagServer(t, "x")
	srv.set("", http.StatusNotFound)
	c := client(newConditionalCache(1<<20), "i")
	get(t, c, srv.URL+"/thing")
	get(t, c, srv.URL+"/thing")
	for _, s := range srv.sent {
		if s != "" {
			t.Errorf("a conditional read followed a 404: %q", srv.sent)
		}
	}
}

func TestOnlyAGetIsEverConditional(t *testing.T) {
	var sawIfNoneMatch atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			sawIfNoneMatch.Store(true)
		}
		w.Header().Set("ETag", `"e"`)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	c := client(newConditionalCache(1<<20), "i")
	for i := 0; i < 2; i++ {
		resp, err := c.Post(srv.URL+"/thing", "text/plain", strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if sawIfNoneMatch.Load() {
		t.Error("a POST was made conditional")
	}
}

// A transport must not change the request it is given: the caller may retry it.
func TestTheCallersRequestIsLeftAlone(t *testing.T) {
	srv := newETagServer(t, "x")
	c := client(newConditionalCache(1<<20), "i")
	get(t, c, srv.URL+"/thing")
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/thing", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if req.Header.Get("If-None-Match") != "" {
		t.Error("the caller's request was given an If-None-Match")
	}
}

// A session cookie is not a property of the resource, and replaying one to a
// later caller would be a leak.
func TestACookieIsNeverReplayed(t *testing.T) {
	srv := newETagServer(t, "x")
	c := client(newConditionalCache(1<<20), "i")
	get(t, c, srv.URL+"/thing")
	_, _, h := get(t, c, srv.URL+"/thing")
	if h.Get("Set-Cookie") != "" {
		t.Errorf("a replayed response carried Set-Cookie: %q", h.Get("Set-Cookie"))
	}
}

// A response too big to keep is still delivered whole.
func TestAResponseTooBigToKeepIsStillDeliveredIntact(t *testing.T) {
	big := strings.Repeat("a", conditionalEntryBytes+10)
	srv := newETagServer(t, big)
	c := client(newConditionalCache(64<<20), "i")
	for i := 0; i < 2; i++ {
		if _, body, _ := get(t, c, srv.URL+"/thing"); body != big {
			t.Fatalf("read %d delivered %d bytes, want %d", i, len(body), len(big))
		}
	}
	if srv.sent[1] != "" {
		t.Error("an oversize response was kept")
	}
}

// Memory is bounded across every installation, least recently used first.
func TestTheCacheNeverHoldsMoreThanItsBoundAndForgetsTheLeastRecentlyUsed(t *testing.T) {
	srv := newETagServer(t, strings.Repeat("a", 2000))
	cache := newConditionalCache(6000) // room for two entries, not three
	c := client(cache, "i")
	get(t, c, srv.URL+"/a")
	get(t, c, srv.URL+"/b")
	get(t, c, srv.URL+"/a") // a is now the more recently used
	get(t, c, srv.URL+"/c") // evicts b
	if cache.used > cache.max {
		t.Fatalf("used %d over the bound %d", cache.used, cache.max)
	}
	before := len(srv.sent)
	get(t, c, srv.URL+"/a")
	get(t, c, srv.URL+"/b")
	if srv.sent[before] == "" {
		t.Error("a, recently used, was forgotten")
	}
	if srv.sent[before+1] != "" {
		t.Error("b, least recently used, was kept")
	}
}

func TestAResponseWithNoETagIsNotKept(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		// Presence, not value: an empty If-None-Match is still a conditional read of
		// something that never offered a version to compare with.
		if _, present := r.Header["If-None-Match"]; present {
			t.Error("a conditional read of a resource that sent no ETag")
		}
		_, _ = io.WriteString(w, "x")
	}))
	defer srv.Close()
	c := client(newConditionalCache(1<<20), "i")
	get(t, c, srv.URL+"/thing")
	get(t, c, srv.URL+"/thing")
	if n.Load() != 2 {
		t.Errorf("requests = %d", n.Load())
	}
}

func TestTheCacheIsSafeUnderConcurrentUse(t *testing.T) {
	srv := newETagServer(t, "x")
	c := client(newConditionalCache(1<<20), "i")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			get(t, c, srv.URL+"/thing?n="+strconv.Itoa(i%4))
		}(i)
	}
	wg.Wait()
}
