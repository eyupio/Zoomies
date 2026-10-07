package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/kennel"
	"github.com/eyupio/zoomies/internal/store"
)

// Kennel Club's loop: what the fleet's repositories are held to, worked out
// from what the fleet observed and from what GitHub says about them.
//
// It runs in a goroutine of its own, and not as a step of Reconcile, for the
// reason the machine loop and the AI Context loop give: a pass of reads from
// GitHub is slow, and reconcileMu is held for a whole scheduling pass. It takes
// no lock the scheduler needs, and it never writes to GitHub: the only thing it
// is handed is a github.RepoReader, which has no method that writes.
//
// There are two tiers per repository. The local one needs no request: the fleet's
// own record of its jobs and pools, re-evaluated when it changes, at most every
// ten minutes. The remote one is the reads from GitHub, due once an interval
// (spread by a per-repository jitter, so a thousand repositories do not all come
// due in the same second), on a recheck, and when the evaluator's version
// changes.
//
// And the budget is a rule: see kennelBudget.

const (
	// kennelFirstPass is how long after startup the first pass runs. A restarting
	// controller does not need Kennel Club's requests in its first seconds.
	kennelFirstPass = 20 * time.Second
	// kennelTick is how often the loop looks for work. Looking costs a few local
	// queries; what is due is decided by each repository's own due time.
	kennelTick = time.Minute
	// kennelRetryHeld and kennelRetryError are how soon a read that was held back,
	// or that failed, is tried again. Waiting a whole interval to find out whether
	// a transient failure has gone would leave a repository half-read for a day.
	kennelRetryHeld  = 15 * time.Minute
	kennelRetryError = 30 * time.Minute
	// kennelUnwellBase and kennelUnwellMax bound how long Kennel Club leaves an
	// installation alone after GitHub answers one of its reads with a 5xx. The
	// wait doubles with each failure in a row, because a GitHub that is failing
	// is not helped by being asked every minute -- and the registration and
	// scaling paths share that API and have first call on it.
	kennelUnwellBase = 5 * time.Minute
	kennelUnwellMax  = time.Hour
)

// kennelRuntime is the loop's own state, apart from the controller's for the
// reason clients and queues are: the loop's state is the loop's.
//
// All of it is memory, and cheap to lose. A restart forgets what was spent this
// hour and which installations were failing, and the next pass finds both out
// again; what has to survive -- the last answer, the run watermark -- is in the
// database.
type kennelRuntime struct {
	mu      sync.Mutex
	budgets map[string]*kennelBudget
	notes   map[string]kennelNote
	// listed is when each installation's repositories were last listed, which is
	// what paces the listing under the installation scope.
	listed map[string]time.Time
	// unwell is the backoff after GitHub answered a read with a 5xx. It is
	// Kennel Club's own: the poller and scheduler's rate-limit hold is not set,
	// so a failing GitHub never stops a runner being registered by it.
	unwell map[string]kennelUnwell
	// rechecks is when each repository was last asked to be read again by a
	// person, which is what the cooldown on Recheck is counted from.
	rechecks map[string]time.Time
	// wake is capacity 1 like nudges: it is a flag, not a queue.
	wake chan struct{}
}

func newKennelRuntime() *kennelRuntime {
	return &kennelRuntime{
		budgets:  map[string]*kennelBudget{},
		notes:    map[string]kennelNote{},
		listed:   map[string]time.Time{},
		unwell:   map[string]kennelUnwell{},
		rechecks: map[string]time.Time{},
		wake:     make(chan struct{}, 1),
	}
}

// kennelUnwell is one installation's run of server errors.
type kennelUnwell struct {
	until   time.Time
	last    time.Time
	strikes int
}

// kennelNoteServerError starts or lengthens the backoff for an installation
// whose read GitHub answered with a 5xx. A failure more than an hour after the
// last one starts the count again, so one bad afternoon does not make the next
// blip cost an hour.
func (c *Controller) kennelNoteServerError(installationID string, now time.Time) {
	c.kennel.mu.Lock()
	defer c.kennel.mu.Unlock()
	u := c.kennel.unwell[installationID]
	if now.Sub(u.last) > kennelUnwellMax {
		u.strikes = 0
	}
	u.strikes++
	wait := kennelUnwellMax
	if u.strikes <= 4 {
		wait = min(kennelUnwellBase<<(u.strikes-1), kennelUnwellMax)
	}
	u.last, u.until = now, now.Add(wait)
	c.kennel.unwell[installationID] = u
	c.log.Warn("GitHub is failing Kennel Club's reads; standing down from it for a while",
		"installation", installationID, "until", u.until.UTC().Format(time.RFC3339), "failures", u.strikes)
}

