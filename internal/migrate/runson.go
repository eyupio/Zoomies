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

	"github.com/eyupio/zoomies/internal/store"
)

// Rewrite is one `runs-on` value this package changed.
type Rewrite struct {
	// Line is the 1-based line number in the original file.
	Line int `json:"line"`
	// Job is the workflow job the line belongs to, as far as indentation can
	// tell. Empty when it could not be attributed.
	Job string `json:"job,omitempty"`
	// Label is the hosted-runner label being replaced. It is set only when the
	// value asks for exactly one, which -- together with a Job -- is what makes
	// this line something an Override can name.
	Label string `json:"label,omitempty"`
	// From and To are the `runs-on` values, rendered as they appear in the
	// file: "ubuntu-latest", "[self-hosted, linux]".
	From string `json:"from"`
	To   string `json:"to"`
	// Overridden says To came from an Override naming this job rather than from
	// the consolidated label mapping, so the review step can show which lines
	// were decided one at a time.
	Overridden bool `json:"overridden,omitempty"`
}

// Skip is a `runs-on` this package deliberately left alone, and why.
//
// A skip is not a failure. It is the honest answer for a workflow the operator
// has to look at: a matrix expression whose values are computed elsewhere, a
// job already pointing at a self-hosted runner, a hosted label nobody mapped.
type Skip struct {
	Line int    `json:"line"`
	Job  string `json:"job,omitempty"`
	// Label is the hosted-runner label this job asks for, set only when the
	// value asks for exactly one. A skip carrying both a Job and a Label is one
	// an Override can settle -- nothing is mapped to that label yet, or the
	// operator pinned this job where it is. A skip carrying neither cannot be:
	// there is no single label to replace, or no stable job to name.
	Label  string `json:"label,omitempty"`
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

// Override sends one job to a pool of its own, whatever the consolidated
// mapping says.
//
// One answer per hosted-runner label is the right default: a fleet usually does
// want every `ubuntu-latest` job on the same Linux pool, and answering that once
// is the difference between a wizard and a spreadsheet. But "usually" is not
// "always" -- one repository's integration tests need the big host, one job
// needs the pool with a GPU, one repository is being moved a job at a time --
// and the answer to those is not a second consolidated mapping. It is an
// exception, named as one.
//
// An override names exactly one place: a job, in a workflow file, in a
// repository. Nothing about it is a pattern. A pattern would quietly match more
// than the operator read in the review step, and reviewing the exact change is
// the whole reason this wizard exists.
type Override struct {
	// Repo is "owner/name", compared case-insensitively as GitHub does.
	Repo string `json:"repo"`
	// Path is the workflow file, repository-relative:
	// ".github/workflows/ci.yml".
	Path string `json:"path"`
	// Job is the job key inside that file. A `runs-on` the job tracker could not
	// attribute has no stable name, so it cannot be overridden -- there would be
	// nothing to key on that survives the file being read again at apply time.
	Job string `json:"job"`
	// To is the runs-on value this job gets. Empty means leave it on the rented
	// runner it names today: a decision the operator made, not the absence of
	// one, and it is reported with a different reason for exactly that reason.
	To string `json:"to"`
}

// Mapping decides what a hosted `runs-on` label becomes.
//
// The zero Mapping rewrites nothing, which is the safe default: a wizard that
// guessed would open pull requests pointing jobs at pools that do not exist.
type Mapping struct {
	// Labels maps one hosted label ("ubuntu-latest") to the runs-on value that
	// replaces it ("zoomies-linux-x64"). Keys are compared lowercased. This is
	// the consolidated answer, and it decides everywhere no override does.
	Labels map[string]string

	// Overrides are the exceptions to Labels, each naming one job in one file in
	// one repository.
	//
	// They are resolved by In, which narrows a Mapping to the file it is about
	// to rewrite; File reads only what In resolved. That is deliberate: a caller
	// that forgets to narrow gets the consolidated mapping, which is the answer
	// it would have got before overrides existed -- never one repository's
	// exception applied to another repository's job.
	Overrides []Override

	// jobs is the narrowed view In builds: job key -> the runs-on it was sent
	// to. A present entry holding "" means "leave this job where it is", which
	// is why presence and value are read separately everywhere below.
	jobs map[string]string
}

// Targets reports whether label is already one of the runs-on values this
// mapping points jobs at.
func (m Mapping) Targets(label string) bool {
	want := strings.ToLower(strings.TrimSpace(label))
	if want == "" {
		return false
	}
	for _, to := range m.Labels {
		if strings.EqualFold(strings.TrimSpace(to), want) {
			return true
		}
	}
	for _, to := range m.jobs {
		if strings.EqualFold(strings.TrimSpace(to), want) {
			return true
		}
	}
	return false
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

// In narrows a mapping to one workflow file, resolving the overrides that name
// a job inside it. It is what File has to be given for an override to apply.
func (m Mapping) In(repo, path string) Mapping {
	out := Mapping{Labels: m.Labels, Overrides: m.Overrides}
	for _, o := range m.Overrides {
		job := strings.TrimSpace(o.Job)
		if job == "" ||
			!strings.EqualFold(strings.TrimSpace(o.Repo), strings.TrimSpace(repo)) ||
			strings.TrimSpace(o.Path) != strings.TrimSpace(path) {
			continue
		}
		if out.jobs == nil {
			out.jobs = make(map[string]string, len(m.Overrides))
		}
		out.jobs[job] = strings.TrimSpace(o.To)
	}
	return out
}

// decide answers where one job asking for one hosted-runner label goes. An
// override wins over the consolidated mapping, including an override that says
// the job stays where it is.
func (m Mapping) decide(job, label string) (to string, overridden bool) {
	if job != "" {
		if to, ok := m.jobs[job]; ok {
			return to, true
		}
	}
	to, _ = m.To(label)
	return to, false
}

// IsHostedLabel reports whether label is one of GitHub's own runner labels.
//
// The list it consults lives in internal/store, which is where the same
// question is asked of the jobs table in SQL; this is the wizard's name for it.
// It matters here for two reasons. It is what the wizard offers to map, so an
// operator sees "ubuntu-latest" rather than every string in the file; and it is
// what tells a label GitHub owns from one an organisation invented, which is
// the difference between a job that can be migrated and a job that is already
// pointed somewhere deliberate.
func IsHostedLabel(label string) bool { return store.IsHostedLabel(label) }

// IsManagedLabel reports whether label names a runner somebody else operates:
// GitHub's own, or one of the hosted-runner vendors.
//
// This, not IsHostedLabel, is what the wizard migrates. The distinction the
// operator cares about is not "GitHub or not" but "rented or ours": a
// repository on "blacksmith-4vcpu-ubuntu-2404" is renting somebody else's
// machines by the minute, which is exactly the bill this fleet exists to
// replace, so a wizard that called that label "already pointed somewhere
// deliberate" would offer an operator nothing to migrate and no reason why.
func IsManagedLabel(label string) bool { return store.IsManagedLabel(label) }

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

// blockScalarHeader matches what follows a key (or a `- ` item marker) when the
// value is a literal or folded block scalar: `|`, `>-`, `|2+`, with an optional
// trailing comment. Anchoring on the colon or the item marker keeps
// `run: echo "a |"` and `run: a >` -- values that merely end in the character --
// from opening one.
var blockScalarHeader = regexp.MustCompile(`(?:^|:[ \t]+)[|>][-+0-9]*[ \t]*(?:#.*)?$`)

// blockScalars tells the three readers of a workflow (File, eachRunsOnLabel
// and, through File, jobTracker) which lines are the text of a script or a
// multi-line string rather than YAML structure. A `runs-on:` inside a
// `run: |` heredoc is not a key, and a `jobs:` or job name in one must not
// move the job attribution either.
//
// It is approximate in the same direction as the rest of the package: it only
// ever hides lines, and only ones indented deeper than the key that opened the
// scalar, so what it gets wrong is a rewrite not made, never a wrong one.
type blockScalars struct {
	open   bool
	parent int
}

// inBody is fed every line in order and reports whether the line is the body
// of a block scalar opened earlier. The header line itself is not body -- a
// `runs-on: |` still has to be seen as a runs-on -- but it arms the tracker.
func (b *blockScalars) inBody(text string) bool {
	if b.open {
		if strings.TrimSpace(text) == "" || indentOf(text) > b.parent {
			return true
		}
		b.open = false
	}
	if strings.HasPrefix(strings.TrimSpace(text), "#") {
		return false
	}
	// Strip the item markers to find the column the key sits at: a body has to
	// be indented deeper than that, not deeper than the dash.
	indent := indentOf(text)
	rest := text[indent:]
	dash := -1
	for strings.HasPrefix(rest, "-") && (len(rest) == 1 || rest[1] == ' ' || rest[1] == '\t') {
		dash = indent
		trimmed := strings.TrimLeft(rest[1:], " \t")
		indent += len(rest) - len(trimmed)
		rest = trimmed
	}
	if blockScalarHeader.MatchString(rest) {
		b.open = true
		b.parent = indent
		if dash >= 0 && (strings.HasPrefix(rest, "|") || strings.HasPrefix(rest, ">")) {
			// `- |`: the scalar is the item itself, so its parent is the
			// sequence, at the dash.
			b.parent = dash
		}
	}
	return false
}

// File rewrites every `runs-on` in one workflow file.
//
// The file is returned unchanged when nothing matched, so a caller can commit
// only what it must.
func File(content string, m Mapping) Result {
	lines := splitLines(content)
	res := Result{}
	tracker := newJobTracker()
	var scalars blockScalars

	for i := 0; i < len(lines); i++ {
		if scalars.inBody(lines[i].text) {
			continue
		}
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
			consumed, rewritten, out := rewriteBlockSequence(lines, i, job, m)
			switch {
			case out.reason != "":
				res.Skips = append(res.Skips, Skip{Line: lineNo, Job: job, Label: out.label, Value: out.from, Reason: out.reason})
			case rewritten:
				res.Rewrites = append(res.Rewrites, Rewrite{
					Line: lineNo, Job: job, Label: out.label,
					From: out.from, To: out.to, Overridden: out.overridden,
				})
			}
			i += consumed
			continue
		}

		out := rewriteValue(job, value, m)
		switch {
		case out.reason != "":
			res.Skips = append(res.Skips, Skip{Line: lineNo, Job: job, Label: out.label, Value: strings.TrimSpace(value), Reason: out.reason})
		case out.to != value:
			lines[i].text = prefix + out.to + comment
			res.Rewrites = append(res.Rewrites, Rewrite{
				Line: lineNo, Job: job, Label: out.label,
				From: strings.TrimSpace(value), To: strings.TrimSpace(out.to),
				Overridden: out.overridden,
			})
		}
	}

	res.Content = joinLines(lines)
	return res
}

// outcome is what deciding one `runs-on` produced: where it goes, what hosted
// label it was asking for, and -- when it goes nowhere -- why.
//
// The label is carried even for a value that was left alone, because a skip
// naming both a job and a label is one the operator can settle with an override,
// and one naming neither is not.
type outcome struct {
	// from is the value as it reads in the file. Only the block-sequence form
	// sets it; an inline value is already in the caller's hand.
	from       string
	to         string
	label      string
	reason     string
	overridden bool
}

// rewriteValue maps an inline `runs-on` value.
func rewriteValue(job, value string, m Mapping) outcome {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return outcome{reason: "the runs-on value is empty"}
	}
	if expression.MatchString(trimmed) {
		return outcome{reason: expressionReason}
	}

	if inner, ok := flowSequence(trimmed); ok {
		items := splitFlowItems(inner)
		if len(items) == 0 {
			return outcome{reason: "the runs-on list is empty"}
		}
		return rewriteLabelSet(job, items, m)
	}
	return rewriteLabelSet(job, []string{trimmed}, m)
}

