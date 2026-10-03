// Package github wraps everything Zoomies needs from the GitHub API: App
// authentication, minting runner credentials, listing runners and queued jobs,
// and validating inbound webhooks.
//
// Zoomies never asks an operator for a personal access token. It authenticates
// as a GitHub App and mints short-lived registration credentials itself, so
// there is no long-lived token sitting in a dotfile next to a runner.
package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// ErrNotFound is returned when GitHub reports a 404 for something Zoomies
// expected to exist.
var ErrNotFound = errors.New("github: not found")

// ErrRateLimited is returned when the installation has exhausted its quota.
// The caller backs off rather than hammering.
var ErrRateLimited = errors.New("github: rate limited")

// RateLimitedError is a refusal that says when to come back.
//
// GitHub sends the answer on every rate-limited response -- a reset instant on
// the primary limit, a retry-after duration on the secondary one -- and it used
// to be formatted straight into the message and lost, leaving every caller to
// guess a fixed wait. Guessing long wastes quota that came back minutes ago;
// guessing short spends the next window on refusals. So the number is carried.
//
// It wraps ErrRateLimited, so the callers that only ask "was this a rate limit"
// keep working through errors.Is and only the ones that want the time reach for
// errors.As.
type RateLimitedError struct {
	// ResetAt is when the quota refills, zero when GitHub did not say.
	ResetAt time.Time
	// RetryAfter is the secondary limit's answer, which is a duration rather
	// than an instant. Zero when unset.
	RetryAfter time.Duration
	Detail     string
}

func (e *RateLimitedError) Error() string {
	switch {
	case !e.ResetAt.IsZero():
		return fmt.Sprintf("%s: quota exhausted until %s%s",
			ErrRateLimited, e.ResetAt.UTC().Format(time.RFC3339), e.Detail)
	case e.RetryAfter > 0:
		return fmt.Sprintf("%s: secondary rate limit, retry after %s%s",
			ErrRateLimited, e.RetryAfter, e.Detail)
	}
	return ErrRateLimited.Error() + e.Detail
}

func (e *RateLimitedError) Unwrap() error { return ErrRateLimited }

// RetryAt is when a caller may try this installation again, given the time it
// is asking at, and whether GitHub said anything at all.
//
// A reset already in the past is no answer -- clocks drift, and a response can
// sit in a queue -- so it is reported as unknown and the caller keeps its own
// default rather than resuming into the same refusal.
func (e *RateLimitedError) RetryAt(now time.Time) (time.Time, bool) {
	if e == nil {
		return time.Time{}, false
	}
	if e.RetryAfter > 0 {
		return now.Add(e.RetryAfter), true
	}
	if !e.ResetAt.IsZero() && e.ResetAt.After(now) {
		return e.ResetAt, true
	}
	return time.Time{}, false
}

// RetryAfterRateLimit reports when err says an installation may be used again.
//
// It answers false for anything that is not a rate limit and for a rate limit
// GitHub said nothing useful about, which is the same thing to a caller: fall
// back to your own wait.
func RetryAfterRateLimit(err error, now time.Time) (time.Time, bool) {
	var rl *RateLimitedError
	if !errors.As(err, &rl) {
		return time.Time{}, false
	}
	return rl.RetryAt(now)
}

// ErrForbidden is returned for a 403 that is not a rate limit, usually meaning
// the App installation is missing a permission.
var ErrForbidden = errors.New("github: forbidden")

// ErrInvalid is GitHub refusing a request as invalid rather than unauthorised:
// a runner name already taken, a label it will not accept. The detail carries
// the field-by-field reason, which is the part worth reading -- the message on
// a 422 is always "Validation Failed".
var ErrInvalid = errors.New("github: refused as invalid")

// ErrRunnerBusy is DeleteRunner refused because GitHub still considers the
// runner to be running a job. It wraps ErrInvalid -- the same 422 GitHub uses
// for every other "refused as invalid" case -- so a caller that only checks
// for ErrInvalid still catches it, and one that wants to tell this apart from
// a genuinely bad request (a stale ID, a name GitHub never had) can.
//
// This is usually not the operator's problem: GitHub's own bookkeeping can lag
// a few seconds behind the workflow_job "completed" webhook that told Zoomies
// the runner was free, and the delete loses that race. Deleting again once
// GitHub reports the runner idle succeeds without anyone doing anything.
var ErrRunnerBusy = errors.New("github: runner is still running a job")

