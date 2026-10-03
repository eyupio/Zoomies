package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/eyupio/zoomies/internal/aicontext"
	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

type AIContextSetupPreview struct {
	Mode         string                  `json:"mode,omitempty"`
	Config       *aicontext.Config       `json:"config,omitempty"`
	PreviousHash string                  `json:"previous_hash,omitempty"`
	Revision     int64                   `json:"revision"`
	BaseCommit   string                  `json:"base_commit"`
	Branch       string                  `json:"branch"`
	PlanHash     string                  `json:"plan_hash"`
	Files        []aicontext.SetupChange `json:"files"`
	Setup        *store.AIContextSetup   `json:"setup,omitempty"`
}

type AIContextSetupApproval struct {
	Revision int64  `json:"revision"`
	PlanHash string `json:"plan_hash"`
}

func (c *Controller) contextSetupClient(ctx context.Context, r *store.AIContextRepository) (github.ContextSetupClient, *github.ContextSetupSource, error) {
	client, err := c.ClientFor(ctx, r.Key.InstallationID)
	if err != nil {
		return nil, nil, err
	}
	info, err := client.Probe(ctx)
	if err != nil {
		return nil, nil, err
	}
	if !info.CanReadContents() {
		_ = c.st.SetAIContextAvailable(ctx, r.ID, false)
		return nil, nil, fmt.Errorf("%w: grant Contents read permission to inspect repository setup", auth.ErrInvalidInput)
	}
	if missing := info.MissingForMigration(); len(missing) > 0 {
		return nil, nil, fmt.Errorf("%w: setup PRs need Contents write, Workflows write and Pull requests write; accept these permissions on the installation", auth.ErrInvalidInput)
	}
	setupClient, ok := client.(github.ContextSetupClient)
	if !ok {
		return nil, nil, fmt.Errorf("%w: this GitHub client does not support managed context setup", auth.ErrInvalidInput)
	}
	source, err := setupClient.ReadContextSetup(ctx, r.FullName, r.Config.SourceBranch)
	if err != nil {
		return nil, nil, err
	}
	if source.Repository.ID != r.Key.RepositoryID || source.Repository.FullName != r.FullName || source.Repository.Archived || source.Repository.DefaultBranch != r.Config.SourceBranch {
		return nil, nil, fmt.Errorf("%w: repository identity, default branch or archived state changed; review its setup", auth.ErrInvalidInput)
	}
	return setupClient, source, nil
}

func (c *Controller) PreviewAIContextSetup(ctx context.Context, id string) (*AIContextSetupPreview, error) {
	r, err := c.st.GetAIContextRepository(ctx, id)
	if err != nil {
		return nil, err
	}
	_, source, err := c.contextSetupClient(ctx, r)
	if err != nil {
		if errors.Is(err, github.ErrSetupConflict) {
			return nil, fmt.Errorf("%w: %s", store.ErrConflict, err)
		}
		return nil, err
	}
	saved, err := c.st.GetAIContextSetup(ctx, id)
	if err == nil {
		var plan AIContextSetupPreview
		if err := json.Unmarshal([]byte(saved.PlanJSON), &plan); err != nil {
			return nil, err
		}
		plan.Setup = saved
		return &plan, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	files, err := aicontext.PlanManagedSetup(r.Key, r.FullName, r.Config, source.Files)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", auth.ErrInvalidInput, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%w: managed files already match; setup repair/reconciliation is required before another PR", auth.ErrInvalidInput)
	}
	plan := &AIContextSetupPreview{Revision: r.Revision, BaseCommit: source.Commit, Files: files}
	hashInputs, _ := json.Marshal(struct {
		Repository aicontext.RepositoryKey
		Name       string
		Revision   int64
		BaseCommit string
		Files      []aicontext.SetupChange
	}{r.Key, r.FullName, r.Revision, source.Commit, files})
	plan.PlanHash = aicontext.Hash(hashInputs)
	plan.Branch = fmt.Sprintf("zoomies-ai-context-setup-%d-%s", r.Key.RepositoryID, plan.PlanHash[:16])
	if encoded, _ := json.Marshal(plan); len(encoded) > 4<<20 {
		return nil, fmt.Errorf("%w: combined setup preview exceeds its size limit; shorten large README or instruction files before setup", auth.ErrInvalidInput)
	}
	return plan, nil
}

func (c *Controller) CreateAIContextSetupPR(ctx context.Context, id string, approval AIContextSetupApproval) (*AIContextSetupPreview, error) {
	plan, err := c.PreviewAIContextSetup(ctx, id)
	if err != nil {
		return nil, err
	}
	if approval.Revision != plan.Revision || approval.PlanHash != plan.PlanHash {
		return nil, store.ErrConflict
	}
	if plan.Setup != nil && plan.Setup.State == "awaiting_merge" {
		return plan, nil
	}
	r, err := c.st.GetAIContextRepository(ctx, id)
	if err != nil {
		return nil, err
	}
	client, source, err := c.contextSetupClient(ctx, r)
	if err != nil {
		return nil, err
	}
	if plan.Setup == nil && source.Commit != plan.BaseCommit {
		return nil, fmt.Errorf("%w: source branch changed after review; no repository writes were made", store.ErrConflict)
	}
	payload, _ := json.Marshal(plan)
	operation := &store.AIContextSetup{RepositoryID: id, Revision: plan.Revision, PlanHash: plan.PlanHash, PlanJSON: string(payload)}
	if err := c.st.ClaimAIContextSetup(ctx, operation); err != nil {
		return nil, err
	}
	return c.publishAIContextPlan(ctx, r, client, plan, operation)
}

func (c *Controller) publishAIContextPlan(ctx context.Context, r *store.AIContextRepository, client github.ContextSetupClient, plan *AIContextSetupPreview, operation *store.AIContextSetup) (*AIContextSetupPreview, error) {
	// An HTTP disconnect must not strand an otherwise finished PR. The
	// bounded operation is persisted first, and safe to reconcile after crash.
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Minute)
	defer cancel()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = c.st.ReleaseAIContextSetup(cleanup, r.ID, operation.LeaseToken)
	}()
	pr, err := client.OpenContextSetup(detached, github.ContextSetupRequest{Action: plan.Mode, Repo: r.FullName, Base: r.Config.SourceBranch, BaseCommit: plan.BaseCommit, Head: plan.Branch, PlanHash: plan.PlanHash, Files: plan.Files})
	if err != nil {
		if errors.Is(err, github.ErrSetupConflict) {
			return nil, fmt.Errorf("%w: %s", store.ErrConflict, err)
		}
		return nil, err
	}
	if err := c.st.CompleteAIContextSetup(detached, r.ID, operation.LeaseToken, pr.Number, pr.HTMLURL); err != nil {
		return nil, err
	}
	plan.Setup, err = c.st.GetAIContextSetup(detached, r.ID)
	return plan, err
}
