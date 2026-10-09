// Package kennel decides what is wrong with a repository, from the facts it is
// handed and from nothing else.
//
// It is pure on purpose, for the reason internal/scheduler is: no clock, no
// database, no network. A check that reads the world cannot be tested by
// describing a world, and a finding whose sentence came from somewhere other
// than this package cannot be trusted not to repeat what a stranger wrote into
// a repository. Both properties are tested: boundary_test.go fails if this
// package imports anything but the standard library or reads a clock, and the
// hostile-input test fails if repository text reaches a sentence.
//
// The question every check answers is "does this affect running CI, or the
// fleet that runs it?". The optional setup area also reports repository-local
// guidance that makes contributions, maintenance and CI easier to sustain.
package kennel

import (
	"slices"
	"strings"
)

// Version is the evaluator's version. A stored evaluation made by an older one
// is re-run, so bump it whenever a check's meaning or wording changes.
const Version = 7

// Code names one check. Codes are stable across releases and form a closed
// set: Evaluate returns no code that is not in the registry below.
type Code string

const (
	CodeGuidanceMissing           Code = "guidance.missing"
	CodeGuidanceBroken            Code = "guidance.broken_reference"
	CodeGuidanceDuplicated        Code = "guidance.duplicated"
	CodeGuidanceUnreadable        Code = "guidance.unreadable"
	CodeNoTimeout                 Code = "ci.no_timeout"
	CodeNoConcurrency             Code = "ci.no_concurrency"
	CodeActionNotPinned           Code = "ci.action_not_pinned"
	CodePermissionsUnset          Code = "token.permissions_unset"
	CodeSetupReadme               Code = "setup.readme"
	CodeSetupLicence              Code = "setup.licence"
	CodeSetupSecurity             Code = "setup.security"
	CodeSetupContributing         Code = "setup.contributing"
	CodeSetupCodeOfConduct        Code = "setup.code_of_conduct"
	CodeSetupIssueTemplate        Code = "setup.issue_template"
	CodeSetupPullRequestTemplate  Code = "setup.pull_request_template"
	CodeSetupCodeowners           Code = "setup.codeowners"
	CodeSetupDependencyUpdates    Code = "setup.dependency_updates"
	CodeSetupWorkflows            Code = "setup.workflows"
	CodePublicRepoOnFleet         Code = "exposure.public_repo_on_fleet"
	CodePublicRepoWeakPool        Code = "exposure.public_repo_weak_pool"
	CodeForkCodeRan               Code = "exposure.fork_code_ran"
	CodeTargetEventRan            Code = "exposure.target_event_ran"
	CodeUnservedLabel             Code = "capacity.unserved_label"
	CodeJobHitDefaultLimit        Code = "capacity.job_hit_default_limit"
	CodeMatrixExceedsPool         Code = "capacity.matrix_exceeds_pool"
	CodeTargetCheckoutPRHead      Code = "exposure.target_checkout_pr_head"
	CodeWorkflowUnreadable        Code = "ci.workflow_unreadable"
	CodePinsWithoutUpdater        Code = "ci.pins_without_updater"
	CodeLabelUnserved             Code = "ci.label_unserved"
	CodeSecretOnCommandLine       Code = "ci.secret_on_command_line"
	CodeDefaultTokenWrite         Code = "token.default_write"
	CodeForkApprovalWeak          Code = "exposure.fork_approval_weak"
	CodePrivateForkSecrets        Code = "exposure.private_fork_secrets"
	CodeRequiredCheckNeverReports Code = "protection.required_check_never_reports"
)

// Area groups codes for the operator, who can turn a whole area off.
type Area string

const (
	AreaGuidance Area = "guidance"
	AreaExposure Area = "exposure"
	AreaCapacity Area = "capacity"
	AreaSetup    Area = "setup"
	AreaCI       Area = "ci"
	AreaToken    Area = "token"
	// AreaProtection is what the default branch requires before a merge.
	AreaProtection Area = "protection"
)

