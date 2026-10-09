package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/naming"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// hostResponse is the shape GET /hosts returns, rendered by the controller
// so the event stream's host.updated frames are the same JSON. See
// controller/views.go for why the renderer lives there.
type hostResponse = controller.HostView

// backendInfoResponse is one entry of hostResponse.backend_info.
type backendInfoResponse = controller.BackendInfoView

// handleListHosts answers GET /api/v1/hosts.
func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.ctrl.Store().ListHosts(r.Context())
	if err != nil {
		s.internal(w, r, "listing hosts", err)
		return
	}
	out := make([]hostResponse, 0, len(hosts))
	platform := isPlatform(r)
	for _, h := range hosts {
		out = append(out, s.ctrl.HostView(h).For(platform))
	}
	writeJSON(w, http.StatusOK, newList(out))
}

// handleListHostSamples answers GET /api/v1/hosts/samples: every host's
// minute samples, for the capacity map. It takes `since` or `window` exactly
// as /samples does, and `host_id` narrows it to one host.
func (s *Server) handleListHostSamples(w http.ResponseWriter, r *http.Request) {
	since, err := queryTime(r, "since")
	if err != nil {
		badRequestField(w, "since", err.Error())
		return
	}
	if since == nil {
		window, werr := queryDuration(r, "window", defaultStatsWindow)
		if werr != nil {
			badRequestField(w, "window", werr.Error())
			return
		}
		t := s.ctrl.Now().Add(-window)
		since = &t
	}
	samples, err := s.ctrl.HostSamples(r.Context(), *since, r.URL.Query().Get("host_id"))
	if err != nil {
		s.internal(w, r, "reading host samples", err)
		return
	}
	writeJSON(w, http.StatusOK, newList(samples))
}

// handleGetHost answers GET /api/v1/hosts/{id}.
func (s *Server) handleGetHost(w http.ResponseWriter, r *http.Request) {
	h, err := s.ctrl.Store().GetHost(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the host", err)
		return
	}
	writeJSON(w, http.StatusOK, s.ctrl.HostView(h).For(isPlatform(r)))
}

type hostUpdateRequest struct {
	// Name is what the host is called in the UI, the CLI and a pool's
	// reasons. It is the operator's to change: the agent's configured name only
	// names the host when it first joins.
	Name     *string            `json:"name"`
	Capacity *int               `json:"capacity"`
	Labels   *map[string]string `json:"labels"`
	// The reserve: what this machine keeps for itself, in the units its own
	// figures are reported in. Each is optional and each is separate -- an
	// operator who holds back memory has said nothing about CPU.
	ReserveCPUs     *int   `json:"reserve_cpus"`
	ReserveMemoryMB *int64 `json:"reserve_memory_mb"`
	ReserveDiskMB   *int64 `json:"reserve_disk_mb"`
	// RunnerProfile replaces the host's whole runner profile. Leave it out to
	// change nothing; send an empty object, {}, to clear it so the host follows
	// the fleet's settings again. A field left out of a profile that is sent
	// follows the fleet's setting too -- it is the whole profile that is
	// replaced, not the fields that were mentioned.
	RunnerProfile *store.RunnerProfile `json:"runner_profile"`
}

// handleUpdateHost changes a host's name, capacity, labels, reserve or runner profile.
func (s *Server) handleUpdateHost(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	h, err := s.ctrl.Store().GetHost(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the host", err)
		return
	}

	var req hostUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	s.applyHostUpdate(w, r, id, h, &req)
}