// expressionReason is one sentence in two places -- an inline value and a block
// sequence reach it by different routes -- and an operator comparing two skips
// should not have to work out whether the difference in wording means anything.
const expressionReason = "the value is a ${{ }} expression, so what it resolves to is decided elsewhere in this workflow"

// rewriteLabelSet maps a whole `runs-on` label set at once.
//
// A set is migrated as a unit rather than label by label. "runs-on:
// [ubuntu-latest]" and "runs-on: [self-hosted, linux, x64]" are both one
// decision about where a job runs, and rewriting half of one -- leaving, say,
// "x64" beside a Zoomies label -- would produce a set that matches nothing.
func rewriteLabelSet(job string, items []string, m Mapping) outcome {
	var (
		hosted  []string
		unquote = func(s string) string { return strings.Trim(strings.TrimSpace(s), `"'`) }
	)
	for _, raw := range items {
		item := unquote(raw)
		if item == "" {
			continue
		}
		if IsManagedLabel(item) {
			hosted = append(hosted, item)
			continue
		}
		// Anything that is not a rented runner is a deliberate choice somebody
		// already made: a self-hosted fleet, a runner group, a label an
		// organisation invented. Migrating it would be guessing.
		if strings.EqualFold(item, "self-hosted") {
			return outcome{reason: "this job already runs on a self-hosted runner"}
		}
		if m.Targets(item) {
			return outcome{reason: fmt.Sprintf("this job already runs on %s", item)}
		}
		return outcome{reason: fmt.Sprintf("%q is not a hosted-runner label, so this job is already pointed somewhere deliberate", item)}
	}
	if len(hosted) == 0 {
		return outcome{reason: "no hosted-runner label to migrate"}
	}
	if len(hosted) > 1 {
		return outcome{reason: fmt.Sprintf("%d hosted labels on one job (%s) is not a combination that resolves to one runner, so it is left for a person to read",
			len(hosted), strings.Join(hosted, ", "))}
	}

	label := hosted[0]
	to, overridden := m.decide(job, label)
	switch {
	case to != "":
		return outcome{to: to, label: label, overridden: overridden}
	case overridden:
		// The operator looked at this job and chose to leave it where it is.
		// Saying "not mapped" here would invite them to fix something that is
		// not broken.
		return outcome{label: label, reason: fmt.Sprintf("this job is set to stay on %s", label)}
	default:
		return outcome{label: label, reason: fmt.Sprintf("%q is not mapped to a pool", label)}
	}
}

