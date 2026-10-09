package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/installer"
)

// The update helper is a grant of root that the installer offers, so every page
// that describes the offer says what it is for in the installer's own sentence:
// that the web UI can then update the host, and that its owner installs and
// removes it. A page that said less would ask for root without saying why, and
// one that said something else would drift from what the question at the
// terminal says. A release that changes what the helper is for changes the
// sentence, and this test finds every page that has to follow.
func TestEveryPageThatOffersTheUpdateHelperSaysWhatItIsFor(t *testing.T) {
	read := func(page string) string {
		t.Helper()
		body, err := os.ReadFile(filepath.Join("..", "..", "docs", page))
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	for _, page := range []string{"upgrading.md", "quickstart.md"} {
		if !strings.Contains(read(page), installer.UpdateHelperExplained) {
			t.Errorf("docs/%s describes the update helper without saying %q", page, installer.UpdateHelperExplained)
		}
	}

	cli := strings.Split(read("cli.md"), "\n")
	row := func(prefix string) string {
		t.Helper()
		for _, l := range cli {
			if strings.HasPrefix(l, prefix) {
				return l
			}
		}
		t.Fatalf("docs/cli.md has no row starting %q", prefix)
		return ""
	}
	for _, prefix := range []string{"| `updates helper install`", "| `zoomies init", "| `zoomies upgrade", "| `zoomies agent join"} {
		if line := row(prefix); !strings.Contains(line, installer.UpdateHelperExplained) {
			t.Errorf("the docs/cli.md row %s describes the update helper without saying %q", prefix, installer.UpdateHelperExplained)
		}
	}
}
