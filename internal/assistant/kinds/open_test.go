package kinds

import (
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
