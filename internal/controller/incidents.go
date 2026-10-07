package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/naming"
	"github.com/eyupio/zoomies/internal/store"
)

// maxIncidentError bounds what an agent's error may put on the host row and
// the Hosts page. The agent bounds it too; an agent is not trusted to.
const maxIncidentError = 500

// maxRuntimeRetry is the longest retry an agent's report is believed about.
// The agent's own ceiling is a minute plus jitter; anything past this is an
// agent reporting nonsense, and a retry time an hour out would read as a host
// nobody needs to look at.
const maxRuntimeRetry = 5 * time.Minute

// noteRuntime folds the heartbeat's runtime report into the host row.
//
// It writes only when the report says something new -- a first failure, a
// further one, a different kind, or a recovery -- so a host sitting in a
// cooldown for a minute costs one write rather than one per beat, and the
// times on the row are when the controller learnt each thing rather than
// when it last happened to be told again.
//
// A beat with no report clears it. That is a healthy runtime from this
// release, and from an agent too old to send one it is the only thing the
// controller has ever known about that host's runtime.
func (c *Controller) noteRuntime(ctx context.Context, h *store.Host, rep *agent.RuntimeReport, now time.Time) {
	was := h.Incidents.Runtime
	if rep == nil || rep.Failures <= 0 {
		if was == nil {
			return
		}
		if err := c.st.SetHostRuntimeIncident(ctx, h.ID, nil); err != nil {
			c.log.Warn("could not clear a host's runtime incident", "host", h.ID, "error", err)
			return
		}
		h.Incidents.Runtime = nil
		c.log.Info("a host's container runtime has recovered", "host", h.ID, "name", h.Name,
			"after_failures", was.Failures, "since", was.Since)
		c.publishHost(h)
		return
	}
	kind := rep.Kind
	if kind != agent.RuntimeTimeout {
		kind = agent.RuntimeUnavailable
	}
	failures := min(rep.Failures, 99)
	if was != nil && was.Failures == failures && was.Kind == kind {
		return
	}
	inc := &store.RuntimeIncident{
		Failures:   failures,
		Kind:       kind,
		Error:      truncateIncident(rep.Error),
		RetryAt:    now.Add(min(max(rep.RetryIn, 0), maxRuntimeRetry)),
		Since:      now,
		ObservedAt: now,
	}
	added := failures
	if was != nil {
		inc.Since = was.Since
		added = max(failures-was.Failures, 0)
	}
	if err := c.st.SetHostRuntimeIncident(ctx, h.ID, inc); err != nil {
		c.log.Warn("could not record a host's runtime incident", "host", h.ID, "error", err)
		return
	}
	h.Incidents.Runtime = inc
	if added > 0 {
		c.metrics.runtimeFailures.WithLabelValues(kind).Add(float64(added))
	}
	c.log.Warn("a host's container runtime failed; its agent is holding new starts before one recovery attempt",
		"host", h.ID, "name", h.Name, "consecutive_failures", failures, "kind", kind,
		"retry_at", inc.RetryAt, "error", inc.Error)
	c.publishHost(h)
}

// noteImagePull records a start or prewarm that failed because its image
// could not be made ready, or clears the record when the same pool's image is
// made ready on the same host.
//
// It is recorded from the task results the agent already sends, because the
// failed runner itself is cleaned up within minutes; before this, a host
// whose egress blocked a registry showed nothing but runners that never
// registered.
func (c *Controller) noteImagePull(ctx context.Context, hostID, poolID, image, source string, ok bool, fault store.FaultKind, message string) {
	if hostID == "" || poolID == "" {
		return
	}
	if !ok && fault != store.FaultImage {
		return
	}
	h, err := c.st.GetHost(ctx, hostID)
	if err != nil {
		return
	}
	was := h.Incidents.ImagePull
	now := c.Now()
	if ok {
		// Only the pool that failed clears it: another pool's image being
		// fine says nothing about a registry this one cannot reach.
		if was == nil || was.PoolID != poolID {
			return
		}
		if err := c.st.SetHostImagePullIncident(ctx, hostID, nil); err != nil {
			c.log.Warn("could not clear a host's image pull incident", "host", hostID, "error", err)
			return
		}
		h.Incidents.ImagePull = nil
		c.log.Info("a pool's image is pulling on this host again", "host", hostID, "pool", poolID, "registry", was.Registry)
		c.publishHost(h)
		return
	}
	pool := poolID
	if p, err := c.st.GetPool(ctx, poolID); err == nil {
		pool = p.Name
		if image == "" {
			image = p.Image
		}
	}
	inc := &store.ImagePullIncident{
		PoolID:     poolID,
		Pool:       pool,
		Image:      image,
		Registry:   backend.RegistryHost(image),
		Source:     source,
		Error:      truncateIncident(message),
		Since:      now,
		ObservedAt: now,
	}
	if was != nil && was.PoolID == poolID && was.Image == image {
		inc.Since = was.Since
	}
	if err := c.st.SetHostImagePullIncident(ctx, hostID, inc); err != nil {
		c.log.Warn("could not record a host's image pull incident", "host", hostID, "error", err)
		return
	}
	h.Incidents.ImagePull = inc
	c.metrics.imagePullFailures.WithLabelValues(c.poolLabel(poolID), source).Inc()
	c.log.Warn("a host could not pull a pool's image", "host", hostID, "name", h.Name,
		"pool", pool, "image", image, "registry", inc.Registry, "source", source, "error", inc.Error)
	c.publishHost(h)
}

