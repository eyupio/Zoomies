package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// maxModels bounds a model list: a gateway such as OpenRouter serves hundreds,
// and a page does not need more than a screenful's worth of choice to be useful.
const maxModels = 1000

// modelsFrom reads the list both protocols answer with, `{"data":[{"id":...}]}`,
// into sorted, distinct names. A body that is not that is an error saying so,
// and never repeats the body: it came from a server the person pointed us at.
func modelsFrom(resp *http.Response) ([]string, error) {
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("reading the model list: %w", err)
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &list); err != nil || list.Data == nil {
		return nil, fmt.Errorf("the provider answered, but not with a list of models; type the model's name instead")
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range list.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	if len(out) > maxModels {
		out = out[:maxModels]
	}
	return out, nil
}
