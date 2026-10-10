package mention

import "testing"

func TestAMentionIsOnlyACommandWhenItStartsALineOutsideQuotesAndFences(t *testing.T) {
	for _, text := range []string{"> @eli fix this", "```\n@eli fix this\n```", "~~~\n@zoomies repair\n~~~", "someone said @eli fix", "@elizabeth fix", "@eli hello"} {
		if _, ok := Command(text, ""); ok {
			t.Errorf("triggered %q", text)
		}
	}
	for _, text := range []string{"@eli fix this PR", "@zoomies fix this issue", "@Eli repair failing tests", "@zoomies[bot] fix", "thanks\n@eli fix it"} {
		if _, ok := Command(text, ""); !ok {
			t.Errorf("missed %q", text)
		}
	}
}

func TestTheCommandIsTheVerbAndWhatFollowsIt(t *testing.T) {
	got, ok := Command("@eli fix the failing test", "")
	if !ok || got != "fix the failing test" {
		t.Errorf("got %q, %v", got, ok)
	}
}

// GitHub autocompletes and links the App's real handle, not "eli", so that is
// the name people actually type once they pick the bot from the suggestions.
func TestTheAppsOwnHandleStartsARepair(t *testing.T) {
	for _, text := range []string{"@zoomies-eyupio2 fix this", "@Zoomies-Eyupio2[bot] repair the test"} {
		if _, ok := Command(text, "zoomies-eyupio2"); !ok {
			t.Errorf("%q should start a repair when the App's slug is zoomies-eyupio2", text)
		}
		if _, ok := Command(text, ""); ok {
			t.Errorf("%q should not start a repair when no slug is known", text)
		}
	}
	if _, ok := Command("@zoomies-eyupio22 fix", "zoomies-eyupio2"); ok {
		t.Error("a longer handle that merely starts with the slug must not match")
	}
}

// A slug is operator-supplied text. It must be matched as written, never read
// as a pattern.
func TestASlugIsNeverReadAsAPattern(t *testing.T) {
	if _, ok := Command("@anything fix", ".*"); ok {
		t.Error("a slug of .* matched an unrelated handle")
	}
}
