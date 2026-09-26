package api

import (
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/controller"
)

// FleetStatus is the name-free projection GET /api/v1/status returns. It is
// the controller's type, aliased, for the reason every view is: one shape,
// defined once.
type FleetStatus = controller.FleetStatus

// statusAllowed answers the one question the three status routes share:
// may this request read the fleet's status at all? It writes the refusal
// itself, so a handler that gets false has nothing left to do.
//
// Off is a 404 rather than a 403 on every route, because a default install
// should look exactly as it did before this existed: a 403 would say there
// is something here to be refused.
func (s *Server) statusAllowed(w http.ResponseWriter, r *http.Request, api bool) bool {
	switch s.cfg().Status.Mode {
	case config.StatusPublic:
		return true
	case config.StatusAuthenticated:
		if info := infoFrom(r.Context()); info != nil && info.identity != nil {
			return true
		}
		if api {
			unauthorized(w, "the fleet status is only shown to people signed in to this controller; sign in, or ask an administrator to set status.mode to public")
			return false
		}
		// The page and the badge are fetched by a browser or an image proxy
		// that cannot sign in; a 401 page is all either could do with it.
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "sign in to this controller to see the fleet status", http.StatusUnauthorized)
		return false
	default:
		if api {
			apiNotFound(w, r)
		} else {
			http.NotFound(w, r)
		}
		return false
	}
}

// handleStatus serves the projection.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !s.statusAllowed(w, r, true) {
		return
	}
	st, err := s.ctrl.Status(r.Context())
	if err != nil {
		s.internal(w, r, "computing the fleet status", err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// statusPlaceholder stands in for the status page in a binary built without
// the UI, so the route still answers what it would in a real build rather
// than a 404 that reads as "status.mode is off".
const statusPlaceholder = `<!doctype html><meta charset="utf-8"><title>Fleet status</title>` +
	`<p>` + placeholderMarker + ` into this binary. The status is at <a href="/api/v1/status">/api/v1/status</a>.</p>`

// handleStatusPage serves /status, the page's own Vite entry.
//
// It is its own document rather than a route of the app, so that a reader
// with no account never downloads the app shell, and so that nothing on it
// can open /api/v1/events: every frame on that stream is a resource view,
// and every resource view carries names.
func (s *Server) handleStatusPage(w http.ResponseWriter, r *http.Request) {
	if !s.statusAllowed(w, r, false) {
		return
	}
	page, err := fs.ReadFile(s.spa.files, "status.html")
	if err != nil {
		page = []byte(statusPlaceholder)
	}
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(page)
}

// badgeColours are the three states on the light theme's status tokens
// (--z-danger, --z-pending, --z-idle in web/src/lib/styles/tokens.css). A
// badge is an image fetched through somebody else's proxy, so it cannot read
// a stylesheet and cannot follow a theme; it carries the values itself.
var badgeColours = map[controller.FleetState]string{
	controller.FleetHealthy:  "#0f7a3d",
	controller.FleetDegraded: "#9a6100",
	controller.FleetBlocked:  "#bd2018",
}

// handleStatusBadge serves /status.svg, the state as a README badge on the
// docs/badge.svg pattern: self-contained, no font, no second request, text
// pinned to a width so it lays out the same wherever it is drawn.
func (s *Server) handleStatusBadge(w http.ResponseWriter, r *http.Request) {
	if !s.statusAllowed(w, r, false) {
		return
	}
	st, err := s.ctrl.Status(r.Context())
	if err != nil {
		s.internal(w, r, "computing the fleet status", err)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	// Short enough that a README shows a blocked fleet within a minute, long
	// enough that an image proxy is not asking every time a page is viewed.
	// Only a public status may sit in a shared cache: one fetched by somebody
	// signed in would otherwise be handed by a proxy or CDN to the next
	// request for the same URL, signed in or not, without it ever reaching
	// statusAllowed.
	if s.cfg().Status.Mode == config.StatusPublic {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int((30*time.Second).Seconds())))
	} else {
		w.Header().Set("Cache-Control", "private, no-store")
	}
	_, _ = w.Write([]byte(statusBadge(st.State)))
}

// statusBadge draws the badge. The state is one of three fixed words, so
// nothing from a row reaches the SVG and nothing needs escaping.
func statusBadge(state controller.FleetState) string {
	colour, ok := badgeColours[state]
	if !ok {
		colour = badgeColours[controller.FleetDegraded]
	}
	word := string(state)
	// Roughly Verdana 11's advance, which is what textLength is pinned to.
	right := 7*len(word) + 12
	const left = 44
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="fleet: %s">`, left+right, word)
	fmt.Fprintf(&b, `<title>fleet: %s</title>`, word)
	fmt.Fprintf(&b, `<clipPath id="r"><rect width="%d" height="20" rx="3" fill="#fff"/></clipPath>`, left+right)
	fmt.Fprintf(&b, `<g clip-path="url(#r)"><rect width="%d" height="20" fill="#080808"/><rect x="%d" width="%d" height="20" fill="%s"/></g>`, left, left, right, colour)
	b.WriteString(`<g fill="#fff" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" font-size="11" text-rendering="geometricPrecision">`)
	fmt.Fprintf(&b, `<text x="6" y="14" textLength="%d" lengthAdjust="spacingAndGlyphs">fleet</text>`, left-12)
	fmt.Fprintf(&b, `<text x="%d" y="14" textLength="%d" lengthAdjust="spacingAndGlyphs">%s</text>`, left+6, right-12, word)
	b.WriteString(`</g></svg>`)
	return b.String()
}
