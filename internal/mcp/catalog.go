package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// The catalog is the one place every problem code and every Kennel Club check
// is explained, and docs/ai-context.md tells an agent to read it before it
// explains a problem. An agent that reaches the fleet only over MCP had no way
// to: an OAuth token is good on /mcp alone. It is a few hundred kilobytes, far
// more than a tool answer should put in front of a model, so without arguments
// the tool is an index, and an entry comes by its code.

// catalogCategories are the catalog's own, in the order an agent should look.
var catalogCategories = []string{"capacity", "reliability", "security", "configuration", "cost"}

// catalogEntry is the part of an entry the index and a category list keep.
type catalogEntry struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Severity string `json:"severity"`
}

func catalogTools() []*tool {
	return []*tool{
		{
			Name:  "get_catalog",
			Title: "Catalog of problem codes and checks",
			Description: "Every problem code the controller raises and every check Kennel Club makes, with what each means, what to change and how to see that it worked: " +
				"the catalog at GET /catalog, which is Zoomies' own fixed text and names no fleet, repository or person. " +
				"Without arguments it is an index, each code under its category (capacity, reliability, security, configuration, cost). " +
				"With code it is that one entry in full, which is what to read before explaining a problem's code, a finding's check or a job explanation's problem_code. " +
				"With category it is that category's codes with their titles and severity. " +
				"A code that is not in the catalog is said to be unknown; it is not an empty entry.",
			InputSchema: object(nil, map[string]any{
				"code":     str("a problem code or check id, as list_problems, get_job or kennel_findings show it, such as pool.no_host or ci.no_timeout"),
				"category": enum("list the codes of one category, with titles", catalogCategories...),
			}),
			Annotations: readOnly,
			call:        getCatalog,
		},
	}
}

func getCatalog(ctx context.Context, c API, raw json.RawMessage) ([]Content, error) {
	var a struct {
		Code     string `json:"code"`
		Category string `json:"category"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	body, err := c.Call(ctx, "GET", "/catalog", nil)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Version int               `json:"version"`
		Entries []json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, errCatalogShape
	}
	var entries []catalogEntry
	if err := json.Unmarshal(body, &struct {
		Entries *[]catalogEntry `json:"entries"`
	}{&entries}); err != nil || len(entries) != len(doc.Entries) {
		return nil, errCatalogShape
	}
	switch {
	case a.Code != "":
		for i, e := range entries {
			if e.ID == a.Code {
				return jsonContent(doc.Entries[i]), nil
			}
		}
		return nil, fmt.Errorf("there is no %s in the catalog; get_catalog without arguments lists every code", a.Code)
	case a.Category != "":
		out := make([]catalogEntry, 0, len(entries))
		for _, e := range entries {
			if e.Category == a.Category {
				out = append(out, e)
			}
		}
		return marshalContent(map[string]any{"version": doc.Version, "category": a.Category, "entries": out})
	}
	categories := map[string][]string{}
	for _, e := range entries {
		categories[e.Category] = append(categories[e.Category], e.ID)
	}
	for _, ids := range categories {
		sort.Strings(ids)
	}
	return marshalContent(map[string]any{"version": doc.Version, "categories": categories})
}

// errCatalogShape is a catalog the tool cannot take apart, refused rather than
// passed on, as the Kennel tools refuse an answer they cannot read.
var errCatalogShape = errors.New("the controller's catalog is not in the shape this tool expects; upgrade the controller and the CLI together")

func marshalContent(v any) ([]Content, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return []Content{{Type: "text", Text: strings.TrimSpace(string(body))}}, nil
}