// applyHostUpdate is the half of a host's update that comes after its request has
// been read, for the reason applyPoolUpdate is: a remedy is applied by this, and
// so is every PATCH, and they cannot disagree about what a change to a host checks.
func (s *Server) applyHostUpdate(w http.ResponseWriter, r *http.Request, id string, h *store.Host, req *hostUpdateRequest) {
	var err error
	var fields []fieldError
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		req.Name = &name
		// The rule is the join's own, so a name one door refuses cannot be put
		// on a host through the other.
		switch nameErr := naming.ValidateHostName(name); {
		case nameErr != nil:
			fields = append(fields, fieldError{"name", nameErr.Error()})
		case name != h.Name:
			if other, gerr := s.ctrl.Store().GetHostByName(r.Context(), name); gerr == nil && other.ID != h.ID {
				fields = append(fields, fieldError{"name", fmt.Sprintf("another host is already called %q; pick a name that is not in use", name)})
			}
		}
	}
	if req.Capacity != nil {
		switch {
		case *req.Capacity < 0:
			fields = append(fields, fieldError{"capacity", "capacity cannot be negative; use 0 to stop this host taking new runners"})
		case *req.Capacity < h.ActiveRunners:
			// Allowed, and worth saying plainly rather than refusing: the
			// running jobs finish, and nothing new is placed until the host is
			// back under its new capacity.
			s.logger(r).Info("a host's capacity was lowered below the number of runners it is already running",
				"host", h.ID, "capacity", *req.Capacity, "active", h.ActiveRunners)
		}
	}
	if req.Labels != nil {
		for k := range *req.Labels {
			if strings.TrimSpace(k) == "" {
				fields = append(fields, fieldError{"labels", "a label needs a name"})
				break
			}
		}
		if f := s.sizeTagField(*req.Labels); f != nil {
			fields = append(fields, *f)
		}
	}
	// A reserve larger than the machine is refused rather than clamped: it
	// leaves nothing placeable, and an operator who typed megabytes where they
	// meant gigabytes should be told, not quietly obeyed. A reserve on a
	// figure the host has never reported is refused for the same reason --
	// there is nothing to hold back a share of, and the scheduler would ignore
	// it while the page showed it.
	if req.ReserveCPUs != nil {
		switch {
		case *req.ReserveCPUs < 0:
			fields = append(fields, fieldError{"reserve_cpus", "a reserve cannot be negative"})
		case h.CPUs <= 0 && *req.ReserveCPUs > 0:
			fields = append(fields, fieldError{"reserve_cpus", "this host has not reported how many CPUs it has, so there is nothing to hold back; upgrade its agent"})
		case h.CPUs > 0 && *req.ReserveCPUs >= h.CPUs:
			fields = append(fields, fieldError{"reserve_cpus", fmt.Sprintf("this host has %d CPUs; a reserve of %d would leave nothing to place on", h.CPUs, *req.ReserveCPUs)})
		}
	}
	if req.ReserveMemoryMB != nil {
		switch {
		case *req.ReserveMemoryMB < 0:
			fields = append(fields, fieldError{"reserve_memory_mb", "a reserve cannot be negative"})
		case h.MemoryMB <= 0 && *req.ReserveMemoryMB > 0:
			fields = append(fields, fieldError{"reserve_memory_mb", "this host has not reported how much memory it has, so there is nothing to hold back; upgrade its agent"})
		case h.MemoryMB > 0 && *req.ReserveMemoryMB >= h.MemoryMB:
			fields = append(fields, fieldError{"reserve_memory_mb", fmt.Sprintf("this host has %d MB of memory; a reserve of %d would leave nothing to place on", h.MemoryMB, *req.ReserveMemoryMB)})
		}
	}
	if req.ReserveDiskMB != nil {
		switch {
		case *req.ReserveDiskMB < 0:
			fields = append(fields, fieldError{"reserve_disk_mb", "a reserve cannot be negative"})
		case h.DiskTotalMB <= 0 && *req.ReserveDiskMB > 0:
			fields = append(fields, fieldError{"reserve_disk_mb", "this host has not reported its work directory's disk, so there is nothing to hold back; upgrade its agent"})
		case h.DiskTotalMB > 0 && *req.ReserveDiskMB >= h.DiskTotalMB:
			fields = append(fields, fieldError{"reserve_disk_mb", fmt.Sprintf("this host's work directory has %d MB of disk; a reserve of %d would leave nothing to place on", h.DiskTotalMB, *req.ReserveDiskMB)})
		}
	}
	if req.RunnerProfile != nil {
		fields = append(fields, validateRunnerProfile(h, *req.RunnerProfile)...)
	}
	if len(fields) > 0 {
		unprocessable(w, "this host cannot be changed as described", fields)
		return
	}

	// A host's settings and a pool's requirements are edited on different
	// pages, so neither form can show the other half. An edit that takes away
	// the last machine a pool could ever run on is refused here rather than
	// discovered an hour later as a job that never started; confirm=true is
	// the operator saying they meant it, which is right when the pool is on
	// its way out.
	if !queryBool(r, "confirm", false) {
		proposed := *h
		if req.Capacity != nil {
			proposed.Capacity = *req.Capacity
		}
		if req.ReserveCPUs != nil {
			proposed.ReserveCPUs = *req.ReserveCPUs
		}
		if req.ReserveMemoryMB != nil {
			proposed.ReserveMemoryMB = *req.ReserveMemoryMB
		}
		if req.ReserveDiskMB != nil {
			proposed.ReserveDiskMB = *req.ReserveDiskMB
		}
		if req.Labels != nil {
			proposed.Labels = store.StringMap(*req.Labels)
		}
		if req.RunnerProfile != nil {
			proposed.RunnerProfile = *req.RunnerProfile
		}
		stranded, serr := s.ctrl.HostStrandings(r.Context(), &proposed)
		if serr != nil {
			s.internal(w, r, "checking which pools this change would leave with nowhere to run", serr)
			return
		}
		if len(stranded) > 0 {
			conflict(w, hostStrandingRefusal(stranded[0].Host, stranded, strandingEnding(r)))
			return
		}
	}

	before := *h
	changes := store.HostChanges{
		Name: req.Name, Capacity: req.Capacity, ReserveCPUs: req.ReserveCPUs,
		ReserveMemoryMB: req.ReserveMemoryMB, ReserveDiskMB: req.ReserveDiskMB,
		RunnerProfile: req.RunnerProfile,
	}
	if req.Labels != nil {
		labels := store.StringMap(*req.Labels)
		changes.Labels = &labels
	}
	if err := s.ctrl.Store().PatchHost(r.Context(), id, changes); err != nil {
		s.fail(w, r, "saving the host", err)
		return
	}
	h, err = s.ctrl.Store().GetHost(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the host back", err)
		return
	}

	// A change to the capacity, the reserve or the runner profile is the
	// operator answering the pressure that throttled the host -- fewer slots,
	// more of the machine kept back, or runners of another size -- so the rung
	// it was on is lifted with it. Left standing, the
	// new figures would only take effect once the old episode had spent five
	// minutes calm, and the card would show a throttle nothing explains. A
	// label change says nothing about the machine and lifts nothing, and
	// neither does a request that repeats the figures the host already has:
	// the edit dialog sends every field it shows, and only a figure that
	// moved is an answer. When the
	// lift itself fails the patch still stands, so the answer is the saved
	// host and a log line, not a 500 that reads as "nothing was saved".
	cleared := false
	resized := before.Capacity != h.Capacity || before.ReserveCPUs != h.ReserveCPUs ||
		before.ReserveMemoryMB != h.ReserveMemoryMB || before.ReserveDiskMB != h.ReserveDiskMB ||
		before.RunnerProfile != h.RunnerProfile
	if h.Throttle.Active() && resized {
		if lifted, err := s.ctrl.ClearHostThrottle(r.Context(), id); err != nil {
			s.logger(r).Warn("a host's capacity or reserve changed but its throttle could not be lifted; the next heartbeat decides it again",
				"host", id, "error", err)
		} else {
			h, cleared = lifted, true
		}
	}

	s.auditHostUpdate(r, id, &before, h, cleared)
	s.ctrl.PublishHost(h)
	// Capacity, labels and the reserve all decide where runners may be placed.
	s.ctrl.Nudge()
	writeJSON(w, http.StatusOK, s.ctrl.HostView(h).For(isPlatform(r)))
}

