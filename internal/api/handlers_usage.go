package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

const maxUsageRange = 366 * 24 * time.Hour

func usageParams(r *http.Request) (time.Time, time.Time, store.UsageGroup, error) {
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	f, err := time.Parse(time.RFC3339, from)
	if err != nil {
		return f, toZero(), "", fmt.Errorf("from is required and must be RFC 3339")
	}
	t, err := time.Parse(time.RFC3339, to)
	if err != nil {
		return f, t, "", fmt.Errorf("to is required and must be RFC 3339")
	}
	if !f.Before(t) {
		return f, t, "", fmt.Errorf("from must be before to")
	}
	if t.Sub(f) > maxUsageRange {
		return f, t, "", fmt.Errorf("date range cannot exceed 366 days")
	}
	g := store.UsageGroup(r.URL.Query().Get("group_by"))
	if g == "" {
		g = store.UsageByPool
	}
	switch g {
	case store.UsageByPool, store.UsageByInstallation, store.UsageByRepository, store.UsageByWorkflow:
	default:
		return f, t, g, fmt.Errorf("group_by must be installation, repository, workflow, or pool")
	}
	return f, t, g, nil
}
func toZero() time.Time { return time.Time{} }

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	f, t, g, err := usageParams(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	rows, err := s.ctrl.Store().Usage(r.Context(), f, t, g)
	if err != nil {
		s.internal(w, r, "querying usage", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": f, "to": t, "group_by": g, "items": rows,
		"costs_are_estimates": true,
		// Told to the client even when items is empty, so the page can explain
		// an absent runner-hours column rather than leaving it blank.
		"allocation_attributable": store.UsageAllocationAttributable(g),
	})
}

func (s *Server) handleUsageCSV(w http.ResponseWriter, r *http.Request) {
	f, t, g, err := usageParams(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	rows, err := s.ctrl.Store().Usage(r.Context(), f, t, g)
	if err != nil {
		s.internal(w, r, "querying usage", err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="zoomies-usage.csv"`)
	c := csv.NewWriter(w)
	_ = c.Write([]string{"group", "job_execution_seconds", "allocated_runner_seconds", "jobs_queued", "jobs_started", "jobs_completed", "average_queue_wait_seconds", "peak_concurrency", "estimated_cost"})
	for _, x := range rows {
		cost := ""
		if x.EstimatedCost != nil {
			cost = strconv.FormatFloat(*x.EstimatedCost, 'f', 2, 64)
		}
		// A blank cell is the honest rendering of "not calculated for this
		// grouping"; a spreadsheet would sum a zero.
		_ = c.Write([]string{csvText(x.Key), fmt.Sprint(x.JobExecutionSeconds), optionalFloat(x.AllocatedRunnerSeconds),
			strconv.Itoa(x.Jobs), strconv.Itoa(x.JobsStarted), strconv.Itoa(x.JobsCompleted),
			optionalFloat(x.AverageQueueWaitSeconds), strconv.Itoa(x.PeakConcurrency), cost})
	}
	c.Flush()
}

func optionalFloat(v *float64) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(*v)
}

// csvText makes a free-text cell safe to open in a spreadsheet.
//
// Excel and Sheets evaluate a cell that begins with =, +, -, @, a tab or a
// carriage return, and CSV quoting does not stop them: the quotes are gone by
// the time the cell is read. The group column carries names that GitHub
// payloads supplied -- a workflow can be called anything, by anyone who can
// push to a repository the App can see -- so a leading formula character is
// somebody else's to choose. A leading apostrophe is the spreadsheet
// convention for "this is text", and it is not shown in the cell.
func csvText(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}
