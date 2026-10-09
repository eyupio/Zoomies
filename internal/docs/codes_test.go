// Package docs holds the tests that keep the documentation site honest about
// the code it describes.
//
// It has no non-test source of its own on purpose: nothing in the product
// imports it, and it exists so that a reference page which claims to be
// complete is checked rather than trusted. A page that goes stale is worse
// than no page, because an operator stops looking for the thing it left out.
package docs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
)

// The files that raise problems, and the page that documents them.
const (
	validatorSource = "../config/validate.go"
	// The database layer raises its own findings -- about the rows rather than
	// about what any one setting says -- and they reach the same two places as
	// the validator's, so they need the same row here.
	databaseSource = "../config/database.go"
	// The outbound address guard builds its one finding itself, because the
	// API raises it about rows the validator never sees.
	egressSource     = "../config/egress.go"
	controllerSource = "../controller/problems.go"
	viewsSource      = "../controller/views.go"
	reference        = "../../docs/problem-codes.md"
)

// controllerSources is every file whose Problem literals count as "raised by
// the controller", found by walking rather than listed, for the reason
// providerSources is: problems.go is not the only one -- PoolWarnings lives in
// views.go and the backup problems in backups.go -- and a hand-written list
// catches up to a new file only after somebody notices. pool.dangerous,
// pool.cache_shared and the backup codes each had no row in
// docs/problem-codes.md while the list was still three names long.
func controllerSources(t *testing.T) []string {
	t.Helper()
	return goFilesIn(t, "../controller")
}

// providerSources is every non-test file in the provider packages, found by
// walking rather than listed, because these are the codes most likely to be
// added without anybody remembering this test exists.
//
// A provider's preflight speaks in findings rather than errors -- a refused
// credential and a template that is not a template are answers, not failures --
// so a provider raises operator-facing codes exactly as the validator does.
// They reached the problems drawer and the provider's own page with no row on
// the reference page and nothing checking, because this list named only the
// files the controller raises problems from. Sixteen of them had that status
// before the walk replaced the list.
func providerSources(t *testing.T) []string {
	t.Helper()
	return goFilesIn(t, "../provider")
}

// goFilesIn is every non-test Go file under root.
func goFilesIn(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if len(out) == 0 {
		t.Fatalf("no sources were found under %s at all; the packages moved, not the docs", root)
	}
	return out
}

// problemSources is every file a code an operator can be shown may come from.
func problemSources(t *testing.T) []string {
	t.Helper()
	sources := append([]string{validatorSource, databaseSource, egressSource}, controllerSources(t)...)
	return append(sources, providerSources(t)...)
}

// codesIn returns every string assigned to a `Code:` field in a Go file.
//
// The source is parsed rather than searched, so a code inside a comment or a
// test fixture cannot be mistaken for one the product can emit.
func codesIn(t *testing.T, path string) []string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Code" {
			return true
		}
		lit, ok := kv.Value.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		code, err := strconv.Unquote(lit.Value)
		if err == nil && code != "" {
			out = append(out, code)
		}
		return true
	})
	return out
}

// Every problem code an operator can be shown has a row explaining it.
//
// The code is the stable half of a problem -- it is what somebody searches
// for, alerts on and quotes in a bug report -- so a new one that reaches the
// startup output or the problems drawer without a row here is a code with no
// answer behind it. Eight of them had exactly that status before this test
// existed.
func TestEveryProblemCodeIsDocumented(t *testing.T) {
	page, err := os.ReadFile(reference)
	if err != nil {
		t.Fatalf("reading %s: %v", filepath.Clean(reference), err)
	}
	text := string(page)

	var missing []string
	seen := map[string]bool{}
	for _, source := range problemSources(t) {
		for _, code := range codesIn(t, source) {
			if seen[code] {
				continue
			}
			seen[code] = true
			// Backticked, so a code that only appears inside a sentence
			// about something else does not count as documented.
			if !strings.Contains(text, "`"+code+"`") {
				missing = append(missing, code)
			}
		}
	}

	if len(missing) > 0 {
		t.Errorf("these problem codes are raised but have no row in docs/problem-codes.md:\n  %s",
			strings.Join(missing, "\n  "))
	}
	if len(seen) == 0 {
		t.Fatal("no codes were found at all; the parser or the paths are wrong, not the docs")
	}
}

// And the reverse: the page does not describe codes that no longer exist.
//
// A row for a code that was renamed or removed sends somebody looking for a
// setting that is not there, which is the more confusing of the two failures.
func TestTheReferenceDescribesNoCodeThatIsGone(t *testing.T) {
	page, err := os.ReadFile(reference)
	if err != nil {
		t.Fatalf("reading %s: %v", filepath.Clean(reference), err)
	}

	real := map[string]bool{}
	for _, source := range problemSources(t) {
		for _, code := range codesIn(t, source) {
			real[code] = true
		}
	}

	// A code is `word.word` inside backticks at the start of a table row.
	var stale []string
	for _, line := range strings.Split(string(page), "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		code := line[3:]
		end := strings.Index(code, "`")
		if end < 0 {
			continue
		}
		code = code[:end]
		if strings.Contains(code, ".") && !real[code] {
			stale = append(stale, code)
		}
	}

	if len(stale) > 0 {
		t.Errorf("docs/problem-codes.md documents codes nothing raises any more:\n  %s",
			strings.Join(stale, "\n  "))
	}
}

