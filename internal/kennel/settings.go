package kennel

import (
	"strconv"
	"strings"
)

// ProtectionJobFloor is how many jobs the fleet must have seen for a repository
// before a required check that none of them produced is called missing. A
// repository the fleet has barely served, or a webhook delivery gap, looks
// exactly like a check that never reports, and fewer than this is "unknown".
const ProtectionJobFloor = 20

// ForkApproval is the approval policy GitHub applies to a fork pull request from
// an outside contributor on a public repository. The controller maps the word
// GitHub sent onto one of these, and onto the empty value for any it does not
// know, so no other string ever reaches the evaluator.
type ForkApproval string

const (
	// ApprovalNewToGitHub asks for approval only when the author is new to
	// GitHub, which leaves every other outside contributor free to run code.
	ApprovalNewToGitHub ForkApproval = "first_time_contributors_new_to_github"
	// ApprovalFirstTime asks for approval from anyone who has not contributed
	// to the repository before.
	ApprovalFirstTime ForkApproval = "first_time_contributors"
	// ApprovalAll asks for approval from every outside contributor.
	ApprovalAll ForkApproval = "all_external_contributors"
)

// SettingsFacts are the repository's Actions settings that Kennel Club judges,
// reduced to flags and one enumerated word.
type SettingsFacts struct {
	// DefaultTokenWrite is true when the default workflow token can write.
	DefaultTokenWrite bool
	// ForkApproval is the approval policy of a public repository, or empty when
	// it was not read or GitHub's word for it is not one Kennel Club knows.
	ForkApproval ForkApproval
	// PrivateFork is what a private repository does with fork pull requests, or
	// nil when it was not read.
	PrivateFork *PrivateForkFacts
}

// PrivateForkFacts is a private repository's policy for fork pull requests.
type PrivateForkFacts struct {
	// Runs is true when fork pull requests run workflows at all.
	Runs bool
	// Secrets is true when they are given the repository's secrets and variables.
	Secrets bool
	// WriteToken is true when they are given a token that can write.
	WriteToken bool
}

// ProtectionFacts are the required status checks of the default branch, as
// counts. The names a repository chose for its checks stay with the controller,
// which compares them with the names of the jobs the fleet saw: nothing here can
// repeat one.
type ProtectionFacts struct {
	// Required is how many required checks pinned to the GitHub Actions app were
	// judged.
	Required int
	// NeverReported is how many of those no job the fleet saw in the window
	// produced.
	NeverReported int
	// Unpinned is how many more required checks were left unjudged because any
	// app may post them, so a job the fleet did not see could have.
	Unpinned int
	// JobsSeen is how many jobs the fleet saw for the repository in the window,
	// hosted or not.
	JobsSeen int
}

func evalDefaultTokenWrite(s *Snapshot) result {
	if s.Settings == nil {
		return result{applies: true, incomplete: true}
	}
	if !s.Settings.DefaultTokenWrite {
		return result{applies: true}
	}
	return result{applies: true, findings: []Finding{{
		Code:     CodeDefaultTokenWrite,
		Severity: SeverityWarning,
		Title:    "The default workflow token can write",
		Detail: "A workflow or job that sets no permissions runs with a token that can change the repository's contents, issues and pull requests, " +
			"so a compromised step or action in any of them can do the same.",
		Fix: "Set the default workflow permissions to read-only in the repository's Actions settings, " +
			"then give each workflow or job the write permissions it needs, where it needs them.",
	}}}
}