// Area is the part of a code before its dot.
func (c Code) Area() Area {
	area, _, _ := strings.Cut(string(c), ".")
	return Area(area)
}

// Severity says how much a finding matters. The values are the problem list's
// own, so the UI reuses its severity badge; a test holds them equal to
// config.Severity without this package importing it.
type Severity string

const (
	// SeverityError: a stranger or a fault can already hurt the fleet, or stop
	// CI.
	SeverityError Severity = "error"
	// SeverityWarning: a guard is weaker than it should be, or capacity is
	// being wasted.
	SeverityWarning Severity = "warning"
	// SeverityInfo: worth knowing, cheap to ignore. It never stops a repository
	// being best in show.
	SeverityInfo Severity = "info"
)

// rank orders severities; an unknown one ranks below them all.
func (s Severity) rank() int {
	switch s {
	case SeverityError:
		return 3
	case SeverityWarning:
		return 2
	case SeverityInfo:
		return 1
	}
	return 0
}

// Check is one entry in the registry: what it is called, how severe it usually
// is, what it needs to have been read, and how to judge a snapshot.
type Check struct {
	Code Code `json:"code"`
	Area Area `json:"area"`
	// Severity is the usual one. A finding can be milder for a first-party
	// action or a private repository, or worse when another check raises it.
	Severity Severity `json:"severity"`
	// Detects is one sentence for the catalogue and the documentation. The
	// sentences an operator reads about a particular repository are written
	// per finding, not here.
	Detects string `json:"detects"`
	// Needs are the sources every evaluation of this check must have read to
	// know whether it applies at all.
	Needs []Source `json:"needs"`
	// Conditional are the sources read only once the check applies, such as the
	// run history, which is read for public repositories and no others. They are
	// listed here so the catalogue can say what a check takes before anything has
	// been read; a test holds this list equal to what the check actually asks for.
	Conditional []Source `json:"conditional"`
	// Fix is what to change, in the imperative, written once for the check.
	// The sentence an operator reads on a finding carries the numbers; this one
	// is for the catalogue, where an agent reads it before it has a finding.
	Fix string `json:"fix"`
	// Verify is how to see that the change worked: what Recheck, the next run
	// or the next tree read shows once it has.
	Verify string `json:"verify"`
	// Docs is the anchor on the Kennel Club page, derived from the code so a
	// renamed check cannot keep an old link.
	Docs string `json:"docs"`

	eval func(*Snapshot) result
}

// docsAnchor is where the Kennel Club page documents a check: one heading per
// code, with the dots Material would drop written as dashes.
func docsAnchor(code Code) string {
	return "kennel-club.md#" + strings.ReplaceAll(string(code), ".", "-")
}

// result is what one check made of one snapshot.
type result struct {
	// applies is whether the repository is one the check has anything to say
	// about: a private repository is not a candidate for the public-exposure
	// checks, which is different from being clear of them.
	applies bool
	// incomplete keeps a sampled workflow inspection incomplete even when every
	// check has positive evidence from the files that were read.
	incomplete bool
	// extra are sources needed only once the check applies, such as the run
	// history, which is read for public repositories and no others.
	extra    []Source
	findings []Finding
}

