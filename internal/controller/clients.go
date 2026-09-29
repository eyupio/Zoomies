package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

// ManagedRunnerGroupName is the organisation runner group Zoomies provisions
// and uses for pools that do not explicitly choose another one.
const ManagedRunnerGroupName = "zoomies"

// probeInterval is how often every installation's credentials are re-checked.
// It is minutes rather than seconds because the failure it catches -- a
// revoked App, a rotated key, a permission removed by an org admin -- happens
// on human timescales, and each probe costs three API calls.
const probeInterval = 5 * time.Minute

// clientCache holds one github.Client per installation.
//
// The cache key includes the installation's updated_at, so editing an
// installation (a new private key, a different target) invalidates its client
// without anyone having to remember to do it.
type clientCache struct {
	c *Controller

	mu      sync.Mutex
	entries map[string]*clientEntry
}

type clientEntry struct {
	client    github.Client
	updatedAt time.Time
	// groups maps runner group names to their GitHub policy for this
	// installation. It is filled lazily because the lookup costs an API call.
	groups map[string]github.RunnerGroup
}

func newClientCache(c *Controller) *clientCache {
	return &clientCache{c: c, entries: map[string]*clientEntry{}}
}

// ClientFor returns a GitHub client for one installation, identified by its
// Zoomies installation ID ("ins_...").
func (c *Controller) ClientFor(ctx context.Context, installationID string) (github.Client, error) {
	inst, err := c.st.GetInstallation(ctx, installationID)
	if err != nil {
		return nil, err
	}
	return c.clients.get(ctx, inst)
}

// ClientForRepo returns the client for whichever installation covers a
// repository, preferring a repo-scoped installation over the owning org.
func (c *Controller) ClientForRepo(ctx context.Context, repoFullName string) (github.Client, error) {
	inst, err := c.st.FindInstallationByTarget(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	return c.clients.get(ctx, inst)
}

// get returns a cached client, building one if the installation is new to the
// cache or has been edited since it was cached.
func (cc *clientCache) get(ctx context.Context, inst *store.Installation) (github.Client, error) {
	if inst == nil {
		return nil, errors.New("controller: no installation given")
	}
	// The demo fixtures have no GitHub App behind them, so every real client
	// path would fail on the placeholder key they carry. The check lives here
	// rather than at one call site because it was at one call site: `ClientFor`
	// had it and `ProbeInstallation` did not, so pressing Verify on the demo
	// installation answered "the stored private key is not a PEM-encoded RSA
	// key" -- on the one page a new operator is most likely to press it.
	if IsDemoID(inst.ID) {
		return newDemoClient(inst), nil
	}
	cc.mu.Lock()
	if e, ok := cc.entries[inst.ID]; ok && e.updatedAt.Equal(inst.UpdatedAt) {
		cc.mu.Unlock()
		return e.client, nil
	}
	cc.mu.Unlock()

	pem, err := cc.c.key.Open(inst.PrivateKeyEnc)
	if err != nil {
		// A key mismatch is a real operational situation -- an operator
		// restored a database without its encryption key -- so the message has
		// to say that, and say which installation cannot be used.
		return nil, fmt.Errorf("installation %s (%s): the stored GitHub App private key cannot be decrypted: %w; "+
			"either restore the encryption key this data was written with, or upload the App's .pem again on the Installations page",
			inst.ID, inst.Target, err)
	}
	if len(pem) == 0 {
		return nil, fmt.Errorf("installation %s (%s) has no GitHub App private key stored; upload the App's .pem file on the Installations page",
			inst.ID, inst.Target)
	}

	client, err := cc.c.factory.For(ctx, inst, pem)
	if err != nil {
		return nil, err
	}

	cc.mu.Lock()
	cc.entries[inst.ID] = &clientEntry{client: client, updatedAt: inst.UpdatedAt}
	cc.mu.Unlock()
	return client, nil
}

// forget drops an installation's cached client, used when it is deleted.
func (cc *clientCache) forget(id string) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	delete(cc.entries, id)
}

