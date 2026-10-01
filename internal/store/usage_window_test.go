package store

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"
)

// The usage report used to ask for the jobs around in a window as one
// condition on two columns, which neither index could serve. It now asks three
// questions the indexes can. Both have to name the same jobs, including the
// ones a tidy table would not have: finished with no finish time, and still
// open with a stale one.
func TestTheUsageWindowNamesTheSameJobsAsTheConditionItReplaced(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rng := rand.New(rand.NewSource(3))

	states := []string{"waiting", "queued", "in_progress", "completed"}
	for i := 0; i < 600; i++ {
		queued := base.Add(time.Duration(rng.Intn(20*24)) * time.Hour)
		state := states[rng.Intn(len(states))]
		var completed any
		switch rng.Intn(3) {
		case 0: // a finish time, plausibly after the queue time
			completed = queued.Add(time.Duration(rng.Intn(48)) * time.Hour).UnixMilli()
		case 1: // none: unfinished, or finished with the stamp lost
			completed = nil
		default: // a stamp on a job that is not finished
			completed = queued.Add(time.Hour).UnixMilli()
		}
		if _, err := s.write.ExecContext(ctx, `INSERT INTO jobs (id, github_job_id, state, queued_at, completed_at)
			VALUES (?, ?, ?, ?, ?)`, fmt.Sprintf("job_%04d", i), 5000+i, state, queued.UnixMilli(), completed); err != nil {
			t.Fatal(err)
		}
	}

	ids := func(query string, args ...any) string {
		rows, err := s.read.QueryContext(ctx, query, args...)
		if err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}

	for _, w := range []struct{ from, to time.Duration }{
		{0, 24 * time.Hour}, {5 * 24 * time.Hour, 12 * 24 * time.Hour}, {-time.Hour, 21 * 24 * time.Hour},
		{19 * 24 * time.Hour, 40 * 24 * time.Hour}, {30 * 24 * time.Hour, 31 * 24 * time.Hour},
	} {
		from, to := base.Add(w.from), base.Add(w.to)
		want := ids(`SELECT id FROM jobs WHERE queued_at < ? AND COALESCE(completed_at, ?) >= ?`, ms(to), ms(to), ms(from))
		got := ids(`SELECT id FROM `+usageJobsInWindowSQL, usageWindowArgs(from, to)...)
		if got != want {
			t.Errorf("window %s to %s: the three ranges name different jobs than the one condition\n got  %s\n want %s", from, to, got, want)
		}
		if want == "" && w.from == 0 {
			t.Error("the fixture put no job in the first window, so it proves nothing")
		}
	}
}