// checks is the registry, in the order findings of equal severity are listed.
var checks = []Check{
	guidanceCheck(CodeGuidanceMissing, SeverityInfo, "No agent guidance found", "No recognised agent instruction file was found on the default branch.", "Preview agent guidance to propose an AGENTS.md and a Claude import, using commands declared in repository files."),
	guidanceCheck(CodeGuidanceBroken, SeverityWarning, "Agent guidance has a broken reference", "An instruction file imports or links to a missing repository path, or its Claude imports form a cycle.", "Preview agent guidance for unambiguous import repairs; review other broken links or import cycles and correct them manually. Use AI Context repair for damaged Zoomies-managed sections."),
	guidanceCheck(CodeGuidanceDuplicated, SeverityInfo, "Agent guidance is duplicated", "A root or nested Claude instruction file repeats the root AGENTS.md in full.", "Preview agent guidance to replace the duplicate Claude file with an import of the shared AGENTS.md."),
	guidanceCheck(CodeGuidanceUnreadable, SeverityWarning, "Agent guidance could not be inspected", "An instruction file is empty, is not regular UTF-8 text or exceeds the file or byte limits.", "Keep instruction files as non-empty regular UTF-8 text under 64 KiB each, with at most 32 files inspected; split or shorten them and recheck."),
	{
		Code: CodePublicRepoOnFleet, Area: AreaExposure, Severity: SeverityWarning,
		Detects: "A public repository ran jobs on this fleet, or has jobs waiting for it.",
		Needs:   []Source{SourceFleet, SourceMetadata},
		Fix:     "Decide whether this fleet should run a public repository's jobs at all; if it should, keep them on an ephemeral pool with no host socket, no privileged daemon and no root, so a stranger's pull request cannot reach the host.",
		Verify:  "Press Recheck once the pool is ephemeral and unprivileged, or once the repository no longer sends jobs here; the finding closes when the next read sees neither.",
		Docs:    docsAnchor(CodePublicRepoOnFleet),
		eval:    evalPublicRepoOnFleet,
	},
	{
		Code: CodePublicRepoWeakPool, Area: AreaExposure, Severity: SeverityError,
		Detects: "The pool that ran a public repository's jobs is persistent, mounts the host Docker socket, gives its jobs a privileged Docker daemon, runs as root or uses no container.",
		Needs:   []Source{SourceFleet, SourceMetadata},
		Fix:     "Move the public repository's jobs to a pool that is ephemeral, does not mount the host Docker socket, runs no privileged daemon and does not run as root, or make this pool so.",
		Verify:  "Press Recheck after the pool's settings change; the finding closes when every run of the repository in the window landed on a pool without those settings.",
		Docs:    docsAnchor(CodePublicRepoWeakPool),
		eval:    evalPublicRepoWeakPool,
	},
	{
		Code: CodeForkCodeRan, Area: AreaExposure, Severity: SeverityError,
		Detects: "A run from a fork's pull request executed on this fleet.",
		Needs:   []Source{SourceFleet, SourceMetadata}, Conditional: []Source{SourceRuns},
		Fix:    "Require approval for workflows from fork pull requests in the repository's Actions settings, or stop routing its pull-request jobs to this fleet.",
		Verify: "Press Recheck after the setting changes; the finding closes once no run from a fork's pull request has executed here in the window.",
		Docs:   docsAnchor(CodeForkCodeRan),
		eval:   evalForkCodeRan,
	},
	{
		Code: CodeTargetEventRan, Area: AreaExposure, Severity: SeverityWarning,
		Detects: "A workflow that strangers can trigger (pull_request_target, workflow_run, issue_comment, issues) ran on this fleet in a public repository.",
		Needs:   []Source{SourceFleet, SourceMetadata}, Conditional: []Source{SourceRuns},
		Fix:    "Read the workflows these events trigger and make sure none of them checks out or executes the pull request's own code; if one must, run it on an isolated, ephemeral pool.",
		Verify: "Press Recheck once the workflow has been reviewed or moved; the finding closes when no run for those events has executed here in the window.",
		Docs:   docsAnchor(CodeTargetEventRan),
		eval:   evalTargetEventRan,
	},
	{
		Code: CodeTargetCheckoutPRHead, Area: AreaExposure, Severity: SeverityError,
		Detects: "A workflow that strangers can trigger (pull_request_target) checks out the pull request's own code, which then runs with the repository's token and secrets.",
		Needs:   []Source{SourceMetadata, SourceWorkflows},
		Fix:     "Do not check out the pull request's head under pull_request_target; read what the event carries, or run the code under pull_request, where it gets the fork's lesser token and no secrets.",
		Verify:  "Press Recheck after the change reaches the default branch; the finding closes when no pull_request_target workflow checks out the pull request's head.",
		Docs:    docsAnchor(CodeTargetCheckoutPRHead),
		eval:    evalTargetCheckoutPRHead,
	},
	{
		Code: CodeForkApprovalWeak, Area: AreaExposure, Severity: SeverityWarning,
		Detects: "A public repository this fleet serves asks for approval of fork pull requests only from contributors new to GitHub, so most outside contributors can start a job here.",
		Needs:   []Source{SourceFleet, SourceMetadata}, Conditional: []Source{SourceSettings},
		Fix:    "Require approval for workflows from all outside contributors in the repository's Actions settings, so a maintainer reads a fork's pull request before it runs here.",
		Verify: "Press Recheck after the setting changes; the finding closes when the approval policy covers every outside contributor.",
		Docs:   docsAnchor(CodeForkApprovalWeak),
		eval:   evalForkApprovalWeak,
	},
	{
		Code: CodePrivateForkSecrets, Area: AreaExposure, Severity: SeverityWarning,
		Detects: "A private repository lets fork pull requests run workflows and sends them secrets or a token that can write.",
		Needs:   []Source{SourceMetadata}, Conditional: []Source{SourceSettings},
		Fix:    "In the repository's Actions settings, stop sending secrets and write tokens to fork pull request workflows, or stop fork pull requests running workflows.",
		Verify: "Press Recheck after the setting changes; the finding closes when fork pull requests are sent neither secrets nor a token that can write.",
		Docs:   docsAnchor(CodePrivateForkSecrets),
		eval:   evalPrivateForkSecrets,
	},
	{
		Code: CodeUnservedLabel, Area: AreaCapacity, Severity: SeverityWarning,
		Detects: "Jobs waited more than ten minutes for a label no pool serves.",
		Needs:   []Source{SourceFleet},
		Fix:     "Add the label to a pool that can run the job, or change the workflow's runs-on to a label a pool serves.",
		Verify:  "Press Recheck after the pool or the workflow changes; the finding closes once no job has waited ten minutes for an unserved label in the last seven days.",
		Docs:    docsAnchor(CodeUnservedLabel),
		eval:    evalUnservedLabel,
	},
	{
		Code: CodeJobHitDefaultLimit, Area: AreaCapacity, Severity: SeverityWarning,
		Detects: "A job ran until GitHub stopped it at its six-hour default limit.",
		Needs:   []Source{SourceFleet},
		Fix:     "Set timeout-minutes on the job from its own usual duration, so a hung run is stopped in minutes rather than hours.",
		Verify:  "Press Recheck after the next run of the job; the finding closes once no run in the window was cancelled at the six-hour limit.",
		Docs:    docsAnchor(CodeJobHitDefaultLimit),
		eval:    evalJobHitDefaultLimit,
	},
	{
		Code: CodeMatrixExceedsPool, Area: AreaCapacity, Severity: SeverityInfo,
		Detects: "A matrix's jobs waited together on a pool with fewer runners than the matrix has jobs, so the matrix ran in waves.",
		Needs:   []Source{SourceFleet},
		Fix:     "Raise the pool's max_runners to the matrix's width, spread the matrix over more than one pool with runs-on, or cap it with max-parallel so the wait is chosen and not suffered.",
		Verify:  "Press Recheck after the pool or the matrix changes; the finding closes when no matrix in the window is wider than the pool it ran on.",
		Docs:    docsAnchor(CodeMatrixExceedsPool),
		eval:    evalMatrixExceedsPool,
	},
	setupCheck(CodeSetupReadme, "README", "People joining the repository have no repository-local starting point for building, testing or running it.", "Add a README with the project purpose, prerequisites and the commands to build, test and run it.", false),
	setupCheck(CodeSetupLicence, "licence", "People cannot tell from a repository-local licence how they may use or redistribute this public project.", "Choose an appropriate licence with the project owner and record it in a root licence file.", true),
	setupCheck(CodeSetupSecurity, "security policy", "There is no repository-local route for privately reporting a vulnerability; an account default may provide one.", "Add SECURITY.md with a private reporting route and supported versions, or confirm that the account default provides them.", false),
	setupCheck(CodeSetupContributing, "contribution guide", "Contributors have no repository-local explanation of the development and review process; an account default may provide one.", "Add CONTRIBUTING.md with setup, test and pull request guidance, or confirm that the account default provides it.", true),
	setupCheck(CodeSetupCodeOfConduct, "code of conduct", "This public project has no repository-local participation and reporting guidance; an account default may provide it.", "Add CODE_OF_CONDUCT.md with an enforcement contact, or confirm that the account default provides it.", true),
	setupCheck(CodeSetupIssueTemplate, "issue template", "Issue authors have no repository-local template to collect reproduction steps and environment details; account defaults may provide one.", "Add an issue form or template under .github/ISSUE_TEMPLATE, or confirm that account defaults provide one.", true),
	setupCheck(CodeSetupPullRequestTemplate, "pull request template", "Reviewers have no repository-local prompt for the purpose and validation of a change; an account default may provide one.", "Add a pull request template with a change summary and validation prompts, or confirm that the account default provides one.", false),
	setupCheck(CodeSetupCodeowners, "CODEOWNERS file", "There is no repository-local ownership file to route reviews of CI workflows and other maintained files.", "Add CODEOWNERS in .github, the repository root or docs and assign owners for the CI workflows; review enforcement separately.", false),
	setupCheck(CodeSetupDependencyUpdates, "dependency update configuration", "A recognised dependency manifest or GitHub Actions workflow is present, but no repository-local Dependabot or Renovate configuration was found; another service may manage updates.", "Configure Dependabot or Renovate for the package ecosystems and GitHub Actions, or confirm that an external service manages updates.", false),
	setupCheck(CodeSetupWorkflows, "CI workflow", "No GitHub Actions workflow file was found on the default branch, so changes have no repository-local Actions validation.", "Add a workflow under .github/workflows that runs the relevant build and tests, or confirm that CI is provided elsewhere.", false),
	workflowCheck(CodeNoTimeout, AreaCI, SeverityWarning, "Executable jobs have no timeout-minutes.",
		"Set timeout-minutes on each executable job in the default-branch workflows, from its usual duration with a margin for a slow run.",
		"Press Recheck after the change reaches the default branch; the finding closes when every executable job declares a timeout.", evalNoTimeout),
	workflowCheck(CodeNoConcurrency, AreaCI, SeverityInfo, "Pull-request-only workflows do not cancel superseded runs at workflow or job level.",
		"Where a superseded pull-request run may be cancelled, add a concurrency group scoped to the workflow and the pull request with cancel-in-progress on.",
		"Press Recheck after the change reaches the default branch; the finding closes when every pull-request-only workflow cancels superseded runs.", evalNoConcurrency),
	workflowCheck(CodeActionNotPinned, AreaCI, SeverityWarning, "External actions or reusable workflows use mutable refs, or Docker actions use no image digest.",
		"Pin every external action and reusable workflow to a reviewed full commit, and every Docker action to an image digest, and let an updater move the pins.",
		"Press Recheck after the change reaches the default branch; the finding closes when no external reference is a tag or a branch.", evalActionNotPinned),
	workflowCheck(CodePermissionsUnset, AreaToken, SeverityWarning, "Jobs inherit token permissions without a declaration at workflow or job level.",
		"Declare the least permissions each workflow or job needs, after reading what its actions and publishing steps use.",
		"Press Recheck after the change reaches the default branch; the finding closes when every job has a permissions block of its own or its workflow's.", evalPermissionsUnset),
	{
		Code: CodeDefaultTokenWrite, Area: AreaToken, Severity: SeverityWarning,
		Detects: "The repository's default workflow token can write, so every workflow that sets no permissions runs with write access.",
		Needs:   []Source{SourceSettings},
		Fix:     "Set the default workflow permissions to read-only in the repository's Actions settings, then declare write permissions only on the workflows or jobs that need them.",
		Verify:  "Press Recheck after the setting changes; the finding closes when the default workflow token is read-only.",
		Docs:    docsAnchor(CodeDefaultTokenWrite),
		eval:    evalDefaultTokenWrite,
	},
	workflowCheck(CodeWorkflowUnreadable, AreaCI, SeverityWarning, "A workflow file could not be read within Kennel Club's limits, so nothing in it was judged.",
		"Bring the file within the limits: under 256 KiB, no YAML anchors, aliases or merge keys, no duplicate keys, one document, valid UTF-8 and a jobs mapping; or split it into smaller workflows.",
		"Press Recheck after the change reaches the default branch; the finding closes when the file is read and judged.", evalWorkflowUnreadable),
	{
		Code: CodePinsWithoutUpdater, Area: AreaCI, Severity: SeverityInfo,
		Detects: "Actions are pinned to commits but no updater configuration moves the pins, so they age until somebody remembers.",
		Needs:   []Source{SourceMetadata, SourceWorkflows}, Conditional: []Source{SourceSetup},
		Fix:    "Configure Dependabot or Renovate for GitHub Actions so the pinned commits are moved by pull request, or confirm that an external service moves them.",
		Verify: "Press Recheck once the configuration is on the default branch; the finding closes when the next tree read finds it.",
		Docs:   docsAnchor(CodePinsWithoutUpdater),
		eval:   evalPinsWithoutUpdater,
	},
	workflowCheck(CodeLabelUnserved, AreaCI, SeverityInfo, "A job's runs-on names labels no pool here serves, so the job will wait until a pool matches it.",
		"Change the job's runs-on to labels a pool serves, or add the label to a pool that can run the job.",
		"Press Recheck after the pool or the workflow changes; the finding closes when every job's runs-on is served.", evalLabelUnserved),
	workflowCheck(CodeSecretOnCommandLine, AreaCI, SeverityWarning, "A secret is interpolated into a run line or a command-line argument, where it reaches the process list and the log.",
		"Pass the secret through the step's env block and read it from the environment in the command; never interpolate it into run or args.",
		"Press Recheck after the change reaches the default branch; the finding closes when no run line or args interpolates a secret.", evalSecretOnCommandLine),
	{
		Code: CodeRequiredCheckNeverReports, Area: AreaProtection, Severity: SeverityWarning,
		Detects: "A status check that GitHub Actions should post is required to merge, but no job this fleet saw in the window produced it.",
		Needs:   []Source{SourceFleet, SourceProtection},
		Fix:     "Rename the required check to the name of the job that posts it, or remove it from the branch protection or ruleset if no job posts it any more.",
		Verify:  "Press Recheck after the required checks change; the finding closes when every required check pinned to GitHub Actions has a job of that name in the window.",
		Docs:    docsAnchor(CodeRequiredCheckNeverReports),
		eval:    evalRequiredCheckNeverReports,
	},
}

