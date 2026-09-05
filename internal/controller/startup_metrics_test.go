package controller

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestObserveDurationRejectsMissingOutOfOrderAndNegativeTimestamps(t *testing.T) {
	h := startupHistogram("test_startup_seconds", "test")
	now := time.Unix(100, 0)
	if observeDuration(h, "linux", "docker", time.Time{}, now) {
		t.Fatal("accepted a missing timestamp")
	}
	if observeDuration(h, "linux", "docker", now, now.Add(-time.Second)) {
		t.Fatal("accepted an out-of-order negative duration")
	}
	if !observeDuration(h, "linux", "docker", now, now.Add(250*time.Millisecond)) {
		t.Fatal("rejected a valid duration")
	}
	r := prometheus.NewRegistry()
	r.MustRegister(h)
	families, err := r.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 || len(families[0].Metric) != 1 {
		t.Fatalf("unexpected metric families: %+v", families)
	}
	m := families[0].Metric[0].GetHistogram()
	if m.GetSampleCount() != 1 || m.GetSampleSum() != .25 {
		t.Fatalf("count/sum = %d/%g", m.GetSampleCount(), m.GetSampleSum())
	}
	labels := families[0].Metric[0].Label
	if len(labels) != 2 || labels[0].GetName() != "backend" || labels[1].GetName() != "pool" {
		t.Fatalf("labels = %+v", labels)
	}
}

func TestStartupMetricsHaveOnlyBoundedDimensionLabels(t *testing.T) {
	h := startupHistogram("test_labels_seconds", "test")
	d := make(chan *prometheus.Desc, 1)
	h.Describe(d)
	desc := (<-d).String()
	for _, forbidden := range []string{"repository", "job", "runner_id", "image", "digest"} {
		if strings.Contains(desc, forbidden) {
			t.Fatalf("descriptor contains forbidden label %q: %s", forbidden, desc)
		}
	}
	if !strings.Contains(desc, "pool") || !strings.Contains(desc, "backend") {
		t.Fatalf("descriptor lacks bounded labels: %s", desc)
	}
}

func TestPercentileDurations(t *testing.T) {
	values := []int64{950, 10, 500, 100, 50}
	if got := percentile(values, .5); got != 100 {
		t.Fatalf("p50 = %d, want 100", got)
	}
	if got := percentile(values, .95); got != 950 {
		t.Fatalf("p95 = %d, want 950", got)
	}
	if got := percentile(nil, .95); got != 0 {
		t.Fatalf("missing percentile = %d, want 0", got)
	}
}
