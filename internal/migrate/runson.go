// Package migrate rewrites the `runs-on` lines in GitHub Actions workflows so
// that a repository's jobs run on a Zoomies pool instead of on GitHub's hosted
// runners.
//
// It is the engine behind the migration wizard: the wizard reads a
// repository's workflows, asks this package what it would change, shows the
// operator the diff, and -- only then -- opens a pull request with the result.
//
// # Why this is not a YAML round trip
//
// The obvious implementation is to unmarshal the workflow, replace the value
// and marshal it back. It produces an unreviewable pull request: comments are
// dropped, key order is normalised, quoting style changes, block scalars are
// reflowed, and anchors are expanded. A workflow is a file a team has written
// and rewritten by hand, and a migration that reformats all of it hides the
// one line that actually changed.
//
// So the rewriting is line-based and surgical. It finds `runs-on` keys, edits
// the value, and leaves every other byte -- indentation, comments, trailing
// whitespace, line endings -- exactly as it found it. What it cannot rewrite
// with confidence it refuses to touch and reports as a skip, because a wrong
// rewrite sends a job to a runner that does not exist and the workflow simply
// hangs.
package migrate

import (
	"fmt"
	"regexp"
	"strings"
)

// Rewrite is one `runs-on` value this package changed.
type Rewrite struct {
	// Line is the 1-based line number in the original file.
	Line int `json:"line"`
	// Job is the workflow job the line belongs to, as far as indentation can
	// tell. Empty when it could not be attributed.
	Job string `json:"job,omitempty"`
	// From and To are the `runs-on` values, rendered as they appear in the
	// file: "ubuntu-latest", "[self-hosted, linux]".
	From string `json:"from"`
	To   string `json:"to"`
}

// Skip is a `runs-on` this package deliberately left alone, and why.
//
// A skip is not a failure. It is the honest answer for a workflow the operator
// has to look at: a matrix expression whose values are computed elsewhere, a
// job already pointing at a self-hosted runner, a hosted label nobody mapped.
type Skip struct {
	Line   int    `json:"line"`
	Job    string `json:"job,omitempty"`
	Value  string `json:"value"`
	Reason string `json:"reason"`
}

// Result is what rewriting one file produced.
type Result struct {
	// Content is the rewritten file. It equals the input when nothing changed.
	Content string `json:"-"`
	// Rewrites and Skips are what to show the operator.
	Rewrites []Rewrite `json:"rewrites"`
	Skips    []Skip    `json:"skips"`
}

// Changed reports whether the file needs to be committed at all.
func (r Result) Changed() bool { return len(r.Rewrites) > 0 }

// Mapping decides what a hosted `runs-on` label becomes.
//
// The zero Mapping rewrites nothing, which is the safe default: a wizard that
// guessed would open pull requests pointing jobs at pools that do not exist.
type Mapping struct {
	// Labels maps one hosted label ("ubuntu-latest") to the runs-on value that
	// replaces it ("zoomies-linux-x64"). Keys are compared lowercased.
	Labels map[string]string
}

// To returns the replacement for a hosted label, and whether there is one.
func (m Mapping) To(label string) (string, bool) {
	if m.Labels == nil {
		return "", false
	}
	to, ok := m.Labels[strings.ToLower(strings.TrimSpace(label))]
	if !ok || strings.TrimSpace(to) == "" {
		return "", false
	}
	return to, true
}

// HostedLabels are the runner labels GitHub itself provides, as of the runner
// images published for github.com.
//
// The list matters for two reasons. It is what the wizard offers to map, so an
// operator sees "ubuntu-latest" rather than every string in the file; and it is
// what tells a label GitHub owns from one an organisation invented, which is
// the difference between a job that can be migrated and a job that is already
// pointed somewhere deliberate.
//
// It is a prefix list, not an exact one: GitHub keeps adding sizes and
// versions ("ubuntu-22.04-arm", "windows-11-arm", the larger-runner names an
// organisation configures), and a wizard that only recognised the exact names
// it shipped with would go quietly blind as they change.
var hostedPrefixes = []string{"ubuntu-", "windows-", "macos-", "macOS-"}

