package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/updates/channel"
	"github.com/eyupio/zoomies/internal/version"
)

const (
	// updateReportWindow is how long after the helper finished its result is
	// still news. A new process cannot know whether the one before it delivered
	// the result, so it sends what it finds once; a result older than this
	// belongs to an attempt the controller has closed by its own timeout.
	updateReportWindow = time.Hour
	// maxUpdateErrorRunes bounds the helper's sentence on the wire. The result
	// file can carry far more, and the rest is in the helper's log tail on the
	// host, which is where somebody reading a long failure goes anyway.
	maxUpdateErrorRunes = 1024
	// maxUpdateFieldRunes bounds an id, a tag or a version read back from the
	// result, each of which is a short name when the helper wrote it.
	maxUpdateFieldRunes = 64
	// maxRequestedBy is the most of requested_by the helper accepts.
	// internal/updates refuses anything longer and does not export its limit;
	// WriteRequest checks before it writes, so the two cannot drift apart unseen.
	maxRequestedBy = 128
	// helperInstallCommand is what installs the helper, run by somebody with root
	// on the host. Nothing the agent does can run it, which is the point.
	helperInstallCommand = `"sudo zoomies updates helper install"`
)

// updateDir is the update folder the helper on this host reads, and false when
// there is none to find.
func (a *Agent) updateDir() (string, bool) {
	if a.opts.UpdateDir == nil {
		return "", false
	}
	return a.opts.UpdateDir()
}

// selfUpdateReady says whether the helper is installed beside this agent. The
// marker is the installer's last step, so a folder without one is a helper that
// was never finished, and is treated as none.
func (a *Agent) selfUpdateReady() bool {
	dir, ok := a.updateDir()
	if !ok {
		return false
	}
	_, found, err := channel.ReadMarker(dir)
	return err == nil && found
}

// handleUpdate hands the helper on this host a request to take it to the
// task's release, and reports whether the request was written. Whether the
// update itself worked is not known here: the helper's upgrade restarts this
// process, and the next one reports the helper's result on its heartbeat.
//
// Only the task's attempt id and tag are taken from it. Both were checked by
// validateTask, and the helper, running as root, checks everything again.
func (a *Agent) handleUpdate(ctx context.Context, task Task) {
	// One at a time: WriteRequest's look for an earlier request is a courtesy and
	// not a lock, so two tasks writing at once could replace each other unseen.
	a.update.Lock()
	defer a.update.Unlock()
	if a.mutationsPaused.Load() {
		a.report(ctx, TaskResult{TaskID: task.ID, Kind: task.Kind, NotStarted: true, CompletedAt: a.now(),
			Error: "the controller has paused every change on this host while it recovers, so the update was not asked for; it is safe to send again"})
		return
	}
	if ctx.Err() != nil {
		a.reportNotStarted(ctx, task)
		return
	}

	running, tag := version.Version, task.UpdateTag
	switch version.CompareBuilds(running, tag) {
	case version.SkewNone, version.SkewAhead:
		// An agent is never taken backwards, whatever the controller asks: an
		// agent ahead of its controller is merely untested, and one taken back
		// across a release could meet a state file it cannot read.
		a.log.Info("asked to update to a release this agent already runs or is past; nothing to do",
			"attempt", task.UpdateID, "running", running, "tag", tag)
		a.answerUpdate(ctx, task, "")
		return
	case version.SkewDiffers:
		a.answerUpdate(ctx, task, "this agent runs the build "+fieldText(running)+
			", which is not a release, so it cannot tell whether "+tag+" is newer and does not ask for it; update it on the host with zoomies upgrade")
		return
	}

	dir, ok := a.updateDir()
	if !ok {
		a.answerUpdate(ctx, task, "this host has no update folder, so there is no update helper to run the update; run "+
			helperInstallCommand+" on the host")
		return
	}
	switch _, found, err := channel.ReadMarker(dir); {
	case err != nil:
		a.log.Warn("could not read the update helper's marker", "attempt", task.UpdateID, "error", err)
		a.answerUpdate(ctx, task, "the update helper's marker on this host cannot be read, so the helper is treated as missing; run "+
			helperInstallCommand+" on the host to install it again")
		return
	case !found:
		a.answerUpdate(ctx, task, "the update helper is not installed on this host, so nothing would read the request; run "+
			helperInstallCommand+" on the host")
		return
	}

	req := updates.Request{
		V: updates.WireVersion, ID: task.UpdateID, Tag: tag,
		RequestedBy: a.updateRequestedBy(), RequestedAt: a.now().UTC(),
	}
	// WriteRequest refuses this too, but in a sentence about the folder; a
	// request the helper would refuse is the controller's mistake, not the host's.
	if body, err := json.Marshal(req); err != nil {
		a.answerUpdate(ctx, task, "the update request could not be encoded, so nothing was written: "+err.Error())
		return
	} else if _, err := updates.ParseRequest(body); err != nil {
		a.answerUpdate(ctx, task, "the controller asked for an update this host's helper would refuse, so nothing was written: "+err.Error())
		return
	}
	if err := channel.WriteRequest(dir, req); err != nil {
		// The full error names the folder, which is for this host's log. The
		// sentence goes to a page every role reads, so it names only the fix.
		a.log.Warn("could not hand the update request to the helper", "attempt", task.UpdateID, "tag", tag, "error", err)
		if errors.Is(err, channel.ErrRequestPending) {
			a.answerUpdate(ctx, task, "an earlier update request is still waiting in this host's update folder, so the helper has not read it and this one was not written; "+
				`check the helper with "sudo zoomies updates helper status" on the host, and remove the waiting request.json if the helper is not going to read it`)
			return
		}
		a.answerUpdate(ctx, task, "the update request could not be written to this host's update folder, so nothing ran; run "+
			helperInstallCommand+" on the host, which creates the folder with the right owner, and see the agent's log there for the reason")
		return
	}
	a.log.Info("asked the update helper to update this host", "attempt", task.UpdateID, "from", running, "to", tag)
	a.answerUpdate(ctx, task, "")
}

