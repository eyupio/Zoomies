package store

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"github.com/eyupio/zoomies/internal/hosttune"
	"time"
)

type HostDoctor struct{ *hosttune.Report }

func (h HostDoctor) Value() (driver.Value, error) {
	if h.Report == nil {
		return "{}", nil
	}
	return marshalJSON(h.Report)
}
func (h *HostDoctor) Scan(value any) error {
	h.Report = nil
	var b []byte
	switch v := value.(type) {
	case string:
		b = []byte(v)
	case []byte:
		b = v
	case nil:
		return nil
	default:
		return fmt.Errorf("doctor: unexpected database value %T", value)
	}
	if string(b) == "{}" || string(b) == "null" || len(b) == 0 {
		return nil
	}
	var r hosttune.Report
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	h.Report = &r
	return nil
}
func (s *Store) SetHostDoctor(ctx context.Context, id string, r *hosttune.Report) error {
	var at int64
	if r != nil {
		at = ms(r.CheckedAt)
	}
	_, err := s.exec(ctx, `UPDATE hosts SET doctor=?, doctor_checked_at=? WHERE id=?`, HostDoctor{r}, at, id)
	return wrapWrite(err)
}

// SetHostDoctorChecked records that a host's agent looked at `at`, without
// touching the stored body or last_heartbeat. A task result is not a heartbeat
// and must not make a wedged heartbeat loop look alive. MAX stops a result that
// overtook a newer report moving freshness backwards.
func (s *Store) SetHostDoctorChecked(ctx context.Context, id string, at time.Time) error {
	res, err := s.exec(ctx, `UPDATE hosts SET doctor_checked_at=MAX(doctor_checked_at, ?) WHERE id=?`, ms(at), id)
	if err != nil {
		return wrapWrite(err)
	}
	return affected(res, "host", id)
}
