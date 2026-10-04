package scheduler

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/naming"
	"github.com/eyupio/zoomies/internal/store"
)

// Why a host is not counted towards an automatic pool. The first four are
// states a healthy fleet passes through -- a machine cordoned for maintenance, a
// network that dropped for an hour -- and are said on the Hosts page and in a
// pool's own cause; the rest are things an operator has to do something about,
// and become findings.
const (
	SkipCordoned     = "cordoned"
	SkipIncompatible = "incompatible"
	SkipSilent       = "silent"
	SkipNoClass      = "unclassified"
	SkipBadSize      = "bad_size_label"
	SkipArch         = "unknown_architecture"
	SkipBackend      = "no_backend"
)

// AutoPoolChangeKind is what the controller has to do to a pool it keeps.
type AutoPoolChangeKind string

const (
	// AutoPoolCreate makes the pool for a class that has its first host.
	AutoPoolCreate AutoPoolChangeKind = "create"
	// AutoPoolResize moves a pool's maximum, or its warm minimum, because the
	// hosts in its class changed.
	AutoPoolResize AutoPoolChangeKind = "resize"
	// AutoPoolEnable puts a pool back in use because a host of its class is back.
	AutoPoolEnable AutoPoolChangeKind = "enable"
	// AutoPoolDisable takes a pool out of use because its last host is gone. The
	// pool and its history stay.
	AutoPoolDisable AutoPoolChangeKind = "disable"
	// AutoPoolReshape brings a pool's fixed fields -- its labels, its selector,
	// its docker mode -- back in line with what a pool of its class is made
	// with, which only differs after a setting changed.
	AutoPoolReshape AutoPoolChangeKind = "reshape"
)

// AutoPoolInput is what PlanAutoPools decides from.
type AutoPoolInput struct {
	Now   time.Time
	Hosts []*store.Host
	// Pools is every pool, the operator's and the controller's, so that a name
	// or a label an operator already uses is never taken over.
	Pools []*store.Pool
	// InstallationID is the installation the pools belong to. Pools the
	// controller keeps for any other are put out of use.
	InstallationID string
	// RunnerGroup is the organisation runner group a pool is made in: the one
	// Zoomies provisions for an organisation installation, and none for a
	// repository's, which has no groups. It is what a pool created by hand on the
	// same installation is given where it names none, and an automatic pool left
	// in GitHub's Default group would be offered to every repository of the
	// organisation while the pools beside it were not.
	RunnerGroup string
	// Since is when the controller began listening, zero where that is not known.
	// A host's silence is counted from the later of its last heartbeat and this: a
	// controller that was down for an hour has heard from no host for an hour,
	// which says nothing about the hosts, and every one would otherwise be
	// dropped from its pool -- and each pool disabled -- on the first pass, until
	// the agents had had time to report again.
	Since time.Time
	// Grace is how long a host may be silent and still count. It is longer than
	// the time after which the fleet gives a silent host's runners up, so that a
	// brief network drop reshuffles nothing.
	Grace time.Duration
	// DockerMode is the docker mode a pool is made with, from the settings.
	DockerMode store.DockerMode
	// Size returns a pool as the scheduler sizes it -- with the figures the
	// fleet's settings give it, which the scheduler cannot read -- or the pool
	// itself. A nil Size is the identity.
	Size func(*store.Pool) *store.Pool
	// Previous is the hosts that counted towards each pool the last time the
	// controller looked, by pool key. It is only there to say what changed in a
	// pool's cause; nothing is decided from it, so a controller that has just
	// started and has none is simply less specific.
	Previous map[string][]string
}

