package updates

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// The rule is only testable to the nanosecond, and only agrees with itself
// between the status and the update, while it reaches for nothing but its
// arguments. Once it imports the store or the controller it can be handed a
// different answer than the one it was asked for, and "which release would be
// taken" is back to being two questions.
func TestTheUpdateRuleImportsOnlyTheStandardLibraryAndVersion(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("reading this package: %v", err)
	}
	if len(pkg.Imports) == 0 {
		t.Fatal("no imports were found at all; the walk is wrong, not the package")
	}
	for _, problem := range impureImports(pkg.Imports) {
		t.Error(problem)
	}
}

// The standard library is not all pure: these are the packages that reach the
// network, the disk, a process, a database or a source of chance, each of which
// makes "which release would be taken" depend on something other than the
// arguments. A package listed here stands for itself and everything beneath it.
var impurePackages = []string{"net", "os", "database/sql", "math/rand", "crypto/rand"}

// impureImports names each import the rule may not have, in the words of the
// failure, so the test and its own check read alike.
func impureImports(imports []string) []string {
	var problems []string
	for _, path := range imports {
		first, _, _ := strings.Cut(path, "/")
		if strings.Contains(first, ".") && path != "github.com/eyupio/zoomies/internal/version" {
			problems = append(problems, "internal/updates imports "+path+"; the rule may use the standard library and internal/version, and what needs a file, a command or the clock belongs to its caller")
			continue
		}
		for _, banned := range impurePackages {
			if path == banned || strings.HasPrefix(path, banned+"/") {
				problems = append(problems, "internal/updates imports "+path+", which reaches outside its arguments; the network, the disk, a process, a database and a random source belong to the caller")
			}
		}
	}
	return problems
}

// The denylist is only worth having if it would catch what it names, so this
// hands it each of them and a package it must leave alone.
func TestThePurityCheckRefusesWhatReachesOutsideTheArguments(t *testing.T) {
	for _, path := range []string{
		"net", "net/http", "os", "os/exec", "database/sql", "database/sql/driver",
		"math/rand", "math/rand/v2", "crypto/rand",
		"github.com/eyupio/zoomies/internal/store",
	} {
		if got := impureImports([]string{path}); len(got) != 1 {
			t.Errorf("importing %s raised %d complaints, want 1", path, len(got))
		}
	}
	for _, path := range []string{
		"fmt", "sort", "strings", "time", "slices", "regexp", "crypto/sha256", "math",
		"github.com/eyupio/zoomies/internal/version",
	} {
		if got := impureImports([]string{path}); len(got) != 0 {
			t.Errorf("importing %s was refused: %v", path, got)
		}
	}
}

// Time arrives as an argument. A read of the clock inside the rule is what
// would let the sentence say a release is due while the decision says it is
// not, a millisecond apart.
func TestTheUpdateRuleNeverReadsTheClock(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading this package: %v", err)
	}
	files := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files++
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" {
				switch sel.Sel.Name {
				case "Now", "Since", "Until", "After", "Sleep", "NewTimer", "NewTicker", "Tick", "AfterFunc":
					t.Errorf("%s: time.%s reads the clock; pass the instant in", fset.Position(sel.Pos()), sel.Sel.Name)
				}
			}
			return true
		})
	}
	if files == 0 {
		t.Fatal("no source files were scanned at all; the walk is wrong, not the rule")
	}
}