// kennelNote is what the loop last found out about reaching one installation.
type kennelNote struct {
	// State is why the reads are not getting through.
	State kennel.CoverageState
	// Since is when they first stopped, and is kept while they go on failing.
	Since time.Time
}

// KickKennel asks the loop to look for work now, which a recheck and a change of
// setting do. It never blocks and never queues.
func (c *Controller) KickKennel() {
	select {
	case c.kennel.wake <- struct{}{}:
	default:
	}
}

// kennelSettingsChanged is whether a change of configuration is one the loop
// should act on at once.
func kennelSettingsChanged(a, b config.Kennel) bool {
	return a.Enabled != b.Enabled || a.Scope != b.Scope || a.RefreshInterval != b.RefreshInterval ||
		a.APIBudgetPercent != b.APIBudgetPercent || !slices.Equal(a.DisabledChecks, b.DisabledChecks)
}

func (c *Controller) kennelLoop(ctx context.Context) {
	wait := kennelFirstPass
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		case <-c.kennel.wake:
		}
		c.KennelPass(ctx)
		wait = kennelTick
	}
}

// kennelPassInput is what one pass works from, read once so that a setting
// changed in the middle of a pass takes effect on the next.
type kennelPassInput struct {
	now      time.Time
	window   time.Duration
	cfg      config.Kennel
	policy   kennel.Policy
	pools    map[string]*store.Pool
	interval time.Duration
}

// KennelPass is one look at every installation. With Kennel Club off it does
// nothing at all: no query, no request, no row.
//
// It is exported, as Reconcile is, so a test of the API can make a pass and read
// what the loop itself wrote, not a row it built to agree with the handler.
//
// A fenced controller, or one that has lost its lease, does no GitHub work and
// writes nothing: mayAct is asked before each installation is touched and before
// each repository is evaluated, which is where a pass could otherwise carry on
// for minutes after the answer changed. It is not asked up here as well, because
// a second copy of a guard that the first already makes is a copy no test can
// tell from the first.
func (c *Controller) KennelPass(ctx context.Context) {
	if !c.cfg().Kennel.Enabled || ctx.Err() != nil {
		return
	}
	in, err := c.kennelInput(ctx)
	if err != nil {
		c.log.Warn("Kennel Club could not read what a pass needs", "error", err)
		return
	}
	insts, err := c.st.ListInstallations(ctx)
	if err != nil {
		c.log.Warn("Kennel Club could not list the installations", "error", err)
		return
	}
	served, err := c.st.KennelServedRepos(ctx, in.now.Add(-in.window))
	if err != nil {
		c.log.Warn("Kennel Club could not list the repositories this fleet has served", "error", err)
		return
	}
	byInstallation := map[string][]store.KennelServed{}
	for _, s := range served {
		byInstallation[s.InstallationID] = append(byInstallation[s.InstallationID], s)
	}

	for _, inst := range insts {
		if ctx.Err() != nil || !c.mayAct() {
			return
		}
		c.kennelInstallation(ctx, inst, byInstallation[inst.ID], in)
	}
}

// kennelInput reads what a pass, or the evaluation of one repository, works
// from: the clock, the window, the operator's settings and the pools. It is read
// once and passed down, so a setting changed in the middle takes effect on the
// next.
func (c *Controller) kennelInput(ctx context.Context) (kennelPassInput, error) {
	cfg := c.cfg()
	in := kennelPassInput{
		now: c.Now(), window: kennelWindow(cfg.Retention.Jobs), cfg: cfg.Kennel,
		policy:   kennel.Policy{Disabled: kennelDisabled(cfg.Kennel)},
		interval: cfg.Kennel.RefreshInterval,
	}
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return in, fmt.Errorf("listing the pools: %w", err)
	}
	in.pools = make(map[string]*store.Pool, len(pools))
	for _, p := range pools {
		in.pools[p.ID] = p
	}
	return in, nil
}

// kennelListing is what a listing of an installation's repositories came to.
type kennelListing struct {
	// State is ok when the listing was read, and otherwise why it was not.
	State  kennel.CoverageState
	Repos  map[string]github.Repository
	Client github.Client
	Reader github.RepoReader
	Limit  github.RateLimit
}