// AutoPoolChange is one thing to do to the pools the controller keeps, with
// the sentence that says why.
type AutoPoolChange struct {
	Kind AutoPoolChangeKind
	// Key is the pool's key: an architecture and a class.
	Key string
	// Before is the pool as it is, nil for a pool being created.
	Before *store.Pool
	// After is the pool as it should be. For a create it is the pool to insert;
	// otherwise it is Before with the changes made.
	After *store.Pool
	// Hosts are the hosts that count towards the pool, and Slots what they add
	// up to before any operator cap.
	Hosts []string
	Slots int
	// Reshaped says the pool's fixed fields differ from what a pool of its class
	// is made with, as well as whatever Kind says changed. They are written with
	// the limits in one statement, so a pool that needed both is not left
	// half-corrected for another pass.
	Reshaped bool
	// Cause is the sentence for the audit log and the Overview feed.
	Cause string
}

// AutoPoolFinding is something the controller could not do, and what to do
// about it.
type AutoPoolFinding struct {
	Code string `json:"code"`
	// Subject is the pool, host or class the finding is about.
	Subject string `json:"subject"`
	Message string `json:"message"`
	Fix     string `json:"fix"`
}

// The findings PlanAutoPools reports.
const (
	FindingNameTaken   = "auto_pool.name_taken"
	FindingLabelTaken  = "auto_pool.label_taken"
	FindingHostSkipped = "auto_pool.host_skipped"
)

// HostSkip is a host that does not count towards any automatic pool, and why.
type HostSkip struct {
	Host   *store.Host
	Reason string
}

// AutoPoolPlan is the outcome of one pass.
type AutoPoolPlan struct {
	Changes  []AutoPoolChange
	Findings []AutoPoolFinding
	// Skipped is every host that counted towards no pool.
	Skipped []HostSkip
	// Members is the hosts counted towards each pool, by pool key, which the
	// controller keeps to say what changed on the next pass.
	Members map[string][]string
	// Slots is the runners the hosts counted towards each pool hold between
	// them, by pool key, before any cap: what the pool's page says its maximum
	// is made of.
	Slots map[string]int
}

// AutoPoolKey is the key of the pool for an architecture and class.
func AutoPoolKey(arch string, class store.SizeClass) string { return store.AutoKeyFor(arch, class) }

// AutoPoolName is the name of the pool for an architecture and class, in the
// grammar every other name follows: the brand, the architecture where it is not
// the usual one, and the class -- zoomies-large, zoomies-arm64-large.
func AutoPoolName(arch string, class store.SizeClass) string {
	return naming.Spec{Arch: arch, Suffix: string(class)}.String()
}

// AutoPoolLabels are the labels the pool for an architecture and class answers
// to: the one every pool carries, the class, and the two kernel and architecture
// labels that stop a job for another machine being claimed by it. The class
// label is the guaranteed path: only a runner that carries it can take a job
// that asks for it.
func AutoPoolLabels(arch string, class store.SizeClass) []string {
	return store.BrandLabels([]string{class.Label(), "linux", naming.ArchLabel(arch)})
}

// NewAutoPool is the pool made for an architecture and class. It leaves its
// runners' size to the host they land on, which is why it is a profile pool, and
// selects the hosts in its class, which is why a host is only ever in one.
//
// It is the pool an operator would get from the API with nothing but these set,
// and a test in the api package holds the two together.
func NewAutoPool(in AutoPoolInput, arch string, class store.SizeClass, backend store.BackendKind) *store.Pool {
	p := &store.Pool{
		Name:            AutoPoolName(arch, class),
		InstallationID:  in.InstallationID,
		RunnerGroup:     in.RunnerGroup,
		Labels:          store.StringSlice(AutoPoolLabels(arch, class)),
		Backend:         backend,
		Platform:        store.Platform{Arch: arch},
		PullPolicy:      store.PullIfNotPresent,
		IdleTimeout:     store.Duration(5 * time.Minute),
		Ephemeral:       true,
		DockerMode:      in.DockerMode,
		Cache:           store.CacheConfig{Scope: store.CacheScopePool},
		SizeFromProfile: true,
		HostSelector:    store.StringMap{store.LabelSize: string(class)},
		AutoKey:         AutoPoolKey(arch, class),
		Enabled:         true,
	}
	if p.DockerMode == "" {
		p.DockerMode = store.DockerNone
	}
	if backend == store.BackendDocker || backend == store.BackendPodman {
		p.CPUBurst = store.CPUBurstPolicy{Mode: store.CPUBurstObserve}
	}
	return p
}