func evalForkApprovalWeak(s *Snapshot) result {
	if !isPublic(s) {
		return result{}
	}
	// Only a repository this fleet serves has anything at stake here: the
	// question is whether a stranger's pull request can start a job on it.
	n := s.Fleet.Jobs.Ran + s.Fleet.Jobs.Queued
	if n == 0 {
		return result{applies: true}
	}
	r := result{applies: true, extra: []Source{SourceSettings}}
	if s.Settings == nil {
		r.incomplete = true
		return r
	}
	switch s.Settings.ForkApproval {
	case ApprovalNewToGitHub:
	case ApprovalFirstTime, ApprovalAll:
		return r
	default:
		// Not read, or a word this version does not know: unjudged, and never
		// taken for a strong policy.
		r.incomplete = true
		return r
	}
	r.findings = []Finding{{
		Code:     CodeForkApprovalWeak,
		Severity: SeverityWarning,
		Title:    "Fork pull requests run without approval for most outside contributors",
		Detail: "This public repository asks for approval before a fork's pull request runs only when its author is new to GitHub, " +
			"so anyone with an older account can start a job on this fleet by opening one. " +
			"In the last " + forDays(s.Fleet.Window) + ", this fleet ran or queued " + count(n, "job", "jobs") + " for it.",
		Fix: "Require approval for all outside contributors in the repository's Actions settings, " +
			"so a maintainer reads a fork's pull request before it runs on a self-hosted runner.",
	}}
	return r
}

func evalPrivateForkSecrets(s *Snapshot) result {
	if s.Repo.Visibility != VisibilityPrivate && s.Repo.Visibility != VisibilityInternal {
		return result{}
	}
	r := result{applies: true, extra: []Source{SourceSettings}}
	if s.Settings == nil || s.Settings.PrivateFork == nil {
		r.incomplete = true
		return r
	}
	pf := s.Settings.PrivateFork
	if !pf.Runs || (!pf.Secrets && !pf.WriteToken) {
		return r
	}
	var sent []string
	if pf.Secrets {
		sent = append(sent, "its secrets and variables")
	}
	if pf.WriteToken {
		sent = append(sent, "a token that can write")
	}
	r.findings = []Finding{{
		Code:     CodePrivateForkSecrets,
		Severity: SeverityWarning,
		Title:    "Fork pull requests can run with secrets or a write token",
		Detail: "This repository lets pull requests from forks run workflows, and sends them " + strings.Join(sent, " and ") + ". " +
			"Anyone who can open such a pull request can use what they are sent by editing a workflow in it.",
		Fix: "Stop sending secrets and write tokens to fork pull request workflows in the repository's Actions settings, " +
			"or stop fork pull requests running workflows at all.",
	}}
	return r
}

func evalRequiredCheckNeverReports(s *Snapshot) result {
	r := result{applies: true}
	p := s.Protection
	if p == nil {
		r.incomplete = true
		return r
	}
	if p.Required == 0 {
		return r
	}
	// Fewer jobs than the floor cannot tell a check nobody produces from a window
	// the fleet barely saw, so it is unknown and says so rather than clear.
	if p.JobsSeen < ProtectionJobFloor {
		r.incomplete = true
		return r
	}
	if p.NeverReported == 0 {
		return r
	}
	// The sentence agrees with one check, with all of several, and with some of
	// several, because "1 of the 1 status check" is not English.
	posted := " by any of the " + count(p.JobsSeen, "job", "jobs") + " this fleet saw for it in the last " + forDays(s.Fleet.Window) + ". "
	const kind = " status checks this repository requires before merging, and that GitHub Actions should post, "
	var detail string
	switch {
	case p.Required == 1:
		detail = "The status check this repository requires before merging, and that GitHub Actions should post, was not posted" + posted
	case p.NeverReported == p.Required:
		detail = "None of the " + strconv.Itoa(p.Required) + kind + "was posted" + posted
	default:
		detail = strconv.Itoa(p.NeverReported) + " of the " + strconv.Itoa(p.Required) + kind + wasWere(p.NeverReported) + " not posted" + posted
	}
	detail += "A pull request cannot merge while a required check is missing, unless the rule is bypassed."
	if p.Unpinned > 0 {
		detail += " " + count(p.Unpinned, "more required check", "more required checks") + " can be posted by any app, and " + wasWere(p.Unpinned) + " not judged."
	}
	r.findings = []Finding{{
		Code:     CodeRequiredCheckNeverReports,
		Severity: SeverityWarning,
		Title:    "Required checks that no job produces",
		Detail:   detail,
		Fix: "Rename each required check to the name of the job that posts it, " +
			"or remove it from the branch protection or ruleset if no job posts it any more.",
	}}
	return r
}

func wasWere(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}