// auditHostUpdate writes the PATCH's audit row: the fields that differ, as
// Auditor.Updated would, plus "throttle_cleared" when the change lifted one.
// The throttle column already appears in the diff, but a reader of the audit
// page should not have to know that {} against {level: 2} means "lifted".
func (s *Server) auditHostUpdate(r *http.Request, id string, before, after *store.Host, cleared bool) {
	b, af := auth.Diff(before, after)
	if cleared {
		detail, _ := af.(map[string]any)
		if detail == nil {
			detail = map[string]any{}
		}
		detail["throttle_cleared"] = true
		af = detail
	}
	if b == nil && af == nil {
		// Nothing changed, and Updated would have written nothing either.
		return
	}
	_ = s.auth.Auditor().Record(r.Context(), Identity(r.Context()), "host.update", "host", id, b, af)
}

// handleClearHostThrottle lifts a host's throttle by hand.
//
// It is for a host whose cause is known and fixed: the runaway job was
// cancelled, the daemon was restarted. Nothing pins the throttle the other
// way, so a host still under pressure is stepped back up by its next
// heartbeat, and the audit row says who lifted it early. A host on no rung is
// answered as it is, and nothing is written about it -- exactly as an
// uncordon of an uncordoned host is.
func (s *Server) handleClearHostThrottle(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	h, err := s.ctrl.Store().GetHost(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the host", err)
		return
	}
	if was := h.Throttle; was.Active() {
		effective := h.EffectiveCapacity()
		h, err = s.ctrl.ClearHostThrottle(r.Context(), id)
		if err != nil {
			s.fail(w, r, "lifting the host's throttle", err)
			return
		}
		s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "host.throttle_clear", "host", id, map[string]any{
			"name": h.Name, "level": was.Level, "reason": was.Reason, "since": was.Since,
			"effective_capacity": effective, "capacity": h.Capacity,
		})
	}
	writeJSON(w, http.StatusOK, s.ctrl.HostView(h).For(isPlatform(r)))
}

type cordonRequest struct {
	Cordoned bool `json:"cordoned"`
}

