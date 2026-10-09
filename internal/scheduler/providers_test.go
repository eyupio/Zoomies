package scheduler

import (
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

func TestAProviderAndAPoolAgreeOnlyWhenBothSelectorsAllowIt(t *testing.T) {
	pool := &store.Pool{Name: "linux-large", Backend: store.BackendDocker, Labels: store.StringSlice{"linux", "x64", "tier=large"}}
	prov := &store.Provider{Name: "proxmox-lab", Kind: store.ProviderProxmox, MachineLabels: store.StringMap{"site": "garage"}}

	tests := []struct {
		name         string
		providerSel  map[string]string
		poolSel      map[string]string
		wantAgrees   bool
		wantBy       string
		wantWhyIn    string
		wantFixInStr string
	}{
		{name: "neither side restricts anything", wantAgrees: true},
		{name: "the provider asks for a label the pool carries", providerSel: map[string]string{"linux": ""}, wantAgrees: true},
		{name: "the provider asks for a key=value label the pool carries", providerSel: map[string]string{"tier": "large"}, wantAgrees: true},
		{name: "the provider names the pool", providerSel: map[string]string{"name": "linux-large"}, wantAgrees: true},
		{name: "the pool asks for a machine label the provider has", poolSel: map[string]string{"site": "garage"}, wantAgrees: true},
		{name: "the pool asks for the provider's kind", poolSel: map[string]string{"kind": "proxmox"}, wantAgrees: true},
		{
			name: "the provider wants a label the pool lacks", providerSel: map[string]string{"gpu": ""},
			wantBy: "provider", wantWhyIn: `has no "gpu" label`, wantFixInStr: "pool selector",
		},
		{
			name: "the provider wants another value of a label", providerSel: map[string]string{"tier": "small"},
			wantBy: "provider", wantWhyIn: "tier=small", wantFixInStr: "pool selector",
		},
		{
			name: "the pool wants a machine label the provider lacks", poolSel: map[string]string{"site": "office"},
			wantBy: "pool", wantWhyIn: "site=office", wantFixInStr: "provider selector",
		},
		{
			// Renting is spending money, so one side's yes never outweighs the
			// other's no.
			name: "one side agreeing is not enough", providerSel: map[string]string{"linux": ""}, poolSel: map[string]string{"site": "office"},
			wantBy: "pool", wantWhyIn: "site=office",
		},
		{name: "case is not significant, as it is not for GitHub labels", providerSel: map[string]string{"LINUX": ""}, wantAgrees: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := *prov
			p.PoolSelector = tc.providerSel
			q := *pool
			q.ProviderSelector = tc.poolSel

			got := Pair(&p, &q)
			if got.Agrees != tc.wantAgrees || got.By != tc.wantBy {
				t.Fatalf("Pair = %+v, want agrees=%v by=%q", got, tc.wantAgrees, tc.wantBy)
			}
			if tc.wantAgrees {
				if got.Why != "" || got.Fix != "" {
					t.Errorf("an agreement carries a refusal: %+v", got)
				}
				return
			}
			if !strings.Contains(got.Why, tc.wantWhyIn) {
				t.Errorf("Why %q does not say %q", got.Why, tc.wantWhyIn)
			}
			if !strings.Contains(got.Fix, tc.wantFixInStr) {
				t.Errorf("Fix %q does not name %q", got.Fix, tc.wantFixInStr)
			}
		})
	}
}
