package github

import (
	"fmt"
	"net/http"
	"strconv"
)

// What the fake needs to be able to say for the Kennel Club settings checks: a
// repository's Actions settings, the status checks a branch requires under
// classic protection, and the ones its rules require.
//
// Reading any of the Actions settings, or classic protection, needs the
// Administration read permission, and without it the fake answers 403 as GitHub
// does. The rules need only Metadata, which every App holds.

// FakeActionsSettings is what the fake answers for a repository's Actions
// settings. A repository it has not been told about has the read-only default
// token and the policy GitHub applies when nobody has changed it.
type FakeActionsSettings struct {
	// DefaultTokenWrite makes the default workflow token read and write.
	DefaultTokenWrite bool
	// ForkApproval is the approval policy word for a public repository; empty
	// is "first_time_contributors".
	ForkApproval string
	// The three answers for a private repository's fork pull requests.
	PrivateForkRuns, PrivateForkSecrets, PrivateForkWriteToken bool
}

// FakeRequiredCheck is a status check a branch requires. AppID is the app that
// must post it, and zero means any app.
type FakeRequiredCheck struct {
	Context string
	AppID   int64
}

type fakeKennelSettings struct {
	actions *FakeActionsSettings
	// classic maps a branch to its classic protection; absent is "Branch not
	// protected".
	classic map[string]*fakeClassicProtection
	// rules maps a branch to the required-check rules that apply to it, one entry
	// per rule.
	rules map[string][][]FakeRequiredCheck
}

type fakeClassicProtection struct {
	checks []FakeRequiredCheck
	// legacy answers only the deprecated list of contexts, which names no app.
	legacy bool
}

func (f *FakeGitHub) kennelSettingsLocked(repo string) *fakeKennelSettings {
	r := f.repoLocked(repo)
	if r.kennelSettings == nil {
		r.kennelSettings = &fakeKennelSettings{
			classic: map[string]*fakeClassicProtection{},
			rules:   map[string][][]FakeRequiredCheck{},
		}
	}
	return r.kennelSettings
}

// SetActionsSettings replaces what the fake answers for a repository's Actions
// settings.
func (f *FakeGitHub) SetActionsSettings(repo string, s FakeActionsSettings) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addRepoLocked(repo)
	f.kennelSettingsLocked(repo).actions = &s
}

// SetBranchProtection gives a branch classic protection requiring these checks.
// With none, the branch is protected and requires nothing.
func (f *FakeGitHub) SetBranchProtection(repo, branch string, checks ...FakeRequiredCheck) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addRepoLocked(repo)
	f.kennelSettingsLocked(repo).classic[branch] = &fakeClassicProtection{checks: checks}
}

// SetLegacyBranchProtection is SetBranchProtection answering only the deprecated
// list of contexts, which names no app, as an older protection rule does.
func (f *FakeGitHub) SetLegacyBranchProtection(repo, branch string, contexts ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addRepoLocked(repo)
	var checks []FakeRequiredCheck
	for _, c := range contexts {
		checks = append(checks, FakeRequiredCheck{Context: c})
	}
	f.kennelSettingsLocked(repo).classic[branch] = &fakeClassicProtection{checks: checks, legacy: true}
}

// AddBranchRule adds one rule, requiring these checks, to the rules that apply
// to a branch. Call it again for another rule.
func (f *FakeGitHub) AddBranchRule(repo, branch string, checks ...FakeRequiredCheck) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addRepoLocked(repo)
	ks := f.kennelSettingsLocked(repo)
	ks.rules[branch] = append(ks.rules[branch], checks)
}

// canReadAdministrationLocked is the Administration read permission, which write
// includes.
func (f *FakeGitHub) canReadAdministrationLocked() bool {
	level := f.permissions["administration"]
	return level == "read" || level == "write"
}

func (f *FakeGitHub) registerKennelSettingsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/permissions/workflow", f.getActionsWorkflowPermissions)
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/permissions/fork-pr-contributor-approval", f.getForkApprovalPolicy)
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/permissions/fork-pr-workflows-private-repos", f.getPrivateForkSettings)
	mux.HandleFunc("GET /repos/{owner}/{repo}/branches/{branch}/protection", f.getBranchProtection)
	mux.HandleFunc("GET /repos/{owner}/{repo}/rules/branches/{branch}", f.getBranchRules)
}

