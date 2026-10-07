package controller

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/eyupio/zoomies/internal/naming"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// A host's settings and a pool's requirements are two halves of one sentence,
// and they are edited on different pages by people who cannot see the other
// half. Raising a host's memory reserve is arithmetic on the Hosts page and a
// pool with nowhere to run on the Pools page; asking a pool for two more cores
// is one number on a form and a fleet that has none. Either way the fleet
// stays green, the scheduler keeps deciding correctly, and the only symptom is
// a job that queues for an hour.
//
// So both edits are checked against the other half before they are saved, and
// an edit that takes away the last host a pool could ever run on is refused
// until the operator says they meant it. The check is deliberately narrow: it
// asks only whether a pool that has somewhere to run would stop having one.
// A pool that is already stranded is not made worse by the next edit, and a
// pool that keeps one home is not the operator's problem today.

// Stranding is one pool an edit would leave with no host in the fleet that
// could ever run it, and the refusal that is the nearest miss -- a host that
// could run it before the edit and could not after, with that host's own
// account of why. The name is the operator's way in: a code is something to
// group by, and the machine that stopped fitting is the one to go and look at.
type Stranding struct {
	PoolID string `json:"pool_id"`
	Pool   string `json:"pool"`
	// Host is the machine whose refusal Reason quotes: the host being edited,
	// or, for a pool edit, one that used to have room for it.
	Host string `json:"host"`
	// Code is which of the placement rules it now fails, in the same
	// vocabulary HostFit's exclusions use: selector, backend, platform, size,
	// profile.
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// HostStrandings reports the pools that could run somewhere in this fleet
// today and would have nowhere to run if this host were saved as proposed.
//
// Proposed is the host as it would be after the edit, id and all; every other
// host is read as it stands. Health is not consulted on either side: a host
// waiting for its agent to check in is still where a pool lives, and refusing
// an edit because some other machine happened to be rebooting would be a rule
// nobody could predict.
func (c *Controller) HostStrandings(ctx context.Context, proposed *store.Host) ([]Stranding, error) {
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return nil, err
	}
	pools = c.sizingPools(pools)
	after := make([]*store.Host, 0, len(hosts))
	for _, h := range hosts {
		if h.ID == proposed.ID {
			after = append(after, proposed)
			continue
		}
		after = append(after, h)
	}
	var out []Stranding
	for _, p := range pools {
		// A disabled pool creates no runners, so nothing is waiting on it and
		// there is nothing to refuse an edit over. It is checked again on its
		// own account when it is enabled.
		//
		// A pool the controller keeps has the hosts of its class and no others,
		// so moving a host out of the class is how the pool is meant to lose it:
		// the controller puts the pool out of use and keeps it. Refusing the
		// edit would make the one way to change a host's class a conflict.
		if !p.Enabled || p.FromHosts() {
			continue
		}
		if !anyHostCouldRun(hosts, p) || anyHostCouldRun(after, p) {
			continue
		}
		code, reason := HostRefusal(proposed, p)
		out = append(out, Stranding{PoolID: p.ID, Pool: p.Name, Host: hostLabel(proposed), Code: code, Reason: reason})
	}
	return out, nil
}

// PoolStranding is the same question asked from the other side: whether this
// pool, saved as proposed, would still have a host it could run on.
//
// It returns at most one entry, because there is only one pool in question.
// The host it names is one that has room for the pool as it stands, which is
// the machine the new figures should be read against -- an operator who has
// just typed 16 GB needs to see the 12 GB host it no longer fits, not a count.
func (c *Controller) PoolStranding(ctx context.Context, current, proposed *store.Pool) ([]Stranding, error) {
	if !proposed.Enabled {
		return nil, nil
	}
	fleet := c.cfg().Runners
	current, proposed = sizingPool(current, fleet), sizingPool(proposed, fleet)
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	if anyHostCouldRun(hosts, proposed) {
		return nil, nil
	}
	// Only an edit that takes the last host away is refused. A pool that had
	// none to begin with is being edited by an operator who has already been
	// shown pool.no_matching_hosts, and stopping them changing anything else
	// about it would be a trap rather than a guard.
	var home *store.Host
	for _, h := range hosts {
		if scheduler.HostCouldRun(h, current) {
			home = h
			break
		}
	}
	if home == nil {
		return nil, nil
	}
	code, reason := HostRefusal(home, proposed)
	return []Stranding{{PoolID: proposed.ID, Pool: proposed.Name, Host: hostLabel(home), Code: code, Reason: reason}}, nil
}

// hostLabel is the host as an operator would name it in a sentence. A host
// that has not yet reported a name is still being talked about, and its id is
// what the Hosts page would show for it.
func hostLabel(h *store.Host) string {
	if h.Name != "" {
		return naming.ForSentence(h.Name)
	}
	return h.ID
}

func anyHostCouldRun(hosts []*store.Host, p *store.Pool) bool {
	for _, h := range hosts {
		if scheduler.HostCouldRun(h, p) {
			return true
		}
	}
	return false
}

// HostRefusal says which of the rules that are configuration -- rather than
// weather -- this host fails for this pool, and why, in the operator's terms.
// It is empty when the host could run the pool.
//
// The order is the scheduler's own, so the reason given is the first rule the
// host actually failed, and the sentences are written about "it" with the
// host's name left out, so a caller can put the name where its layout wants
// it. HostFit renders its exclusions from here, and so does every refusal an
// edit produces: the count before a pool exists and the refusal that stops one
// being stranded are the same fact, and they are said in the same words.
func HostRefusal(h *store.Host, p *store.Pool) (code, reason string) {
	switch {
	case !scheduler.HostSelects(h, p):
		return ExcludedSelector, selectorReason(h, p)
	case !scheduler.HostOffers(h, p):
		return ExcludedBackend, backendReason(h, p)
	case !scheduler.HostIsPlatform(h, p):
		return ExcludedPlatform, platformReason(h, p)
	}
	// The host's own profile is asked before its size: it is two things an
	// operator wrote disagreeing, and the sentence names both, where the
	// shortfall would call the host too small for a pool it is merely not
	// configured for.
	if ex := scheduler.ExcludedBySize(h, p); ex != nil {
		return ExcludedProfile, ex.Reason
	}
	if short := scheduler.HostShortfall(h, p); short != "" {
		return ExcludedSize, short
	}
	return "", ""
}

// selectorReason names the one key that did not answer, and what it answered
// instead. The pair is the whole fix: a host missing the label is labelled, a
// host carrying the wrong value is corrected, and "it does not match the host
// selector" sends an operator to compare two lists by eye.
func selectorReason(h *store.Host, p *store.Pool) string {
	for _, k := range slices.Sorted(maps.Keys(p.HostSelector)) {
		want := p.HostSelector[k]
		got := h.SelectorValue(k)
		if got == want {
			continue
		}
		if got == "" {
			return fmt.Sprintf("it has no %s label, and this pool's host selector asks for %s=%s", k, k, want)
		}
		return fmt.Sprintf("its %s is %s, and this pool's host selector asks for %s", k, got, want)
	}
	return "it does not match this pool's host selector"
}

// backendReason adds the agent's own probe when it has one. The backend's name
// alone sends an operator looking in the wrong place: the usual cause is a
// socket that is not readable or a daemon that is not running, and the probe
// says which.
func backendReason(h *store.Host, p *store.Pool) string {
	reason := "its agent does not offer the " + string(p.Backend) + " backend"
	if info, ok := h.BackendInfo.Find(p.Backend); ok && !info.Available && info.Detail != "" {
		reason += " -- it reports: " + info.Detail
	}
	return reason
}
