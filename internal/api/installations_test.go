package api

import (
	"net/http"
	"testing"

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
