package kennel

import (
	"testing"
	"time"
)

// A window the sentence reports must never be shorter than the one that was
// read: "in the last 1 day" over a thirty-hour window would understate what the
// finding is built on.
func TestAWindowIsReportedInWholeDaysRoundedUpAndNeverBelowOne(t *testing.T) {
	tests := []struct {
		window time.Duration
		want   string
	}{
		{0, "1 day"},
		{time.Hour, "1 day"},
		{24 * time.Hour, "1 day"},
		{25 * time.Hour, "2 days"},
		{36 * time.Hour, "2 days"},
		{30 * 24 * time.Hour, "30 days"},
	}
	for _, tt := range tests {
		if got := forDays(tt.window); got != tt.want {
			t.Errorf("forDays(%s) = %q, want %q", tt.window, got, tt.want)
		}
	}
}

func TestADurationIsSaidInItsLargestWholeUnit(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "1 minute"},
		{30 * time.Second, "1 minute"},
		{10 * time.Minute, "10 minutes"},
		{59*time.Minute + 59*time.Second, "59 minutes"},
		{time.Hour, "1 hour"},
		{119 * time.Minute, "1 hour"},
		{2 * time.Hour, "2 hours"},
		{25 * time.Hour, "25 hours"},
	}
	for _, tt := range tests {
		if got := span(tt.d); got != tt.want {
			t.Errorf("span(%s) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestACountAgreesWithItsNoun(t *testing.T) {
	if got := count(1, "job", "jobs"); got != "1 job" {
		t.Errorf("got %q", got)
	}
	for _, n := range []int{0, 2, 11} {
		if got := count(n, "job", "jobs"); got[len(got)-4:] != "jobs" {
			t.Errorf("count(%d) = %q", n, got)
		}
	}
}
