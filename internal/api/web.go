package api

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

// webdist holds the built operator UI.
//
// The directory is produced by `make ui` (Vite writes straight into it) and the
// Makefile keeps an index.html placeholder there for builds that skip the UI,
// so this embed never fails on a fresh checkout. The all: prefix is what pulls
// in dot-prefixed files, which a bundler will happily emit and which the
// default embed rules would silently drop.
//
//go:embed all:webdist
var webdist embed.FS

// placeholderMarker appears in the Makefile's stub index.html. Its presence is
// how the server knows the UI was not built, which /meta reports and the
// startup line says out loud -- otherwise "the dashboard is blank" is a mystery
// with no message attached to it.
const placeholderMarker = "The UI was not built"

// immutableMaxAge is how long a hashed asset may be cached. Vite content-hashes
// every file under assets/, so a changed file is a changed URL and a year is
// safe; index.html is the one file that is not hashed and is never cached.
const immutableMaxAge = 365 * 24 * time.Hour

// originToken is the placeholder the page's sharing tags carry in place of this
// controller's own address.
//
// og:url and og:image have to be absolute for a link preview to render at all:
// the service fetching the page is not a browser and has no base to resolve a
// relative path against. The address is a deployment fact rather than a build
// one, so it is substituted when the controller starts. A controller with no
// server.external_url set is left with relative paths, which is the honest
// answer -- it does not know its own address, and inventing one would produce a
// preview pointing at the wrong host.
const originToken = "__ZOOMIES_ORIGIN__"

// spaHandler serves the embedded single-page application.
type spaHandler struct {
	files fs.FS
	// index is index.html, held in memory because every unmatched route serves
	// it and it is a couple of kilobytes.
	index []byte
	// built is false when only the Makefile's placeholder is embedded.
	built   bool
	modTime time.Time
	hashes  []string
}

// newSPAHandler prepares the embedded UI for serving. externalURL is the
// address operators reach this controller on, and may be empty.
func newSPAHandler(externalURL string) (*spaHandler, error) {
	sub, err := fs.Sub(webdist, "webdist")
	if err != nil {
		return nil, fmt.Errorf("api: the embedded UI directory is unusable: %w", err)
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return nil, fmt.Errorf("api: the embedded UI has no index.html; run `make ui` (or `make build-nogui` for the placeholder): %w", err)
	}
	// The substitution touches attribute values only, never a script body, so
	// the inline-script hashes below still describe what the browser executes.
	index = bytes.ReplaceAll(index, []byte(originToken),
		[]byte(strings.TrimRight(strings.TrimSpace(externalURL), "/")))

	h := &spaHandler{
		files:   sub,
		index:   index,
		built:   !strings.Contains(string(index), placeholderMarker),
		modTime: buildTime(),
	}
	h.hashes = inlineScriptHashes(index)
	return h, nil
}

// inlineScriptHashes returns the CSP source expressions for the page's inline
// scripts.
//
// Computing them from the embedded file rather than hard-coding them means the
// policy follows the UI build: if the theme bootstrap changes by a byte, the
// hash changes with it, and neither a stale allow-list nor an 'unsafe-inline'
// escape hatch is left behind.
func (h *spaHandler) inlineScriptHashes() []string { return h.hashes }

// The two groups are the opening tag's attributes and the script body.
var inlineScriptRE = regexp.MustCompile(`(?is)<script([^>]*)>(.*?)</script\s*>`)
var srcAttrRE = regexp.MustCompile(`(?is)\ssrc\s*=`)

func inlineScriptHashes(html []byte) []string {
	var out []string
	for _, m := range inlineScriptRE.FindAllSubmatch(html, -1) {
		if srcAttrRE.Match(m[1]) {
			// An external script is covered by 'self'; only the inline body of
			// a script needs a hash.
			continue
		}
		body := m[2]
		if len(strings.TrimSpace(string(body))) == 0 {
			continue
		}
		sum := sha256.Sum256(body)
		out = append(out, "sha256-"+base64.StdEncoding.EncodeToString(sum[:]))
	}
	return out
}

// ServeHTTP serves a built asset, or index.html for anything that looks like a
// client-side route.
func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, r)
		return
	}

	name := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	switch name {
	case ".", "/", "index.html":
		h.serveIndex(w, r)
		return
	}

	f, err := h.files.Open(name)
	if err == nil {
		defer f.Close()
		if st, serr := f.Stat(); serr == nil && !st.IsDir() {
			if seeker, ok := f.(io.ReadSeeker); ok {
				h.setCacheHeaders(w, name)
				http.ServeContent(w, r, path.Base(name), h.modTime, seeker)
				return
			}
		}
	}

	// Not a file we have. A path with an extension was asking for one -- a
	// missing image, a stale asset URL from a cached page -- and answering it
	// with the index page would produce a JavaScript file that is secretly
	// HTML, which fails much later and much more confusingly than a 404.
	if path.Ext(name) != "" {
		notFound(w, "no such file: /"+name)
		return
	}
	h.serveIndex(w, r)
}

func (h *spaHandler) serveIndex(w http.ResponseWriter, r *http.Request) {
	// Never cached: it names the hashed assets, so a stale copy points a
	// browser at files that no longer exist after an upgrade.
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "index.html", h.modTime, bytes.NewReader(h.index))
}

func (h *spaHandler) setCacheHeaders(w http.ResponseWriter, name string) {
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d, immutable", int(immutableMaxAge.Seconds())))
		return
	}
	// Everything else at the root -- the favicon, a manifest -- is not hashed,
	// so it is revalidated rather than held.
	w.Header().Set("Cache-Control", "public, max-age=300")
}

// buildTime is the modification time reported for embedded files.
//
// Embedding does not preserve timestamps, so there is nothing truthful to
// report; a fixed, non-zero time makes conditional requests work consistently
// (the ETag-less If-Modified-Since path) without pretending each file was
// written when the process started.
func buildTime() time.Time { return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) }
