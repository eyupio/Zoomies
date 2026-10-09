package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// shownCommands returns every place a skill shows a command: a code span, or a
// line of a fenced block, that starts with `zoomies`.
func shownCommands(text string) []string {
	var shown []string
	for _, m := range regexp.MustCompile("`(zoomies [^`]+)`").FindAllStringSubmatch(text, -1) {
		shown = append(shown, m[1])
	}
	fenced := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
			continue
		}
		if fenced && strings.HasPrefix(line, "zoomies ") {
			shown = append(shown, line)
		}
	}
	return shown
}

// A skill is read whole by an agent that then types what it names. The docs
// package holds every command a skill names to the binary; this holds the flags
// to it. A flag renamed in the binary and not in a skill is an agent that runs
// the right command with a flag it does not take, gets a usage error, and
// reports it as if it were about the repository or the fleet.
func TestEveryFlagASkillPassesIsOneItsCommandTakes(t *testing.T) {
	var docs []commandDoc
	out, _ := runCLI(t, "commands", "--output", "json")
	if err := json.Unmarshal([]byte(out), &docs); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	help := map[string]string{}
	for _, d := range docs {
		help["zoomies "+d.Name] = d.Help
		for _, s := range d.Subcommands {
			help["zoomies "+d.Name+" "+s.Name] = s.Help
		}
	}

	files, err := filepath.Glob("../../skills/*/SKILL.md")
	if err != nil || len(files) == 0 {
		t.Fatalf("found no skills; the test is looking in the wrong place: %v", err)
	}
	flag := regexp.MustCompile(`--[a-z][a-z-]*`)
	checked := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		// A Windows checkout ends the lines with \r\n, and the fenced-block
		// reader above is on \n.
		text := strings.ReplaceAll(string(raw), "\r\n", "\n")
		for _, command := range shownCommands(text) {
			// The command is the longest run of words that is one the binary has;
			// what follows it is arguments and flags.
			words := strings.Fields(command)
			leaf := ""
			for n := len(words); n >= 2; n-- {
				if _, ok := help[strings.Join(words[:n], " ")]; ok {
					leaf = strings.Join(words[:n], " ")
					break
				}
			}
			if leaf == "" {
				// Whether the command exists is the docs package's question.
				continue
			}
			for _, f := range flag.FindAllString(command, -1) {
				checked++
				if !regexp.MustCompile(`(?m)^\s+` + regexp.QuoteMeta(f) + `(=|\s)`).MatchString(help[leaf]) {
					t.Errorf("%s passes %s to `%s`, which takes no such flag", file, f, leaf)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no skill passes a flag to a command; the test is looking in the wrong place")
	}
}

// The kennel skill names the fields of the JSON `zoomies kennel check` prints
// and says what each means. A field renamed in the command and not in the skill
// is an agent that reads a key which is no longer there and finds nothing, which
// looks exactly like a clean repository, so the fields it names are the ones the
// command prints.
func TestTheKennelSkillNamesFieldsTheCheckStillPrints(t *testing.T) {
	raw, err := os.ReadFile("../../skills/zoomies-kennel/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")

	dir := t.TempDir()
	workflows := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(workflows, 0o755); err != nil {
		t.Fatal(err)
	}
	// A job with no timeout, so there is a finding to carry every field a
	// finding carries.
	const workflow = "on: push\njobs:\n  build:\n    runs-on: self-hosted\n    steps:\n      - run: echo hello\n"
	if err := os.WriteFile(filepath.Join(workflows, "ci.yml"), []byte(workflow), 0o644); err != nil {
		t.Fatal(err)
	}
	// Exit status 4 is the command saying it found something, which is what
	// this arranged; any other is a failure of the test's own set-up.
	out, _, code := runCLIFailing(t, "kennel", "check", "--output", "json", "--prompts", dir)
	if code != 4 {
		t.Fatalf("exit code = %d, want 4 (findings)\n%s", code, out)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	var findings []map[string]json.RawMessage
	if err := json.Unmarshal(doc["findings"], &findings); err != nil || len(findings) == 0 {
		t.Fatalf("no findings to read the fields of: %v\n%s", err, out)
	}

	keys := func(m map[string]json.RawMessage) []string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return ks
	}
	for _, want := range []string{"files", "findings", "not_checked", "unreadable"} {
		if _, ok := doc[want]; !ok {
			t.Errorf("the skill reads `%s`, which the command does not print; it prints %v", want, keys(doc))
		}
		if !strings.Contains(text, "`"+want+"`") {
			t.Errorf("the skill does not mention the field `%s`", want)
		}
	}
	// A finding's own fields: `prompt` is the one the skill tells an agent to
	// follow as its task, and evidence is where the file and line are.
	for _, want := range []string{"prompt", "evidence"} {
		if _, ok := findings[0][want]; !ok {
			t.Errorf("the skill reads a finding's %s, which the command does not print; a finding has %v", want, keys(findings[0]))
		}
		if !strings.Contains(text, want) {
			t.Errorf("the skill does not mention a finding's %s", want)
		}
	}
}
