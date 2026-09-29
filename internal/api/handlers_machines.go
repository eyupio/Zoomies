package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
)

// machineResponse is the shape GET /machines returns, rendered by the
// controller so the event stream's machine.updated frames are the same JSON.
type machineResponse = controller.MachineView

// machineTimelineResponse is one phase a machine reached.
type machineTimelineResponse = controller.MachineTimelineEntry

// There is deliberately no POST /machines.
//
// A machine exists because demand asked for one: one creation path means one
// accounting path, and a machine made by hand would be a second source of
// supply that the reconciler -- which decides how many should exist from the
// demand it can see -- would then decide to delete. An operator who wants more
// machines raises the provider's ceiling, and an operator who wants one for
// something else creates it in their hypervisor, where Zoomies will leave it
// alone because it carries none of its marks.

// handleListMachines answers GET /api/v1/machines.
func (s *Server) handleListMachines(w http.ResponseWriter, r *http.Request) {
	filter := store.MachineFilter{
		ProviderIDs:    queryList(r, "provider"),
		PoolIDs:        queryList(r, "pool"),
		HostIDs:        queryList(r, "host"),
		Search:         strings.TrimSpace(r.URL.Query().Get("q")),
		IncludeDeleted: queryBool(r, "include_deleted", false),
	}
	for _, raw := range queryList(r, "state") {
		state := store.MachineState(raw)
		if !state.Valid() {
			badRequestField(w, "state", fmt.Sprintf("%q is not a machine state; use planned, creating, starting, "+
				"bootstrapping, enrolling, ready, draining, deleting, deleted, failed or quarantined", raw))
			return
		}
		filter.States = append(filter.States, state)
	}

	page := parsePage(r)
	machines, total, err := s.ctrl.Store().ListMachines(r.Context(), filter, page)
	if err != nil {
		s.internal(w, r, "listing machines", err)
		return
	}
	view, err := s.ctrl.MachineRenderer(r.Context())
	if err != nil {
		s.internal(w, r, "reading what the machines belong to", err)
		return
	}
	out := make([]machineResponse, 0, len(machines))
	for _, m := range machines {
		out = append(out, view.View(m))
	}
	writeJSON(w, http.StatusOK, newPage(out, total, page))
}

// handleGetMachine answers GET /api/v1/machines/{id}.
func (s *Server) handleGetMachine(w http.ResponseWriter, r *http.Request) {
	m, view, ok := s.machineFor(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, view.View(m))
}

// machineFor reads one machine and the renderer its view needs, answering the
// client itself when either read fails.
func (s *Server) machineFor(w http.ResponseWriter, r *http.Request) (*store.Machine, *controller.MachineRenderer, bool) {
	m, err := s.ctrl.Store().GetMachine(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "reading the machine", err)
		return nil, nil, false
	}
	view, err := s.ctrl.MachineRenderer(r.Context())
	if err != nil {
		s.internal(w, r, "reading what the machine belongs to", err)
		return nil, nil, false
	}
	return m, view, true
}

// cordonMachineHost stops the scheduler placing onto a machine that is on its
// way out, and reports whether the request may go on. A machine with no host
// has nothing to cordon.
func (s *Server) cordonMachineHost(w http.ResponseWriter, r *http.Request, m *store.Machine) bool {
	if m.HostID == "" {
		return true
	}
	if err := s.ctrl.Store().SetHostCordoned(r.Context(), m.HostID, true); err != nil {
		s.fail(w, r, "cordoning the machine's host", err)
		return false
	}
	if h, err := s.ctrl.Store().GetHost(r.Context(), m.HostID); err == nil {
		s.ctrl.PublishHost(h)
	}
	return true
}