// IsHostedLabel reports whether label is one of GitHub's own runner labels.
func IsHostedLabel(label string) bool {
	l := strings.ToLower(strings.TrimSpace(label))
	if l == "" {
		return false
	}
	for _, p := range hostedPrefixes {
		if strings.HasPrefix(l, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// runsOnKey matches a `runs-on:` key and splits it into the parts that must be
// preserved byte for byte.
//
//	1: everything up to and including the colon and the space after it
//	2: the value, with any trailing comment left in group 3
//	3: a trailing comment, including the whitespace before it
//
// The key is matched case-insensitively because GitHub accepts "Runs-On" and
// people write it.
var runsOnKey = regexp.MustCompile(`^(\s*(?:-\s+)?(?i:runs-on)\s*:[ \t]*)(.*?)([ \t]*#.*)?$`)

// jobKey matches the key of a job inside `jobs:`: two spaces of indentation by
// convention, but any indentation deeper than `jobs:` in practice.
var jobKey = regexp.MustCompile(`^(\s*)([A-Za-z0-9_][A-Za-z0-9_.-]*)\s*:\s*(#.*)?$`)

// expression matches a GitHub Actions expression, which is the one value this
// package will not rewrite: `${{ matrix.os }}` is decided somewhere else in the
// file, or in a reusable workflow, or by a repository variable.
var expression = regexp.MustCompile(`\$\{\{`)

// File rewrites every `runs-on` in one workflow file.
//
// The file is returned unchanged when nothing matched, so a caller can commit
// only what it must.
func File(content string, m Mapping) Result {
	lines := splitLines(content)
	res := Result{}
	tracker := newJobTracker()

	for i := 0; i < len(lines); i++ {
		tracker.observe(lines[i].text)

		match := runsOnKey.FindStringSubmatch(lines[i].text)
		if match == nil {
			continue
		}
		prefix, value, comment := match[1], match[2], match[3]
		lineNo := i + 1
		job := tracker.job

		// `runs-on:` with nothing after it introduces a block sequence:
		//
		//	runs-on:
		//	  - self-hosted
		//	  - linux
		//
		// which is the one form whose value is not on this line.
		if strings.TrimSpace(value) == "" {
			consumed, rewritten, outcome := rewriteBlockSequence(lines, i, m)
			switch {
			case outcome.err != "":
				res.Skips = append(res.Skips, Skip{Line: lineNo, Job: job, Value: outcome.from, Reason: outcome.err})
			case rewritten:
				res.Rewrites = append(res.Rewrites, Rewrite{Line: lineNo, Job: job, From: outcome.from, To: outcome.to})
			}
			i += consumed
			continue
		}

		to, reason := rewriteValue(value, m)
		switch {
		case reason != "":
			res.Skips = append(res.Skips, Skip{Line: lineNo, Job: job, Value: strings.TrimSpace(value), Reason: reason})
		case to != value:
			lines[i].text = prefix + to + comment
			res.Rewrites = append(res.Rewrites, Rewrite{
				Line: lineNo, Job: job,
				From: strings.TrimSpace(value), To: strings.TrimSpace(to),
			})
		}
	}

	res.Content = joinLines(lines)
	return res
}

// rewriteValue maps an inline `runs-on` value. It returns the replacement, or
// a reason the value was left alone.
func rewriteValue(value string, m Mapping) (string, string) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return value, "the runs-on value is empty"
	}
	if expression.MatchString(trimmed) {
		return value, "the value is a ${{ }} expression, so what it resolves to is decided elsewhere in this workflow"
	}

	if inner, ok := flowSequence(trimmed); ok {
		items := splitFlowItems(inner)
		if len(items) == 0 {
			return value, "the runs-on list is empty"
		}
		return rewriteLabelSet(items, m)
	}
	return rewriteLabelSet([]string{trimmed}, m)
}

// rewriteLabelSet maps a whole `runs-on` label set at once.
//
// A set is migrated as a unit rather than label by label. "runs-on:
// [ubuntu-latest]" and "runs-on: [self-hosted, linux, x64]" are both one
// decision about where a job runs, and rewriting half of one -- leaving, say,
// "x64" beside a Zoomies label -- would produce a set that matches nothing.
func rewriteLabelSet(items []string, m Mapping) (string, string) {
	var (
		hosted  []string
		unquote = func(s string) string { return strings.Trim(strings.TrimSpace(s), `"'`) }
	)
	for _, raw := range items {
		item := unquote(raw)
		if item == "" {
			continue
		}
		if IsHostedLabel(item) {
			hosted = append(hosted, item)
			continue
		}
		// Anything that is not one of GitHub's own labels is a deliberate
		// choice somebody already made: a self-hosted fleet, a larger runner
		// group, a label from another vendor. Migrating it would be guessing.
		if strings.EqualFold(item, "self-hosted") {
			return "", "this job already runs on a self-hosted runner"
		}
		return "", fmt.Sprintf("%q is not one of GitHub's hosted labels, so this job is already pointed somewhere deliberate", item)
	}
	if len(hosted) == 0 {
		return "", "no GitHub-hosted label to migrate"
	}
	if len(hosted) > 1 {
		return "", fmt.Sprintf("%d hosted labels on one job (%s) is not a combination GitHub runs, so it is left for a person to read",
			len(hosted), strings.Join(hosted, ", "))
	}
	to, ok := m.To(hosted[0])
	if !ok {
		return "", fmt.Sprintf("%q is not mapped to a pool", hosted[0])
	}
	return to, ""
}

// blockOutcome is what rewriting a block sequence produced.
type blockOutcome struct {
	from string
	to   string
	err  string
}

// rewriteBlockSequence handles the multi-line form of runs-on. It returns how
// many extra lines it consumed, whether it changed anything, and what to
// report.
//
// The rewrite collapses the sequence onto the `runs-on:` line, because a single
// branded label is what replaces it and a one-item block sequence spread over
// two lines would be a strange thing to leave behind.
func rewriteBlockSequence(lines []line, at int, m Mapping) (int, bool, blockOutcome) {
	keyIndent := indentOf(lines[at].text)
	var (
		items    []string
		consumed int
	)
	for j := at + 1; j < len(lines); j++ {
		text := lines[j].text
		if strings.TrimSpace(text) == "" || strings.HasPrefix(strings.TrimSpace(text), "#") {
			// A blank line or a comment inside the sequence: stop rather than
			// guess, since collapsing would delete it.
			break
		}
		if indentOf(text) <= keyIndent {
			break
		}
		item := strings.TrimSpace(text)
		if !strings.HasPrefix(item, "- ") && item != "-" {
			break
		}
		items = append(items, strings.TrimSpace(strings.TrimPrefix(item, "-")))
		consumed = j - at
	}
	if len(items) == 0 {
		return 0, false, blockOutcome{err: "the runs-on key has no value on its line and no list under it"}
	}
	from := "[" + strings.Join(items, ", ") + "]"
	if expression.MatchString(from) {
		return consumed, false, blockOutcome{from: from,
			err: "the value is a ${{ }} expression, so what it resolves to is decided elsewhere in this workflow"}
	}
	to, reason := rewriteLabelSet(items, m)
	if reason != "" {
		return consumed, false, blockOutcome{from: from, err: reason}
	}

	// Collapse: the key line carries the value, and the item lines go.
	match := runsOnKey.FindStringSubmatch(lines[at].text)
	comment := ""
	if match != nil {
		comment = match[3]
	}
	lines[at].text = strings.TrimRight(lines[at].text[:strings.Index(lines[at].text, ":")+1], " \t") + " " + to + comment
	for j := at + 1; j <= at+consumed; j++ {
		lines[j].dropped = true
	}
	return consumed, true, blockOutcome{from: from, to: to}
}

// HostedLabelsIn returns every GitHub-hosted label a workflow's runs-on lines
// name, in the order they first appear.
//
// This is what the wizard's mapping step is built from: an operator maps the
// labels their own workflows actually use, not the twenty GitHub publishes.
func HostedLabelsIn(content string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(raw string) {
		l := strings.ToLower(strings.Trim(strings.TrimSpace(raw), `"'`))
		if l == "" || seen[l] || !IsHostedLabel(l) {
			return
		}
		seen[l] = true
		out = append(out, l)
	}

	lines := splitLines(content)
	for i := 0; i < len(lines); i++ {
		match := runsOnKey.FindStringSubmatch(lines[i].text)
		if match == nil {
			continue
		}
		value := strings.TrimSpace(match[2])
		if value == "" {
			keyIndent := indentOf(lines[i].text)
			for j := i + 1; j < len(lines); j++ {
				item := strings.TrimSpace(lines[j].text)
				if item == "" || indentOf(lines[j].text) <= keyIndent || !strings.HasPrefix(item, "-") {
					break
				}
				add(strings.TrimPrefix(item, "-"))
			}
			continue
		}
		if inner, ok := flowSequence(value); ok {
			for _, item := range splitFlowItems(inner) {
				add(item)
			}
			continue
		}
		add(value)
	}
	return out
}

// ---------------------------------------------------------------------------
// Line handling
// ---------------------------------------------------------------------------

// line is one line of the file plus what rewriting decided about it. The
// original ending is kept so that a CRLF file stays a CRLF file: a migration
// that flipped every line ending would show up as a whole-file diff.
type line struct {
	text    string
	ending  string
	dropped bool
}

func splitLines(content string) []line {
	if content == "" {
		return nil
	}
	raw := strings.Split(content, "\n")
	out := make([]line, 0, len(raw))
	for i, text := range raw {
		// The split leaves a final empty element for a file ending in a
		// newline; that element is the absence of a last line, not a line.
		if i == len(raw)-1 && text == "" {
			break
		}
		l := line{text: text, ending: "\n"}
		if strings.HasSuffix(text, "\r") {
			l.text, l.ending = strings.TrimSuffix(text, "\r"), "\r\n"
		}
		if i == len(raw)-1 && !strings.HasSuffix(content, "\n") {
			l.ending = ""
		}
		out = append(out, l)
	}
	return out
}

func joinLines(lines []line) string {
	var b strings.Builder
	for _, l := range lines {
		if l.dropped {
			continue
		}
		b.WriteString(l.text)
		b.WriteString(l.ending)
	}
	return b.String()
}

func indentOf(s string) int {
	n := 0
	for _, r := range s {
		if r != ' ' && r != '\t' {
			break
		}
		n++
	}
	return n
}

// flowSequence unwraps "[a, b]" into "a, b".
func flowSequence(s string) (string, bool) {
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		return s[1 : len(s)-1], true
	}
	return "", false
}