func (c *Controller) kennelInstallation(ctx context.Context, inst *store.Installation, served []store.KennelServed, in kennelPassInput) {
	host, err := aiContextGitHubHost(inst.APIBaseURL)
	if err != nil {
		c.log.Warn("Kennel Club cannot name this installation's GitHub host", "installation", inst.ID, "error", err)
		c.kennelNoteResult(inst.ID, kennel.CoverageError, in.now)
		return
	}
	rows, err := c.kennelRows(ctx, inst.ID)
	if err != nil {
		c.log.Warn("Kennel Club could not read its repositories", "installation", inst.ID, "error", err)
		return
	}
	known := make(map[string]*store.KennelRepository, len(rows))
	anyDue := false
	for _, r := range rows {
		known[strings.ToLower(r.FullName)] = r
		if kennelDue(r, in.now) {
			anyDue = true
		}
	}
	missing := false
	for _, s := range served {
		if known[strings.ToLower(s.Repo)] == nil {
			missing = true
		}
	}

	// The listing is the one read every repository shares. It is made when a
	// repository has no row yet, when one is due, and under the installation scope
	// when a refresh interval has passed -- and not otherwise, which is why a
	// quiet fleet costs nothing.
	c.kennel.mu.Lock()
	listedAt := c.kennel.listed[inst.ID]
	c.kennel.mu.Unlock()
	wide := in.cfg.Scope == config.KennelScopeInstallation && in.now.Sub(listedAt) >= in.interval
	var listing *kennelListing
	if missing || anyDue || wide {
		listing = c.kennelList(ctx, inst, in)
		if listing.State == kennel.CoverageOK {
			c.kennel.mu.Lock()
			c.kennel.listed[inst.ID] = in.now
			c.kennel.mu.Unlock()
			rows = c.kennelTouch(ctx, inst, host, served, listing, in)
		}
	}

	installationState := kennel.CoverageOK
	if listing != nil {
		installationState = listing.State
	}
	for _, r := range rows {
		if ctx.Err() != nil || !c.mayAct() {
			return
		}
		got := c.kennelRefresh(ctx, inst, r, listing, in)
		if got == kennel.CoverageError {
			installationState = kennel.CoverageError
		}
	}
	// Only a pass that tried to read says anything about whether reading works:
	// one that found nothing due must not clear a failure it never looked at.
	if listing != nil {
		c.kennelNoteResult(inst.ID, installationState, in.now)
	}
}

// kennelDue is whether a repository's reads from GitHub are due: its time has
// come, or what evaluated it is not the evaluator that is running.
func kennelDue(r *store.KennelRepository, now time.Time) bool {
	return !r.NextDueAt.After(now) || r.EvaluatorVersion != kennel.Version
}

// kennelRows lists every repository Kennel Club has for an installation.
func (c *Controller) kennelRows(ctx context.Context, installationID string) ([]*store.KennelRepository, error) {
	var all []*store.KennelRepository
	for {
		page, total, err := c.st.ListKennelRepositories(ctx,
			store.KennelFilter{InstallationID: installationID}, store.Page{Limit: 500, Offset: len(all)})
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) == 0 || len(all) >= total {
			return all, nil
		}
	}
}

// kennelState says what a failed read means for how far a source was read.
func kennelState(err error) kennel.CoverageState {
	switch {
	case err == nil:
		return kennel.CoverageOK
	case errors.Is(err, github.ErrRateLimited):
		return kennel.CoverageHeld
	case errors.Is(err, github.ErrForbidden):
		return kennel.CoverageDenied
	}
	return kennel.CoverageError
}

// kennelRetryAfter is how soon a repository whose read ended in this state is
// tried again. Zero means the ordinary interval: a refused permission does not
// come back by asking sooner, and the Recheck button is for the day it is
// granted.
func kennelRetryAfter(state kennel.CoverageState) time.Duration {
	switch state {
	case kennel.CoverageHeld:
		return kennelRetryHeld
	case kennel.CoverageError:
		return kennelRetryError
	}
	return 0
}

// kennelJitter spreads the due times of the repositories one pass found, so a
// thousand of them do not all come due again in the same minute a day later. It
// is a function of the repository, so it is the same every time, and it never
// exceeds a tenth of the interval or an hour.
func kennelJitter(repositoryID int64, interval time.Duration) time.Duration {
	span := min(interval/10, time.Hour)
	seconds := int64(span / time.Second)
	if seconds <= 0 {
		return 0
	}
	return time.Duration((uint64(repositoryID)*2654435761)%uint64(seconds+1)) * time.Second
}

// kennelTake says whether one more request may be made of an installation. It
// is the one gate every read passes through, so the budget cannot be forgotten
// by a new kind of read.
func (c *Controller) kennelTake(installationID string, in kennelPassInput, rl github.RateLimit) bool {
	c.kennel.mu.Lock()
	defer c.kennel.mu.Unlock()
	if c.Now().Before(c.kennel.unwell[installationID].until) {
		return false
	}
	b := c.kennel.budgets[installationID]
	if b == nil {
		b = &kennelBudget{}
		c.kennel.budgets[installationID] = b
	}
	return b.take(c.Now(), in.cfg.APIBudgetPercent, rl)
}

// kennelSpend charges requests that were made beyond the one taken, such as the
// further pages of a listing.
func (c *Controller) kennelSpend(installationID string, n int) {
	if n <= 0 {
		return
	}
	c.kennel.mu.Lock()
	defer c.kennel.mu.Unlock()
	if b := c.kennel.budgets[installationID]; b != nil {
		b.spent += n
	}
}

