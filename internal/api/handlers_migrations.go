package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/migrate"
	"github.com/eyupio/zoomies/internal/store"
)

// The migration endpoints: what moving a repository's workflows onto this
// fleet would change, and then doing it.
//
// They are two halves of one thing, deliberately shaped like /pools/validate
// and /pools: a plan is computed and shown, and only a second, explicit call
// writes anything. Nothing about a migration should ever be a surprise -- it
// opens pull requests in other people's repositories -- so the plan endpoint
// creates nothing at all, and the apply endpoint takes the plan's own inputs
// back rather than a token that could stand for a plan the operator never saw.
//
// Neither endpoint stores anything. A migration is a thing an operator does
// once per repository; a table of half-finished migrations would be a schema to
// maintain, a thing to garbage-collect, and another place for a stale plan to
// hide.

// migrationLimits bound what one request will do, because both endpoints fan
// out into GitHub's API on a quota the scheduler shares.
const (
	// maxPlanRepos is the most repositories one plan will read workflows from.
	// The wizard scans in pages; an operator with more repositories than this
	// migrates them in batches, which is how anyone would want to review them
	// anyway.
	maxPlanRepos = 50
	// maxApplyRepos is the most pull requests one call will open. It is lower
	// than the plan's limit on purpose: fifty pull requests landing at once in
	// somebody's review queue is not a migration, it is an incident.
	maxApplyRepos = 25
	// planConcurrency is how many repositories are read at once. GitHub's
	// secondary rate limits punish bursts, and a scan is not urgent.
	planConcurrency = 4
)

// ---------------------------------------------------------------------------
// Requests
// ---------------------------------------------------------------------------

// migrationPlanRequest asks what a migration would change.
type migrationPlanRequest struct {
	InstallationID string `json:"installation_id"`
	// Repos limits the plan to these repositories. Empty means every
	// repository the installation can see, up to the limit.
	Repos []string `json:"repos"`
	// Mapping is hosted label -> the runs-on value that replaces it. Empty asks
	// the server to propose one from the pools that exist.
	Mapping map[string]string `json:"mapping"`
}

// migrationApplyRequest opens the pull requests a plan described.
type migrationApplyRequest struct {
	InstallationID string            `json:"installation_id"`
	Repos          []string          `json:"repos"`
	Mapping        map[string]string `json:"mapping"`
	// Title, Body and CommitMessage override the defaults. They are here
	// because the pull request lands in somebody else's repository, and an
	// organisation with a pull-request template or a commit convention should
	// not have to accept ours.
	Title         string `json:"title"`
	Body          string `json:"body"`
	CommitMessage string `json:"commit_message"`
}

// ---------------------------------------------------------------------------
// Responses
// ---------------------------------------------------------------------------

// migrationPoolOption is one pool the wizard can map a hosted label to.
type migrationPoolOption struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Labels []string `json:"labels"`
	// RunsOn is what a workflow writes to reach this pool: the value the
	// mapping should hold.
	RunsOn  string `json:"runs_on"`
	Enabled bool   `json:"enabled"`
}

// migrationPlanResponse is the whole review step.
type migrationPlanResponse struct {
	InstallationID string `json:"installation_id"`
	Target         string `json:"target"`
	// Repositories is every repository looked at, whether or not it changed.
	// A repository with nothing to do is information: it means its workflows
	// are already somewhere deliberate.
	Repositories []migrate.RepoPlan `json:"repositories"`
	// HostedLabels is every GitHub-hosted label found across them, which is
	// what the mapping step lists.
	HostedLabels []string `json:"hosted_labels"`
	// Mapping is what was applied -- the request's, or the proposal the server
	// made from the pools that exist.
	Mapping map[string]string `json:"mapping"`
	// Unmapped are the hosted labels no pool was proposed for. They are the
	// operator's decision, and the reason a plan can be empty.
	Unmapped []string `json:"unmapped"`
	// Pools is what the mapping step chooses between.
	Pools  []migrationPoolOption `json:"pools"`
	Counts migrate.Counts        `json:"counts"`
	// Truncated says the installation has more repositories than one plan
	// reads, so the operator knows this is a page rather than the whole
	// organisation.
	Truncated bool `json:"truncated"`
	// MissingPermissions is what the App still needs before the apply step can
	// work. It is reported here, in the step before, because discovering it
	// halfway through opening pull requests leaves half of them open.
	MissingPermissions []string `json:"missing_permissions"`
	// PermissionHint is the sentence that fixes MissingPermissions.
	PermissionHint string `json:"permission_hint,omitempty"`
	// SettingsURL is where to go and fix it.
	SettingsURL string `json:"settings_url,omitempty"`
}

