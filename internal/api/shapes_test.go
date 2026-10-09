package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/cryptox"
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
	// A host on a rung and a runner with a defaulted allocation, so the
	// fields that are omitted when empty are present to be checked: a check
	// that never sees them would pass on a document that does not have them.
	since := time.Now().Add(-time.Minute)
	if err := h.st.SetHostThrottle(h.ctx, host.ID, store.HostThrottle{
		Level: 1, Since: &since, ChangedAt: &since, Reason: "available memory is at or below the host's reserve",
	}, 0); err != nil {
		t.Fatalf("SetHostThrottle: %v", err)
	}
	run.AllocatedCPUs, run.AllocatedMemoryMB, run.AllocationSource = 1.87, 3968, store.AllocationFromHost
	if err := h.st.UpdateRunner(h.ctx, run); err != nil {
		t.Fatalf("UpdateRunner: %v", err)
	}
	u, _ := h.user("admin", store.RoleAdmin)
	cookie := h.session(u)
	prov := h.provider("proxmox-lab")
	h.machine(prov, "zoomies-mach-shapes")

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

	t.Run("UpdatesStatus", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/updates", cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "updates status")
		assertShape(t, doc, "UpdatesStatus", resp.body)
	})

	t.Run("UpdatesStatusNamingARelease", func(t *testing.T) {
		// The route above answers for a controller that has read no release list,
		// so the three objects that exist only once it has are null in it and
		// assertShape has nothing to say about their keys. The view is checked
		// against the document directly, filled in, as the orphan is below: it is
		// what the route and the event stream both render.
		at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
		due := at.Add(24 * time.Hour)
		raw, err := json.Marshal(controller.UpdatesView{
			Mode: "auto", Soak: "24h",
			Running:   controller.UpdatesRunning{Version: "1.3.0", Release: true},
			Latest:    &controller.UpdatesRelease{Tag: "v1.3.2", URL: "https://example.invalid/releases/v1.3.2", PublishedAt: at},
			Target:    &controller.UpdatesTarget{Tag: "v1.3.2", Newer: true, DueAt: &due},
			Reason:    "Waiting: v1.3.2 has been public for 6 hours and auto waits for 24; it can be taken in 17 hours.",
			CheckedAt: &at,
			Helper:    controller.UpdatesHelper{State: controller.HelperMissing, Reason: "No update helper is installed.", InstallCommand: "sudo zoomies updates helper install"},
			Controller: &controller.UpdatesAttempt{ID: "upd_abc", State: "failed", From: "1.3.0", To: "v1.3.2", Trigger: "manual",
				RequestedAt: at, FinishedAt: &due, Error: "the download failed"},
		})
		if err != nil {
			t.Fatalf("marshalling a status: %v", err)
		}
		assertShape(t, doc, "UpdatesStatus", raw)

		var parts struct {
			Running    json.RawMessage `json:"running"`
			Latest     json.RawMessage `json:"latest"`
			Target     json.RawMessage `json:"target"`
			Helper     json.RawMessage `json:"helper"`
			Controller json.RawMessage `json:"controller"`
		}
		if err := json.Unmarshal(raw, &parts); err != nil {
			t.Fatalf("reading the status back: %v", err)
		}
		assertShape(t, doc, "UpdatesRunning", parts.Running)
		assertShape(t, doc, "UpdatesRelease", parts.Latest)
		assertShape(t, doc, "UpdatesTarget", parts.Target)
		assertShape(t, doc, "UpdatesHelper", parts.Helper)
		assertShape(t, doc, "UpdatesAttempt", parts.Controller)
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

	t.Run("SupportBundle", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/diagnostics/bundle", cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "support bundle")
		assertShape(t, doc, "SupportBundle", resp.body)
	})

	t.Run("Provider", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/providers/" + prov.ID, cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "provider")
		assertShape(t, doc, "Provider", resp.body)
	})

	t.Run("ProviderKind", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/providers/kinds", cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "provider kinds")
		var listed struct {
			Items []json.RawMessage `json:"items"`
		}
		resp.into(t, &listed)
		if len(listed.Items) == 0 {
			t.Fatal("this build ships no provider driver, so the wizard would have nothing to offer")
		}
		assertShape(t, doc, "ProviderKind", listed.Items[0])

		var kind struct {
			Settings []json.RawMessage `json:"settings"`
		}
		if err := json.Unmarshal(listed.Items[0], &kind); err != nil {
			t.Fatalf("reading the driver's settings: %v", err)
		}
		if len(kind.Settings) == 0 {
			t.Fatal("the driver asks for nothing, so ProviderSetting is unchecked")
		}
		assertShape(t, doc, "ProviderSetting", kind.Settings[0])
		var setting providerSettingResponse
		if err := json.Unmarshal(kind.Settings[0], &setting); err != nil {
			t.Fatalf("reading a setting: %v", err)
		}
		if setting.Key == "" || setting.Label == "" {
			t.Errorf("a setting with no key or label cannot be rendered as a field: %+v", setting)
		}
	})

	t.Run("ProviderValidation", func(t *testing.T) {
		resp := h.do(request{method: http.MethodPost, path: "/api/v1/providers/validate", cookie: cookie,
			body: map[string]any{"kind": "fake", "name": "a draft", "settings": map[string]string{"zone": "zone-a"}}})
		resp.mustStatus(t, http.StatusOK, "validate a draft provider")
		assertShape(t, doc, "ProviderValidation", resp.body)
	})

	t.Run("ProviderCheck", func(t *testing.T) {
		resp := h.do(request{method: http.MethodPost, path: "/api/v1/providers/" + prov.ID + "/check", cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "provider check")
		assertShape(t, doc, "ProviderCheck", resp.body)
	})

	t.Run("ProviderDiscovery", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/providers/" + prov.ID + "/discovery", cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "provider discovery")
		assertShape(t, doc, "ProviderDiscovery", resp.body)
	})

	t.Run("ProviderOrphans", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/providers/" + prov.ID + "/orphans", cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "provider orphans")
		assertShape(t, doc, "ProviderOrphans", resp.body)
	})

	t.Run("Orphan", func(t *testing.T) {
		// An untracked resource is found by an ownership sweep against a live
		// provider, which no test harness has, so the type is checked against
		// the document directly. That is the contract either way: the view is
		// what the route renders.
		raw, err := json.Marshal(controller.OrphanView{Name: "zoomies-mach-k3f9q", Note: "look at it yourself"})
		if err != nil {
			t.Fatalf("marshalling an orphan: %v", err)
		}
		assertShape(t, doc, "Orphan", raw)
	})

	t.Run("Machine", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/machines", cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "list machines")
		var listed struct {
			Items []json.RawMessage `json:"items"`
		}
		resp.into(t, &listed)
		if len(listed.Items) == 0 {
			t.Fatal("no machines returned")
		}
		assertShape(t, doc, "Machine", listed.Items[0])
		var timeline struct {
			Timeline []json.RawMessage `json:"timeline"`
		}
		if err := json.Unmarshal(listed.Items[0], &timeline); err != nil {
			t.Fatalf("reading the machine's timeline: %v", err)
		}
		if len(timeline.Timeline) == 0 {
			t.Fatal("a machine reached no phase at all, so MachineTimelineEntry is unchecked")
		}
		assertShape(t, doc, "MachineTimelineEntry", timeline.Timeline[0])
		var phase machineTimelineResponse
		if err := json.Unmarshal(timeline.Timeline[0], &phase); err != nil {
			t.Fatalf("reading a phase: %v", err)
		}
		if phase.Phase == "" || phase.At.IsZero() {
			// A zero timestamp in a timeline reads as 1970, which is why a
			// phase a machine skipped is absent rather than empty.
			t.Errorf("a timeline entry names no phase or no time: %+v", phase)
		}
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

	// A Proxmox API token, in the shape a hypervisor actually issues one, so a
	// hit is a leak rather than a coincidence.
	const proxmoxToken = "zoomies@pve!ci=9f1c0e2a-7b44-4d18-9f0a-nobody-may-ever-see"
	created := h.do(request{method: http.MethodPost, path: "/api/v1/providers", cookie: cookie,
		body: map[string]any{"kind": "fake", "name": "proxmox-lab", "endpoint": "https://pve.example.com:8006",
			"credential": proxmoxToken, "settings": map[string]string{"zone": "zone-a"}}})
	created.mustStatus(t, http.StatusCreated, "create provider")
	var prov struct {
		ID string `json:"id"`
	}
	created.into(t, &prov)

	secrets := []string{tokenPlain, join.Token, "PRIVATE KEY", "webhook-secret", testKey, proxmoxToken}
	paths := []string{
		"/api/v1/installations",
		"/api/v1/installations/" + inst.ID,
		// A provider's credential reaches a hypervisor, and the row it is
		// sealed in is read by every page that lists providers.
		"/api/v1/providers",
		"/api/v1/providers/" + prov.ID,
		"/api/v1/tokens",
		"/api/v1/join-tokens",
		"/api/v1/users",
		"/api/v1/settings",
		"/api/v1/audit",
		// The bundle is on this list because it is the others put together:
		// a document assembled from secret-free sections is only secret-free
		// while every section still is, and this is what notices when one
		// stops being.
		"/api/v1/diagnostics/bundle",
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

// TestSecretsAreNeverInAFailure is the other side of the coin from
// TestSecretsAreNeverInAResponse, which walks eight admin reads on the success
// path. A response that succeeded has been thought about; the ones that leak
// are the ones nobody was looking at.
//
// It covers what a refusal answers with, what the fleet's own resources carry,
// what goes out on the event stream, what /metrics exposes, the audit rows
// written when something fails, and the controller's own log -- which is read
// by more people than the API is, shipped to wherever logs are shipped, and
// outlives the request that produced it.
func TestSecretsAreNeverInAFailure(t *testing.T) {
	h := newHarness(t, allowPrivateEgress)
	admin, _ := h.user("root", store.RoleAdmin)
	cookie := h.session(admin)

	// Distinctive plaintexts, so that a hit is a leak and not a coincidence.
	const (
		appKey     = "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAtheAppKeyNobodyMayEverSee\n-----END RSA PRIVATE KEY-----"
		keyMarker  = "theAppKeyNobodyMayEverSee"
		hookSecret = "webhook-hmac-nobody-may-ever-see"
		password   = "the-operators-own-password-12345"
		// The credential a Proxmox API token is: it reaches a hypervisor, so
		// a leak of it is a leak of every machine on the cluster.
		proxmoxToken = "zoomies@pve!ci=9f1c0e2a-7b44-4d18-9f0a-nobody-may-ever-see"
	)

	created := h.do(request{method: http.MethodPost, path: "/api/v1/installations", cookie: cookie,
		body: map[string]any{
			"app_id": h.gh.AppID(), "installation_id": h.gh.InstallationID(),
			"target": "acme", "target_type": "org", "api_base_url": h.gh.URL(),
			"private_key": appKey, "webhook_secret": hookSecret,
		}})
	created.mustStatus(t, http.StatusCreated, "create installation")
	var inst struct {
		ID string `json:"id"`
	}
	created.into(t, &inst)

	madeProvider := h.do(request{method: http.MethodPost, path: "/api/v1/providers", cookie: cookie,
		body: map[string]any{"kind": "fake", "name": "proxmox-lab", "endpoint": "https://pve.example.com:8006",
			"credential": proxmoxToken, "settings": map[string]string{"zone": "zone-a"}}})
	madeProvider.mustStatus(t, http.StatusCreated, "create provider")
	var prov struct {
		ID string `json:"id"`
	}
	madeProvider.into(t, &prov)

	pool := h.pool(&store.Installation{ID: inst.ID}, "linux-x64")
	hostID, agentToken := h.agentToken("vm-1")
	host, err := h.st.GetHost(h.ctx, hostID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	run := h.runner(pool, host, store.RunnerBusy)
	apiToken := h.token("ci", store.RoleAdmin)

	joinResp := h.do(request{method: http.MethodPost, path: "/api/v1/join-tokens", cookie: cookie,
		body: map[string]any{"ttl": "15m", "capacity": 2}})
	joinResp.mustStatus(t, http.StatusCreated, "create join token")
	var join createJoinTokenResponse
	joinResp.into(t, &join)

	// Spent here, so that the probe below is refused rather than quietly
	// enrolling a host and asserting nothing.
	spend := h.do(request{method: http.MethodPost, path: "/api/v1/agent/join",
		body: map[string]any{"protocol_version": 1, "join_token": join.Token, "name": "vm-spender",
			"capacity": 1, "os": "linux", "arch": "amd64", "version": "test"}})
	spend.mustStatus(t, http.StatusOK, "spending the join token")

	secrets := []struct {
		what  string
		value string
	}{
		{"the App private key", keyMarker},
		{"the installation's webhook secret", hookSecret},
		{"an operator's password", password},
		{"a host's agent token", agentToken},
		// The hash as well as the credential. A hash is as good as the token
		// to anyone who can replay it against the store's own lookup, and it
		// is the form that is actually on the row -- so it is what leaks when
		// a store row is rendered where a view belongs.
		{"a host's agent token hash", cryptox.HashToken(agentToken)},
		{"an API token", apiToken},
		{"a join token", join.Token},
		{"the instance encryption key", testKey},
		{"a provider's API token", proxmoxToken},
	}
	seen := func(t *testing.T, what string, body []byte) {
		t.Helper()
		for _, secret := range secrets {
			if secret.value != "" && strings.Contains(string(body), secret.value) {
				t.Errorf("%s carries %s", what, secret.what)
			}
		}
	}

	// --- What a refusal answers with. -----------------------------------
	t.Run("error bodies", func(t *testing.T) {
		// GitHub refusing everything, so the paths that talk to it fail.
		h.gh.SetError("/", http.StatusUnauthorized, "the App is not installed here")
		t.Cleanup(h.gh.ClearErrors)

		for _, probe := range []struct {
			what string
			req  request
			// reportsInBody marks the routes that answer 200 and describe the
			// failure in the payload rather than refusing outright. Verify is
			// one: "we asked GitHub and GitHub said no" is a successful check
			// with a bad answer, and the answer is where a quoted credential
			// would end up.
			reportsInBody bool
		}{
			// The key is in the request that is refused, which is the case
			// most likely to echo it back: a validator quoting what it was
			// given is an ordinary and helpful thing to write.
			{"a rejected installation carrying a key", request{method: http.MethodPost, path: "/api/v1/installations", cookie: cookie,
				body: map[string]any{"app_id": 0, "installation_id": 0, "target": "", "target_type": "",
					"private_key": appKey, "webhook_secret": hookSecret}}, false},
			{"a failed verify", request{method: http.MethodPost, path: "/api/v1/installations/" + inst.ID + "/verify", cookie: cookie}, true},
			// The credential is in the body that is refused, which is the case
			// most likely to echo it back: a validator quoting what it was
			// given is an ordinary and helpful thing to write.
			{"a rejected provider carrying a credential", request{method: http.MethodPost, path: "/api/v1/providers", cookie: cookie,
				body: map[string]any{"kind": "fake", "name": "", "credential": proxmoxToken, "settings": map[string]string{}}}, false},
			{"a wrong setup token", request{method: http.MethodPost, path: "/api/v1/auth/bootstrap",
				body: map[string]any{"username": "mallory", "password": password,
					"setup_token": h.setupToken() + "-wrong"}}, false},
			{"a wrong password", request{method: http.MethodPost, path: "/api/v1/auth/login",
				body: map[string]any{"username": "root", "password": password}}, false},
			{"a spent join token", request{method: http.MethodPost, path: "/api/v1/agent/join",
				body: map[string]any{"protocol_version": 1, "join_token": join.Token, "name": "vm-2",
					"capacity": 1, "os": "linux", "arch": "amd64", "version": "test"}}, false},
			{"an unrecognised agent token", request{method: http.MethodPost, path: "/api/v1/agent/heartbeat",
				token: agentToken + "-tampered", body: map[string]any{"protocol_version": 1}}, false},
			{"an unparseable settings patch", request{method: http.MethodPatch, path: "/api/v1/settings", cookie: cookie,
				rawBody: `{"scaling":{"max_runners":"` + appKey + `"}}`,
				headers: map[string]string{"Content-Type": "application/json"}}, false},
		} {
			resp := h.do(probe.req)
			switch {
			case probe.reportsInBody:
				if resp.status != http.StatusOK || !strings.Contains(string(resp.body), `"ok":false`) {
					t.Errorf("%s answered %d and did not report a failure, so this probe is not exercising one: %s",
						probe.what, resp.status, truncate(resp.body))
				}
			case resp.status < 400:
				t.Errorf("%s answered %d, so this probe is not exercising a failure path: %s",
					probe.what, resp.status, truncate(resp.body))
			}
			seen(t, probe.what+" ("+strconv.Itoa(resp.status)+")", resp.body)
		}
	})

	// --- What the fleet's own resources carry. ---------------------------
	t.Run("fleet resources", func(t *testing.T) {
		for _, path := range []string{
			"/api/v1/hosts", "/api/v1/hosts/" + hostID,
			"/api/v1/runners", "/api/v1/runners/" + run.ID,
			"/api/v1/pools", "/api/v1/pools/" + pool.ID,
			"/api/v1/jobs", "/api/v1/stats", "/api/v1/problems", "/api/v1/scaling-events",
			"/api/v1/providers", "/api/v1/providers/" + prov.ID, "/api/v1/machines",
		} {
			resp := h.do(request{method: http.MethodGet, path: path, cookie: cookie})
			resp.mustStatus(t, http.StatusOK, path)
			seen(t, path, resp.body)
		}
	})

	// --- What goes out on the event stream. ------------------------------
	t.Run("an event frame", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		frames, resp := h.openStream(t, ctx, "/api/v1/events", cookie, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("event stream status %d", resp.StatusCode)
		}

		// A host update is the frame most likely to carry a host's credential,
		// because the row it is rendered from holds the token hash.
		patched := h.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + hostID, cookie: cookie,
			body: map[string]any{"labels": map[string]string{"role": "rebuilt"}}})
		patched.mustStatus(t, http.StatusOK, "patch host")

		frame := await(t, frames, "a host frame", func(f sseFrame) bool {
			return strings.HasPrefix(f.event, "host.")
		})
		seen(t, "the "+frame.event+" frame", []byte(frame.data))
	})

	// --- What /metrics exposes. ------------------------------------------
	t.Run("metrics", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/metrics", token: apiToken})
		resp.mustStatus(t, http.StatusOK, "metrics")
		seen(t, "/metrics", resp.body)
	})

	// --- The audit rows the failures above wrote. ------------------------
	t.Run("audit rows written on failure", func(t *testing.T) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/audit?limit=200", cookie: cookie})
		resp.mustStatus(t, http.StatusOK, "audit")
		seen(t, "the audit log", resp.body)
		// And it is not empty, or it would have nothing to leak: the failed
		// sign-in above is the row that has to be there.
		if !strings.Contains(string(resp.body), "auth.login_failed") {
			t.Errorf("no sign-in was audited, so this asserted nothing: %s", truncate(resp.body))
		}
	})

	// --- The controller's own log, across everything above. ---------------
	t.Run("the controller log", func(t *testing.T) {
		logged := h.logs.text()
		if logged == "" {
			t.Fatal("nothing was logged, so this asserted nothing")
		}
		seen(t, "the controller log", []byte(logged))
	})
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

// A job's timeline is served straight from what the controller wrote, and with
// scheduler.auto_rerun on it writes a re-run the fleet decided on with the
// source "recovery". The published enum listed only the four an observer can
// be, so a strict client generated from the document would reject or mistype
// the timeline of exactly the jobs the fleet broke.
func TestTheJobTimelineSourceEnumNamesEverySourceTheControllerWrites(t *testing.T) {
	doc := loadSpec(t)
	components, _ := doc["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	event, _ := schemas["JobEvent"].(map[string]any)
	props, _ := event["properties"].(map[string]any)
	source, _ := props["source"].(map[string]any)
	enum, _ := source["enum"].([]any)

	got := map[string]bool{}
	for _, v := range enum {
		if s, ok := v.(string); ok {
			got[s] = true
		}
	}
	for _, want := range []string{"webhook", "poller", "agent", "controller", "recovery"} {
		if !got[want] {
			t.Errorf("JobEvent.source does not list %q, which the controller writes to a job's timeline", want)
		}
	}
}
