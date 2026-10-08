package provenance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The terms are seeded from pieces inside this test, for the same reason the
// package assembles the real ones from fragments: no file in the tree may
// hold a forbidden term whole, this one included.
func seed(t *testing.T) string {
	t.Helper()
	term := "exam" + "plevendor"
	SetTermsForTest(t, []string{term})
	return term
}

func TestScanFindsATermWhateverItsCase(t *testing.T) {
	term := seed(t)
	text := "first line\nmentions " + strings.ToUpper(term[:4]) + term[4:] + " here\nand " + term + " again\n"
	hits := Scan("a.md", strings.NewReader(text))
	if len(hits) != 2 || hits[0].Line != 2 || hits[1].Line != 3 || hits[0].Term != term || hits[0].Path != "a.md" {
		t.Fatalf("hits = %+v", hits)
	}
	if hits := Scan("b.md", strings.NewReader("nothing to see\n")); len(hits) != 0 {
		t.Fatalf("hits on clean text: %+v", hits)
	}
}

// The walk has to be cheap enough to run on every test invocation and must
// not choke on a font or a bundle, so it skips what is not source; and it has
// to read the places a name would most likely slip in, which are prose and
// workflows rather than Go.
func TestScanTreeSkipsWhatItShouldAndReadsWhatItMust(t *testing.T) {
	term := seed(t)
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".git/HEAD", term)
	write("web/node_modules/x.js", term)
	write("internal/api/webdist/app.js", term)
	write("site/index.html", term)
	write("big.txt", strings.Repeat("x", 2<<20)+term)
	write("binary.bin", "\x00\x01"+term)
	write("roadmap/a.md", "fine\n"+term+"\n")
	write(".github/workflows/b.yml", "name: "+term+"\n")
	hits, err := ScanTree(root, DefaultSkip)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range hits {
		got = append(got, h.Path)
	}
	want := []string{".github/workflows/b.yml", "roadmap/a.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("hit paths = %v, want %v", got, want)
	}
}

func TestTermsAreLowerCasedAndDeduplicated(t *testing.T) {
	SetTermsForTest(t, []string{"Abc", "abc", "Def"})
	if got := strings.Join(Terms(), ","); got != "abc,def" {
		t.Fatalf("terms = %q", got)
	}
}
