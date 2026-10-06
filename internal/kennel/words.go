package kennel

import (
	"strconv"
	"time"
)

// The helpers here build the numbers and phrases a sentence is allowed to
// contain. Nothing in this file takes a string from a snapshot.

// count says "1 run" or "4 runs".
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// days rounds a window up to whole days, and never says less than one.
func days(d time.Duration) int {
	n := int((d + 24*time.Hour - 1) / (24 * time.Hour))
	if n < 1 {
		return 1
	}
	return n
}

// forDays says "1 day" or "30 days".
func forDays(d time.Duration) string { return count(days(d), "day", "days") }

// span says how long, in the largest whole unit and no finer: "42 minutes",
// "3 hours".
func span(d time.Duration) string {
	switch {
	case d >= 2*time.Hour:
		return count(int(d/time.Hour), "hour", "hours")
	case d >= time.Hour:
		return "1 hour"
	default:
		m := int(d / time.Minute)
		if m < 1 {
			m = 1
		}
		return count(m, "minute", "minutes")
	}
}
