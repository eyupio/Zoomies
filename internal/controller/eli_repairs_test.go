package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/prrepair"
	"github.com/eyupio/zoomies/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRepairAdmissionRequiresLinkedHumanCommentAndDeduplicates(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	fake := &repairFake{Client: h.gh.Client(inst.Target, inst.TargetType)}
	h.c.clients.entries[inst.ID] = &clientEntry{client: fake, updatedAt: inst.UpdatedAt.Truncate(time.Millisecond)}
	repo := inst.Target + "/repo"
	p := &store.AssistantProvider{Name: "install", Kind: "openai", Enabled: true}
	if e := h.st.CreateAssistantProvider(h.ctx, p); e != nil {
		t.Fatal(e)
	}
	policy := store.EliRepairPolicy{Repo: repo, InstallationID: inst.ID, ProviderID: p.ID, Enabled: true, Automatic: true, DailyLimit: 5}
	if e := h.st.SetEliRepairPolicy(h.ctx, policy); e != nil {
		t.Fatal(e)
	}
	user := &store.User{Username: "requester", Role: store.RoleViewer}
	if e := h.st.CreateUser(h.ctx, user); e != nil {
		t.Fatal(e)
	}
	if e := h.st.SetEliIdentity(h.ctx, store.EliIdentity{UserID: user.ID, GitHubUserID: 42, GitHubLogin: "octo"}); e != nil {
		t.Fatal(e)
	}
	if e := h.st.ConfirmEliIdentity(h.ctx, user.ID, 42, true); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		action, kind   string
		author, sender int64
		pr             bool
	}{{"created", "User", 9, 9, true}, {"edited", "User", 42, 42, true}, {"created", "Bot", 42, 42, true}, {"created", "User", 42, 9, true}, {"created", "User", 42, 42, false}, {"created", "User", 42, 42, true}, {"created", "User", 42, 42, true}} {
		issue := map[string]any{"number": 1}
		if tc.pr {
			issue["pull_request"] = map[string]any{}
		}
		body, _ := json.Marshal(map[string]any{"action": tc.action, "repository": map[string]any{"full_name": repo}, "issue": issue, "comment": map[string]any{"id": 100, "body": "/eli fix this PR", "user": map[string]any{"id": tc.author, "login": "octo", "type": tc.kind}}, "sender": map[string]any{"id": tc.sender}})
		if rec := h.deliver("issue_comment", body, testWebhookSecret); rec.Code != 202 {
			t.Fatalf("signed delivery: %d %s", rec.Code, rec.Body.String())
		}
		if rec := h.deliver("issue_comment", body, "forged"); rec.Code >= 200 && rec.Code < 300 {
			t.Fatal("forged repair delivery accepted")
		}
	}
	rows, e := h.st.ListEliRepairs(h.ctx)
	if e != nil || len(rows) != 1 || rows[0].UserID != user.ID || rows[0].State != "queued" {
		t.Fatalf("rows %+v %v", rows, e)
	}
}

// The GitHub transport is fake, but the model adapter, owner selection, durable
// progress and publication decision are the same path the worker runs.
type repairFake struct {
	github.Client
	commits        int
	check          string
	onRead         func()
	unsupportedRun bool
	noWrite        bool
	reacted        []int64
}

