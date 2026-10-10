package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// What was typed or pasted is read for credentials and email addresses before it
// goes to the model, the answer says how many were hidden, and the audit row keeps
// the counts and never what was hidden.
func TestWhatIsSentToTheModelHasNoCredentialsAndNoEmailAddressesInIt(t *testing.T) {
	h, cookie, srv := chatHarness(t)
	const token = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	resp := h.do(request{method: http.MethodPost, path: assistantChat, cookie: cookie, readStream: true, body: chatBody(
		"user", "The job failed with GITHUB_TOKEN="+token+" for ada@example.com",
		"assistant", "Which step?",
		"user", "Same again with "+token+" from bob@example.org",
	)})
	resp.mustStatus(t, http.StatusOK, "asking")
	var done map[string]any
	for _, f := range frames(t, resp.body) {
		if f.kind == "done" {
			done = f.data
		}
	}
	// Counted: what is new in the last question, a credential and an email address.
	// The first question, sent again as history, is hidden as well and not counted twice.
	redacted, _ := done["redacted"].(map[string]any)
	if redacted["credentials"] != float64(1) || redacted["emails"] != float64(1) {
		t.Errorf("done = %v", done)
	}

	var sent string
	for _, r := range srv.Requests() {
		if r.Path == "/v1/chat/completions" {
			b, _ := json.Marshal(r.Body)
			sent += string(b)
		}
	}
	for _, leaked := range []string{token, "ada@example.com", "bob@example.org"} {
		if strings.Contains(sent, leaked) {
			t.Errorf("the model was sent %q: %s", leaked, sent)
		}
	}
	if !strings.Contains(sent, "[redacted credential]") || !strings.Contains(sent, "[redacted email]") || !strings.Contains(sent, "The job failed with") {
		t.Errorf("the model was not sent the question with the hidden parts marked: %s", sent)
	}

	rows, _, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{"assistant.chat"}}, store.Page{Limit: 5})
	if len(rows) != 1 {
		t.Fatalf("%d audit rows", len(rows))
	}
	if !strings.Contains(rows[0].After, `"credentials_hidden_count":1`) || !strings.Contains(rows[0].After, `"emails_hidden_count":1`) {
		t.Fatalf("the audit row does not count what was hidden: %s", rows[0].After)
	}
	if strings.Contains(rows[0].After, "ghp_") || strings.Contains(rows[0].After, "example.com") {
		t.Errorf("the audit row carries what was hidden: %s", rows[0].After)
	}
}

// An ordinary question is sent as it was, and says nothing was hidden.
func TestAQuestionWithNothingToHideIsSentAsItWasAndCountsNothing(t *testing.T) {
	h, cookie, srv := chatHarness(t)
	resp := h.do(request{method: http.MethodPost, path: assistantChat, cookie: cookie, readStream: true, body: chatBody("user", "Why is runner-7 idle?")})
	resp.mustStatus(t, http.StatusOK, "asking")
	var done map[string]any
	for _, f := range frames(t, resp.body) {
		if f.kind == "done" {
			done = f.data
		}
	}
	if redacted, _ := done["redacted"].(map[string]any); redacted["credentials"] != float64(0) || redacted["emails"] != float64(0) {
		t.Errorf("done = %v", done)
	}
	var sent string
	for _, r := range srv.Requests() {
		if r.Path == "/v1/chat/completions" {
			b, _ := json.Marshal(r.Body)
			sent += string(b)
		}
	}
	if !strings.Contains(sent, "Why is runner-7 idle?") || strings.Contains(sent, "[redacted") {
		t.Errorf("the model was sent %s", sent)
	}
}
