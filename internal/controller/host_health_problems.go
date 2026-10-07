package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/naming"
	"github.com/eyupio/zoomies/internal/store"
)

// maxNamedFindings is how many checks an OS health problem names. The rest are
// counted: a title is read at a glance, and a host with a dozen findings is
// better served by its own page than by a dozen names in a drawer.
const maxNamedFindings = 3

// hostHealthReport is the report a host's OS health is judged on, or nil when
// there is nothing fair to say: no report yet, a host that is not connected
// (host.unhealthy speaks, and a report from before it went quiet describes a
// machine nobody can see), or a report that is only the container's view, which
// skips most checks and warns about the image's distribution for ever.
//
// The problems and the metrics share it so they cannot disagree about which
// hosts may speak: an alert on a metric that fires for a host the drawer says
// nothing about is the failure this is here to design out.
func hostHealthReport(h *store.Host, now time.Time) *hosttune.Report {
	r := h.Doctor.Report
	if r == nil || r.Container || !h.Healthy(now) {
		return nil
	}
	return r
}

// hostHealthProblems raises, for each connected host with a full OS report,
// what the report says needs attention, that the reports have stopped coming,
// and that a reboot is due.
//
// None of the three carries a Remedy. The controller never changes a host's
// operating system -- only a person on the host does, with consent, through
// zoomies doctor --interactive or zoomies tune -- and the autopilot only acts
// on problems that carry one, so each Fix is a sentence that sends a person to
// the host rather than a button.
func (c *Controller) hostHealthProblems(ctx context.Context, out *[]Problem) error {
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return fmt.Errorf("listing hosts: %w", err)
	}
	// Read once from the controller's own clock, never the store's: the
	// staleness of a report is a statement about this controller's view of the
	// host, and a test that moves the clock has to move all of it together.
	now := c.Now()
	for _, h := range hosts {
		r := hostHealthReport(h, now)
		if r == nil {
			continue
		}
		// A cordoned host is not skipped. A cordoned, idle host is exactly
		// where a pending reboot belongs, and cordoning says nothing about
		// whether its settings are right.
		if p, ok := osHealthProblem(h, r); ok {
			*out = append(*out, p)
		}
		if p, ok := healthStaleProblem(h, r, now); ok {
			*out = append(*out, p)
		}
		if p, ok := rebootPendingProblem(h, r); ok {
			*out = append(*out, p)
		}
	}
	return nil
}

// osHealthProblem is the checks that count -- the ones zoomies doctor counts by
// default -- that found something. A warning is a setting below the
// recommendation and an error is a check that could not run, so the severity
// follows the errors: a host with only warnings is one an operator should look
// at, not one to be woken for.
//
// Nothing the host wrote is repeated here. A report is the agent's own text and
// this reaches every signed-in viewer, so checks are named from this build's
// catalogue and never from the report, which also makes the sentence a function
// of the check ids alone. A "current" value such as the free disk space moves
// with every report, and text that moved would send the whole problems list to
// every open tab each time. There is no Since for the same reason: nothing
// records when a finding first appeared, and CheckedAt moves on every report.
func osHealthProblem(h *store.Host, r *hosttune.Report) (Problem, bool) {
	// A host name is the agent's to choose, so it goes into prose, never into a
	// code span the UI would offer to copy.
	label := naming.ForSentence(h.Name)
	findings := r.Findings()
	if len(findings) == 0 {
		return Problem{}, false
	}
	severity := config.SeverityWarning
	if r.Summary().Errors > 0 {
		severity = config.SeverityError
	}

	title := fmt.Sprintf("host %s has an OS health check that needs attention", label)
	if len(findings) > 1 {
		title = fmt.Sprintf("host %s has %s that need attention", label, plural(len(findings), "OS health check"))
	}
	detail := fmt.Sprintf("%s's latest OS report flags %s. "+
		"Only the safe checks count here -- the ones zoomies doctor counts by default -- "+
		"so settings in the aggressive and dedicated tiers, and anything optional, are suggestions "+
		"that show on the host's page and never raise a problem.", label, nameFindings(findings))
	if severity == config.SeverityError {
		detail += " A check that could not run failed to read what it looks at, or found a file that is not valid, " +
			"such as /etc/docker/daemon.json; that is different from a setting below the recommendation."
	}
	return Problem{
		Code:     "host.os_health",
		Severity: severity,
		Title:    title,
		Detail:   detail,
		Fix: fmt.Sprintf("on %s, run `sudo zoomies doctor --verbose` to see what each check found, why any could not run "+
			"and what would be better, then `sudo zoomies doctor --interactive` to be offered each fix it can make, "+
			"with the changes shown first. From another machine, the host's page has the command to read the report, with the "+
			"controller's address and a short-lived token already in it. Zoomies never changes a host's operating system "+
			"without that consent.", label),
		TargetKind: "host", TargetID: h.ID,
	}, true
}