// kennelList is everything that has to be true before a request may be made of
// an installation, and then the listing of its repositories: the installation is
// not being held, the budget allows it, and the client can read.
func (c *Controller) kennelList(ctx context.Context, inst *store.Installation, in kennelPassInput) *kennelListing {
	l := &kennelListing{State: kennel.CoverageError}
	// The hold comes first, and is the one thing Kennel Club must never skip: an
	// installation GitHub has rate-limited is being waited on by the poller and the
	// scheduler, which have first call on whatever is left.
	if c.githubHeld(inst.ID, in.now) {
		l.State = kennel.CoverageHeld
		return l
	}
	client, err := c.clients.get(ctx, inst)
	if err != nil {
		c.log.Warn("Kennel Club could not build a GitHub client for an installation", "installation", inst.ID, "error", err)
		return l
	}
	reader, ok := client.(github.RepoReader)
	if !ok {
		l.State = kennel.CoverageUnavailable
		return l
	}
	l.Client, l.Reader = client, reader
	// The limit is read over the wire, because nothing keeps the last one. GitHub
	// does not count this request against it.
	if rl, err := client.RateLimit(ctx); err == nil && rl != nil {
		l.Limit = *rl
	}
	c.observeKennel(inst.ID, nil)
	if !c.kennelTake(inst.ID, in, l.Limit) {
		l.State = kennel.CoverageHeld
		return l
	}
	repos, err := reader.KennelRepositories(ctx, kennelListingLimit)
	c.observeKennel(inst.ID, err)
	if err != nil {
		c.holdIfRateLimited(inst.ID, err, in.now, "listing repositories for Kennel Club")
		l.State = kennelState(err)
		c.log.Warn("Kennel Club could not list an installation's repositories", "installation", inst.ID, "state", l.State, "error", err)
		return l
	}
	// One request has been taken; a listing is a page per hundred.
	c.kennelSpend(inst.ID, (len(repos)-1)/100)
	l.State = kennel.CoverageOK
	l.Repos = make(map[string]github.Repository, len(repos))
	for _, r := range repos {
		l.Repos[strings.ToLower(r.FullName)] = r
	}
	return l
}

// holdIfRateLimited is holdRateLimited for an error that may be anything.
func (c *Controller) holdIfRateLimited(installationID string, err error, now time.Time, doing string) {
	if errors.Is(err, github.ErrRateLimited) {
		c.metrics.kennelHolds.Inc()
		c.holdRateLimited(installationID, err, now, doing)
	}
}

// kennelTouch records the repositories this pass is about, and returns the rows
// to refresh: every repository of the installation that has one.
//
// A repository is served if the fleet had a hand in a job for it; under the
// installation scope every repository the App can see is, and none of them is
// pruned while it can be seen.
func (c *Controller) kennelTouch(ctx context.Context, inst *store.Installation, host string, served []store.KennelServed, l *kennelListing, in kennelPassInput) []*store.KennelRepository {
	touch := func(r github.Repository) {
		_, err := c.st.TouchKennelRepository(ctx, store.KennelRepositoryRef{
			GitHubHost: host, RepositoryID: r.ID, InstallationID: inst.ID, FullName: r.FullName, Visibility: r.Visibility,
		})
		if err != nil {
			c.log.Warn("Kennel Club could not record a repository", "installation", inst.ID, "repository", r.FullName, "error", err)
		}
	}
	if in.cfg.Scope == config.KennelScopeInstallation {
		for _, r := range l.Repos {
			touch(r)
		}
	} else {
		for _, s := range served {
			// A repository the listing does not name has no ID, and so no row:
			// it may have been deleted, or the App may have lost sight of it.
			if r, ok := l.Repos[strings.ToLower(s.Repo)]; ok {
				touch(r)
			}
		}
	}
	rows, err := c.kennelRows(ctx, inst.ID)
	if err != nil {
		c.log.Warn("Kennel Club could not read its repositories", "installation", inst.ID, "error", err)
	}
	return rows
}