// rewriteBlockSequence handles the multi-line form of runs-on. It returns how
// many extra lines it consumed, whether it changed anything, and what to
// report.
//
// The rewrite collapses the sequence onto the `runs-on:` line, because a single
// branded label is what replaces it and a one-item block sequence spread over
// two lines would be a strange thing to leave behind.
func rewriteBlockSequence(lines []line, at int, job string, m Mapping) (int, bool, outcome) {
	keyIndent := indentOf(lines[at].text)
	var (
		items    []string
		consumed int
		// commented is the first item that carries a comment of its own.
		commented string
	)
	for j := at + 1; j < len(lines); j++ {
		text := lines[j].text
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			// A blank line or a comment line. When the sequence carries on
			// below it, the line sits inside the list, and collapsing the list
			// would delete it -- or collapse the items above it and leave the
			// ones below dangling under a flow value, which is not YAML. Stop
			// and say so rather than guess. When nothing of the sequence
			// follows, the line belongs to whatever comes next.
			if sequenceContinues(lines, j, keyIndent) {
				return consumed, false, outcome{from: "[" + strings.Join(items, ", ") + "]",
					reason: "the runs-on list has a comment or a blank line inside it, which collapsing the list would delete; move it above runs-on, or change this job by hand"}
			}
			break
		}
		if indentOf(text) <= keyIndent {
			break
		}
		if !strings.HasPrefix(trimmed, "- ") && trimmed != "-" {
			break
		}
		item, comment := splitItemComment(strings.TrimPrefix(trimmed, "-"))
		if comment != "" && commented == "" {
			commented = item
		}
		items = append(items, item)
		consumed = j - at
	}
	if len(items) == 0 {
		return 0, false, outcome{reason: "the runs-on key has no value on its line and no list under it"}
	}
	from := "[" + strings.Join(items, ", ") + "]"
	if expression.MatchString(from) {
		return consumed, false, outcome{from: from, reason: expressionReason}
	}
	out := rewriteLabelSet(job, items, m)
	out.from = from
	if out.reason != "" {
		return consumed, false, out
	}
	if commented != "" {
		// The comment was about the item, and the item is what the rewrite
		// replaces; carrying it onto the collapsed line would leave it
		// describing something that is no longer there. This is the same rule
		// as a comment line inside the list, and the same way out.
		out.reason = fmt.Sprintf("the list item %q carries a comment, which collapsing the list would delete; move it above runs-on, or change this job by hand", commented)
		return consumed, false, out
	}

	// Collapse: the key line carries the value, and the item lines go.
	match := runsOnKey.FindStringSubmatch(lines[at].text)
	comment := ""
	if match != nil {
		comment = match[3]
	}
	lines[at].text = strings.TrimRight(lines[at].text[:strings.Index(lines[at].text, ":")+1], " \t") + " " + out.to + comment
	for j := at + 1; j <= at+consumed; j++ {
		lines[j].dropped = true
	}
	return consumed, true, out
}