// Checks returns the registry. The caller may keep and change the slice.
func Checks() []Check {
	out := make([]Check, len(checks))
	for i, c := range checks {
		c.Needs = append([]Source(nil), c.Needs...)
		c.Conditional = append([]Source(nil), c.Conditional...)
		out[i] = c
	}
	return out
}

// Lookup returns the check with this code.
func Lookup(c Code) (Check, bool) {
	for _, k := range Checks() {
		if k.Code == c {
			return k, true
		}
	}
	return Check{}, false
}

// Names lists every name kennel.disabled_checks accepts -- each area, then each
// code -- in registry order. The validator quotes it back to somebody who
// misspelt one, so the answer comes from the registry and not from a second
// list that could fall behind it.
func Names() []string {
	var out []string
	for _, c := range checks {
		if !slices.Contains(out, string(c.Area)) {
			out = append(out, string(c.Area))
		}
	}
	for _, c := range checks {
		out = append(out, string(c.Code))
	}
	return out
}

// KnownCodeOrArea says whether a name in kennel.disabled_checks means anything,
// so a misspelling is refused when it is saved rather than silently ignored.
func KnownCodeOrArea(name string) bool {
	for _, c := range checks {
		if string(c.Code) == name || string(c.Area) == name {
			return true
		}
	}
	return false
}
