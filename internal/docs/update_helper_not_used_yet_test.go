package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/installer"
)

// The update helper is offered by the installer, but nothing in the controller
// writes a request yet, so a page that describes the offer without saying so
// would promise an update button that is not there. Every page that describes
// the helper carries the installer's own sentence, and a later release removes
// it from them all at once: this test is how they are found.
func TestEveryPageThatOffersTheUpdateHelperSaysTheWebUICannotUseItYet(t *testing.T) {
	for _, page := range []string{"upgrading.md", "cli.md", "quickstart.md"} {
		body, err := os.ReadFile(filepath.Join("..", "..", "docs", page))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), installer.UpdateHelperNotUsedYet) {
			t.Errorf("docs/%s describes the update helper without saying %q", page, installer.UpdateHelperNotUsedYet)
		}
	}
}
