package offline

import (
	"os"
	"strings"
	"testing"
)

// A file is known by Git's blob SHA, and the goldens hold the SHAs of the
// fixture trees. A Windows checkout converts a text file's line endings to
// CRLF unless the repository says otherwise, which changes every fixture's
// SHA and fails the golden test on that platform alone; the attribute pins
// the fixtures to LF everywhere, as the installer's templates are pinned.
func TestTheFixtureTreesKeepTheirLineEndingsOnEveryCheckout(t *testing.T) {
	raw, err := os.ReadFile("../../../.gitattributes")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "internal/kennel/offline/testdata/") {
			continue
		}
		for _, attr := range fields[1:] {
			if attr == "eol=lf" {
				return
			}
		}
	}
	t.Fatal(".gitattributes does not pin internal/kennel/offline/testdata to eol=lf")
}