// kennelRefresh brings one repository up to date: the reads from GitHub if they
// are due, and the evaluation if anything changed. It returns the worst state a
// read ended in.
func (c *Controller) kennelRefresh(ctx context.Context, inst *store.Installation, row *store.KennelRepository, l *kennelListing, in kennelPassInput) kennel.CoverageState {
	wm := parseKennelWatermark(row.Watermark)
	due := kennelDue(row, in.now)
	var retry time.Duration
	outcome := kennel.CoverageOK

	if due {
		switch {
		case l == nil:
			// Due and nothing was listed cannot happen: due rows are what make the
			// pass list. A repository that somehow got here is left for the next.
			return outcome
		case l.State != kennel.CoverageOK:
			// The listing did not get through, so the repository's visibility is
			// whatever was last read, and for a public one the runs are not read.
			outcome = l.State
			if row.Visibility == string(kennel.VisibilityPublic) {
				wm.State = l.State
			}
			retry = kennelRetryAfter(l.State)
		case row.Visibility == string(kennel.VisibilityPublic):
			outcome, retry = c.kennelReadRuns(ctx, inst, row, &wm, l, in)
		}
	}
	if err := c.kennelEvaluate(ctx, inst, row, wm, l, due, retry, in); err != nil {
		c.log.Warn("Kennel Club could not evaluate a repository", "repository", row.FullName, "error", err)
	}
	return outcome
}

// kennelReadRuns reads the trigger and head repository of runs the fleet ran in
// a public repository, newest and unseen only, and remembers the ones a check
// can use.
//
// It is the one read that scales with the fleet's own activity, so it is where
// the caps are: a hundred runs a refresh, behind a watermark so a restart does
// not read them again. The runs come from the fleet's own record of its jobs --
// nothing is listed from GitHub -- so the repository's other runs, the ones
// somebody else's runners ran, are never asked about.
//
// The runs are read oldest first among the hundred taken, so that a failure in
// the middle leaves the watermark at the last run actually read and the rest
// are asked for next time. When there were more than a hundred the older ones are
// skipped for good, and the watermark says so: the evidence is a sample until
// they have left the window.
func (c *Controller) kennelReadRuns(ctx context.Context, inst *store.Installation, row *store.KennelRepository, wm *kennelWatermark, l *kennelListing, in kennelPassInput) (kennel.CoverageState, time.Duration) {
	runs, err := c.st.KennelRunsAfter(ctx, row.FullName, wm.After, in.now.Add(-in.window), kennelRunsPerRefresh+1)
	if err != nil {
		c.log.Warn("Kennel Club could not list the runs this fleet ran", "repository", row.FullName, "error", err)
		return kennel.CoverageError, kennelRetryError
	}
	if len(runs) > kennelRunsPerRefresh {
		// The newest hundred are kept. The extra one, the oldest taken, is when
		// whatever lies below it was queued at the latest.
		wm.GapQueuedAt = max(wm.GapQueuedAt, runs[len(runs)-1].QueuedAt.UnixMilli())
		runs = runs[:kennelRunsPerRefresh]
	}
	slices.Reverse(runs)

	var found []kennelSeenRun
	failed := kennel.CoverageState("")
	progressed := false
	for _, run := range runs {
		if c.githubHeld(inst.ID, c.Now()) || !c.kennelTake(inst.ID, in, l.Limit) {
			failed = kennel.CoverageHeld
			break
		}
		got, err := l.Reader.KennelRun(ctx, row.FullName, run.ID)
		c.observeKennel(inst.ID, err)
		if err != nil {
			if errors.Is(err, github.ErrNotFound) {
				// A run GitHub no longer has -- deleted, or aged out of its own
				// retention -- has nothing to read, and is stepped over.
				wm.After = max(wm.After, run.ID)
				progressed = true
				continue
			}
			c.holdIfRateLimited(inst.ID, err, in.now, "reading workflow runs for Kennel Club")
			failed = kennelState(err)
			if failed != kennel.CoverageDenied {
				c.log.Warn("Kennel Club could not read a workflow run", "repository", row.FullName, "run", run.ID, "state", failed, "error", err)
			}
			break
		}
		wm.After = max(wm.After, run.ID)
		progressed = true
		event := kennel.NormalizeEvent(got.Event)
		if event != "other" && (event != "pull_request" || got.FromFork()) {
			found = append(found, kennelSeenRun{ID: run.ID, Event: event, Fork: got.FromFork(), QueuedAt: run.QueuedAt.UnixMilli()})
		}
	}
	wm.remember(found, in.now, in.window)
	wm.State = failed
	if failed == "" || progressed {
		// A read that got anywhere counts as one, including a read that found
		// nothing to read: that is an answer, and "none" is not "never asked".
		wm.ReadAt = in.now.UnixMilli()
	}
	return failed, kennelRetryAfter(failed)
}

