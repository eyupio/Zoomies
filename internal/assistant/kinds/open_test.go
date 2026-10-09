package kinds

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/assistant"
)

func TestOpenRefusesAnUnknownKindAndNamesTheOnesItKnows(t *testing.T) {
	_, err := Open("gemini", Config{Model: "m"})
	if err == nil {
		t.Fatal("no error")
	}
	for _, k := range []string{"openai_compatible", "anthropic", "openai"} {
		if !strings.Contains(err.Error(), k) {
			t.Errorf("error %q does not name %s", err, k)
		}
	}
}

// A hosted kind needs no address typed: the one it has is the one it has.
func TestOpenGivesTheHostedKindsTheirBaseURL(t *testing.T) {
	if got := assistant.DefaultBaseURL(assistant.KindOpenAI); got != "https://api.openai.com/v1" {
		t.Errorf("openai %q", got)
	}
	if got := assistant.DefaultBaseURL(assistant.KindAnthropic); got != "https://api.anthropic.com" {
		t.Errorf("anthropic %q", got)
	}
	if got := assistant.DefaultBaseURL(assistant.KindOpenAICompatible); got != "" {
		t.Errorf("compatible has a default %q; it must be typed", got)
	}
	for _, k := range []assistant.Kind{assistant.KindFake, assistant.KindOpenAI, assistant.KindAnthropic} {
		if _, err := Open(k, Config{Model: "m"}); err != nil {
			t.Errorf("%s with no base URL: %v", k, err)
		}
	}
	if _, err := Open(assistant.KindOpenAICompatible, Config{Model: "m"}); err == nil {
		t.Error("a compatible provider with no base URL opened")
	}
}

// Review: Go's client drops Authorization on a cross-host redirect but not a
// custom header, so a base URL that redirected elsewhere would hand the
// Anthropic key to that host. A model API never needs a redirect, so none is
// followed, on either adapter.
func TestAnOpenedProviderFollowsNoRedirect(t *testing.T) {
	var leaked http.Header
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = r.Header.Clone() }))
	defer sink.Close()
	hop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer hop.Close()
	for _, k := range []assistant.Kind{assistant.KindAnthropic, assistant.KindOpenAICompatible} {
		p, err := Open(k, Config{BaseURL: hop.URL, APIKey: "sk-secret", Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Check(context.Background())
		if err == nil || !strings.Contains(err.Error(), "redirect") {
			t.Errorf("%s: err %v, want a refused redirect", k, err)
		}
		if leaked != nil {
			t.Errorf("%s: the redirect target received headers %v", k, leaked)
		}
	}
}
