package github

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func decodeManifest(t *testing.T, o ManifestOptions) map[string]any {
	t.Helper()
	b, err := Manifest(o)
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}
	return m
}

func permissions(t *testing.T, m map[string]any) map[string]string {
	t.Helper()
	raw, ok := m["default_permissions"].(map[string]any)
	if !ok {
		t.Fatalf("default_permissions missing from %v", m)
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("permission %q is %T, want string", k, v)
		}
		out[k] = s
	}
	return out
}

func TestManifestOrgShape(t *testing.T) {
	m := decodeManifest(t, ManifestOptions{
		Name:         "zoomies-acme",
		URL:          "https://zoomies.example.com",
		WebhookURL:   "https://zoomies.example.com/webhooks/github",
		Organization: "acme",
		SetupURL:     "https://zoomies.example.com/setup/github",
	})

	if m["name"] != "zoomies-acme" || m["url"] != "https://zoomies.example.com" {
		t.Fatalf("identity fields wrong: %v", m)
	}
	if m["public"] != false {
		t.Fatalf("public = %v, want false: a fleet controller's App must not be installable by strangers", m["public"])
	}

	hook, ok := m["hook_attributes"].(map[string]any)
	if !ok {
		t.Fatalf("hook_attributes missing: %v", m)
	}
	if hook["url"] != "https://zoomies.example.com/webhooks/github" || hook["active"] != true {
		t.Fatalf("hook_attributes = %v", hook)
	}

	events, _ := m["default_events"].([]any)
	if len(events) != 1 || events[0] != "workflow_job" {
		t.Fatalf("default_events = %v, want exactly [workflow_job]", events)
	}

	want := map[string]string{
		"organization_self_hosted_runners": "write",
		"actions":                          "read",
		"metadata":                         "read",
	}
	if got := permissions(t, m); !maps.Equal(got, want) {
		t.Fatalf("permissions = %v, want %v", got, want)
	}

	// Both hops of the flow land back on the installer page: one with ?code=,
	// the other with ?installation_id=.
	if m["redirect_url"] != "https://zoomies.example.com/setup/github" ||
		m["setup_url"] != "https://zoomies.example.com/setup/github" {
		t.Fatalf("setup urls = %v / %v", m["redirect_url"], m["setup_url"])
	}
}

// GitHub validates the manifest key by key and rejects the whole thing with
// `"<key>" is not a permitted key` when it does not recognise one, so a
// well-meant addition here breaks App creation for everybody. These are the
// keys GitHub's manifest schema accepts.
func TestManifestSendsOnlyPermittedKeys(t *testing.T) {
	permitted := map[string]bool{
		"name": true, "url": true, "hook_attributes": true, "redirect_url": true,
		"callback_urls": true, "setup_url": true, "description": true, "public": true,
		"default_events": true, "default_permissions": true, "request_oauth_on_install": true,
		"setup_on_update": true,
	}
	m := decodeManifest(t, ManifestOptions{
		Name:         "zoomies-acme",
		URL:          "https://zoomies.example.com",
		WebhookURL:   "https://zoomies.example.com/webhooks/github",
		Organization: "acme",
		SetupURL:     "https://zoomies.example.com/setup/github",
	})
	for k := range m {
		if !permitted[k] {
			t.Errorf("manifest carries %q, which GitHub does not permit and will reject the whole manifest over", k)
		}
	}

	// hook_attributes has its own, much shorter, list. A "secret" here is the
	// tempting one: GitHub generates the secret itself and returns it from the
	// conversion, and naming one makes App creation fail outright.
	hook, ok := m["hook_attributes"].(map[string]any)
	if !ok {
		t.Fatalf("hook_attributes missing: %v", m)
	}
	for k := range hook {
		if k != "url" && k != "active" {
			t.Errorf("hook_attributes carries %q; GitHub permits only url and active", k)
		}
	}
}

func TestManifestRepoShape(t *testing.T) {
	m := decodeManifest(t, ManifestOptions{
		Name:       "zoomies-widgets",
		URL:        "https://zoomies.example.com",
		WebhookURL: "https://zoomies.example.com/webhooks/github",
	})
	want := map[string]string{
		"administration": "write",
		"actions":        "read",
		"metadata":       "read",
	}
	got := permissions(t, m)
	if !maps.Equal(got, want) {
		t.Fatalf("permissions = %v, want %v", got, want)
	}
	// A repo-scoped App has no business holding org-wide runner administration.
	if _, ok := got["organization_self_hosted_runners"]; ok {
		t.Fatal("repo manifest asked for organization_self_hosted_runners")
	}
}

