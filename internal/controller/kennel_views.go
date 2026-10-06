package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/kennel"
	"github.com/eyupio/zoomies/internal/store"
)

// The views below are the JSON of Kennel Club's resources. The API's handlers
// alias them and the event stream renders the same ones, so a frame dropped
// straight into the UI's cache is the shape a fetch would have returned: a
// kennel.updated that carried a bare store row, with no counts and no coverage
// words, would repaint a repository wrong.

// KennelSources is the order a repository's sources are listed in.
var KennelSources = []kennel.Source{kennel.SourceFleet, kennel.SourceMetadata, kennel.SourceRuns}

// KennelCoverageView is how far one source could be read, with the sentence that
// says why when it could not, and the permission that would fix it.
type KennelCoverageView struct {
	Source     kennel.Source        `json:"source"`
	Label      string               `json:"label"`
	State      kennel.CoverageState `json:"state"`
	Reason     string               `json:"reason"`
	Permission string               `json:"permission"`
}

// KennelSkippedView is a check that did not run, and why.
type KennelSkippedView struct {
	Code   kennel.Code          `json:"code"`
	Source kennel.Source        `json:"source"`
	State  kennel.CoverageState `json:"state"`
	Reason string               `json:"reason"`
}

// KennelRepositoryView is one repository and what Kennel Club last concluded
// about it. It is what GET /kennel/repositories/{id} returns, an item in the
// list, and the payload of kennel.updated.
type KennelRepositoryView struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	RepositoryID   int64  `json:"repository_id"`
	InstallationID string `json:"installation_id"`
	Visibility     string `json:"visibility"`
	// State is pending until a first evaluation lands, and then the evaluator's
	// own answer.
	State       kennel.State `json:"state"`
	EvaluatedAt *time.Time   `json:"evaluated_at"`
	// NextDueAt is when the reads from GitHub are next due. Null means they are
	// due now.
	NextDueAt *time.Time    `json:"next_due_at"`
	Counts    kennel.Counts `json:"counts"`
	// Complete is whether every enabled check ran against everything it needs.
	Complete bool                   `json:"complete"`
	Coverage []KennelCoverageView   `json:"coverage"`
	Findings []kennel.Finding       `json:"findings"`
	Waived   []kennel.WaivedFinding `json:"waived"`
	// Lapsed are waivers that match an open finding and no longer cover it,
	// because they ended or the finding got worse.
	Lapsed   []kennel.Waiver     `json:"lapsed"`
	Skipped  []KennelSkippedView `json:"skipped"`
	Disabled []kennel.Code       `json:"disabled"`
}

// newKennelRepositoryView renders a stored row. It reads nothing but the row, so
// the page and the stream cannot disagree about what it said.
func newKennelRepositoryView(r *store.KennelRepository) KennelRepositoryView {
	v := KennelRepositoryView{
		ID: r.ID, Name: r.FullName, RepositoryID: r.RepositoryID, InstallationID: r.InstallationID,
		Visibility: r.Visibility, State: kennel.State(r.State), EvaluatedAt: r.EvaluatedAt,
	}
	// A due time of zero is stored for "now", and the store reads it back as the
	// start of 1970 and not as a zero time, so the test is for a real date. Null
	// is how the page says the reads are due.
	if r.NextDueAt.UnixMilli() > 0 {
		due := r.NextDueAt
		v.NextDueAt = &due
	}
	// A document that does not parse is a repository nothing has evaluated yet.
	// The store refuses to keep one that is not JSON, so this is a row from
	// before an evaluation landed, whose documents are empty objects.
	var ev kennel.Evaluation
	_ = json.Unmarshal(r.Evaluation, &ev)
	var cov kennel.Coverage
	_ = json.Unmarshal(r.Coverage, &cov)

	v.Counts = kennel.Counts{Error: r.OpenErrors, Warning: r.OpenWarnings, Info: r.OpenInfos, Waived: r.Waived}
	v.Complete = ev.Complete
	v.Findings = nonNilSlice(ev.Findings)
	v.Waived = nonNilSlice(ev.Waived)
	v.Lapsed = nonNilSlice(ev.Lapsed)
	v.Disabled = nonNilSlice(ev.Disabled)
	v.Skipped = make([]KennelSkippedView, 0, len(ev.Skipped))
	for _, s := range ev.Skipped {
		v.Skipped = append(v.Skipped, KennelSkippedView{Code: s.Code, Source: s.Source, State: s.State, Reason: s.Reason()})
	}
	v.Coverage = make([]KennelCoverageView, 0, len(cov))
	for _, src := range KennelSources {
		st, ok := cov[src]
		if !ok {
			continue
		}
		v.Coverage = append(v.Coverage, KennelCoverageView{
			Source: src, Label: src.Label(), State: st.State,
			Reason: kennel.Explain(src, st.State), Permission: src.Permission(),
		})
	}
	return v
}

func nonNilSlice[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}

// KennelCheckView is one entry in the Overview's by-check table.
type KennelCheckView struct {
	Code     kennel.Code     `json:"code"`
	Area     kennel.Area     `json:"area"`
	Severity kennel.Severity `json:"severity"`
	Detects  string          `json:"detects"`
	// Repositories is how many have this check open.
	Repositories int `json:"repositories"`
	// Disabled says the operator turned it off, by its code or its area, so the
	// page shows it as off in Settings and not as clear.
	Disabled bool `json:"disabled"`
}

// KennelStateCounts is how many repositories are in each standing.
type KennelStateCounts struct {
	Pending    int `json:"pending"`
	Partial    int `json:"partial"`
	Attention  int `json:"attention"`
	BestInShow int `json:"best_in_show"`
}

