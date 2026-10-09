package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	gh "github.com/google/go-github/v88/github"
)

// GitHubActionsAppID is the ID GitHub gives the app that posts a workflow job's
// check. A required status check is pinned to Actions when it names this app,
// and only then can the jobs the fleet saw say whether the check ever reports:
// a check that any app may post could come from somewhere the fleet cannot see.
const GitHubActionsAppID int64 = 15368

// kennelRulePages bounds the rules read for one branch: a hundred a page, five
// pages. A branch with more is read as far as that and marked partial, so a
// repository cannot make one refresh unbounded.
const kennelRulePages = 5

// kennelRequiredCap bounds how many required checks are kept for a branch. It
// is far above what a repository requires, and a repository over it is marked
// partial rather than stored whole.
const kennelRequiredCap = 100

// KennelSettingsReader is optional, like the other Kennel Club readers: a
// client without it gives no settings coverage and needs no new capability.
// Everything on it is a GET, and nothing on it can write a setting.
//
// Both methods need the App's Administration read permission, which GitHub
// offers no narrower form of, so it is asked for separately and only when the
// operator has switched the settings checks on.
type KennelSettingsReader interface {
	// KennelSettings reads the Actions settings that govern a workflow's token
	// and a fork's approval. public says which fork policy applies: a public
	// repository has an approval policy, and a private one has the rules for
	// fork pull requests. An internal repository is read as private.
	KennelSettings(ctx context.Context, repo string, public bool) (*KennelSettings, error)
	// KennelProtection reads the status checks a branch requires, from classic
	// branch protection and from the rules that apply to the branch. Both are
	// read, because a repository protected one way looks unprotected to a reader
	// of the other.
	KennelProtection(ctx context.Context, repo, branch string) (*KennelProtection, error)
}

var _ KennelSettingsReader = (*appClient)(nil)

// KennelSettings is the part of a repository's Actions settings that Kennel Club
// judges, as booleans and one policy word. Nothing a repository wrote is in it.
type KennelSettings struct {
	// DefaultTokenWrite is true when the default workflow token can write.
	DefaultTokenWrite bool
	// ForkApproval is GitHub's approval policy for a fork pull request from an
	// outside contributor, for a public repository. It is empty when the policy
	// was not read: a private repository, or a GitHub that lacks the endpoint.
	ForkApproval string
	// PrivateFork says what a private repository does with fork pull requests.
	// It is nil for a public repository and when GitHub lacks the endpoint.
	PrivateFork *KennelPrivateFork
	// Partial is set when an endpoint this GitHub does not offer was left out, so
	// a finding that needed it cannot be judged.
	Partial bool
}

// KennelPrivateFork is a private repository's policy for fork pull requests.
type KennelPrivateFork struct {
	// Runs is true when fork pull requests run workflows at all.
	Runs bool
	// Secrets is true when they are given the repository's secrets and variables.
	Secrets bool
	// WriteToken is true when they are given a token that can write.
	WriteToken bool
}

// KennelProtection is the required status checks of one branch.
type KennelProtection struct {
	// Required are the checks pinned to the GitHub Actions app, from every source
	// that requires them, sorted and without repeats. They are names a
	// repository chose, so the controller compares them with job names and keeps
	// them out of every sentence.
	Required []string
	// Unpinned counts the required checks that name no app or another one, or
	// that one source leaves open to any app. They are counted and never judged.
	Unpinned int
	// Partial is set when one half could not be read or a limit was reached, so
	// the list may be short.
	Partial bool
}

func (c *appClient) KennelSettings(ctx context.Context, repo string, public bool) (*KennelSettings, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	rc := c.readClient()
	wf, resp, err := rc.Repositories.GetDefaultWorkflowPermissions(ctx, owner, name)
	if err != nil {
		return nil, c.failAdministration("read the default workflow permissions", resp, err)
	}
	out := &KennelSettings{DefaultTokenWrite: wf.GetDefaultWorkflowPermissions() == "write"}
	if public {
		ap, resp, err := rc.Actions.GetForkPRContributorApprovalPermissions(ctx, owner, name)
		switch {
		case err == nil:
			out.ForkApproval = ap.ApprovalPolicy
		case kennelEndpointMissing(resp, err):
			out.Partial = true
		default:
			return nil, c.failAdministration("read the fork pull request approval policy", resp, err)
		}
		return out, nil
	}
	pf, resp, err := rc.Repositories.GetPrivateRepoForkPRWorkflowSettings(ctx, owner, name)
	switch {
	case err == nil:
		out.PrivateFork = &KennelPrivateFork{
			Runs:       pf.GetRunWorkflowsFromForkPullRequests(),
			Secrets:    pf.GetSendSecretsAndVariables(),
			WriteToken: pf.GetSendWriteTokensToWorkflows(),
		}
	case kennelEndpointMissing(resp, err):
		out.Partial = true
	default:
		return nil, c.failAdministration("read the fork pull request settings", resp, err)
	}
	return out, nil
}

