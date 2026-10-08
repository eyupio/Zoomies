package provenance

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The tree test cannot see a pull request's commit messages, so CI runs this
// one with the messages in hand: PROVENANCE_MESSAGES_FILE names a file holding
// them, one commit after another, or PROVENANCE_COMMIT_RANGE names a git range
// for a checkout with the history to show it. Without either it skips, and
// says so, rather than passing for nothing.
func TestTheCommitRangeIsClean(t *testing.T) {
	var text string
	switch {
	case os.Getenv("PROVENANCE_MESSAGES_FILE") != "":
		b, err := os.ReadFile(os.Getenv("PROVENANCE_MESSAGES_FILE"))
		if err != nil {
			t.Fatal(err)
		}
		text = string(b)
	case os.Getenv("PROVENANCE_COMMIT_RANGE") != "":
		out, err := exec.Command("git", "log", "--format=%H%n%B", os.Getenv("PROVENANCE_COMMIT_RANGE")).Output()
		if err != nil {
			t.Fatalf("git log %s: %v", os.Getenv("PROVENANCE_COMMIT_RANGE"), err)
		}
		text = string(out)
	default:
		t.Skip("set PROVENANCE_MESSAGES_FILE or PROVENANCE_COMMIT_RANGE to check commit messages")
	}
	if hits := Scan("commit messages", strings.NewReader(text)); len(hits) != 0 {
		for _, h := range hits {
			t.Errorf("line %d of the commit messages carries a third-party name (%s); reword the commit", h.Line, h.Term)
		}
	}
}