// kennelEvaluate works one repository out from what is known, and keeps and
// announces the answer if it is one that has not been given.
func (c *Controller) kennelEvaluate(ctx context.Context, inst *store.Installation, row *store.KennelRepository, wm kennelWatermark, l *kennelListing, due bool, retry time.Duration, in kennelPassInput) error {
	waivers, err := c.st.ListKennelWaivers(ctx, row.ID)
	if err != nil {
		return fmt.Errorf("reading waivers: %w", err)
	}
	policy := in.policy
	policy.Waivers = make([]kennel.Waiver, 0, len(waivers))
	for _, w := range waivers {
		policy.Waivers = append(policy.Waivers, kennel.Waiver{
			ID: w.ID, Code: kennel.Code(w.Code), Subject: w.Subject, Severity: kennel.Severity(w.Severity),
			Reason: w.Reason, By: w.CreatedByName, At: w.CreatedAt, ExpiresAt: w.ExpiresAt,
		})
	}

	snap, err := c.kennelSnapshot(ctx, inst, row, wm, l, in)
	if err != nil {
		return err
	}
	digest := kennelDigest(snap, policy, in.now)

	// A repository nothing is due for is only re-evaluated when something it is
	// judged on changed. What the operator decided is acted on at once; what the
	// fleet did waits for the ten-minute rule.
	if !due && row.EvaluatedAt != nil && row.EvaluatorVersion == kennel.Version {
		was := parseKennelDigests(row.InputsDigest)
		policyMoved := digest.Policy != was.Policy
		factsMoved := digest.Facts != was.Facts
		if !policyMoved && (!factsMoved || in.now.Sub(*row.EvaluatedAt) < kennelLocalInterval) {
			return nil
		}
	}

	ev := kennel.Evaluate(snap, policy)
	counts := ev.Counts()
	evJSON, err := marshalJSON(ev)
	if err != nil {
		return err
	}
	covJSON, err := marshalJSON(snap.Coverage)
	if err != nil {
		return err
	}
	opened, closed, waived := kennelMovement(row, ev)
	next := row.NextDueAt
	if due {
		next = in.now.Add(in.interval + kennelJitter(row.RepositoryID, in.interval))
		if retry > 0 {
			next = in.now.Add(retry)
		}
	}
	if err := c.st.SaveKennelEvaluation(ctx, row.ID, store.KennelEvaluationRecord{
		State: string(ev.State()), EvaluatorVersion: kennel.Version, EvaluatedAt: in.now, NextDueAt: next,
		InputsDigest: digest.String(), Coverage: covJSON, Evaluation: evJSON, Watermark: wm.marshal(),
		OpenErrors: counts.Error, OpenWarnings: counts.Warning, OpenInfos: counts.Info, Waived: counts.Waived,
	}); err != nil {
		return err
	}

	for _, code := range opened {
		c.metrics.kennelOpened.WithLabelValues(string(code)).Inc()
	}
	for _, code := range closed {
		c.metrics.kennelClosed.WithLabelValues(string(code)).Inc()
	}
	for _, code := range waived {
		c.metrics.kennelWaived.WithLabelValues(string(code)).Inc()
	}

	// A waiver for a finding that is no longer reported has no use. It is retired
	// here, and not left to expire, so that the day the finding comes back it is
	// judged again and not excused by a decision about something that went away.
	if len(ev.Stale) > 0 {
		ids := make([]string, 0, len(ev.Stale))
		for _, w := range ev.Stale {
			ids = append(ids, w.ID)
		}
		if err := c.st.DeleteKennelWaivers(ctx, ids); err != nil {
			c.log.Warn("Kennel Club could not retire waivers for findings that stopped being reported", "repository", row.FullName, "error", err)
		} else {
			c.log.Info("Kennel Club retired waivers for findings that are no longer reported", "repository", row.FullName, "waivers", len(ids))
			// A decision with a reason and an owner does not disappear without a
			// trace: the audit row says whose it was and why it was made.
			for _, w := range ev.Stale {
				_ = c.authsvc.Auditor().Record(ctx, nil, "kennel.waiver_retired", "kennel_repository", row.ID, w, nil)
			}
		}
	}
	c.PublishKennelRepository(ctx, row.ID)
	return nil
}