// KennelAttentionView is a repository that needs looking at, as a line in the
// Overview's list.
type KennelAttentionView struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Visibility string        `json:"visibility"`
	State      kennel.State  `json:"state"`
	Counts     kennel.Counts `json:"counts"`
}

// KennelCoverageSummary is how many repositories are in each state of reading
// one source: the numbers behind the Overview's coverage panel.
type KennelCoverageSummary struct {
	Source     kennel.Source  `json:"source"`
	Label      string         `json:"label"`
	Permission string         `json:"permission"`
	States     map[string]int `json:"states"`
}

// KennelInstallationNote says an installation's reads from GitHub are not
// getting through, which is the one thing a per-repository coverage state
// cannot say: a repository nothing has been read for has no row to say it on.
type KennelInstallationNote struct {
	InstallationID string               `json:"installation_id"`
	Target         string               `json:"target"`
	State          kennel.CoverageState `json:"state"`
	Reason         string               `json:"reason"`
	Since          time.Time            `json:"since"`
}

// KennelOverviewView is the Overview's document. It is what GET /kennel returns
// and what kennel.summary carries, computed rather than stored.
type KennelOverviewView struct {
	// Enabled is false when Kennel Club is off, and then nothing else is filled:
	// the page needs a 200 to render its explanation, and an off feature has
	// read nothing to count.
	Enabled bool   `json:"enabled"`
	Scope   string `json:"scope"`
	// Repositories is every one it has a row for.
	Repositories int               `json:"repositories"`
	States       KennelStateCounts `json:"states"`
	// Counts are the open findings across every repository, by severity, and the
	// waived ones beside them.
	Counts           kennel.Counts            `json:"counts"`
	Checks           []KennelCheckView        `json:"checks"`
	Attention        []KennelAttentionView    `json:"attention"`
	Coverage         []KennelCoverageSummary  `json:"coverage"`
	Unavailable      []KennelInstallationNote `json:"unavailable"`
	OldestEvaluation *time.Time               `json:"oldest_evaluation"`
	// DisabledChecks are the names the operator turned off, as written.
	DisabledChecks []string `json:"disabled_checks"`
}

// kennelAttentionLines is how many repositories the Overview names.
const kennelAttentionLines = 10

// kennelDisabled turns the setting into the set the evaluator is given.
func kennelDisabled(k config.Kennel) map[string]bool {
	if len(k.DisabledChecks) == 0 {
		return nil
	}
	out := make(map[string]bool, len(k.DisabledChecks))
	for _, name := range k.DisabledChecks {
		out[name] = true
	}
	return out
}

// KennelOverview computes the Overview's document. With Kennel Club off it
// reads nothing from the database, which is the property that lets "off" mean
// no rows and no work.
func (c *Controller) KennelOverview(ctx context.Context) (*KennelOverviewView, error) {
	cfg := c.cfg().Kennel
	out := &KennelOverviewView{
		Enabled: cfg.Enabled, Scope: cfg.Scope,
		Checks: []KennelCheckView{}, Attention: []KennelAttentionView{}, Coverage: []KennelCoverageSummary{},
		Unavailable: []KennelInstallationNote{}, DisabledChecks: nonNilSlice(slices.Clone(cfg.DisabledChecks)),
	}
	if !cfg.Enabled {
		return out, nil
	}
	counts, err := c.st.KennelCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("counting repositories: %w", err)
	}
	byCheck, err := c.st.KennelCheckCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("counting findings by check: %w", err)
	}
	byCoverage, err := c.st.KennelCoverageCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("counting coverage: %w", err)
	}
	worst, _, err := c.st.ListKennelRepositories(ctx, store.KennelFilter{States: []string{string(kennel.StateAttention)}}, store.Page{Limit: kennelAttentionLines})
	if err != nil {
		return nil, fmt.Errorf("listing the repositories that need attention: %w", err)
	}

	out.Repositories = counts.Repositories
	out.States = KennelStateCounts{
		Pending: counts.ByState[string(kennel.StatePending)], Partial: counts.ByState[string(kennel.StatePartial)],
		Attention: counts.ByState[string(kennel.StateAttention)], BestInShow: counts.ByState[string(kennel.StateBestInShow)],
	}
	out.Counts = kennel.Counts{Error: counts.OpenErrors, Warning: counts.OpenWarnings, Info: counts.OpenInfos, Waived: counts.Waived}
	out.OldestEvaluation = counts.OldestEvaluation

	disabled := kennelDisabled(cfg)
	for _, ck := range kennel.Checks() {
		out.Checks = append(out.Checks, KennelCheckView{
			Code: ck.Code, Area: ck.Area, Severity: ck.Severity, Detects: ck.Detects,
			Repositories: byCheck[string(ck.Code)],
			Disabled:     disabled[string(ck.Code)] || disabled[string(ck.Area)],
		})
	}
	for _, r := range worst {
		out.Attention = append(out.Attention, KennelAttentionView{
			ID: r.ID, Name: r.FullName, Visibility: r.Visibility, State: kennel.State(r.State),
			Counts: kennel.Counts{Error: r.OpenErrors, Warning: r.OpenWarnings, Info: r.OpenInfos, Waived: r.Waived},
		})
	}
	for _, src := range KennelSources {
		states := byCoverage[string(src)]
		if len(states) == 0 {
			continue
		}
		out.Coverage = append(out.Coverage, KennelCoverageSummary{
			Source: src, Label: src.Label(), Permission: src.Permission(), States: states,
		})
	}
	out.Unavailable = c.kennelNotes(ctx)
	return out, nil
}
