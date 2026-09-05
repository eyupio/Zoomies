package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

const testPEM = "-----BEGIN RSA PRIVATE KEY-----\nmanifest\n-----END RSA PRIVATE KEY-----"

// TestManifestReturnCanFinishInAFreshTab is the shape of the App manifest flow
// as an operator actually experiences it.
//
// GitHub creates the App in a tab of its own and returns the operator to the
// setup URL there. That tab never saw the form the flow started from, so it
// knows the App and the installation and nothing else. The handshake this
// controller is holding knows the rest, and completing the connection must not
// depend on the browser repeating it back.
func TestManifestReturnCanFinishInAFreshTab(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)

	h.api.manifests.put(&pendingApp{
		state:         store.NewSecret(16),
		target:        "acme",
		targetType:    store.TargetOrg,
		apiBaseURL:    h.cfg.GitHub.APIBaseURL,
		appID:         4242,
		slug:          "zoomies-acme",
		pem:           testPEM,
		webhookSecret: "from-github",
		createdAt:     h.ctrl.Now(),
	})

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/installations", cookie: h.session(admin),
		body: map[string]any{"app_id": 4242, "installation_id": 99}})
	resp.mustStatus(t, http.StatusCreated, "recording an installation from the manifest flow")

	body := resp.json(t)
	if body["target"] != "acme" {
		t.Errorf("target = %v, want the one the manifest was built for", body["target"])
	}
	if body["target_type"] != string(store.TargetOrg) {
		t.Errorf("target_type = %v, want %q", body["target_type"], store.TargetOrg)
	}
}

// The pending handshake fills in what the browser left out; it never overrides
// what the browser said.
func TestManifestPendingDoesNotOverrideTheRequest(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)

	h.api.manifests.put(&pendingApp{
		state:      store.NewSecret(16),
		target:     "acme",
		targetType: store.TargetOrg,
		appID:      4243,
		pem:        testPEM,
		createdAt:  h.ctrl.Now(),
	})

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/installations", cookie: h.session(admin),
		body: map[string]any{"app_id": 4243, "installation_id": 100, "target": "acme/widgets", "target_type": "repo"}})
	resp.mustStatus(t, http.StatusCreated, "recording an installation with an explicit target")

	if got := resp.json(t)["target"]; got != "acme/widgets" {
		t.Errorf("target = %v, want the one the request named", got)
	}
}

// The manifest is posted by the browser, and the browser only posts where the
// page policy allows. A manifest built for a GitHub the policy does not name
// would be accepted here and refused there, and the refusal is a blank tab:
// so it is refused here instead, naming the setting that lets it through.
func TestManifestRefusesAGitHubTheBrowserCouldNotPostTo(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/installations/manifest", cookie: h.session(admin),
		body: map[string]any{"target": "acme", "api_base_url": "https://ghes.example.com/api/v3"}})
	resp.mustStatus(t, http.StatusUnprocessableEntity, "a manifest for an Enterprise host the policy does not name")

	var env errorEnvelope
	resp.into(t, &env)
	if len(env.Errors) != 1 || env.Errors[0].Field != "api_base_url" {
		t.Fatalf("errors = %+v, want one on api_base_url", env.Errors)
	}
	for _, want := range []string{"https://ghes.example.com", "github.api_base_url", "ZOOMIES_GITHUB_API_BASE_URL"} {
		if !strings.Contains(env.Errors[0].Message, want) {
			t.Errorf("the message does not say %q: %s", want, env.Errors[0].Message)
		}
	}

	// github.com is always allowed, and the post URL is the organisation
	// endpoint for an org target.
	ok := h.do(request{method: http.MethodPost, path: "/api/v1/installations/manifest", cookie: h.session(admin),
		body: map[string]any{"target": "acme"}})
	ok.mustStatus(t, http.StatusOK, "a manifest for github.com")
	if got := ok.json(t)["post_url"]; got != "https://github.com/organizations/acme/settings/apps/new" {
		t.Errorf("post_url = %v", got)
	}
}

// The Enterprise host the controller is configured against is allowed, since it
// is the one the page policy names.
func TestManifestForTheConfiguredEnterpriseServerIsBuilt(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.GitHub.APIBaseURL = "https://ghes.example.com/api/v3"
	})
	admin, _ := h.user("admin", store.RoleAdmin)

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/installations/manifest", cookie: h.session(admin),
		body: map[string]any{"target": "acme/widgets", "target_type": "repo"}})
	resp.mustStatus(t, http.StatusOK, "a manifest for the configured Enterprise host")
	if got := resp.json(t)["post_url"]; got != "https://ghes.example.com/settings/apps/new" {
		t.Errorf("post_url = %v", got)
	}
}

// A code GitHub has not yet honoured is still good, and so must be the
// handshake that goes with it. Spending the state before the call to GitHub
// meant a controller with no egress yet -- the compose container behind a
// firewall that is not open yet -- answered the retry with "start the App
// creation again", against an App that already existed.
func TestExchangeKeepsTheHandshakeWhenGitHubCannotBeReached(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)

	h.api.manifests.put(&pendingApp{
		state:      "state-1",
		target:     "acme",
		targetType: store.TargetOrg,
		// A port nothing listens on: the exchange fails before GitHub sees it.
		apiBaseURL: "http://127.0.0.1:1/api/v3/",
		createdAt:  h.ctrl.Now(),
	})

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/installations/manifest/exchange", cookie: h.session(admin),
		body: map[string]any{"code": "abc123", "state": "state-1"}})
	resp.mustStatus(t, http.StatusUnprocessableEntity, "an exchange GitHub never received")

	if h.api.manifests.peek("state-1") == nil {
		t.Fatal("the handshake was thrown away by a failure that spent nothing")
	}
}

// The last step, arriving after the credentials it relies on are gone, has to
// say so and say what to do instead; "paste the private key" on a step with no
// such field is a dead end.
func TestFinishingAfterTheCredentialsAreGoneSaysWhatHappened(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/installations", cookie: h.session(admin),
		body: map[string]any{"app_id": 4244, "installation_id": 77, "target": "acme", "target_type": "org", "private_key": ""}})
	resp.mustStatus(t, http.StatusUnprocessableEntity, "finishing without the pending credentials")

	var env errorEnvelope
	resp.into(t, &env)
	var msg string
	for _, f := range env.Errors {
		if f.Field == "private_key" {
			msg = f.Message
		}
	}
	for _, want := range []string{"App 4244", "do not survive a restart", "Use an App you already have"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the private_key error does not say %q: %q", want, msg)
		}
	}
}
