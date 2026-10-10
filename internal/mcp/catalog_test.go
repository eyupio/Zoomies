package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// catalogDoc is a catalog small enough to read whole: three entries in two
// categories, one of them a check.
func catalogDoc() string {
	return `{"version":1,"generated_from":"x","entries":[
	  {"id":"pool.no_host","kind":"problem","title":"A pool has no host","category":"capacity","severity":"error","detection":"runtime","detects":"d1","fix":"f1","verify":null,"docs_html":"h","docs_md":"m"},
	  {"id":"ci.no_timeout","kind":"check","title":"A job has no timeout","category":"reliability","severity":"warning","detection":"static","detects":"d2","fix":"f2","verify":"v2","area":"ci","docs_html":"h","docs_md":"m"},
	  {"id":"pool.minimum_overcharges","kind":"problem","title":"A minimum costs more than it saves","category":"capacity","severity":"info","detection":"runtime","detects":"d3","fix":"f3","verify":null,"docs_html":"h","docs_md":"m"}
	]}`
}

// The catalog is the one place every code and check is explained, and the docs
// tell an agent to read it before explaining a problem; an agent that reaches the
// fleet only over MCP had no way to. It is far too big to send whole, so without
// arguments it is an index, and one entry comes by its code.
func TestTheCatalogIsAnIndexWithoutArgumentsAndOneEntryByCode(t *testing.T) {
	api := &kennelAPI{body: catalogDoc()}
	out, err := kennelCall(t, kennelTool(t, "get_catalog").call, api, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if api.got != "GET /catalog" {
		t.Errorf("asked %q", api.got)
	}
	var index struct {
		Version    int                 `json:"version"`
		Categories map[string][]string `json:"categories"`
	}
	if len(out) != 1 || json.Unmarshal([]byte(out[0].Text), &index) != nil {
		t.Fatalf("the index is not one block of the shape expected: %+v", out)
	}
	if index.Version != 1 || len(index.Categories["capacity"]) != 2 || index.Categories["reliability"][0] != "ci.no_timeout" {
		t.Errorf("index = %+v", index)
	}
	if strings.Contains(out[0].Text, "f1") {
		t.Error("the index carries an entry's text, so it is not an index")
	}

	out, err = kennelCall(t, kennelTool(t, "get_catalog").call, api, `{"code":"ci.no_timeout"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || !strings.Contains(out[0].Text, `"fix":"f2"`) || strings.Contains(out[0].Text, "pool.no_host") {
		t.Errorf("one entry = %+v", out)
	}
}

// A category narrows the index to its codes with their titles and severity, which
// is what an agent reads to choose which entry to open.
func TestTheCatalogListsACategoryWithTitles(t *testing.T) {
	api := &kennelAPI{body: catalogDoc()}
	out, err := kennelCall(t, kennelTool(t, "get_catalog").call, api, `{"category":"capacity"}`)
	if err != nil {
		t.Fatal(err)
	}
	text := out[0].Text
	if !strings.Contains(text, "A minimum costs more than it saves") || !strings.Contains(text, `"severity":"error"`) || strings.Contains(text, "ci.no_timeout") || strings.Contains(text, "f1") {
		t.Errorf("category = %s", text)
	}
}

// A code nobody documented is answered with the code and where the index is,
// not with an empty document the model would read as "nothing is known".
func TestAnUnknownCatalogCodeIsSaidToBeUnknown(t *testing.T) {
	api := &kennelAPI{body: catalogDoc()}
	out, err := kennelCall(t, kennelTool(t, "get_catalog").call, api, `{"code":"pool.made_up"}`)
	if err == nil || !strings.Contains(err.Error(), "pool.made_up") || out != nil {
		t.Errorf("out %+v err %v", out, err)
	}
}
