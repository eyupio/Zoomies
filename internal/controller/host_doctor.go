package controller

import (
	"fmt"
	"time"

	"github.com/eyupio/zoomies/internal/hosttune"
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
