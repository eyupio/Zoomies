package docs

import (
	"os"
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