// Every metric the controller exposes has a row on the metrics page.
//
// The same argument as the problem codes: a reference page that claims to list
// everything is worth having only if something checks. Metric names are found
// by their `zoomies_` prefix rather than by parsing, because they are declared
// three different ways -- a struct literal, a helper call and a NewDesc -- and
// the prefix is the thing they actually have in common.
func TestEveryMetricIsDocumented(t *testing.T) {
	const (
		source = "../controller/metrics.go"
		page   = "../../docs/metrics.md"
	)

	code, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("reading %s: %v", source, err)
	}
	reference, err := os.ReadFile(page)
	if err != nil {
		t.Fatalf("reading %s: %v", page, err)
	}

	names := map[string]bool{}
	for _, quoted := range strings.Split(string(code), `"`) {
		if strings.HasPrefix(quoted, "zoomies_") && !strings.Contains(quoted, " ") {
			names[quoted] = true
		}
	}
	if len(names) == 0 {
		t.Fatal("no metric names were found at all; the source moved, not the docs")
	}

	var missing []string
	for name := range names {
		if !strings.Contains(string(reference), "`"+name+"`") {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("these metrics are exposed but have no row in docs/metrics.md:\n  %s",
			strings.Join(missing, "\n  "))
	}
}

// Every top-level command has a mention on the command-line page.
//
// A command an operator cannot find is a command that does not exist to them,
// and the reference is where they look after `--help`. This checks the fifteen
// the dispatch table knows about; the sub-subcommands are checked by nothing,
// because a table of every flag would be a copy of the binary rather than a
// document.
func TestEveryCommandIsDocumented(t *testing.T) {
	const (
		source = "../../cmd/zoomies/main.go"
		page   = "../../docs/cli.md"
	)

	code, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("reading %s: %v", source, err)
	}
	reference, err := os.ReadFile(page)
	if err != nil {
		t.Fatalf("reading %s: %v", page, err)
	}

	table := regexp.MustCompile(`\{"([a-z-]+)", group(?:Run|Fleet|Setup),`)
	found := table.FindAllStringSubmatch(string(code), -1)
	if len(found) == 0 {
		t.Fatal("no commands were found at all; the dispatch table moved, not the docs")
	}

	var missing []string
	for _, m := range found {
		if !strings.Contains(string(reference), "`zoomies "+m[1]) {
			missing = append(missing, m[1])
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("these commands exist but are not on docs/cli.md:\n  zoomies %s",
			strings.Join(missing, "\n  zoomies "))
	}
}

// A screenshot has one description, wherever it appears.
//
// An alt text is read *instead of* the image, not beside it, so two
// descriptions of one picture are two different pictures to anybody who cannot
// see it -- and the two drift, because nobody rewrites both. Five screenshots
// had two descriptions each before this test existed.
func TestOneDescriptionPerScreenshot(t *testing.T) {
	pages, err := filepath.Glob("../../docs/*.md")
	if err != nil {
		t.Fatalf("globbing docs: %v", err)
	}
	pages = append(pages, "../../README.md")

	markdown := regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]*screenshots/[^)\s]+)`)
	html := regexp.MustCompile(`<img src="([^"]*screenshots/[^"]+)" alt="([^"]*)"`)

	// image file -> description -> the pages that use it.
	seen := map[string]map[string][]string{}
	note := func(src, alt, page string) {
		// The theme's light/dark suffix is part of the link, not the image.
		name := strings.SplitN(filepath.Base(src), "#", 2)[0]
		if seen[name] == nil {
			seen[name] = map[string][]string{}
		}
		seen[name][alt] = append(seen[name][alt], filepath.Base(page))
	}

	for _, page := range pages {
		body, err := os.ReadFile(page)
		if err != nil {
			t.Fatalf("reading %s: %v", page, err)
		}
		for _, m := range markdown.FindAllStringSubmatch(string(body), -1) {
			note(m[2], m[1], page)
		}
		for _, m := range html.FindAllStringSubmatch(string(body), -1) {
			note(m[1], m[2], page)
		}
	}
	if len(seen) == 0 {
		t.Fatal("no screenshots were found at all; the docs moved, not the alt text")
	}

	for name, descriptions := range seen {
		if len(descriptions) < 2 {
			continue
		}
		var lines []string
		for alt, pages := range descriptions {
			lines = append(lines, "    "+strings.Join(pages, ", ")+": "+alt)
		}
		sort.Strings(lines)
		t.Errorf("%s is described %d different ways:\n%s", name, len(descriptions), strings.Join(lines, "\n"))
	}
}

