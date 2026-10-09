package main

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/mcp"
)

const zoomiesSkill = "../../skills/zoomies/SKILL.md"

// readSkill returns the skill with its lines ended by \n whatever the checkout
// did to them: git on Windows turns them into \r\n, and the paragraph and
// frontmatter splits below are on \n.
func readSkill(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(zoomiesSkill)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// The zoomies skill tells a coding agent which commands it may run on its own,
// which it must ask about first and which it must leave to the user. That is only
// worth anything if it is complete: a command added to the binary and not placed
// in one of the three lists is a command the agent has no rule for, and the safe
// guess depends on the agent. So every command is in exactly one list, a test
// says so, and a release cannot go out with a gap.
func TestEveryCommandIsPlacedInExactlyOneOfTheSkillsThreeLists(t *testing.T) {
	lists := skillLists(t)

	var docs []commandDoc
	out, _ := runCLI(t, "commands", "--output", "json")
	if err := json.Unmarshal([]byte(out), &docs); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	real := map[string]bool{}
	for _, d := range docs {
		if d.Name == "commands" {
			// A generator, not something an agent runs on a fleet.
			continue
		}
		if len(d.Subcommands) == 0 {
			real["zoomies "+d.Name] = true
			continue
		}
		for _, s := range d.Subcommands {
			real["zoomies "+d.Name+" "+s.Name] = true
		}
	}

	placed := map[string][]string{}
	for list, commands := range lists {
		for _, command := range commands {
			placed[command] = append(placed[command], list)
			if !real[command] {
				t.Errorf("the skill's %q list names `%s`, which is not a command", list, command)
			}
		}
	}
	var missing []string
	for command := range real {
		switch len(placed[command]) {
		case 0:
			missing = append(missing, command)
		case 1:
		default:
			t.Errorf("`%s` is in more than one list: %v", command, placed[command])
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("these commands are in none of skills/zoomies/SKILL.md's lists (Reads, Changes, Leave to the user); place each one:\n  %s", strings.Join(missing, "\n  "))
	}
}

// A command whose only job is to change something must not be listed as a read,
// whatever the skill says in prose: the verbs that mean "change" are what a
// careless edit is most likely to file under Reads.
func TestNoCommandThatChangesTheFleetIsListedAsARead(t *testing.T) {
	changing := regexp.MustCompile(` (create|edit|delete|enable|disable|prewarm|import|drain|cordon|uncordon|apply|rerun|set|unset|revoke|rotate-secret|passwd|reset-two-step|purge|join-token|pause|resume|add|connect-proxmox|restore|import-env)$`)
	for _, command := range skillLists(t)["Reads"] {
		if changing.MatchString(command) {
			t.Errorf("`%s` is listed as a read, and its name says it changes something", command)
		}
	}
}

// The MCP tools a controller holds back are the ones an agent must ask about, so
// the skill names exactly those. A tool added without being named here would be
// one the agent calls without being told to ask.
func TestTheSkillNamesEveryMCPToolTheControllerHoldsBack(t *testing.T) {
	var paragraph string
	for _, p := range strings.Split(readSkill(t), "\n\n") {
		if strings.Contains(p, "not offered until the controller") {
			paragraph = p
		}
	}
	if paragraph == "" {
		t.Fatal("the skill has no paragraph about the MCP tools a controller holds back")
	}
	said := map[string]bool{}
	for _, m := range regexp.MustCompile("`([a-z_]+)`").FindAllStringSubmatch(paragraph, -1) {
		said[m[1]] = true
	}
	for _, name := range mcp.ActionTools() {
		if !said[name] {
			t.Errorf("the skill does not name the MCP tool %s, which the controller holds back", name)
		}
		delete(said, name)
	}
	for name := range said {
		t.Errorf("the skill names the MCP tool %s, which is not one the controller holds back", name)
	}
}

// skillLists reads the three lists out of the skill: the bullet lines under
// "### Reads", "### Changes" and "### Leave to the user", each code span that
// starts with `zoomies` being one command. Prose between the bullets is the
// skill talking to the agent, and is not part of a list.
func skillLists(t *testing.T) map[string][]string {
	t.Helper()
	span := regexp.MustCompile("`(zoomies[^`]*)`")
	lists := map[string][]string{}
	var current string
	for _, line := range strings.Split(readSkill(t), "\n") {
		switch {
		case strings.HasPrefix(line, "### Reads"):
			current = "Reads"
		case strings.HasPrefix(line, "### Changes"):
			current = "Changes"
		case strings.HasPrefix(line, "### Leave to the user"):
			current = "Leave to the user"
		case strings.HasPrefix(line, "#"):
			current = ""
		case current != "" && strings.HasPrefix(line, "* "):
			for _, m := range span.FindAllStringSubmatch(line, -1) {
				lists[current] = append(lists[current], m[1])
			}
		}
	}
	for _, name := range []string{"Reads", "Changes", "Leave to the user"} {
		if len(lists[name]) == 0 {
			t.Fatalf("skills/zoomies/SKILL.md has no %q list", name)
		}
	}
	return lists
}
