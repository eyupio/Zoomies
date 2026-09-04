package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/eyupio/zoomies/internal/store"
)

// The shape tests read api/openapi.yaml and check the responses against it.
//
// The UI's TypeScript client is generated from that document, so a response
// with a field the document does not have is a field the UI cannot see, and a
// missing one is a runtime error in a browser rather than a test failure here.
// Checking it directly is cheaper than discovering it in Playwright.

// schemaShape is what a test needs from a schema: which keys it allows and
// which it promises.
type schemaShape struct {
	properties map[string]bool
	required   []string
}

func loadSpec(t *testing.T) map[string]any {
	t.Helper()
	raw, err := openapiSpec()
	if err != nil {
		t.Fatalf("openapiSpec: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing the OpenAPI document: %v", err)
	}
	return doc
}

// shapeOf resolves a schema by name, following $ref and flattening allOf.
func shapeOf(t *testing.T, doc map[string]any, name string) schemaShape {
	t.Helper()
	components, _ := doc["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	schema, ok := schemas[name].(map[string]any)
	if !ok {
		t.Fatalf("the OpenAPI document has no schema named %q", name)
	}
	out := schemaShape{properties: map[string]bool{}}
	collectShape(t, doc, schema, &out)
	return out
}

func collectShape(t *testing.T, doc map[string]any, schema map[string]any, out *schemaShape) {
	t.Helper()
	if ref, ok := schema["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		components, _ := doc["components"].(map[string]any)
		schemas, _ := components["schemas"].(map[string]any)
		if target, ok := schemas[name].(map[string]any); ok {
			collectShape(t, doc, target, out)
		}
		return
	}
	if allOf, ok := schema["allOf"].([]any); ok {
		for _, part := range allOf {
			if m, ok := part.(map[string]any); ok {
				collectShape(t, doc, m, out)
			}
		}
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		for k := range props {
			out.properties[k] = true
		}
	}
	if required, ok := schema["required"].([]any); ok {
		for _, r := range required {
			if s, ok := r.(string); ok && !slices.Contains(out.required, s) {
				out.required = append(out.required, s)
			}
		}
	}
}

// assertShape checks one JSON object against a schema: no key the document does
// not describe, and every key it says is required.
func assertShape(t *testing.T, doc map[string]any, schemaName string, raw json.RawMessage) {
	t.Helper()
	shape := shapeOf(t, doc, schemaName)
	if len(shape.properties) == 0 {
		t.Fatalf("%s: no properties were resolved from the document, so this check would pass on anything", schemaName)
	}

	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("%s: response is not an object: %v", schemaName, err)
	}
	for key := range obj {
		if !shape.properties[key] {
			t.Errorf("%s: the response has %q, which is not in the OpenAPI document; "+
				"a field the document does not describe is one the UI's generated client cannot see", schemaName, key)
		}
	}
	for _, key := range shape.required {
		if _, ok := obj[key]; !ok {
			t.Errorf("%s: the response is missing the required field %q", schemaName, key)
		}
	}
}

func TestResponsesMatchTheSpecShapes(t *testing.T) {
	doc := loadSpec(t)
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	run := h.runner(pool, host, store.RunnerBusy)
	job := h.job(pool, store.JobInProgress)
	if err := h.st.AssignRunnerJob(h.ctx, run.ID, job.ID); err != nil {
		t.Fatalf("AssignRunnerJob: %v", err)
	}
	u, _ := h.user("admin", store.RoleAdmin)
	cookie := h.session(u)

	t.Run("Pool", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/pools", cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "list pools")
		var listed struct {
			Items []json.RawMessage `json:"items"`
		}
		resp.into(t, &listed)
		if len(listed.Items) == 0 {
			t.Fatal("no pools returned")
		}
		assertShape(t, doc, "Pool", listed.Items[0])
	})

	t.Run("Runner", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/runners", cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "list runners")
		var listed struct {
			Items  []json.RawMessage `json:"items"`
			Total  *int              `json:"total"`
			Limit  *int              `json:"limit"`
			Offset *int              `json:"offset"`
		}
		resp.into(t, &listed)
		if listed.Total == nil || listed.Limit == nil || listed.Offset == nil {
			t.Fatalf("the page envelope is incomplete: %s", truncate(resp.body))
		}
		if len(listed.Items) == 0 {
			t.Fatal("no runners returned")
		}
		assertShape(t, doc, "Runner", listed.Items[0])
	})

	t.Run("RunnerDetail", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/runners/" + run.ID, cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "runner detail")
		assertShape(t, doc, "RunnerDetail", resp.body)
	})

	t.Run("Job", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/jobs/" + job.ID, cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "job")
		assertShape(t, doc, "Job", resp.body)
	})

	t.Run("Host", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/hosts/" + host.ID, cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "host")
		assertShape(t, doc, "Host", resp.body)
	})

	t.Run("Installation", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/installations/" + inst.ID, cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "installation")
		assertShape(t, doc, "Installation", resp.body)
	})

	t.Run("Identity", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/auth/session", cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "session")
		assertShape(t, doc, "Identity", resp.body)
	})

	t.Run("Meta", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/meta"})
		resp.mustStatus(t, http.StatusOK, "meta")
		assertShape(t, doc, "Meta", resp.body)
	})

	t.Run("Stats", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/stats", cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "stats")
		assertShape(t, doc, "Stats", resp.body)
	})

	t.Run("Problem", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/problems", cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "problems")
		var body struct {
			OK    *bool             `json:"ok"`
			Items []json.RawMessage `json:"items"`
		}
		resp.into(t, &body)
		if body.OK == nil {
			t.Fatal("the problems response has no ok flag")
		}
		if len(body.Items) == 0 {
			t.Skip("this instance has nothing to report, so there is no Problem to check")
		}
		for _, item := range body.Items {
			assertShape(t, doc, "Problem", item)
		}
	})

	t.Run("User", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/users/" + u.ID, cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "user")
		assertShape(t, doc, "User", resp.body)
	})

	t.Run("Settings", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/settings", cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "settings")
		assertShape(t, doc, "Settings", resp.body)
	})

	t.Run("ErrorEnvelope", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/pools/pool_nope", cookie: cookie})
		resp.mustStatus(t, http.StatusNotFound, "missing pool")
		assertShape(t, doc, "ErrorEnvelope", resp.body)
	})
}

