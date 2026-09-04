package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"

	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
)

// Auditor writes the audit trail: one row per mutating action, with the actor,
// the target and a before/after picture whose secrets have been blanked.
type Auditor struct {
	store  *store.Store
	events *events.Bus
	logger *slog.Logger
}

// NewAuditor returns an auditor. bus may be nil, in which case rows are still
// written but the UI's audit page will only see them on its next fetch.
func NewAuditor(st *store.Store, bus *events.Bus, logger *slog.Logger) *Auditor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Auditor{store: st, events: bus, logger: logger}
}

// maxAuditPayload caps a single before/after document. A pool with a very large
// environment map should not be able to turn the audit table into the biggest
// thing in the database.
const maxAuditPayload = 16 << 10

// Record writes one audit row and publishes it to the event bus.
//
// It returns an error for tests and for callers that genuinely want to know,
// but nothing in a request path should act on it: refusing an operator's change
// because the audit write failed would turn a logging problem into an outage,
// and the failure is already logged here. The convenience helpers below
// therefore discard it.
func (a *Auditor) Record(ctx context.Context, id *Identity, action, targetKind, targetID string, before, after any) error {
	ev := &store.AuditEvent{
		Action:     action,
		TargetKind: targetKind,
		TargetID:   targetID,
		Before:     a.encode(before),
		After:      a.encode(after),
		ActorKind:  KindSystem,
		ActorName:  "zoomies",
	}
	if id != nil {
		ev.ActorID, ev.ActorName, ev.ActorKind, ev.IP = id.ID, id.Name, id.Kind, id.IP
		if ev.ActorKind == "" {
			ev.ActorKind = KindSystem
		}
	}
	if err := a.store.AppendAudit(ctx, ev); err != nil {
		a.logger.Error("could not write audit entry", "action", action,
			"target_kind", targetKind, "target_id", targetID, "error", err)
		return err
	}
	if a.events != nil {
		a.events.Publish(events.KindAudit, "audit:"+targetKind, ev)
	}
	return nil
}

// Created records the creation of an object.
func (a *Auditor) Created(ctx context.Context, id *Identity, targetKind, targetID string, after any) {
	_ = a.Record(ctx, id, targetKind+".create", targetKind, targetID, nil, after)
}

// Updated records a change, keeping only the fields that actually differ so the
// audit page shows "max_runners 4 -> 8" rather than the whole object twice.
func (a *Auditor) Updated(ctx context.Context, id *Identity, targetKind, targetID string, before, after any) {
	b, af := Diff(before, after)
	if b == nil && af == nil {
		// Nothing changed. A row saying so is noise in the one log an operator
		// reads when something has gone wrong.
		return
	}
	_ = a.Record(ctx, id, targetKind+".update", targetKind, targetID, b, af)
}

// Deleted records a removal, keeping the object as it was.
func (a *Auditor) Deleted(ctx context.Context, id *Identity, targetKind, targetID string, before any) {
	_ = a.Record(ctx, id, targetKind+".delete", targetKind, targetID, before, nil)
}

// Act records an action that is not a create/update/delete -- draining a
// runner, cordoning a host, verifying an installation -- with whatever detail
// the caller wants preserved.
func (a *Auditor) Act(ctx context.Context, id *Identity, action, targetKind, targetID string, detail any) {
	_ = a.Record(ctx, id, action, targetKind, targetID, nil, detail)
}

// Auth records an authentication event: a login, a logout, a refused password.
// The identity may be partial -- a failed login knows the username and the
// address but not a user ID -- which is exactly what makes these rows useful.
func (a *Auditor) Auth(ctx context.Context, id *Identity, action string, detail any) {
	targetID := ""
	if id != nil {
		targetID = id.ID
	}
	_ = a.Record(ctx, id, action, "auth", targetID, nil, detail)
}

