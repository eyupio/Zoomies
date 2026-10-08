package catalog

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/kennel"
)

// The embedded catalog is a build product checked into the tree, like the
// OpenAPI document and the image catalogue, so that the binary serves exactly
// what the site publishes. This test is what makes a stale copy a failure
// rather than a surprise: it rebuilds the catalog from the page and the
// registry and compares, ignoring only the commit the generator stamped.
func TestTheCatalogIsCurrent(t *testing.T) {
	md, err := os.ReadFile("../../docs/problem-codes.md")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := Build(md, kennel.Checks(), "")
	if err != nil {
		t.Fatal(err)
	}
	want, err := Marshal(fresh)
	if err != nil {
		t.Fatal(err)
	}
	got := blankCommit(t, Embedded())
	if !bytes.Equal(got, want) {
		t.Fatalf("internal/catalog/catalog.json is out of date; run `make generate` from the repository root and commit the result")
	}
}

// blankCommit returns the catalog bytes with generated_from set to "", so a
// rebase that changes nothing but the commit does not fail the build.
func blankCommit(t *testing.T, b []byte) []byte {
	t.Helper()
	var c Catalog
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	c.GeneratedFrom = ""
	out, err := Marshal(&c)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// schema.json is published beside the catalog for readers who validate with a
// JSON Schema tool. Nothing here validates with one -- that would be a
// dependency for a test -- so this test holds the two to the same shape by
// hand: the enums, the required keys and the rule that an entry carries no
// key the schema does not name.
func TestTheCatalogMatchesItsSchema(t *testing.T) {
	schema := readJSON(t, "schema.json")
	entrySchema := schema["$defs"].(map[string]any)["entry"].(map[string]any)
	properties := entrySchema["properties"].(map[string]any)
	enum := func(key string) map[string]bool {
		out := map[string]bool{}
		for _, v := range properties[key].(map[string]any)["enum"].([]any) {
			out[v.(string)] = true
		}
		return out
	}
	kinds, categories, detections := enum("kind"), enum("category"), enum("detection")
	var doc struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(Embedded(), &doc); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range doc.Entries {
		id, _ := e["id"].(string)
		if seen[id] {
			t.Errorf("%s appears twice", id)
		}
		seen[id] = true
		for key := range e {
			if _, ok := properties[key]; !ok {
				t.Errorf("%s carries %q, which schema.json does not name", id, key)
			}
		}
		for _, r := range entrySchema["required"].([]any) {
			if _, ok := e[r.(string)]; !ok {
				t.Errorf("%s lacks required %q", id, r)
			}
		}
		if !kinds[e["kind"].(string)] || !categories[e["category"].(string)] || !detections[e["detection"].(string)] {
			t.Errorf("%s: kind/category/detection outside the schema's enums: %v %v %v", id, e["kind"], e["category"], e["detection"])
		}
	}
	if len(doc.Entries) < 250 {
		t.Errorf("only %d entries; the page documents far more", len(doc.Entries))
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return out
}

var checkHeading = regexp.MustCompile("(?m)^### `([a-z_.]+)` \\{ #[a-z_-]+ \\}$")

// The Kennel Club page's check list is generated from the registry between two
// markers, so every check has a heading an anchor can reach and nothing else
// is there.
func TestTheKennelClubPageListsEveryCheck(t *testing.T) {
	b, err := os.ReadFile("../../docs/kennel-club.md")
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	begin, end := strings.Index(page, "<!-- zoomies:catalogue-begin -->"), strings.Index(page, "<!-- zoomies:catalogue-end -->")
	if begin < 0 || end < begin {
		t.Fatal("the page has no generated block between the catalogue markers")
	}
	block := page[begin:end]
	found := map[string]bool{}
	for _, m := range checkHeading.FindAllStringSubmatch(block, -1) {
		found[m[1]] = true
	}
	for _, c := range kennel.Checks() {
		if !found[string(c.Code)] {
			t.Errorf("%s has no heading on the page; run `make generate`", c.Code)
		}
		delete(found, string(c.Code))
	}
	for code := range found {
		t.Errorf("the page lists %s, which the registry does not have", code)
	}
}
