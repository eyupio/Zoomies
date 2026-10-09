package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/installer"
)

// The update helper is offered by the installer, and in this release the web UI
// can use it to update the controller and nothing else, so a page that
// describes the offer without saying so would promise an update button that is
// not there on an agent host, or deny the one that is on the controller's. Every
// page that describes the helper carries the installer's own sentence, and a
// later release changes it in them all at once: this test is how they are found.
//
// The join is the one place that knows the host is an agent's, so its row says
// the host-only sentence and must not say the controller's.
func TestEveryPageThatOffersTheUpdateHelperSaysWhatTheWebUICanDoWithItYet(t *testing.T) {
	read := func(page string) string {
		t.Helper()
		body, err := os.ReadFile(filepath.Join("..", "..", "docs", page))
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	for _, page := range []string{"upgrading.md", "quickstart.md"} {
		if !strings.Contains(read(page), installer.UpdateHelperControllerOnlyYet) {
			t.Errorf("docs/%s describes the update helper without saying %q", page, installer.UpdateHelperControllerOnlyYet)
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
	for _, prefix := range []string{"| `updates helper install`", "| `zoomies init", "| `zoomies upgrade"} {
		line := row(prefix)
		if !strings.Contains(line, installer.UpdateHelperControllerOnlyYet) {
			t.Errorf("the docs/cli.md row %s describes the update helper without saying %q", prefix, installer.UpdateHelperControllerOnlyYet)
		}
		if strings.Contains(line, installer.UpdateHelperAgentHostNotUsedYet) {
			t.Errorf("the docs/cli.md row %s is not an agent's, yet says %q", prefix, installer.UpdateHelperAgentHostNotUsedYet)
		}
	}
	join := row("| `zoomies agent join")
	if !strings.Contains(join, installer.UpdateHelperAgentHostNotUsedYet) {
		t.Errorf("the docs/cli.md agent join row describes the update helper without saying %q", installer.UpdateHelperAgentHostNotUsedYet)
	}
	if strings.Contains(join, installer.UpdateHelperControllerOnlyYet) {
		t.Errorf("the docs/cli.md agent join row promises the controller's button to an agent host")
	}
}