// Forget drops the cached client for an installation. The API calls it after
// deleting or re-keying one, so the next call rebuilds from the stored row.
func (c *Controller) Forget(installationID string) {
	c.clients.forget(installationID)
	// Its rate-limit hold goes with it. The hold is read only when the
	// installation comes round on a sweep, so a stale entry changes nothing --
	// but an installation that is gone should leave nothing behind, and one
	// re-added under the same identifier would inherit a stand-down it never
	// earned.
	c.githubMu.Lock()
	delete(c.githubPaused, installationID)
	c.githubMu.Unlock()
}

// runnerGroupID resolves a runner group name to the ID the JIT config API
// wants. An empty or unknown name falls back to 0, which the github package
// turns into the Default group every target has.
//
// The second return value is why the fallback happened, empty when it did not.
// Falling back is not a detail: Default is usually wider than a named group,
// so a pool that asked to be fenced into one and quietly landed in Default may
// run jobs it was not meant for. Conversely, a group that blocks public
// repositories makes an online runner ineligible for their jobs. The caller
// turns both conditions into standing warnings.
func (cc *clientCache) runnerGroupID(ctx context.Context, inst *store.Installation, client github.Client, name string) (int64, string, bool) {
	name = strings.TrimSpace(name)
	wantsDefault := name == "" || strings.EqualFold(name, "default")
	lookup := strings.ToLower(name)
	if wantsDefault {
		lookup = "default"
	}

	if inst.TargetType == store.TargetRepo {
		if wantsDefault {
			return 0, "", false
		}
		cc.c.log.Warn("a repository target has no runner groups; using the default group",
			"installation", inst.ID, "target", inst.Target, "group", name)
		return 0, "runner groups belong to an organisation, and " + inst.Target + " is a repository", false
	}

	cc.mu.Lock()
	e := cc.entries[inst.ID]
	if e != nil && e.groups != nil {
		if group, ok := e.groups[lookup]; ok {
			cc.mu.Unlock()
			return group.ID, "", group.PublicRepositoryAccessKnown && !group.AllowsPublicRepositories
		}
	}
	cc.mu.Unlock()

	// A name that resolved to nothing is deliberately not cached, so that the
	// next create asks again. It costs one call per runner on a pool that is
	// misconfigured, and it is what lets the warning clear by itself the
	// moment an operator creates the group GitHub was missing.
	groups, err := client.ListRunnerGroups(ctx)
	if errors.Is(err, github.ErrRateLimited) {
		cc.c.holdRateLimited(inst.ID, err, cc.c.Now(), "resolving runner groups")
	}
	if err != nil {
		// A pool naming a group Zoomies cannot list still deserves a runner;
		// GitHub will place it in Default and the operator sees the warning.
		cc.c.log.Warn("could not list runner groups; falling back to the default group",
			"installation", inst.ID, "group", name, "error", err)
		if wantsDefault {
			return 0, "", false
		}
		return 0, "GitHub would not say which runner groups exist on " + inst.Target, false
	}

	var found github.RunnerGroup
	m := make(map[string]github.RunnerGroup, len(groups))
	for _, g := range groups {
		key := strings.ToLower(g.Name)
		m[key] = g
		if key == lookup || (wantsDefault && g.Default) {
			found = g
		}
	}
	cc.mu.Lock()
	if e := cc.entries[inst.ID]; e != nil {
		e.groups = m
	}
	cc.mu.Unlock()

	if found.ID == 0 {
		if wantsDefault {
			return 0, "", false
		}
		cc.c.log.Warn("the pool names a runner group that does not exist on the target; using the default group",
			"installation", inst.ID, "target", inst.Target, "group", name)
		return 0, "the group " + name + " does not exist on " + inst.Target, false
	}
	return found.ID, "", found.PublicRepositoryAccessKnown && !found.AllowsPublicRepositories
}

// probeLoop re-checks every installation's credentials on an interval, so a
// revoked App or a rotated key shows up on the Installations page instead of
// as runners that quietly stop being created.
func (c *Controller) probeLoop(ctx context.Context) {
	// Probe once shortly after startup rather than immediately: it costs API
	// calls, and an operator restarting the controller does not need them in
	// the first second.
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			c.probeInstallations(ctx)
			timer.Reset(probeInterval)
		}
	}
}