// JITRequest asks GitHub for a just-in-time runner configuration.
type JITRequest struct {
	// Name must be unique within the target. GitHub rejects reuse.
	Name string
	// Labels are the custom labels this runner advertises, without the
	// implicit self-hosted/os/arch set.
	Labels []string
	// RunnerGroupID is 1 (Default) unless the pool names a group.
	RunnerGroupID int64
	// WorkFolder defaults to "_work".
	WorkFolder string
}

// JITConfig is the base64 blob handed to `actions-runner --jitconfig`. It
// registers an ephemeral runner exactly once and expires quickly, which is what
// makes it safe to pass through an environment variable.
type JITConfig struct {
	// Encoded is the base64 configuration.
	Encoded string
	// RunnerID is the ID GitHub assigned, so Zoomies can delete the
	// registration later if the runner never comes up.
	RunnerID int64
	// Name echoes the requested name.
	Name string
}

// RegistrationToken is the older credential, used by non-ephemeral pools that
// must run config.sh. It is valid for one hour.
type RegistrationToken struct {
	Token     string
	ExpiresAt time.Time
}

// Runner is a runner as GitHub sees it, used to reconcile Zoomies' view with
// reality and to clean up registrations Zoomies has lost track of.
type Runner struct {
	ID     int64
	Name   string
	OS     string
	Status string // online | offline
	Busy   bool
	Labels []string
	// Ephemeral is reported by GitHub for JIT-configured runners.
	Ephemeral bool
}

// QueuedJob is a job the fallback poller found unfinished: waiting for a
// runner, or already running on one. It carries only what the scheduler needs
// to match it to a pool and, once it has started, to its runner.
type QueuedJob struct {
	ID    int64
	RunID int64
	// RunNumber is GitHub's own sequential number for the workflow run --
	// free from the same run listing this job was found in, unlike the
	// webhook path, which has to ask for it separately.
	RunNumber    int64
	Repo         string
	WorkflowName string
	JobName      string
	Labels       []string
	QueuedAt     time.Time
	HTMLURL      string
	// RunnerName is set once GitHub has assigned the job.
	RunnerName  string
	Status      string
	Conclusion  string
	StartedAt   *time.Time
	CompletedAt *time.Time
}

// WorkflowRunState is GitHub's authoritative lifecycle for one workflow run.
// Status becomes "completed" before Conclusion is meaningful.
type WorkflowRunState struct {
	Status     string
	Conclusion string
	// RunNumber is the "#1009" GitHub's Actions UI shows next to the workflow
	// name. workflow_job deliveries never carry it, which is what this lookup
	// is for.
	RunNumber int64
}

// Cancelled reports whether GitHub has finished the whole run as cancelled.
// A cancelled workflow job by itself is deliberately not enough evidence.
func (s WorkflowRunState) Cancelled() bool {
	return s.Status == string(store.JobCompleted) && s.Conclusion == "cancelled"
}

// RunnerGroup is a runner group in the target org.
type RunnerGroup struct {
	ID         int64
	Name       string
	Default    bool
	Visibility string
	// PublicRepositoryAccessKnown distinguishes an older GitHub Enterprise
	// response that omitted the setting from a group that explicitly disables
	// it. Treating both as false would raise a warning nobody could act on.
	PublicRepositoryAccessKnown bool
	AllowsPublicRepositories    bool
	RestrictedToWorkflows       bool
}

// RunnerGroupCreate is the policy for a new organisation runner group.
type RunnerGroupCreate struct {
	Name                     string
	Visibility               string
	AllowsPublicRepositories bool
}

// AppInfo describes the authenticated GitHub App.
type AppInfo struct {
	ID    int64
	Slug  string
	Name  string
	Owner string
	// Permissions is what the App was granted, so setup can tell the operator
	// exactly which permission is missing rather than "403".
	Permissions map[string]string
	Events      []string
	// RepositorySelection is GitHub's own word for how much of the target this
	// installation covers: "all" or "selected". It is the difference between a
	// pool that will see every repository somebody pushes to and one that
	// silently sees none of them -- the second commonest setup mistake after a
	// missing permission, and the one nothing in Zoomies could previously say
	// anything about.
	RepositorySelection string
}

