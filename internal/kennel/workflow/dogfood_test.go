package workflow

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var pinnedRef = regexp.MustCompile(`@[a-f0-9]{40}`)

// This repository holds its own workflows to several of these rules, in
// internal/docs: every action is pinned to a commit, no workflow grants write
// to every job. The parser is held to the same files: every one readable, no
// unpinned reference, no secret on a command line, no checkout of a pull
// request's head under pull_request_target; and with one pin stripped from a
// file, exactly one unpinned reference, placed at the step it belongs to. If
// the parser and the repository's own tests ever disagree, one of them is
// wrong.
func TestKennelAgreesWithTheRepositorysOwnWorkflows(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "..", ".github", "workflows", "*.yml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no workflows found: %v", err)
	}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		facts, err := Inspect(data)
		if err != nil {
			t.Errorf("%s: the parser refused this repository's own workflow: %v", filepath.Base(path), err)
			continue
		}
		if n := len(facts.FirstPartyUnpinned) + len(facts.OtherUnpinned); n != 0 {
			t.Errorf("%s: %d unpinned references, and this repository pins every action", filepath.Base(path), n)
		}
		if len(facts.SecretOnCommandLine) != 0 || len(facts.TargetCheckoutPRHead) != 0 {
			t.Errorf("%s: a secret on a command line or a checkout of a pull request's head: %+v", filepath.Base(path), facts)
		}
		loc := pinnedRef.FindIndex(data)
		if loc == nil {
			continue
		}
		stripped := string(data[:loc[0]]) + "@v1" + string(data[loc[1]:])
		line := stepLine(string(data), strings.Count(string(data[:loc[0]]), "\n"))
		after, err := Inspect([]byte(stripped))
		if err != nil {
			t.Errorf("%s: stripping a pin made the file unreadable: %v", filepath.Base(path), err)
			continue
		}
		unpinned := append(after.FirstPartyUnpinned, after.OtherUnpinned...)
		if len(unpinned) != 1 || unpinned[0].Line != line {
			t.Errorf("%s: with the pin on line %d stripped, unpinned = %v, want that one line", filepath.Base(path), line, unpinned)
		}
	}
}

var stepStart = regexp.MustCompile(`^\s*- `)

// stepLine is the 1-based line of the step that holds line index i: a
// finding points at a step's first key, which is where a person looks for it,
// and the pin may sit on a later key of the same step.
func stepLine(data string, i int) int {
	lines := strings.Split(data, "\n")
	for ; i > 0; i-- {
		if stepStart.MatchString(lines[i]) {
			break
		}
	}
	return i + 1
}