func (f *repairFake) RepairActor(context.Context, string, string) (github.RepairActor, error) {
	return github.RepairActor{ID: 42, Login: "octo", CanWrite: !f.noWrite}, nil
}
func (f *repairFake) RepairPull(context.Context, string, int) (github.RepairPull, error) {
	head := "old"
	if f.commits > 0 {
		head = "fixed"
	}
	return github.RepairPull{Number: 1, Branch: "feature", HeadSHA: head, Open: true}, nil
}
func (f *repairFake) RepairPullForRun(ctx context.Context, repo string, _ int64) (github.RepairPull, error) {
	if f.unsupportedRun {
		return github.RepairPull{}, github.ErrRepairUnsupported
	}
	return f.RepairPull(ctx, repo, 1)
}
func (f *repairFake) RepairSource(context.Context, string, github.RepairPull, bool) (*github.RepairSource, error) {
	if f.onRead != nil {
		f.onRead()
	}
	return &github.RepairSource{Snapshot: prrepair.Snapshot{Files: []prrepair.File{{Path: "main.go", Content: "old", Mode: "100644"}}}}, nil
}
func (f *repairFake) RepairFailures(context.Context, string, github.RepairPull, int64) ([]string, error) {
	return []string{"main.go: test failed"}, nil
}
func (f *repairFake) CommitRepair(_ context.Context, _ string, p github.RepairPull, plan prrepair.Plan) (string, error) {
	if p.HeadSHA != "old" || len(plan.Files) != 1 || plan.Files[0].Content != "fixed" {
		return "", fmt.Errorf("bad plan")
	}
	f.commits++
	return "fixed", nil
}
func (f *repairFake) RepairComment(context.Context, string, int, int64, string) (int64, error) {
	return 1, nil
}
func (f *repairFake) RepairReact(_ context.Context, _ string, id int64, content string) error {
	if content != "eyes" {
		return fmt.Errorf("unexpected reaction %q", content)
	}
	f.reacted = append(f.reacted, id)
	return nil
}
func (f *repairFake) RepairChecks(context.Context, string, string) (string, error) {
	return f.check, nil
}
func TestRepairWorkerSelectsTheHybridProviderAndRecordsRealCheckOutcome(t *testing.T) {
	for _, trigger := range []string{"mention", "automatic", "revoked", "expired-lease", "cancelled", "queued-old"} {
		t.Run(trigger, func(t *testing.T) {
			h := newHarness(t)
			inst := h.installation()
			fake := &repairFake{Client: h.gh.Client(inst.Target, inst.TargetType), check: "failed"}
			h.c.clients.entries[inst.ID] = &clientEntry{client: fake, updatedAt: inst.UpdatedAt.Truncate(time.Millisecond)}
			var models []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in struct {
					Model string `json:"model"`
				}
				json.NewDecoder(r.Body).Decode(&in)
				models = append(models, in.Model)
				w.Header().Set("Content-Type", "text/event-stream")
				chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": `{"summary":"Fix cause","files":[{"path":"main.go","content":"fixed"}]}`}, "finish_reason": nil}}})
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			}))
			defer srv.Close()
			user := &store.User{Username: "octo", Role: store.RoleViewer}
			h.st.CreateUser(h.ctx, user)
			h.st.SetEliIdentity(h.ctx, store.EliIdentity{UserID: user.ID, GitHubUserID: 42, GitHubLogin: "octo"})
			h.st.ConfirmEliIdentity(h.ctx, user.ID, 42, true)
			shared := &store.AssistantProvider{Name: "Shared", Kind: "openai_compatible", BaseURL: srv.URL, Model: "installation-model", Enabled: true}
			personal := &store.AssistantProvider{Name: "Personal", OwnerID: user.ID, Kind: "openai_compatible", BaseURL: srv.URL, Model: "personal-model", Enabled: true}
			for _, p := range []*store.AssistantProvider{shared, personal} {
				if e := h.st.CreateAssistantProvider(h.ctx, p); e != nil {
					t.Fatal(e)
				}
				h.st.SetDefaultAssistantProvider(h.ctx, p.ID)
			}
			repo := "acme/repo"
			h.st.SetEliRepairPolicy(h.ctx, store.EliRepairPolicy{Repo: repo, InstallationID: inst.ID, ProviderID: shared.ID, Enabled: true, Automatic: true, DailyLimit: 5})
			actualTrigger := "mention"
			if trigger == "automatic" {
				actualTrigger = "automatic"
			}
			if trigger == "revoked" || trigger == "expired-lease" {
				actualTrigger = "mention"
				fake.onRead = func() {
					if trigger == "revoked" {
						h.st.ConfirmEliIdentity(h.ctx, user.ID, 42, false)
					} else {
						h.c.lease = &store.ControllerLease{Holder: "stale", RenewedAt: h.c.Now().Add(-2 * LeaseTTL)}
					}
				}
			}
			dedup := trigger
			if trigger == "mention" {
				dedup = "comment:acme/repo:77"
			}
			row := &store.EliRepair{DedupKey: dedup, Repo: repo, InstallationID: inst.ID, PullNumber: 1, HeadSHA: "old", RunID: 9, Trigger: actualTrigger, UserID: user.ID, GitHubUserID: 42, GitHubLogin: "octo"}
			h.st.EnqueueEliRepair(h.ctx, row, 5)
			if trigger == "queued-old" {
				h.advance(2 * time.Hour)
				fake.check = "waiting"
			}
			runCtx := h.ctx
			if trigger == "cancelled" {
				var cancel context.CancelFunc
				runCtx, cancel = context.WithCancel(h.ctx)
				defer cancel()
				fake.onRead = cancel
			}
			h.c.runEliRepair(runCtx, row)
			if trigger == "cancelled" {
				rows, _ := h.st.ListEliRepairs(h.ctx)
				if len(rows) != 1 || rows[0].State != "failed" || fake.commits != 0 {
					t.Fatalf("cancelled repair stuck: %+v", rows)
				}
				return
			}
			if trigger == "expired-lease" {
				if fake.commits != 0 || row.State != "failed" || !strings.Contains(row.Message, "stopped before publication") {
					t.Fatalf("published without a current lease: %+v", row)
				}
				return
			}
			if trigger == "revoked" {
				if fake.commits != 0 || row.State != "failed" || !strings.Contains(row.Message, "access changed") {
					t.Fatalf("published after revocation: %+v", row)
				}
				return
			}
			want := "personal-model"
			if trigger == "automatic" {
				want = "installation-model"
			}
			if len(models) != 1 || models[0] != want || row.State != "checking" || fake.commits != 1 {
				t.Fatalf("models %v repair %+v commits %d", models, row, fake.commits)
			}
			// The comment that asked is acknowledged; a repair nobody asked for in a
			// comment has nothing to react to.
			if trigger == "mention" && (len(fake.reacted) != 1 || fake.reacted[0] != 77) || trigger == "automatic" && len(fake.reacted) != 0 {
				t.Fatalf("reactions %v for a %s repair", fake.reacted, trigger)
			}
			h.advance(31 * time.Second)
			h.c.checkEliRepairs(h.ctx)
			if trigger == "queued-old" {
				rows, _ := h.st.CheckingEliRepairs(h.ctx)
				if len(rows) != 1 {
					t.Fatal("queue age shortened the verification window")
				}
				h.advance(time.Hour)
				h.c.checkEliRepairs(h.ctx)
				all, _ := h.st.ListEliRepairs(h.ctx)
				if all[0].State != "unverified" {
					t.Fatalf("verification never expired: %+v", all[0])
				}
				return
			}
			rows, _ := h.st.CheckingEliRepairs(h.ctx)
			if len(rows) != 0 {
				t.Fatal("failed checks remained pending")
			}
			all, _ := h.st.ListEliRepairs(h.ctx)
			if all[0].State != "checks_failed" {
				t.Fatalf("false success %+v", all[0])
			}
		})
	}
}
func TestRepairWorkerStopsBeforeModelUseWhenPersonalProviderIsMissing(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	fake := &repairFake{Client: h.gh.Client(inst.Target, inst.TargetType)}
	h.c.clients.entries[inst.ID] = &clientEntry{client: fake, updatedAt: inst.UpdatedAt.Truncate(time.Millisecond)}
	user := &store.User{Username: "octo", Role: store.RoleViewer}
	h.st.CreateUser(h.ctx, user)
	h.st.SetEliIdentity(h.ctx, store.EliIdentity{UserID: user.ID, GitHubUserID: 42, GitHubLogin: "octo"})
	h.st.ConfirmEliIdentity(h.ctx, user.ID, 42, true)
	p := &store.AssistantProvider{Name: "Shared", Kind: "openai", Enabled: true}
	h.st.CreateAssistantProvider(h.ctx, p)
	h.st.SetDefaultAssistantProvider(h.ctx, p.ID)
	h.st.SetEliRepairPolicy(h.ctx, store.EliRepairPolicy{Repo: "acme/repo", InstallationID: inst.ID, ProviderID: p.ID, Enabled: true, DailyLimit: 5})
	row := &store.EliRepair{DedupKey: "missing", Repo: "acme/repo", InstallationID: inst.ID, PullNumber: 1, Trigger: "mention", UserID: user.ID, GitHubUserID: 42, GitHubLogin: "octo"}
	h.st.EnqueueEliRepair(h.ctx, row, 5)
	h.c.runEliRepair(h.ctx, row)
	if row.State != "failed" || fake.commits != 0 || !strings.Contains(row.Message, "personal default") {
		t.Fatalf("installation fallback: %+v", row)
	}
}

