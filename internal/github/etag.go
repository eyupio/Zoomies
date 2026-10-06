package github

import (
	"bytes"
	"container/list"
	"io"
	"net/http"
	"strings"
	"sync"
)

// The conditional-request transport makes a repeated read cost nothing against
// GitHub's primary rate limit: a GET that carries If-None-Match and comes back
// 304 is not counted. Kennel Club re-reads the same repositories every day and
// almost nothing changes between reads, so without this its daily sweep would
// spend the quota the poller and the JIT configurations live on.
//
// It is layered under Kennel Club's reads only. The poller and the runner calls
// keep the transport they have, because a transport that quietly answered from
// memory is the wrong default for code whose whole job is to see the queue as it
// is now. A 304 means "unchanged", so what a caller gets is what it would have
// got, and the only observable difference is the request count.

const (
	// conditionalCacheBytes bounds everything the cache holds, across every
	// installation. A restart empties it, which costs one full-price read per
	// repository that is then due, spread by the schedule's jitter.
	conditionalCacheBytes = 20 << 20
	// conditionalEntryBytes is the largest single response worth remembering.
	// Bigger ones are delivered intact and not kept: a listing that large changes
	// too often to repay holding it.
	conditionalEntryBytes = 512 << 10
)

// conditionalCache is a size-bounded, least-recently-used store of responses by
// request. It is shared by every installation's transport, which is safe because
// the scope an installation's transport adds to each key is what separates one
// tenant's responses from another's.
type conditionalCache struct {
	mu      sync.Mutex
	max     int64
	used    int64
	order   *list.List // front is the most recently used
	entries map[string]*list.Element
}

type conditionalEntry struct {
	key    string
	etag   string
	status int
	header http.Header
	body   []byte
}

func (e *conditionalEntry) size() int64 { return int64(len(e.body) + len(e.key) + len(e.etag) + 256) }

func newConditionalCache(max int64) *conditionalCache {
	return &conditionalCache{max: max, order: list.New(), entries: map[string]*list.Element{}}
}

func (c *conditionalCache) get(key string) (*conditionalEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*conditionalEntry), true
}

func (c *conditionalCache) put(e *conditionalEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[e.key]; ok {
		c.used -= old.Value.(*conditionalEntry).size()
		c.order.Remove(old)
		delete(c.entries, e.key)
	}
	if e.size() > c.max {
		return
	}
	c.entries[e.key] = c.order.PushFront(e)
	c.used += e.size()
	for c.used > c.max {
		last := c.order.Back()
		if last == nil {
			break
		}
		old := last.Value.(*conditionalEntry)
		c.used -= old.size()
		c.order.Remove(last)
		delete(c.entries, old.key)
	}
}

func (c *conditionalCache) drop(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.used -= el.Value.(*conditionalEntry).size()
		c.order.Remove(el)
		delete(c.entries, key)
	}
}

// conditionalTransport revalidates GET responses against what it last saw.
type conditionalTransport struct {
	base  http.RoundTripper
	cache *conditionalCache
	// scope separates one installation's responses from another's. Two
	// installations asking for the same URL are different questions: each is
	// answered for what that installation may see.
	scope string
}

// rateLimitHeaders are what a 304 refreshes on the response the caller gets,
// because the quota left is as true on a not-modified answer as on any other
// and go-github reads it from there.
var rateLimitHeaders = []string{"X-Ratelimit-Limit", "X-Ratelimit-Remaining", "X-Ratelimit-Reset", "X-Ratelimit-Used", "X-Ratelimit-Resource", "Date"}

// RoundTrip implements http.RoundTripper. It never changes the request it is
// given, as the interface requires.
func (t *conditionalTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return t.base.RoundTrip(req)
	}
	key := t.scope + "\x00" + req.Header.Get("Accept") + "\x00" + req.URL.String()
	entry, cached := t.cache.get(key)
	out := req
	if cached {
		out = req.Clone(req.Context())
		out.Header.Set("If-None-Match", entry.etag)
	}
	resp, err := t.base.RoundTrip(out)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusNotModified && cached {
		_ = resp.Body.Close()
		return replay(req, entry, resp.Header), nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// A refusal or a missing resource is not worth keeping, and what was kept
		// for the same request may no longer be true: a permission withdrawn, a
		// repository deleted.
		if cached {
			t.cache.drop(key)
		}
		return resp, nil
	}

	etag := resp.Header.Get("ETag")
	if etag == "" || resp.StatusCode != http.StatusOK {
		return resp, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, conditionalEntryBytes+1))
	if err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	if len(body) > conditionalEntryBytes {
		// Too big to keep. Hand the caller everything, the part already read and
		// what is still to come.
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), resp.Body), resp.Body}
		return resp, nil
	}
	_ = resp.Body.Close()
	h := resp.Header.Clone()
	// A cookie is a session, not a property of the resource.
	h.Del("Set-Cookie")
	t.cache.put(&conditionalEntry{key: key, etag: etag, status: resp.StatusCode, header: h, body: body})
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

// replay builds the response a caller would have got had the resource been sent
// again: the remembered body and headers, with the quota headers of the 304 that
// has just been answered.
func replay(req *http.Request, e *conditionalEntry, fresh http.Header) *http.Response {
	h := e.header.Clone()
	for _, name := range rateLimitHeaders {
		if v := fresh.Values(name); len(v) > 0 {
			h[name] = append([]string(nil), v...)
		}
	}
	return &http.Response{
		Status:        "200 OK",
		StatusCode:    e.status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        h,
		Body:          io.NopCloser(bytes.NewReader(e.body)),
		ContentLength: int64(len(e.body)),
		Request:       req,
	}
}

// newConditionalClient returns an http.Client like hc whose GETs are revalidated.
func newConditionalClient(hc *http.Client, cache *conditionalCache, scope string) *http.Client {
	base := hc.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	return &http.Client{
		Transport:     &conditionalTransport{base: base, cache: cache, scope: strings.TrimSpace(scope)},
		CheckRedirect: hc.CheckRedirect,
		Jar:           hc.Jar,
		Timeout:       hc.Timeout,
	}
}
