package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
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
	// repositories, since each run costs at least a second call to list its
	// jobs, and a large matrix costs one more for every hundred of them.
	maxPollRuns = 50
	// maxPollRequests caps the API calls one sweep may make in total: the
	// repository listing, the two run listings per repository and the job
	// listing per run (a page each, for a run with more than a hundred jobs).
	// maxPollRuns bounds only the last of those, and a
	// hundred-repository organisation costs two hundred run listings before
	// a single job is looked at -- every thirty seconds, in exactly the
	// failure the poller exists for, which spent the installation's hourly
	// quota in about ten minutes and then starved JIT minting. Sixty a sweep
	// at the default interval is 7,200 an hour at the very worst, and the
	// poller only sweeps at all while no webhook has arrived.
	maxPollRequests = 60
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
	// etags remembers conditional reads for every installation this factory
	// builds a client for. It is bounded, and the scope each client adds to a key
	// is what keeps one installation's answers from another.
	etags *conditionalCache
}

// NewAppFactory returns a factory that dials GitHub with httpClient. Passing
// nil uses a client with a request timeout, which is what production wants;
// tests pass their own to point at a fake.
func NewAppFactory(httpClient *http.Client) *AppFactory {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &AppFactory{http: httpClient, etags: newConditionalCache(conditionalCacheBytes)}
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
	c := newAppClient(asApp, asInstallation, inst.Target, inst.TargetType, inst.InstallationID, WebURLForAPI(apiBase))
	c.enableConditionalReads(f.etags, apiBase+"|"+strconv.FormatInt(inst.InstallationID, 10))
	return c, nil
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
//
// It uses WithURLs rather than WithEnterpriseURLs because NormalizeAPIBaseURL
// has already put the URL in the form go-github wants; letting go-github
// normalise it a second time would append /api/v3 to a URL that has it.
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
	// conditional reads like asInstallation, but a repeated GET that GitHub says
	// is unchanged is answered from memory and costs nothing against the primary
	// rate limit. Nil until enableConditionalReads, and readClient falls back to
	// asInstallation, so a client built without it still works.
	conditional    *gh.Client
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

// enableConditionalReads gives the client a second go-github client over the
// same installation token whose GETs are revalidated rather than repeated. If
// it cannot be built the client simply keeps reading as it did.
func (c *appClient) enableConditionalReads(cache *conditionalCache, scope string) {
	if cache == nil || c.asInstallation.BaseURL() == "" || c.asInstallation.UploadURL() == "" {
		return
	}
	k, err := newGitHubClient(newConditionalClient(c.asInstallation.Client(), cache, scope),
		c.asInstallation.BaseURL(), c.asInstallation.UploadURL())
	if err != nil {
		return
	}
	c.conditional = k
}

// readClient is the client a read that can tolerate a remembered answer uses.
func (c *appClient) readClient() *gh.Client {
	if c.conditional != nil {
		return c.conditional
	}
	return c.asInstallation
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
	info.RepositorySelection = inst.GetRepositorySelection()
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
	return out
}

// SubscribedToJobs reports whether the App will be told about queued jobs.
//
// It is deliberately not one of MissingRequirements: every one of those is a
// permission the fleet cannot work without, and this is a subscription the
// fleet works without -- more slowly, on the fallback poller, which is a
// supported way to run. Bundling the two made a probe return an error for an
// App with every permission it needs, so an installation that was merely slower
// was recorded as unhealthy and the verify dialog's own "will run on the
// fallback poller" sentence could never be reached.
func (a *AppInfo) SubscribedToJobs() bool {
	return slices.Contains(a.Events, "workflow_job")
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

// runnersPage mirrors the self-hosted runner list response.
//
// It is decoded by hand rather than through go-github's Runner type because
// that type has no ephemeral field, and ephemerality is what tells the
// reconciler apart a runner that exited normally after one job from one that
// died. Everything else -- auth, base URL, pagination headers -- still comes
// from go-github.
type runnersPage struct {
	TotalCount int `json:"total_count"`
	Runners    []struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		OS        string `json:"os"`
		Status    string `json:"status"`
		Busy      bool   `json:"busy"`
		Ephemeral bool   `json:"ephemeral"`
		Labels    []struct {
			Name string `json:"name"`
		} `json:"labels"`
	} `json:"runners"`
}

// ListRunners returns every self-hosted runner registered on the target.
func (c *appClient) ListRunners(ctx context.Context) ([]Runner, error) {
	base := "orgs/" + c.owner + "/actions/runners"
	if !c.isOrg() {
		base = "repos/" + c.owner + "/" + c.repo + "/actions/runners"
	}
	var out []Runner
	for page := 1; ; {
		u := fmt.Sprintf("%s?per_page=%d&page=%d", base, pollPerPage, page)
		req, err := c.asInstallation.NewRequest(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, errorf("list runners on "+c.target, err)
		}
		var body runnersPage
		resp, err := c.asInstallation.Do(req, &body)
		if err != nil {
			return nil, c.fail("list runners on "+c.target, resp, err)
		}
		for _, r := range body.Runners {
			runner := Runner{
				ID:        r.ID,
				Name:      r.Name,
				OS:        r.OS,
				Status:    r.Status,
				Busy:      r.Busy,
				Ephemeral: r.Ephemeral,
			}
			for _, l := range r.Labels {
				runner.Labels = append(runner.Labels, l.Name)
			}
			out = append(out, runner)
		}
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		page = resp.NextPage
	}
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
	e := classify(resp, err)
	if e == nil || errors.Is(e, ErrNotFound) {
		return nil
	}
	if errors.Is(e, ErrInvalid) && strings.Contains(strings.ToLower(e.Error()), "currently running a job") {
		e = fmt.Errorf("%w: %w", ErrRunnerBusy, e)
	}
	return c.decorate(fmt.Sprintf("delete runner %d", id), e)
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
			out = append(out, RunnerGroup{
				ID:                          g.GetID(),
				Name:                        g.GetName(),
				Default:                     g.GetDefault(),
				Visibility:                  g.GetVisibility(),
				PublicRepositoryAccessKnown: g.AllowsPublicRepositories != nil,
				AllowsPublicRepositories:    g.GetAllowsPublicRepositories(),
				RestrictedToWorkflows:       g.GetRestrictedToWorkflows(),
			})
		}
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// CreateRunnerGroup creates a group under an organisation installation.
func (c *appClient) CreateRunnerGroup(ctx context.Context, in RunnerGroupCreate) (*RunnerGroup, error) {
	if !c.isOrg() {
		return nil, fmt.Errorf("github: runner groups belong to an organisation, and %s is a repository", c.target)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, errors.New("github: create runner group: a name is required")
	}
	visibility := strings.TrimSpace(in.Visibility)
	if visibility == "" {
		visibility = "all"
	}
	g, resp, err := c.asInstallation.Actions.CreateOrganizationRunnerGroup(ctx, c.owner, gh.CreateRunnerGroupRequest{
		Name:                     &name,
		Visibility:               &visibility,
		AllowsPublicRepositories: gh.Ptr(in.AllowsPublicRepositories),
	})
	if err != nil {
		return nil, c.fail("create runner group "+name+" on "+c.target, resp, err)
	}
	return &RunnerGroup{
		ID:                          g.GetID(),
		Name:                        g.GetName(),
		Default:                     g.GetDefault(),
		Visibility:                  g.GetVisibility(),
		PublicRepositoryAccessKnown: g.AllowsPublicRepositories != nil,
		AllowsPublicRepositories:    g.GetAllowsPublicRepositories(),
		RestrictedToWorkflows:       g.GetRestrictedToWorkflows(),
	}, nil
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
	requests := maxPollRequests
	repos, err := c.pollRepos(ctx, &requests)
	if err != nil {
		return nil, err
	}
	budget := maxPollRuns
	seen := make(map[int64]bool)
	var out []QueuedJob

	for _, full := range repos {
		if budget <= 0 || requests <= 0 {
			break
		}
		owner, name, _ := SplitTarget(full)
		if name == "" {
			continue
		}
		// A run that is in_progress can still hold jobs nobody has picked up,
		// so both statuses matter.
		for _, status := range []string{"queued", "in_progress"} {
			if budget <= 0 || requests <= 0 {
				break
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			requests--
			runs, resp, err := c.asInstallation.Actions.ListRepositoryWorkflowRuns(ctx, owner, name,
				&gh.ListWorkflowRunsOptions{
					Status:      status,
					ListOptions: gh.ListOptions{PerPage: min(budget, pollPerPage)},
				})
			if err != nil {
				e := classify(resp, err)
				// A repository with Actions disabled 404s. That is normal in a
				// large org and must not abort the whole sweep.
				if errors.Is(e, ErrNotFound) {
					break
				}
				return nil, c.decorate("list workflow runs for "+full, e)
			}
			for _, run := range runs.WorkflowRuns {
				if budget <= 0 || requests <= 0 {
					break
				}
				budget--
				requests--
				jobs, err := c.queuedJobsForRun(ctx, owner, name, run, &requests)
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

// pollRepos returns the repositories one poll should look at, most recently
// pushed first: a queued job usually follows a push, so when the sweep runs
// out of requests it is the quiet repositories it has not reached, not the
// busy ones. Each page listed comes off the request budget.
func (c *appClient) pollRepos(ctx context.Context, requests *int) ([]string, error) {
	if !c.isOrg() {
		return []string{c.target}, nil
	}
	type candidate struct {
		name     string
		pushedAt time.Time
	}
	opts := &gh.ListOptions{PerPage: pollPerPage}
	var found []candidate
	for len(found) < maxPollRepos && *requests > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		*requests--
		page, resp, err := c.asInstallation.Apps.ListRepos(ctx, opts)
		if err != nil {
			return nil, c.fail("list repositories visible to the installation", resp, err)
		}
		for _, r := range page.Repositories {
			if r.GetFullName() == "" {
				continue
			}
			found = append(found, candidate{name: r.GetFullName(), pushedAt: r.GetPushedAt().Time})
			if len(found) == maxPollRepos {
				break
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	slices.SortStableFunc(found, func(a, b candidate) int { return b.pushedAt.Compare(a.pushedAt) })
	out := make([]string, 0, len(found))
	for _, r := range found {
		out = append(out, r.name)
	}
	return out, nil
}

// queuedJobsForRun lists a run's unfinished jobs, following the listing's
// pages: a matrix can hold 256 jobs, and completed jobs stay in the listing, so
// the tail past the first page is otherwise never seen. The caller has already
// paid for the first page; each further one comes off requests, and a run that
// outlasts the sweep's budget is cut short with what it has rather than failed.
func (c *appClient) queuedJobsForRun(ctx context.Context, owner, repo string, run *gh.WorkflowRun, requests *int) ([]QueuedJob, error) {
	opts := &gh.ListWorkflowJobsOptions{
		Filter:      "latest",
		ListOptions: gh.ListOptions{PerPage: pollPerPage},
	}
	full := owner + "/" + repo
	var out []QueuedJob
	for {
		jobs, resp, err := c.asInstallation.Actions.ListWorkflowJobs(ctx, owner, repo, run.GetID(), opts)
		if err != nil {
			e := classify(resp, err)
			if errors.Is(e, ErrNotFound) {
				return out, nil
			}
			return nil, c.decorate(fmt.Sprintf("list jobs for run %d in %s/%s", run.GetID(), owner, repo), e)
		}
		out = append(out, queuedJobsOnPage(full, run, jobs.Jobs)...)
		if resp == nil || resp.NextPage == 0 || *requests <= 0 {
			return out, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		*requests--
		opts.Page = resp.NextPage
	}
}

// queuedJobsOnPage keeps the jobs of one page that are waiting or running.
//
// Running ones are kept because a sweep is a sample, not a stream: an idle
// runner takes a job within a second or two of GitHub queuing it, so the job
// is already in progress the first time a sweep sees it, and a listing that
// dropped it would leave the runner under it looking free for the whole build.
// They cost no request -- they are on the page this sweep already paid for.
func queuedJobsOnPage(full string, run *gh.WorkflowRun, jobs []*gh.WorkflowJob) []QueuedJob {
	var out []QueuedJob
	for _, j := range jobs {
		status := store.JobState(j.GetStatus())
		if status != store.JobQueued && status != store.JobInProgress {
			continue
		}
		q := QueuedJob{
			ID:           j.GetID(),
			RunID:        run.GetID(),
			RunNumber:    int64(run.GetRunNumber()),
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
		// GitHub stamps started_at on a queued job too, equal to created_at,
		// so it only means something once the job has left the queue.
		if started := j.GetStartedAt().Time; status == store.JobInProgress && !started.IsZero() {
			q.StartedAt = &started
		}
		out = append(out, q)
	}
	return out
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

// fail turns a go-github error into one an operator can act on.
func (c *appClient) fail(op string, resp *gh.Response, err error) error {
	return c.decorate(op, classify(resp, err))
}

// decorate names the operation that failed and, for a 403, the permission that
// is almost certainly the cause.
func (c *appClient) decorate(op string, e error) error {
	if e == nil {
		return nil
	}
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

	// A rate limit that lands on the installation token refresh is still a rate
	// limit, and it is the one a real fleet meets first: every background sweep
	// mints a token before it does anything else. ghinstallation does that
	// inside the transport, so the refusal arrives as its own error with no
	// go-github response attached and used to fall through this function
	// unclassified -- which meant the per-installation stand-down never
	// engaged, and the fleet kept spending a quota it did not have while its
	// paused gauge read zero.
	var he *ghinstallation.HTTPError
	if errors.As(err, &he) && he.Response != nil {
		if out, ok := rateLimitFromResponse(he.Response); ok {
			return out
		}
	}

	var rl *gh.RateLimitError
	if errors.As(err, &rl) {
		return &RateLimitedError{ResetAt: rl.Rate.Reset.Time}
	}
	var abuse *gh.AbuseRateLimitError
	if errors.As(err, &abuse) {
		out := &RateLimitedError{}
		if abuse.RetryAfter != nil {
			out.RetryAfter = *abuse.RetryAfter
		}
		return out
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
			return &RateLimitedError{ResetAt: resp.Rate.Reset.Time}
		}
		return fmt.Errorf("%w%s", ErrForbidden, detail(message))
	case http.StatusUnprocessableEntity:
		// The one status where GitHub says which field it objected to, and the
		// one that used to fall through as go-github's raw "422 Validation
		// Failed" -- printed on a runner row as the reason it never started.
		return fmt.Errorf("%w%s", ErrInvalid, validationDetail(er, message))
	case http.StatusTooManyRequests:
		// A 429 with no rate headers go-github recognised. The retry-after is
		// still worth taking when the proxy in front of GitHub sent one.
		out := &RateLimitedError{Detail: detail(message)}
		if resp != nil && resp.Response != nil {
			if after, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && after > 0 {
				out.RetryAfter = time.Duration(after) * time.Second
			}
		}
		if resp != nil && resp.Rate.Limit > 0 && resp.Rate.Remaining == 0 {
			out.ResetAt = resp.Rate.Reset.Time
		}
		return out
	case http.StatusUnauthorized:
		return fmt.Errorf("github: authentication rejected%s: the App ID or private key no longer "+
			"matches the App on GitHub", detail(message))
	}
	return err
}

// rateLimitFromResponse reads a refusal's own rate-limit headers, for the
// failures that never reach go-github's classification.
//
// A refusal is only read as a rate limit when the headers say the quota is
// actually gone: a 403 for a permission the App does not have carries a
// remaining quota, and calling that a rate limit would stand an installation
// down for fifteen minutes over something waiting will never fix.
func rateLimitFromResponse(resp *http.Response) (*RateLimitedError, bool) {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return nil, false
	}
	remaining, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining"))
	if err != nil || remaining > 0 {
		return nil, false
	}
	out := &RateLimitedError{}
	if sec, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && sec > 0 {
		out.ResetAt = time.Unix(sec, 0).UTC()
	}
	if after, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && after > 0 {
		out.RetryAfter = time.Duration(after) * time.Second
	}
	return out, true
}

func detail(message string) string {
	if message == "" {
		return ""
	}
	return ": " + message
}

// validationDetail renders GitHub's per-field errors on a 422, which say what
// was actually wrong: "name is already taken" rather than "Validation Failed".
func validationDetail(er *gh.ErrorResponse, message string) string {
	if er == nil || len(er.Errors) == 0 {
		return detail(message)
	}
	parts := make([]string, 0, len(er.Errors))
	for _, e := range er.Errors {
		switch {
		case e.Message != "":
			parts = append(parts, e.Message)
		case e.Field != "" && e.Code != "":
			parts = append(parts, strings.TrimSpace(e.Resource+" "+e.Field)+" "+e.Code)
		case e.Code != "":
			parts = append(parts, e.Code)
		}
	}
	if len(parts) == 0 {
		return detail(message)
	}
	return ": " + strings.Join(parts, "; ")
}

// GetWorkflowJob uses the job ID rather than an unfinished-run listing: a
// cancelled run disappears from that listing before a missed webhook is repaired.
func (c *appClient) GetWorkflowJob(ctx context.Context, repo string, id int64) (*WorkflowJobEvent, error) {
	owner, name, kind := SplitTarget(repo)
	if kind != store.TargetRepo {
		return nil, fmt.Errorf("github: invalid job repository %q", repo)
	}
	job, resp, err := c.asInstallation.Actions.GetWorkflowJobByID(ctx, owner, name, id)
	if err != nil {
		return nil, c.fail("read workflow job", resp, err)
	}
	body, err := json.Marshal(map[string]any{
		"action": job.GetStatus(), "repository": map[string]string{"full_name": repo}, "workflow_job": job,
	})
	if err != nil {
		return nil, err
	}
	return ParseWorkflowJob(body)
}

// GetWorkflowRun is the run-level authority for cancellation. workflow_job
// only says that one job was cancelled; it does not say why or whether its
// queued and running siblings were cancelled too.
func (c *appClient) GetWorkflowRun(ctx context.Context, repo string, runID int64) (*WorkflowRunState, error) {
	owner, name, kind := SplitTarget(repo)
	if kind != store.TargetRepo || runID <= 0 {
		return nil, fmt.Errorf("github: invalid workflow run %q/%d", repo, runID)
	}
	run, resp, err := c.asInstallation.Actions.GetWorkflowRunByID(ctx, owner, name, runID)
	if err != nil {
		return nil, c.fail("read workflow run", resp, err)
	}
	return &WorkflowRunState{
		Status:     run.GetStatus(),
		Conclusion: run.GetConclusion(),
		RunNumber:  int64(run.GetRunNumber()),
	}, nil
}

// RerunWorkflowJob asks GitHub to run one job again.
//
// GitHub re-runs the job together with any job that names it in `needs`, which
// is the narrowest thing the API offers and the right default: a job that a
// fleet fault killed should not drag the run's unrelated failures back through
// CI with it. What it cannot do is make the re-run free of consequence -- a job
// that had already pushed an image before it died will push it again, because
// a re-run starts at the first step.
//
// Unlike cancellation, this really is job-level. The comment that used to sit
// on RerunFailedWorkflowJobs said GitHub had no such endpoint; it has had one
// since 2021, and the run-level call was doing four times the work on a run
// with four failures in it.
func (c *appClient) RerunWorkflowJob(ctx context.Context, repo string, jobID int64) error {
	owner, name, kind := SplitTarget(repo)
	if kind != store.TargetRepo || jobID <= 0 {
		return fmt.Errorf("github: invalid workflow job %q/%d", repo, jobID)
	}
	resp, err := c.asInstallation.Actions.RerunJobByID(ctx, owner, name, jobID)
	if err == nil || (resp != nil && (resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusCreated)) {
		return nil
	}
	e := classify(resp, err)
	if errors.Is(e, ErrForbidden) {
		return fmt.Errorf("github: rerun job: %w; check the App installation on %s: it needs \"Actions\" (actions) read and write, and changed permissions must be accepted on the installation", e, c.target)
	}
	return errorf("rerun job", e)
}

// RerunFailedWorkflowJobs asks GitHub to run the failed jobs of a run again.
//
// It is the counterpart of CancelWorkflowRun and shares its blast radius: every
// failed job in the run comes back, not only the one somebody had in mind. That
// is why RerunWorkflowJob above is preferred, and this is kept for the job whose
// own GitHub job ID this fleet never recorded. The jobs come back as new
// deliveries with a higher run attempt, and nothing here pretends otherwise --
// the re-run is a request, and what GitHub does with it arrives through the
// ordinary webhook path.
func (c *appClient) RerunFailedWorkflowJobs(ctx context.Context, repo string, runID int64) error {
	owner, name, kind := SplitTarget(repo)
	if kind != store.TargetRepo || runID <= 0 {
		return fmt.Errorf("github: invalid workflow run %q/%d", repo, runID)
	}
	resp, err := c.asInstallation.Actions.RerunFailedJobsByID(ctx, owner, name, runID)
	// 201 Created is the documented success, and go-github turns a 202 into an
	// AcceptedError for the same reason it does on a cancel: the work is
	// asynchronous. Both mean GitHub took the request.
	if err == nil || (resp != nil && (resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusCreated)) {
		return nil
	}
	e := classify(resp, err)
	if errors.Is(e, ErrForbidden) {
		return fmt.Errorf("github: rerun failed jobs: %w; check the App installation on %s: it needs \"Actions\" (actions) read and write, and changed permissions must be accepted on the installation", e, c.target)
	}
	return errorf("rerun failed jobs", e)
}

// CancelWorkflowRun calls GitHub's run-level cancellation API. GitHub does not
// expose job-level cancellation, so callers must make that blast radius clear.
func (c *appClient) CancelWorkflowRun(ctx context.Context, repo string, runID int64, force bool) error {
	owner, name, kind := SplitTarget(repo)
	if kind != store.TargetRepo || runID <= 0 {
		return fmt.Errorf("github: invalid workflow run %q/%d", repo, runID)
	}
	var resp *gh.Response
	var err error
	if force {
		path := fmt.Sprintf("repos/%s/%s/actions/runs/%d/force-cancel", owner, name, runID)
		req, reqErr := c.asInstallation.NewRequest(ctx, http.MethodPost, path, nil)
		if reqErr != nil {
			return errorf("force cancel workflow run", reqErr)
		}
		resp, err = c.asInstallation.Do(req, nil)
	} else {
		resp, err = c.asInstallation.Actions.CancelWorkflowRunByID(ctx, owner, name, runID)
	}
	// go-github represents 202 Accepted as AcceptedError because the operation
	// is asynchronous. For cancellation, accepted is exactly success.
	if err == nil || (resp != nil && resp.StatusCode == http.StatusAccepted) {
		return nil
	}
	op := "cancel workflow run"
	if force {
		op = "force cancel workflow run"
	}
	e := classify(resp, err)
	if errors.Is(e, ErrForbidden) {
		return fmt.Errorf("github: %s: %w; check the App installation on %s: it needs \"Actions\" (actions) read and write, and changed permissions must be accepted on the installation", op, e, c.target)
	}
	return errorf(op, e)
}