// RateLimit reports the installation's remaining API quota, which the UI shows
// on the Installations page.
type RateLimit struct {
	Limit     int
	Remaining int
	ResetAt   time.Time
}

// Client is the GitHub surface Zoomies uses, scoped to one installation.
//
// It is an interface so that tests can run the whole controller against a fake
// GitHub without a network, and so that GHES differences stay behind one seam.
type Client interface {
	// Target returns the org or repo this client acts on.
	Target() (name string, kind store.TargetType)
	// Probe verifies the credentials and permissions, returning an error whose
	// message names the missing permission where GitHub tells us.
	Probe(ctx context.Context) (*AppInfo, error)
	// CreateJITConfig mints an ephemeral runner registration.
	CreateJITConfig(ctx context.Context, req JITRequest) (*JITConfig, error)
	// CreateRegistrationToken mints a one-hour token for config.sh.
	CreateRegistrationToken(ctx context.Context) (*RegistrationToken, error)
	// CreateRemoveToken mints a token for deregistering a runner from the host.
	CreateRemoveToken(ctx context.Context) (*RegistrationToken, error)
	// ListRunners returns every self-hosted runner registered on the target.
	ListRunners(ctx context.Context) ([]Runner, error)
	// DeleteRunner removes a registration. Deleting one that is already gone
	// returns nil, because the desired end state has been reached.
	DeleteRunner(ctx context.Context, id int64) error
	// ListRunnerGroups returns the target's runner groups.
	ListRunnerGroups(ctx context.Context) ([]RunnerGroup, error)
	// CreateRunnerGroup creates an organisation runner group. Repository
	// targets reject it because GitHub only supports groups at organisation
	// scope.
	CreateRunnerGroup(ctx context.Context, req RunnerGroupCreate) (*RunnerGroup, error)
	// ListQueuedJobs is the webhook fallback: it walks recent workflow runs and
	// returns the jobs still waiting for a runner and the ones running on one.
	// Status tells the two apart.
	ListQueuedJobs(ctx context.Context) ([]QueuedJob, error)
	// GetWorkflowJob reconciles a known job even after its run has completed.
	GetWorkflowJob(ctx context.Context, repo string, id int64) (*WorkflowJobEvent, error)
	// GetWorkflowRun reads the run-level status used to distinguish one
	// cancelled job from cancellation of the entire workflow run.
	GetWorkflowRun(ctx context.Context, repo string, runID int64) (*WorkflowRunState, error)
	// CancelWorkflowRun asks GitHub to cancel the entire run containing a job.
	// Force bypasses conditions that may otherwise keep a run cancelling.
	CancelWorkflowRun(ctx context.Context, repo string, runID int64, force bool) error
	// RerunWorkflowJob asks GitHub to run one job again, together with any
	// job that declares it in `needs`. It is the narrower of the two and the
	// one to reach for: a job re-run on its own account should not drag the
	// rest of the run's failures with it.
	RerunWorkflowJob(ctx context.Context, repo string, jobID int64) error
	// RerunFailedWorkflowJobs asks GitHub to run a run's failed jobs again.
	// Like cancellation it is run-level: GitHub reruns the failed jobs of a
	// run together, so a caller must be clear about what else goes with it.
	// It is the fallback for a job whose GitHub job ID this fleet never saw.
	RerunFailedWorkflowJobs(ctx context.Context, repo string, runID int64) error
	// RateLimit reports remaining quota.
	RateLimit(ctx context.Context) (*RateLimit, error)
	// WebURL returns the browser URL for the target.
	WebURL() string

	// The migration surface. These are the only calls Zoomies makes that
	// write anything to a repository, and they are used by one feature: the
	// wizard that moves a repository's workflows onto this fleet. They need
	// permissions the rest of Zoomies does not have and does not ask for, so
	// every one of them can fail with ErrForbidden on a perfectly healthy
	// installation; callers must say so rather than reporting a broken App.

	// ListRepositories returns the repositories this installation can see.
	ListRepositories(ctx context.Context, limit int) ([]Repository, error)
	// ListWorkflows returns the workflow files at the top of a repository's
	// .github/workflows, with their contents.
	ListWorkflows(ctx context.Context, repo string) ([]WorkflowFile, error)
	// ReadReadme returns the README GitHub renders on the repository's front
	// page, or ErrNoReadme when there is none. It is where the badge goes.
	ReadReadme(ctx context.Context, repo string) (*Readme, error)
	// OpenPullRequest commits a set of files to a new branch and opens a pull
	// request for it.
	OpenPullRequest(ctx context.Context, req PullRequestRequest) (*PullRequest, error)
}