func TestAutomaticRepairIgnoresNonPRFailuresBeforeChargingTheBudget(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	repo := inst.Target + "/repo"
	fake := &repairFake{Client: h.gh.Client(inst.Target, inst.TargetType), unsupportedRun: true}
	h.c.clients.entries[inst.ID] = &clientEntry{client: fake, updatedAt: inst.UpdatedAt.Truncate(time.Millisecond)}
	p := &store.AssistantProvider{Name: "Install", Kind: "openai", Enabled: true}
	h.st.CreateAssistantProvider(h.ctx, p)
	h.st.SetEliRepairPolicy(h.ctx, store.EliRepairPolicy{Repo: repo, InstallationID: inst.ID, ProviderID: p.ID, Enabled: true, Automatic: true, DailyLimit: 1})
	body, _ := json.Marshal(map[string]any{"action": "completed", "repository": map[string]any{"full_name": repo}, "workflow_job": map[string]any{"id": 12, "run_id": 9, "head_sha": "old", "conclusion": "failure"}})
	if err := h.c.enqueueRepairWebhook(h.ctx, inst, "workflow_job", body); err != nil {
		t.Fatal(err)
	}
	rows, _ := h.st.ListEliRepairs(h.ctx)
	if len(rows) != 0 {
		t.Fatal("non-PR job charged the budget")
	}
	fake.unsupportedRun = false
	if err := h.c.enqueueRepairWebhook(h.ctx, inst, "workflow_job", body); err != nil {
		t.Fatal(err)
	}
	rows, _ = h.st.ListEliRepairs(h.ctx)
	if len(rows) != 1 || rows[0].PullNumber != 1 {
		t.Fatalf("PR was not admitted: %+v", rows)
	}
}

