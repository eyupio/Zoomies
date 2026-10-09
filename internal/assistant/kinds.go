package assistant

// Kind is which protocol a provider speaks.
type Kind string

const (
	KindFake             Kind = "fake"
	KindOpenAICompatible Kind = "openai_compatible"
	KindAnthropic        Kind = "anthropic"
	KindOpenAI           Kind = "openai"
)

// Kinds is every kind a person may create, in the order the page lists
// them. The fake is not among them: it exists for the demo and for tests.
var Kinds = []Kind{KindOpenAICompatible, KindAnthropic, KindOpenAI}

// DefaultBaseURL is the address a hosted kind has, and "" for the kind whose
// address has to be typed.
func DefaultBaseURL(kind Kind) string {
	switch kind {
	case KindOpenAI:
		return "https://api.openai.com/v1"
	case KindAnthropic:
		return "https://api.anthropic.com"
	}
	return ""
}
