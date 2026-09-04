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
}

// Factory builds a Client for an installation. The controller holds one
// factory and caches clients per installation.
type Factory interface {
	For(ctx context.Context, inst *store.Installation, privateKeyPEM []byte) (Client, error)
}

// RunnerName builds a unique, human-readable runner name for a pool.
//
// GitHub requires runner names to be unique within a target and rejects a
// number of characters, so the pool name is sanitised and a short random
// suffix guarantees uniqueness across restarts.
func RunnerName(poolName string) string {
	return "zoomies-" + sanitizeName(poolName) + "-" + store.NewSecret(4)
}

func sanitizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune('-')
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "pool"
	}
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	return out
}

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
	s := strings.TrimSpace(raw)
	if s == "" || s == "https://github.com" || s == "https://api.github.com" {
		return "https://api.github.com/", nil
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	s = strings.TrimRight(s, "/")
	if !strings.HasSuffix(s, "/api/v3") && !strings.HasSuffix(s, "/api/uploads") &&
		!strings.Contains(s, "api.github.com") {
		s += "/api/v3"
	}
	return s + "/", nil
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
