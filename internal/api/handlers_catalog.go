package api

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/eyupio/zoomies/internal/catalog"
)

// catalogETag is computed once: the catalog is compiled into the binary and
// changes only with it.
var catalogETag = func() string {
	sum := sha256.Sum256(catalog.Embedded())
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}()

// handleCatalog answers GET /api/v1/catalog with the embedded catalog verbatim,
// so a controller serves exactly what the docs site publishes for its release.
// An agent fetches it at the start of every session, so a client that
// remembers the tag is answered with nothing rather than the whole document.
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("ETag", catalogETag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == catalogETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(catalog.Embedded())
}
