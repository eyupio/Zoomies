package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SizeClass is how big a host is, or how big a host a job needs, in the three
// steps a workflow's author can ask for by name.
//
// Three because that is what people actually choose between. A class is a
// promise about the machine -- "at least this much CPU and memory, per runner's
// worth" -- and a fourth would be a distinction nobody could be asked to make
// when writing a workflow. The thresholds between them are settings
// (scheduler.size_*), not constants here: what counts as large depends on the
// fleet, and the names do not.
type SizeClass string

const (
	SizeSmall  SizeClass = "small"
	SizeMedium SizeClass = "medium"
	SizeLarge  SizeClass = "large"
)

// sizeClasses is every class, smallest first. That order is the only one
// anything that compares classes uses.
var sizeClasses = []SizeClass{SizeSmall, SizeMedium, SizeLarge}

// SizeClasses returns every class, smallest first.
func SizeClasses() []SizeClass { return append([]SizeClass(nil), sizeClasses...) }

// ParseSizeClass reads a class the way an operator types it. The comparison is
// the one GitHub makes of a label, which is what a class is spelled in a
// workflow, so "Large" and " large " are the same class.
func ParseSizeClass(s string) (SizeClass, bool) {
	c := SizeClass(strings.ToLower(strings.TrimSpace(s)))
	return c, c.Valid()
}

// Valid reports whether this is one of the three.
func (c SizeClass) Valid() bool { return c.Rank() >= 0 }

// Rank is the class's place in the order, 0 for small, and -1 for a value that
// is not a class, so that an unknown string never compares as a size.
func (c SizeClass) Rank() int {
	for i, k := range sizeClasses {
		if c == k {
			return i
		}
	}
	return -1
}

// Larger is the next class up, and false for the largest.
func (c SizeClass) Larger() (SizeClass, bool) {
	if i := c.Rank(); i >= 0 && i+1 < len(sizeClasses) {
		return sizeClasses[i+1], true
	}
	return "", false
}

// Smaller is the next class down, and false for the smallest.
func (c SizeClass) Smaller() (SizeClass, bool) {
	if i := c.Rank(); i > 0 {
		return sizeClasses[i-1], true
	}
	return "", false
}

// Label is what a workflow writes to ask for this class: zoomies-large. It is
// built the way every other label this fleet advertises is, so that it can
// never collide with one a pool's own name would produce, and so that an
// operator who has read one pool's label can guess another's.
func (c SizeClass) Label() string { return BrandedLabel(string(c)) }

// ClassOfLabel is the class a runs-on label asks for, if it is one.
func ClassOfLabel(label string) (SizeClass, bool) {
	l := NormalizeLabel(label)
	for _, c := range sizeClasses {
		if l == c.Label() {
			return c, true
		}
	}
	return "", false
}

// LabelSize is the host tag the class is read through: a pool selecting
// `size: large` matches the hosts in the large class, whether an operator put
// that on the host or the controller worked it out. See Host.SelectorValue.
const LabelSize = "size"

// HostSizeClass is the controller's reading of how big a host is.
//
// A host's measurements wobble: the agent re-reads its allocatable memory on
// every heartbeat, an operator changes a reserve, a VM is resized and then
// resized back. Moving a host between pools on each of those would reshuffle
// the fleet around a figure nobody meant to change, so a class moves only after
// the measurements have named the new one for as long as the operator chose
// (scheduler.size_class_hold): Pending is what they say now, PendingSince is
// when they started saying it, and Class stays where it is until then.
//
// The controller owns every field. A heartbeat carries the measurements and
// never the class, and an operator who wants a host in a class says so with a
// size label, which wins over this without being written into it.
type HostSizeClass struct {
	// Class is the class the host is in. Empty until the controller has worked
	// one out, which it does only while size routing or automatic pools are on.
	Class SizeClass `json:"class,omitempty"`
	// Pending is the class the host's allocatable resources now say, when it
	// is not Class, and PendingSince is when they first said it. Both are empty
	// while the host sits where the measurements put it.
	Pending      SizeClass  `json:"pending,omitempty"`
	PendingSince *time.Time `json:"pending_since,omitempty"`
	// ChangedAt is when Class last moved, which is what the Hosts page says
	// when it explains why a host is where it is.
	ChangedAt *time.Time `json:"changed_at,omitempty"`
}

// Set reports whether the controller has worked a class out for this host.
func (h HostSizeClass) Set() bool { return h.Class != "" }

// Value stores the document as JSON; "{}" is "not worked out".
func (h HostSizeClass) Value() (driver.Value, error) { return marshalJSON(h) }

// Scan reads the column back. NULL, which a hand-edited database can leave, and
// an empty string are "not worked out", so a host is never unreadable for lack
// of one.
func (h *HostSizeClass) Scan(value any) error {
	*h = HostSizeClass{}
	switch v := value.(type) {
	case string:
		return unmarshalJSON(v, h)
	case []byte:
		return unmarshalJSON(string(v), h)
	case nil:
		return nil
	default:
		return fmt.Errorf("host size class: unexpected database value %T", value)
	}
}

// ErrSizeClassMoved is returned by SetHostSizeClass when the host is no longer
// in the class the caller decided from.
var ErrSizeClassMoved = errors.New("the host's size class was decided elsewhere first")

// SetHostSizeClass records the controller's reading of one host, provided the
// host is still in the class the reading was made from.
//
// Its own statement, like the throttle and the reserve: the path a heartbeat's
// facts take through UpdateHost and SetHostReported cannot reach this column,
// and an operator's PATCH cannot either. The class is worked out from what a
// host measured and never written by what a host said.
//
// The write is conditional on from because more than one pass can be deciding:
// the reconciler that owns the pools, and the join that tags a host the moment
// it appears. A move written twice would be two audit rows for one decision,
// and a reading made before another landed would put a host back where it
// just left; a caller that finds the class moved leaves the decision to
// whoever moved it.
func (s *Store) SetHostSizeClass(ctx context.Context, id string, value HostSizeClass, from SizeClass) error {
	res, err := s.exec(ctx, `UPDATE hosts SET size_class=? WHERE id=? AND COALESCE(json_extract(size_class, '$.class'), '')=?`,
		value, id, string(from))
	if err != nil {
		return wrapWrite(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	var exists int
	if err := s.read.QueryRowContext(ctx, `SELECT 1 FROM hosts WHERE id=?`, id).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	return ErrSizeClassMoved
}

// EffectiveSizeClass is the class this host is in for the purpose of pools: an
// operator's size label when it names a class, otherwise the class the
// controller holds it in. The second result says which of the two answered, so
// that a page can mark an automatic tag as automatic.
//
// A size label that is not one of the three answers "" with operator set: the
// host is the operator's to place, and nothing derived may quietly replace what
// they wrote. Hosts like that are left out of the pools the controller keeps,
// and a finding says so.
//
// The label has to be the class exactly, "large" and not "Large". A pool asks
// for a class with a host selector, which compares a label as written, so a host
// the controller counted towards a class's pool on a looser reading would be one
// that pool's own selector never matched: its slots in the pool's maximum, no
// runner ever placed on it, and nothing on the host saying why.
func (h *Host) EffectiveSizeClass() (class SizeClass, operator bool) {
	if v, ok := h.Labels[LabelSize]; ok {
		if c := SizeClass(v); c.Valid() {
			return c, true
		}
		return "", true
	}
	return h.SizeClass.Class, false
}