// probeInstallations checks every installation and records the result.
func (c *Controller) probeInstallations(ctx context.Context) {
	insts, err := c.st.ListInstallations(ctx)
	if err != nil {
		c.log.Error("could not list installations to check their credentials", "error", err)
		return
	}
	for _, inst := range insts {
		if ctx.Err() != nil {
			return
		}
		if IsDemoID(inst.ID) {
			continue
		}
		// Every other background path stands down from an installation GitHub
		// has rate-limited; the probe spends the same quota (runners, groups,
		// repositories), so it waits too.
		if c.githubHeld(inst.ID, c.Now()) {
			c.log.Debug("not checking installation credentials while it is rate limited",
				"installation", inst.ID, "target", inst.Target)
			continue
		}
		if _, err := c.ProbeInstallation(ctx, inst.ID); err != nil {
			c.log.Warn("installation credentials are not usable",
				"installation", inst.ID, "target", inst.Target, "error", err)
		}
	}
}

// refreshRunnerGroupProblems makes group eligibility visible even when a
// controller upgrade finds an already-full pool and therefore has no reason
// to mint another runner. Without this probe-only path, the warning would not
// appear until the next scale-up -- exactly when an ineligible group can leave
// every existing runner idle forever.
func (c *Controller) refreshRunnerGroupProblems(ctx context.Context, inst *store.Installation, client github.Client) {
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		c.log.Warn("could not inspect pool runner groups", "installation", inst.ID, "error", err)
		return
	}
	for _, pool := range pools {
		if pool.InstallationID != inst.ID {
			continue
		}
		_, unresolved, publicBlocked := c.clients.runnerGroupID(ctx, inst, client, pool.RunnerGroup)
		c.noteRunnerGroup(pool, pool.RunnerGroup, unresolved, publicBlocked)
	}
}

// ensureManagedRunnerGroup creates the organisation-wide group Zoomies owns,
// or reuses a compatible one left by an earlier setup attempt. It deliberately
// does not rewrite a same-named group with a different policy: that group may
// belong to an administrator, and widening it to public repositories is a
// security decision setup must never make silently.
func (c *Controller) ensureManagedRunnerGroup(ctx context.Context, inst *store.Installation, client github.Client) (*github.RunnerGroup, error) {
	if inst.TargetType != store.TargetOrg || IsDemoID(inst.ID) {
		return nil, nil
	}
	groups, err := client.ListRunnerGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("list runner groups before provisioning %s: %w", ManagedRunnerGroupName, err)
	}
	for i := range groups {
		g := &groups[i]
		if !strings.EqualFold(g.Name, ManagedRunnerGroupName) {
			continue
		}
		if g.Visibility != "all" || !g.PublicRepositoryAccessKnown || !g.AllowsPublicRepositories || g.RestrictedToWorkflows {
			return nil, fmt.Errorf("runner group %s already exists but is not the organisation-wide group Zoomies needs; configure it with visibility all, Allow public repositories enabled, and no workflow restriction, or rename it so Zoomies can create its own", g.Name)
		}
		return g, nil
	}

	g, err := client.CreateRunnerGroup(ctx, github.RunnerGroupCreate{
		Name: ManagedRunnerGroupName, Visibility: "all", AllowsPublicRepositories: true,
	})
	if err != nil {
		return nil, fmt.Errorf("provision runner group %s: %w", ManagedRunnerGroupName, err)
	}
	// ListRunnerGroups is cached for JIT creation. Drop only that snapshot so
	// the new group is resolvable immediately without rebuilding credentials.
	c.clients.mu.Lock()
	if e := c.clients.entries[inst.ID]; e != nil {
		e.groups = nil
	}
	c.clients.mu.Unlock()
	c.log.Info("created the managed GitHub runner group", "installation", inst.ID,
		"target", inst.Target, "group", g.Name)
	return g, nil
}

