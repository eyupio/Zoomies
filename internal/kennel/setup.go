package kennel

// SetupFacts contains only recognised file-presence flags. Raw paths and file
// contents stay outside the evaluator, so they cannot become finding prose.
type SetupFacts struct {
	Present         map[Code]bool `json:"present"`
	HasDependencies bool          `json:"has_dependencies"`
}

func setupCheck(code Code, label, detail, fix string, publicOnly bool) Check {
	return Check{
		Code: code, Area: AreaSetup, Severity: SeverityInfo,
		Detects: "No repository-local " + label + " was found on the default branch.",
		Fix:     fix,
		Verify:  "Press Recheck once the file is on the default branch; the finding closes when the next tree read finds it.",
		Docs:    docsAnchor(code),
		Needs:   []Source{SourceMetadata}, Conditional: []Source{SourceSetup},
		eval: func(s *Snapshot) result {
			if publicOnly && s.Repo.Visibility != VisibilityPublic {
				return result{}
			}
			r := result{applies: true, extra: []Source{SourceSetup}}
			// Absence in a truncated tree is not evidence of a missing file.
			if s.Coverage.state(SourceSetup) != CoverageOK || s.Setup == nil {
				return r
			}
			if code == CodeSetupDependencyUpdates && !s.Setup.HasDependencies {
				return r
			}
			if !s.Setup.Present[code] {
				r.findings = []Finding{{Code: code, Severity: SeverityInfo,
					Title: "No repository-local " + label + " found", Detail: detail, Fix: fix}}
			}
			return r
		},
	}
}