// kennelSnapshot assembles everything the evaluator is told about a repository.
// It is plain data: a number, a flag, a word, an identifier this controller
// made. Nothing a repository's author wrote is in it.
func (c *Controller) kennelSnapshot(ctx context.Context, inst *store.Installation, row *store.KennelRepository, wm kennelWatermark, l *kennelListing, in kennelPassInput) (kennel.Snapshot, error) {
	facts, err := c.st.KennelFleetFacts(ctx, store.KennelFleetQuery{
		Repo: row.FullName, Since: in.now.Add(-in.window), UnservedSince: in.now.Add(-kennelUnservedWindow),
		LongFloor: kennel.LongJobFloor,
	})
	if err != nil {
		return kennel.Snapshot{}, fmt.Errorf("reading what the fleet observed: %w", err)
	}
	jobs := kennel.JobFacts{Ran: facts.Ran, Queued: facts.Queued}
	for _, waited := range facts.Unserved {
		jobs.Unserved = append(jobs.Unserved, kennel.Unserved{Waited: waited})
	}
	for _, long := range facts.Long {
		jobs.Long = append(jobs.Long, kennel.FinishedJob{Duration: long.Duration, Conclusion: long.Conclusion})
	}
	var poolFacts []kennel.PoolFact
	var used []*store.Pool
	for _, pj := range facts.Pools {
		p := in.pools[pj.PoolID]
		if p == nil {
			// A pool that has since been deleted cannot be weak any more, and its
			// finding clears by itself.
			continue
		}
		used = append(used, p)
		poolFacts = append(poolFacts, kennel.PoolFact{ID: p.ID, Name: p.Name, JobsRun: pj.Jobs, Dangers: kennelDangers(p)})
	}

	public := row.Visibility == string(kennel.VisibilityPublic)
	cov := kennel.Coverage{kennel.SourceFleet: {State: kennel.CoverageOK}}
	switch {
	case l != nil && l.State == kennel.CoverageOK:
		cov[kennel.SourceMetadata] = kennel.SourceState{State: kennel.CoverageOK}
	case row.Visibility != "":
		// Read on an earlier pass, and a visibility changes rarely and makes the
		// repository due at once when it does: stale is still a fact.
		cov[kennel.SourceMetadata] = kennel.SourceState{State: kennel.CoverageOK}
	case l != nil:
		cov[kennel.SourceMetadata] = kennel.SourceState{State: l.State}
	default:
		cov[kennel.SourceMetadata] = kennel.SourceState{State: kennel.CoverageNotRead}
	}
	snap := kennel.Snapshot{
		At:   in.now,
		Repo: kennel.Repo{Visibility: kennel.Visibility(row.Visibility)},
		Fleet: kennel.Fleet{
			Window: in.window, Jobs: jobs, Pools: poolFacts,
			RunnerGroupAllowsPublic: c.kennelRunnerGroupAllowsPublic(inst.ID, used),
		},
		Coverage: cov,
	}
	// A private repository's runs are not read, so they are not in its coverage
	// either: a source nothing needs is not a gap.
	if public {
		snap.Runs = wm.facts(in.now, in.window)
		cov[kennel.SourceRuns] = kennel.SourceState{State: wm.coverage(in.now, in.window)}
	}
	return snap, nil
}

// kennelRunnerGroupAllowsPublic says what the runner groups the pools that ran a
// repository's jobs say about public repositories, from what the controller
// already knows. It asks GitHub nothing: it is read from the groups cached
// when a runner was last created, and where that has not happened, or the pools
// disagree, the answer is that nobody said.
func (c *Controller) kennelRunnerGroupAllowsPublic(installationID string, pools []*store.Pool) kennel.Tri {
	if len(pools) == 0 {
		return kennel.TriUnknown
	}
	c.clients.mu.Lock()
	defer c.clients.mu.Unlock()
	e := c.clients.entries[installationID]
	if e == nil || e.groups == nil {
		return kennel.TriUnknown
	}
	yes, no := 0, 0
	for _, p := range pools {
		name := strings.ToLower(strings.TrimSpace(p.RunnerGroup))
		if name == "" {
			name = "default"
		}
		g, ok := e.groups[name]
		if !ok || !g.PublicRepositoryAccessKnown {
			return kennel.TriUnknown
		}
		if g.AllowsPublicRepositories {
			yes++
		} else {
			no++
		}
	}
	switch {
	case no == 0:
		return kennel.TriYes
	case yes == 0:
		return kennel.TriNo
	}
	return kennel.TriUnknown
}

// kennelNoteResult records whether reading an installation worked: a success
// clears what was recorded, and a failure is kept from when it began.
func (c *Controller) kennelNoteResult(installationID string, state kennel.CoverageState, now time.Time) {
	c.kennel.mu.Lock()
	defer c.kennel.mu.Unlock()
	if state == kennel.CoverageOK {
		delete(c.kennel.notes, installationID)
		return
	}
	n := c.kennel.notes[installationID]
	if n.Since.IsZero() {
		n.Since = now
	}
	n.State = state
	c.kennel.notes[installationID] = n
}

// kennelNotes are the installations whose reads are not getting through, for the
// Overview.
func (c *Controller) kennelNotes(ctx context.Context) []KennelInstallationNote {
	c.kennel.mu.Lock()
	notes := make(map[string]kennelNote, len(c.kennel.notes))
	for id, n := range c.kennel.notes {
		notes[id] = n
	}
	c.kennel.mu.Unlock()
	out := make([]KennelInstallationNote, 0, len(notes))
	if len(notes) == 0 {
		return out
	}
	insts, err := c.st.ListInstallations(ctx)
	if err != nil {
		return out
	}
	for _, inst := range insts {
		n, ok := notes[inst.ID]
		if !ok {
			continue
		}
		out = append(out, KennelInstallationNote{
			InstallationID: inst.ID, Target: inst.Target, State: n.State,
			Reason: kennel.Explain(kennel.SourceMetadata, n.State), Since: n.Since,
		})
	}
	slices.SortFunc(out, func(a, b KennelInstallationNote) int { return strings.Compare(a.Target, b.Target) })
	return out
}

