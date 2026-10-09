package workflow

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The parser is shared by the controller's collector and by the offline
// zoomies kennel check, and the second of those must run on a laptop with no
// controller and no GitHub client. So it may import the standard library's
// pure parts and the YAML library, and nothing from this module: the moment it
// reaches for the store, the GitHub package or the controller, the offline
// command would drag them along. A deliberate change here changes this list,
// where a reviewer sees it.
var allowedImports = []string{
	"bytes", "errors", "io", "regexp", "strings", "unicode/utf8", "gopkg.in/yaml.v3",
}

func sources(t *testing.T) []string {
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
	if len(out) == 0 {
		t.Fatal("found no source files; the test is looking in the wrong place")
	}
	return out
}

func TestTheParserImportsNothingFromThisModuleOrTheOutsideWorld(t *testing.T) {
	fset := token.NewFileSet()
	for _, name := range sources(t) {
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if !slices.Contains(allowedImports, path) {
				t.Errorf("%s imports %s; the workflow parser may import only %v", name, path, allowedImports)
			}
		}
	}
}

// The parser is handed bytes and answers with facts. A file read, an
// environment lookup or a clock would make its answer depend on where and
// when it ran, which is what a fixture-driven test cannot describe.
func TestTheParserNeverReadsAClockTheFilesystemOrTheEnvironment(t *testing.T) {
	forbidden := map[string][]string{
		"time": {"Now", "Since", "Until", "After", "AfterFunc", "Sleep", "Tick", "NewTicker", "NewTimer"},
		"os":   {"ReadFile", "Open", "Getenv", "LookupEnv", "Environ"},
	}
	fset := token.NewFileSet()
	for _, name := range sources(t) {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && slices.Contains(forbidden[id.Name], sel.Sel.Name) {
				t.Errorf("%s: %s.%s: the parser is given bytes and gives facts", fset.Position(sel.Pos()), id.Name, sel.Sel.Name)
			}
			return true
		})
	}
}