// encode renders a before/after document: redacted, JSON, and capped.
func (a *Auditor) encode(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(Redact(v))
	if err != nil {
		a.logger.Warn("could not render audit payload", "error", err)
		return ""
	}
	if len(b) > maxAuditPayload {
		// Truncating JSON in the middle would leave a document nothing can
		// parse, so say what happened instead of storing a broken one.
		a.logger.Warn("audit payload too large to store in full", "bytes", len(b))
		return fmt.Sprintf(`{"truncated":true,"bytes":%d}`, len(b))
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// Redaction
// ---------------------------------------------------------------------------

// redactedMarker replaces a secret value. It matches what the logging package
// substitutes, so an operator sees the same word in both places.
const redactedMarker = "[redacted]"

// maxRedactDepth stops a cyclic structure from recursing forever. Nothing in
// the domain model is anywhere near this deep.
const maxRedactDepth = 12

// sensitiveTerms mark a field or map key as carrying a credential. They are
// matched as substrings of the name with punctuation removed, so "password"
// catches PasswordHash and new_password, and "secret" catches both
// client_secret and WebhookSecretEnc.
var sensitiveTerms = []string{
	"password", "token", "secret", "privatekey", "jitconfig",
	"encryptionkey", "clientsecret", "webhooksecret", "credential",
	"passphrase", "apikey",
}

// nameExceptions are suffixes that turn a would-be secret name back into a
// harmless one. A token's ID, its display name and its visible prefix are the
// things that make an audit row readable; blanking them would leave an entry
// that records that something happened to some token.
var nameExceptions = []string{
	"id", "ids", "name", "names", "prefix", "count", "at",
	"file", "path", "url", "type", "kind", "hint",
}

// Redact returns a copy of v -- as maps, slices and scalars -- with every value
// whose field or key name looks like a credential replaced by "[redacted]".
//
// It works on the shape a JSON document would have, not on the Go types, so the
// audit trail matches what the API shows. Fields tagged `json:"-"` are dropped
// entirely: in this codebase those are exactly the hashes and the sealed blobs,
// and they are absent from API responses for the same reason.
func Redact(v any) any { return redactValue(reflect.ValueOf(v), 0) }

var jsonMarshaler = reflect.TypeOf((*json.Marshaler)(nil)).Elem()

func redactValue(rv reflect.Value, depth int) any {
	if !rv.IsValid() {
		return nil
	}
	if depth > maxRedactDepth {
		return "[truncated]"
	}
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return nil
		}
		return redactValue(rv.Elem(), depth)
	}
	// A type that marshals itself -- time.Time, store.Duration -- is kept whole
	// so the audit entry shows "5m" and an RFC 3339 timestamp rather than a
	// nanosecond count. Containers are excluded: their contents still need
	// walking.
	if rv.Type().Implements(jsonMarshaler) && !isContainer(rv.Kind()) {
		if rv.CanInterface() {
			return rv.Interface()
		}
	}

	switch rv.Kind() {
	case reflect.Struct:
		return redactStruct(rv, depth)
	case reflect.Map:
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			key := fmt.Sprint(iter.Key().Interface())
			if isSensitiveName(key) {
				out[key] = redactedValue(iter.Value())
				continue
			}
			out[key] = redactValue(iter.Value(), depth+1)
		}
		return out
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return nil
		}
		// A byte slice in this codebase is a sealed secret. Its length is
		// occasionally useful; its content never is.
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return fmt.Sprintf("[%d bytes]", rv.Len())
		}
		out := make([]any, rv.Len())
		for i := range rv.Len() {
			out[i] = redactValue(rv.Index(i), depth+1)
		}
		return out
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return nil
	default:
		if !rv.CanInterface() {
			return nil
		}
		return rv.Interface()
	}
}

