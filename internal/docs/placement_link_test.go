package docs

import (
	"os"
	"strings"
	"testing"
)

// The Connect GitHub dialog stops an operator whose controller GitHub cannot
// reach and sends them to the page about where a controller goes. A docs edit
// that renames that heading would leave the link landing at the top of a long
// page, so the heading and the link that names it are kept together here.
func TestTheConnectDialogsPlacementLinkLandsOnItsHeading(t *testing.T) {
	links, err := os.ReadFile("../../web/src/lib/links.ts")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(links), "/home-lab/#where-the-controller-goes") {
		t.Error("web/src/lib/links.ts no longer links CONTROLLER_PLACEMENT_URL to /home-lab/#where-the-controller-goes")
	}

	page, err := os.ReadFile("../../docs/home-lab.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "\n## Where the controller goes\n") {
		t.Error("docs/home-lab.md has no \"Where the controller goes\" heading, and the Connect GitHub dialog links to its anchor")
	}
}