func truncateIncident(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= maxIncidentError {
		return string(r)
	}
	return string(r[:maxIncidentError]) + "..."
}

// ordinalFailure is "third failure", the way the card says it. The agent
// counts no higher than five, so five is "at least the fifth".
func ordinalFailure(n int) string {
	words := []string{"first", "second", "third", "fourth"}
	switch {
	case n >= 1 && n <= len(words):
		return words[n-1] + " failure"
	case n == 5:
		return "fifth failure or later"
	default:
		return fmt.Sprintf("failure %d", n)
	}
}

// runtimeReason is the incident as the host card's sentence, without its
// times: the card renders the retry and the age against the viewer's clock,
// so the sentence stays true while the page is open.
func runtimeReason(inc *store.RuntimeIncident) string {
	if inc == nil {
		return ""
	}
	what := "the container runtime could not be reached"
	if inc.Kind == agent.RuntimeTimeout {
		what = "the container runtime did not answer in time"
	}
	return fmt.Sprintf("Runtime recovering: %s in a row, %s. New starts here are held until one recovery attempt; running jobs continue.",
		ordinalFailure(inc.Failures), what)
}

// runtimeFix is the fix the agent itself would give, by kind: a daemon that
// is not there is started on the host, and one that is there but slow is
// relieved of load.
func runtimeFix(hostName string, inc *store.RuntimeIncident) string {
	if inc.Kind == agent.RuntimeTimeout {
		return fmt.Sprintf("the runtime on %s is running but not answering in time: check its load and its disk "+
			"(`docker info`, `df -h /var/lib/docker`), and lower the host's capacity if it carries more than the machine can. "+
			"The agent retries by itself, and this clears on the next start that succeeds.", hostName)
	}
	return fmt.Sprintf("start the container runtime on %s, or give the agent's user access to its socket -- the error names "+
		"the socket and how to start it (for Docker, `sudo systemctl start docker`). "+
		"The agent retries by itself, and this clears on the next start that succeeds.", hostName)
}

func incidentTime(t time.Time) string { return t.UTC().Format("15:04:05 UTC") }

// hostIncidentProblems names each host whose runtime is recovering and each
// host that could not pull a pool's image, from what is stored on the host
// row -- so a controller that restarts in the middle of either still says so.
//
// The times in them are absolute rather than "in 40 s": the list is sent only
// when it changes, and a sentence counting down would be wrong by the time it
// was read. The card counts down against the viewer's own clock instead.
func (c *Controller) hostIncidentProblems(ctx context.Context, out *[]Problem) error {
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return fmt.Errorf("listing hosts: %w", err)
	}
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}
	live := make(map[string]bool, len(pools))
	for _, p := range pools {
		live[p.ID] = true
	}
	for _, h := range hosts {
		label := naming.ForSentence(h.Name)
		if inc := h.Incidents.Runtime; inc != nil {
			since := inc.Since
			detail := fmt.Sprintf("%s's agent reported its %s in a row at %s and holds new starts until one recovery attempt at %s. "+
				"Running jobs continue.", label, ordinalFailure(inc.Failures), incidentTime(inc.ObservedAt), incidentTime(inc.RetryAt))
			if inc.Error != "" {
				detail += " The last error: " + inc.Error
			}
			*out = append(*out, Problem{
				Code:     "host.runtime_recovering",
				Severity: config.SeverityWarning,
				Title:    fmt.Sprintf("the container runtime on %s is recovering after its %s", label, ordinalFailure(inc.Failures)),
				Detail:   detail,
				Fix:      runtimeFix(label, inc),
				// Since is when the controller first heard of it; the
				// detail carries when it last heard anything new.
				TargetKind: "host", TargetID: h.ID, Since: &since,
			})
		}
		if inc := h.Incidents.ImagePull; inc != nil && live[inc.PoolID] {
			since := inc.Since
			what := "a runner start"
			if inc.Source == "prewarm" {
				what = "a prewarm"
			}
			detail := fmt.Sprintf("%s of %s for pool %s failed on %s at %s because the image could not be made ready. "+
				"Every runner that pool places on this host fails the same way until it can.",
				what, inc.Image, inc.Pool, label, incidentTime(inc.ObservedAt))
			if inc.Error != "" {
				detail += " The error: " + inc.Error
			}
			*out = append(*out, Problem{
				Code:     "host.image_pull_failed",
				Severity: config.SeverityWarning,
				Title:    fmt.Sprintf("%s cannot pull pool %s's image from %s", label, inc.Pool, inc.Registry),
				Detail:   detail,
				Fix: fmt.Sprintf("check that %s can reach %s -- its egress rules, proxy and DNS -- and is logged in to it if the image "+
					"is private (`docker pull %s` on the host gives the daemon's own answer), and that the pool's image and tag exist. "+
					"This clears on the next start or prewarm of %s that succeeds on this host.", label, inc.Registry, inc.Image, inc.Pool),
				TargetKind: "host", TargetID: h.ID, Since: &since,
			})
		}
	}
	return nil
}
