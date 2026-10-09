package docs

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// A person whose job did not run, or failed, is on the queued-job page or the
// FAQ before they are anywhere else. Both send them to the fleet's own verdict
// before a log, and the FAQ's answer about the offline check says what a
// laptop cannot check, so "nothing found" is not read as an all clear.
func TestTheQueuedJobPageAndTheFAQSendAReaderToTheVerdictFirst(t *testing.T) {
	for page, wants := range map[string][]string{
		"../../docs/queued-job.md": {"## 6. Ask the fleet why", "`zoomies why", "before it reads a log"},
		"../../docs/faq.md": {
			"## Why did my job fail, and how do I find out?",
			"## Can a coding agent check my workflow files without sending them anywhere?",
			"`zoomies kennel check", "not checked here",
		},
	} {
		raw, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(raw), want) {
				t.Errorf("%s does not say %q", page, want)
			}
		}
	}
}

// The CLI page is written by hand and the binary's command list is not, so
// the two drift unless something holds them together: every subcommand the
// binary has is named on the page, with its command, in the backticks the
// page's tables use. A bare verb does not count, because a reader searching
// for "zoomies kennel recheck" would not find "recheck" under a heading.
func TestEverySubcommandIsDocumented(t *testing.T) {
	raw, err := os.ReadFile("../../docs/cli.md")
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)
	names, groups := commandNames(t)
	var missing []string
	for name := range names {
		words := strings.Fields(name)
		if len(words) != 3 || !groups[words[0]+" "+words[1]] {
			continue
		}
		two := words[1] + " " + words[2]
		if strings.Contains(page, "`zoomies "+two) || strings.Contains(page, "`"+two+"`") || strings.Contains(page, "`"+two+" ") {
			continue
		}
		missing = append(missing, name)
	}
	slices.Sort(missing)
	if len(missing) > 0 {
		t.Errorf("these subcommands exist and are not on docs/cli.md:\n  %s", strings.Join(missing, "\n  "))
	}
}
