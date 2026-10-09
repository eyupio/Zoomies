// Package agentguidance inspects instruction files without executing their
// contents. Its repairs are proposals, derived from repository evidence.
package agentguidance

import (
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const MaxBytes = 64 << 10
const MaxFiles = 32

type File struct {
	Path    string
	SHA     string
	Mode    string
	Content string
}

type Issue struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	SHA  string `json:"sha"`
	Line int    `json:"line"`
}

type Change struct {
	Path        string `json:"path"`
	Mode        string `json:"mode"`
	PreviousSHA string `json:"previous_sha"`
	Before      string `json:"before"`
	Content     string `json:"content"`
}

// Recognised includes scoped guidance. A single supported file is sufficient;
// a project is never obliged to adopt a particular directory layout.
func Recognised(p string) bool {
	return path.Base(p) == "AGENTS.md" || path.Base(p) == "CLAUDE.md" || path.Base(p) == "GEMINI.md" || p == ".github/copilot-instructions.md" || p == ".zoomies/AI_CONTEXT.md"
}

func EntryPoint(p string) bool { return Recognised(p) && p != ".zoomies/AI_CONTEXT.md" }

var plainPath = regexp.MustCompile(`^[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*$`)

// SafePath is also the gate used before a path is kept beside Kennel findings.
func SafePath(p string) bool {
	return len(p) <= 240 && plainPath.MatchString(p) && path.Clean(p) == p && p != "." && p != ".." && !strings.HasPrefix(p, "../")
}

var inlineCode = regexp.MustCompile("`+[^`]*`+")
var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

var markdownLink = regexp.MustCompile(`!?\[[^\]\n]*\]\(([^\s)]+)\)`)

type reference struct {
	target   string
	line     int
	imported bool
}

func references(f File) []reference {
	var out []reference
	fence := ""
	content := htmlComment.ReplaceAllStringFunc(f.Content, func(comment string) string { return strings.Repeat("\n", strings.Count(comment, "\n")) })
	for i, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			mark := trim[:3]
			if fence == "" {
				fence = mark
			} else if fence == mark {
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		if strings.HasPrefix(trim, "@") && !strings.ContainsAny(trim, " \t") && (path.Base(f.Path) == "CLAUDE.md" || strings.HasSuffix(trim, ".md")) {
			out = append(out, reference{target: trim[1:], line: i + 1, imported: true})
		}
		for _, m := range markdownLink.FindAllStringSubmatch(inlineCode.ReplaceAllString(line, ""), -1) {
			out = append(out, reference{target: m[1], line: i + 1})
		}
	}
	return out
}

func localTarget(from, target string) (string, bool) {
	u, err := url.Parse(target)
	if err != nil {
		return "", false
	}
	if u.IsAbs() || u.Host != "" || u.Path == "" {
		return "", false
	}
	decoded, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		return "", false
	}
	if strings.HasPrefix(decoded, "/") {
		return "", true
	}
	p := path.Clean(path.Join(path.Dir(from), decoded))
	if p == ".." || strings.HasPrefix(p, "../") || strings.ContainsAny(p, "\\\x00\r\n") {
		return "", true
	}
	return p, true
}

// Inspect uses the pinned inventory to check references, never network fetches.
// Missing targets and missing guidance are reported only with a complete tree.
func Inspect(files []File, inventory []string, complete bool) []Issue {
	files = slices.Clone(files)
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	present := map[string]bool{}
	for _, p := range inventory {
		present[p] = true
	}
	var out []Issue
	entry := false
	for _, p := range inventory {
		if EntryPoint(p) {
			entry = true
		}
	}
	imports := map[string][]string{}
	for _, f := range files {
		if !Recognised(f.Path) {
			continue
		}
		if EntryPoint(f.Path) {
			entry = true
		}
		if len(f.Content) > MaxBytes || !utf8.ValidString(f.Content) || strings.ContainsRune(f.Content, 0) || strings.TrimSpace(f.Content) == "" {
			out = append(out, Issue{Kind: "unreadable", Path: f.Path, SHA: f.SHA})
			continue
		}
		for _, ref := range references(f) {
			target, local := localTarget(f.Path, ref.target)
			if !local {
				continue
			}
			if complete && !present[target] {
				out = append(out, Issue{Kind: "broken_reference", Path: f.Path, SHA: f.SHA, Line: ref.line})
			}
			if ref.imported && present[target] {
				imports[f.Path] = append(imports[f.Path], target)
			}
		}
	}
	// Imports execute a traversal. Cycles are unhealthy even when every target
	// exists. Markdown links are navigation and do not participate in it.
	for _, f := range files {
		var reaches func(string, map[string]bool) bool
		reaches = func(p string, seen map[string]bool) bool {
			if seen[p] {
				return false
			}
			seen[p] = true
			for _, target := range imports[p] {
				if target == f.Path || reaches(target, seen) {
					return true
				}
			}
			return false
		}
		if reaches(f.Path, map[string]bool{}) {
			out = append(out, Issue{Kind: "broken_reference", Path: f.Path, SHA: f.SHA})
		}
	}
	for _, f := range files {
		if f.Path != "CLAUDE.md" && f.Path != ".claude/CLAUDE.md" {
			continue
		}
		for _, canonical := range files {
			if canonical.Path == "AGENTS.md" && normalise(f.Content) != "" && normalise(f.Content) == normalise(canonical.Content) {
				out = append(out, Issue{Kind: "duplicated", Path: f.Path, SHA: f.SHA})
			}
		}
	}
	if !entry && complete {
		out = append(out, Issue{Kind: "missing"})
	}
	return out
}

