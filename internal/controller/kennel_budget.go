package controller

import (
	"time"

	"github.com/eyupio/zoomies/internal/github"
)

// kennelDefaultLimit is GitHub's primary limit for an App installation, which is
// what the budget is a share of until the installation has reported its own. A
// client that has never made a request has nothing to report, and the first
// request Kennel Club makes is what tells it.
const kennelDefaultLimit = 5000

// kennelReserveDivisor says what must be left whatever the budget is: a half
// (one over this). Below it Kennel Club stops altogether until the limit
// resets, because scaling, registration and the poller come first and have
// nothing to fall back on when the installation is nearly out.
const kennelReserveDivisor = 2

// kennelBudget is how much of one installation's hourly request limit Kennel
// Club has spent this hour.
//
// It is a rule and not a hope: the limit is shared with everything else this
// installation does -- the poller, every JIT configuration, the registration
// reap -- and a read that is slow to finish must not be able to starve them.
// Zoomies' own comments say the quota is "shared with every other call Zoomies
// makes", and the toolchain scan "does not ration itself"; this does.
//
// It is memory only, and cheap to lose: a restart forgets what was spent this
// hour, which at worst lets one hour spend its share twice, and the percentage
// is small enough that twice is still inside the limit.
type kennelBudget struct {
	window time.Time
	spent  int
}

// take reports whether one more request may start, and counts it if so.
//
// percent is the share of the limit this hour may spend, and rl is what the
// installation last reported. Every request is counted, including the ones
// GitHub answers "not modified", which cost nothing against its limit: the
// budget is Zoomies' own ceiling and a conservative count only makes it lower.
func (b *kennelBudget) take(now time.Time, percent int, rl github.RateLimit) bool {
	limit := rl.Limit
	if limit <= 0 {
		limit = kennelDefaultLimit
	}
	// Under half the limit left, and the reset not yet reached: stand down. A
	// reset time GitHub did not give is treated as not yet reached, because the
	// alternative is guessing that the quota came back.
	if rl.Limit > 0 && rl.Remaining < rl.Limit/kennelReserveDivisor && (rl.ResetAt.IsZero() || now.Before(rl.ResetAt)) {
		return false
	}
	if window := now.Truncate(time.Hour); !window.Equal(b.window) {
		b.window, b.spent = window, 0
	}
	if b.spent >= limit*percent/100 {
		return false
	}
	b.spent++
	return true
}