// handleCordonHost cordons or uncordons a host.
//
// A cordoned host keeps everything it is already running: cordoning is how an
// operator drains a machine before maintenance without interrupting the jobs on
// it, and killing them would defeat the purpose.
func (s *Server) handleCordonHost(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	h, err := s.ctrl.Store().GetHost(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the host", err)
		return
	}

	var req cordonRequest
	if !decode(w, r, &req) {
		return
	}
	if h.Cordoned != req.Cordoned {
		if err := s.ctrl.Store().SetHostCordoned(r.Context(), id, req.Cordoned); err != nil {
			s.fail(w, r, "cordoning the host", err)
			return
		}
		h.Cordoned = req.Cordoned
		action := "host.uncordon"
		if req.Cordoned {
			action = "host.cordon"
		}
		s.auth.Auditor().Act(r.Context(), Identity(r.Context()), action, "host", id, map[string]any{
			"name": h.Name, "cordoned": req.Cordoned, "active_runners": h.ActiveRunners,
		})
		s.ctrl.PublishHost(h)
		s.ctrl.Nudge()
	}
	writeJSON(w, http.StatusOK, s.ctrl.HostView(h).For(isPlatform(r)))
}

// handleCheckHost answers POST /api/v1/hosts/{id}/health-check.
//
// It queues a request and returns at once, because the agent answers on its own
// poll and a browser should not hold a connection for the checks to finish; the
// page learns the outcome from the host's event frames. Only a request that
// queued a task is audited: a repeat press or a refusal changed nothing, and an
// audit row per press would let a held-down button fill the log.
func (s *Server) handleCheckHost(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	h, queued, err := s.ctrl.RequestHostCheck(r.Context(), id)
	if err != nil {
		var refused *controller.HostCheckError
		var cooling *controller.HostCheckCooldownError
		switch {
		case errors.As(err, &refused):
			conflict(w, refused.Message)
		case errors.As(err, &cooling):
			rateLimited(w, cooling.Error(), max(cooling.Until.Sub(s.ctrl.Now()), time.Second))
		default:
			s.fail(w, r, "asking the host to check", err)
		}
		return
	}
	if queued {
		s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "host.check_requested", "host", id, map[string]any{"name": h.Name})
	}
	writeJSON(w, http.StatusAccepted, s.ctrl.HostView(&h).For(isPlatform(r)))
}

// handleRequestHostUpdate answers POST /api/v1/hosts/{id}/update: ask the host's
// agent to have its update helper take it to this controller's release.
//
// It answers 202 with the host, whose update block holds the attempt, because the
// request is made and nothing is done: the task goes out on the agent's own poll
// and the page learns how it went from the host's frames. The audit row is written
// once the controller has said yes, so a refused press, which changed nothing,
// leaves none. The row names the host and the two releases and nothing the helper
// wrote, which can be a path on the host, because an administrator reads the audit
// log and only the platform reads that.
func (s *Server) handleRequestHostUpdate(w http.ResponseWriter, r *http.Request) {
	if !decodeNoUpdateBody(w, r) {
		return
	}
	id := chiURLParam(r, "id")
	ident := Identity(r.Context())
	view, err := s.ctrl.RequestHostUpdate(r.Context(), controller.UpdateActor{ID: ident.ID, Name: ident.Name}, id)
	if err != nil {
		s.failUpdate(w, r, err)
		return
	}
	// Detached from the request: the task may already be queued, and a browser that
	// gave up waiting must not leave it with no record of who asked.
	ctx := context.WithoutCancel(r.Context())
	attemptID, detail := "", map[string]any{"host_id": id}
	if view.Update != nil && view.Update.AttemptID != "" {
		attemptID = view.Update.AttemptID
		if latest, err := s.ctrl.Store().ListUpdateAttempts(ctx, store.UpdateScopeHost, id, 1); err == nil && len(latest) == 1 && latest[0].ID == attemptID {
			detail["from"], detail["to"] = latest[0].FromVersion, latest[0].ToVersion
		}
	}
	s.auth.Auditor().Act(ctx, ident, "update.host_requested", "update", attemptID, detail)
	writeJSON(w, http.StatusAccepted, view.For(isPlatform(r)))
}

