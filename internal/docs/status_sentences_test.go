package docs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

const statusSource = "../controller/status.go"

// publicSentencesIn reads the publicSentences map literal out of the
// controller's source, parsed rather than searched for the reason codesIn
// is: a sentence in a comment is not one the status page can show.
func publicSentencesIn(t *testing.T) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), statusSource, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", statusSource, err)
	}
	out := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "publicSentences" || len(spec.Values) != 1 {
			return true
		}
		lit, ok := spec.Values[0].(*ast.CompositeLit)
		if !ok {
			return false
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			k, kok := kv.Key.(*ast.BasicLit)
			v, vok := kv.Value.(*ast.BasicLit)
			if !kok || !vok {
				t.Errorf("a publicSentences entry is not a pair of string literals; a sentence built from anything else could carry a name")
				continue
			}
			code, _ := strconv.Unquote(k.Value)
			sentence, _ := strconv.Unquote(v.Value)
			out[code] = sentence
		}
		return false
	})
	if len(out) == 0 {
		t.Fatal("no public sentences were found; the map moved, not the docs")
	}
	return out
}

// statusTable is the "What the status page says" table on the problem-codes
// page, code to sentence.
func statusTable(t *testing.T) map[string]string {
	t.Helper()
	page, err := os.ReadFile(reference)
	if err != nil {
		t.Fatalf("reading %s: %v", reference, err)
	}
	_, section, ok := strings.Cut(string(page), "\n## What the status page says\n")
	if !ok {
		t.Fatal(`docs/problem-codes.md has no "What the status page says" section`)
	}
	if end := strings.Index(section, "\n## "); end >= 0 {
		section = section[:end]
	}
	out := map[string]string{}
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), " | ")
		if len(cells) != 2 {
			t.Errorf("a row in the status table is not two cells: %s", line)
			continue
		}
		out[strings.Trim(strings.TrimSpace(cells[0]), "`")] = strings.TrimSpace(cells[1])
	}
	return out
}

// The status page shows a reader with no account one sentence per problem
// code, and the problem-codes page publishes the same sentence beside the
// operator's -- in both directions and word for word, so that what an
// operator reads there is what their developers are being told.
func TestEveryPublicSentenceIsOnTheProblemCodesPage(t *testing.T) {
	code := publicSentencesIn(t)
	page := statusTable(t)
	for c, s := range code {
		got, ok := page[c]
		switch {
		case !ok:
			t.Errorf("%s has a public sentence and no row in the status table of docs/problem-codes.md", c)
		case got != s:
			t.Errorf("%s says %q in the code and %q on the page", c, s, got)
		}
	}
	for c := range page {
		if _, ok := code[c]; !ok {
			t.Errorf("docs/problem-codes.md gives %s a public sentence the status page does not have", c)
		}
	}
}