// actionsSettingsLocked is what the fake holds for a repository's Actions
// settings, or the defaults. It answers false, and has written the 403, when the
// App may not read Administration.
func (f *FakeGitHub) actionsSettingsLocked(w http.ResponseWriter, repo string) (FakeActionsSettings, bool) {
	if !f.canReadAdministrationLocked() {
		writeError(w, http.StatusForbidden, "Resource not accessible by integration")
		return FakeActionsSettings{}, false
	}
	if s := f.kennelSettingsLocked(repo).actions; s != nil {
		return *s, true
	}
	return FakeActionsSettings{}, true
}

func (f *FakeGitHub) getActionsWorkflowPermissions(w http.ResponseWriter, r *http.Request) {
	full, _ := target(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.actionsSettingsLocked(w, full)
	if !ok {
		return
	}
	level := "read"
	if s.DefaultTokenWrite {
		level = "write"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"default_workflow_permissions":     level,
		"can_approve_pull_request_reviews": false,
	})
}

func (f *FakeGitHub) getForkApprovalPolicy(w http.ResponseWriter, r *http.Request) {
	full, _ := target(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.actionsSettingsLocked(w, full)
	if !ok {
		return
	}
	policy := s.ForkApproval
	if policy == "" {
		policy = "first_time_contributors"
	}
	writeJSON(w, http.StatusOK, map[string]any{"approval_policy": policy})
}

func (f *FakeGitHub) getPrivateForkSettings(w http.ResponseWriter, r *http.Request) {
	full, _ := target(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.actionsSettingsLocked(w, full)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run_workflows_from_fork_pull_requests":  s.PrivateForkRuns,
		"send_write_tokens_to_workflows":         s.PrivateForkWriteToken,
		"send_secrets_and_variables":             s.PrivateForkSecrets,
		"require_approval_for_fork_pr_workflows": true,
	})
}

func (f *FakeGitHub) getBranchProtection(w http.ResponseWriter, r *http.Request) {
	full, _ := target(r)
	branch := r.PathValue("branch")
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.canReadAdministrationLocked() {
		writeError(w, http.StatusForbidden, "Resource not accessible by integration")
		return
	}
	p, ok := f.kennelSettingsLocked(full).classic[branch]
	if !ok {
		// Not a failure: this is how GitHub says a branch has no classic
		// protection, and the exact message is what go-github looks for.
		writeError(w, http.StatusNotFound, "Branch not protected")
		return
	}
	rsc := map[string]any{"strict": false}
	var contexts []string
	var checks []map[string]any
	for _, c := range p.checks {
		contexts = append(contexts, c.Context)
		var app any
		if c.AppID != 0 {
			app = c.AppID
		}
		checks = append(checks, map[string]any{"context": c.Context, "app_id": app})
	}
	if len(p.checks) > 0 {
		rsc["contexts"] = contexts
		if !p.legacy {
			rsc["checks"] = checks
		}
	}
	body := map[string]any{"url": "https://example.invalid/protection"}
	if len(p.checks) > 0 {
		body["required_status_checks"] = rsc
	}
	writeJSON(w, http.StatusOK, body)
}

func (f *FakeGitHub) getBranchRules(w http.ResponseWriter, r *http.Request) {
	full, _ := target(r)
	branch := r.PathValue("branch")
	f.mu.Lock()
	defer f.mu.Unlock()
	rules := f.kennelSettingsLocked(full).rules[branch]

	// GitHub pages this list, a hundred at most to a page.
	per, page := 30, 1
	if n, err := strconv.Atoi(r.URL.Query().Get("per_page")); err == nil && n > 0 && n <= 100 {
		per = n
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && n > 0 {
		page = n
	}
	start := (page - 1) * per
	if start > len(rules) {
		start = len(rules)
	}
	end := start + per
	if end > len(rules) {
		end = len(rules)
	}
	out := make([]map[string]any, 0, end-start)
	for i, checks := range rules[start:end] {
		var rsc []map[string]any
		for _, c := range checks {
			var app any
			if c.AppID != 0 {
				app = c.AppID
			}
			rsc = append(rsc, map[string]any{"context": c.Context, "integration_id": app})
		}
		out = append(out, map[string]any{
			"type":                "required_status_checks",
			"ruleset_source_type": "Repository",
			"ruleset_source":      full,
			"ruleset_id":          start + i + 1,
			"parameters": map[string]any{
				"required_status_checks":               rsc,
				"strict_required_status_checks_policy": false,
			},
		})
	}
	if end < len(rules) {
		w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?page=%d&per_page=%d>; rel="next"`, r.Host, r.URL.Path, page+1, per))
	}
	writeJSON(w, http.StatusOK, out)
}
