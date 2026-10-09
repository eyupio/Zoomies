package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// PairingView is one provider and one pool, and whether the provider would
// rent a machine for it.
//
// A pool uses a provider only when three things are true, and an operator
// looking at an empty Machines list needs to know which one is not. The two
// selectors are the opt-in each side gives (the provider's pool_selector, the
// pool's provider_selector); the third is that the machine the provider builds
// is one the pool's runners could be placed on at all. They are reported apart
// because the first two are choices somebody made and the third is a mismatch
// of shape.
type PairingView struct {
	ProviderID   string `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	PoolID       string `json:"pool_id"`
	PoolName     string `json:"pool_name"`
	// Serves is the answer: this provider would rent a machine for this pool.
	Serves bool `json:"serves"`
	// Agrees is whether the two selectors allow it. By says which side refused,
	// "provider" or "pool", and Why and Fix say what did not match and what to
	// change.
	Agrees bool   `json:"agrees"`
	By     string `json:"by,omitempty"`
	// Fits is whether the machine the provider would build suits the pool.
	// FitWhy is the first thing that does not, in the setting's own terms.
	Fits   bool   `json:"fits"`
	FitWhy string `json:"fit_why,omitempty"`
	Why    string `json:"why,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// ProviderPairings answers, for every provider and pool, whether the provider
// would rent for the pool and, if not, whose setting says so. It asks the same
// rules the machine decision does (scheduler.Pair and scheduler.HostCouldRun),
// so the page that explains a pairing and the pass that acts on it cannot
// disagree.
func (c *Controller) ProviderPairings(ctx context.Context) ([]PairingView, error) {
	providers, err := c.st.ListProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing providers: %w", err)
	}
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing pools: %w", err)
	}
	now := c.Now()
	out := make([]PairingView, 0, len(providers)*len(pools))
	for _, p := range providers {
		synth := providerSynthHost(p, now)
		for _, pool := range pools {
			pair := scheduler.Pair(p, pool)
			fitWhy := machineFit(synth, pool)
			v := PairingView{
				ProviderID: p.ID, ProviderName: p.Name, PoolID: pool.ID, PoolName: pool.Name,
				Agrees: pair.Agrees, By: pair.By, Fits: fitWhy == "", FitWhy: fitWhy,
			}
			v.Serves = v.Agrees && v.Fits
			switch {
			case !pair.Agrees:
				v.Why, v.Fix = sentence(pair.Why), sentence(pair.Fix)
			case fitWhy != "":
				v.Why = fitWhy
			}
			out = append(out, v)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].PoolName != out[j].PoolName {
			return out[i].PoolName < out[j].PoolName
		}
		return out[i].ProviderName < out[j].ProviderName
	})
	return out, nil
}

// machineFit says why the machine a provider would build could not run a
// pool's runners, or returns "" when it could. It reads the same four tests as
// scheduler.HostCouldRun, in its order, and names the first that fails.
func machineFit(machine *store.Host, pool *store.Pool) string {
	switch {
	case !scheduler.HostOffers(machine, pool):
		return fmt.Sprintf("The machines it builds run %s, and this pool needs %s.",
			strings.Join(machine.Backends, ", "), pool.Backend)
	case !scheduler.HostIsPlatform(machine, pool):
		return "The machines it builds are not the operating system or architecture this pool asks for."
	case !scheduler.HostSelects(machine, pool):
		return "The machine labels it gives its machines do not satisfy this pool's host selector."
	case !scheduler.HostFits(machine, pool):
		return "The machines it builds are too small for one of this pool's runners."
	}
	return ""
}

// sentence makes a finding's clause into one an operator reads on a page: the
// scheduler writes them to continue a log line, which starts lower-case and
// ends without a stop.
func sentence(s string) string {
	if s == "" {
		return s
	}
	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}
