package updates

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/version"
)

// AttemptTimeout is how long an update may stay open before it is recorded as
// timed out. The engine's own worst case is fifty minutes, so a shorter limit
// would fail an update that was still working.
const AttemptTimeout = 90 * time.Minute

// RetryAfter is how long the planner leaves a machine alone after an update of
// it failed, so that a cause outside Zoomies (a held upgrade.lock, a registry
// that is down) has time to clear before the next try.
const RetryAfter = 30 * time.Minute

// MaxFailuresPerTag is how many failed or timed-out attempts to take one machine
// to one release the planner makes before it leaves that release on that
// machine to a person. Twice is a pattern, and a third try would only add to the
// log.
const MaxFailuresPerTag = 2

// The words the store keeps an attempt's scope and state and a rollout's state
// in. This package may not import the store, so they are written out here as
// well, and the two change together.
const (
	scopeController = "controller"
	scopeHost       = "host"
	attemptOpen     = "requested"
	rolloutRunning  = "running"
	rolloutHalted   = "halted"
)

// Attempt is one update that is open: the controller's own, or one host's.
type Attempt struct {
	ID string
	// Scope is "controller" or "host". HostID names the host for a host's
	// attempt and is empty for the controller's.
	Scope, HostID string
	// To is the tag the attempt takes the machine to. It is read only for the
	// sentence, and only when ValidTag says it is a tag.
	To string
	// State is the store's word for it. Only "requested" is open; an attempt in
	// any other state has ended and is not read.
	State       string
	RequestedAt time.Time
}

// Rollout is the open walk of the fleet to one release.
type Rollout struct {
	ID string
	// Target is the tag the rollout was started for.
	Target string
	// State is "running" or "halted"; anything else is not open and is read as
	// no rollout.
	State        string
	HaltedReason string
}

// HostFacts is what the planner knows of one host.
type HostFacts struct {
	ID, Name string
	// Version is what the host's agent last reported, which for a release binary
	// is "1.3.5" without the v of its tag. Empty is not known.
	Version string
	// GOOS and GOARCH are the host's system, as its agent reported it; empty is
	// not known, and a release is then not held against it.
	GOOS, GOARCH string
	// Embedded is the agent inside the controller, which the controller's own
	// update takes with it.
	Embedded bool
	Healthy  bool
	// CanSelfUpdate and WhyNot are the controller's hostCanSelfUpdate, the one
	// answer the host's card and the planner share. WhyNot is its sentence when
	// the host cannot, and holds no path.
	CanSelfUpdate bool
	WhyNot        string
	ActiveRunners int
	// Open is the host's open attempt, or nil.
	Open *Attempt
	// Failures is how many attempts to take the host to the controller's release
	// failed or timed out. LastFailedAt is when the most recent of its attempts
	// failed or timed out, whatever release it was for, or zero.
	Failures     int
	LastFailedAt time.Time
	// FailedInRollout is the error of the open rollout's failed or timed-out
	// attempt for this host since the rollout was started or last resumed, or
	// empty. One an operator has resumed past is not carried, or the rollout
	// would halt again on the failure it was resumed from.
	FailedInRollout string
}

// Snapshot is everything Decide reads, gathered by the controller.
type Snapshot struct {
	// Now is the decision time; nothing here reads a clock.
	Now  time.Time
	Mode Mode
	Soak time.Duration
	// Running is the controller's version as it reports itself.
	Running      string
	GOOS, GOARCH string
	Releases     []Release
	// HelperReady says the update helper beside the controller is installed and
	// answering.
	HelperReady bool
	// Fenced says the controller may not act: it is fenced for recovery or does
	// not hold the database's lease.
	Fenced bool
	// Controller is the controller's open attempt, or nil.
	Controller *Attempt
	// ControllerFailures is how many attempts to take the controller to the
	// release Choose picks failed or timed out; ControllerLastFailedAt is when its
	// most recent attempt failed or timed out, whatever release it was for.
	ControllerFailures     int
	ControllerLastFailedAt time.Time
	// Rollout is the open rollout, or nil.
	Rollout *Rollout
	Hosts   []HostFacts
}

// ActionKind is one thing the controller is asked to do.
type ActionKind string

