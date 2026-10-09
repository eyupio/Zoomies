package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// The agent is the side holding the evidence when a runner dies: by the time
// the controller hears of it, the container is about to be removed and its
// output with it. A faulted exit therefore travels with its last lines, so
// the explanation of the job can quote the line that decided it; a clean
// exit carries none, because there is nothing to explain.
func TestAFaultedRunnerReportCarriesItsLastLines(t *testing.T) {
	a, _, be, _ := newAgent(t, 2)
	a.polled.Store(true)
	var out bytes.Buffer
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&out, "line %d\n", i)
	}
	be.mu.Lock()
	be.logs = func() io.ReadCloser { return io.NopCloser(bytes.NewReader(out.Bytes())) }
	be.mu.Unlock()

	track(a, "runner-1", "wl-1", true)
	be.setWorkloads(exited("wl-1", "runner-1", 137))
	reports, err := a.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if len(reports) != 1 || reports[0].State != store.RunnerFailed {
		t.Fatalf("reports = %+v, want one failed", reports)
	}
	tail := reports[0].OutputTail
	if len(tail) != outputTailLines || tail[0] != "line 21" || tail[len(tail)-1] != "line 60" {
		t.Fatalf("tail = %d lines, first %q, last %q; want the last %d", len(tail), first(tail), last(tail), outputTailLines)
	}

	track(a, "runner-2", "wl-2", true)
	be.setWorkloads(exited("wl-2", "runner-2", 0))
	reports, err = a.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	for _, r := range reports {
		if r.RunnerID == "runner-2" && len(r.OutputTail) != 0 {
			t.Fatalf("a clean exit carries no tail: %+v", strings.Join(r.OutputTail, "|"))
		}
	}
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func last(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[len(s)-1]
}