func (c *appClient) KennelProtection(ctx context.Context, repo, branch string) (*KennelProtection, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	if branch == "" {
		return nil, fmt.Errorf("github: repository has no default branch")
	}
	rc := c.readClient()
	// A context is pinned only if every source that requires it pins it to
	// Actions: one source that lets any app post it is enough for the check to
	// come from somewhere the fleet cannot see.
	pinned := map[string]bool{}
	note := func(check string, appID *int64) {
		check = strings.TrimSpace(check)
		if check == "" {
			return
		}
		ok := appID != nil && *appID == GitHubActionsAppID
		if prev, seen := pinned[check]; seen {
			pinned[check] = prev && ok
			return
		}
		pinned[check] = ok
	}
	out := &KennelProtection{}

	classicDenied, rulesDenied := false, false
	var classicErr error

	prot, resp, err := rc.Repositories.GetBranchProtection(ctx, owner, name, branch)
	switch {
	case err == nil:
		if rsc := prot.GetRequiredStatusChecks(); rsc != nil {
			if rsc.Checks != nil {
				for _, ck := range *rsc.Checks {
					if ck != nil {
						note(ck.Context, ck.AppID)
					}
				}
			} else if rsc.Contexts != nil {
				// The deprecated form names no app at all.
				for _, name := range *rsc.Contexts {
					note(name, nil)
				}
			}
		}
	case errors.Is(err, gh.ErrBranchNotProtected):
		// Normal, and not a failure: classic protection answers 404 "Branch not
		// protected" for a branch that has none, and the rules may still require
		// checks of it.
	case kennelStatus(resp, err) == http.StatusForbidden:
		classicDenied = true
		classicErr = c.failAdministration("read the branch protection", resp, err)
	default:
		return nil, c.failAdministration("read the branch protection", resp, err)
	}

	// go-github escapes the branch name for classic protection and does not for
	// the rules, and a slash in a branch name is one segment to GitHub, not two,
	// so the rules read is given the name already escaped.
	escaped := url.PathEscape(branch)
	opts := &gh.ListOptions{PerPage: 100}
	for page := 0; page < kennelRulePages; page++ {
		rules, resp, err := rc.Repositories.ListRulesForBranch(ctx, owner, name, escaped, opts)
		if err != nil {
			switch {
			case kennelEndpointMissing(resp, err):
				// A GitHub without rules has none that require anything.
			case kennelStatus(resp, err) == http.StatusForbidden:
				rulesDenied = true
			default:
				return nil, c.fail("read the rules for the branch", resp, err)
			}
			break
		}
		for _, rule := range rules.RequiredStatusChecks {
			if rule == nil {
				continue
			}
			for _, ck := range rule.Parameters.RequiredStatusChecks {
				if ck != nil {
					note(ck.Context, ck.IntegrationID)
				}
			}
		}
		if resp.NextPage == 0 {
			break
		}
		if page == kennelRulePages-1 {
			out.Partial = true
		}
		opts.Page = resp.NextPage
	}

	switch {
	case classicDenied && rulesDenied:
		return nil, classicErr
	case classicDenied:
		out.Partial = true
	case rulesDenied:
		out.Partial = true
	}

	for check, ok := range pinned {
		if !ok {
			out.Unpinned++
			continue
		}
		out.Required = append(out.Required, check)
	}
	sort.Strings(out.Required)
	if len(out.Required) > kennelRequiredCap {
		out.Required = out.Required[:kennelRequiredCap]
		out.Partial = true
	}
	return out, nil
}

// failAdministration is fail for a read that needs the Administration read
// permission. The shared hint on a 403 talks about runner permissions, which
// would send an operator to the wrong checkbox.
func (c *appClient) failAdministration(op string, resp *gh.Response, err error) error {
	e := classify(resp, err)
	if errors.Is(e, ErrForbidden) {
		return fmt.Errorf("github: %s: %w; the App installation on %s needs the \"Administration\" (administration) "+
			"read permission, which is optional, and changing permissions needs the installation to accept them",
			op, e, c.target)
	}
	return errorf(op, e)
}

// kennelStatus is the HTTP status of a failed go-github call, or zero.
func kennelStatus(resp *gh.Response, err error) int {
	var er *gh.ErrorResponse
	if errors.As(err, &er) && er.Response != nil {
		return er.Response.StatusCode
	}
	if resp != nil && resp.Response != nil {
		return resp.StatusCode
	}
	return 0
}

// kennelEndpointMissing is a 404 or a 405: a GitHub that does not offer the
// endpoint, which on an older server is a fact about the server and not a
// failure. The shared classify maps 404 and leaves 405 to fall through, and this
// is the one place that treats the two alike.
func kennelEndpointMissing(resp *gh.Response, err error) bool {
	switch kennelStatus(resp, err) {
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return true
	}
	return false
}
