package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/eyupio/zoomies/internal/store"
)

// List defaults, from the OpenAPI document's Limit and Offset parameters. They
// are stated here because the store applies its own maxima independently, and
// the two must agree or a client asking for 500 rows quietly gets 50.
const (
	defaultLimit = 50
	maxLimit     = 500
)

// page is the { items, total, limit, offset } envelope every list endpoint
// returns. The generic parameter keeps the item type in the response's own
// terms rather than as []any.
type page[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// newPage builds the envelope, normalising nil to an empty array: a UI that
// does `items.map(...)` should not have to defend against null.
func newPage[T any](items []T, total int, p store.Page) page[T] {
	if items == nil {
		items = []T{}
	}
	return page[T]{Items: items, Total: total, Limit: p.Limit, Offset: p.Offset}
}

// list is the { items } envelope of the endpoints that are not paginated.
type list[T any] struct {
	Items []T `json:"items"`
}

func newList[T any](items []T) list[T] {
	if items == nil {
		items = []T{}
	}
	return list[T]{Items: items}
}

// parsePage reads limit, offset, sort and order.
//
// An out-of-range limit is clamped rather than refused, and an unknown sort
// column is left for the store to fall back on, because a bookmarked URL from
// three versions ago should still render the page rather than an error.
func parsePage(r *http.Request) store.Page {
	q := r.URL.Query()
	p := store.Page{
		Limit:  clamp(atoiOr(q.Get("limit"), defaultLimit), 1, maxLimit),
		Offset: max(atoiOr(q.Get("offset"), 0), 0),
		Sort:   strings.TrimSpace(q.Get("sort")),
		Desc:   !strings.EqualFold(strings.TrimSpace(q.Get("order")), "asc"),
	}
	return p
}

// queryList reads a repeated query parameter, which is how the OpenAPI
// document's `style: form, explode: true` arrays arrive: ?state=idle&state=busy.
// A comma-separated single value is accepted too, because it is what somebody
// typing a URL by hand will write.
func queryList(r *http.Request, name string) []string {
	var out []string
	for _, v := range r.URL.Query()[name] {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func queryBool(r *http.Request, name string, fallback bool) bool {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return b
}

// queryBoolPtr distinguishes "not asked" from "asked for false", which is what
// the jobs filter's `unmatched` needs.
func queryBoolPtr(r *http.Request, name string) *bool {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return nil
	}
	return &b
}

func queryInt(r *http.Request, name string, fallback int) int {
	return atoiOr(r.URL.Query().Get(name), fallback)
}

// queryTime reads an RFC 3339 timestamp, returning nil when absent and an error
// naming the parameter when it is unparseable -- a filter that silently ignores
// a malformed date shows the wrong rows and says nothing.
func queryTime(r *http.Request, name string) (*time.Time, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("%s must be an RFC 3339 timestamp such as 2024-05-06T07:08:09Z, not %q", name, raw)
	}
	return &t, nil
}

// queryDuration reads a Go duration such as "1h" or "15m".
func queryDuration(r *http.Request, name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a duration such as 1h, 24h or 15m, not %q", name, raw)
	}
	return d, nil
}

// chiURLParam reads a path parameter.
func chiURLParam(r *http.Request, name string) string {
	return strings.TrimSpace(chi.URLParam(r, name))
}

func atoiOr(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---------------------------------------------------------------------------
// Request bodies
// ---------------------------------------------------------------------------

// decode reads a JSON request body into v, answering the client itself when it
// cannot and reporting whether the handler should continue.
//
// Unknown fields are rejected. A client that sends `max_runner` and gets a 200
// has been told its change was applied when it was not, which is a far worse
// outcome than being told about the typo.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeWith(w, r, v, true)
}

func decodeWith(w http.ResponseWriter, r *http.Request, v any, strict bool) bool {
	if ct := r.Header.Get("Content-Type"); ct != "" && !isJSONContentType(ct) {
		badRequest(w, "this endpoint takes a JSON body; send Content-Type: application/json")
		return false
	}
	dec := json.NewDecoder(r.Body)
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.Is(err, io.EOF):
			badRequest(w, "this endpoint needs a JSON body and the request had none")
		case errors.As(err, &maxErr):
			badRequest(w, fmt.Sprintf("the request body is larger than the %d byte limit", maxErr.Limit))
		default:
			badRequest(w, "the request body is not valid JSON for this endpoint: "+err.Error())
		}
		return false
	}
	// A second JSON value in the same body is a client bug worth naming rather
	// than silently ignoring.
	if err := dec.Decode(new(json.RawMessage)); err == nil {
		badRequest(w, "the request body contains more than one JSON value")
		return false
	} else if !errors.Is(err, io.EOF) {
		badRequest(w, "the request body has trailing content after the JSON value")
		return false
	}
	return true
}

// decodeLenient is decode for the agent protocol, where an unknown field is
// tolerated rather than refused.
//
// The two halves of that protocol are the same binary at different versions
// for as long as an upgrade is rolling across a fleet, and a newer agent that
// adds one optional field to its heartbeat must not be answered 400 by an
// older controller and stop heartbeating -- that turns a routine upgrade into
// every host going unhealthy at once. ProtocolVersion is for changes a peer
// cannot tolerate; an additive one is not that. The strictness the user API
// keeps is for a person's typo, which is a different failure with a different
// remedy.
func decodeLenient(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeWith(w, r, v, false)
}

// decodeOptional is decode for an endpoint whose body may be omitted entirely,
// which is what POST /join-tokens and PATCH bodies with every field defaulted
// are.
func decodeOptional(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.ContentLength == 0 {
		return true
	}
	return decode(w, r, v)
}

func isJSONContentType(ct string) bool {
	mediaType, _, _ := strings.Cut(ct, ";")
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

// marshalJSON is encoding/json's Marshal, wrapped so the SSE writer does not
// import it directly and every payload in this package is encoded the same way.
func marshalJSON(v any) ([]byte, error) { return json.Marshal(v) }

// ---------------------------------------------------------------------------
// Small shared shapes
// ---------------------------------------------------------------------------

// timePtr returns nil for a zero time, which is how a "type: [string, null]"
// field in the OpenAPI document is rendered.
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	out := t
	return &out
}

// millis renders a duration the way every *_ms field in the API does.
func millis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return d.Milliseconds()
}

// urlQueryEscape escapes a value for a query string. It is a thin wrapper so
// that handlers building a redirect do not import net/url for one call.
func urlQueryEscape(v string) string { return url.QueryEscape(v) }
