package provider

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A provider's refusal is shown to an administrator on the settings page and
// kept on the row, so it says what went wrong in words and never carries the
// request, whose headers hold the key.
func TestStatusErrorsNameTheCauseAndNotTheRequest(t *testing.T) {
	for _, tt := range []struct {
		status int
		body   string
		want   string
	}{
		{401, `{"error":{"message":"Incorrect API key provided: sk-abc. Authorization: Bearer sk-abc"}}`, "the provider refused the key"},
		{403, `{"error":{"message":"forbidden"}}`, "the provider refused the key"},
		{404, `{"error":{"message":"The model 'gpt-nope' does not exist"}}`, "no model named"},
		{429, `{"error":{"message":"slow down"}}`, "rate limited"},
		{500, `{"error":{"message":"upstream exploded"}}`, "500: upstream exploded"},
		{503, `not json`, "503"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tt.status)
			w.Write([]byte(tt.body))
		}))
		resp, err := http.Get(srv.URL)
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		got := StatusError(resp, true)
		if got == nil || !strings.Contains(got.Error(), tt.want) {
			t.Errorf("%d: got %v, want it to contain %q", tt.status, got, tt.want)
		}
		if got != nil && (strings.Contains(got.Error(), "sk-abc") || strings.Contains(got.Error(), "Authorization")) {
			t.Errorf("%d: the error carries the key or the header: %v", tt.status, got)
		}
	}
}

func TestStatusErrorIsNilForASuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	resp, _ := http.Get(srv.URL)
	if err := StatusError(resp, true); err != nil {
		t.Errorf("got %v", err)
	}
}
