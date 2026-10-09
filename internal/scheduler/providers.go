package scheduler

import (
	"fmt"
	"sort"
	"strings"

	"github.com/eyupio/zoomies/internal/store"
)

// The two halves of a pool's agreement with a provider.
//
// A pool already says which hosts it may land on (host_selector, matched against
// a host's labels), and a provider already says what its machines are (its
// machine labels). Renting is spending money, so both sides get a say in the
// same way and a machine is bought only where both agree:
//
//   - the provider's pool_selector says which pools it will buy for, matched
//     against the pool's labels and name;
//   - the pool's provider_selector says which providers may buy for it, matched
//     against the provider's name, kind and machine labels.
//
// An empty selector on either side constrains nothing, which is what every
// provider and pool meant before either could say otherwise. These functions are
// pure, as the rest of this package is, because the machine decision, the pool
// editor's "providers that would rent for this" and the explanation an operator
// reads must all be the same answer.

// Pairing is one provider and one pool, judged on the two selectors.
type Pairing struct {
	// Agrees is true when neither side's selector rules the other out.
	Agrees bool `json:"agrees"`
	// By names the side that refused: "provider" or "pool". Empty when Agrees.
	By string `json:"by,omitempty"`
	// Why says what did not match, in the voice of the setting an operator
	// would edit, and Fix what to change. Both empty when Agrees.
	Why string `json:"why,omitempty"`
	Fix string `json:"fix,omitempty"`
}

// Pair judges a provider and a pool on their two selectors. It does not ask
// whether the machine the provider makes would suit the pool (backend,
// platform, size): that is HostCouldRun's question, asked of the machine the
// provider would build.
func Pair(p *store.Provider, pool *store.Pool) Pairing {
	if key, want, ok := firstMiss(p.PoolSelector, func(k string) (string, bool) { return poolSelectorValue(pool, k) }); !ok {
		return Pairing{
			By:  "provider",
			Why: fmt.Sprintf("provider %s only rents for pools matching %s, and %s", p.Name, formatSelector(p.PoolSelector), missWhy("pool "+pool.Name, key, want)),
			Fix: fmt.Sprintf("edit the provider's pool selector, or give the pool %s", wantLabel(key, want)),
		}
	}
	if key, want, ok := firstMiss(pool.ProviderSelector, func(k string) (string, bool) { return providerSelectorValue(p, k) }); !ok {
		return Pairing{
			By:  "pool",
			Why: fmt.Sprintf("pool %s only lets providers matching %s rent for it, and %s", pool.Name, formatSelector(pool.ProviderSelector), missWhy("provider "+p.Name, key, want)),
			Fix: fmt.Sprintf("edit the pool's provider selector, or give the provider's machines the label %s=%s", key, want),
		}
	}
	return Pairing{Agrees: true}
}

// firstMiss returns the first selector entry (in key order, so the sentence is
// stable) that the subject does not satisfy.
func firstMiss(sel map[string]string, value func(key string) (string, bool)) (key, want string, ok bool) {
	keys := make([]string, 0, len(sel))
	for k := range sel {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		got, has := value(k)
		// An entry with no value asks only that the subject carries the key.
		if sel[k] == "" {
			if !has {
				return k, "", false
			}
			continue
		}
		if !has || !strings.EqualFold(got, sel[k]) {
			return k, sel[k], false
		}
	}
	return "", "", true
}

// poolSelectorValue is what a pool answers for a key in a provider's
// pool_selector: a built-in (name, backend), or the value of a `key=value`
// label it carries. A bare label answers for itself, with an empty value, so
// `large` as a selector key asks for a pool labelled "large".
func poolSelectorValue(pool *store.Pool, key string) (string, bool) {
	switch strings.ToLower(key) {
	case "name":
		return pool.Name, true
	case "backend":
		return string(pool.Backend), pool.Backend != ""
	}
	for _, l := range pool.Labels {
		if strings.EqualFold(l, key) {
			return "", true
		}
		if k, v, found := strings.Cut(l, "="); found && strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

// providerSelectorValue is what a provider answers for a key in a pool's
// provider_selector: a built-in (name, kind), or one of its machine labels.
func providerSelectorValue(p *store.Provider, key string) (string, bool) {
	switch strings.ToLower(key) {
	case "name":
		return p.Name, true
	case "kind":
		return string(p.Kind), true
	}
	v, ok := p.MachineLabels[key]
	return v, ok
}

func formatSelector(sel map[string]string) string {
	keys := make([]string, 0, len(sel))
	for k := range sel {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if sel[k] == "" {
			parts = append(parts, k)
		} else {
			parts = append(parts, k+"="+sel[k])
		}
	}
	return strings.Join(parts, ", ")
}

func missWhy(subject, key, want string) string {
	if want == "" {
		return fmt.Sprintf("%s has no %q label", subject, key)
	}
	return fmt.Sprintf("%s does not have %s=%s", subject, key, want)
}

func wantLabel(key, want string) string {
	if want == "" {
		return fmt.Sprintf("the label %q", key)
	}
	return fmt.Sprintf("the label %s=%s", key, want)
}
