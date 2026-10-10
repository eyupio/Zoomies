package controller

import (
	"errors"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/assistant"
	"github.com/eyupio/zoomies/internal/redact"
)

const leakyToken = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"

// What a tool returns is read for credentials and email addresses before the model
// is shown it, and only the count of what was hidden is kept.
func TestAToolResultIsRedactedBeforeTheModelSeesIt(t *testing.T) {
	box := &fakeBox{results: map[string]string{
		"get_runner_log": "cloning with " + leakyToken + "\nAuthor: Ada <ada@example.com>\nDB_PASSWORD=correct-horse-battery",
	}}
	p := &scriptedProvider{rounds: [][]assistant.Event{
		{call("c1", "get_runner_log"), {Done: true}},
		{{Delta: "Done."}, {Done: true}},
	}}
	chat := newChat(p, box, "get_runner_log")
	drainChat(t, chat)

	seen := p.asked[1].Messages[2].Content
	for _, leaked := range []string{leakyToken, "ada@example.com", "correct-horse-battery"} {
		if strings.Contains(seen, leaked) {
			t.Errorf("the model was shown %q:\n%s", leaked, seen)
		}
	}
	if !strings.Contains(seen, "DB_PASSWORD="+redact.Credential) || !strings.Contains(seen, redact.Email) {
		t.Errorf("the model is not told something was hidden:\n%s", seen)
	}
	if got := chat.Redactions(); got.Credentials != 2 || got.Emails != 1 {
		t.Errorf("redactions = %+v, want two credentials and one email", got)
	}
}

// A tool that fails says why in words of its own, and those can carry a credential
// too: a request that echoed an address with a password in it.
func TestAToolErrorIsRedactedToo(t *testing.T) {
	box := &fakeBox{err: errors.New("GET https://bot:hunter2hunter2@api.example.com failed")}
	p := &scriptedProvider{rounds: [][]assistant.Event{
		{call("c1", "list_jobs"), {Done: true}},
		{{Delta: "It failed."}, {Done: true}},
	}}
	chat := newChat(p, box, "list_jobs")
	drainChat(t, chat)
	if seen := p.asked[1].Messages[2].Content; strings.Contains(seen, "hunter2hunter2") {
		t.Errorf("the model was shown the password:\n%s", seen)
	}
	if chat.Redactions().Credentials != 1 {
		t.Errorf("redactions = %+v", chat.Redactions())
	}
}

// A result is redacted before it is cut, so a credential that straddles the cut
// is gone whole and not left as a recognisable start.
func TestACredentialAtTheCutIsHiddenWholeAndNotLeftAsAStart(t *testing.T) {
	padding := strings.Repeat("x", assistantToolResultBytes-10)
	box := &fakeBox{results: map[string]string{"get_runner_log": padding + leakyToken + " and more after it"}}
	p := &scriptedProvider{rounds: [][]assistant.Event{
		{call("c1", "get_runner_log"), {Done: true}},
		{{Delta: "ok"}, {Done: true}},
	}}
	chat := newChat(p, box, "get_runner_log")
	drainChat(t, chat)
	seen := p.asked[1].Messages[2].Content
	if strings.Contains(seen, "ghp_") {
		t.Errorf("the start of a token reached the model: %q", seen[len(seen)-300:])
	}
	if chat.Redactions().Credentials != 1 {
		t.Errorf("redactions = %+v", chat.Redactions())
	}
}

// A result with nothing to hide is passed on as it was, and counts nothing.
func TestAToolResultWithNothingToHideIsUntouched(t *testing.T) {
	box := &fakeBox{results: map[string]string{"fleet_status": `{"runners":3,"input_tokens":1234}`}}
	p := &scriptedProvider{rounds: [][]assistant.Event{
		{call("c1", "fleet_status"), {Done: true}},
		{{Delta: "Three."}, {Done: true}},
	}}
	chat := newChat(p, box, "fleet_status")
	drainChat(t, chat)
	if seen := p.asked[1].Messages[2].Content; !strings.Contains(seen, `{"runners":3,"input_tokens":1234}`) {
		t.Errorf("the result was changed:\n%s", seen)
	}
	if chat.Redactions().Total() != 0 {
		t.Errorf("redactions = %+v", chat.Redactions())
	}
}