// splitFlowItems splits "a, 'b, c', d" on the commas that separate items,
// leaving quoted commas alone.
func splitFlowItems(s string) []string {
	var (
		out   []string
		cur   strings.Builder
		quote rune
	)
	flush := func() {
		if item := strings.TrimSpace(cur.String()); item != "" {
			out = append(out, item)
		}
		cur.Reset()
	}
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			cur.WriteRune(r)
		case r == ',':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// ---------------------------------------------------------------------------
// Job attribution
// ---------------------------------------------------------------------------

// jobTracker names the job a `runs-on` belongs to by watching indentation.
//
// It is deliberately approximate: it exists so the wizard can say "build" next
// to a change rather than "line 34", and a wrong guess costs a label in a
// review screen, not a wrong rewrite. Nothing downstream branches on it.
type jobTracker struct {
	inJobs     bool
	jobsIndent int
	jobIndent  int
	job        string
}

func newJobTracker() *jobTracker { return &jobTracker{jobsIndent: -1, jobIndent: -1} }

func (t *jobTracker) observe(text string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return
	}
	indent := indentOf(text)

	if !t.inJobs {
		if trimmed == "jobs:" {
			t.inJobs, t.jobsIndent = true, indent
		}
		return
	}
	// A key at or above `jobs:` ends the block.
	if indent <= t.jobsIndent {
		t.inJobs, t.job, t.jobIndent = false, "", -1
		if trimmed == "jobs:" {
			t.inJobs, t.jobsIndent = true, indent
		}
		return
	}
	// The first key inside `jobs:` fixes the indentation every job sits at;
	// anything deeper belongs to the job that is already open.
	if t.jobIndent == -1 || indent == t.jobIndent {
		if m := jobKey.FindStringSubmatch(text); m != nil {
			t.jobIndent, t.job = indent, m[2]
		}
	}
}
