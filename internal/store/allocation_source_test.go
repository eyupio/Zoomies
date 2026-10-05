package store

import "testing"

// A pool that leaves its size to its hosts is sized by them whether the figure
// is the host's plain share or its runner profile's standard size, and the
// controller judges the two the same way. A runner somebody typed a size for,
// or one that was given less or more than a share, is not.
func TestARunnerIsSizedByTheHostWhenItsLimitsAreTheHostsShareOrItsProfile(t *testing.T) {
	for source, want := range map[string]bool{
		AllocationFromHost:    true,
		AllocationFromProfile: true,
		AllocationFromPool:    false,
		AllocationReduced:     false,
		AllocationHistory:     false,
		"":                    false,
	} {
		if got := SizedByHost(source); got != want {
			t.Errorf("SizedByHost(%q) = %t, want %t", source, got, want)
		}
	}
}