// answerUpdate reports what became of an update task: written, or why not. It
// names no runner and carries no runner state, because the task is the host's,
// and a failure counted against a runner would be counted against its pool.
func (a *Agent) answerUpdate(ctx context.Context, task Task, failure string) {
	if failure != "" {
		a.log.Warn("did not ask the update helper for an update", "attempt", task.UpdateID, "tag", task.UpdateTag, "reason", failure)
	}
	a.report(ctx, TaskResult{TaskID: task.ID, Kind: task.Kind, OK: failure == "", Error: failure, CompletedAt: a.now()})
}

// updateRequestedBy is who the helper's log says asked. The agent does not know
// which person pressed the button, only that its controller asked; the
// controller's own record of the attempt says who.
func (a *Agent) updateRequestedBy() string {
	return cutBytes(printableText("the controller, through the agent on "+a.opts.Name), maxRequestedBy)
}

// updateReport is the helper's last result, for the next heartbeat to carry, or
// nil when there is nothing new to say. A result is carried until a beat that
// carried it has been answered, then not again by this process.
//
// result.json is written by root, but the folder belongs to the service account,
// so anything in it may have been put there by something else. It is read with
// the channel's limits and every field is cut to size and stripped of what a page
// must not show; whether it answers an attempt is the controller's to decide.
func (a *Agent) updateReport() *UpdateReport {
	dir, ok := a.updateDir()
	if !ok {
		return nil
	}
	res, found, err := channel.ReadResult(dir)
	if err != nil {
		a.log.Debug("ignoring a result in the update folder that cannot be read", "error", err)
		return nil
	}
	if !found || res.FinishedAt.IsZero() {
		return nil
	}
	if age := a.now().Sub(res.FinishedAt); age > updateReportWindow || age < -updateReportWindow {
		return nil
	}
	id := fieldText(res.ID)
	a.mu.Lock()
	delivered := a.updateDelivered[id]
	a.mu.Unlock()
	if delivered {
		return nil
	}
	return &UpdateReport{
		ID: id, OK: res.OK, Tag: fieldText(res.Tag), From: fieldText(res.From), To: fieldText(res.To),
		Error:      truncateRunes(printableText(res.Error), maxUpdateErrorRunes),
		FinishedAt: res.FinishedAt.UTC(),
	}
}

// markUpdateDelivered records that a heartbeat carrying the result with this id
// was answered, so later beats leave it out. It is called only after the beat
// succeeded: a beat that failed may never have reached the controller.
func (a *Agent) markUpdateDelivered(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.updateDelivered == nil {
		a.updateDelivered = make(map[string]bool)
	}
	a.updateDelivered[id] = true
}

// printableText is s with what a terminal or a page would act on taken out: a
// line break or a tab becomes a space, and an escape sequence, a direction
// override or any other unprintable character is dropped.
func printableText(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		switch {
		case unicode.IsGraphic(r):
			return r
		case unicode.IsSpace(r):
			return ' '
		}
		return -1
	}, s))
}

// fieldText is a short field from a file the agent did not write, made safe to
// pass on and cut to the length a name has.
func fieldText(s string) string {
	return truncateRunes(printableText(s), maxUpdateFieldRunes)
}

// cutBytes is s cut to at most limit bytes, at a character boundary.
func cutBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	end := limit
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return strings.TrimSpace(s[:end])
}
