package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	gh "github.com/google/go-github/v88/github"

	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// defaultHTTPTimeout bounds a single GitHub call. Without it a hung TLS
// handshake would wedge the scheduler for as long as the kernel keeps the
// socket, which in practice means forever.
const defaultHTTPTimeout = 30 * time.Second

// Bounds on one fallback poll. ListQueuedJobs runs on a timer against an
// installation quota of 5000 requests/hour shared with every other call
// Zoomies makes, so a single sweep of a large organisation must not be allowed
// to spend it. Webhooks are the primary path; the poller only has to notice
// work that a missed delivery left behind.
const (
	// maxPollRuns caps how many workflow runs one poll inspects across all
	// repositories, since each run costs a second call to list its jobs.
	maxPollRuns = 50
	// maxPollRepos caps how many of the installation's repositories one poll
	// walks. Organisations with more repositories than this rely on webhooks.
	maxPollRepos = 100
	// pollPerPage is the largest page GitHub serves, which keeps the number of
	// round trips down.
	pollPerPage = 100
)

// AppFactory builds GitHub clients that authenticate as a GitHub App
// installation.
//
// One factory is shared by the whole process so that every installation reuses
// the same TCP connection pool.
type AppFactory struct {
	http *http.Client
}

// NewAppFactory returns a factory that dials GitHub with httpClient. Passing
// nil uses a client with a request timeout, which is what production wants;
// tests pass their own to point at a fake.
func NewAppFactory(httpClient *http.Client) *AppFactory {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &AppFactory{http: httpClient}
}

// For returns a Client scoped to one installation, authenticated with the
// App's private key.
//
// privateKeyPEM is passed in rather than read from the installation because
// the store keeps it sealed; unsealing is the caller's job and the plaintext
// key never lives longer than this call needs it to.
func (f *AppFactory) For(ctx context.Context, inst *store.Installation, privateKeyPEM []byte) (Client, error) {
	if inst == nil {
		return nil, errors.New("github: no installation given")
	}
	if inst.AppID == 0 || inst.InstallationID == 0 {
		return nil, fmt.Errorf("github: installation %s has no app_id or installation_id: "+
			"re-run the GitHub App setup so Zoomies learns them", inst.ID)
	}
	if len(privateKeyPEM) == 0 {
		return nil, fmt.Errorf("github: installation %s has no private key: "+
			"upload the App's .pem file again on the Installations page", inst.ID)
	}
	if !inst.TargetType.Valid() {
		return nil, fmt.Errorf("github: installation %s has target type %q, want %q or %q",
			inst.ID, inst.TargetType, store.TargetOrg, store.TargetRepo)
	}
	owner, repo, _ := SplitTarget(inst.Target)
	if owner == "" || (inst.TargetType == store.TargetRepo && repo == "") {
		return nil, fmt.Errorf("github: installation %s targets %q, which is not %s: "+
			"use \"owner\" for an organisation or \"owner/name\" for a repository",
			inst.ID, inst.Target, expectedTargetShape(inst.TargetType))
	}

	apiBase, err := NormalizeAPIBaseURL(inst.APIBaseURL)
	if err != nil {
		return nil, fmt.Errorf("github: installation %s has an unusable api_base_url %q: %w",
			inst.ID, inst.APIBaseURL, err)
	}
	uploadBase := apiBase
	if inst.UploadBaseURL != "" {
		if uploadBase, err = NormalizeAPIBaseURL(inst.UploadBaseURL); err != nil {
			return nil, fmt.Errorf("github: installation %s has an unusable upload_base_url %q: %w",
				inst.ID, inst.UploadBaseURL, err)
		}
	}

	base := f.http.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	appTransport, err := ghinstallation.NewAppsTransport(base, inst.AppID, privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("github: installation %s: the stored private key is not a "+
			"PEM-encoded RSA key: generate a new one under the App's settings and upload it again: %w",
			inst.ID, err)
	}
	// ghinstallation builds the token URL by hand, so it wants the API root
	// without the trailing slash that go-github requires.
	appTransport.BaseURL = strings.TrimSuffix(apiBase, "/")
	instTransport := ghinstallation.NewFromAppsTransport(appTransport, inst.InstallationID)
	instTransport.BaseURL = appTransport.BaseURL

	asApp, err := newGitHubClient(&http.Client{Transport: appTransport, Timeout: f.http.Timeout}, apiBase, uploadBase)
	if err != nil {
		return nil, err
	}
	asInstallation, err := newGitHubClient(&http.Client{Transport: instTransport, Timeout: f.http.Timeout}, apiBase, uploadBase)
	if err != nil {
		return nil, err
	}
	return newAppClient(asApp, asInstallation, inst.Target, inst.TargetType, inst.InstallationID, WebURLForAPI(apiBase)), nil
}

