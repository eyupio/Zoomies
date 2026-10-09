package assistant

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The package that defines what a provider is must not know about any one
// provider's wire protocol, a database or the controller: the adapters,
// the store and the API all import it, and a cycle or a hidden dependency
// here is one every later slice inherits.
func TestTheAssistantPackageImportsOnlyWhatItMayImport(t *testing.T) {
	allowed := []string{
		"bufio", "bytes", "context", "crypto/tls", "encoding/json", "errors", "fmt", "io", "iter",
		"net", "net/http", "net/netip", "net/url", "slices", "strings", "sync", "testing", "time",
	}
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
			if !slices.Contains(allowed, path) {
				t.Errorf("%s imports %s; the assistant package may import only %v", name, path, allowed)
			}
		}
	}
}

// Section 9.2: nothing else may import a provider package but the assistant,
// and within it only the kinds package, which opens one by kind, so that a
// model's wire protocol is reachable from one place and the rule that every
// provider passes the contract has teeth.
func TestNothingButTheAssistantImportsAnAdapter(t *testing.T) {
	fset := token.NewFileSet()
	for _, root := range []string{"../../internal", "../../cmd"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			slash := filepath.ToSlash(path)
			if strings.Contains(slash, "/internal/assistant/kinds/") || strings.Contains(slash, "/internal/assistant/provider/") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imp := range file.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if strings.HasPrefix(p, "github.com/eyupio/zoomies/internal/assistant/provider") {
					t.Errorf("%s imports %s; only internal/assistant/kinds may", path, p)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat("provider.go"); err != nil {
		t.Fatal("provider.go is missing: the package this test guards does not exist yet")
	}
}