// PublishKennelRepository announces one repository in the shape GET
// /kennel/repositories/{id} returns. The API calls it after its own write, as it
// does for a pool, and the loop calls it after an evaluation.
func (c *Controller) PublishKennelRepository(ctx context.Context, id string) {
	if c.bus == nil {
		return
	}
	row, err := c.st.GetKennelRepository(ctx, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			c.log.Warn("could not render a Kennel Club repository for the event stream", "repository", id, "error", err)
		}
		return
	}
	c.publish(events.KindKennelUpdated, "kennel:"+id, newKennelRepositoryView(row))
}

// publishKennelDeleted announces repositories that are gone.
func (c *Controller) publishKennelDeleted(ids []string) {
	for _, id := range ids {
		c.publish(events.KindKennelDeleted, "kennel:"+id, deletedPayload{ID: id})
	}
}

// pruneKennel deletes what was last served so long ago nobody is waiting on it.
// It runs with Kennel Club off, because it only ever deletes Kennel Club's own
// rows and asks GitHub for nothing: a feature that is off still tidies up after
// itself, and creates nothing.
func (c *Controller) pruneKennel(ctx context.Context, now time.Time) {
	ids, err := c.st.PruneKennelRepositories(ctx, now.Add(-kennelRetention))
	if err != nil {
		c.log.Warn("could not prune Kennel Club's repositories", "error", err)
		return
	}
	if len(ids) > 0 {
		c.log.Debug("pruned Kennel Club's repositories", "rows", len(ids), "older_than", kennelRetention)
		c.publishKennelDeleted(ids)
	}
}

// kennelRowsOf is the IDs Kennel Club holds for an installation, taken before
// the installation is deleted, because the rows cascade away with it and a
// cascade announces nothing.
func (c *Controller) kennelRowsOf(ctx context.Context, installationID string) []string {
	ids, err := c.st.KennelRepositoryIDs(ctx, installationID)
	if err != nil {
		c.log.Warn("could not list an installation's Kennel Club repositories before deleting it", "installation", installationID, "error", err)
	}
	return ids
}

func marshalJSON(v any) (json.RawMessage, error) {
	raw, err := json.Marshal(v)
	return raw, err
}

// observeKennel counts a request Kennel Club made of GitHub, as every sweep
// does, so the installation's request counter says all of them.
func (c *Controller) observeKennel(installationID string, err error) {
	c.observeGitHub(installationID, err)
	if errors.Is(err, github.ErrServerError) {
		c.kennelNoteServerError(installationID, c.Now())
	}
	c.metrics.kennelRequests.WithLabelValues(githubResult(err)).Inc()
}

// kennelMovement says what an evaluation changed about a repository's findings,
// for the three counters that say whether a check is earning its place.
//
// A finding is identified by its code and subject. Opened is one that was not
// there before, open or waived; closed is one that was there and is not reported
// at all now; waived is one that was open and a waiver now covers. One that goes
// from waived back to open because its waiver ended is none of the three: nothing
// was fixed and nothing is new, and counting it would make a lapsed waiver look
// like a failure of the check.
func kennelMovement(row *store.KennelRepository, now kennel.Evaluation) (opened, closed, waived []kennel.Code) {
	type key struct {
		code    kennel.Code
		subject string
	}
	var before kennel.Evaluation
	_ = json.Unmarshal(row.Evaluation, &before)
	was, wasOpen, is := map[key]bool{}, map[key]bool{}, map[key]bool{}
	for _, f := range before.Findings {
		was[key{f.Code, f.Subject}], wasOpen[key{f.Code, f.Subject}] = true, true
	}
	for _, w := range before.Waived {
		was[key{w.Finding.Code, w.Finding.Subject}] = true
	}
	for _, f := range now.Findings {
		k := key{f.Code, f.Subject}
		is[k] = true
		if !was[k] {
			opened = append(opened, f.Code)
		}
	}
	for _, w := range now.Waived {
		k := key{w.Finding.Code, w.Finding.Subject}
		is[k] = true
		switch {
		case !was[k]:
			opened = append(opened, w.Finding.Code)
		case wasOpen[k]:
			waived = append(waived, w.Finding.Code)
		}
	}
	for _, f := range before.Findings {
		if !is[key{f.Code, f.Subject}] {
			closed = append(closed, f.Code)
		}
	}
	for _, w := range before.Waived {
		if !is[key{w.Finding.Code, w.Finding.Subject}] {
			closed = append(closed, w.Finding.Code)
		}
	}
	return opened, closed, waived
}
