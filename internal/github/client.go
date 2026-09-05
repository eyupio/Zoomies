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

// ErrForbidden is returned for a 403 that is not a rate limit, usually meaning
// the App installation is missing a permission.
var ErrForbidden = errors.New("github: forbidden")

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

// QueuedJob is a job the fallback poller found waiting. It carries only what
// the scheduler needs to match it to a pool.
type QueuedJob struct {
	ID           int64
	RunID        int64
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

// RunnerGroup is a runner group in the target org.
type RunnerGroup struct {
	ID   int64
	Name string
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
	// ListQueuedJobs is the webhook fallback: it walks recent workflow runs and
	// returns the jobs still waiting for a runner.
	ListQueuedJobs(ctx context.Context) ([]QueuedJob, error)
	// RateLimit reports remaining quota.
	RateLimit(ctx context.Context) (*RateLimit, error)
	// WebURL returns the browser URL for the target.
	WebURL() string

	// The migration surface. These three are the only calls Zoomies makes that
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
	// OpenPullRequest commits a set of files to a new branch and opens a pull
	// request for it.
	OpenPullRequest(ctx context.Context, req PullRequestRequest) (*PullRequest, error)
}

// Repository is one repository an installation can see.
type Repository struct {
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

// RunnerName mints the name one runner registers under: the brand and a short
// random suffix, "zoomies-k3f9qz2m".
//
// GitHub requires the name to be unique within a target and shows it in the
// runner list, in the job header and in the "Set up job" step of every log. It
// used to carry the pool name as well, which made the common case --
// "zoomies-linux-x64-a3f9q" -- long enough that the brand was what got
// truncated in GitHub's own tables. Which pool a runner belongs to is a click
// away in Zoomies and is on the runner's labels either way.
func RunnerName() string { return store.NewRunnerName() }

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
func WebURLForAPI(apiBaseURL string) string {
	if !IsEnterprise(apiBaseURL) {
		return "https://github.com"
	}
	s := strings.TrimSuffix(strings.TrimRight(apiBaseURL, "/"), "/api/v3")
	return s
}

// errorf wraps a GitHub failure with the operation that produced it.
func errorf(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("github: %s: %w", op, err)
}