func redactStruct(rv reflect.Value, depth int) map[string]any {
	t := rv.Type()
	out := make(map[string]any, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if f.Anonymous && name == "" {
			// An embedded struct is flattened, which is what encoding/json does.
			if inner, ok := redactValue(rv.Field(i), depth+1).(map[string]any); ok {
				for k, v := range inner {
					if _, taken := out[k]; !taken {
						out[k] = v
					}
				}
				continue
			}
		}
		if name == "" {
			name = f.Name
		}
		if isSensitiveName(name) || isSensitiveName(f.Name) {
			out[name] = redactedValue(rv.Field(i))
			continue
		}
		out[name] = redactValue(rv.Field(i), depth+1)
	}
	return out
}

// redactedValue blanks one value.
//
// An empty value stays empty: an audit entry that shows "password": "[redacted]"
// for an account that has no password would be a lie. A bool or a number is
// returned unchanged, because neither can carry a credential and blanking
// store.Setting.Secret -- a flag whose name matches -- would make the entry
// less readable rather than safer.
func redactedValue(rv reflect.Value) any {
	if !rv.IsValid() {
		return nil
	}
	switch rv.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		if rv.CanInterface() {
			return rv.Interface()
		}
		return nil
	}
	if isEmptyValue(rv) {
		if rv.Kind() == reflect.String {
			return ""
		}
		return nil
	}
	return redactedMarker
}

func isEmptyValue(rv reflect.Value) bool {
	switch rv.Kind() {
	case reflect.Slice, reflect.Map, reflect.Array, reflect.String:
		return rv.Len() == 0
	case reflect.Pointer, reflect.Interface:
		return rv.IsNil()
	}
	return rv.IsZero()
}

func isContainer(k reflect.Kind) bool {
	return k == reflect.Map || k == reflect.Slice || k == reflect.Array
}

// isSensitiveName reports whether a field or key name means "this holds a
// credential".
func isSensitiveName(name string) bool {
	n := normalizeName(name)
	if n == "" {
		return false
	}
	matched := false
	for _, term := range sensitiveTerms {
		if strings.Contains(n, term) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	for _, ex := range nameExceptions {
		if n != ex && strings.HasSuffix(n, ex) {
			return false
		}
	}
	return true
}

// normalizeName lowercases a name and drops everything that is not a letter or
// a digit, so PasswordHash, password_hash and password-hash all compare equal.
func normalizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Diffing
// ---------------------------------------------------------------------------

// Diff returns the parts of before and after that differ, already redacted.
//
// An audit entry for "the operator raised max_runners" should say that and
// nothing else; printing two whole pool objects and leaving a human to spot the
// one changed number is how audit logs stop being read. Both results are nil
// when nothing changed.
func Diff(before, after any) (any, any) {
	b, a := Redact(before), Redact(after)
	bm, bok := b.(map[string]any)
	am, aok := a.(map[string]any)
	if !bok || !aok {
		if reflect.DeepEqual(b, a) {
			return nil, nil
		}
		return b, a
	}
	db, da := diffMaps(bm, am)
	var rb, ra any
	if len(db) > 0 {
		rb = db
	}
	if len(da) > 0 {
		ra = da
	}
	return rb, ra
}

func diffMaps(before, after map[string]any) (map[string]any, map[string]any) {
	db, da := map[string]any{}, map[string]any{}
	keys := make([]string, 0, len(before)+len(after))
	for k := range before {
		keys = append(keys, k)
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)

	for _, k := range keys {
		bv, bok := before[k]
		av, aok := after[k]
		if bok && aok {
			bsub, bIsMap := bv.(map[string]any)
			asub, aIsMap := av.(map[string]any)
			if bIsMap && aIsMap {
				sb, sa := diffMaps(bsub, asub)
				if len(sb) > 0 {
					db[k] = sb
				}
				if len(sa) > 0 {
					da[k] = sa
				}
				continue
			}
			if reflect.DeepEqual(bv, av) {
				continue
			}
		}
		if bok {
			db[k] = bv
		}
		if aok {
			da[k] = av
		}
	}
	return db, da
}
