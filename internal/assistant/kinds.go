package assistant

// Kind is which protocol a provider speaks.
type Kind string

const (
	KindFake             Kind = "fake"
	KindOpenAICompatible Kind = "openai_compatible"
	KindAnthropic        Kind = "anthropic"
	KindOpenAI           Kind = "openai"
	// KindClaudeCode runs the person's own signed-in Claude Code on the controller's
	// machine, to use their Claude subscription without Zoomies holding it.
	KindClaudeCode Kind = "claude_code"
)

// Kinds is every kind a person may create, in the order the page lists
// them. The fake is not among them: it exists for the demo and for tests.
var Kinds = []Kind{KindOpenAICompatible, KindAnthropic, KindOpenAI, KindClaudeCode}

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

// Subscription is whether a kind is somebody's own subscription, used through the
// vendor's own tool on the controller's machine. Such a provider has no address
// and no key for Zoomies to hold, and it belongs to the person who added it: the
// vendors' terms are that each person uses their own, so nobody else may.
func Subscription(kind Kind) bool { return kind == KindClaudeCode }

// SupportsTools is whether a kind can be given Eli's tools. The subscription
// kinds cannot: they run another program's own agent, with its tools turned off,
// and take a question as text.
func SupportsTools(kind Kind) bool { return !Subscription(kind) }
