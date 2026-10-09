package kennel

// GuidanceFacts carries identities and enumerated problems only. Repository
// prose is discarded before entering the evaluator or its stored findings.
type GuidanceFacts struct {
	Missing    bool               `json:"missing"`
	Broken     []GuidanceLocation `json:"broken"`
	Duplicated []GuidanceLocation `json:"duplicated"`
	Unreadable []GuidanceLocation `json:"unreadable"`
}

type GuidanceLocation struct {
	SHA  string `json:"sha"`
	Line int    `json:"line"`
}

func guidanceCheck(code Code, severity Severity, title, detail, fix string) Check {
	return Check{Code: code, Area: AreaGuidance, Severity: severity, Detects: detail, Fix: fix,
		Verify: "Press Recheck after the change reaches the default branch; the finding closes when the guidance is read without this problem.",
		Docs:   docsAnchor(code), Needs: []Source{SourceGuidance},
		eval: func(s *Snapshot) result {
			r := result{applies: true}
			if s.Guidance == nil {
				r.incomplete = true
				return r
			}
			var locations []GuidanceLocation
			switch code {
			case CodeGuidanceMissing:
				if s.Guidance.Missing && s.Coverage.state(SourceGuidance) == CoverageOK {
					r.findings = []Finding{{Code: code, Severity: severity, Title: title, Detail: detail, Fix: fix}}
				}
			case CodeGuidanceBroken:
				locations = s.Guidance.Broken
			case CodeGuidanceDuplicated:
				locations = s.Guidance.Duplicated
			case CodeGuidanceUnreadable:
				locations = s.Guidance.Unreadable
			}
			// Several broken links in one file are one finding and one waiver.
			seen := map[string]bool{}
			for _, loc := range locations {
				if seen[loc.SHA] {
					continue
				}
				seen[loc.SHA] = true
				r.findings = append(r.findings, Finding{Code: code, Severity: severity, Subject: subjectOf(loc.SHA), Title: title, Detail: detail, Fix: fix, Evidence: []Evidence{fileEvidence(loc.SHA, Location{JobIndex: -1, Line: loc.Line})}})
			}
			return r
		}}
}
