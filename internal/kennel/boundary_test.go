package kennel

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The package is pure for the reason internal/scheduler is: a check that reads
// a clock, a database or the network cannot be tested by describing a world.
// So it may import the standard library's pure parts and nothing else, and it
// may not ask the time.
//
// If a change here is deliberate, this list is what has to change with it, and
// a reviewer is meant to see it do so.
var allowedImports = []string{
	"regexp", "slices", "sort", "strconv", "strings", "time",
}

func nonTestFiles(t *testing.T) []string {
	t.Helper()
	all, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range all {
		if !strings.HasSuffix(f, "_test.go") {
			out = append(out, f)
		}
	}
	if len(out) < 5 {
		t.Fatalf("found only %d source files; the test is looking in the wrong place", len(out))
	}
	return out
}

func TestTheEvaluatorImportsNothingImpure(t *testing.T) {
	fset := token.NewFileSet()
	for _, name := range nonTestFiles(t) {
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if !slices.Contains(allowedImports, path) {
				t.Errorf("%s imports %s; internal/kennel is pure and may import only %v", name, path, allowedImports)
			}
		}
	}
}

// "time" is allowed for its types. Asking it what time it is, or for a timer,
// is what makes a check depend on when it ran.
func TestTheEvaluatorNeverReadsAClock(t *testing.T) {
	forbidden := []string{"Now", "Since", "Until", "After", "AfterFunc", "Sleep", "Tick", "NewTicker", "NewTimer"}
	fset := token.NewFileSet()
	for _, name := range nonTestFiles(t) {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "time" && slices.Contains(forbidden, sel.Sel.Name) {
				t.Errorf("%s: time.%s -- the evaluator is handed the time in Snapshot.At", fset.Position(sel.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
}

func TestTheEvaluatorTouchesNoFilesystemOrEnvironment(t *testing.T) {
	// os is not importable (see allowedImports), so this is belt and braces: the
	// directory has no other kind of source file either.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".go") || e.IsDir() {
			continue
		}
		t.Errorf("unexpected file %s; the package holds Go source and nothing it could read at run time", e.Name())
	}
}
