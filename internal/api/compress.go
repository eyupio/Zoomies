package api

import (
	"bytes"
	"compress/gzip"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// gzipMinSize is the smallest body worth compressing. Below this the gzip
// header and the round trip through the encoder cost more than they save, and
// a 200-byte error body would arrive larger than it left.
const gzipMinSize = 1024

// acceptsGzip reports whether the client will take a gzip body. It reads the
// q-values, because "gzip;q=0" is a refusal and a substring match would send a
// client that said so a body it asked not to have.
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(strings.Join(r.Header.Values("Accept-Encoding"), ","), ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			continue
		}
		q := 1.0
		if _, v, ok := strings.Cut(params, "="); ok {
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				q = f
			}
		}
		return q > 0
	}
	return false
}

// compressibleType is whether a response of this media type is text a gzip
// body suits. Anything already compressed (an archive, an image) or streamed
// (text/event-stream, the log relay's text/plain chunks) is left alone.
func compressibleType(contentType string) bool {
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	switch media {
	case "application/json", "application/yaml", "application/x-yaml", "text/csv":
		return true
	}
	return false
}

var gzipWriters = sync.Pool{New: func() any {
	zw, _ := gzip.NewWriterLevel(io.Discard, 5)
	return zw
}}

// gzipPath says which requests may have their answers compressed at all.
//
// The answer is no for anything that carries a credential. Compressing a body
// that mixes a secret with text an attacker can influence is the BREACH
// pattern, and the session endpoint, token creation and the sign-in handshake
// are exactly the bodies that do. Excluding them costs nothing -- they are a
// few hundred bytes -- and leaves the question of whether a given body is safe
// to compress to the only people who can answer it: whoever adds an endpoint
// here has to decide, rather than inherit a yes.
func gzipPath(p string) bool {
	for _, prefix := range []string{
		"/api/v1/auth/", "/api/v1/tokens", "/api/v1/join-tokens", "/api/v1/users",
		"/api/v1/mcp-clients", "/api/v1/mcp-connections", "/api/v1/backups",
		"/api/v1/agent/", "/api/v1/events",
	} {
		if strings.HasPrefix(p, prefix) {
			return false
		}
	}
	return true
}

// compressResponses gzips JSON and CSV answers to GET requests that ask for it.
//
// It decides at the moment the handler commits to a status, from what the
// handler said about the body: the media type and, for the handlers that know
// it (writeJSON does), the length. Everything else -- a stream, an archive, a
// body declared too small to be worth it, a response that is already encoded --
// passes through untouched, still flushable, which is what keeps the event stream and
// the log relay working behind it.
func compressResponses(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !gzipPath(r.URL.Path) || !acceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		// A cache that stores the identity body must not serve it to a client
		// that wanted gzip, nor the reverse.
		w.Header().Add("Vary", "Accept-Encoding")
		cw := &compressWriter{ResponseWriter: w}
		defer cw.finish()
		next.ServeHTTP(cw, r)
	})
}

type compressWriter struct {
	http.ResponseWriter
	zw      *gzip.Writer
	decided bool
}

func (c *compressWriter) decide(status int) {
	if c.decided {
		return
	}
	c.decided = true
	h := c.Header()
	if status != http.StatusOK || h.Get("Content-Encoding") != "" || !compressibleType(h.Get("Content-Type")) {
		return
	}
	// A declared length is the best evidence of size, and a body under the
	// threshold is not worth the encoder. A handler that declared none wrote a
	// body it did not buffer -- the CSV and YAML exports, which are among the
	// largest answers there are -- and the media types this accepts are never
	// streams (those are text/event-stream and the log relay's text/plain, which
	// compressibleType refuses), so it is compressed from the first byte.
	if declared := h.Get("Content-Length"); declared != "" {
		if n, err := strconv.Atoi(declared); err != nil || n < gzipMinSize {
			return
		}
	}
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	zw := gzipWriters.Get().(*gzip.Writer)
	zw.Reset(c.ResponseWriter)
	c.zw = zw
}

func (c *compressWriter) WriteHeader(status int) {
	c.decide(status)
	c.ResponseWriter.WriteHeader(status)
}

func (c *compressWriter) Write(p []byte) (int, error) {
	c.decide(http.StatusOK)
	if c.zw != nil {
		return c.zw.Write(p)
	}
	return c.ResponseWriter.Write(p)
}

// Flush keeps http.Flusher working for the responses that pass through, and
// is what http.ResponseController reaches for.
func (c *compressWriter) Flush() {
	c.decide(http.StatusOK)
	if c.zw != nil {
		_ = c.zw.Flush()
	}
	// Through the controller, not a type assertion: the writers above this one
	// (the access log's, for one) reach the connection by Unwrap and do not
	// all implement http.Flusher themselves.
	_ = http.NewResponseController(c.ResponseWriter).Flush()
}

// Unwrap lets http.NewResponseController reach the connection beneath, for
// the deadlines and hijacks the streaming handlers ask for.
func (c *compressWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (c *compressWriter) finish() {
	if c.zw == nil {
		return
	}
	_ = c.zw.Close()
	gzipWriters.Put(c.zw)
	c.zw = nil
}

// gzipBody compresses a static body once, at the best ratio, for the UI's
// files: they are served thousands of times and compressed once.
func gzipBody(body []byte) []byte {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(body)
	_ = zw.Close()
	return buf.Bytes()
}
