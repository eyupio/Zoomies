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
	// groups maps runner group names to their IDs for this installation. It is
	// filled lazily because most pools use the default group and the lookup
	// costs an API call.
	groups map[string]int64
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
func (c *Controller) Forget(installationID string) { c.clients.forget(installationID) }

// runnerGroupID resolves a runner group name to the ID the JIT config API
// wants. An empty or unknown name falls back to 0, which the github package
// turns into the Default group every target has.
func (cc *clientCache) runnerGroupID(ctx context.Context, inst *store.Installation, client github.Client, name string) int64 {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "default") {
		return 0
	}

	cc.mu.Lock()
	e := cc.entries[inst.ID]
	if e != nil && e.groups != nil {
		if id, ok := e.groups[strings.ToLower(name)]; ok {
			cc.mu.Unlock()
			return id
		}
	}
	cc.mu.Unlock()

	groups, err := client.ListRunnerGroups(ctx)
	if err != nil {
		// A pool naming a group Zoomies cannot list still deserves a runner;
		// GitHub will place it in Default and the operator sees the warning.
		cc.c.log.Warn("could not list runner groups; falling back to the default group",
			"installation", inst.ID, "group", name, "error", err)
		return 0
	}

	found := int64(0)
	m := make(map[string]int64, len(groups))
	for _, g := range groups {
		m[strings.ToLower(g.Name)] = g.ID
		if strings.EqualFold(g.Name, name) {
			found = g.ID
		}
	}

	cc.mu.Lock()
	if e := cc.entries[inst.ID]; e != nil {
		e.groups = m
	}
	cc.mu.Unlock()

	if found == 0 {
		cc.c.log.Warn("the pool names a runner group that does not exist on the target; using the default group",
			"installation", inst.ID, "target", inst.Target, "group", name)
	}
	return found
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
		if _, err := c.ProbeInstallation(ctx, inst.ID); err != nil {
			c.log.Warn("installation credentials are not usable",
				"installation", inst.ID, "target", inst.Target, "error", err)
		}
	}
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
	}
	c.observeGitHub(inst.ID, err)

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
	inst.LastError = msg
	now := c.Now()
	inst.LastCheckedAt = &now
	c.publish(events.KindInstallation, "installation:"+inst.ID, inst)

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