// handleDeleteHost removes a host.
//
// It refuses while the host still has live runners unless forced, because
// deleting the row cascades to those runners and their workloads would be left
// running on a machine Zoomies no longer knows about. It refuses a host that is
// a machine Zoomies rented for a second reason: the row is what says a VM
// exists and who is paying for it, and forgetting it here would leave the VM
// running with nothing tracking it.
func (s *Server) handleDeleteHost(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	force := queryBool(r, "force", false)

	h, err := s.ctrl.Store().GetHost(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the host", err)
		return
	}
	if !force {
		switch m, merr := s.ctrl.Store().GetMachineByHost(r.Context(), id); {
		case merr == nil && m != nil:
			rentedFrom := m.ProviderID
			if row, perr := s.ctrl.Store().GetProvider(r.Context(), m.ProviderID); perr == nil {
				rentedFrom = row.Name
			}
			conflict(w, fmt.Sprintf("host %s is a machine Zoomies created on %s. Delete the machine instead "+
				"(that removes the VM too), or repeat this with ?force=true to forget the host and the machine record and leave the VM running, untracked.",
				h.Name, rentedFrom))
			return
		case merr != nil && !errors.Is(merr, store.ErrNotFound):
			s.internal(w, r, "checking whether the host is a machine Zoomies created", merr)
			return
		}
	}
	live, err := s.ctrl.Store().ListRunnersForHost(r.Context(), id)
	if err != nil {
		s.internal(w, r, "listing the host's runners", err)
		return
	}
	alive := 0
	for _, run := range live {
		if !run.State.Terminal() {
			alive++
		}
	}
	if alive > 0 && !force {
		conflict(w, fmt.Sprintf("host %s is still running %d runner(s); cordon it and wait for them to finish, or repeat this with ?force=true to remove it and leave those workloads to be cleaned up by their agent", h.Name, alive))
		return
	}

	var forgotten *store.Machine
	if force {
		var ok bool
		if forgotten, ok = s.machineToForget(w, r, id); !ok {
			return
		}
	}

	machineID := ""
	if forgotten != nil {
		machineID = forgotten.ID
	}
	if err := s.ctrl.DeleteHostForgettingMachine(r.Context(), id, machineID); err != nil {
		s.fail(w, r, "deleting the host", err)
		return
	}
	if forgotten != nil {
		// After the commit, and on a context the client hanging up cannot
		// cancel: from here the audit row is the only record of the VM, so a
		// request that dies now must not take the resource identifier with it.
		s.auth.Auditor().Act(context.WithoutCancel(r.Context()), Identity(r.Context()), "machine.release", "machine", forgotten.ID, map[string]any{
			"name": forgotten.Name, "state": forgotten.State, "provider_id": forgotten.ProviderID,
			"resource_zone": forgotten.ResourceZone, "resource_id": forgotten.ResourceID,
			"address": forgotten.Address, "host_id": forgotten.HostID, "reason": "its host was force-deleted",
		})
	}
	s.auth.Auditor().Deleted(r.Context(), Identity(r.Context()), "host", id, h)
	s.ctrl.Nudge()
	noContent(w)
}

// machineToForget finds the machine a forced host delete has to forget so it
// stays to what the 409 promises -- the VM is left running -- and reports
// whether the delete may go on. It writes nothing: the row goes in the same
// transaction as the host, and the caller audits it once that has committed.
//
// machines.host_id is ON DELETE SET NULL, and the machine loop treats a ready
// machine with no host as one to release: drain, then destroy the VM, without
// waiting for the jobs the cascade just erased. So forgetting only the host
// would end in the destructive act the operator was told would not happen. A
// machine that is already deleting is left alone: the delete is under way, and
// dropping the row would abandon it half done with the VM still billed.
func (s *Server) machineToForget(w http.ResponseWriter, r *http.Request, hostID string) (*store.Machine, bool) {
	m, err := s.ctrl.Store().GetMachineByHost(r.Context(), hostID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, true
	}
	if err != nil {
		s.internal(w, r, "checking whether the host is a machine Zoomies created", err)
		return nil, false
	}
	if m == nil || m.State == store.MachineDeleting {
		return nil, true
	}
	return m, true
}

// ---------------------------------------------------------------------------
// Join tokens
// ---------------------------------------------------------------------------

// joinTokenResponse never carries the secret. The prefix is enough to recognise
// a token in a list and to match a leaked string to a row.
type joinTokenResponse struct {
	ID        string            `json:"id"`
	Prefix    string            `json:"prefix"`
	CreatedBy string            `json:"created_by,omitempty"`
	Capacity  int               `json:"capacity"`
	Labels    map[string]string `json:"labels"`
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt time.Time         `json:"expires_at"`
	UsedAt    *time.Time        `json:"used_at"`
	UsedByID  string            `json:"used_by_id,omitempty"`
	Usable    bool              `json:"usable"`
}

// createJoinTokenResponse adds the things that exist exactly once: the
// plaintext token, the command to paste on the new host, and -- when the
// command could not be pinned to this controller's build -- what that will
// cost the operator.
type createJoinTokenResponse struct {
	joinTokenResponse
	Token             string `json:"token"`
	Command           string `json:"command"`
	JoinCommand       string `json:"join_command"`
	ControllerVersion string `json:"controller_version"`
	InstallTag        string `json:"install_tag,omitempty"`
	// VersionNote is empty when the command installs a matching agent.
	VersionNote string `json:"version_note,omitempty"`
}

