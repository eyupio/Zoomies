package kennel

import (
	"sort"
)

// WorkflowFile is what the parser said about one default-branch workflow,
// keyed by its blob SHA: positions and counts, never a path, a job name or a
// line of YAML. See internal/kennel/workflow for how each is decided.
type WorkflowFile struct {
	SHA                  string     `json:"sha"`
	NoTimeout            []Location `json:"no_timeout,omitempty"`
	NoConcurrency        *Location  `json:"no_concurrency,omitempty"`
	FirstPartyUnpinned   []Location `json:"first_party_unpinned,omitempty"`
	OtherUnpinned        []Location `json:"other_unpinned,omitempty"`
	PermissionsUnset     []Location `json:"permissions_unset,omitempty"`
	TargetCheckoutPRHead []Location `json:"target_checkout_pr_head,omitempty"`
	SecretOnCommandLine  []Location `json:"secret_on_command_line,omitempty"`
	// LabelUnserved is each job whose literal runs-on no pool serves, decided
	// by the controller, which knows the pools; the labels never come here.
	LabelUnserved []Location `json:"label_unserved,omitempty"`
	// Pinned is how many external references in the file are pinned to a
	// commit or an image digest.
	Pinned int `json:"pinned,omitempty"`
}

// WorkflowFacts are the default-branch workflow files as read, and the ones
// that could not be: a file the inventory skipped for its size or the parser
// refused, by SHA.
type WorkflowFacts struct {
	Files      []WorkflowFile `json:"files"`
	Unreadable []string       `json:"unreadable"`
}

func workflowCheck(code Code, area Area, severity Severity, detects, fix, verify string, eval func(*Snapshot) result) Check {
	return Check{Code: code, Area: area, Severity: severity, Detects: detects, Fix: fix, Verify: verify, Docs: docsAnchor(code), Needs: []Source{SourceMetadata, SourceWorkflows}, eval: func(s *Snapshot) result {
		r := eval(s)
		r.incomplete = s.Coverage.state(SourceWorkflows) == CoveragePartial
		return r
	}}
}

// perFile raises one finding per file with any of the locations pick returns:
// the file is the subject, the locations are the evidence, lowest line first
// and at most maxEvidence of them, and the detail carries their count.
func perFile(s *Snapshot, code Code, severity func(*Snapshot, *WorkflowFile) Severity, pick func(*WorkflowFile) []Location, title string, detail func(n int) string, fix string) result {
	r := result{applies: true}
	if s.Workflows == nil {
		return r
	}
	for i := range s.Workflows.Files {
		f := &s.Workflows.Files[i]
		locs := pick(f)
		if len(locs) == 0 {
			continue
		}
		sorted := append([]Location(nil), locs...)
		sort.Slice(sorted, func(a, b int) bool { return sorted[a].Line < sorted[b].Line })
		var ev []Evidence
		for _, loc := range sorted[:min(len(sorted), maxEvidence)] {
			ev = append(ev, fileEvidence(f.SHA, loc))
		}
		r.findings = append(r.findings, Finding{
			Code: code, Severity: severity(s, f), Subject: subjectOf(f.SHA),
			Title: title, Detail: detail(len(locs)), Fix: fix, Evidence: ev,
		})
	}
	return r
}

func fixed(sev Severity) func(*Snapshot, *WorkflowFile) Severity {
	return func(*Snapshot, *WorkflowFile) Severity { return sev }
}

func evalNoTimeout(s *Snapshot) result {
	return perFile(s, CodeNoTimeout, fixed(SeverityWarning),
		func(f *WorkflowFile) []Location { return f.NoTimeout },
		"Jobs have no explicit timeout",
		func(n int) string {
			return count(n, "executable job", "executable jobs") + " in this workflow " + has(n) + " no timeout-minutes; a hung job can hold a runner until the platform limit."
		},
		"Set timeout-minutes on the executable jobs in the default-branch workflows, using their observed duration and a margin for slower runs.")
}