// sequenceContinues reports whether a block sequence under a key indented at
// keyIndent has more items after line at, looking past blank and comment
// lines, which is what decides whether such a line is inside the list or
// after it.
func sequenceContinues(lines []line, at, keyIndent int) bool {
	for j := at + 1; j < len(lines); j++ {
		trimmed := strings.TrimSpace(lines[j].text)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		return indentOf(lines[j].text) > keyIndent && (strings.HasPrefix(trimmed, "- ") || trimmed == "-")
	}
	return false
}

// splitItemComment separates a sequence item from the comment that follows it
// on the same line. YAML starts a comment at a # preceded by whitespace and
// outside quotes, so a # inside a quoted label stays part of the label. Both
// halves come back trimmed.
func splitItemComment(item string) (string, string) {
	var quote byte
	for i := 0; i < len(item); i++ {
		c := item[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#' && (i == 0 || item[i-1] == ' ' || item[i-1] == '\t'):
			return strings.TrimSpace(item[:i]), strings.TrimSpace(item[i:])
		}
	}
	return strings.TrimSpace(item), ""
}

// HostedLabelsIn returns every hosted-runner label a workflow's runs-on lines
// name, in the order they first appear -- GitHub's own and the vendor labels
// IsManagedLabel recognises.
//
// This is what the wizard's mapping step is built from: an operator maps the
// labels their own workflows actually use, not the twenty GitHub publishes.
func HostedLabelsIn(content string) []string {
	var out []string
	seen := map[string]bool{}
	eachRunsOnLabel(content, func(l string) {
		if seen[l] || !IsManagedLabel(l) {
			return
		}
		seen[l] = true
		out = append(out, l)
	})
	return out
}