const (
	// ActionRequestController asks the helper beside the controller to take it
	// to Tag.
	ActionRequestController ActionKind = "request_controller"
	// ActionStartRollout opens a rollout to Tag.
	ActionStartRollout ActionKind = "start_rollout"
	// ActionUpdateHost asks the host HostID to take itself to Tag, as part of the
	// open rollout.
	ActionUpdateHost ActionKind = "update_host"
	// ActionTimeOut records the attempt AttemptID as timed out.
	ActionTimeOut ActionKind = "time_out"
	// ActionHalt halts the open rollout on a failure of the host HostID.
	ActionHalt ActionKind = "halt"
	// ActionFinishRollout closes the open rollout as done.
	ActionFinishRollout ActionKind = "finish_rollout"
	// ActionCancelRollout closes the open rollout as cancelled.
	ActionCancelRollout ActionKind = "cancel_rollout"
)

// Action is one step of a plan, and the reason for it in a person's words.
type Action struct {
	Kind                   ActionKind
	HostID, AttemptID, Tag string
	Reason                 string
}

// Plan is what to do now, in order, and the sentence the status shows.
type Plan struct {
	Actions  []Action
	Sentence string
}

// Decide says what automatic updating does next, and why.
//
// It is pure, as the scheduler's Decide is: time is Snapshot.Now and everything
// else arrives in the snapshot, so a pass that is missed, run twice or run by the
// next process after a restart comes to the same answer, and every edge can be
// tried in a table. Hosts and releases are sorted before they are read, so the
// order rows arrive in never picks a host or changes a word.
//
// The rules, in order; each but the time-outs ends the pass once it decides:
//
//  1. Fenced: nothing at all. A fenced controller may be one of two.
//  2. Time-outs, in every mode: closing an attempt that has had 90 minutes is
//     bookkeeping, not a decision to update, and the controller's own closer
//     does the same. An attempt timed out this pass still counts as open.
//  3. Off (or a mode nothing knows): cancel an open rollout and nothing else.
//  4. A failure in a running rollout halts it; a halted rollout moves nothing,
//     not even the controller, until an operator resumes or cancels it.
//  5. In auto, the controller: once Choose says its release is due, it goes
//     before any host, or waits with the reason it cannot go.
//  6. The rollout: carried on in manual or auto, one host at a time; started
//     only in auto. Manual never starts anything; a rollout open in manual was
//     started by a person, and moving it is what they asked for.
//
// The helper's path unit allows five starts in ten minutes. A plan holds at most
// one action that writes a request, an open attempt blocks the next, and
// RetryAfter spaces the retries of a failure, so no machine nears that limit.
func Decide(s Snapshot) Plan {
	if s.Fenced {
		return Plan{Sentence: "This controller is fenced for recovery or does not hold the database's lease, " +
			"so automatic updating starts, halts and closes nothing until it may act again."}
	}
	p := newPlanner(s)
	p.timeOuts()
	if s.Mode != ModeManual && s.Mode != ModeAuto {
		p.off()
		return p.plan
	}
	if p.halt() || p.halted() {
		return p.plan
	}
	if s.Mode == ModeAuto && p.controller() {
		return p.plan
	}
	if p.rollout != nil {
		p.advance()
	} else {
		p.start()
	}
	return p.plan
}

type planner struct {
	s        Snapshot
	hosts    []HostFacts
	releases []Release
	choice   Target
	// target is the controller's release as a tag, which every host is taken
	// to, or empty when the controller is not on a release.
	target  string
	rollout *Rollout
	// timedOut is the reason given for each attempt this pass timed out.
	timedOut map[string]string
	plan     Plan
}