func evalNoConcurrency(s *Snapshot) result {
	return perFile(s, CodeNoConcurrency, fixed(SeverityInfo),
		func(f *WorkflowFile) []Location {
			if f.NoConcurrency == nil {
				return nil
			}
			return []Location{*f.NoConcurrency}
		},
		"Pull request workflows do not cancel superseded runs",
		func(int) string {
			return "This pull-request-only workflow has no cancelling concurrency group at workflow level or on every job; superseded work can keep consuming runners."
		},
		"Review whether superseded pull request runs may be cancelled, then add a concurrency group scoped to the workflow and pull request with cancel-in-progress enabled.")
}

func evalActionNotPinned(s *Snapshot) result {
	return perFile(s, CodeActionNotPinned,
		func(_ *Snapshot, f *WorkflowFile) Severity {
			if len(f.OtherUnpinned) > 0 {
				return SeverityWarning
			}
			return SeverityInfo
		},
		func(f *WorkflowFile) []Location {
			return append(append([]Location(nil), f.FirstPartyUnpinned...), f.OtherUnpinned...)
		},
		"External actions use mutable references",
		func(n int) string {
			return count(n, "external reference", "external references") + " in this workflow " + is(n) + " not a full commit pin, or a Docker action without an image digest; the code it runs may change without a repository change."
		},
		"Pin external actions and reusable workflows to reviewed full commit SHAs and Docker actions to image digests; use an updater to maintain the pins.")
}

func evalPermissionsUnset(s *Snapshot) result {
	// A workflow that sets no permissions is only as strong as the repository's
	// default token. With that read from GitHub the check can say so: silent when
	// the default is read-only, and naming it when it can write. The settings are
	// looked at here and are not declared as a source this check needs, because a
	// declared source that is off, denied or unread would skip the check, and
	// what that case needs is the old answer, not silence.
	known := s.Settings != nil && s.Coverage.state(SourceSettings).readable()
	if known && !s.Settings.DefaultTokenWrite {
		return result{applies: true}
	}
	return perFile(s, CodePermissionsUnset,
		func(s *Snapshot, _ *WorkflowFile) Severity {
			if s.Repo.Visibility == VisibilityPublic {
				return SeverityWarning
			}
			return SeverityInfo
		},
		func(f *WorkflowFile) []Location { return f.PermissionsUnset },
		"Jobs inherit undeclared token permissions",
		func(n int) string {
			if known {
				return count(n, "job", "jobs") + " in this workflow " + has(n) + " no permissions block at job or workflow level and " + inherits(n) + " the repository's default token, which can write."
			}
			return count(n, "job", "jobs") + " in this workflow " + has(n) + " no permissions block at job or workflow level and " + inherits(n) + " the repository or organisation default; that default has not been checked."
		},
		"Declare the least token permissions each workflow or job needs, reviewing its actions and publishing steps before reducing access.")
}

// has, is and inherits agree a verb with a count, so a sentence about one
// job reads as well as one about three.
func has(n int) string {
	if n == 1 {
		return "has"
	}
	return "have"
}

