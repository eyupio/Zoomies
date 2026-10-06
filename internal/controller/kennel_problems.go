package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/kennel"
	"github.com/eyupio/zoomies/internal/store"
)

// kennelUnavailableAfter is how long Kennel Club's reads of an installation must
// have been failing before it is a problem and not a bad hour. A held installation
// clears itself, a transient failure is retried within half an hour, and a
// problem that came and went before anybody could open the drawer is noise.
const kennelUnavailableAfter = 6 * time.Hour

// kennelProblems raises the two things Kennel Club can say on the problems list,
// and says nothing at all while it is off.
//
// Two, and no more, because the list is for what needs doing now. A finding that
// is a warning, or an error that a waiver covers, is on Kennel Club's own page
// and nowhere else: a problems drawer that carried every standard a repository
// falls short of would be a drawer nobody opened.
func (c *Controller) kennelProblems(ctx context.Context, out *[]Problem) error {
	if !c.cfg().Kennel.Enabled {
		return nil
	}
	if err := c.kennelExposureProblems(ctx, out); err != nil {
		return err
	}
	c.kennelUnavailableProblems(ctx, out)
	return nil
}

// kennelExposureProblems is one problem per repository with an open error among
// its exposure findings: a stranger can already run code on the fleet, or a pool
// that does is set up to be hurt by it.
//
// The sentences are the finding's own, written from templates with only numbers and
// enumerated words in them, so nothing a repository's author wrote reaches the
// drawer. The repository's name is the one GitHub reports, which is validated
// owner/name, and it is the same text the page puts in a heading.
func (c *Controller) kennelExposureProblems(ctx context.Context, out *[]Problem) error {
	for offset := 0; ; {
		rows, total, err := c.st.ListKennelRepositories(ctx,
			store.KennelFilter{WithErrors: true}, store.Page{Limit: 100, Offset: offset})
		if err != nil {
			return fmt.Errorf("listing the repositories with open errors: %w", err)
		}
		for _, r := range rows {
			errs := exposureErrors(newKennelRepositoryView(r).Findings)
			if len(errs) == 0 {
				continue
			}
			worst := errs[0]
			detail := worst.Detail
			if len(errs) > 1 {
				detail += fmt.Sprintf(" %s open for this repository as well.", plural(len(errs)-1, "other exposure error")+isAre(len(errs)-1))
			}
			*out = append(*out, Problem{
				Code: "kennel.exposure", Severity: config.SeverityError,
				Title:  r.FullName + ": " + worst.Title,
				Detail: detail,
				Fix:    worst.Fix + " Kennel Club lists every finding for the repository, and is where one is waived with a reason.",
				// The repository's page, which is where the findings, their
				// evidence and the waive button are.
				TargetKind: "kennel_repository", TargetID: r.ID,
			})
		}
		offset += len(rows)
		if len(rows) == 0 || offset >= total {
			return nil
		}
	}
}

// exposureErrors are the findings that make a repository a problem: open errors
// in the exposure area, worst first as the evaluator ordered them. An error in
// another area -- storage, a token default -- is Kennel Club's to show and is not
// a stranger's code running on the fleet, so it does not raise this code.
func exposureErrors(findings []kennel.Finding) []kennel.Finding {
	var out []kennel.Finding
	for _, f := range findings {
		if f.Severity == kennel.SeverityError && f.Code.Area() == kennel.AreaExposure {
			out = append(out, f)
		}
	}
	return out
}

func isAre(n int) string {
	if n == 1 {
		return " is"
	}
	return " are"
}

// kennelUnavailableProblems says that Kennel Club has not been able to read an
// installation's repositories for six hours, for a reason that is not a choice.
//
// A permission the operator has not granted is never here: it is a state of
// coverage, shown on the repositories it affects and naming the permission, and an
// operator who declined it has not been wronged and must not be nagged. What is
// here is the listing of repositories being refused -- which every App is allowed
// to do, so it means the installation was suspended or changed -- and repeated
// transport failures and rate limits.
func (c *Controller) kennelUnavailableProblems(ctx context.Context, out *[]Problem) {
	now := c.Now()
	for _, n := range c.kennelNotes(ctx) {
		if now.Sub(n.Since) < kennelUnavailableAfter {
			continue
		}
		since := n.Since
		*out = append(*out, Problem{
			Code: "kennel.unavailable", Severity: config.SeverityWarning,
			Title: "Kennel Club cannot read the repositories on " + n.Target,
			Detail: fmt.Sprintf("%s It has not got through since %s, so what it says about those repositories is as old as that.",
				n.Reason, since.UTC().Format("2 Jan 15:04 MST")),
			Fix:        kennelUnavailableFix(n.State),
			TargetKind: "installation", TargetID: n.InstallationID, Since: &since,
		})
	}
}

// kennelUnavailableFix says what to do about each way the reads can fail.
func kennelUnavailableFix(state kennel.CoverageState) string {
	switch state {
	case kennel.CoverageHeld:
		return "Check what else this installation is spending its GitHub requests on: the Overview's request counter and zoomies_github_api_requests_total say which installation, and lowering kennel.api_budget_percent gives the scheduler more of them."
	case kennel.CoverageDenied:
		return "GitHub refused to list the repositories, which every installation is allowed to do, so the App may have been suspended, uninstalled or had its permissions changed. Verify the installation on the Installations page."
	case kennel.CoverageUnavailable:
		return "This installation's client cannot read repositories. Verify the installation on the Installations page."
	}
	return "Check the controller log for the line beginning \"Kennel Club could not\", which says what GitHub or the network answered, and verify the installation on the Installations page."
}
