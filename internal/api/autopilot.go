package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
)

// The controller proposes changes -- a sidecar share, a smallest runner, a warm
// runner -- each priced against the fleet before it is said. With
// security.auto_apply_remedies on it also makes them, and this file is how, with
// every limit that makes that something an operator can leave on:
//
//   - It is the controller's own proposal and nothing else. The change is never
//     composed here: this asks POST /problems/apply for the remedy the problems now
//     carry, so the route's checks, its refusal of a change that leaves a pool with
//     nowhere to run, and its audit row are the ones a person's click meets.
//   - A proposal has to stand unchanged for autopilotSteady first. The remedy's ID
//     covers the change it makes, so a proposal that moved starts again, and one
//     noisy window cannot make a change.
//   - A pool or host is changed at most once in autopilotCooldown, so a proposal
//     that keeps being re-worked cannot walk a size back and forth.
//   - What it replaced is written down, and a change is only ever undone to that,
//     only while nobody has edited the target since. A proposal that has been made
//     once is never made again, whether it was undone here or put back by hand:
//     whoever reversed it has answered the question.
//   - It makes one change a pass. Each one changes what the next proposal would
//     be, and the next pass is the controller's chance to see that.
const (
	autopilotInterval = 5 * time.Minute
	autopilotSteady   = 2 * time.Hour
	autopilotCooldown = 24 * time.Hour
	autopilotRetry    = time.Hour
	// autoAppliedHistory is how far back the list of automatic changes reaches,
	// which is also how long an undo is offered.
	autoAppliedHistory = 30 * 24 * time.Hour

	auditAutoApplied = "problem.remedy_auto_applied"
	auditAutoUndone  = "problem.remedy_undone"
)

// autopilotIdentity is who the controller acts as: an operator, which is the role
// the apply route asks of anybody, and named so the audit log says whose change
// it was.
var autopilotIdentity = &auth.Identity{Kind: auth.KindSystem, ID: "autopilot", Name: "zoomies autopilot", Role: store.RoleOperator}

// autopilotState is what the loop remembers between passes: when each proposal
// was first seen, and when one that was refused may be tried again. Neither
// outlives a restart, which only makes the controller more patient.
type autopilotState struct {
	mu       sync.Mutex
	seen     map[string]time.Time
	retryAt  map[string]time.Time
	lastPass time.Time
}

func (s *Server) runAutopilot(ctx context.Context) {
	t := time.NewTicker(autopilotInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.autopilotPass(ctx); err != nil {
				s.log.Warn("applying suggested changes automatically", "error", err)
			}
		}
	}
}

// autopilotPass looks at the proposals now and makes at most one.
func (s *Server) autopilotPass(ctx context.Context) error {
	st := &s.autopilot
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.seen == nil {
		st.seen, st.retryAt = map[string]time.Time{}, map[string]time.Time{}
	}
	if !s.cfg().Security.AutoApplyRemedies {
		// Off forgets what it had seen, so turning it on later starts the wait afresh
		// rather than acting on a proposal that stood while nobody had asked.
		clear(st.seen)
		clear(st.retryAt)
		return nil
	}
	now := s.ctrl.Now()
	st.lastPass = now

	items, err := s.ctrl.Problems(ctx)
	if err != nil {
		return fmt.Errorf("gathering the current problems: %w", err)
	}
	for i := range items {
		if items[i].Code == "controller.problems_partial" {
			// A part the controller could not read leaves its proposals out; acting on
			// what is left would be acting on half a picture.
			return nil
		}
	}
	present := map[string]bool{}
	var due []*controller.Problem
	for i := range items {
		p := &items[i]
		if p.Remedy == nil || !p.Audience.For(false) {
			continue
		}
		id := p.Remedy.ID
		present[id] = true
		first, ok := st.seen[id]
		if !ok {
			st.seen[id] = now
			continue
		}
		if now.Sub(first) >= autopilotSteady && !now.Before(st.retryAt[id]) {
			due = append(due, p)
		}
	}
	for id := range st.seen {
		if !present[id] {
			delete(st.seen, id)
			delete(st.retryAt, id)
		}
	}
	sort.Slice(due, func(i, j int) bool { return st.seen[due[i].Remedy.ID].Before(st.seen[due[j].Remedy.ID]) })

	for _, p := range due {
		if ok, err := s.mayAutoApply(ctx, p, now); err != nil {
			return err
		} else if !ok {
			continue
		}
		if err := s.autoApply(ctx, p); err != nil {
			st.retryAt[p.Remedy.ID] = now.Add(autopilotRetry)
			s.log.Info("a suggested change was not applied automatically", "code", p.Code, "target", p.TargetID, "reason", err.Error())
			continue
		}
		return nil
	}
	return nil
}