func expectedTargetShape(kind store.TargetType) string {
	if kind == store.TargetRepo {
		return "owner/name"
	}
	return "an organisation login"
}

// newGitHubClient points go-github at apiBaseURL. GitHub Enterprise Server and
// github.com differ only here, which is the whole reason Client is an
// interface.
func newGitHubClient(hc *http.Client, apiBaseURL, uploadBaseURL string) (*gh.Client, error) {
	c, err := gh.NewClient(
		gh.WithHTTPClient(hc),
		gh.WithURLs(&apiBaseURL, &uploadBaseURL),
		gh.WithUserAgent(version.UserAgent()),
	)
	if err != nil {
		return nil, fmt.Errorf("github: cannot use api base url %q: %w", apiBaseURL, err)
	}
	return c, nil
}

// appClient is the Client implementation backed by the real GitHub API.
//
// It holds two go-github clients because GitHub splits the two identities an
// App has: /app and /app/installations/{id} are signed with the App's JWT,
// while everything that touches runners uses a short-lived installation token.
type appClient struct {
	asApp          *gh.Client
	asInstallation *gh.Client
	installationID int64

	target string
	kind   store.TargetType
	owner  string
	repo   string
	webURL string
}

func newAppClient(asApp, asInstallation *gh.Client, target string, kind store.TargetType, installationID int64, webBase string) *appClient {
	owner, repo, _ := SplitTarget(target)
	return &appClient{
		asApp:          asApp,
		asInstallation: asInstallation,
		installationID: installationID,
		target:         target,
		kind:           kind,
		owner:          owner,
		repo:           repo,
		webURL:         strings.TrimRight(webBase, "/") + "/" + target,
	}
}

// Target returns the org or repo this client acts on.
func (c *appClient) Target() (string, store.TargetType) { return c.target, c.kind }

// WebURL returns the browser URL for the target.
func (c *appClient) WebURL() string { return c.webURL }

// isOrg reports whether the org-scoped half of each Actions endpoint pair
// applies.
func (c *appClient) isOrg() bool { return c.kind == store.TargetOrg }

// Probe verifies the credentials end to end: the private key signs a JWT, the
// App exists, the installation exists, and the installation token it mints can
// actually read runners. The last step is the one that catches the common
// failure -- an App that was created without the runner permission -- which
// no amount of reading /app would reveal.
func (c *appClient) Probe(ctx context.Context) (*AppInfo, error) {
	app, resp, err := c.asApp.Apps.Get(ctx, "")
	if err != nil {
		return nil, c.fail("verify app credentials", resp, err)
	}
	info := &AppInfo{
		ID:          app.GetID(),
		Slug:        app.GetSlug(),
		Name:        app.GetName(),
		Owner:       app.GetOwner().GetLogin(),
		Permissions: permissionMap(app.Permissions),
		Events:      app.Events,
	}

	inst, resp, err := c.asApp.Apps.GetInstallation(ctx, c.installationID)
	if err != nil {
		return nil, c.fail(fmt.Sprintf("read installation %d", c.installationID), resp, err)
	}
	// What the installation was granted is what actually governs API calls; the
	// App's own defaults may be wider than the operator accepted.
	if p := permissionMap(inst.Permissions); len(p) > 0 {
		info.Permissions = p
	}
	if len(inst.Events) > 0 {
		info.Events = inst.Events
	}

	if _, err := c.ListRunners(ctx); err != nil {
		return nil, err
	}
	return info, nil
}