func (s *Server) joinTokenResponse(t *store.JoinToken) joinTokenResponse {
	return joinTokenResponse{
		ID:        t.ID,
		Prefix:    t.Prefix,
		CreatedBy: t.CreatedBy,
		Capacity:  t.Capacity,
		Labels:    emptyMap(t.Labels),
		CreatedAt: t.CreatedAt,
		ExpiresAt: t.ExpiresAt,
		UsedAt:    t.UsedAt,
		UsedByID:  t.UsedByID,
		Usable:    t.Usable(s.ctrl.Now()),
	}
}

// handleListJoinTokens answers GET /api/v1/join-tokens.
func (s *Server) handleListJoinTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.ctrl.Store().ListJoinTokens(r.Context())
	if err != nil {
		s.internal(w, r, "listing join tokens", err)
		return
	}
	out := make([]joinTokenResponse, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, s.joinTokenResponse(t))
	}
	writeJSON(w, http.StatusOK, newList(out))
}

// sizeTagField is the refusal for a size tag that is not one of the three
// classes exactly, or nil for labels that have none.
//
// While the controller works out size classes, the size tag is how an operator
// says which one a host is in, and one that is not a class would leave the host
// in none. It is said when it is written, on a host or on the join token that
// will put it there, rather than found later as a host that has quietly dropped
// out of every automatic pool. The three words are asked for as written because
// a pool's host selector compares a label exactly. With both switches off the
// key is an ordinary label and is not looked at.
func (s *Server) sizeTagField(labels map[string]string) *fieldError {
	v, ok := labels[store.LabelSize]
	if !ok || !s.ctrl.TracksClasses() || store.SizeClass(v).Valid() {
		return nil
	}
	return &fieldError{"labels", fmt.Sprintf("the size tag is a size class while the controller works classes out: write small, medium or large exactly, not %q, or remove the tag to let it be worked out from the machine", v)}
}

type createJoinTokenRequest struct {
	TTL      string            `json:"ttl"`
	Capacity int               `json:"capacity"`
	Labels   map[string]string `json:"labels"`
	// ControllerURL is the address the new host should join on, when the
	// caller knows better than server.external_url does. The UI always does:
	// the browser reached this controller on some address, and a machine on
	// the same network will usually reach it there too.
	ControllerURL string `json:"controller_url"`
	Connection    string `json:"connection"`
}

// handleCreateJoinToken mints a single-use enrolment credential.
func (s *Server) handleCreateJoinToken(w http.ResponseWriter, r *http.Request) {
	// No capacity: the agent decides from the machine it measures, which is
	// the only figure in this exchange that has seen the machine. A constant
	// here pinned every host that did not name one to that constant, however
	// large it was -- and under automatic sizing the capacity is no longer
	// only a slot count, it is the divisor that gives each runner its CPU and
	// memory, so two slots on a thirty-two core box is half the machine per
	// runner and two jobs at a time.
	req := createJoinTokenRequest{TTL: "15m"}
	if !decodeOptional(w, r, &req) {
		return
	}

	var fields []fieldError
	ttl := 15 * time.Minute
	if raw := strings.TrimSpace(req.TTL); raw != "" {
		d, err := time.ParseDuration(raw)
		switch {
		case err != nil:
			fields = append(fields, fieldError{"ttl", fmt.Sprintf("%q is not a duration; write it like 15m, 1h or 24h", raw)})
		case d <= 0:
			fields = append(fields, fieldError{"ttl", "a join token has to be usable for some length of time; try 15m"})
		default:
			ttl = d
		}
	}
	if req.Capacity < 0 {
		fields = append(fields, fieldError{"capacity", "capacity cannot be negative; leave it at 0 to let the agent decide from the host's CPU count"})
	}
	if f := s.sizeTagField(req.Labels); f != nil {
		fields = append(fields, *f)
	}
	if req.Connection != "" && req.Connection != "direct" && req.Connection != "tailcat" {
		fields = append(fields, fieldError{"connection", "choose direct or tailcat"})
	}
	controllerURL := strings.TrimRight(strings.TrimSpace(req.ControllerURL), "/")
	if controllerURL != "" {
		if msg := checkControllerURL(controllerURL); msg != "" {
			fields = append(fields, fieldError{"controller_url", msg})
		}
	}
	if len(fields) > 0 {
		unprocessable(w, "this join token could not be created", fields)
		return
	}
	if err := s.ctrl.AdmitJoinToken(r.Context()); err != nil {
		if !asLimit(w, err) {
			s.internal(w, r, "checking limits.join_tokens", err)
		}
		return
	}

	if req.Connection == "tailcat" {
		address, err := s.ensureTailcat(r.Context())
		if err != nil {
			unprocessable(w, err.Error(), []fieldError{{"connection", err.Error()}})
			return
		}
		controllerURL = "tailcat://" + address
	}
	id := Identity(r.Context())
	createdBy := ""
	if id != nil {
		createdBy = id.Name
	}
	token, plaintext, err := s.auth.CreateJoinToken(r.Context(), ttl, req.Labels, req.Capacity, createdBy)
	if err != nil {
		s.internal(w, r, "creating the join token", err)
		return
	}
	// The plaintext is deliberately not in the audit row: the row records that
	// a credential was minted, not the credential.
	s.auth.Auditor().Created(r.Context(), id, "join_token", token.ID, token)

	writeJSON(w, http.StatusCreated, createJoinTokenResponse{
		joinTokenResponse: s.joinTokenResponse(token),
		Token:             plaintext,
		Command:           s.joinCommand(plaintext, controllerURL),
		JoinCommand:       "zoomies agent join " + shellArgument(s.joinControllerURL(controllerURL)) + " --token " + shellArgument(plaintext),
		ControllerVersion: version.Short(),
		InstallTag:        version.Channel(version.Version),
		VersionNote:       joinVersionNote(),
	})
}