// The two documents that draw a map of the repository. They are written for
// different readers -- one for a visitor, one for a contributor -- so they are
// allowed to differ in wording, but neither is allowed to name a path that is
// not there or to leave a top-level directory out.
var layouts = []string{"../../README.md", "../../AGENTS.md"}

// The fenced block under a layout heading, and one `path  what it is for` line
// inside it. The continuation lines of a wrapped description start with spaces
// and so do not match.
var (
	layoutBlock = regexp.MustCompile("(?is)## (?:Project )?Layout\\n+```text\\n(.*?)```")
	layoutLine  = regexp.MustCompile(`(?m)^(\S+)\s{2,}\S`)
)

// Directories that are build products, tooling or checkouts rather than parts
// of the repository, so a layout block that omits them is right to.
var notInLayout = map[string]bool{
	"node_modules": true, "site": true,
	// `make` writes binaries here -- release archives, and the pair of builds
	// `make test-upgrade` installs -- so it exists on a machine that has run a
	// build and nowhere else.
	"dist": true, "bin": true,
}

// A layout block that has drifted is the most quietly misleading kind of
// documentation: it reads as authoritative, so a newcomer trusts it and spends
// twenty minutes looking for a directory that moved.
func TestTheLayoutBlocksDescribeTheRepositoryAsItIs(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolving the repository root: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading the repository root: %v", err)
	}
	wanted := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() && !strings.HasPrefix(name, ".") && !notInLayout[name] {
			wanted[name] = true
		}
	}
	if len(wanted) == 0 {
		t.Fatal("no top-level directories were found; the test is looking in the wrong place")
	}

	for _, doc := range layouts {
		body, err := os.ReadFile(doc)
		if err != nil {
			t.Fatalf("reading %s: %v", doc, err)
		}
		block := layoutBlock.FindStringSubmatch(string(body))
		if block == nil {
			t.Errorf("%s has no layout block under a Layout heading any more", filepath.Base(doc))
			continue
		}

		covered := map[string]bool{}
		for _, m := range layoutLine.FindAllStringSubmatch(block[1], -1) {
			path := strings.TrimSuffix(m[1], "/")
			if _, err := os.Stat(filepath.Join(root, path)); err != nil {
				t.Errorf("%s names %q, which is not in the repository", filepath.Base(doc), m[1])
				continue
			}
			// `cmd/zoomies` is how a block says what `cmd/` holds.
			covered[strings.SplitN(path, "/", 2)[0]] = true
		}

		for dir := range wanted {
			if !covered[dir] {
				t.Errorf("%s does not say what %s/ is for", filepath.Base(doc), dir)
			}
		}
	}
}

// Every configuration key is on the page that claims to list them all.
//
// docs/configuration.md opens with "Every zoomies.yaml key and its ZOOMIES_*
// environment override", and until this test it was a promise kept by hand.
// The keys that had quietly stopped being kept were the ones nobody used, which
// is exactly backwards: a setting an operator has never heard of is the one
// they need the documentation for.
//
// The registry in internal/config is the source, because it is what the
// product itself reads -- the settings page, the environment, the database and
// this page are all descriptions of that one list, and this holds the last of
// them to the other three.
func TestEveryConfigurationKeyIsDocumented(t *testing.T) {
	const page = "../../docs/configuration.md"
	reference, err := os.ReadFile(page)
	if err != nil {
		t.Fatalf("reading %s: %v", page, err)
	}
	text := string(reference)

	var missingKey, missingEnv []string
	for _, s := range config.Settings() {
		if !strings.Contains(text, s.Key) {
			missingKey = append(missingKey, s.Key)
		}
		if !strings.Contains(text, s.Env) {
			missingEnv = append(missingEnv, s.Env)
		}
	}
	sort.Strings(missingKey)
	sort.Strings(missingEnv)
	if len(missingKey) > 0 {
		t.Errorf("these settings exist but are not on docs/configuration.md:\n  %s",
			strings.Join(missingKey, "\n  "))
	}
	if len(missingEnv) > 0 {
		t.Errorf("these environment overrides exist but are not on docs/configuration.md:\n  %s",
			strings.Join(missingEnv, "\n  "))
	}
}

// A code documented twice is two answers to one question, and the catalog
// built from this page refuses to choose between them. The status-page table
// repeats codes on purpose and is the one place a second row is allowed.
func TestTheReferenceDocumentsEachCodeOnce(t *testing.T) {
	raw, err := os.ReadFile(reference)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	line := 0
	for _, l := range strings.Split(string(raw), "\n") {
		line++
		if strings.HasPrefix(l, "## What the status page says") {
			break
		}
		m := codeRow.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if first, dup := seen[m[1]]; dup {
			t.Errorf("%s is documented on line %d and again on line %d", m[1], first, line)
			continue
		}
		seen[m[1]] = line
	}
}

var codeRow = regexp.MustCompile("^\\| `([a-z0-9_.]+)`")