func TestManifestValidation(t *testing.T) {
	base := ManifestOptions{
		Name:       "zoomies",
		URL:        "https://zoomies.example.com",
		WebhookURL: "https://zoomies.example.com/webhooks/github",
	}
	tests := []struct {
		name string
		mut  func(*ManifestOptions)
		want string
	}{
		{"no name", func(o *ManifestOptions) { o.Name = " " }, "name is required"},
		{"long name", func(o *ManifestOptions) { o.Name = strings.Repeat("z", 40) }, "at most 34"},
		{"no url", func(o *ManifestOptions) { o.URL = "" }, "homepage url"},
		{"no webhook", func(o *ManifestOptions) { o.WebhookURL = "" }, "webhook url"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := base
			tc.mut(&o)
			_, err := Manifest(o)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestManifestURL(t *testing.T) {
	tests := []struct {
		api, org, want string
	}{
		{"https://api.github.com", "acme", "https://github.com/organizations/acme/settings/apps/new"},
		{"https://api.github.com", "", "https://github.com/settings/apps/new"},
		{"https://ghes.example.com/api/v3/", "acme", "https://ghes.example.com/organizations/acme/settings/apps/new"},
		{"https://ghes.example.com/api/v3/", "", "https://ghes.example.com/settings/apps/new"},
	}
	for _, tc := range tests {
		if got := ManifestURL(tc.api, tc.org); got != tc.want {
			t.Errorf("ManifestURL(%q, %q) = %q, want %q", tc.api, tc.org, got, tc.want)
		}
	}
}

func TestInstallURL(t *testing.T) {
	if got := InstallURL("https://github.com/apps/zoomies-acme/"); got != "https://github.com/apps/zoomies-acme/installations/new" {
		t.Fatalf("InstallURL = %q", got)
	}
	if got := InstallURL("  "); got != "" {
		t.Fatalf("InstallURL of nothing = %q, want empty", got)
	}
}

func TestExchangeManifestCode(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":             77,
			"slug":           "zoomies-acme",
			"name":           "Zoomies Acme",
			"pem":            "-----BEGIN RSA PRIVATE KEY-----\nfake\n-----END RSA PRIVATE KEY-----\n",
			"webhook_secret": "whsec",
			"client_id":      "Iv1.deadbeef",
			"client_secret":  "cs",
			"html_url":       "https://github.com/apps/zoomies-acme",
		})
	}))
	defer srv.Close()

	creds, err := ExchangeManifestCode(context.Background(), srv.URL, "abc123")
	if err != nil {
		t.Fatalf("ExchangeManifestCode: %v", err)
	}
	if gotPath != "POST /api/v3/app-manifests/abc123/conversions" {
		t.Fatalf("posted to %q", gotPath)
	}
	if creds.AppID != 77 || creds.Slug != "zoomies-acme" || creds.WebhookSecret != "whsec" {
		t.Fatalf("credentials = %+v", creds)
	}
	if !strings.Contains(creds.PEM, "BEGIN RSA PRIVATE KEY") {
		t.Fatal("private key not carried through")
	}
	if InstallURL(creds.HTMLURL) != "https://github.com/apps/zoomies-acme/installations/new" {
		t.Fatalf("install url = %q", InstallURL(creds.HTMLURL))
	}
}

func TestExchangeManifestCodeErrors(t *testing.T) {
	t.Run("empty code", func(t *testing.T) {
		if _, err := ExchangeManifestCode(context.Background(), "https://api.github.com", " "); err == nil {
			t.Fatal("empty code accepted")
		}
	})

	t.Run("expired code", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}))
		defer srv.Close()
		_, err := ExchangeManifestCode(context.Background(), srv.URL, "stale")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("got %v, want ErrNotFound", err)
		}
		if !strings.Contains(err.Error(), "one hour") {
			t.Fatalf("error does not explain the expiry: %v", err)
		}
	})

	t.Run("proxy answered", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<html>login page</html>"))
		}))
		defer srv.Close()
		_, err := ExchangeManifestCode(context.Background(), srv.URL, "abc")
		if err == nil || !strings.Contains(err.Error(), "proxy") {
			t.Fatalf("got %v, want a proxy hint", err)
		}
	})

	t.Run("empty credentials", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"id":0}`))
		}))
		defer srv.Close()
		if _, err := ExchangeManifestCode(context.Background(), srv.URL, "abc"); err == nil {
			t.Fatal("credential-less response accepted")
		}
	})
}

func TestManifestPermissionsAreMinimal(t *testing.T) {
	// Every permission in the manifest has to be one Zoomies actually uses.
	allowed := []string{"actions", "metadata", "administration", "organization_self_hosted_runners"}
	for _, org := range []string{"", "acme"} {
		m := decodeManifest(t, ManifestOptions{
			Name: "zoomies", URL: "https://z.example", WebhookURL: "https://z.example/w",
			Organization: org,
		})
		for name := range permissions(t, m) {
			if !slices.Contains(allowed, name) {
				t.Errorf("manifest for org=%q asks for unused permission %q", org, name)
			}
		}
	}
}
