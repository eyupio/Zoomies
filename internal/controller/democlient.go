package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

// demoClient stands in for GitHub on the seeded demo installation.
//
// The fixtures exist so that a demo instance, a screenshot and the UI test
// suite have a fleet to show. There is no GitHub App behind them, so every
// call that would leave the process has to be answered here instead. Without
// this the Installations page 500s on the rate limit, the poller logs a
// credential failure every thirty seconds, and the whole demo reads as broken
// software rather than as a working fleet.
//
// Reads answer plausibly. Writes refuse, and say why: a demo fixture cannot
// mint a real runner credential, and pretending otherwise would leave runners
// stuck in provisioning with no explanation.
type demoClient struct {
	target string
}

// ErrDemoFixture is returned by the demo installation for anything that would
// have to reach GitHub to be true.
var ErrDemoFixture = errors.New("this is a seeded demo installation with no GitHub App behind it")

func newDemoClient(inst *store.Installation) github.Client {
	return &demoClient{target: inst.Target}
}

func (d *demoClient) Target() (string, store.TargetType) { return d.target, store.TargetOrg }

func (d *demoClient) Probe(context.Context) (*github.AppInfo, error) {
	return &github.AppInfo{
		ID:    123456,
		Slug:  "zoomies-demo",
		Name:  "Zoomies (demo)",
		Owner: d.target,
		Permissions: map[string]string{
			"organization_self_hosted_runners": "write",
			"actions":                          "read",
			"metadata":                         "read",
		},
		Events: []string{"workflow_job"},
	}, nil
}

func (d *demoClient) CreateJITConfig(context.Context, github.JITRequest) (*github.JITConfig, error) {
	return nil, fmt.Errorf("%w, so it cannot register a runner; create a real installation to run jobs", ErrDemoFixture)
}

func (d *demoClient) CreateRegistrationToken(context.Context) (*github.RegistrationToken, error) {
	return nil, fmt.Errorf("%w, so it cannot mint a registration token", ErrDemoFixture)
}

func (d *demoClient) CreateRemoveToken(context.Context) (*github.RegistrationToken, error) {
	return nil, fmt.Errorf("%w, so it cannot mint a removal token", ErrDemoFixture)
}

// ListRunners returns nothing, so the registration reaper has nothing to reap.
func (d *demoClient) ListRunners(context.Context) ([]github.Runner, error) { return nil, nil }

// DeleteRunner succeeds: the desired end state -- no such registration -- is
// already true.
func (d *demoClient) DeleteRunner(context.Context, int64) error { return nil }

func (d *demoClient) ListRunnerGroups(context.Context) ([]github.RunnerGroup, error) {
	return []github.RunnerGroup{{ID: 1, Name: "Default"}}, nil
}

// ListQueuedJobs returns nothing. The demo's queued jobs are already in the
// database; inventing more on every poll would make the fixture drift.
func (d *demoClient) ListQueuedJobs(context.Context) ([]github.QueuedJob, error) { return nil, nil }

func (d *demoClient) RateLimit(context.Context) (*github.RateLimit, error) {
	return &github.RateLimit{
		Limit:     5000,
		Remaining: 4873,
		ResetAt:   time.Now().Add(41 * time.Minute).UTC(),
	}, nil
}

func (d *demoClient) WebURL() string { return "https://github.com/" + d.target }
