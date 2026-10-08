package docs

import (
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/provenance"
)

// Everything in this repository is original work that stands on its own, so
// nothing in it names the products, skills, commands or rule prefixes of
// whatever a feature was compared with. The terms are assembled from pieces in
// internal/provenance and supplied by the owner; until they are, this passes
// for the honest reason that there is nothing to look for, and the mechanism
// is tested with seeded terms in that package.
func TestTheTreeCarriesNoThirdPartyNames(t *testing.T) {
	hits, err := provenance.ScanTree("../..", provenance.DefaultSkip)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		t.Errorf("%s:%d names %q; the clean-room rule in roadmap/agent-readiness.md section 2 says why it cannot", h.Path, h.Line, strings.ToUpper(h.Term[:1])+h.Term[1:])
	}
}