// MissingRequirements lists, in words an operator can act on, what this App is
// still missing for the given target type. It is empty when the App is
// correctly configured.
func (a *AppInfo) MissingRequirements(kind store.TargetType) []string {
	var out []string
	need := func(perm, level, label string) {
		got := a.Permissions[perm]
		if got == level || (level == "read" && got == "write") {
			return
		}
		out = append(out, fmt.Sprintf("permission %q (%s) is %s", label, level, describeLevel(got)))
	}
	if kind == store.TargetOrg {
		need("organization_self_hosted_runners", "write", "Self-hosted runners")
	} else {
		need("administration", "write", "Administration")
	}
	need("actions", "read", "Actions")
	need("metadata", "read", "Metadata")
	if !slices.Contains(a.Events, "workflow_job") {
		out = append(out, `the App is not subscribed to the "workflow_job" event, so Zoomies will only see queued jobs when the fallback poller runs`)
	}
	return out
}

func describeLevel(got string) string {
	if got == "" {
		return "not granted"
	}
	return "only " + got
}

// permissionMap flattens go-github's permission struct into the map AppInfo
// exposes, so that a permission GitHub adds later still reaches the UI.
func permissionMap(p *gh.InstallationPermissions) map[string]string {
	if p == nil {
		return nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}

// CreateJITConfig mints an ephemeral runner registration.
func (c *appClient) CreateJITConfig(ctx context.Context, req JITRequest) (*JITConfig, error) {
	if req.Name == "" {
		return nil, errors.New("github: create jit config: a runner name is required")
	}
	labels := store.NormalizeLabels(req.Labels)
	if len(labels) == 0 {
		return nil, fmt.Errorf("github: create jit config for %q: GitHub requires at least one "+
			"custom label: give the pool a label such as \"self-hosted\"", req.Name)
	}
	group := req.RunnerGroupID
	if group == 0 {
		group = 1 // "Default", the only group every target is guaranteed to have.
	}
	work := req.WorkFolder
	if work == "" {
		work = "_work"
	}
	body := &gh.GenerateJITConfigRequest{
		Name:          req.Name,
		RunnerGroupID: group,
		WorkFolder:    &work,
		Labels:        labels,
	}

	var (
		cfg  *gh.JITRunnerConfig
		resp *gh.Response
		err  error
	)
	if c.isOrg() {
		cfg, resp, err = c.asInstallation.Actions.GenerateOrgJITConfig(ctx, c.owner, body)
	} else {
		cfg, resp, err = c.asInstallation.Actions.GenerateRepoJITConfig(ctx, c.owner, c.repo, body)
	}
	if err != nil {
		return nil, c.fail("create jit config for "+req.Name, resp, err)
	}
	if cfg.GetEncodedJITConfig() == "" {
		return nil, fmt.Errorf("github: create jit config for %s: GitHub returned no configuration; "+
			"check that the runner group %d exists on %s", req.Name, group, c.target)
	}
	return &JITConfig{
		Encoded:  cfg.GetEncodedJITConfig(),
		RunnerID: cfg.GetRunner().GetID(),
		Name:     req.Name,
	}, nil
}

// CreateRegistrationToken mints a one-hour token for config.sh.
func (c *appClient) CreateRegistrationToken(ctx context.Context) (*RegistrationToken, error) {
	var (
		tok  *gh.RegistrationToken
		resp *gh.Response
		err  error
	)
	if c.isOrg() {
		tok, resp, err = c.asInstallation.Actions.CreateOrganizationRegistrationToken(ctx, c.owner)
	} else {
		tok, resp, err = c.asInstallation.Actions.CreateRegistrationToken(ctx, c.owner, c.repo)
	}
	if err != nil {
		return nil, c.fail("create registration token", resp, err)
	}
	return &RegistrationToken{Token: tok.GetToken(), ExpiresAt: tok.GetExpiresAt().Time}, nil
}

// CreateRemoveToken mints a token for deregistering a runner from the host.
func (c *appClient) CreateRemoveToken(ctx context.Context) (*RegistrationToken, error) {
	var (
		tok  *gh.RemoveToken
		resp *gh.Response
		err  error
	)
	if c.isOrg() {
		tok, resp, err = c.asInstallation.Actions.CreateOrganizationRemoveToken(ctx, c.owner)
	} else {
		tok, resp, err = c.asInstallation.Actions.CreateRemoveToken(ctx, c.owner, c.repo)
	}
	if err != nil {
		return nil, c.fail("create remove token", resp, err)
	}
	return &RegistrationToken{Token: tok.GetToken(), ExpiresAt: tok.GetExpiresAt().Time}, nil
}

// ListRunners returns every self-hosted runner registered on the target.
func (c *appClient) ListRunners(ctx context.Context) ([]Runner, error) {
	opts := &gh.ListRunnersOptions{ListOptions: gh.ListOptions{PerPage: pollPerPage}}
	var out []Runner
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var (
			page *gh.Runners
			resp *gh.Response
			err  error
		)
		if c.isOrg() {
			page, resp, err = c.asInstallation.Actions.ListOrganizationRunners(ctx, c.owner, opts)
		} else {
			page, resp, err = c.asInstallation.Actions.ListRunners(ctx, c.owner, c.repo, opts)
		}
		if err != nil {
			return nil, c.fail("list runners on "+c.target, resp, err)
		}
		for _, r := range page.Runners {
			out = append(out, convertRunner(r))
		}
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

func convertRunner(r *gh.Runner) Runner {
	out := Runner{
		ID:     r.GetID(),
		Name:   r.GetName(),
		OS:     r.GetOS(),
		Status: r.GetStatus(),
		Busy:   r.GetBusy(),
	}
	for _, l := range r.Labels {
		out.Labels = append(out.Labels, l.GetName())
		// GitHub does not report ephemerality directly on the runner, but a
		// JIT-configured runner always carries the "ephemeral" label GitHub
		// adds for it.
		if l.GetName() == "ephemeral" {
			out.Ephemeral = true
		}
	}
	return out
}

// DeleteRunner removes a registration. A runner GitHub has already forgotten
// counts as success: the desired end state has been reached.
func (c *appClient) DeleteRunner(ctx context.Context, id int64) error {
	var (
		resp *gh.Response
		err  error
	)
	if c.isOrg() {
		resp, err = c.asInstallation.Actions.RemoveOrganizationRunner(ctx, c.owner, id)
	} else {
		resp, err = c.asInstallation.Actions.RemoveRunner(ctx, c.owner, c.repo, id)
	}
	if err == nil {
		return nil
	}
	if e := classify(resp, err); errors.Is(e, ErrNotFound) {
		return nil
	}
	return c.fail(fmt.Sprintf("delete runner %d", id), resp, err)
}

// ListRunnerGroups returns the target's runner groups. Repositories do not
// have them -- groups are an organisation concept -- so a repo target reports
// none rather than failing, which lets the pool form show an empty picker.
func (c *appClient) ListRunnerGroups(ctx context.Context) ([]RunnerGroup, error) {
	if !c.isOrg() {
		return nil, nil
	}
	opts := &gh.ListOrgRunnerGroupOptions{ListOptions: gh.ListOptions{PerPage: pollPerPage}}
	var out []RunnerGroup
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, resp, err := c.asInstallation.Actions.ListOrganizationRunnerGroups(ctx, c.owner, opts)
		if err != nil {
			return nil, c.fail("list runner groups on "+c.target, resp, err)
		}
		for _, g := range page.RunnerGroups {
			out = append(out, RunnerGroup{ID: g.GetID(), Name: g.GetName()})
		}
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// ListQueuedJobs is the webhook fallback.
//
// GitHub has no endpoint that lists an organisation's queued jobs, so this
// walks the repositories the installation can see and asks each for its
// unfinished workflow runs, then for the jobs of those runs. The maxPollRepos
// and maxPollRuns bounds keep one sweep of a busy organisation from spending
// the installation's hourly quota, which would take the rest of the
// integration down with it.
func (c *appClient) ListQueuedJobs(ctx context.Context) ([]QueuedJob, error) {
	repos, err := c.pollRepos(ctx)
	if err != nil {
		return nil, err
	}
	budget := maxPollRuns
	seen := make(map[int64]bool)
	var out []QueuedJob

	for _, full := range repos {
		if budget <= 0 {
			break
		}
		owner, name, _ := SplitTarget(full)
		if name == "" {
			continue
		}
		// A run that is in_progress can still hold jobs nobody has picked up,
		// so both statuses matter.
		for _, status := range []string{"queued", "in_progress"} {
			if budget <= 0 {
				break
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			runs, resp, err := c.asInstallation.Actions.ListRepositoryWorkflowRuns(ctx, owner, name,
				&gh.ListWorkflowRunsOptions{
					Status:      status,
					ListOptions: gh.ListOptions{PerPage: min(budget, pollPerPage)},
				})
			if err != nil {
				// A repository with Actions disabled 404s. That is normal in a
				// large org and must not abort the whole sweep.
				if errors.Is(classify(resp, err), ErrNotFound) {
					break
				}
				return nil, c.fail("list workflow runs for "+full, resp, err)
			}
			for _, run := range runs.WorkflowRuns {
				if budget <= 0 {
					break
				}
				budget--
				jobs, err := c.queuedJobsForRun(ctx, owner, name, run)
				if err != nil {
					return nil, err
				}
				for _, j := range jobs {
					if seen[j.ID] {
						continue
					}
					seen[j.ID] = true
					out = append(out, j)
				}
			}
		}
	}
	return out, nil
}

// pollRepos returns the repositories one poll should look at.
func (c *appClient) pollRepos(ctx context.Context) ([]string, error) {
	if !c.isOrg() {
		return []string{c.target}, nil
	}
	opts := &gh.ListOptions{PerPage: pollPerPage}
	var out []string
	for len(out) < maxPollRepos {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, resp, err := c.asInstallation.Apps.ListRepos(ctx, opts)
		if err != nil {
			return nil, c.fail("list repositories visible to the installation", resp, err)
		}
		for _, r := range page.Repositories {
			if r.GetFullName() == "" {
				continue
			}
			out = append(out, r.GetFullName())
			if len(out) == maxPollRepos {
				break
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

func (c *appClient) queuedJobsForRun(ctx context.Context, owner, repo string, run *gh.WorkflowRun) ([]QueuedJob, error) {
	jobs, resp, err := c.asInstallation.Actions.ListWorkflowJobs(ctx, owner, repo, run.GetID(),
		&gh.ListWorkflowJobsOptions{
			Filter:      "latest",
			ListOptions: gh.ListOptions{PerPage: pollPerPage},
		})
	if err != nil {
		if errors.Is(classify(resp, err), ErrNotFound) {
			return nil, nil
		}
		return nil, c.fail(fmt.Sprintf("list jobs for run %d in %s/%s", run.GetID(), owner, repo), resp, err)
	}
	full := owner + "/" + repo
	var out []QueuedJob
	for _, j := range jobs.Jobs {
		if j.GetStatus() != string(store.JobQueued) {
			continue
		}
		q := QueuedJob{
			ID:           j.GetID(),
			RunID:        run.GetID(),
			Repo:         full,
			WorkflowName: j.GetWorkflowName(),
			JobName:      j.GetName(),
			Labels:       slices.Clone(j.Labels),
			QueuedAt:     j.GetCreatedAt().Time,
			HTMLURL:      j.GetHTMLURL(),
			RunnerName:   j.GetRunnerName(),
			Status:       j.GetStatus(),
			Conclusion:   j.GetConclusion(),
		}
		if q.WorkflowName == "" {
			q.WorkflowName = run.GetName()
		}
		if q.QueuedAt.IsZero() {
			q.QueuedAt = run.GetCreatedAt().Time
		}
		out = append(out, q)
	}
	return out, nil
}

// RateLimit reports the installation's remaining quota.
func (c *appClient) RateLimit(ctx context.Context) (*RateLimit, error) {
	limits, resp, err := c.asInstallation.RateLimit.Get(ctx)
	if err != nil {
		return nil, c.fail("read rate limit", resp, err)
	}
	core := limits.GetCore()
	if core == nil {
		return nil, errors.New("github: read rate limit: GitHub returned no core quota")
	}
	return &RateLimit{Limit: core.Limit, Remaining: core.Remaining, ResetAt: core.Reset.Time}, nil
}

// fail turns a go-github error into one an operator can act on: the operation
// that failed, the mapped sentinel, and -- for a 403 -- the permission that is
// almost certainly the cause.
func (c *appClient) fail(op string, resp *gh.Response, err error) error {
	e := classify(resp, err)
	if errors.Is(e, ErrForbidden) {
		return fmt.Errorf("github: %s: %w; %s", op, e, c.permissionHint())
	}
	return errorf(op, e)
}

// permissionHint names the App permissions a 403 on these endpoints means are
// missing. "403" on its own sends operators to the wrong settings page.
func (c *appClient) permissionHint() string {
	perm := `"Administration" (administration) read and write`
	if c.isOrg() {
		perm = `"Self-hosted runners" (organization_self_hosted_runners) read and write`
	}
	return fmt.Sprintf("check the App installation on %s: it needs %s, \"Actions\" (actions) read, "+
		"\"Metadata\" (metadata) read, and a subscription to the \"workflow_job\" webhook event; "+
		"changing permissions also needs the installation to accept them", c.target, perm)
}

// classify maps a go-github failure onto this package's sentinels so callers
// can branch on the kind of failure without knowing about HTTP.
func classify(resp *gh.Response, err error) error {
	if err == nil {
		return nil
	}
	// A cancelled context is the caller's own doing, not GitHub's.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	var rl *gh.RateLimitError
	if errors.As(err, &rl) {
		return fmt.Errorf("%w: quota exhausted until %s", ErrRateLimited, rl.Rate.Reset.Format(time.RFC3339))
	}
	var abuse *gh.AbuseRateLimitError
	if errors.As(err, &abuse) {
		if abuse.RetryAfter != nil {
			return fmt.Errorf("%w: secondary rate limit, retry after %s", ErrRateLimited, *abuse.RetryAfter)
		}
		return fmt.Errorf("%w: secondary rate limit", ErrRateLimited)
	}

	status, message := 0, ""
	var er *gh.ErrorResponse
	if errors.As(err, &er) {
		message = er.Message
		if er.Response != nil {
			status = er.Response.StatusCode
		}
	}
	if status == 0 && resp != nil && resp.Response != nil {
		status = resp.StatusCode
	}

	switch status {
	case http.StatusNotFound:
		return fmt.Errorf("%w%s", ErrNotFound, detail(message))
	case http.StatusForbidden:
		// go-github only builds a RateLimitError when GitHub sends the header;
		// GHES and proxies sometimes do not, so check the parsed rate too.
		if resp != nil && resp.Rate.Limit > 0 && resp.Rate.Remaining == 0 {
			return fmt.Errorf("%w: quota exhausted until %s", ErrRateLimited, resp.Rate.Reset.Format(time.RFC3339))
		}
		return fmt.Errorf("%w%s", ErrForbidden, detail(message))
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w%s", ErrRateLimited, detail(message))
	case http.StatusUnauthorized:
		return fmt.Errorf("github: authentication rejected%s: the App ID or private key no longer "+
			"matches the App on GitHub", detail(message))
	}
	return err
}

func detail(message string) string {
	if message == "" {
		return ""
	}
	return ": " + message
}
