package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/eyupio/zoomies/internal/catalog"
	"github.com/eyupio/zoomies/internal/store"
)

// An agent fetches the catalog before it has looked at anything else, so it
// has to answer for a viewer and whether or not Kennel Club is on: the checks
// are part of what the fleet can say, not part of what it is saying now.
func TestTheCatalogIsServedToAViewerWithKennelClubOff(t *testing.T) {
	h := newHarness(t)
	_, viewer := h.user("viewer", store.RoleViewer)
	resp := h.do(request{method: http.MethodGet, path: "/api/v1/catalog", cookie: viewer})
	resp.mustStatus(t, http.StatusOK, "catalog")
	if !bytes.Equal(resp.body, catalog.Embedded()) {
		t.Fatalf("the route does not serve the embedded catalog byte for byte (%d vs %d bytes)", len(resp.body), len(catalog.Embedded()))
	}
	var doc catalog.Catalog
	if err := json.Unmarshal(resp.body, &doc); err != nil {
		t.Fatal(err)
	}
	checks := 0
	for _, e := range doc.Entries {
		if e.Kind == catalog.KindCheck {
			checks++
		}
	}
	if checks == 0 {
		t.Fatal("no check entries while Kennel Club is off")
	}
}

// The catalog changes with a release and nothing else, and an agent asks for
// it at the start of every session, so a client that remembers the tag is
// answered with nothing rather than 300 KiB.
func TestTheCatalogAnswers304ToItsOwnETag(t *testing.T) {
	h := newHarness(t)
	_, viewer := h.user("viewer", store.RoleViewer)
	first := h.do(request{method: http.MethodGet, path: "/api/v1/catalog", cookie: viewer})
	first.mustStatus(t, http.StatusOK, "catalog")
	tag := first.header.Get("ETag")
	if tag == "" {
		t.Fatal("no ETag on the catalog")
	}
	again := h.do(request{method: http.MethodGet, path: "/api/v1/catalog", cookie: viewer, headers: map[string]string{"If-None-Match": tag}})
	again.mustStatus(t, http.StatusNotModified, "catalog with a matching tag")
}
