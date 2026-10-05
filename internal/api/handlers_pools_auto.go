package api

import (
	"net/http"
	"reflect"
	"strings"

	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
)

// autoPoolEditable is the fields of a pool the controller keeps that an operator
// may change. Every other field of poolInput is refused by name, which is the
// right default for one added later: a field nobody has thought about for these
// pools is one the reconciler would put back.
//
// The memory valve is one of them because it is a policy and not a figure worked
// out from the hosts: the reconciler writes no column it lives in, and without
// this the pools most fleets run on would be the ones that could never turn it on.
var autoPoolEditable = map[string]bool{"idle_timeout": true, "auto": true, "enabled": true, "memory_burst": true}

// autoPoolWorkedOut is the refused fields the controller itself sets, from the
// hosts and the class a pool is kept for. They are said to be worked out because
// they are; the others are refused for a different reason, which is that these
// pools are all set up one way, and the sentence for them says that instead.
var autoPoolWorkedOut = map[string]bool{
	"name": true, "installation_id": true, "labels": true, "backend": true, "platform": true,
	"min_runners": true, "max_runners": true, "ephemeral": true, "docker_mode": true,
	"resources": true, "host_selector": true, "size_from_profile": true,
}

// isSet reports whether a field of a request was named. A request names a field
// with a pointer, but a field added later need not be one, and a non-nillable
// kind must not make the check panic -- or, worse, be taken for unnamed.
func isSet(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface, reflect.Func, reflect.Chan:
		return !v.IsNil()
	}
	return !v.IsZero()
}

// refusedForAutoPool lists the fields a request names that are not an operator's
// to change on a pool the controller keeps.
func refusedForAutoPool(in *poolInput) []fieldError {
	v := reflect.ValueOf(in).Elem()
	t := v.Type()
	var errs []fieldError
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" || !isSet(v.Field(i)) || autoPoolEditable[name] {
			continue
		}
		msg := "the controller sets this pool up and takes no change to this; its idle timeout, warm runners, cap, pause and memory valve are the settings that are yours. To change anything else, make a pool of your own."
		switch {
		case name == "min_runners" || name == "max_runners":
			msg = "this pool's minimum and maximum are worked out from the hosts in it; ask for runners to keep ready with auto.warm and for a ceiling with auto.cap"
		case autoPoolWorkedOut[name]:
			msg = "this pool is kept by the controller from your hosts, so this is worked out and not typed; its idle timeout, warm runners, cap, pause and memory valve are the settings that are yours. To change anything else, make a pool of your own."
		}
		errs = append(errs, fieldError{name, msg})
	}
	return errs
}

// updateAutoPool is PATCH /pools/{id} for a pool the controller keeps.
//
// The operator's side is stored beside what the controller derives, and a pass of
// the reconciler is run before answering, so that a pause or a cap has taken
// effect in the response and not only on the next tick. The pool is not run
// through the validation a pool somebody made is: a pool whose last host has gone
// has a maximum of nought, which is right for it and is what that validation
// exists to refuse.
func (s *Server) updateAutoPool(w http.ResponseWriter, r *http.Request, existing *store.Pool, in *poolInput) {
	errs := refusedForAutoPool(in)
	if in.Enabled != nil {
		if in.Auto != nil && in.Auto.Paused != nil {
			errs = append(errs, fieldError{"enabled", "give auto.paused or enabled, not both: enabled false is a pause"})
		} else {
			if in.Auto == nil {
				in.Auto = &autoPoolInput{}
			}
			paused := !*in.Enabled
			in.Auto.Paused = &paused
		}
		in.Enabled = nil
	}
	before := *existing
	updated := *existing
	errs = append(errs, in.apply(&updated)...)
	if in.MemoryBurst != nil {
		validateMemoryBurst(&updated, func(field, msg string) { errs = append(errs, fieldError{field, msg}) })
	}
	if in.Auto != nil {
		if in.Auto.Warm != nil {
			if *in.Auto.Warm < 0 {
				errs = append(errs, fieldError{"auto.warm", "warm runners cannot be negative; use 0 to keep none ready"})
			}
			updated.AutoMin = *in.Auto.Warm
		}
		if in.Auto.Cap != nil {
			if *in.Auto.Cap < 0 {
				errs = append(errs, fieldError{"auto.cap", "a cap cannot be negative; use 0 for none"})
			}
			updated.AutoCap = *in.Auto.Cap
		}
		if in.Auto.Paused != nil {
			updated.AutoPaused = *in.Auto.Paused
			s.pauseNow(&updated, existing.AutoPaused)
		}
	}
	if updated.Ephemeral && updated.AutoMin > 0 && updated.IdleTimeout.Duration() == 0 {
		errs = append(errs, fieldError{"idle_timeout", "warm ephemeral runners with no idle timeout are replaced after every job and never reaped; give an idle timeout, or ask for no warm runners"})
	}
	if len(errs) > 0 {
		unprocessable(w, "this pool cannot be changed as described", errs)
		return
	}
	// What the controller derives is left as it was read; the pass below is what
	// moves it, from the hosts as they are.
	if err := s.ctrl.Store().UpdatePool(r.Context(), &updated); err != nil {
		s.fail(w, r, "saving the pool", err)
		return
	}
	s.auth.Auditor().Updated(r.Context(), Identity(r.Context()), "pool", existing.ID, poolForAudit(&before), poolForAudit(&updated))
	s.finishAutoPoolEdit(w, r, existing.ID)
}

