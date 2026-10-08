package kennel

import "strconv"

// WorkflowFacts are counts from bounded default-branch workflow reads. No job
// name, file path, expression or YAML text is retained in the snapshot.
type WorkflowFacts struct {
	Files              int `json:"files"`
	NoTimeout          int `json:"no_timeout"`
	NoConcurrency      int `json:"no_concurrency"`
	FirstPartyUnpinned int `json:"first_party_unpinned"`
	OtherUnpinned      int `json:"other_unpinned"`
	PermissionsUnset   int `json:"permissions_unset"`
}

func workflowCheck(code Code, area Area, severity Severity, detects string, eval func(*Snapshot) result) Check {
	return Check{Code: code, Area: area, Severity: severity, Detects: detects, Needs: []Source{SourceMetadata, SourceWorkflows}, eval: func(s *Snapshot) result {
		r := eval(s)
		r.incomplete = s.Coverage.state(SourceWorkflows) == CoveragePartial
		return r
	}}
}

func workflowFinding(code Code, severity Severity, count int, title, detail, fix string) result {
	r := result{applies: true}
	if count > 0 {
		r.findings = []Finding{{Code: code, Severity: severity, Title: title, Detail: strconv.Itoa(count) + " " + detail, Fix: fix}}
	}
	return r
}

func evalNoTimeout(s *Snapshot) result {
	n := 0
	if s.Workflows != nil {
		n = s.Workflows.NoTimeout
	}
	return workflowFinding(CodeNoTimeout, SeverityWarning, n, "Jobs have no explicit timeout", "executable job declarations have no timeout-minutes; a hung job can hold a runner until the platform limit.", "Set timeout-minutes on the executable jobs in the default-branch workflows, using their observed duration and a margin for slower runs.")
}
func evalNoConcurrency(s *Snapshot) result {
	n := 0
	if s.Workflows != nil {
		n = s.Workflows.NoConcurrency
	}
	return workflowFinding(CodeNoConcurrency, SeverityInfo, n, "Pull request workflows do not cancel superseded runs", "pull-request-only workflows have no cancelling concurrency group at workflow level or on every job; superseded work can keep consuming runners.", "Review whether superseded pull request runs may be cancelled, then add a concurrency group scoped to the workflow and pull request with cancel-in-progress enabled.")
}
func evalActionNotPinned(s *Snapshot) result {
	n := 0
	severity := SeverityInfo
	if s.Workflows != nil {
		n = s.Workflows.FirstPartyUnpinned + s.Workflows.OtherUnpinned
		if s.Workflows.OtherUnpinned > 0 {
			severity = SeverityWarning
		}
	}
	return workflowFinding(CodeActionNotPinned, severity, n, "External actions use mutable references", "external action or reusable-workflow references are not full commit pins, or Docker action references lack an image digest; the code they run may change without a repository change.", "Pin external actions and reusable workflows to reviewed full commit SHAs and Docker actions to image digests; use an updater to maintain the pins.")
}
func evalPermissionsUnset(s *Snapshot) result {
	n := 0
	if s.Workflows != nil {
		n = s.Workflows.PermissionsUnset
	}
	severity := SeverityInfo
	if s.Repo.Visibility == VisibilityPublic {
		severity = SeverityWarning
	}
	return workflowFinding(CodePermissionsUnset, severity, n, "Jobs inherit undeclared token permissions", "job declarations set no permissions at job or workflow level and inherit the repository or organisation default; that default has not been checked.", "Declare the least token permissions each workflow or job needs, reviewing its actions and publishing steps before reducing access.")
}
