package controller

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/eyupio/zoomies/internal/aicontext"
	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

// AIContextDiscovery describes candidates only; discovery never grants source
// access, enables a repository or changes the GitHub App's permissions.
type AIContextDiscovery struct {
	Repositories            []github.Repository `json:"repositories"`
	CanReadContents         bool                `json:"can_read_contents"`
	MissingSetupPermissions []string            `json:"missing_setup_permissions"`
	DefaultExclusions       []string            `json:"default_exclusions"`
	DefaultKeepSnapshots    int                 `json:"default_keep_snapshots"`
	Capped                  bool                `json:"capped"`
}

func (c *Controller) DiscoverAIContext(ctx context.Context, installationID string) (*AIContextDiscovery, error) {
	client, err := c.ClientFor(ctx, installationID)
	if err != nil {
		return nil, err
	}
	// The fleet's Verify operation may create/adopt a runner group. Discovery
	// probes the client directly so opening the wizard cannot mutate a fleet.
	info, err := client.Probe(ctx)
	if err != nil {
		return nil, err
	}
	repos, err := client.ListRepositories(ctx, 0)
	if err != nil {
		return nil, err
	}
	// ListRepositories has a hard ceiling. Conservatively report that ceiling
	// even when an installation happens to have exactly this many repositories.
	defaults := aicontext.DefaultConfig("main")
	out := &AIContextDiscovery{DefaultExclusions: defaults.Exclude, DefaultKeepSnapshots: defaults.KeepSnapshots, Repositories: repos, CanReadContents: info.CanReadContents(), MissingSetupPermissions: info.MissingForMigration(), Capped: len(repos) >= github.RepositoryDiscoveryLimit}
	if out.Repositories == nil {
		out.Repositories = []github.Repository{}
	}
	if out.MissingSetupPermissions == nil {
		out.MissingSetupPermissions = []string{}
	}
	sort.Slice(out.Repositories, func(i, j int) bool { return out.Repositories[i].FullName < out.Repositories[j].FullName })
	return out, nil
}

// AIContextDraftRequest selects a discovered identity, never a caller-supplied
// host or name. Those fields must come from the installation's own metadata.
type AIContextDraftRequest struct {
	InstallationID string `json:"installation_id"`
	RepositoryID   int64  `json:"repository_id"`
}

func (c *Controller) CreateAIContextDraft(ctx context.Context, req AIContextDraftRequest) (*store.AIContextRepository, error) {
	if req.InstallationID == "" || req.RepositoryID <= 0 {
		return nil, fmt.Errorf("%w: choose a known installation and repository", auth.ErrInvalidInput)
	}
	discovery, err := c.DiscoverAIContext(ctx, req.InstallationID)
	if err != nil {
		return nil, err
	}
	if !discovery.CanReadContents {
		return nil, fmt.Errorf("%w: grant the GitHub App Contents read permission before preparing this repository", auth.ErrInvalidInput)
	}
	var selected *github.Repository
	for i := range discovery.Repositories {
		if discovery.Repositories[i].ID == req.RepositoryID {
			selected = &discovery.Repositories[i]
			break
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("%w: this repository is not in the installation's discovered selection", auth.ErrInvalidInput)
	}
	if selected.Archived {
		return nil, fmt.Errorf("%w: choose an active repository; archived repositories cannot receive setup pull requests", auth.ErrInvalidInput)
	}
	cfg := aicontext.DefaultConfig(selected.DefaultBranch)
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %s", auth.ErrInvalidInput, err)
	}
	inst, err := c.st.GetInstallation(ctx, req.InstallationID)
	if err != nil {
		return nil, err
	}
	host, err := aiContextGitHubHost(inst.APIBaseURL)
	if err != nil {
		return nil, err
	}
	draft := &store.AIContextRepository{Key: aicontext.RepositoryKey{GitHubHost: host, InstallationID: inst.ID, RepositoryID: selected.ID}, FullName: selected.FullName, Config: cfg}
	if err := c.st.CreateAIContextRepository(ctx, draft); err != nil {
		return nil, err
	}
	return draft, nil
}

func aiContextGitHubHost(baseURL string) (string, error) {
	if baseURL == "" {
		return "github.com", nil
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("%w: correct the installation's GitHub API URL", auth.ErrInvalidInput)
	}
	host := strings.ToLower(u.Host)
	if host == "api.github.com" {
		host = "github.com"
	}
	return host, nil
}

func (c *Controller) FindAIContextDraft(ctx context.Context, installationID string, repositoryID int64) (*store.AIContextRepository, error) {
	inst, err := c.st.GetInstallation(ctx, installationID)
	if err != nil {
		return nil, err
	}
	host, err := aiContextGitHubHost(inst.APIBaseURL)
	if err != nil {
		return nil, err
	}
	if repositoryID <= 0 {
		return nil, fmt.Errorf("%w: choose a known repository ID", auth.ErrInvalidInput)
	}
	return c.st.FindAIContextRepository(ctx, aicontext.RepositoryKey{GitHubHost: host, InstallationID: installationID, RepositoryID: repositoryID})
}
