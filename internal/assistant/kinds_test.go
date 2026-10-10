package assistant

import "testing"

// A subscription kind is somebody's own, and cannot be given tools; every kind
// that speaks to an API with a key is shared and can. The controller's rules rest
// on these two answers, so they are held here.
func TestSubscriptionKindsAreOwnedAndHaveNoTools(t *testing.T) {
	for _, k := range Kinds {
		sub := k == KindClaudeCode || k == KindCodex || k == KindCopilot
		if Subscription(k) != sub || SupportsTools(k) == sub {
			t.Errorf("%s: subscription = %v, tools = %v", k, Subscription(k), SupportsTools(k))
		}
	}
	if Subscription(KindFake) || !SupportsTools(KindFake) {
		t.Error("the fake model is not somebody's subscription")
	}
	for _, k := range []Kind{KindClaudeCode, KindCodex, KindCopilot} {
		if !Subscription(k) || DefaultBaseURL(k) != "" {
			t.Errorf("%s: a subscription has no address", k)
		}
	}
}