// handleDrainMachine answers POST /api/v1/machines/{id}/drain.
//
// Draining is reversible right up until the delete starts: demand coming back
// takes a draining machine to ready again rather than paying for a new one.
// The host is cordoned in the same breath, because a machine on its way out
// that kept accepting runners would never empty.
func (s *Server) handleDrainMachine(w http.ResponseWriter, r *http.Request) {
	m, _, ok := s.machineFor(w, r)
	if !ok {
		return
	}
	// The transition is checked before the cordon: a refused drain (only a ready
	// machine can drain) must not leave the host of a failed or deleting machine
	// cordoned by a request that answered 409 and wrote no audit row.
	if !store.CanTransitionMachine(m.State, store.MachineDraining) {
		s.fail(w, r, "draining the machine", fmt.Errorf("%w: machine %s cannot go %s -> %s",
			store.ErrInvalidTransition, m.ID, m.State, store.MachineDraining))
		return
	}
	if !s.cordonMachineHost(w, r, m) {
		return
	}
	out, err := s.ctrl.Store().TransitionMachine(r.Context(), m.ID, store.MachineDraining,
		"an operator asked for this machine to be drained")
	if err != nil {
		s.fail(w, r, "draining the machine", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "machine.drain", "machine", m.ID, map[string]any{
		"name": m.Name, "was": m.State, "host_id": m.HostID,
	})
	s.publishMachine(r, out)
	writeJSON(w, http.StatusOK, s.machineView(r, out))
}

// handleDeleteMachine answers DELETE /api/v1/machines/{id}.
//
// It records the intent and lets the machine loop carry it out, because a
// delete is not finished when the provider returns: it is finished when an
// inspect can no longer find the resource. So this answers with the machine in
// deleting rather than with 204, and the page watches it reach deleted.
func (s *Server) handleDeleteMachine(w http.ResponseWriter, r *http.Request) {
	m, _, ok := s.machineFor(w, r)
	if !ok {
		return
	}
	force := queryBool(r, "force", false)

	// A quarantined machine is never deleted from here, forced or not. Its
	// ownership could not be proved, so the resource behind it may belong to
	// another fleet, and a delete would destroy somebody else's machine. The
	// escape hatch is release, which touches nothing.
	if m.State == store.MachineQuarantined {
		conflict(w, fmt.Sprintf("machine %s is quarantined: nothing has been able to prove the resource behind it is ours, "+
			"so deleting it could destroy a machine belonging to somebody else. Look at %s in the provider yourself. "+
			"If it is ours, remove it there; if it is not, POST /api/v1/machines/%s/release to forget this row, which touches no resource.",
			m.Name, resourceLabel(m), m.ID))
		return
	}
	if m.HostID != "" && !force {
		runners, err := s.ctrl.Store().ListRunnersForHost(r.Context(), m.HostID)
		if err != nil {
			s.internal(w, r, "listing the machine's runners", err)
			return
		}
		alive := 0
		for _, run := range runners {
			if !run.State.Terminal() {
				alive++
			}
		}
		if alive > 0 {
			conflict(w, fmt.Sprintf("machine %s is still running %s, and deleting it destroys the VM they are running on. "+
				"Drain it and call this again when they have finished, or repeat this with ?force=true to delete it now and lose that work.",
				m.Name, runnerCount(alive)))
			return
		}
	}

	// Cordoned on the way to deleting, as every other road there does
	// (beginDrain, quarantine, a vanished VM): the scheduler's snapshot carries
	// hosts and never machines, so without it the host stays placeable for the
	// whole delete and a runner placed in that window dies with the VM. The
	// forced path cordons too -- the host is going either way. It comes after
	// the refusals above so that a delete that says no changes nothing.
	if store.CanTransitionMachine(m.State, store.MachineDeleting) && !s.cordonMachineHost(w, r, m) {
		return
	}
	out, err := s.ctrl.Store().TransitionMachine(r.Context(), m.ID, store.MachineDeleting,
		"an operator asked for this machine to be deleted")
	if err != nil {
		s.fail(w, r, "deleting the machine", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "machine.delete", "machine", m.ID, map[string]any{
		"name": m.Name, "was": m.State, "forced": force,
		"resource_zone": m.ResourceZone, "resource_id": m.ResourceID,
	})
	s.publishMachine(r, out)
	writeJSON(w, http.StatusOK, s.machineView(r, out))
}

// releaseMachineRequest asks for the machine's name to be typed, which is the
// one guard on an act that cannot be undone.
type releaseMachineRequest struct {
	Name string `json:"name"`
}

// handleReleaseMachine answers POST /api/v1/machines/{id}/release: forget this
// row and touch nothing.
//
// It exists for the machine nobody can safely delete -- a quarantined one whose
// resource turned out to belong to somebody else, or a row for a resource that
// was removed in the hypervisor by hand. What it costs is exactly what it says:
// if the resource does still exist, it goes on running and stops being anything
// Zoomies knows about, so the audit row keeps the provider's own identifiers
// for it after the database has stopped holding them.
func (s *Server) handleReleaseMachine(w http.ResponseWriter, r *http.Request) {
	m, _, ok := s.machineFor(w, r)
	if !ok {
		return
	}
	var req releaseMachineRequest
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) != m.Name {
		unprocessable(w, fmt.Sprintf("releasing a machine forgets it without deleting anything, and it cannot be undone: "+
			"send the machine's name, %s, to confirm that is what you mean", m.Name),
			[]fieldError{{"name", fmt.Sprintf("type %s to confirm", m.Name)}})
		return
	}
	if err := s.ctrl.Store().ForgetMachine(r.Context(), m.ID); err != nil {
		s.fail(w, r, "releasing the machine", err)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), "machine.release", "machine", m.ID, map[string]any{
		"name": m.Name, "state": m.State, "provider_id": m.ProviderID,
		"resource_zone": m.ResourceZone, "resource_id": m.ResourceID,
		"address": m.Address, "host_id": m.HostID,
	})
	s.ctrl.PublishMachineDeleted(m.ID)
	noContent(w)
}

// publishMachine announces a machine change and asks the machine loop to look
// again, which is what turns "deleting" into a delete rather than a state a row
// sits in until the next interval.
func (s *Server) publishMachine(r *http.Request, m *store.Machine) {
	s.ctrl.PublishMachine(r.Context(), m)
	s.ctrl.NudgeMachines()
}

// machineView renders one machine, falling back to what can be rendered
// without the lookups when they fail: a write that succeeded must not answer as
// though it had not.
func (s *Server) machineView(r *http.Request, m *store.Machine) machineResponse {
	view, err := s.ctrl.MachineRenderer(r.Context())
	if err != nil {
		s.logger(r).Warn("could not render a machine's provider, pool and host names", "machine", m.ID, "error", err)
		return controller.MachineView{ID: m.ID, ProviderID: m.ProviderID, Name: m.Name, State: m.State}
	}
	return view.View(m)
}

// resourceLabel names the resource behind a machine the way its provider does,
// so an operator can paste it into their own console.
func resourceLabel(m *store.Machine) string {
	switch {
	case m.ResourceZone != "" && m.ResourceID != "":
		return m.ResourceZone + "/" + m.ResourceID
	case m.ResourceID != "":
		return m.ResourceID
	}
	return m.Name
}