// checkControllerURL says what is wrong with an address a host is being told
// to join, or nothing. The bar is "an agent could dial it": an absolute
// http(s) URL with a host in it. Loopback is allowed on purpose -- an operator
// enrolling a second agent on the controller's own machine means it -- and
// the UI is what warns about it, since the UI knows which machine it is on.
func checkControllerURL(raw string) string {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return fmt.Sprintf("%q is not a URL an agent could join; write it like https://zoomies.example.com", raw)
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Sprintf("the controller address has to start with http:// or https://, not %q", u.Scheme+"://")
	case u.Host == "":
		return "the controller address needs a host name or IP address, like https://zoomies.example.com"
	case u.User != nil:
		return "the controller address must not carry a username or password; the join token is the credential"
	}
	return ""
}

// joinCommand renders the one-liner for the new host.
//
// The caller's address wins when it gave one. Otherwise the command names the
// controller's external URL, because that is the address the agent has to
// reach; when that is not configured either the command is still printed,
// with the placeholder in it, since an operator who has not set it yet needs
// to see what is missing rather than a blank field.
func (s *Server) joinControllerURL(controllerURL string) string {
	controller := controllerURL
	if controller == "" {
		controller = s.cfg().Server.ExternalURL
		// A loopback external URL is as unusable here as no URL at all, and
		// worse for being plausible: the default single-VM install makes it
		// http://localhost:8080, so the command told the new machine to join
		// itself, and the operator found out after a download, a system
		// write and a spent single-use token. The placeholder makes the gap
		// visible, and the UI fills it in.
		if controller == "" || s.cfg().ExternalURLIsLocal() {
			controller = "https://<this-controller>"
		}
	}
	return controller
}

func (s *Server) joinCommand(token, controllerURL string) string {
	controller := s.joinControllerURL(controllerURL)
	cmd := fmt.Sprintf("curl -fsSL https://zoomies.sh/install.sh | sh -s -- --mode agent --controller %s --join-token %s",
		shellArgument(controller), shellArgument(token))

	// Pin the agent to this controller's own published channel.
	//
	// Unpinned, the installer resolves "latest", which is the newest *published
	// release* -- not this build. A fleet whose controller was upgraded past the
	// newest release therefore enrolled every new host on an older agent, and
	// the host arrived reading "Different build" for good: re-running the
	// command changed nothing, because the version it asks for is the version
	// already there.
	//
	// It is worse than a cosmetic badge when the older agent predates something.
	// An agent that cannot yet measure its own machine reports no CPUs, memory
	// or disk, so the host shows "Size unknown" and a pool's resource limits
	// have nothing to fit against -- and no argument to the command could have
	// fixed it.
	if tag, ok := version.InstallTag(version.Version); ok {
		return cmd + " --version " + tag
	}

	// No pin, because there is nothing honest to pin to. Local and fork builds
	// have no repository asset; the command still works and the note says what
	// it cannot promise instead.
	return cmd
}

// shellArgument keeps a URL or credential one literal argument when the
// operator pastes the command into a POSIX shell.
func shellArgument(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// joinVersionNote explains a join command that could not be pinned, or is empty
// when it was.
//
// The command above stays runnable either way, so this is the difference
// between an operator who knows their new host will read "Different build" and
// one who finds out afterwards and starts re-running the installer to fix it.
func joinVersionNote() string {
	if _, ok := version.InstallTag(version.Version); ok {
		return ""
	}
	return fmt.Sprintf("This controller is %s, which is a local or unpublished build, "+
		"so there is no matching agent to install. The host will get the newest release and the "+
		"fleet will show it as a different build until this controller runs a released version.",
		version.Version)
}

// handleGetJoinToken answers GET /api/v1/join-tokens/{id}.
//
// It is what the Add-a-host page polls while the operator is over on the new
// machine: the fleet stream deliberately carries nothing about credentials, so
// a token being redeemed is learnt by asking, and used_by_id is the host it
// became.
func (s *Server) handleGetJoinToken(w http.ResponseWriter, r *http.Request) {
	t, err := s.ctrl.Store().GetJoinToken(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the join token", err)
		return
	}
	writeJSON(w, http.StatusOK, s.joinTokenResponse(t))
}

// handleDeleteJoinToken revokes an unused join token.
func (s *Server) handleDeleteJoinToken(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	found, err := s.ctrl.Store().GetJoinToken(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w, "there is no join token "+id+"; it may already have been used or revoked")
			return
		}
		s.internal(w, r, "reading the join token", err)
		return
	}
	if err := s.ctrl.Store().DeleteJoinToken(r.Context(), id); err != nil {
		s.fail(w, r, "revoking the join token", err)
		return
	}
	s.auth.Auditor().Deleted(r.Context(), Identity(r.Context()), "join_token", id, found)
	noContent(w)
}

