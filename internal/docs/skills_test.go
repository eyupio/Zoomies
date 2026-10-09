package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// skillsDir holds the skills a coding agent installs. It is not .claude/skills,
// which holds the skills for working on this repository: a user's agent must never
// pick up babysit or steward, and a maintainer's must not be handed these.
const skillsDir = "../../skills"

// maxSkillLines keeps a skill short enough to be read whole every time it is
// loaded. What does not fit belongs in a file beside it that the skill points to.
const maxSkillLines = 200

type skill struct {
	dir  string
	text string
	meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
}

func readSkills(t *testing.T) []skill {
	t.Helper()
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		t.Fatal(err)
	}
	var skills []skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(skillsDir, e.Name(), "SKILL.md"))
		if err != nil {
			t.Errorf("skills/%s has no SKILL.md: an agent installs a skill by that file", e.Name())
			continue
		}
		// Git on Windows ends the lines of a checkout with \r\n; every check below
		// is on \n.
		s := skill{dir: e.Name(), text: strings.ReplaceAll(string(raw), "\r\n", "\n")}
		front, ok := frontmatter(s.text)
		if !ok {
			t.Errorf("skills/%s/SKILL.md does not start with a --- frontmatter block", e.Name())
			continue
		}
		if err := yaml.Unmarshal([]byte(front), &s.meta); err != nil {
			t.Errorf("skills/%s/SKILL.md: frontmatter is not YAML: %v", e.Name(), err)
			continue
		}
		skills = append(skills, s)
	}
	if len(skills) == 0 {
		t.Fatal("found no skills; the test is looking in the wrong place")
	}
	return skills
}

// frontmatter returns what lies between the opening and closing --- lines.
func frontmatter(text string) (string, bool) {
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return "", false
	}
	front, _, ok := strings.Cut(rest, "\n---\n")
	return front, ok
}

// The installers that read a skills folder choose a skill by its name and decide
// when to load it from its description alone, so both must be there and say
// something: a name that differs from its folder is a skill that installs under
// one name and is asked for under another, and a description that does not say
// when to use the skill is a skill nothing ever loads.
func TestEverySkillNamesItselfAndSaysWhenToUseIt(t *testing.T) {
	for _, s := range readSkills(t) {
		if s.meta.Name != s.dir {
			t.Errorf("skills/%s: name is %q, and it must be the folder's name", s.dir, s.meta.Name)
		}
		if !regexp.MustCompile(`^[a-z][a-z0-9-]*$`).MatchString(s.meta.Name) {
			t.Errorf("skills/%s: name %q is not lower-case words joined by hyphens", s.dir, s.meta.Name)
		}
		d := strings.TrimSpace(s.meta.Description)
		if d == "" {
			t.Errorf("skills/%s: no description", s.dir)
		}
		if len(d) > 1024 {
			t.Errorf("skills/%s: description is %d characters; installers cut it at 1024", s.dir, len(d))
		}
		if !strings.Contains(d, "Use when") {
			t.Errorf("skills/%s: the description does not say \"Use when\", which is what makes an agent load the skill", s.dir)
		}
		if strings.ContainsAny(d, "<>") {
			t.Errorf("skills/%s: the description has angle brackets, which some installers reject", s.dir)
		}
	}
}

func TestEverySkillFitsInTwoHundredLines(t *testing.T) {
	for _, s := range readSkills(t) {
		if n := strings.Count(s.text, "\n"); n > maxSkillLines {
			t.Errorf("skills/%s/SKILL.md is %d lines; the limit is %d. Put the detail in a file beside it and link to it", s.dir, n, maxSkillLines)
		}
	}
}

// A skill is copied into somebody else's project or home folder, so a link out
// of its own folder points at nothing there, and a link inside it that names a
// file that was never written points at nothing anywhere.
func TestEverySkillLinkStaysInsideTheSkillAndPointsAtAFile(t *testing.T) {
	link := regexp.MustCompile(`\]\(([^)\s]+)\)`)
	for _, s := range readSkills(t) {
		for _, m := range link.FindAllStringSubmatch(s.text, -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			path := filepath.Join(skillsDir, s.dir, strings.SplitN(target, "#", 2)[0])
			rel, err := filepath.Rel(filepath.Join(skillsDir, s.dir), path)
			if err != nil || strings.HasPrefix(rel, "..") {
				t.Errorf("skills/%s links to %s, which is outside the skill", s.dir, target)
				continue
			}
			if _, err := os.Stat(path); err != nil {
				t.Errorf("skills/%s links to %s, which does not exist", s.dir, target)
			}
		}
	}
}

// A skill describes how to work on somebody else's machine, so a path from the
// one it was written on is wrong for everyone.
func TestNoSkillNamesAPathFromTheMachineItWasWrittenOn(t *testing.T) {
	machine := regexp.MustCompile(`(/home/[a-z]|/Users/[A-Za-z]|/root/|C:\\Users|/tmp/claude|/private/var)`)
	for _, s := range readSkills(t) {
		if m := machine.FindString(s.text); m != "" {
			t.Errorf("skills/%s/SKILL.md names a path from one machine, %q", s.dir, m)
		}
	}
}
