package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/eyupio/zoomies/internal/agentguidance"
	"github.com/eyupio/zoomies/internal/aicontext"
	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

type KennelGuidancePreview struct {
	BaseCommit  string                 `json:"base_commit"`
	Base        string                 `json:"base"`
	Branch      string                 `json:"branch"`
	PlanHash    string                 `json:"plan_hash"`
	Issues      []agentguidance.Issue  `json:"issues"`
	Files       []agentguidance.Change `json:"files"`
	PullRequest *github.PullRequest    `json:"pull_request,omitempty"`
}

type KennelGuidanceApproval struct {
	PlanHash string `json:"plan_hash"`
}

func (c *Controller) guidanceRepository(ctx context.Context, id string) (*store.KennelRepository, error) {
	if !c.cfg().Kennel.Enabled {
		return nil, ErrKennelOff
	}
	if !c.cfg().Kennel.AgentGuidance {
		return nil, fmt.Errorf("%w: an administrator can turn on kennel.agent_guidance to check and propose agent guidance", store.ErrConflict)
	}
	row, err := c.st.GetKennelRepository(ctx, id)
	if err != nil {
		return nil, err
	}
	if row.Untracked != nil {
		return nil, ErrKennelNotTracked
	}
	return row, nil
}

func (c *Controller) prepareGuidance(ctx context.Context, id string) (*KennelGuidancePreview, github.AgentGuidanceClient, *store.KennelRepository, error) {
	row, err := c.guidanceRepository(ctx, id)
	if err != nil {
		return nil, nil, nil, err
	}
	if c.githubHeld(row.InstallationID, c.Now()) {
		return nil, nil, nil, fmt.Errorf("%w: requests for this installation are held for a GitHub rate limit; try again after the hold ends", store.ErrConflict)
	}
	client, err := c.ClientFor(ctx, row.InstallationID)
	if err != nil {
		return nil, nil, nil, err
	}
	info, err := client.Probe(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	if info == nil || !info.CanReadContents() {
		return nil, nil, nil, fmt.Errorf("%w: agent guidance preview needs Contents read permission on the installation", auth.ErrInvalidInput)
	}
	writer, ok := client.(github.AgentGuidanceClient)
	if !ok {
		return nil, nil, nil, fmt.Errorf("%w: this GitHub client does not support agent guidance proposals", auth.ErrInvalidInput)
	}
	// The reader resolves and pins the current default branch. Verify the
	// installation's stored repository identity before proposing any write.
	source, err := writer.ReadAgentGuidance(ctx, row.FullName, "")
	if err != nil {
		if errors.Is(err, github.ErrSetupConflict) {
			err = fmt.Errorf("%w: %s", store.ErrConflict, err)
		}
		return nil, nil, nil, err
	}
	if source == nil || source.Repository.ID != row.RepositoryID || !strings.EqualFold(source.Repository.FullName, row.FullName) || source.Repository.Archived || source.Repository.DefaultBranch == "" {
		return nil, nil, nil, store.ErrConflict
	}
	files := make([]agentguidance.File, 0, len(source.Files))
	for _, f := range source.Files {
		files = append(files, agentguidance.File{Path: f.Path, SHA: f.SHA, Mode: f.Mode, Content: f.Content})
	}
	plan := &KennelGuidancePreview{BaseCommit: source.Commit, Base: source.Repository.DefaultBranch, Issues: nonNilSlice(agentguidance.Inspect(files, source.Inventory, true)), Files: nonNilSlice(agentguidance.Plan(files, source.Inventory))}
	raw, _ := json.Marshal(struct {
		Repository   string
		Installation string
		RepositoryID int64
		Plan         *KennelGuidancePreview
	}{id, row.InstallationID, row.RepositoryID, plan})
	if len(raw) > 4<<20 {
		return nil, nil, nil, fmt.Errorf("%w: guidance preview exceeds 4 MiB; shorten instruction files before proposing changes", store.ErrConflict)
	}
	plan.PlanHash = aicontext.Hash(raw)
	branchInput, _ := json.Marshal(struct {
		RepositoryID int64
		Files        []agentguidance.Change
	}{row.RepositoryID, plan.Files})
	branchHash := aicontext.Hash(branchInput)
	plan.Branch = fmt.Sprintf("zoomies-agent-guidance-%d-%s", row.RepositoryID, branchHash[:16])
	return plan, writer, row, nil
}

func (c *Controller) PreviewKennelGuidance(ctx context.Context, id string) (*KennelGuidancePreview, error) {
	plan, _, _, err := c.prepareGuidance(ctx, id)
	return plan, err
}

func (c *Controller) CreateKennelGuidancePR(ctx context.Context, id string, approval KennelGuidanceApproval) (*KennelGuidancePreview, error) {
	plan, writer, row, err := c.prepareGuidance(ctx, id)
	if err != nil {
		return nil, err
	}
	if approval.PlanHash != plan.PlanHash {
		return nil, fmt.Errorf("%w: repository or proposal changed after review; preview it again", store.ErrConflict)
	}
	if len(plan.Files) == 0 {
		return nil, fmt.Errorf("%w: there are no automatic guidance changes to propose; review the findings manually", store.ErrConflict)
	}
	client, err := c.ClientFor(ctx, row.InstallationID)
	if err != nil {
		return nil, err
	}
	info, err := client.Probe(ctx)
	if err != nil {
		return nil, err
	}
	if info == nil || info.Permissions["contents"] != "write" || info.Permissions["pull_requests"] != "write" {
		return nil, fmt.Errorf("%w: guidance draft pull requests need Contents write and Pull requests write permission; Workflows write is not required", auth.ErrInvalidInput)
	}
	// Recheck local consent immediately before the external write. The GitHub
	// writer checks the pinned base before publishing and reconciles retries.
	current, err := c.guidanceRepository(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.InstallationID != row.InstallationID || current.RepositoryID != row.RepositoryID || current.FullName != row.FullName {
		return nil, store.ErrConflict
	}
	changes := make([]aicontext.SetupChange, 0, len(plan.Files))
	for _, f := range plan.Files {
		changes = append(changes, aicontext.SetupChange{Path: f.Path, Mode: f.Mode, PreviousSHA: f.PreviousSHA, Content: f.Content})
	}
	pr, err := writer.OpenContextSetup(ctx, github.ContextSetupRequest{Action: "agent-guidance", Repo: row.FullName, Base: plan.Base, BaseCommit: plan.BaseCommit, Head: plan.Branch, PlanHash: plan.PlanHash, Files: changes})
	if errors.Is(err, github.ErrSetupConflict) {
		return nil, fmt.Errorf("%w: %s", store.ErrConflict, err)
	}
	if err != nil {
		return nil, err
	}
	plan.PullRequest = pr
	return plan, nil
}
