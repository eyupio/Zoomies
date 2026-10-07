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
// falls short of would be a drawer nobody opened. The same goes for the
// repositories themselves: the drawer says that some are exposed and Kennel Club
// says which.
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

// kennelExposureProblems is one problem for the whole fleet, however many
// repositories have an open error among their exposure findings: a stranger can
// already run code on the fleet, or a pool that does is set up to be hurt by it.
//
// One, and with no repository in it, because Kennel Club is where each finding,
// its evidence and the waive button are, and a drawer that listed the repositories
// as well would say it twice and grow with the fleet. What the entry carries is a
// count and the way to the list, which is also why nothing a repository's author
// wrote can reach the drawer.
//
// Its identity is the same whatever the count. The drawer dismisses by code and
// target, so a dismissal holds while errors stay open and is forgotten when they
// all clear, as it is for every other problem; a target per repository was how
// each one could be muted alone, and is what this gives up.
func (c *Controller) kennelExposureProblems(ctx context.Context, out *[]Problem) error {
	exposed := 0
	for offset := 0; ; {
		rows, total, err := c.st.ListKennelRepositories(ctx,
			store.KennelFilter{Severity: "error"}, store.Page{Limit: 100, Offset: offset})
		if err != nil {
			return fmt.Errorf("listing the repositories with open errors: %w", err)
		}
		for _, r := range rows {
			if len(exposureErrors(newKennelRepositoryView(r).Findings)) > 0 {
				exposed++
			}
		}
		offset += len(rows)
		if len(rows) == 0 || offset >= total {
			break
		}
	}
	if exposed == 0 {
		return nil
	}
	title := "Kennel Club: 1 repository has an exposure error open"
	if exposed != 1 {
		title = fmt.Sprintf("Kennel Club: %d repositories have an exposure error open", exposed)
	}
	*out = append(*out, Problem{
		Code: "kennel.exposure", Severity: config.SeverityError,
		Title: title,
		Detail: "Code from a stranger's pull request has run on your runners, or a public repository's jobs ran on a pool " +
			"set up so that such code could reach the host.",
		Fix: "Open Kennel Club to see which repositories, what each finding is, the evidence behind it and what to change. " +
			"It is also where one is waived with a reason.",
		// Kennel Club's list, which the UI narrows to the repositories with an
		// error. It has no ID: there is one of these.
		TargetKind: "kennel",
	})
	return nil
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
