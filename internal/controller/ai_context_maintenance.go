package controller

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/eyupio/zoomies/internal/aicontext"
	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

type AIContextMaintenanceRequest struct {
	Mode     string            `json:"mode"`
	Revision int64             `json:"revision"`
	Config   *aicontext.Config `json:"config,omitempty"`
	PlanHash string            `json:"plan_hash,omitempty"`
}

func (c *Controller) PreviewAIContextMaintenance(ctx context.Context, id string, req AIContextMaintenanceRequest) (*AIContextSetupPreview, error) {
	if req.Mode != "reinstall" && req.Mode != "amend" && req.Mode != "remove" {
		return nil, fmt.Errorf("%w: choose reinstall, amend or remove", auth.ErrInvalidInput)
	}
	r, err := c.st.GetAIContextRepository(ctx, id)
	if err != nil {
		return nil, err
	}
	saved, err := c.st.GetAIContextSetup(ctx, id)
	if err != nil {
		return nil, err
	}
	var previous AIContextSetupPreview
	if err = json.Unmarshal([]byte(saved.PlanJSON), &previous); err != nil {
		return nil, err
	}
	// A lost publication response resumes the persisted operation, never a new one.
	if req.PlanHash != "" && req.PlanHash == saved.PlanHash && req.Mode == previous.Mode {
		previous.Setup = saved
		return &previous, nil
	}
	if req.Revision != r.Revision || saved.PRNumber == 0 {
		return nil, fmt.Errorf("%w: resume the existing proposal before maintenance", store.ErrConflict)
	}
	client, source, err := c.contextSetupClient(ctx, r)
	if err != nil {
		return nil, err
	}
	statusClient, ok := client.(github.ContextSetupStatusClient)
	if !ok {
		return nil, fmt.Errorf("%w: GitHub cannot verify the previous proposal", auth.ErrInvalidInput)
	}
	status, err := statusClient.ContextSetupStatus(ctx, r.FullName, saved.PRNumber, r.Config.SourceBranch)
	if err != nil {
		return nil, err
	}
	if status != "merged" && status != "closed" {
		return nil, fmt.Errorf("%w: merge or close the existing setup PR before maintenance", store.ErrConflict)
	}
	config := r.Config
	if req.Mode == "amend" {
		if req.Config == nil {
			return nil, fmt.Errorf("%w: provide amended settings", auth.ErrInvalidInput)
		}
		config = *req.Config
	}
	if config.SourceBranch != r.Config.SourceBranch {
		return nil, fmt.Errorf("%w: keep the verified source branch", auth.ErrInvalidInput)
	}
	config.ReadmeBadge = nil
	// The upload address follows the controller, never the request: a
	// reinstall after server.external_url changed re-aims the workflow too.
	config.UploadURL = ""
	if config.Destination == aicontext.Zoomies {
		config.UploadURL = aicontext.UploadURLFor(c.cfg().Server.ExternalURL)
	}
	config.SetupGeneration = r.Config.SetupGeneration + 1
	config.Disabled = req.Mode == "remove"
	if err = config.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %s", auth.ErrInvalidInput, err)
	}
	files, err := aicontext.PlanMaintenance(r.Key, r.FullName, r.Config, config, source.Files, previous.Files, req.Mode == "remove")
	if err != nil {
		return nil, fmt.Errorf("%w: %s", auth.ErrInvalidInput, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%w: no managed files remain to remove", auth.ErrInvalidInput)
	}
	plan := &AIContextSetupPreview{Mode: req.Mode, Config: &config, PreviousHash: saved.PlanHash, Revision: r.Revision, BaseCommit: source.Commit, Files: files}
	payload, _ := json.Marshal(struct {
		Key  aicontext.RepositoryKey
		Plan *AIContextSetupPreview
	}{r.Key, plan})
	plan.PlanHash = aicontext.Hash(payload)
	plan.Branch = fmt.Sprintf("zoomies-ai-context-%s-%d-%s", req.Mode, r.Key.RepositoryID, plan.PlanHash[:16])
	if payload, _ = json.Marshal(plan); len(payload) > 4<<20 {
		return nil, fmt.Errorf("%w: maintenance preview exceeds size limit", auth.ErrInvalidInput)
	}
	return plan, nil
}

func (c *Controller) ApplyAIContextMaintenance(ctx context.Context, id string, req AIContextMaintenanceRequest) (*AIContextSetupPreview, error) {
	if len(req.PlanHash) != 64 {
		return nil, auth.ErrInvalidInput
	}
	plan, err := c.PreviewAIContextMaintenance(ctx, id, req)
	if err != nil {
		return nil, err
	}
	if plan.PlanHash != req.PlanHash {
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
	operation := &store.AIContextSetup{RepositoryID: id, PlanHash: plan.PlanHash}
	if plan.Setup != nil {
		operation.Revision = plan.Revision
		operation.PlanJSON = plan.Setup.PlanJSON
		err = c.st.ClaimAIContextSetup(ctx, operation)
	} else {
		if source.Commit != plan.BaseCommit {
			return nil, store.ErrConflict
		}
		plan.Revision++
		payload, _ := json.Marshal(plan)
		operation.Revision = plan.Revision
		operation.PlanJSON = string(payload)
		err = c.st.ClaimAIContextMaintenance(ctx, operation, plan.PreviousHash, *plan.Config)
	}
	if err != nil {
		return nil, err
	}
	return c.publishAIContextPlan(ctx, r, client, plan, operation)
}