func newPlanner(s Snapshot) *planner {
	p := &planner{s: s, timedOut: map[string]string{}}
	p.hosts = slices.Clone(s.Hosts)
	slices.SortStableFunc(p.hosts, func(a, b HostFacts) int {
		return cmp.Or(cmp.Compare(a.ActiveRunners, b.ActiveRunners), strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
	// Ordered by text, which is total, rather than by CompareBuilds, which is not.
	// Choose and the binary check already answer alike whatever the order; the
	// sorted copy keeps any later reader of the list from depending on the order
	// the rows arrived in.
	p.releases = slices.Clone(s.Releases)
	slices.SortStableFunc(p.releases, func(a, b Release) int {
		return cmp.Or(strings.Compare(a.Tag, b.Tag), a.PublishedAt.Compare(b.PublishedAt), strings.Compare(a.URL, b.URL),
			compareBool(a.Prerelease, b.Prerelease), compareBool(a.Draft, b.Draft),
			strings.Compare(strings.Join(a.Assets, "\n"), strings.Join(b.Assets, "\n")))
	})
	p.choice = Choose(ChooseInput{Mode: s.Mode, Soak: s.Soak, Now: s.Now, Running: s.Running, Releases: p.releases, GOOS: s.GOOS, GOARCH: s.GOARCH})
	p.target, _ = TargetTag(s.Running)
	if r := s.Rollout; r != nil && (r.State == rolloutRunning || r.State == rolloutHalted) {
		p.rollout = r
	}
	return p
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	}
	return 1
}

func (p *planner) act(a Action)        { p.plan.Actions = append(p.plan.Actions, a) }
func (p *planner) say(sentence string) { p.plan.Sentence = sentence }

// controllerAttempt is the controller's open attempt, or nil. One that is not
// the controller's, or has ended, is not.
func (p *planner) controllerAttempt() *Attempt {
	if a := p.s.Controller; a != nil && a.Scope == scopeController && a.State == attemptOpen {
		return a
	}
	return nil
}

// hostAttempt is h's open attempt, or nil. One for another host, which a host
// re-joined under a new id can leave behind, is not h's to wait on or time out.
func hostAttempt(h HostFacts) *Attempt {
	if a := h.Open; a != nil && a.Scope == scopeHost && a.HostID == h.ID && a.State == attemptOpen {
		return a
	}
	return nil
}

// expired matches the controller's own closer: at exactly 90 minutes an attempt
// is still waited on, so the two never disagree about the minute.
func (p *planner) expired(a *Attempt) bool {
	return p.s.Now.Sub(a.RequestedAt) > AttemptTimeout
}

func (p *planner) timeOuts() {
	if a := p.controllerAttempt(); a != nil && p.expired(a) {
		reason := fmt.Sprintf("The controller's update to %s did not finish within 90 minutes, so it is recorded as timed out.", tagOr(a.To, "a new release"))
		p.timedOut[a.ID] = reason
		p.act(Action{Kind: ActionTimeOut, AttemptID: a.ID, Reason: reason})
	}
	for _, h := range p.hosts {
		if a := hostAttempt(h); a != nil && p.expired(a) {
			reason := fmt.Sprintf("The update of %s to %s did not finish within 90 minutes, so it is recorded as timed out.", label(h), tagOr(a.To, "a new release"))
			p.timedOut[a.ID] = reason
			p.act(Action{Kind: ActionTimeOut, HostID: h.ID, AttemptID: a.ID, Reason: reason})
		}
	}
}

// off cancels the open rollout. An update already handed to a helper is not the
// planner's to stop: it finishes by itself, and the controller records it.
func (p *planner) off() {
	if p.rollout != nil {
		reason := fmt.Sprintf("Updates are off, so the rollout to %s is cancelled. An update already under way finishes by itself and is recorded.",
			tagOr(p.rollout.Target, "its release"))
		p.act(Action{Kind: ActionCancelRollout, Reason: reason})
		p.say(reason)
		return
	}
	if sentence, open := p.blocked(); open {
		p.say(sentence)
		return
	}
	p.say(p.choice.Reason)
}

// halt halts a running rollout on the first host, in planning order, whose update
// in it failed. The helper's error is not copied into the reason: it can name a
// path on the host, and the attempt already holds it where only the platform
// role reads it.
func (p *planner) halt() bool {
	if p.rollout == nil || p.rollout.State != rolloutRunning {
		return false
	}
	for _, h := range p.hosts {
		if h.FailedInRollout == "" {
			continue
		}
		reason := fmt.Sprintf("The update of %s to %s did not succeed, so the rollout is halted. Read why on the host's card, then resume the rollout or cancel it.",
			label(h), tagOr(p.rollout.Target, "its release"))
		p.act(Action{Kind: ActionHalt, HostID: h.ID, Reason: reason})
		p.say(reason)
		return true
	}
	return false
}

func (p *planner) halted() bool {
	if p.rollout == nil || p.rollout.State != rolloutHalted {
		return false
	}
	p.say(fmt.Sprintf("The rollout to %s is halted, so nothing is updated until an administrator resumes or cancels it.",
		tagOr(p.rollout.Target, "its release")))
	return true
}

// controller updates the controller once its release is due, and says why it
// cannot when it cannot. Until it is due, hosts follow the release it runs.
func (p *planner) controller() bool {
	t := p.choice
	if !t.DueBy(p.s.Now) {
		return false
	}
	tag := t.Release.Tag
	if sentence, open := p.blocked(); open {
		p.say(sentence)
		return true
	}
	switch {
	case !p.s.HelperReady:
		p.say(fmt.Sprintf("%s can be taken now, but the update helper is not installed beside the controller, so auto waits, and no host is updated before the controller. "+
			"Run sudo zoomies updates helper install on the controller's host.", tag))
	case p.s.ControllerFailures >= MaxFailuresPerTag:
		p.say(fmt.Sprintf("Auto stops trying to update the controller to %s after %d failed attempts, and no host is updated before the controller; an operator must act. "+
			"Read why in Settings → Updates, then press Update there or run sudo zoomies upgrade on the controller's host.", tag, p.s.ControllerFailures))
	case p.waiting(p.s.ControllerLastFailedAt):
		p.say(fmt.Sprintf("The controller's last update did not succeed; auto tries %s again in %s.", tag, p.retryIn(p.s.ControllerLastFailedAt)))
	default:
		p.act(Action{Kind: ActionRequestController, Tag: tag, Reason: t.Reason})
		p.say(t.Reason)
	}
	return true
}

// advance moves the running rollout on by one host, or ends it.
func (p *planner) advance() {
	r := p.rollout
	if !ValidTag(r.Target) {
		p.say("The open rollout's target is not a release tag, so it updates nothing. Cancel it and start another.")
		return
	}
	if p.target == "" {
		reason := "The controller no longer runs a release, so there is nothing to take hosts to and the rollout is cancelled."
		p.act(Action{Kind: ActionCancelRollout, Reason: reason})
		p.say(reason)
		return
	}
	switch version.CompareBuilds(r.Target, p.target) {
	case version.SkewNone:
	case version.SkewBehind:
		next := "hosts follow " + p.target + " in a new one."
		if p.s.Mode != ModeAuto {
			next = "start another to bring hosts to " + p.target + "."
		}
		reason := fmt.Sprintf("The controller now runs %s, newer than this rollout's %s, so the rollout is finished; %s", p.target, r.Target, next)
		p.act(Action{Kind: ActionFinishRollout, Reason: reason})
		p.say(reason)
		return
	default:
		reason := fmt.Sprintf("The controller runs %s, not this rollout's %s, so the rollout is cancelled: a host is never taken past its controller.", p.target, r.Target)
		p.act(Action{Kind: ActionCancelRollout, Reason: reason})
		p.say(reason)
		return
	}
	if sentence, open := p.blocked(); open {
		p.say(sentence)
		return
	}
	sv := p.survey()
	switch {
	case sv.ready != nil:
		p.updateHost(*sv.ready)
	case sv.pending != "":
		p.say(fmt.Sprintf("The rollout to %s is waiting. %s", p.target, sv.pending))
	default:
		reason := fmt.Sprintf("Every host that can be updated from here runs %s, so the rollout is finished.", p.target)
		p.act(Action{Kind: ActionFinishRollout, Reason: reason})
		p.say(withNote(reason, sv.note))
	}
}

// start opens a rollout in auto when a host can be taken to the controller's
// release now. A host that cannot is never the reason one starts, or each pass
// would start one and the next finish it.
func (p *planner) start() {
	if sentence, open := p.blocked(); open {
		p.say(sentence)
		return
	}
	if p.s.Mode != ModeAuto || p.target == "" {
		p.say(p.choice.Reason)
		return
	}
	sv := p.survey()
	if sv.ready == nil {
		p.say(withNote(p.choice.Reason, sv.note))
		return
	}
	hosts := strconv.Itoa(sv.updatable) + " hosts are behind it and are"
	if sv.updatable == 1 {
		hosts = "1 host is behind it and is"
	}
	reason := fmt.Sprintf("Starting a rollout to %s, the release the controller runs: %s updated one at a time.", p.target, hosts)
	p.act(Action{Kind: ActionStartRollout, Tag: p.target, Reason: reason})
	p.say(reason)
}

func (p *planner) updateHost(h HostFacts) {
	p.act(Action{Kind: ActionUpdateHost, HostID: h.ID, Tag: p.target, Reason: fmt.Sprintf(
		"%s is behind %s and runs the fewest jobs (%d) of the hosts that can be updated now, so it goes next.", label(h), p.target, h.ActiveRunners)})
	p.say(fmt.Sprintf("Updating %s to %s. Hosts are updated one at a time, the one running the fewest jobs first.", label(h), p.target))
}

// blocked is the sentence for an open attempt, if there is one: while any is
// open, nothing new starts, so that one machine is updated at a time and the
// controller is never updated under a host's update or the other way round.
func (p *planner) blocked() (string, bool) {
	if a := p.controllerAttempt(); a != nil {
		if reason, ok := p.timedOut[a.ID]; ok {
			return reason, true
		}
		return fmt.Sprintf("The controller is being updated to %s; nothing else starts until that finishes.", tagOr(a.To, "a new release")), true
	}
	for _, h := range p.hosts {
		if a := hostAttempt(h); a != nil {
			if reason, ok := p.timedOut[a.ID]; ok {
				return reason, true
			}
			return fmt.Sprintf("%s is being updated to %s; nothing else starts until that finishes.", label(h), tagOr(a.To, "a new release")), true
		}
	}
	return "", false
}

// survey is where the hosts stand against the controller's release, in planning
// order.
type survey struct {
	// ready is the first host that can be updated now, or nil.
	ready *HostFacts
	// pending is why the first host that can be updated, but not yet, waits.
	pending string
	// note is the first thing worth saying about a host that is not updated.
	note string
	// updatable counts the hosts that can be updated now or soon.
	updatable int
}

func (p *planner) survey() survey {
	var sv survey
	for i := range p.hosts {
		h := &p.hosts[i]
		stand, note := p.assess(*h)
		switch stand {
		case standReady:
			sv.updatable++
			if sv.ready == nil {
				sv.ready = h
			}
		case standPending:
			sv.updatable++
			if sv.pending == "" {
				sv.pending = note
			}
		}
		if sv.note == "" {
			sv.note = note
		}
	}
	return sv
}

type standing int

const (
	standCurrent standing = iota // on the release: nothing to do and nothing to say
	standSkipped                 // left alone, with the reason
	standPending                 // to be updated once a wait is over
	standReady                   // can be updated now
)

// assess places one host. Every comparison with the release goes through
// CompareBuilds, because a release binary says 1.3.5 where its tag says v1.3.5.
func (p *planner) assess(h HostFacts) (standing, string) {
	name := label(h)
	if strings.TrimSpace(h.Version) == "" {
		return standSkipped, name + " has not said which version it runs, so it is left alone."
	}
	switch version.CompareBuilds(h.Version, p.target) {
	case version.SkewNone:
		return standCurrent, ""
	case version.SkewAhead:
		return standSkipped, fmt.Sprintf("%s runs a later release than the controller's %s and is never taken back.", name, p.target)
	case version.SkewDiffers:
		return standSkipped, name + " runs a build that is not a release, so it is left alone."
	}
	switch {
	case h.Embedded:
		return standSkipped, name + " is the agent inside the controller, so it is updated with the controller."
	case !h.CanSelfUpdate:
		if why := strings.TrimSpace(h.WhyNot); why != "" {
			return standSkipped, name + " cannot be updated from here. " + why
		}
		return standSkipped, name + " cannot be updated from here."
	case !p.carriesBinaryFor(h):
		return standSkipped, fmt.Sprintf("%s carries no binary for the system %s runs on, so it is left alone.", p.target, name)
	case h.Failures >= MaxFailuresPerTag:
		return standSkipped, fmt.Sprintf("%d attempts to update %s to %s have failed, so Zoomies stops trying; an operator must act. "+
			"Read why on the host's card, then press Update there or run sudo zoomies upgrade on the host.", h.Failures, name, p.target)
	case !h.Healthy:
		return standPending, name + " is not answering, so it is updated once it is back."
	case p.waiting(h.LastFailedAt):
		return standPending, fmt.Sprintf("%s's last update did not succeed; it is tried again in %s.", name, p.retryIn(h.LastFailedAt))
	}
	return standReady, ""
}

// carriesBinaryFor says whether the controller's release, as the list has it,
// has a binary for h's system. A release the list does not hold, or a system not
// known, is not held against the host: the download says, and the attempt fails
// with its reason.
func (p *planner) carriesBinaryFor(h HostFacts) bool {
	if h.GOOS == "" || h.GOARCH == "" {
		return true
	}
	listed := false
	for _, r := range p.releases {
		if !ValidTag(r.Tag) || r.Draft || r.Prerelease || version.CompareBuilds(r.Tag, p.target) != version.SkewNone {
			continue
		}
		if r.Complete(h.GOOS, h.GOARCH) {
			return true
		}
		listed = true
	}
	return !listed
}

func (p *planner) waiting(failedAt time.Time) bool {
	return !failedAt.IsZero() && p.s.Now.Before(failedAt.Add(RetryAfter))
}

func (p *planner) retryIn(failedAt time.Time) span {
	return spanOf(failedAt.Add(RetryAfter).Sub(p.s.Now))
}

// tagOr is tag when it is a release tag and fallback when it is not, so that a
// sentence never repeats text that only claimed to be one.
func tagOr(tag, fallback string) string {
	if ValidTag(tag) {
		return tag
	}
	return fallback
}

func label(h HostFacts) string {
	if h.Name != "" {
		return h.Name
	}
	return h.ID
}

func withNote(sentence, note string) string {
	if note == "" {
		return sentence
	}
	return sentence + " " + note
}