// autoGroup is the hosts that fall in one architecture and class.
type autoGroup struct {
	arch  string
	class store.SizeClass
	hosts []*store.Host
}

// PlanAutoPools works out what the pools the controller keeps should look like
// for the hosts that exist, and what has to change to make them so.
//
// It is the whole of the policy and holds no state. It is run on every host
// event and on a timer, and because every figure is recomputed from the hosts as
// they are, running it twice in a row changes nothing the second time, and
// running it after a missed event repairs what the event would have done. That
// is what lets the controller treat an event as a hint to look sooner rather
// than as the only way anything is noticed.
func PlanAutoPools(in AutoPoolInput) AutoPoolPlan {
	plan := AutoPoolPlan{Members: map[string][]string{}, Slots: map[string]int{}}
	size := in.Size
	if size == nil {
		size = func(p *store.Pool) *store.Pool { return p }
	}

	// Which hosts count, and in which group.
	groups := map[string]*autoGroup{}
	reasons := map[string]string{} // by host ID, for wording what changed
	for _, h := range sortedHosts(in.Hosts) {
		arch, class, reason := autoClassOf(h, in)
		if reason != "" {
			reasons[h.ID] = reason
			plan.Skipped = append(plan.Skipped, HostSkip{Host: h, Reason: reason})
			continue
		}
		key := AutoPoolKey(arch, class)
		g := groups[key]
		if g == nil {
			g = &autoGroup{arch: arch, class: class}
			groups[key] = g
		}
		g.hosts = append(g.hosts, h)
	}

	// The pools the controller already keeps, by key, in this installation.
	ours := map[string]*store.Pool{}
	var elsewhere []*store.Pool
	for _, p := range sortedPools(in.Pools) {
		switch {
		case !p.FromHosts():
		case p.InstallationID == in.InstallationID:
			ours[p.AutoKey] = p
		default:
			elsewhere = append(elsewhere, p)
		}
	}

	keys := map[string]bool{}
	for k := range groups {
		keys[k] = true
	}
	for k := range ours {
		keys[k] = true
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Slice(ordered, func(i, j int) bool {
		ai, ci, _ := store.ParseAutoKey(ordered[i])
		aj, cj, _ := store.ParseAutoKey(ordered[j])
		if ci.Rank() != cj.Rank() {
			return ci.Rank() < cj.Rank()
		}
		return ai < aj
	})

	for _, key := range ordered {
		arch, class, ok := store.ParseAutoKey(key)
		if !ok {
			continue
		}
		existing := ours[key]
		g := groups[key]

		var members []*store.Host
		backend := store.BackendKind("")
		if existing != nil {
			backend = existing.Backend
		}
		if g != nil {
			if backend == "" {
				backend = autoBackend(g.hosts)
			}
			for _, h := range g.hosts {
				if backend != "" && slices.Contains(h.Backends, string(backend)) {
					members = append(members, h)
				} else {
					reasons[h.ID] = SkipBackend
					plan.Skipped = append(plan.Skipped, HostSkip{Host: h, Reason: SkipBackend})
				}
			}
		}

		if existing == nil {
			if len(members) == 0 {
				continue
			}
			if clash := autoCollision(in, arch, class); clash != nil {
				plan.Findings = append(plan.Findings, *clash)
				continue
			}
			desired := NewAutoPool(in, arch, class, backend)
			slots := autoSlots(desired, members, size)
			plan.Slots[key] = slots
			desired.MaxRunners = capped(slots, desired.AutoCap)
			desired.MinRunners = min(desired.AutoMin, desired.MaxRunners)
			plan.Members[key] = hostNames(members)
			plan.Changes = append(plan.Changes, AutoPoolChange{
				Kind: AutoPoolCreate, Key: key, After: desired, Hosts: hostNames(members), Slots: slots,
				Cause: fmt.Sprintf("no pool existed for %s %s hosts, and %s has joined: %s",
					class, archPhrase(arch), hostPhrase(members), joinedWithHosts(members, slots)),
			})
			continue
		}

		desired := *existing
		reshaped := reshape(in, &desired, arch, class)
		slots := autoSlots(&desired, members, size)
		plan.Slots[key] = slots
		desired.MaxRunners = capped(slots, desired.AutoCap)
		desired.MinRunners = min(desired.AutoMin, desired.MaxRunners)
		desired.Enabled = len(members) > 0 && !desired.AutoPaused
		if len(members) == 0 {
			desired.MinRunners, desired.MaxRunners = 0, 0
		}
		plan.Members[key] = hostNames(members)

		change := AutoPoolChange{Key: key, Before: existing, After: &desired, Hosts: hostNames(members), Slots: slots, Reshaped: reshaped}
		derived := existing.MinRunners != desired.MinRunners || existing.MaxRunners != desired.MaxRunners ||
			existing.Enabled != desired.Enabled
		switch {
		case !derived && !reshaped:
			if clash := labelClash(in, existing, class); clash != nil {
				plan.Findings = append(plan.Findings, *clash)
			}
			continue
		case desired.AutoPaused && existing.Enabled:
			change.Kind = AutoPoolDisable
			change.Cause = "an operator paused the pool, so it takes no new work whatever its hosts do"
		case len(members) == 0 && existing.Enabled:
			change.Kind = AutoPoolDisable
			change.Cause = fmt.Sprintf("no %s %s host is left to take work", class, archPhrase(arch))
			if left := leftParts(in.Previous[key], nil, in.Hosts, reasons); len(left) > 0 {
				change.Cause += ": " + strings.Join(left, "; ")
			}
		case len(members) > 0 && !existing.Enabled && !existing.AutoPaused:
			change.Kind = AutoPoolEnable
			change.Cause = fmt.Sprintf("%s is back, so the pool is in use again with room for %s",
				hostPhrase(members), plural(desired.MaxRunners, "runner"))
			// A pool whose hosts were all there the last time we looked was not
			// out of use for want of one: someone paused it, and has stopped.
			if len(in.Previous[key]) > 0 {
				change.Cause = fmt.Sprintf("an operator resumed the pool, so it is in use again with room for %s", plural(desired.MaxRunners, "runner"))
			}
		case derived:
			change.Kind = AutoPoolResize
			change.Cause = resizeCause(existing, &desired, in.Previous[key], members, in.Hosts, reasons, slots)
		default:
			change.Kind = AutoPoolReshape
			change.Cause = "its fixed settings were brought back in line with the automatic pool settings"
		}
		if reshaped && change.Kind != AutoPoolReshape {
			change.Cause += "; its fixed settings were also brought back in line with the automatic pool settings"
		}
		plan.Changes = append(plan.Changes, change)
		if clash := labelClash(in, existing, class); clash != nil {
			plan.Findings = append(plan.Findings, *clash)
		}
	}

	// A pool the controller keeps for an installation that is no longer the one
	// in use is not ours to leave scaling. It is put out of use, as a pool with
	// no hosts is, and kept.
	for _, p := range elsewhere {
		if !p.Enabled && p.MaxRunners == 0 {
			continue
		}
		after := *p
		after.Enabled, after.MinRunners, after.MaxRunners = false, 0, 0
		plan.Changes = append(plan.Changes, AutoPoolChange{
			Kind: AutoPoolDisable, Key: p.AutoKey, Before: p, After: &after,
			Cause: "automatic pools now use another GitHub App installation, so this one is put out of use",
		})
	}

	plan.Findings = append(plan.Findings, skippedFindings(plan.Skipped)...)
	return plan
}

// autoClassOf is the architecture and class a host counts under, or the reason
// it counts under none.
func autoClassOf(h *store.Host, in AutoPoolInput) (arch string, class store.SizeClass, reason string) {
	switch {
	case h.Cordoned:
		return "", "", SkipCordoned
	case h.Incompatible:
		return "", "", SkipIncompatible
	case in.Now.Sub(laterOf(h.LastHeartbeat, in.Since)) > in.Grace:
		return "", "", SkipSilent
	}
	arch = naming.NormalizeArch(h.Arch)
	if arch == "" {
		return "", "", SkipArch
	}
	class, operator := h.EffectiveSizeClass()
	switch {
	case class.Valid():
		return arch, class, ""
	case operator:
		return "", "", SkipBadSize
	}
	return "", "", SkipNoClass
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// autoBackend is the backend a new pool is made for: docker where any of the
// hosts offers it, and otherwise podman. A host that offers neither is not a
// host the runner image can run on.
func autoBackend(hosts []*store.Host) store.BackendKind {
	for _, b := range []store.BackendKind{store.BackendDocker, store.BackendPodman} {
		if slices.ContainsFunc(hosts, func(h *store.Host) bool { return slices.Contains(h.Backends, string(b)) }) {
			return b
		}
	}
	return ""
}

// autoCollision reports an operator's pool that already uses the name or the
// class label a new pool would be made with. It is never taken over, and the
// pool is not made: two pools answering to the same class label would split the
// jobs that ask for it between a pool the operator tunes and one they did not
// ask for.
func autoCollision(in AutoPoolInput, arch string, class store.SizeClass) *AutoPoolFinding {
	name := AutoPoolName(arch, class)
	for _, p := range sortedPools(in.Pools) {
		// A pool kept for another installation, which stays where it is when the
		// installation the pools belong to is changed, still holds the name: pool
		// names are unique across the whole instance, so the pool for this one
		// cannot be made while it does. Said, because the alternative is a pool
		// that is never made and a finding-free fleet that wonders why.
		if p.FromHosts() && p.InstallationID != in.InstallationID && store.NormalizeLabel(p.Name) == store.NormalizeLabel(name) {
			return &AutoPoolFinding{Code: FindingNameTaken, Subject: name,
				Message: fmt.Sprintf("the pool for %s %s hosts would be called %s, which is the name of the pool the controller kept for another GitHub App installation before automatic pools were moved to this one", class, archPhrase(arch), name),
				Fix:     fmt.Sprintf("delete pool %s, which is out of use and holds the name, and the automatic pool for this installation will be made on the next pass.", p.Name)}
		}
		if p.FromHosts() {
			continue
		}
		if store.NormalizeLabel(p.Name) == store.NormalizeLabel(name) {
			return &AutoPoolFinding{Code: FindingNameTaken, Subject: name,
				Message: fmt.Sprintf("the pool for %s %s hosts would be called %s, which is the name of a pool you made", class, archPhrase(arch), name),
				Fix:     fmt.Sprintf("rename pool %s, or move its hosts into the automatic pool by removing its host selector and letting this one take them.", p.Name)}
		}
		if p.InstallationID == in.InstallationID && slices.Contains(store.NormalizeLabels(p.Labels), class.Label()) {
			return &AutoPoolFinding{Code: FindingLabelTaken, Subject: name,
				Message: fmt.Sprintf("pool %s already answers to %s, the label the pool for %s hosts would use, so jobs that ask for it would be split between the two", p.Name, class.Label(), class),
				Fix:     fmt.Sprintf("take %s off pool %s, or rename it, and the automatic pool will be made on the next pass.", class.Label(), p.Name)}
		}
	}
	return nil
}

// labelClash reports an operator's pool that answers to the class label of a
// pool the controller already keeps, which is how it happens when the operator's
// pool came second: autoCollision stops a new automatic pool being made beside
// one of theirs, and nothing stopped them making theirs beside it. It is a
// finding and not a refusal -- the pool is theirs, and so is the label -- but the
// two split the jobs that ask for the class between them, which is the thing the
// controller refuses to cause.
func labelClash(in AutoPoolInput, kept *store.Pool, class store.SizeClass) *AutoPoolFinding {
	for _, p := range sortedPools(in.Pools) {
		if p.FromHosts() || p.InstallationID != kept.InstallationID || !p.Enabled ||
			!slices.Contains(store.NormalizeLabels(p.Labels), class.Label()) {
			continue
		}
		return &AutoPoolFinding{Code: FindingLabelTaken, Subject: kept.Name,
			Message: fmt.Sprintf("pool %s answers to %s as well as the pool %s the controller keeps for %s hosts, so jobs that ask for it are split between the two", p.Name, class.Label(), kept.Name, class),
			Fix:     fmt.Sprintf("take %s off pool %s, or rename it and give it a label of its own.", class.Label(), p.Name)}
	}
	return nil
}

// reshape lays the fixed fields of a pool of this class over p and reports
// whether any of them differed.
func reshape(in AutoPoolInput, p *store.Pool, arch string, class store.SizeClass) bool {
	want := NewAutoPool(in, arch, class, p.Backend)
	changed := false
	set := func(differs bool, apply func()) {
		if differs {
			apply()
			changed = true
		}
	}
	set(!slices.Equal(store.NormalizeLabels(p.Labels), store.NormalizeLabels(want.Labels)), func() { p.Labels = want.Labels })
	set(p.Platform != want.Platform, func() { p.Platform = want.Platform })
	set(!sameSelector(p.HostSelector, want.HostSelector), func() { p.HostSelector = want.HostSelector })
	set(!p.SizeFromProfile, func() { p.SizeFromProfile = true })
	set(p.DockerMode != want.DockerMode, func() { p.DockerMode = want.DockerMode })
	set(!p.Ephemeral, func() { p.Ephemeral = true })
	set(p.Resources != (store.Resources{}), func() { p.Resources = store.Resources{} })
	return changed
}

func sameSelector(a, b store.StringMap) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// autoSlots is how many runners of p the hosts hold between them: each host's
// room for the pool as the scheduler sizes it, which already accounts for the
// host's slot count, any throttle, its profile and whether the pool is kept off
// it by a size.
func autoSlots(p *store.Pool, hosts []*store.Host, size func(*store.Pool) *store.Pool) int {
	sized := size(p)
	total := 0
	for _, h := range hosts {
		total += HostRoomFor(withoutLiveDisk(h), sized).Room
	}
	return total
}

// withoutLiveDisk is a copy of h that has measured no disk. A pool's maximum is
// how many runners the machine can hold, and the free space on it is a reading
// that moves with every image pulled and every cache written: a host that
// crossed its disk reserve and came back would take its slots out of the pool
// and put them back, a line in the audit log each way and a pool disabled in
// between, for what the scheduler already does by itself -- it places nothing on
// a host whose disk is full, whatever the pool's maximum says.
func withoutLiveDisk(h *store.Host) *store.Host {
	c := *h
	c.DiskTotalMB, c.DiskFreeMB = 0, 0
	return &c
}

// capped applies an operator's cap; zero is none.
func capped(slots, cap int) int {
	if cap > 0 {
		return min(slots, cap)
	}
	return slots
}

func hostNames(hosts []*store.Host) []string {
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = h.Name
	}
	return out
}

func archPhrase(arch string) string {
	if arch == naming.ArchAMD64 {
		return "x64"
	}
	return arch
}

func hostPhrase(hosts []*store.Host) string {
	switch len(hosts) {
	case 0:
		return "no host"
	case 1:
		return "host " + hosts[0].Name
	}
	return plural(len(hosts), "host")
}

func joinedWithHosts(hosts []*store.Host, slots int) string {
	return fmt.Sprintf("%s (%s)", strings.Join(hostNames(hosts), ", "), plural(slots, "slot"))
}

// resizeCause says what moved a pool's maximum, from who joined and who left
// since the last look. Without a last look it can only say what the pool is now.
func resizeCause(before, after *store.Pool, previous []string, members, all []*store.Host, reasons map[string]string, slots int) string {
	head := fmt.Sprintf("maximum runners %d → %d", before.MaxRunners, after.MaxRunners)
	if before.MaxRunners == after.MaxRunners {
		head = fmt.Sprintf("warm runners %d → %d", before.MinRunners, after.MinRunners)
	}
	if previous == nil {
		return fmt.Sprintf("%s: recomputed from %s, %s", head, hostPhrase(members), plural(slots, "slot"))
	}
	now := hostNames(members)
	var parts []string
	for _, n := range now {
		if !slices.Contains(previous, n) {
			parts = append(parts, "host "+n+" joined")
		}
	}
	parts = append(parts, leftParts(previous, now, all, reasons)...)
	if len(parts) == 0 {
		switch {
		case before.MaxRunners == after.MaxRunners:
			parts = append(parts, fmt.Sprintf("an operator asked for %s kept warm", plural(after.AutoMin, "runner")))
		case after.AutoCap > 0 && slots > after.AutoCap:
			parts = append(parts, fmt.Sprintf("an operator capped the pool at %s", plural(after.AutoCap, "runner")))
		default:
			parts = append(parts, fmt.Sprintf("the slots of %s changed", hostPhrase(members)))
		}
	}
	return head + ": " + strings.Join(parts, "; ")
}

// leftParts names the hosts that counted last time and do not now, and why: a
// host that is cordoned, one that has gone quiet, one that is gone.
func leftParts(previous, now []string, all []*store.Host, reasons map[string]string) []string {
	var parts []string
	byName := map[string]*store.Host{}
	for _, h := range all {
		byName[h.Name] = h
	}
	for _, n := range previous {
		if slices.Contains(now, n) {
			continue
		}
		if h := byName[n]; h != nil {
			parts = append(parts, "host "+n+" "+skipPhrase(reasons[h.ID]))
		} else {
			parts = append(parts, "host "+n+" was removed")
		}
	}
	return parts
}

func skipPhrase(reason string) string {
	switch reason {
	case SkipCordoned:
		return "was cordoned"
	case SkipIncompatible:
		return "runs an agent this controller cannot place work on"
	case SkipSilent:
		return "has not been heard from for too long"
	case SkipBackend:
		return "offers no backend this pool can use"
	case SkipBadSize:
		return "has a size label that is not a class"
	case SkipArch:
		return "reports an architecture no pool is kept for"
	case SkipNoClass:
		return "has no class yet"
	}
	return "no longer counts"
}

// skippedFindings turns the hosts left out for a reason an operator has to act
// on into findings. A cordon or a silence is not one: both are things a fleet
// does, and both end by themselves.
func skippedFindings(skipped []HostSkip) []AutoPoolFinding {
	var out []AutoPoolFinding
	for _, s := range skipped {
		var fix string
		switch s.Reason {
		case SkipBadSize:
			fix = fmt.Sprintf("set the size tag on host %s to small, medium or large exactly as written here, or remove it to let the controller work it out.", s.Host.Name)
		case SkipArch:
			fix = fmt.Sprintf("host %s reports %q; automatic pools are kept for amd64 and arm64 hosts only.", s.Host.Name, s.Host.Arch)
		case SkipBackend:
			fix = fmt.Sprintf("host %s offers no backend the pool for its class uses; enable Docker or Podman on it.", s.Host.Name)
		default:
			continue
		}
		out = append(out, AutoPoolFinding{Code: FindingHostSkipped, Subject: s.Host.Name,
			Message: fmt.Sprintf("host %s is in no automatic pool: it %s", s.Host.Name, skipPhrase(s.Reason)), Fix: fix})
	}
	return out
}