// mayAutoApply is the cooldown and the once-only rule: false when this target has
// been changed automatically within autopilotCooldown, or when this very proposal
// has already been made to it.
func (s *Server) mayAutoApply(ctx context.Context, p *controller.Problem, now time.Time) (bool, error) {
	since := now.Add(-autopilotCooldown)
	recent, _, err := s.ctrl.Store().ListAudit(ctx, store.AuditFilter{Actions: []string{auditAutoApplied}, TargetID: p.Remedy.TargetID, Since: &since}, store.Page{Limit: 1})
	if err != nil {
		return false, fmt.Errorf("reading the automatic changes already made: %w", err)
	}
	if len(recent) > 0 {
		return false, nil
	}
	// A proposal that has been made, or made and then undone, is not made again. The
	// remedy's ID covers the change itself, so the same ID coming back means
	// somebody has put the target back as it was -- by undoing it or by editing it --
	// and that is an answer, whichever way it was given.
	for _, action := range []string{auditAutoApplied, auditAutoUndone} {
		rows, _, err := s.ctrl.Store().ListAudit(ctx, store.AuditFilter{Actions: []string{action}, TargetID: p.Remedy.TargetID}, store.Page{Limit: 500})
		if err != nil {
			return false, fmt.Errorf("reading the changes already made: %w", err)
		}
		for _, row := range rows {
			var a struct {
				Remedy string `json:"remedy"`
			}
			if json.Unmarshal([]byte(row.After), &a) == nil && a.Remedy == p.Remedy.ID {
				return false, nil
			}
		}
	}
	return true, nil
}

// autoApply makes one change and writes down what it replaced.
func (s *Server) autoApply(ctx context.Context, p *controller.Problem) error {
	r := p.Remedy
	var body map[string]json.RawMessage
	if err := json.Unmarshal(r.Body, &body); err != nil {
		return fmt.Errorf("the proposal's request is unreadable: %w", err)
	}
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	before, err := s.targetSnapshot(ctx, r, keys)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"code": p.Code, "target_id": p.TargetID, "remedy_id": r.ID})
	if err != nil {
		return err
	}
	if status, answer := s.callAs(ctx, autopilotIdentity, http.MethodPost, "/api/v1/problems/apply", payload); status >= 400 {
		return fmt.Errorf("the route answered %d: %s", status, bytes.TrimSpace(answer))
	}
	applied, err := s.targetSnapshot(ctx, r, keys)
	if err != nil {
		// The change is made; what it left is only needed to undo it safely, and
		// without it the undo is withheld, which is the careful way to fail.
		s.log.Warn("could not read back an automatic change", "target", r.TargetID, "error", err)
		applied = nil
	}
	s.auth.Auditor().Record(ctx, autopilotIdentity, auditAutoApplied, targetKindOf(r), r.TargetID, before, map[string]any{
		"code": p.Code, "remedy": r.ID, "label": r.Label, "effect": r.Effect, "kind": r.Kind, "applied": applied,
	})
	s.log.Info("applied a suggested change automatically", "code", p.Code, "target", r.TargetID, "change", r.Label)
	return nil
}

func targetKindOf(r *controller.Remedy) string {
	if r.Kind == controller.RemedyHostUpdate {
		return "host"
	}
	return "pool"
}

// targetSnapshot is the named top-level fields of the pool or host a remedy
// changes, as the store holds them. The fields are the request's own names, so
// what was there before can be sent back as an update.
//
// They are decoded values and not raw JSON, because the audit log redacts what it
// is given and a raw message is a byte slice to it: it would be written down as
// "[40 bytes]", which is not something to put back.
func (s *Server) targetSnapshot(ctx context.Context, r *controller.Remedy, keys []string) (map[string]any, error) {
	var v any
	var err error
	if r.Kind == controller.RemedyHostUpdate {
		v, err = s.ctrl.Store().GetHost(ctx, r.TargetID)
	} else {
		v, err = s.ctrl.Store().GetPool(ctx, r.TargetID)
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s %s: %w", targetKindOf(r), r.TargetID, err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var all map[string]any
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		out[k] = all[k]
	}
	return out, nil
}