func is(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func inherits(n int) string {
	if n == 1 {
		return "inherits"
	}
	return "inherit"
}

func evalTargetCheckoutPRHead(s *Snapshot) result {
	r := perFile(s, CodeTargetCheckoutPRHead,
		func(s *Snapshot, _ *WorkflowFile) Severity {
			// Only a stranger opens a pull request against a public repository;
			// on a private one the author is a member, and the checkout is a
			// weakness rather than an open door.
			if isPublic(s) {
				return SeverityError
			}
			return SeverityWarning
		},
		func(f *WorkflowFile) []Location { return f.TargetCheckoutPRHead },
		"A workflow strangers can trigger checks out their code",
		func(n int) string {
			return count(n, "step", "steps") + " in this pull_request_target workflow " + checksOut(n) + " the pull request's head. The workflow runs on the default branch with the repository's token and its secrets, and the checked-out code runs with them."
		},
		"Do not check out the pull request's head under pull_request_target: read what the event carries, or run the pull request's code under pull_request, where it gets the fork's lesser token and no secrets.")
	// The row's severity is the usual one; the finding's is the file's.
	return r
}

func evalWorkflowUnreadable(s *Snapshot) result {
	r := result{applies: true}
	if s.Workflows == nil {
		return r
	}
	for _, sha := range s.Workflows.Unreadable {
		r.findings = append(r.findings, Finding{
			Code: CodeWorkflowUnreadable, Severity: SeverityWarning, Subject: subjectOf(sha),
			Title:    "A workflow file could not be read",
			Detail:   "This workflow is over Kennel Club's limits or has a shape its parser will not judge in part, so nothing in it was checked: a finding it may hold is not reported, and a clean reading of the other files is not an all-clear.",
			Fix:      "Bring the file within the limits: under 256 KiB, no YAML anchors, aliases or merge keys, no duplicate keys, one document, valid UTF-8 and a jobs mapping; or split it into smaller workflows.",
			Evidence: []Evidence{fileEvidence(sha, Location{JobIndex: -1})},
		})
	}
	return r
}

// evalPinsWithoutUpdater needs the setup source, which says whether an updater
// is configured, so it is read only once the check applies, as the setup
// checks read it, and is skipped and says so when it was not read.
func evalPinsWithoutUpdater(s *Snapshot) result {
	r := result{applies: true, extra: []Source{SourceSetup}}
	if s.Workflows == nil || s.Setup == nil || s.Coverage.state(SourceSetup) != CoverageOK {
		return r
	}
	pinned := 0
	for _, f := range s.Workflows.Files {
		pinned += f.Pinned
	}
	if pinned == 0 || s.Setup.Present[CodeSetupDependencyUpdates] {
		return r
	}
	r.findings = []Finding{{
		Code: CodePinsWithoutUpdater, Severity: SeverityInfo,
		Title:  "Pinned actions have nothing to move the pins",
		Detail: count(pinned, "external reference is", "external references are") + " pinned to a commit or an image digest, and no Dependabot or Renovate configuration was found on the default branch, so the pins stay where they were put until somebody remembers them.",
		Fix:    "Configure Dependabot or Renovate for GitHub Actions so the pinned commits are moved by pull request, or confirm that an external service moves them.",
	}}
	return r
}

func evalLabelUnserved(s *Snapshot) result {
	return perFile(s, CodeLabelUnserved, fixed(SeverityInfo),
		func(f *WorkflowFile) []Location { return f.LabelUnserved },
		"Jobs ask for labels no pool serves",
		func(n int) string {
			return count(n, "job", "jobs") + " in this workflow " + names(n) + " a runs-on label no pool of this fleet serves; such a job waits until a pool matches it or somebody cancels it."
		},
		"Change the job's runs-on to labels a pool serves, or add the label to a pool that can run the job.")
}

func evalSecretOnCommandLine(s *Snapshot) result {
	return perFile(s, CodeSecretOnCommandLine, fixed(SeverityWarning),
		func(f *WorkflowFile) []Location { return f.SecretOnCommandLine },
		"A secret is put on a command line",
		func(n int) string {
			return count(n, "step", "steps") + " in this workflow " + interpolates(n) + " a secret into a run line or a command-line argument, where it can reach the process list and the log."
		},
		"Pass the secret through the step's env block and read it from the environment in the command; never interpolate it into run or args.")
}

func checksOut(n int) string {
	if n == 1 {
		return "checks out"
	}
	return "check out"
}

func names(n int) string {
	if n == 1 {
		return "names"
	}
	return "name"
}

func interpolates(n int) string {
	if n == 1 {
		return "interpolates"
	}
	return "interpolate"
}