// Repository is one repository an installation can see.
type Repository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	HTMLURL       string `json:"html_url"`
}

// WorkflowFile is one file under .github/workflows as it exists on the
// repository's default branch.
type WorkflowFile struct {
	// Path is repository-relative.
	Path string
	// SHA is the blob SHA. GitHub requires it to update the file, and it is
	// what makes an update fail rather than clobber somebody's change.
	SHA string
	// Content is the decoded file.
	Content string
}

// FileChange is one file a pull request writes.
type FileChange struct {
	Path    string
	Content string
	// SHA is the blob SHA the change was computed against.
	SHA string
}

// PullRequestRequest describes the pull request to open.
type PullRequestRequest struct {
	// Repo is "owner/name".
	Repo string
	// Base is the branch to open against. Empty means the default branch.
	Base string
	// Head is the branch to create. It must not already exist.
	Head          string
	Title         string
	Body          string
	CommitMessage string
	Files         []FileChange
}

// PullRequest is what opening one produced.
type PullRequest struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Branch  string `json:"branch"`
}

// Factory builds a Client for an installation. The controller holds one
// factory and caches clients per installation.
type Factory interface {
	For(ctx context.Context, inst *store.Installation, privateKeyPEM []byte) (Client, error)
}

// RunnerName mints the name one runner of this pool registers under:
// "zoomies-4vcpu-ubuntu-2404-biscuit-a3f9qz2m".
//
// This is the one name Zoomies puts in somebody else's account, so GitHub's
// constraints on it are worth stating where the registration is made. The name
// must be unique within the target -- two runners answering to one name is one
// registration being taken over, not two runners -- which is what the random
// token at the end is for, and why nothing may truncate it away. GitHub also
// shows it in three places a reader arrives at knowing nothing: the runner list,
// the job header, and the "Set up job" step of every log. That is what the shape
// in the middle answers, and store.NewRunnerName explains why it is worth the
// characters.
//
// The 64-character limit GitHub enforces is internal/naming's MaxNameLength, so
// a name is trimmed to fit here rather than refused by the API at registration.
func RunnerName(pool *store.Pool) string { return store.NewRunnerName(pool) }

// SplitTarget parses "owner" or "owner/repo" into its parts.
func SplitTarget(target string) (owner, repo string, kind store.TargetType) {
	if o, r, ok := strings.Cut(target, "/"); ok && r != "" {
		return o, r, store.TargetRepo
	}
	return target, "", store.TargetOrg
}

// NormalizeAPIBaseURL turns the forms operators actually type into the form
// go-github expects: an absolute URL ending in a slash, with /api/v3 appended
// for a bare GHES hostname.
func NormalizeAPIBaseURL(raw string) (string, error) {
	// The rule lives in config so that zoomies.yaml is normalised by the same
	// code as an installation row: the docs promise a bare GHES hostname works
	// in both places, and two copies of the rule had already let the config
	// side refuse what this side accepted.
	return config.NormalizeGitHubAPIBaseURL(raw)
}

// IsEnterprise reports whether an API base URL points at GitHub Enterprise
// Server rather than github.com.
func IsEnterprise(apiBaseURL string) bool {
	return apiBaseURL != "" && !strings.Contains(apiBaseURL, "api.github.com")
}

// WebURLForAPI derives the browser-facing base URL from an API base URL, which
// is what the UI links to.
//
// It is also the URL a runner registers against, so a GHE.com tenant has to
// lose its api. prefix here: config.sh pointed at api.<tenant>.ghe.com/<org>
// fails to register, and the App-creation page there does not exist.
func WebURLForAPI(apiBaseURL string) string {
	if !IsEnterprise(apiBaseURL) {
		return "https://github.com"
	}
	s := strings.TrimSuffix(strings.TrimRight(apiBaseURL, "/"), "/api/v3")
	if config.IsGHECom(s) {
		s = strings.Replace(s, "://api.", "://", 1)
	}
	return s
}

// errorf wraps a GitHub failure with the operation that produced it.
func errorf(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("github: %s: %w", op, err)
}
