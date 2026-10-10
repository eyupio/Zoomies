package api

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

var providerReadTools = []string{"list_providers", "provider_pairings", "list_machines"}

// The provider tools are reads, so they are offered to every role and the routes
// behind them decide, exactly as list_hosts is. A viewer holds providers.read and
// machines.read, and sees what the Providers page shows it: never a credential.
func TestAViewerTokenReadsProvidersAndMachinesOverMCPWithoutAnyCredential(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	prov := h.provider("proxmox-lab")
	m := h.machine(prov, "zoomies-proxmox-lab-1")
	viewer := h.token("reader", store.RoleViewer)

	names := h.mcpToolNames(viewer)
	for _, tool := range providerReadTools {
		if !slices.Contains(names, tool) {
			t.Errorf("%s must be offered to a viewer token, got %v", tool, names)
		}
	}

	got := h.mcpTool(viewer, "list_providers", map[string]any{})
	if got.IsError {
		t.Fatalf("list_providers failed: %s", resultText(got))
	}
	text := resultText(got)
	var list struct {
		Items []struct {
			ID                    string `json:"id"`
			Name                  string `json:"name"`
			CredentialsConfigured bool   `json:"credentials_configured"`
			Owned                 int    `json:"owned"`
		} `json:"items"`
		ProvidersAvailable *bool `json:"providers_available"`
	}
	if err := json.Unmarshal([]byte(text), &list); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, text)
	}
	if len(list.Items) != 1 || list.Items[0].ID != prov.ID || !list.Items[0].CredentialsConfigured {
		t.Errorf("providers = %+v, want %s with its credential reported as configured", list.Items, prov.ID)
	}
	if list.ProvidersAvailable == nil {
		t.Errorf("list_providers must say whether this controller can rent at all: %s", text)
	}
	// The credential was sealed from this string; nothing a tool returns may carry
	// it, in the clear or by any other name.
	if strings.Contains(text, "prv-token-proxmox-lab") {
		t.Errorf("a provider's credential reached an MCP client: %s", text)
	}

	pairs := h.mcpTool(viewer, "provider_pairings", map[string]any{"pool_id": pool.ID})
	if pairs.IsError || !strings.Contains(resultText(pairs), prov.ID) || !strings.Contains(resultText(pairs), pool.ID) {
		t.Errorf("provider_pairings for the pool = %q (error %v)", resultText(pairs), pairs.IsError)
	}

	machines := h.mcpTool(viewer, "list_machines", map[string]any{"provider_id": prov.ID})
	if machines.IsError || !strings.Contains(resultText(machines), m.ID) {
		t.Errorf("list_machines = %q (error %v), want machine %s", resultText(machines), machines.IsError, m.ID)
	}
}

// A token narrowed to other resources is offered the tools and refused on the
// route, in the controller's own words: the scope it is missing, not "forbidden".
// providers:read and machines:read are separate scopes, and one does not open the
// other, so a token minted to watch providers cannot also list the machines'
// addresses.
func TestProviderToolsFollowTheTokensScopesOnTheRoute(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	prov := h.provider("proxmox-lab")
	h.machine(prov, "zoomies-proxmox-lab-1")

	poolsOnly := h.token("pools-only", store.RoleOperator, "pools:read")
	providersOnly := h.token("providers-only", store.RoleViewer, "providers:read")

	for tool, scope := range map[string]string{
		"list_providers":    "providers:read",
		"provider_pairings": "providers:read",
		"list_machines":     "machines:read",
	} {
		r := h.mcpTool(poolsOnly, tool, map[string]any{})
		if !r.IsError || !strings.Contains(resultText(r), scope) {
			t.Errorf("%s with a pools:read token = %q (error %v), want a refusal naming %s", tool, resultText(r), r.IsError, scope)
		}
	}
	// The token can still do what it was minted for.
	if r := h.mcpTool(poolsOnly, "list_pools", map[string]any{}); r.IsError || !strings.Contains(resultText(r), pool.ID) {
		t.Errorf("list_pools with a pools:read token = %q", resultText(r))
	}

	if r := h.mcpTool(providersOnly, "list_providers", map[string]any{}); r.IsError {
		t.Errorf("list_providers with a providers:read token was refused: %s", resultText(r))
	}
	if r := h.mcpTool(providersOnly, "list_machines", map[string]any{}); !r.IsError || !strings.Contains(resultText(r), "machines:read") {
		t.Errorf("list_machines with a providers:read token = %q (error %v), want a refusal naming machines:read", resultText(r), r.IsError)
	}
}

// No MCP tool configures, pauses or drains a provider or a machine. Those routes
// are an administrator's or an operator's and stay with a person, so a role that
// reaches them over REST still finds nothing here to reach them with.
func TestNoMCPToolReachesAProviderOrMachineWriteRoute(t *testing.T) {
	h := newHarness(t)
	admin := h.token("boss", store.RoleAdmin)
	for _, name := range h.mcpToolNames(admin) {
		if strings.Contains(name, "provider") || strings.Contains(name, "machine") {
			if !slices.Contains(providerReadTools, name) {
				t.Errorf("%s is an MCP tool about providers or machines and is not one of the read tools", name)
			}
		}
	}
}
