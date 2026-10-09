package main

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const kennelSkill = "../../skills/zoomies-kennel/SKILL.md"

// The zoomies-kennel skill tells an agent to run one command with named flags
// and to read named fields out of what it prints. A command or flag renamed in
// the binary and not in the skill is an agent that runs something that does not
// exist and reports the error as if it were about the repository, so every
// command the skill shows is a real one and every flag it passes is one that
// command takes.
func TestTheKennelSkillNamesOnlyRealCommandsAndFlags(t *testing.T) {
	raw, err := os.ReadFile(kennelSkill)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")

	var docs []commandDoc
	out, _ := runCLI(t, "commands", "--output", "json")
	if err := json.Unmarshal([]byte(out), &docs); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	help := map[string]string{}
	for _, d := range docs {
		// A group is a command too: "zoomies kennel" is something the binary answers,
		// and a skill may say a build lacks it. It takes no flags of its own.
		help["zoomies "+d.Name] = d.Help
		for _, s := range d.Subcommands {
			help["zoomies "+d.Name+" "+s.Name] = s.Help
		}
	}

	// Each place the skill shows a command: a code span, or a line in a fenced
	// block, that starts with `zoomies`.
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
	if len(shown) == 0 {
		t.Fatal("the skill shows no command; the test is looking in the wrong place")
	}

	flag := regexp.MustCompile(`--[a-z][a-z-]*`)
	for _, command := range shown {
		// The command is the longest run of words that is one the binary has.
		words := strings.Fields(command)
		leaf := ""
		for n := len(words); n >= 2; n-- {
			if _, ok := help[strings.Join(words[:n], " ")]; ok {
				leaf = strings.Join(words[:n], " ")
				break
			}
		}
		if leaf == "" {
			t.Errorf("the skill shows `%s`, and no such command exists", command)
			continue
		}
		for _, f := range flag.FindAllString(command, -1) {
			if !regexp.MustCompile(`(?m)^\s+` + regexp.QuoteMeta(f) + `(=|\s)`).MatchString(help[leaf]) {
				t.Errorf("the skill passes %s to `%s`, which takes no such flag", f, leaf)
			}
		}
	}
}

// The skill names the fields of the JSON `zoomies kennel check` prints and tells
// an agent what each means. A field renamed in the command and not here is an
// agent that reads a key that is no longer there and finds nothing, which looks
// exactly like a clean repository.
func TestTheKennelSkillNamesFieldsTheCheckStillPrints(t *testing.T) {
	raw, err := os.ReadFile(kennelSkill)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")

	dir := t.TempDir()
	workflows := dir + "/.github/workflows"
	if err := os.MkdirAll(workflows, 0o755); err != nil {
		t.Fatal(err)
	}
	// A job with no timeout, so there is a finding to carry every field a
	// finding carries.
	if err := os.WriteFile(workflows+"/ci.yml", []byte("on: push\njobs:\n  build:\n    runs-on: self-hosted\n    steps:\n      - run: echo hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Exit status 4 is the command saying it found something, which is what
	// this test arranged; any other is a failure.
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
	// Every field the skill says to read must be one the command prints.
	for _, want := range []string{"findings", "files", "unreadable", "not_checked"} {
		if _, ok := doc[want]; !ok {
			t.Errorf("the skill reads `%s`, which the command does not print; it prints %v", want, keys(doc))
		}
		if !strings.Contains(text, "`"+want+"`") {
			t.Errorf("the skill does not mention the field `%s`", want)
		}
	}
	for _, want := range []string{"code", "severity", "title", "detail", "fix", "evidence", "prompt"} {
		if _, ok := findings[0][want]; !ok {
			t.Errorf("the skill reads a finding's `%s`, which the command does not print; a finding has %v", want, keys(findings[0]))
		}
		if !strings.Contains(text, "`"+want+"`") {
			t.Errorf("the skill does not mention a finding's `%s`", want)
		}
	}
}