// healthStaleProblem is a connected host that has stopped producing reports. It
// is judged against the controller's clock, so a host whose own clock is behind
// reads old, which is also true: what Zoomies shows of it cannot be trusted.
//
// A report up to a minute in the future is accepted on arrival, so its age here
// is negative and it is not stale. The boundary is strict: a report exactly
// ReportStaleAfter old is the last one that is not.
//
// A stale report does not silence the other two. A collector that has stopped
// does not fix an OS setting, and dropping them would turn "I can no longer see"
// into "all clear".
func healthStaleProblem(h *store.Host, r *hosttune.Report, now time.Time) (Problem, bool) {
	label := naming.ForSentence(h.Name)
	if now.Sub(r.CheckedAt) <= hosttune.ReportStaleAfter {
		return Problem{}, false
	}
	// The time of the newest report rather than how long ago it was, for the
	// reason host.unhealthy gives the time of the last heartbeat: the list is
	// sent only when it changes, and "for 11 minutes" would be a new problem
	// every minute the collector stays silent. Since is the same time, which
	// does not move while no report arrives.
	since := r.CheckedAt
	return Problem{
		Code:     "host.health_stale",
		Severity: config.SeverityWarning,
		Title: fmt.Sprintf("host %s has sent no OS health report for over %d minutes",
			label, int(hosttune.ReportStaleAfter.Minutes())),
		Detail: fmt.Sprintf("the newest OS health report from %s is dated %s by the host's own clock, "+
			"but the host is still sending heartbeats, so its health collector has stopped producing reports "+
			"and what Zoomies shows of its operating system may no longer be true. "+
			"The age is judged against the controller's clock, so a host whose clock is wrong reads old.",
			label, r.CheckedAt.UTC().Format(time.RFC3339)),
		Fix: fmt.Sprintf("on %s, restart the agent (`sudo systemctl restart zoomies-agent`) and check that its clock "+
			"is right (`timedatectl`). It clears with the next report.", label),
		TargetKind: "host", TargetID: h.ID, Since: &since,
	}, true
}

// rebootPendingProblem is an update that has been installed and not yet taken
// up. It is a note and not a fault: the host keeps working, and nothing here
// can reboot it.
//
// It is raised whether or not anything is running on the host, because a plain
// dismissal is dropped when its entry stops being reported, so an entry that
// appeared only while the host was idle would come back, and need dismissing
// again, every time the host swung from busy to idle. Title and detail are the
// same either way; only the fix says whether the host can be rebooted now.
func rebootPendingProblem(h *store.Host, r *hosttune.Report) (Problem, bool) {
	label := naming.ForSentence(h.Name)
	if !r.RebootPending {
		return Problem{}, false
	}
	// Cordon, not drain: drain cordons the host and then stops a runner that
	// is still busy after five minutes, which is the wrong advice for waiting
	// for jobs to finish. Both sentences are true whether or not the host is
	// already cordoned, so cordoning it does not make the text move a third way.
	// ActiveRunners is the field ListHosts fills; the sentence never says how
	// many, because a count would change with every runner that came and went.
	fix := fmt.Sprintf("nothing is running on %s, so it can be rebooted now. "+
		"If it is not cordoned, `zoomies hosts cordon %s` first keeps a job from landing on it meanwhile, "+
		"and `zoomies hosts uncordon %s` lets it take work again afterwards.", label, h.ID, h.ID)
	if h.ActiveRunners > 0 {
		fix = fmt.Sprintf("runners are still running on %s. "+
			"If it is not cordoned, `zoomies hosts cordon %s` stops new ones arriving; "+
			"reboot it once they have finished, then `zoomies hosts uncordon %s`. "+
			"Zoomies never reboots a host itself.", label, h.ID, h.ID)
	}
	return Problem{
		Code:     "host.reboot_pending",
		Severity: config.SeverityInfo,
		Title:    fmt.Sprintf("host %s is waiting for a reboot", label),
		Detail: fmt.Sprintf("an update installed on %s takes effect only after it restarts, usually a newer kernel "+
			"than the one running. It keeps working until then, so this is a note and not a fault. "+
			"Zoomies never reboots a host itself.", label),
		Fix:        fix,
		TargetKind: "host", TargetID: h.ID,
	}, true
}

// nameFindings says which checks a problem is about, in the order given -- errors
// first, because Findings puts them there -- naming at most maxNamedFindings and
// counting the rest.
//
// A name is the catalogue's own and never the report's. An id this build has
// never heard of -- from an agent newer than the controller, or one lying -- is
// counted in "and N more" and never named, so a host cannot put its own words in
// front of everyone who can read the problems list.
func nameFindings(fs []hosttune.Result) string {
	var named []string
	for _, x := range fs {
		if len(named) == maxNamedFindings {
			break
		}
		title, ok := hosttune.Title(x.ID)
		if !ok {
			continue
		}
		if x.Status == hosttune.Error {
			title += " (could not run)"
		}
		named = append(named, title)
	}
	if len(named) == 0 {
		return plural(len(fs), "check") + " this controller does not recognise"
	}
	if more := len(fs) - len(named); more > 0 {
		named = append(named, fmt.Sprintf("%d more", more))
	}
	if len(named) == 1 {
		return named[0]
	}
	last := len(named) - 1
	return strings.Join(named[:last], ", ") + " and " + named[last]
}
