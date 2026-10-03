package api

import (
	"encoding/json"
	"github.com/eyupio/zoomies/internal/aicontext"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
	"net/http"
	"strings"
	"testing"
)

func TestAIContextMaintenanceLifecycle(t *testing.T) {
	h, inst, _ := migrationHarness(t)
	reader, admin := h.user("maintenance-admin", store.RoleAdmin)
	discovery, err := h.ctrl.DiscoverAIContext(h.ctx, inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	selected := discovery.Repositories[0]
	draft := store.AIContextRepository{Key: aicontext.RepositoryKey{GitHubHost: "github.com", InstallationID: inst.ID, RepositoryID: selected.ID}, FullName: selected.FullName, Config: aicontext.DefaultConfig(selected.DefaultBranch)}
	if err = h.st.CreateAIContextRepository(h.ctx, &draft); err != nil {
		t.Fatal(err)
	}
	h.gh.AddFile(draft.FullName, "README.md", "# User project\nKeep this badge and text.\n")
	plan, err := h.ctrl.PreviewAIContextSetup(h.ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = h.ctrl.CreateAIContextSetupPR(h.ctx, draft.ID, controller.AIContextSetupApproval{Revision: plan.Revision, PlanHash: plan.PlanHash})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/ai-context/repositories/" + draft.ID + "/maintenance"
	h.do(request{method: http.MethodPost, path: path, cookie: admin, body: controller.AIContextMaintenanceRequest{Mode: "reinstall", Revision: draft.Revision}}).mustStatus(t, http.StatusConflict, "open setup blocks maintenance")
	if !h.gh.MergeContextPull(draft.FullName, plan.Setup.PRNumber) {
		t.Fatal("merge failed")
	}
	for _, mode := range []string{"reinstall", "amend", "remove", "reinstall"} {
		current, err := h.st.GetAIContextRepository(h.ctx, draft.ID)
		if err != nil {
			t.Fatal(err)
		}
		req := controller.AIContextMaintenanceRequest{Mode: mode, Revision: current.Revision}
		if mode == "amend" {
			cfg := current.Config
			cfg.KeepSnapshots = 5
			cfg.Exclude = append(cfg.Exclude, "private/**")
			req.Config = &cfg
		}
		if mode == "remove" {
			if err = h.st.ReplaceAIContextMembers(h.ctx, draft.ID, []string{reader.ID}); err != nil {
				t.Fatal(err)
			}
		}
		response := h.do(request{method: http.MethodPost, path: path, cookie: admin, body: req})
		response.mustStatus(t, http.StatusOK, "preview "+mode)
		var preview controller.AIContextSetupPreview
		response.into(t, &preview)
		unchanged, _ := h.st.GetAIContextRepository(h.ctx, draft.ID)
		if unchanged.Revision != current.Revision || unchanged.Config.Disabled != current.Config.Disabled {
			t.Fatal("preview changed state")
		}
		req.PlanHash = strings.Repeat("0", 64)
		h.do(request{method: http.MethodPost, path: path + "/apply", cookie: admin, body: req}).mustStatus(t, http.StatusConflict, "wrong approval")
		req.PlanHash = preview.PlanHash
		if mode == "reinstall" {
			h.gh.LoseNextContextPullResponse(draft.FullName)
		}
		response = h.do(request{method: http.MethodPost, path: path + "/apply", cookie: admin, body: req})
		if mode == "reinstall" {
			response = h.do(request{method: http.MethodPost, path: path + "/apply", cookie: admin, body: req})
		}
		response.mustStatus(t, http.StatusOK, "apply "+mode)
		var applied controller.AIContextSetupPreview
		response.into(t, &applied)
		repeated := h.do(request{method: http.MethodPost, path: path + "/apply", cookie: admin, body: req})
		repeated.mustStatus(t, http.StatusOK, "idempotent retry")
		var retry controller.AIContextSetupPreview
		repeated.into(t, &retry)
		if applied.Setup.PRNumber != retry.Setup.PRNumber {
			t.Fatal("duplicate PR")
		}
		after, _ := h.st.GetAIContextRepository(h.ctx, draft.ID)
		if after.Revision != current.Revision+1 || after.Available || after.Config.Disabled != (mode == "remove") {
			t.Fatalf("bad state %+v", after)
		}
		if mode == "amend" && after.Config.KeepSnapshots != 5 {
			t.Fatal("amendment not persisted")
		}
		if mode == "remove" {
			members, err := h.st.AIContextMembers(h.ctx, draft.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(members) != 0 {
				t.Fatal("removal retained members")
			}
			if err = h.ctrl.RefreshAIContext(h.ctx, draft.ID); err == nil {
				t.Fatal("removed setup refreshed")
			}
		}
		if !h.gh.MergeContextPull(draft.FullName, applied.Setup.PRNumber) {
			t.Fatal("maintenance merge failed")
		}
		client, _ := h.ctrl.ClientFor(h.ctx, inst.ID)
		source, err := client.(github.ContextSetupClient).ReadContextSetup(h.ctx, draft.FullName, after.Config.SourceBranch)
		if err != nil {
			t.Fatal(err)
		}
		hasConfig, hasBadge := false, false
		for _, file := range source.Files {
			if file.Path == aicontext.ConfigPath {
				hasConfig = true
				var cfg aicontext.Config
				if err = json.Unmarshal([]byte(file.Content), &cfg); err != nil {
					t.Fatal(err)
				}
				if cfg.SetupGeneration != after.Config.SetupGeneration {
					t.Fatal("stale generation")
				}
			}
			if file.Path == "README.md" {
				if !strings.Contains(file.Content, "Keep this badge and text.") {
					t.Fatal("user text lost")
				}
				hasBadge = strings.Contains(file.Content, "[![Zoomies AI Context]")
			}
		}
		if hasConfig != (mode != "remove") || hasBadge != (mode != "remove") {
			t.Fatalf("managed files/badge after %s: config %v badge %v", mode, hasConfig, hasBadge)
		}
	}
}
