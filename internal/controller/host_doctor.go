package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/store"
)

// validateDoctor checks an agent's report and clears the three fields only the
// controller may write. It runs before the report is stored or published, so an
// agent that claims "accepted" is dropped on the way in and never sits in the
// database for a later reader to trust; Report.Judged clears them again on the
// way out as a second guard.
func validateDoctor(r *hosttune.Report, now time.Time) error {
	if len(r.Results) > 64 || r.CheckedAt.IsZero() || r.CheckedAt.After(now.Add(time.Minute)) {
		return fmt.Errorf("invalid host doctor report")
	}
	seen := map[string]bool{}
	for i := range r.Results {
		x := &r.Results[i]
		x.Accepted, x.Ended, x.Acceptable = nil, nil, false
		if x.ID == "" || seen[x.ID] || len(x.ID) > 100 || len(x.Title) > 200 || len(x.Current) > 4096 || len(x.Recommended) > 4096 || len(x.Rationale) > 1024 || len(x.Reason) > 4096 {
			return fmt.Errorf("invalid host doctor check")
		}
		seen[x.ID] = true
		switch x.Status {
		case hosttune.OK, hosttune.Warn, hosttune.Skip, hosttune.Error:
		default:
			return fmt.Errorf("invalid host doctor status")
		}
		switch x.Tier {
		case hosttune.Safe, hosttune.Aggressive, hosttune.Dedicated:
		default:
			return fmt.Errorf("invalid host doctor tier")
		}
	}
	return nil
}

// doctorBodyMaxAge bounds how old the stored report body may get while reports
// keep saying the same thing. It is under every staleness threshold (the page's
// three minutes, the problem's ten), and it is the most the disk figure on the
// host page can lag the "Checked" time beside it.
const doctorBodyMaxAge = 5 * time.Minute

// ingestDoctor is the one place a host's report is accepted. It writes the body
// only when the report says something the stored one does not, or the body has
// reached doctorBodyMaxAge, and publishes only then: every UPDATE on a host
// rewrites the whole record, 8 KB of report included, and a report that moved
// nothing but its own time is not news.
//
// For an unchanged report it returns the time to record as the host's
// freshness, which the caller folds into the heartbeat's own UPDATE; the next
// pass frame carries it, at most one heartbeat later. A zero time means there is
// nothing for the caller to record. The first report always writes.
func (c *Controller) ingestDoctor(ctx context.Context, h *store.Host, in *hosttune.Report) (freshAt time.Time, err error) {
	if h.Doctor.Report != nil && !in.CheckedAt.After(h.Doctor.CheckedAt) {
		return time.Time{}, nil
	}
	if h.Doctor.Report != nil && h.Doctor.SameFindings(*in) && in.CheckedAt.Sub(h.DoctorBodyAt) < doctorBodyMaxAge {
		h.Doctor.CheckedAt = in.CheckedAt
		return in.CheckedAt, nil
	}
	if err := c.st.SetHostDoctor(ctx, h.ID, in); err != nil {
		return time.Time{}, err
	}
	h.Doctor = store.HostDoctor{Report: in}
	h.DoctorBodyAt = in.CheckedAt
	c.publishHost(h)
	return time.Time{}, nil
}
