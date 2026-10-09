package offline

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The offline check runs on a laptop with no controller, so it may reach the
// evaluator, the parser and the standard library, and nothing else from this
// module: the store, the GitHub client and the controller would come with
// their dependencies and their reasons to talk to something.
var allowedImports = []string{
	"crypto/sha1", "encoding/hex", "errors", "fmt", "io/fs", "os", "path/filepath", "regexp", "sort", "strconv", "strings",
	"github.com/eyupio/zoomies/internal/kennel", "github.com/eyupio/zoomies/internal/kennel/workflow",
}

func TestTheOfflineCheckImportsOnlyTheEvaluatorTheParserAndTheStandardLibrary(t *testing.T) {
	all, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range all {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if !slices.Contains(allowedImports, path) {
				t.Errorf("%s imports %s; the offline check may import only %v", name, path, allowedImports)
			}
		}
	}
}