func normalise(s string) string { return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n")) }

// Plan limits edits to absent entry points, exact duplicate adapters and
// unambiguous imports of recognised guidance. Other findings need a person.
func Plan(files []File, inventory []string) []Change {
	issues := Inspect(files, inventory, true)
	present := map[string]bool{}
	for _, p := range inventory {
		present[p] = true
	}
	var out []Change
	for _, issue := range issues {
		if issue.Kind == "missing" {
			if present["AGENTS.md"] || present["CLAUDE.md"] {
				continue
			}
			content := "# Agent guidance\n\nRead the repository README and contribution guide, when present, before changing code. Follow existing conventions and include relevant validation in the pull request.\n"
			if present[".zoomies/AI_CONTEXT.md"] {
				content += "\n## Repository context\n\nRead [.zoomies/AI_CONTEXT.md](.zoomies/AI_CONTEXT.md) before using prepared repository context.\n"
			}
			commands := evidencedCommands(files)
			if len(commands) > 0 {
				content += "\n## Commands\n\nThese commands are declared in repository files. Review their prerequisites and behaviour before running them.\n\n```sh\n" + strings.Join(commands, "\n") + "\n```\n"
			}
			out = append(out, Change{Path: "AGENTS.md", Mode: "100644", Content: content}, Change{Path: "CLAUDE.md", Mode: "100644", Content: "@AGENTS.md\n"})
			break
		}
	}
	for _, f := range files {
		if !Recognised(f.Path) || len(f.Content) > MaxBytes || !utf8.ValidString(f.Content) || strings.ContainsRune(f.Content, 0) {
			continue
		}
		content := f.Content
		newline := "\n"
		if strings.Contains(content, "\r\n") {
			newline = "\r\n"
		}
		for _, issue := range issues {
			if issue.Path == f.Path && issue.Kind == "duplicated" {
				prefix := ""
				if f.Path == ".claude/CLAUDE.md" {
					prefix = "../"
				}
				content = "@" + prefix + "AGENTS.md" + newline
			}
		}
		if content == f.Content && path.Base(f.Path) == "CLAUDE.md" {
			lines := strings.Split(content, "\n")
			for _, ref := range references(f) {
				if !ref.imported {
					continue
				}
				target, local := localTarget(f.Path, ref.target)
				if !local || present[target] || !Recognised(path.Base(ref.target)) {
					continue
				}
				var matches []string
				for _, p := range inventory {
					if path.Base(p) == path.Base(ref.target) && p != f.Path && SafePath(p) {
						matches = append(matches, p)
					}
				}
				if len(matches) != 1 {
					continue
				}
				original := lines[ref.line-1]
				prefix := original[:len(original)-len(strings.TrimLeft(original, " \t"))]
				ending := ""
				if strings.HasSuffix(original, "\r") {
					ending = "\r"
				}
				lines[ref.line-1] = prefix + "@" + relative(path.Dir(f.Path), matches[0]) + ending
			}
			content = strings.Join(lines, "\n")
		}
		if content != f.Content {
			out = append(out, Change{Path: f.Path, Mode: f.Mode, PreviousSHA: f.SHA, Before: f.Content, Content: content})
		}
	}
	// A repaired import must not create a cycle through another adapter.
	proposed := slices.Clone(files)
	paths := slices.Clone(inventory)
	for _, change := range out {
		found := false
		for i := range proposed {
			if proposed[i].Path == change.Path {
				proposed[i].Content = change.Content
				found = true
			}
		}
		if !found {
			proposed = append(proposed, File{Path: change.Path, Content: change.Content})
			paths = append(paths, change.Path)
		}
	}
	cyclic := map[string]bool{}
	for _, issue := range Inspect(proposed, paths, true) {
		if issue.Kind == "broken_reference" && issue.Line == 0 {
			cyclic[issue.Path] = true
		}
	}
	out = slices.DeleteFunc(out, func(change Change) bool { return cyclic[change.Path] })
	slices.SortFunc(out, func(a, b Change) int { return strings.Compare(a.Path, b.Path) })
	return out
}

func relative(dir, target string) string {
	if dir == "." {
		return target
	}
	a, b := strings.Split(dir, "/"), strings.Split(target, "/")
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return strings.Repeat("../", len(a)-i) + strings.Join(b[i:], "/")
}

var makeTarget = regexp.MustCompile(`(?m)^(build|test|lint|check|dev):(?:[ \t]|$)`)
var commandName = regexp.MustCompile(`^(build|test|lint|check|dev)$`)

func evidencedCommands(files []File) []string {
	var out []string
	for _, f := range files {
		if f.Path == "Makefile" {
			for _, m := range makeTarget.FindAllStringSubmatch(f.Content, -1) {
				out = append(out, "make "+m[1])
			}
		}
		if f.Path == "package.json" {
			var manifest struct {
				Scripts map[string]string `json:"scripts"`
			}
			if json.Unmarshal([]byte(f.Content), &manifest) == nil {
				for name := range manifest.Scripts {
					if commandName.MatchString(name) {
						out = append(out, "npm run "+name)
					}
				}
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
