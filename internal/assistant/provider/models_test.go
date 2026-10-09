package provider

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/assistant"
	"github.com/eyupio/zoomies/internal/assistant/assistanttest"
)

// Both protocols answer a model list as {"data":[{"id":...}]}, and a person
// chooses from it, so it comes back sorted and without repeats, and with the
// key sent as that protocol sends it.
func TestAProvidersModelListIsSortedDistinctAndAskedForWithTheKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(srv *assistanttest.Server) assistant.ModelLister
		path string
		key  func(h map[string][]string) string
	}{
		{"openai", func(s *assistanttest.Server) assistant.ModelLister {
			return NewOpenAICompatible(Config{BaseURL: s.URL + "/v1", APIKey: "sk-test"})
		}, "/v1/models", func(h map[string][]string) string { return strings.Join(h["Authorization"], "") }},
		{"anthropic", func(s *assistanttest.Server) assistant.ModelLister {
			return NewAnthropic(Config{BaseURL: s.URL, APIKey: "sk-test"})
		}, "/v1/models", func(h map[string][]string) string { return strings.Join(h["X-Api-Key"], "") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var srv *assistanttest.Server
			if tc.name == "openai" {
				srv = assistanttest.NewOpenAI(t)
			} else {
				srv = assistanttest.NewAnthropic(t)
			}
			srv.Models = []string{"zeta", "alpha", "alpha", " ", "mid"}
			got, err := tc.open(srv).Models(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if want := []string{"alpha", "mid", "zeta"}; !reflect.DeepEqual(got, want) {
				t.Errorf("models = %v, want %v", got, want)
			}
			reqs := srv.Requests()
			if len(reqs) != 1 || reqs[0].Path != tc.path || reqs[0].Method != "GET" {
				t.Fatalf("requests = %+v", reqs)
			}
			if k := tc.key(reqs[0].Header); !strings.Contains(k, "sk-test") {
				t.Errorf("the key was not sent: %q", k)
			}
		})
	}
}

// A server that answers something other than a model list is told so in words
// that do not repeat what it sent, and a refused key is the refusal every other
// call reports.
func TestAModelListThatIsNotOneAndOneThatIsRefusedBothSayWhy(t *testing.T) {
	srv := assistanttest.NewOpenAI(t)
	p := NewOpenAICompatible(Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})

	srv.Status, srv.Body = 200, `<html>sk-test secret page</html>`
	_, err := p.Models(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not with a list of models") || strings.Contains(err.Error(), "secret page") {
		t.Errorf("a body that is not a list: %v", err)
	}

	// Well-formed JSON that has no list in it is not an empty list: telling the
	// two apart is what keeps a page from offering a dropdown with nothing in it.
	srv.Status, srv.Body = 200, `{"object":"list"}`
	_, err = p.Models(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not with a list of models") {
		t.Errorf("JSON without a list: %v", err)
	}

	srv.Status, srv.Body = 401, `{"error":{"message":"bad key sk-test"}}`
	_, err = p.Models(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refused the key") || strings.Contains(err.Error(), "sk-test") {
		t.Errorf("a refused key: %v", err)
	}

	// The other protocol reports a refusal the same way, and not as an empty list.
	asrv := assistanttest.NewAnthropic(t)
	asrv.Status, asrv.Body = 401, `{"error":{"message":"bad key sk-test"}}`
	_, err = NewAnthropic(Config{BaseURL: asrv.URL, APIKey: "sk-test"}).Models(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refused the key") || strings.Contains(err.Error(), "sk-test") {
		t.Errorf("a refused key on the Messages protocol: %v", err)
	}
}

// A gateway can serve hundreds of models; the list stops at the bound and keeps
// the first of the sorted names, so what a page shows does not depend on the order
// the server happened to answer in.
func TestAModelListIsCutAtTheBound(t *testing.T) {
	srv := assistanttest.NewOpenAI(t)
	for i := 0; i < maxModels+25; i++ {
		srv.Models = append(srv.Models, fmt.Sprintf("model-%04d", i))
	}
	got, err := NewOpenAICompatible(Config{BaseURL: srv.URL + "/v1"}).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != maxModels || got[0] != "model-0000" || got[maxModels-1] != fmt.Sprintf("model-%04d", maxModels-1) {
		t.Errorf("got %d models, first %q, last %q", len(got), got[0], got[len(got)-1])
	}
}

func TestAnEmptyModelListIsAnEmptyListAndNotAnError(t *testing.T) {
	srv := assistanttest.NewOpenAI(t)
	srv.Models = []string{}
	got, err := NewOpenAICompatible(Config{BaseURL: srv.URL + "/v1"}).Models(context.Background())
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
}
