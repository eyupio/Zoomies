package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
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

// Administration read reaches more than the checks that need it do, so an
// organisation App asks for it only when the request says so, and a request from a
// client written before the question existed gets the smaller set. A repository
// App holds Administration write already and is not lowered to read.
func TestManifestAsksForAdministrationReadOnlyWhenTheRequestSaysSo(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)
	administration := func(body map[string]any) string {
		t.Helper()
		resp := h.do(request{method: http.MethodPost, path: "/api/v1/installations/manifest", cookie: h.session(admin), body: body})
		resp.mustStatus(t, http.StatusOK, "building a manifest")
		var m struct {
			Permissions map[string]string `json:"default_permissions"`
		}
		if err := json.Unmarshal([]byte(resp.json(t)["manifest"].(string)), &m); err != nil {
			t.Fatalf("the manifest is not JSON: %v", err)
		}
		return m.Permissions["administration"]
	}

	if got := administration(map[string]any{"target": "acme", "target_type": "org"}); got != "" {
		t.Errorf("an organisation App asks for administration:%s without being asked to", got)
	}
	if got := administration(map[string]any{"target": "acme", "target_type": "org", "kennel_settings": true}); got != "read" {
		t.Errorf("administration = %q, want read", got)
	}
	if got := administration(map[string]any{"target": "acme/widgets", "target_type": "repo", "kennel_settings": true}); got != "write" {
		t.Errorf("a repository App asks for administration:%q, want write", got)
	}
}

// The user's click posts to Zoomies first. The controller validates the
// handshake, then preserves the POST across a redirect to GitHub so Android
// cannot dispatch the clicked URL to a different browser or app.
func TestManifestHandoffRedirectsTheValidatedPOSTToGitHub(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)
	const state = "state-1"
	const manifest = `{"name":"Zoomies (acme)"}`
	h.api.manifests.put(&pendingApp{
		state:      state,
		target:     "acme",
		targetType: store.TargetOrg,
		manifest:   manifest,
		postURL:    "https://github.com/organizations/acme/settings/apps/new",
		createdAt:  h.ctrl.Now(),
	})

	body := url.Values{"manifest": {manifest}}.Encode()
	resp := h.do(request{
		method:  http.MethodPost,
		path:    "/api/v1/installations/manifest/handoff?state=" + state,
		cookie:  h.session(admin),
		rawBody: body,
		headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
	})
	resp.mustStatus(t, http.StatusTemporaryRedirect, "handing the manifest to GitHub")
	if got := resp.header.Get("Location"); got != "https://github.com/organizations/acme/settings/apps/new?state=state-1" {
		t.Fatalf("Location = %q", got)
	}
	if got := resp.header.Get("Content-Security-Policy"); got != "" {
		t.Fatalf("the GitHub redirect carries the dashboard CSP %q", got)
	}

	bad := h.do(request{
		method:  http.MethodPost,
		path:    "/api/v1/installations/manifest/handoff?state=" + state,
		cookie:  h.session(admin),
		rawBody: url.Values{"manifest": {`{"name":"altered"}`}}.Encode(),
		headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
	})
	bad.mustStatus(t, http.StatusBadRequest, "refusing an altered manifest")
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