// TestSecretsAreNeverInAResponse is the one property no schema check can
// enforce, because a leaked secret would arrive in a field that does exist.
func TestSecretsAreNeverInAResponse(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	u, _ := h.user("admin", store.RoleAdmin)
	cookie := h.session(u)

	// Mint credentials whose plaintext we know, then look for it everywhere.
	tokenPlain := h.token("ci", store.RoleAdmin)
	joinResp := h.do(request{method: http.MethodPost, path: "/api/v1/join-tokens", cookie: cookie,
		body: map[string]any{"ttl": "15m", "capacity": 2}})
	joinResp.mustStatus(t, http.StatusCreated, "create join token")
	var join createJoinTokenResponse
	joinResp.into(t, &join)
	if join.Token == "" {
		t.Fatal("the join token was not returned at creation, which is the one time it can be")
	}
	if !strings.Contains(join.Command, join.Token) {
		t.Error("the ready-to-paste command does not carry the token")
	}

	secrets := []string{tokenPlain, join.Token, "PRIVATE KEY", "webhook-secret", testKey}
	paths := []string{
		"/api/v1/installations",
		"/api/v1/installations/" + inst.ID,
		"/api/v1/tokens",
		"/api/v1/join-tokens",
		"/api/v1/users",
		"/api/v1/settings",
		"/api/v1/audit",
	}
	for _, path := range paths {
		resp := h.do(request{method: http.MethodGet, path: path, cookie: cookie})
		resp.mustStatus(t, http.StatusOK, path)
		for _, secret := range secrets {
			if secret != "" && strings.Contains(string(resp.body), secret) {
				t.Errorf("%s leaked a secret (%q...)", path, secret[:min(8, len(secret))])
			}
		}
		// Nor the hashes: they are as good as the credential for an attacker
		// who can replay them against the store's own lookup.
		for _, field := range []string{"token_hash", "password_hash", "private_key", "webhook_secret"} {
			if strings.Contains(string(resp.body), `"`+field+`"`) {
				t.Errorf("%s has a %s field", path, field)
			}
		}
	}
}

// TestTokenIsShownExactlyOnce is the other half of that: the value exists in a
// response at creation and never again.
func TestTokenIsShownExactlyOnce(t *testing.T) {
	h := newHarness(t)
	u, _ := h.user("admin", store.RoleAdmin)
	cookie := h.session(u)

	created := h.do(request{method: http.MethodPost, path: "/api/v1/tokens", cookie: cookie,
		body: map[string]any{"name": "prometheus", "role": "viewer", "expires_in": "720h"}})
	created.mustStatus(t, http.StatusCreated, "create token")

	var token createdTokenResponse
	created.into(t, &token)
	if token.Token == "" {
		t.Fatal("the token value was not returned at creation")
	}
	if token.Prefix == "" || !strings.HasPrefix(token.Token, token.Prefix) {
		t.Errorf("the prefix does not identify the token: %q vs %q", token.Prefix, token.Token)
	}
	if token.ExpiresAt == nil {
		t.Error("expires_in was given but no expiry was recorded")
	}

	listed := h.do(request{method: http.MethodGet, path: "/api/v1/tokens", cookie: cookie})
	listed.mustStatus(t, http.StatusOK, "list tokens")
	if strings.Contains(string(listed.body), token.Token) {
		t.Fatal("the token list contains the token value")
	}

	// And it works, once.
	use := h.do(request{method: http.MethodGet, path: "/api/v1/pools", token: token.Token})
	use.mustStatus(t, http.StatusOK, "use the token")

	revoke := h.do(request{method: http.MethodDelete, path: "/api/v1/tokens/" + token.ID, cookie: cookie})
	revoke.mustStatus(t, http.StatusNoContent, "revoke")
	after := h.do(request{method: http.MethodGet, path: "/api/v1/pools", token: token.Token})
	after.mustStatus(t, http.StatusUnauthorized, "use a revoked token")
}