// UsesAnyLabel reports whether any runs-on in content names one of labels,
// which are expected already lowercased.
//
// It is how the wizard tells a repository somebody has already migrated from
// one nobody has touched: both have nothing to rewrite, and they mean opposite
// things.
func UsesAnyLabel(content string, labels map[string]bool) bool {
	if len(labels) == 0 {
		return false
	}
	found := false
	eachRunsOnLabel(content, func(l string) {
		if labels[l] {
			found = true
		}
	})
	return found
}

// eachRunsOnLabel calls fn with every label named by a runs-on in content,
// lowercased and unquoted, in the order it appears.
//
// All three runs-on forms are read -- a bare value, a flow sequence, and a
// block sequence -- because a scan that understood only the first would report
// a repository pinned to `- self-hosted` as if nobody had touched it.
func eachRunsOnLabel(content string, fn func(label string)) {
	add := func(raw string) {
		l := strings.ToLower(strings.Trim(strings.TrimSpace(raw), `"'`))
		if l == "" {
			return
		}
		fn(l)
	}

	lines := splitLines(content)
	var scalars blockScalars
	for i := 0; i < len(lines); i++ {
		if scalars.inBody(lines[i].text) {
			continue
		}
		match := runsOnKey.FindStringSubmatch(lines[i].text)
		if match == nil {
			continue
		}
		value := strings.TrimSpace(match[2])
		if value == "" {
			keyIndent := indentOf(lines[i].text)
			for j := i + 1; j < len(lines); j++ {
				item := strings.TrimSpace(lines[j].text)
				if item == "" || strings.HasPrefix(item, "#") {
					// Not an item; the indent of the next real line says
					// whether the list goes on.
					continue
				}
				if indentOf(lines[j].text) <= keyIndent || !strings.HasPrefix(item, "-") {
					break
				}
				// The label is the item without the comment that may follow
				// it, or the wizard offers "ubuntu-latest # pinned" as a
				// label to map.
				label, _ := splitItemComment(strings.TrimPrefix(item, "-"))
				add(label)
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
// It is deliberately approximate. It exists so the wizard can say "build" next
// to a change rather than "line 34", and so an Override has something to name
// -- but a wrong guess still cannot produce a wrong rewrite, because the diff
// the operator approves is built by this same attribution. An override that
// lands on a job the tracker misread shows up as a diff on that job, on the
// screen, before anything is written.
//
// It is also why an override keys on a job name rather than a line number. The
// apply step re-reads the file from GitHub, and a name that no longer exists
// simply falls back to the consolidated mapping, where a line that has shifted
// would point somewhere new.
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
