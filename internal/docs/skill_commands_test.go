package docs

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// A skill is read whole by an agent that then types what it names, so a
// command in a skill has to be one the binary has: an agent that follows a
// skill into a verb that does not exist learns to distrust the whole thing.
// The list comes from the binary itself, never from a copy.
func commandNames(t *testing.T) (names, groups map[string]bool) {
	t.Helper()
	cmd := exec.Command("go", "run", "../../cmd/zoomies", "commands", "--output", "json")
	cmd.Env = append(os.Environ(), "ZOOMIES_URL=", "ZOOMIES_TOKEN=")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("zoomies commands: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("zoomies commands: %v", err)
	}
	var cmds []struct {
		Name        string `json:"name"`
		Subcommands []struct {
			Name string `json:"name"`
		} `json:"subcommands"`
	}
	if err := json.Unmarshal(out, &cmds); err != nil {
		t.Fatal(err)
	}
	names, groups = map[string]bool{}, map[string]bool{}
	for _, c := range cmds {
		names["zoomies "+c.Name] = true
		if len(c.Subcommands) > 0 {
			groups["zoomies "+c.Name] = true
		}
		for _, s := range c.Subcommands {
			names["zoomies "+c.Name+" "+s.Name] = true
		}
	}
	return names, groups
}

// A command is named in backticks; "zoomies is not installed" in prose is a
// sentence, not a verb.
var skillCommand = regexp.MustCompile("`zoomies ([a-z][a-z-]*)(?: ([a-z][a-z-]*))?[^`]*`")

// unknownCommands is every backticked "zoomies ..." in text that the binary
// does not have. A second word after a group's name is a verb and must be one
// of the group's; after a command that takes arguments it is an argument
// ("zoomies why job_1") and only the command is checked.
func unknownCommands(text string, names, groups map[string]bool) []string {
	var out []string
	for _, m := range skillCommand.FindAllStringSubmatch(text, -1) {
		one := "zoomies " + m[1]
		if m[2] != "" && groups[one] {
			if !names[one+" "+m[2]] {
				out = append(out, strings.Trim(m[0], "` "))
			}
			continue
		}
		if !names[one] {
			out = append(out, strings.Trim(m[0], "` "))
		}
	}
	return out
}

func TestEveryCommandASkillNamesIsOneTheBinaryHas(t *testing.T) {
	names, groups := commandNames(t)
	for _, s := range readSkills(t) {
		for _, bad := range unknownCommands(s.text, names, groups) {
			t.Errorf("skills/%s: names %q, which the binary does not have", s.dir, bad)
		}
	}
}

// The matcher is the whole point of the test above, so it is held to the
// failure it exists to catch: a verb mistyped under a group, which the
// group's own existence must not excuse.
func TestAMistypedSubcommandIsCaughtAndAnArgumentIsNot(t *testing.T) {
	names := map[string]bool{"zoomies jobs": true, "zoomies jobs advice": true, "zoomies why": true}
	groups := map[string]bool{"zoomies jobs": true}
	for text, want := range map[string][]string{
		"run `zoomies jobs advice` first":                 nil,
		"run `zoomies jobs advise` first":                 {"zoomies jobs advise"},
		"then `zoomies jobs frobnicate --output json`":    {"zoomies jobs frobnicate --output json"},
		"and `zoomies why job_1`, an argument not a verb": nil,
		"never `zoomies frob`":                            {"zoomies frob"},
		"zoomies is not installed, in prose":              nil,
	} {
		if got := unknownCommands(text, names, groups); !slices.Equal(got, want) {
			t.Errorf("%q: unknown = %v, want %v", text, got, want)
		}
	}
}

// The kennel skill is the one that may touch a workflow file, so the rules it
// works under are pinned as sentences: the command it runs, what comes first,
// that it never edits unasked, and that it says nothing left the machine.
func TestTheKennelSkillRunsTheOfflineCheckAndEditsNothingUnasked(t *testing.T) {
	var kennel *skill
	for _, s := range readSkills(t) {
		if s.dir == "zoomies-kennel" {
			kennel = &s
		}
	}
	if kennel == nil {
		t.Fatal("no skills/zoomies-kennel")
	}
	for _, want := range []string{
		"zoomies kennel check --output json --prompts",
		"Never edit a workflow file without a selection",
		"security first",
		"nothing left the machine",
	} {
		if !strings.Contains(kennel.text, want) {
			t.Errorf("the kennel skill lacks %q", want)
		}
	}
}

// The install documentation names both skills by the names their folders
// have, and the path an installer takes, so a renamed skill or a moved
// repository fails here before a reader follows a stale line.
func TestTheInstallDocsNameBothSkillsAndThePath(t *testing.T) {
	for _, page := range []string{"../../README.md", "../../docs/cli.md"} {
		raw, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"eyupio/zoomies", "skills/zoomies-kennel", "skills/zoomies"} {
			if !strings.Contains(string(raw), want) {
				t.Errorf("%s does not say %q", page, want)
			}
		}
	}
}