// adoptManagedRunnerGroup moves only pools that still use GitHub's implicit
// Default group. Explicit group choices are administrator policy and stay
// untouched. Existing non-busy runners were registered in Default, so recycle
// them; the scheduler replaces them in the managed group on its next pass.
func (c *Controller) adoptManagedRunnerGroup(ctx context.Context, inst *store.Installation, group *github.RunnerGroup) error {
	if group == nil {
		return nil
	}
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return err
	}
	changed := false
	for _, pool := range pools {
		if pool.InstallationID != inst.ID || strings.TrimSpace(pool.RunnerGroup) != "" {
			continue
		}
		runners, err := c.st.ListRunnersForPool(ctx, pool.ID)
		if err != nil {
			return err
		}
		pool.RunnerGroup = group.Name
		if err := c.st.UpdatePool(ctx, pool); err != nil {
			return err
		}
		c.PublishPool(ctx, events.KindPoolUpdated, pool)
		for _, runner := range runners {
			if runner.State != store.RunnerIdle && runner.State != store.RunnerRegistering {
				continue
			}
			if err := c.drainRunnerID(ctx, runner.ID,
				"pool moved from GitHub Default to the managed "+group.Name+" runner group", pool); err != nil {
				c.log.Warn("could not recycle a runner after assigning its pool to the managed group",
					"pool", pool.Name, "runner", runner.ID, "error", err)
			}
		}
		changed = true
	}
	if changed {
		c.Nudge()
	}
	return nil
}

// ProbeInstallation verifies one installation's credentials end to end and
// records the outcome, publishing it so the UI updates.
//
// It returns the App information on success and the operator-facing error on
// failure; the error is also stored, because "why is nothing scaling?" is
// answered on the Installations page.
func (c *Controller) ProbeInstallation(ctx context.Context, installationID string) (*github.AppInfo, error) {
	inst, err := c.st.GetInstallation(ctx, installationID)
	if err != nil {
		return nil, err
	}

	var info *github.AppInfo
	client, err := c.clients.get(ctx, inst)
	if err == nil {
		info, err = client.Probe(ctx)
		if err == nil {
			var group *github.RunnerGroup
			group, err = c.ensureManagedRunnerGroup(ctx, inst, client)
			if err == nil {
				err = c.adoptManagedRunnerGroup(ctx, inst, group)
			}
			if err == nil {
				c.refreshRunnerGroupProblems(ctx, inst, client)
			}
		}
	}
	c.observeGitHub(inst.ID, err)

	if errors.Is(err, github.ErrRateLimited) {
		// A quota refusal says nothing about the credentials and refills on
		// its own, so it is neither recorded as the installation's health --
		// that raised "not usable, check your private key" -- nor published:
		// the row and the event stream must keep agreeing. The caller still
		// gets the reason, so a manual Verify can say why it has no answer.
		c.holdRateLimited(inst.ID, err, c.Now(), "checking credentials")
		return nil, err
	}

	msg := ""
	if err != nil {
		msg = err.Error()
	} else if info != nil {
		// A working credential with a missing permission is not an error yet,
		// but it will become one the first time a runner is created, so it is
		// recorded now rather than discovered later.
		if missing := info.MissingRequirements(inst.TargetType); len(missing) > 0 {
			msg = "the App is installed but " + strings.Join(missing, "; ")
		}
	}

	if serr := c.st.SetInstallationHealth(ctx, inst.ID, msg); serr != nil {
		c.log.Error("could not record installation health", "installation", inst.ID, "error", serr)
	}
	// The probe is the only place the App's slug is learned for an installation
	// that was added by hand, and every link to the App on GitHub is built from
	// it -- including the settings page where its avatar is uploaded, which is
	// the one setup step a manifest cannot do.
	if info != nil && info.Slug != "" && info.Slug != inst.AppSlug {
		if serr := c.st.SetInstallationAppSlug(ctx, inst.ID, info.Slug); serr != nil {
			c.log.Error("could not record the App's slug", "installation", inst.ID, "error", serr)
		} else {
			inst.AppSlug = info.Slug
		}
	}
	inst.LastError = msg
	now := c.Now()
	inst.LastCheckedAt = &now
	c.publishInstallation(ctx, inst)

	if err != nil {
		return nil, err
	}
	if msg != "" {
		return info, errors.New(msg)
	}
	return info, nil
}

// observeGitHub records one GitHub API outcome for the metrics endpoint.
func (c *Controller) observeGitHub(installationID string, err error) {
	result := "ok"
	switch {
	case err == nil:
	case errors.Is(err, github.ErrRateLimited):
		result = "rate_limited"
	case errors.Is(err, github.ErrForbidden):
		result = "forbidden"
	case errors.Is(err, github.ErrNotFound):
		result = "not_found"
	default:
		result = "error"
	}
	c.metrics.githubRequests.WithLabelValues(installationID, result).Inc()
}
