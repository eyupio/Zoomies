package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/controller"
)

func TestGuidancePreviewAndPRRequireAnAdministratorAndAnExactReviewedPlan(t *testing.T) {
	s := newKennelStack(t)
	s.ctrl.UpdateConfig(func(c *config.Config) { c.Kennel.AgentGuidance = true })
	s.gh.SetPermissions(map[string]string{"contents": "write", "pull_requests": "write"})
	s.gh.AddFile("acme/quiet", "Makefile", "test:\n\techo tests\n")
	path := kennelRepository + s.quiet + "/agent-guidance"
	for _, cookie := range []string{s.viewer, s.operator} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			resp := s.do(request{method: method, path: path, cookie: cookie, body: map[string]string{"plan_hash": "not reviewed"}})
			if resp.status != http.StatusForbidden {
				t.Fatalf("%s: status=%d body=%s", method, resp.status, resp.body)
			}
		}
	}
	resp := s.do(request{method: http.MethodGet, path: path, cookie: s.admin})
	if resp.status != http.StatusOK {
		t.Fatalf("preview=%d %s", resp.status, resp.body)
	}
	var plan controller.KennelGuidancePreview
	if err := json.Unmarshal(resp.body, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 2 {
		t.Fatalf("plan=%+v", plan)
	}
	resp = s.do(request{method: http.MethodPost, path: path, cookie: s.admin, body: map[string]string{"plan_hash": "wrong"}})
	if resp.status != http.StatusConflict {
		t.Fatalf("unreviewed proposal=%d %s", resp.status, resp.body)
	}
	resp = s.do(request{method: http.MethodPost, path: path, cookie: s.admin, body: map[string]string{"plan_hash": plan.PlanHash}})
	if resp.status != http.StatusOK {
		t.Fatalf("proposal=%d %s", resp.status, resp.body)
	}
	var result controller.KennelGuidancePreview
	if err := json.Unmarshal(resp.body, &result); err != nil || result.PullRequest == nil {
		t.Fatalf("result=%+v %v", result, err)
	}
	s.ctrl.UpdateConfig(func(c *config.Config) { c.Kennel.AgentGuidance = false })
	before := len(s.gh.Requests())
	resp = s.do(request{method: http.MethodGet, path: path, cookie: s.admin})
	if resp.status != http.StatusConflict || len(s.gh.Requests()) != before {
		t.Fatalf("disabled read=%d %s", resp.status, resp.body)
	}
}