// migrationResult is what happened to one repository.
type migrationResult struct {
	Repo string `json:"repo"`
	// Status is "opened", "skipped" or "failed".
	Status string `json:"status"`
	// PullRequestURL and PullRequestNumber are set when Status is "opened".
	PullRequestURL    string `json:"pull_request_url,omitempty"`
	PullRequestNumber int    `json:"pull_request_number,omitempty"`
	Branch            string `json:"branch,omitempty"`
	// Workflows is how many files the pull request changed.
	Workflows int `json:"workflows"`
	// Jobs is how many runs-on lines it rewrote.
	Jobs int `json:"jobs"`
	// Reason explains a skip or a failure.
	Reason string `json:"reason,omitempty"`
}

type migrationApplyResponse struct {
	Results []migrationResult `json:"results"`
	Opened  int               `json:"opened"`
	Skipped int               `json:"skipped"`
	Failed  int               `json:"failed"`
}

// ---------------------------------------------------------------------------
// Plan
// ---------------------------------------------------------------------------

// handleMigrationPlan answers POST /api/v1/migrations/plan. It creates nothing.
func (s *Server) handleMigrationPlan(w http.ResponseWriter, r *http.Request) {
	var req migrationPlanRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()

	inst, client, ok := s.migrationClient(w, r, req.InstallationID)
	if !ok {
		return
	}

	pools, err := s.migrationPools(ctx, inst.ID)
	if err != nil {
		s.internal(w, r, "listing the pools this installation could migrate to", err)
		return
	}
	if len(pools) == 0 {
		unprocessable(w, "there is nowhere to migrate to: this installation has no enabled pool. Create one on the Pools page first, then come back.",
			[]fieldError{{"installation_id", "no enabled pool belongs to this installation"}})
		return
	}

	repos, truncated, err := s.migrationRepos(ctx, client, req.Repos, maxPlanRepos)
	if err != nil {
		s.githubFail(w, r, "listing the repositories this installation can see", err)
		return
	}

	// Two passes. The first reads every repository and works out which hosted
	// labels are in play; the second applies a mapping to them. They are
	// separate because the mapping the server proposes depends on what the
	// first pass found, and reading each repository twice would double the
	// cost of the most expensive call Zoomies makes.
	sources := s.readWorkflows(ctx, client, repos)

	hosted := hostedLabelsAcross(sources)
	mapping := normalizeMapping(req.Mapping)
	if len(mapping) == 0 {
		mapping = migrate.Suggest(pools, hosted)
	}

	plans := make([]migrate.RepoPlan, 0, len(sources))
	for _, src := range sources {
		if src.err != "" {
			plans = append(plans, migrate.RepoPlan{Repo: src.repo.FullName, DefaultBranch: src.repo.DefaultBranch, Error: src.err})
			continue
		}
		plans = append(plans, migrate.PlanRepo(src.repo.FullName, src.repo.DefaultBranch, src.workflows, migrate.Mapping{Labels: mapping}))
	}

	var unmapped []string
	for _, l := range hosted {
		if _, ok := mapping[l]; !ok {
			unmapped = append(unmapped, l)
		}
	}

	out := migrationPlanResponse{
		InstallationID:     inst.ID,
		Target:             inst.Target,
		Repositories:       plans,
		HostedLabels:       hosted,
		Mapping:            mapping,
		Unmapped:           emptySlice(unmapped),
		Pools:              poolOptions(pools),
		Counts:             migrate.Count(plans),
		Truncated:          truncated,
		MissingPermissions: []string{},
	}
	// Asking GitHub what the App may do costs one call and turns "403 halfway
	// through" into a sentence in the step before.
	if info, err := client.Probe(ctx); err == nil {
		if missing := info.MissingForMigration(); len(missing) > 0 {
			out.MissingPermissions = missing
			out.PermissionHint = github.MigrationPermissionHint
			settingsOrg := ""
			if inst.TargetType == store.TargetOrg {
				settingsOrg = inst.Target
			}
			out.SettingsURL = github.SettingsURL(inst.APIBaseURL, info.Slug, settingsOrg)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// Apply
// ---------------------------------------------------------------------------

// handleMigrationApply answers POST /api/v1/migrations/pull-requests.
//
// It re-reads and re-plans rather than trusting a plan the browser sends back.
// The workflows may have changed since the review step, and the alternative --
// committing content the browser supplied -- would make this endpoint a way to
// write arbitrary files into any repository the App can reach.
func (s *Server) handleMigrationApply(w http.ResponseWriter, r *http.Request) {
	var req migrationApplyRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()

	inst, client, ok := s.migrationClient(w, r, req.InstallationID)
	if !ok {
		return
	}
	if len(req.Repos) == 0 {
		unprocessable(w, "name the repositories to migrate; this endpoint will not touch every repository an App can see by default",
			[]fieldError{{"repos", "at least one repository is required"}})
		return
	}
	if len(req.Repos) > maxApplyRepos {
		unprocessable(w, fmt.Sprintf("that is %d repositories; %d is the most one call will open pull requests on, so that a mistake is %d pull requests to close rather than an organisation-wide one",
			len(req.Repos), maxApplyRepos, maxApplyRepos),
			[]fieldError{{"repos", fmt.Sprintf("at most %d repositories per call", maxApplyRepos)}})
		return
	}
	mapping := normalizeMapping(req.Mapping)
	if len(mapping) == 0 {
		unprocessable(w, "nothing would change: no hosted label is mapped to a pool",
			[]fieldError{{"mapping", "map at least one label, such as ubuntu-latest, to a pool"}})
		return
	}

	repos, _, err := s.migrationRepos(ctx, client, req.Repos, maxApplyRepos)
	if err != nil {
		s.githubFail(w, r, "reading the repositories to migrate", err)
		return
	}

	branch := github.BranchName(s.ctrl.Now())
	title := firstNonEmpty(strings.TrimSpace(req.Title), "Run CI on Zoomies runners")
	commit := firstNonEmpty(strings.TrimSpace(req.CommitMessage), title)

	out := migrationApplyResponse{Results: make([]migrationResult, 0, len(repos))}
	// One repository at a time. Opening pull requests is a write against a
	// shared quota, and a burst of them is exactly what GitHub's secondary
	// rate limits exist to stop.
	for _, repo := range repos {
		if err := ctx.Err(); err != nil {
			break
		}
		out.Results = append(out.Results, s.migrateRepo(ctx, client, repo, mapping, branch, title, req.Body, commit))
	}
	for _, res := range out.Results {
		switch res.Status {
		case "opened":
			out.Opened++
		case "failed":
			out.Failed++
		default:
			out.Skipped++
		}
	}

	s.auth.Auditor().Act(ctx, Identity(ctx), "migration.pull_requests", "installation", inst.ID, map[string]any{
		"repos": len(out.Results), "opened": out.Opened, "failed": out.Failed, "branch": branch,
	})
	writeJSON(w, http.StatusOK, out)
}

// migrateRepo plans and opens the pull request for one repository.
func (s *Server) migrateRepo(ctx context.Context, client github.Client, repo github.Repository,
	mapping map[string]string, branch, title, body, commit string) migrationResult {

	res := migrationResult{Repo: repo.FullName, Status: "skipped"}
	if repo.Archived {
		res.Reason = "the repository is archived, so it accepts no pull requests"
		return res
	}

	workflows, err := client.ListWorkflows(ctx, repo.FullName)
	if err != nil {
		if errors.Is(err, github.ErrNoWorkflows) {
			res.Reason = "the repository has no .github/workflows"
			return res
		}
		res.Status, res.Reason = "failed", err.Error()
		return res
	}

	plan := migrate.PlanRepo(repo.FullName, repo.DefaultBranch, asMigrateWorkflows(workflows), migrate.Mapping{Labels: mapping})
	var files []github.FileChange
	for _, wf := range plan.Workflows {
		if !wf.Changed() {
			continue
		}
		files = append(files, github.FileChange{Path: wf.Path, Content: wf.After, SHA: wf.SHA})
		res.Workflows++
		res.Jobs += len(wf.Rewrites)
	}
	if len(files) == 0 {
		res.Reason = "no job in this repository is on a mapped GitHub-hosted label"
		return res
	}

	pr, err := client.OpenPullRequest(ctx, github.PullRequestRequest{
		Repo:          repo.FullName,
		Base:          repo.DefaultBranch,
		Head:          branch,
		Title:         title,
		Body:          firstNonEmpty(body, pullRequestBody(plan)),
		CommitMessage: commit,
		Files:         files,
	})
	if err != nil {
		res.Status, res.Reason = "failed", err.Error()
		return res
	}
	res.Status = "opened"
	res.PullRequestURL, res.PullRequestNumber, res.Branch = pr.HTMLURL, pr.Number, pr.Branch
	return res
}

// pullRequestBody is what somebody reviewing the change reads first.
//
// It says what moved and what did not, because the skips are the part a
// reviewer has to act on: a job left on `${{ matrix.os }}` is still running on
// GitHub's runners after this merges, and nobody should have to work that out
// from the diff.
func pullRequestBody(plan migrate.RepoPlan) string {
	var b strings.Builder
	b.WriteString("Moves this repository's CI onto self-hosted runners managed by [Zoomies](https://zoomies.sh).\n\n")

	changed := 0
	for _, wf := range plan.Workflows {
		if !wf.Changed() {
			continue
		}
		changed++
		fmt.Fprintf(&b, "### `%s`\n\n", wf.Path)
		for _, rw := range wf.Rewrites {
			if rw.Job != "" {
				fmt.Fprintf(&b, "- `%s`: `%s` → `%s`\n", rw.Job, rw.From, rw.To)
			} else {
				fmt.Fprintf(&b, "- line %d: `%s` → `%s`\n", rw.Line, rw.From, rw.To)
			}
		}
		b.WriteString("\n")
	}

	var skips []migrate.Skip
	for _, wf := range plan.Workflows {
		skips = append(skips, wf.Skips...)
	}
	if len(skips) > 0 {
		b.WriteString("### Left alone\n\n")
		for _, sk := range skips {
			where := sk.Job
			if where == "" {
				where = fmt.Sprintf("line %d", sk.Line)
			}
			fmt.Fprintf(&b, "- `%s` (`%s`): %s\n", where, sk.Value, sk.Reason)
		}
		b.WriteString("\nThose jobs still run on GitHub's runners.\n")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Shared plumbing
// ---------------------------------------------------------------------------

// migrationClient resolves the installation and its GitHub client, answering
// the request itself when either is unusable.
func (s *Server) migrationClient(w http.ResponseWriter, r *http.Request, id string) (*store.Installation, github.Client, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		unprocessable(w, "name the installation to migrate: a migration reads and writes repositories through one GitHub App",
			[]fieldError{{"installation_id", "an installation is required"}})
		return nil, nil, false
	}
	inst, err := s.ctrl.Store().GetInstallation(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the installation", err)
		return nil, nil, false
	}
	client, err := s.ctrl.ClientFor(r.Context(), id)
	if err != nil {
		s.githubFail(w, r, "authenticating as the GitHub App", err)
		return nil, nil, false
	}
	return inst, client, true
}

// migrationPools returns the enabled pools belonging to an installation, which
// are the only places a job could actually be sent.
func (s *Server) migrationPools(ctx context.Context, installationID string) ([]*store.Pool, error) {
	all, err := s.ctrl.Store().ListPools(ctx)
	if err != nil {
		return nil, err
	}
	var out []*store.Pool
	for _, p := range all {
		if p.InstallationID == installationID && p.Enabled {
			out = append(out, p)
		}
	}
	return out, nil
}

func poolOptions(pools []*store.Pool) []migrationPoolOption {
	out := make([]migrationPoolOption, 0, len(pools))
	for _, p := range pools {
		out = append(out, migrationPoolOption{
			ID:      p.ID,
			Name:    p.Name,
			Labels:  emptySlice(p.Labels),
			RunsOn:  store.RunsOn(p.Labels),
			Enabled: p.Enabled,
		})
	}
	return out
}

// migrationRepos resolves the repositories a request names, or lists what the
// installation can see when it names none.
func (s *Server) migrationRepos(ctx context.Context, client github.Client, named []string, limit int) ([]github.Repository, bool, error) {
	all, err := client.ListRepositories(ctx, 0)
	if err != nil {
		return nil, false, err
	}
	byName := make(map[string]github.Repository, len(all))
	for _, r := range all {
		byName[strings.ToLower(r.FullName)] = r
	}

	if len(named) > 0 {
		out := make([]github.Repository, 0, len(named))
		for _, want := range named {
			want = strings.TrimSpace(want)
			if want == "" {
				continue
			}
			repo, ok := byName[strings.ToLower(want)]
			if !ok {
				// A repository the App cannot see is named, not silently
				// dropped: the operator picked it, and it disappearing from the
				// results with no explanation is worse than a failure.
				return nil, false, fmt.Errorf("%w: this installation cannot see %s; check the App is installed on it", github.ErrNotFound, want)
			}
			out = append(out, repo)
		}
		return out, false, nil
	}

	sort.Slice(all, func(i, j int) bool { return all[i].FullName < all[j].FullName })
	if len(all) > limit {
		return all[:limit], true, nil
	}
	return all, false, nil
}

// workflowSource is one repository's workflows, or why they could not be read.
type workflowSource struct {
	repo      github.Repository
	workflows []migrate.Workflow
	err       string
}

// readWorkflows reads every repository's workflows, a few at a time.
//
// One repository failing is recorded against that repository rather than
// failing the scan: in an organisation of any size there is always one archived
// repository, one the App was removed from, and one with a .github/workflows
// that is a file rather than a directory.
func (s *Server) readWorkflows(ctx context.Context, client github.Client, repos []github.Repository) []workflowSource {
	out := make([]workflowSource, len(repos))
	sem := make(chan struct{}, planConcurrency)
	var wg sync.WaitGroup

	for i, repo := range repos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			out[i] = workflowSource{repo: repo}
			if ctx.Err() != nil {
				out[i].err = "the request was cancelled before this repository was read"
				return
			}
			files, err := client.ListWorkflows(ctx, repo.FullName)
			switch {
			case errors.Is(err, github.ErrNoWorkflows):
				// Not an error: most repositories have no workflows.
			case err != nil:
				out[i].err = err.Error()
			default:
				out[i].workflows = asMigrateWorkflows(files)
			}
		}()
	}
	wg.Wait()
	return out
}

func asMigrateWorkflows(files []github.WorkflowFile) []migrate.Workflow {
	out := make([]migrate.Workflow, 0, len(files))
	for _, f := range files {
		if !migrate.IsWorkflowPath(f.Path) {
			continue
		}
		out = append(out, migrate.Workflow{Path: f.Path, SHA: f.SHA, Content: f.Content})
	}
	return out
}

// hostedLabelsAcross collects every GitHub-hosted label the scan found, sorted
// so the mapping step is in the same order every time it is opened.
func hostedLabelsAcross(sources []workflowSource) []string {
	seen := map[string]bool{}
	var out []string
	for _, src := range sources {
		for _, wf := range src.workflows {
			for _, l := range migrate.HostedLabelsIn(wf.Content) {
				if !seen[l] {
					seen[l] = true
					out = append(out, l)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// normalizeMapping lowercases the keys and drops the entries the browser sends
// for a label the operator chose not to map, which arrive as empty strings.
func normalizeMapping(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		out[k] = v
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
