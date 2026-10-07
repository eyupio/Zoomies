package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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
			// Read-only source access, so AI Context's wizard can be shown
			// preparing a draft. Setup writes still refuse, as every write does.
			"contents": "read",
		},
		Events: []string{"workflow_job"},
		// The demo fixture is a fleet with nothing wrong, and an installation
		// on "selected" would read as one of the two setup mistakes the verify
		// dialog exists to name.
		RepositorySelection: "all",
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

func (d *demoClient) CreateRunnerGroup(context.Context, github.RunnerGroupCreate) (*github.RunnerGroup, error) {
	return nil, errors.New("the demo installation cannot create runner groups")
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

// The migration surface. Reads answer with the fixture's repositories so the
// wizard has something to render in a demo; the write refuses, because a
// fixture has no App behind it and a pull request that silently went nowhere
// would be worse than a refusal that says why.

func (d *demoClient) ListRepositories(context.Context, int) ([]github.Repository, error) {
	names := demoRepoNames()
	out := make([]github.Repository, 0, len(names))
	for _, name := range names {
		out = append(out, github.Repository{
			// Stable numeric IDs, as GitHub gives: AI Context keys a
			// repository by its ID so a rename cannot move its source grants.
			ID:            demoRepoID(name),
			FullName:      name,
			DefaultBranch: "main",
			Private:       true,
			Archived:      slices.Contains(demoArchivedRepos, name),
			HTMLURL:       "https://github.com/" + name,
		})
	}
	return out, nil
}

// demoPublicRepo is the one fixture repository Kennel Club is told is public,
// so a demo has a finding to show. The migration wizard above keeps listing every
// repository as private, because what it does with visibility is not what Kennel
// Club does, and a demo that changed its wizard to make a second page interesting
// would be a worse demo of both.
const demoPublicRepo = "acme/site"

// demoRepoNames is every repository the fixture's organisation has, in the order
// whose position gives each its stable numeric ID.
func demoRepoNames() []string {
	names := append(append([]string{}, demoRepos...), demoQuietRepos...)
	return append(append(names, demoMigratedRepos...), demoArchivedRepos...)
}

func demoRepoID(name string) int64 {
	if i := slices.Index(demoRepoNames(), name); i >= 0 {
		return int64(900001 + i)
	}
	return 0
}

// KennelRepositories answers Kennel Club from the fixture, which is the only
// place it could be answered from: there is no GitHub App behind the demo, so
// asking one would only put a failure on the Overview that says nothing about the
// fleet. It never reaches GitHub, and Kennel Club's own request counters never
// see it.
func (d *demoClient) KennelRepositories(ctx context.Context, limit int) ([]github.Repository, error) {
	repos, err := d.ListRepositories(ctx, limit)
	if err != nil {
		return nil, err
	}
	for i := range repos {
		repos[i].Private, repos[i].Visibility = true, "private"
		if repos[i].FullName == demoPublicRepo {
			repos[i].Private, repos[i].Visibility = false, "public"
		}
	}
	return repos, nil
}

// KennelRun says a fixture run was a push from the repository itself, which is
// nothing a stranger started: the demo's finding is about where the repository
// runs, not about who triggered it.
func (d *demoClient) KennelRun(_ context.Context, repo string, runID int64) (*github.KennelRun, error) {
	id := demoRepoID(repo)
	return &github.KennelRun{ID: runID, Event: "push", RepositoryID: id, HeadRepositoryID: id}, nil
}

var _ github.RepoReader = (*demoClient)(nil)

// ListWorkflows gives the fixture the answers a real organisation gives: a
// repository with one workflow, a repository with several -- so the wizard's
// per-file choice has something to choose between -- a repository with no
// workflows at all, which is what the "hide repositories with nothing to move"
// filter exists for, and one that has already been migrated, which has nothing
// to move for the opposite reason.
func (d *demoClient) ListWorkflows(_ context.Context, repo string) ([]github.WorkflowFile, error) {
	for _, quiet := range demoQuietRepos {
		if repo == quiet {
			return nil, fmt.Errorf("%s: %w", repo, github.ErrNoWorkflows)
		}
	}
	sha := "demo" + strings.ReplaceAll(repo, "/", "")
	if slices.Contains(demoMigratedRepos, repo) {
		return []github.WorkflowFile{{
			Path:    ".github/workflows/ci.yml",
			SHA:     sha,
			Content: demoMigratedWorkflow,
		}}, nil
	}
	out := []github.WorkflowFile{{
		Path:    ".github/workflows/ci.yml",
		SHA:     sha,
		Content: demoWorkflow,
	}}
	if repo == demoRepos[0] {
		out = append(out, github.WorkflowFile{
			Path:    ".github/workflows/release.yml",
			SHA:     sha + "release",
			Content: demoReleaseWorkflow,
		})
	}
	return out, nil
}

// ReadReadme answers as a repository with no README does. The demo cannot open
// a pull request, so nothing would be written into one anyway.
func (d *demoClient) ReadReadme(_ context.Context, repo string) (*github.Readme, error) {
	return nil, fmt.Errorf("%s: %w", repo, github.ErrNoReadme)
}

func (d *demoClient) OpenPullRequest(context.Context, github.PullRequestRequest) (*github.PullRequest, error) {
	return nil, fmt.Errorf("%w, so it cannot open a pull request; connect a real installation to migrate a repository", ErrDemoFixture)
}

// demoMigratedWorkflow is a repository somebody has already moved. It reads as
// "nothing to do" in exactly the same way as a repository nobody has touched,
// which is why the wizard has to tell the two apart by name.
const demoMigratedWorkflow = `name: CI

on: [push]

jobs:
  build:
    runs-on: zoomies-demo-linux-x64
    steps:
      - uses: actions/checkout@v4
      - run: make build
`

// demoReleaseWorkflow is the fixture's second workflow file in one repository:
// a release is exactly the workflow an operator might want to leave on GitHub's
// runners while the rest of the repository moves.
const demoReleaseWorkflow = `name: Release

on:
  push:
    tags: ["v*"]

jobs:
  publish:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: make release
`

// demoWorkflow is a workflow with one job the wizard can migrate and one it
// must refuse to touch, so a demo shows both halves of the review step.
const demoWorkflow = `name: CI

on: [push]

jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: make build
  matrix:
    strategy:
      matrix:
        os: [ubuntu-latest, macos-14]
    runs-on: ${{ matrix.os }}
    steps:
      - run: make test
`

func (d *demoClient) GetWorkflowJob(context.Context, string, int64) (*github.WorkflowJobEvent, error) {
	return nil, ErrDemoFixture
}

func (d *demoClient) GetWorkflowRun(context.Context, string, int64) (*github.WorkflowRunState, error) {
	return nil, ErrDemoFixture
}

func (d *demoClient) RerunWorkflowJob(context.Context, string, int64) error {
	return nil
}

func (d *demoClient) RerunFailedWorkflowJobs(context.Context, string, int64) error {
	return ErrDemoFixture
}

func (d *demoClient) CancelWorkflowRun(context.Context, string, int64, bool) error {
	return ErrDemoFixture
}