// acceptHostCheckRequest is PUT /api/v1/hosts/{id}/check-acceptances.
type acceptHostCheckRequest struct {
	CheckID     string    `json:"check_id"`
	SeenCurrent string    `json:"seen_current"`
	Reason      string    `json:"reason"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// hostAcceptanceAudit is what an audit row says about an acceptance. The reason
// is a person's, so it belongs in the log; the check's value is the report's
// text and is kept to the acceptance itself.
type hostAcceptanceAudit struct {
	CheckID   string    `json:"check_id"`
	Reason    string    `json:"reason"`
	By        string    `json:"by"`
	ExpiresAt time.Time `json:"expires_at"`
}

func acceptanceAudit(a *store.HostCheckAcceptance) hostAcceptanceAudit {
	return hostAcceptanceAudit{CheckID: a.CheckID, Reason: a.Reason, By: a.ByName, ExpiresAt: a.ExpiresAt}
}

// failHostAcceptance maps the controller's answers about acceptances onto
// status codes and leaves anything else to the common mapping.
func (s *Server) failHostAcceptance(w http.ResponseWriter, r *http.Request, doing string, err error) {
	var (
		invalid *controller.HostAcceptInvalidError
		changed *controller.HostCheckChangedError
	)
	switch {
	case errors.As(err, &invalid):
		fields := make([]fieldError, 0, len(invalid.Fields))
		for _, f := range invalid.Fields {
			fields = append(fields, fieldError{Field: f.Field, Message: f.Message})
		}
		unprocessable(w, "", fields)
	case errors.As(err, &changed):
		conflict(w, changed.Error())
	case errors.Is(err, controller.ErrHostAcceptLimit):
		conflict(w, err.Error())
	default:
		s.fail(w, r, doing, err)
	}
}

// handleAcceptHostCheck answers PUT /api/v1/hosts/{id}/check-acceptances.
//
// A PUT because deciding the same thing again renews it: the row keeps its
// identity and the value, reason, owner and end are replaced. It changes what
// Zoomies counts and nothing on the host, and it answers the host as it now
// stands, which is what the page repaints from.
func (s *Server) handleAcceptHostCheck(w http.ResponseWriter, r *http.Request) {
	var in acceptHostCheckRequest
	if !decode(w, r, &in) {
		return
	}
	ident := Identity(r.Context())
	id := chiURLParam(r, "id")
	v, a, err := s.ctrl.AcceptHostCheck(r.Context(), id, controller.AcceptInput{
		CheckID: in.CheckID, SeenCurrent: in.SeenCurrent, Reason: in.Reason, ExpiresAt: in.ExpiresAt,
	}, controller.HostActor{ID: ident.ID, Name: ident.Name})
	if err != nil {
		s.failHostAcceptance(w, r, "accepting a host check", err)
		return
	}
	_ = s.auth.Auditor().Record(r.Context(), ident, "host.check_accepted", "host", id, nil, acceptanceAudit(a))
	writeJSON(w, http.StatusOK, v.For(isPlatform(r)))
}

// handleRevokeHostCheck answers DELETE /api/v1/hosts/{id}/check-acceptances/{check_id}.
//
// Any operator may end any acceptance: it only ever makes Zoomies stricter. It
// is idempotent, because two people pressing the button want the same outcome,
// and only a revoke that ended something is audited.
func (s *Server) handleRevokeHostCheck(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	v, ended, err := s.ctrl.RevokeHostCheck(r.Context(), id, chiURLParam(r, "check_id"))
	if err != nil {
		s.failHostAcceptance(w, r, "revoking a host check acceptance", err)
		return
	}
	if ended != nil {
		_ = s.auth.Auditor().Record(r.Context(), Identity(r.Context()), "host.check_revoked", "host", id, acceptanceAudit(ended), nil)
	}
	writeJSON(w, http.StatusOK, v.For(isPlatform(r)))
}