func TestLinkedCommentCannotSpendTheBudgetAfterRepositoryAccessIsRemoved(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	repo := inst.Target + "/repo"
	fake := &repairFake{Client: h.gh.Client(inst.Target, inst.TargetType), noWrite: true}
	h.c.clients.entries[inst.ID] = &clientEntry{client: fake, updatedAt: inst.UpdatedAt.Truncate(time.Millisecond)}
	p := &store.AssistantProvider{Name: "Install", Kind: "openai", Enabled: true}
	h.st.CreateAssistantProvider(h.ctx, p)
	h.st.SetEliRepairPolicy(h.ctx, store.EliRepairPolicy{Repo: repo, InstallationID: inst.ID, ProviderID: p.ID, Enabled: true, DailyLimit: 1})
	user := &store.User{Username: "octo", Role: store.RoleViewer}
	h.st.CreateUser(h.ctx, user)
	h.st.SetEliIdentity(h.ctx, store.EliIdentity{UserID: user.ID, GitHubUserID: 42, GitHubLogin: "octo"})
	h.st.ConfirmEliIdentity(h.ctx, user.ID, 42, true)
	body, _ := json.Marshal(map[string]any{"action": "created", "repository": map[string]any{"full_name": repo}, "issue": map[string]any{"number": 1, "pull_request": map[string]any{}}, "comment": map[string]any{"id": 101, "body": "/eli fix this PR", "user": map[string]any{"id": 42, "login": "octo", "type": "User"}}, "sender": map[string]any{"id": 42}})
	if err := h.c.enqueueRepairWebhook(h.ctx, inst, "issue_comment", body); err != nil {
		t.Fatal(err)
	}
	rows, _ := h.st.ListEliRepairs(h.ctx)
	if len(rows) != 0 {
		t.Fatal("removed collaborator charged the budget")
	}
	fake.noWrite = false
	user.Disabled = true
	h.st.UpdateUser(h.ctx, user)
	if err := h.c.enqueueRepairWebhook(h.ctx, inst, "issue_comment", body); err != nil {
		t.Fatal(err)
	}
	rows, _ = h.st.ListEliRepairs(h.ctx)
	if len(rows) != 0 {
		t.Fatal("disabled user charged the budget")
	}
	user.Disabled = false
	h.st.UpdateUser(h.ctx, user)
	if err := h.c.enqueueRepairWebhook(h.ctx, inst, "issue_comment", body); err != nil {
		t.Fatal(err)
	}
	rows, _ = h.st.ListEliRepairs(h.ctx)
	if len(rows) != 1 {
		t.Fatal("current collaborator was not admitted")
	}
}

// A thread of Eli's updates is read top to bottom by someone deciding whether to
// trust the push, so every stage a repair can be in needs a heading of its own
// and none may fall back to the bare name.
func TestEveryRepairStageHasItsOwnHeading(t *testing.T) {
	seen := map[string]string{}
	for _, state := range []string{"working", "checking", "succeeded", "checks_failed", "failed", "superseded"} {
		h := repairHeading(state)
		if h == repairHeading("") {
			t.Errorf("state %q has no heading of its own", state)
		}
		if other, dup := seen[h]; dup {
			t.Errorf("states %q and %q share the heading %q", state, other, h)
		}
		seen[h] = state
	}
}

func TestTheTriggeringCommentIsReadBackFromTheDedupKey(t *testing.T) {
	for key, want := range map[string]int64{"comment:acme/repo:77": 77, "comment:other/repo:77": 0, "ui:acme/repo:1:abc:u": 0, "comment:acme/repo:x": 0, "comment:acme/repo:-3": 0, "": 0} {
		if got := triggerCommentID(&store.EliRepair{Repo: "acme/repo", DedupKey: key}); got != want {
			t.Errorf("%q gave %d, want %d", key, got, want)
		}
	}
}
