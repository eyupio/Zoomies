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
	writeJSON(w, http.StatusOK, map[string]any{"from": f, "to": t, "group_by": g, "items": rows, "costs_are_estimates": true})
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
	_ = c.Write([]string{"group", "job_execution_seconds", "allocated_runner_seconds", "jobs", "average_queue_wait_seconds", "peak_concurrency", "estimated_cost"})
	for _, x := range rows {
		cost := ""
		if x.EstimatedCost != nil {
			cost = strconv.FormatFloat(*x.EstimatedCost, 'f', 2, 64)
		}
		_ = c.Write([]string{x.Key, fmt.Sprint(x.JobExecutionSeconds), fmt.Sprint(x.AllocatedRunnerSeconds), strconv.Itoa(x.Jobs), fmt.Sprint(x.AverageQueueWaitSeconds), strconv.Itoa(x.PeakConcurrency), cost})
	}
	c.Flush()
}