// setAutoPoolPaused is the Enable and Disable buttons for a pool the controller
// keeps: an operator's pause, which the controller honours whatever its hosts do.
func (s *Server) setAutoPoolPaused(w http.ResponseWriter, r *http.Request, p *store.Pool, paused bool) {
	if p.AutoPaused != paused {
		before := *p
		p.AutoPaused = paused
		s.pauseNow(p, before.AutoPaused)
		if err := s.ctrl.Store().UpdatePool(r.Context(), p); err != nil {
			s.fail(w, r, "saving the pool", err)
			return
		}
		action := "pool.enable"
		if paused {
			action = "pool.disable"
		}
		s.auth.Auditor().Act(r.Context(), Identity(r.Context()), action, "pool", p.ID, map[string]any{
			"name": p.Name, "paused": paused, "was": before.AutoPaused,
		})
	}
	s.finishAutoPoolEdit(w, r, p.ID)
}

// pauseNow makes a pause, or the end of one, take effect at once for a pool the
// controller is not keeping.
//
// For a pool it keeps, the reconciler turns the pause into a pool that is out of
// use, and the pass the edit runs does it before the response. For one it is not
// keeping -- the switch is off or only watching -- no pass will, so the pool would
// go on taking work for as long as it stayed "paused", and the operator was told
// it was saved. was is the pause as it stood before the edit; a request that does
// not change it leaves the pool's own switch as it was.
func (s *Server) pauseNow(p *store.Pool, was bool) {
	if p.AutoPaused == was || s.ctrl.AutoPoolKept(p) {
		return
	}
	// Resuming puts back what the pool had, which is in use only if it has room
	// for a runner: a leftover whose last host went has none.
	p.Enabled = !p.AutoPaused && p.MaxRunners > 0
}

// finishAutoPoolEdit runs a pass of the reconciler so the operator's change has
// taken effect, then answers with the pool as it now is.
func (s *Server) finishAutoPoolEdit(w http.ResponseWriter, r *http.Request, id string) {
	if err := s.ctrl.ReconcileAutoPools(r.Context()); err != nil {
		// The edit is saved and the controller's next pass applies it.
		s.logger(r).Warn("a pool the controller keeps was edited but the pass that applies it failed", "pool", id, "error", err)
	}
	fresh, err := s.ctrl.Store().GetPool(r.Context(), id)
	if err != nil {
		s.fail(w, r, "reading the pool back", err)
		return
	}
	s.ctrl.PublishPool(r.Context(), events.KindPoolUpdated, fresh)
	s.ctrl.Nudge()
	view, err := s.ctrl.PoolRenderer(r.Context())
	if err != nil {
		s.internal(w, r, "reading the pool back", err)
		return
	}
	writeJSON(w, http.StatusOK, poolFor(r, view.View(fresh)))
}