// Nothing but the migration wizard writes to a repository, so a manifest built
// for an operator who has not asked for the wizard must not ask GitHub for write
// access to contents, pull requests or workflows. The question is a field on the
// request, and an absent field has to mean no: a client written before the
// question existed must not silently get the larger permission set.
func TestManifestAsksForMigrationPermissionsOnlyWhenTheOperatorSaidSo(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("admin", store.RoleAdmin)

	permissions := func(body map[string]any) map[string]any {
		t.Helper()
		resp := h.do(request{method: http.MethodPost, path: "/api/v1/installations/manifest",
			cookie: h.session(admin), body: body})
		resp.mustStatus(t, http.StatusOK, "building a manifest")
		var m struct {
			DefaultPermissions map[string]any `json:"default_permissions"`
		}
		if err := json.Unmarshal([]byte(resp.json(t)["manifest"].(string)), &m); err != nil {
			t.Fatalf("the manifest is not JSON: %v", err)
		}
		return m.DefaultPermissions
	}

	without := permissions(map[string]any{"target": "acme"})
	with := permissions(map[string]any{"target": "acme", "migration": true})
	for _, name := range []string{"contents", "pull_requests", "workflows"} {
		if got, ok := without[name]; ok {
			t.Errorf("a request that did not ask for migration got %s:%v", name, got)
		}
		if got := with[name]; got != "write" {
			t.Errorf("a request that asked for migration got %s:%v, want write", name, got)
		}
	}
	// The runner permissions are the same either way: the question only adds.
	for _, name := range []string{"organization_self_hosted_runners", "actions", "metadata"} {
		if without[name] == nil || without[name] != with[name] {
			t.Errorf("%s = %v without migration and %v with it, want the same non-empty level", name, without[name], with[name])
		}
	}
}

// A code GitHub has not yet honoured is still good, and so must be the
// handshake that goes with it. Spending the state before the call to GitHub
// meant a controller with no egress yet -- the compose container behind a
// firewall that is not open yet -- answered the retry with "start the App
// creation again", against an App that already existed.
func TestExchangeKeepsTheHandshakeWhenGitHubCannotBeReached(t *testing.T) {
	// The address is on loopback, so the exchange only gets as far as the dial
	// when private egress is allowed.
	h := newHarness(t, allowPrivateEgress)
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

// The exchange dials an address the caller chose and returns what came back in
// its error, so without the outbound address guard it is a way to reach
// loopback, metadata and LAN services from the controller's network position.
// The guard has to hold wherever the address came from -- the request body or
// the handshake -- and the handshake must survive the refusal, because nothing
// was spent.
func TestExchangeRefusesAPrivateAPIBaseURLWithoutDialingIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string // api_base_url in the request; "" leaves it to the handshake
		pending string
	}{
		{"one in the request body", "http://%s/api/v3/", ""},
		{"one carried by the handshake", "", "http://%s/api/v3/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dialed atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				dialed.Add(1)
				http.Error(w, "internal admin console", http.StatusBadRequest)
			}))
			t.Cleanup(target.Close)
			host := strings.TrimPrefix(target.URL, "http://")

			h := newHarness(t)
			admin, _ := h.user("admin", store.RoleAdmin)
			pending := &pendingApp{state: "state-1", target: "acme", targetType: store.TargetOrg, createdAt: h.ctrl.Now()}
			if tc.pending != "" {
				pending.apiBaseURL = fmt.Sprintf(tc.pending, host)
			}
			h.api.manifests.put(pending)
			body := map[string]any{"code": "abc123", "state": "state-1"}
			if tc.body != "" {
				body["api_base_url"] = fmt.Sprintf(tc.body, host)
			}

			resp := h.do(request{method: http.MethodPost, path: "/api/v1/installations/manifest/exchange",
				cookie: h.session(admin), body: body})
			resp.mustStatus(t, http.StatusUnprocessableEntity, "an exchange aimed at a private address")
			if !strings.Contains(string(resp.body), "api_base_url") || !strings.Contains(string(resp.body), "security.allow_private_egress") {
				t.Errorf("the refusal must name the field and the setting that would allow it:\n%s", resp.body)
			}
			if strings.Contains(string(resp.body), "internal admin console") {
				t.Errorf("the response read back from the internal service:\n%s", resp.body)
			}
			if n := dialed.Load(); n != 0 {
				t.Errorf("the controller sent %d request(s) to a private address it should have refused", n)
			}
			if h.api.manifests.peek("state-1") == nil {
				t.Error("the handshake was thrown away by a refusal that spent nothing")
			}
		})
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
	for _, want := range []string{"App 4244", "do not survive a restart", "Existing App"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the private_key error does not say %q: %q", want, msg)
		}
	}
}