// callAs is a request to this server's own router as an identity that has no
// credential to send: the controller itself.
func (s *Server) callAs(ctx context.Context, id *auth.Identity, method, path string, body []byte) (int, []byte) {
	ctx = context.WithValue(ctx, chi.RouteCtxKey, (*chi.Context)(nil))
	ctx = context.WithValue(noConfirm(ctx), ctxInProcess, id)
	req, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(body))
	if err != nil {
		return http.StatusInternalServerError, []byte(err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.RemoteAddr = "127.0.0.1:0"
	rec := &bufferedResponse{header: http.Header{}, limit: mcpResponseLimit}
	s.handler.ServeHTTP(rec, req)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return rec.status, rec.body.Bytes()
}

// autoAppliedChange is one automatic change as the list shows it.
type autoAppliedChange struct {
	ID       string                `json:"id"`
	At       time.Time             `json:"at"`
	Code     string                `json:"code"`
	Label    string                `json:"label"`
	Effect   string                `json:"effect,omitempty"`
	Kind     controller.RemedyKind `json:"kind"`
	TargetID string                `json:"target_id"`
	// Undone is true once somebody has reversed it. Undoable is false then, and
	// also when what the change left has been edited since or was never read back.
	Undone   bool `json:"undone"`
	Undoable bool `json:"undoable"`
}

type autoAppliedRecord struct {
	row     *store.AuditEvent
	before  map[string]any
	applied map[string]any
	remedy  string
	code    string
	label   string
	effect  string
	kind    controller.RemedyKind
}

// autoAppliedRecords is the automatic changes of the last autoAppliedHistory,
// newest first, and the set of them that have been undone.
func (s *Server) autoAppliedRecords(ctx context.Context) ([]autoAppliedRecord, map[string]bool, error) {
	since := s.ctrl.Now().Add(-autoAppliedHistory)
	rows, _, err := s.ctrl.Store().ListAudit(ctx, store.AuditFilter{Actions: []string{auditAutoApplied}, Since: &since}, store.Page{Limit: 500})
	if err != nil {
		return nil, nil, err
	}
	undoneRows, _, err := s.ctrl.Store().ListAudit(ctx, store.AuditFilter{Actions: []string{auditAutoUndone}, Since: &since}, store.Page{Limit: 500})
	if err != nil {
		return nil, nil, err
	}
	undone := map[string]bool{}
	for _, u := range undoneRows {
		var a struct {
			Undoes string `json:"undoes"`
		}
		if json.Unmarshal([]byte(u.After), &a) == nil && a.Undoes != "" {
			undone[a.Undoes] = true
		}
	}
	var out []autoAppliedRecord
	for _, row := range rows {
		rec := autoAppliedRecord{row: row}
		var after struct {
			Code    string                `json:"code"`
			Remedy  string                `json:"remedy"`
			Label   string                `json:"label"`
			Effect  string                `json:"effect"`
			Kind    controller.RemedyKind `json:"kind"`
			Applied map[string]any        `json:"applied"`
		}
		if json.Unmarshal([]byte(row.After), &after) != nil {
			continue
		}
		if json.Unmarshal([]byte(row.Before), &rec.before) != nil {
			continue
		}
		rec.applied, rec.remedy, rec.code, rec.label, rec.effect, rec.kind = after.Applied, after.Remedy, after.Code, after.Label, after.Effect, after.Kind
		out = append(out, rec)
	}
	return out, undone, nil
}

// handleAutoApplied answers GET /api/v1/problems/auto-applied.
func (s *Server) handleAutoApplied(w http.ResponseWriter, r *http.Request) {
	records, undone, err := s.autoAppliedRecords(r.Context())
	if err != nil {
		s.internal(w, r, "reading the automatic changes", err)
		return
	}
	out := make([]autoAppliedChange, 0, len(records))
	for _, rec := range records {
		c := autoAppliedChange{ID: rec.row.ID, At: rec.row.CreatedAt, Code: rec.code, Label: rec.label, Effect: rec.effect, Kind: rec.kind, TargetID: rec.row.TargetID, Undone: undone[rec.row.ID]}
		c.Undoable = !c.Undone && rec.applied != nil && s.untouchedSince(r.Context(), rec)
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": s.cfg().Security.AutoApplyRemedies, "items": out})
}

// untouchedSince reports whether the target still holds what the automatic change
// left in it.
func (s *Server) untouchedSince(ctx context.Context, rec autoAppliedRecord) bool {
	keys := make([]string, 0, len(rec.applied))
	for k := range rec.applied {
		keys = append(keys, k)
	}
	now, err := s.targetSnapshot(ctx, &controller.Remedy{Kind: rec.kind, TargetID: rec.row.TargetID}, keys)
	if err != nil {
		return false
	}
	return sameFields(now, rec.applied)
}

// sameFields compares two sets of fields as the JSON values they decode to.
func sameFields(a, b map[string]any) bool {
	return len(a) == len(b) && reflect.DeepEqual(a, b)
}

// handleUndoAutoApplied answers POST /api/v1/problems/auto-applied/{id}/undo.
//
// It puts back what the change replaced, through the pool's or host's own update
// as the caller, so it needs the role that update needs and is refused for what that
// update refuses. It will not run over an edit: if anyone has changed the target
// since, putting the old value back would undo their work too.
func (s *Server) handleUndoAutoApplied(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	records, undone, err := s.autoAppliedRecords(r.Context())
	if err != nil {
		s.internal(w, r, "reading the automatic changes", err)
		return
	}
	var rec *autoAppliedRecord
	for i := range records {
		if records[i].row.ID == id {
			rec = &records[i]
			break
		}
	}
	if rec == nil {
		notFound(w, "there is no automatic change "+id+" in the last 30 days; GET /problems/auto-applied lists them")
		return
	}
	if undone[id] {
		conflict(w, "that change has already been undone")
		return
	}
	action := auth.ActionPoolsWrite
	if rec.kind == controller.RemedyHostUpdate {
		action = auth.ActionHostsWrite
	}
	if who := Identity(r.Context()); !auth.Allowed(who, action) {
		forbidden(w, auth.Explain(who, action))
		return
	}
	if rec.applied == nil || !s.untouchedSince(r.Context(), *rec) {
		conflict(w, "that change cannot be undone safely: the "+targetKindOf(&controller.Remedy{Kind: rec.kind})+" was edited after it was made, and putting the old value back would undo that edit too. Change it by hand instead.")
		return
	}
	payload, err := json.Marshal(rec.before)
	if err != nil {
		s.internal(w, r, "undoing the change", err)
		return
	}
	req := r.Clone(noConfirm(r.Context()))
	req.URL.RawQuery = ""
	req.Body = io.NopCloser(bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	out := &bufferedResponse{header: http.Header{}, limit: mcpResponseLimit}

	target := rec.row.TargetID
	switch rec.kind {
	case controller.RemedyPoolUpdate:
		existing, gerr := s.ctrl.Store().GetPool(r.Context(), target)
		if gerr != nil {
			s.fail(w, r, "reading the pool", gerr)
			return
		}
		var body poolInput
		if !decode(out, req, &body) {
			s.relay(w, out)
			return
		}
		s.applyPoolUpdate(out, req, target, existing, &body)
	case controller.RemedyHostUpdate:
		h, gerr := s.ctrl.Store().GetHost(r.Context(), target)
		if gerr != nil {
			s.fail(w, r, "reading the host", gerr)
			return
		}
		var body hostUpdateRequest
		if !decode(out, req, &body) {
			s.relay(w, out)
			return
		}
		s.applyHostUpdate(out, req, target, h, &body)
	default:
		s.internal(w, r, "undoing the change", fmt.Errorf("a change of unknown kind %q", rec.kind))
		return
	}
	if out.status >= http.StatusBadRequest {
		s.relay(w, out)
		return
	}
	s.auth.Auditor().Act(r.Context(), Identity(r.Context()), auditAutoUndone, targetKindOf(&controller.Remedy{Kind: rec.kind}), target, map[string]any{
		"undoes": id, "remedy": rec.remedy, "label": rec.label,
	})
	writeJSON(w, http.StatusOK, map[string]any{"undone": true, "id": id, "target_id": target})
}
