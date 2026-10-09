package controller

import "errors"

// ErrUpdateCheckDisabled is the answer to asking for a release check while
// updates.check_interval is 0. That setting is the air-gap switch: it keeps the
// one request Zoomies makes to github.com that is not about the fleet from ever
// being made, and a button must not make it either.
var ErrUpdateCheckDisabled = errors.New("the release check is switched off, so this controller does not ask github.com which release is current; set updates.check_interval to a duration such as 24h to turn it on")

// The refusals of an update. Each is a sentinel so that the API can give it a
// stable code, and each is written for the person who pressed the button: what
// stopped it, and what to change. A refusal that needs a detail (which folder,
// which release) wraps its sentinel, so the code survives the detail.
var (
	// ErrUpdateModeOff refuses every update while updates.mode is off, which is
	// the promise that mode makes: Zoomies says a release exists and does nothing
	// about it.
	ErrUpdateModeOff = errors.New("updates.mode is off, so this controller updates nothing; set updates.mode to manual on the Settings page to update from here")
	// ErrUpdateHelperMissing refuses an update nothing on the host could carry
	// out. Only the helper, installed by the host's owner, may replace a binary.
	ErrUpdateHelperMissing = errors.New("the update helper is not ready on this host")
	// ErrUpdateInProgress refuses a second update while one is open for the same
	// target, so that two people, or a person and the planner, never send two
	// requests.
	ErrUpdateInProgress = errors.New("an update is already in flight for this target; wait for it to finish, or for it to time out after 90 minutes, before asking again")
	// ErrUpdateNotARelease refuses to update a build that did not come from a
	// release. A build from main is usually ahead of every release, and taking one
	// would be a downgrade.
	ErrUpdateNotARelease = errors.New("this controller is not running a release, so no release can be compared with it: a build from main is usually ahead of every release; install a release with zoomies upgrade to update from here")
	// ErrUpdateNothingNewer refuses an update that would change nothing, or would
	// go backwards.
	ErrUpdateNothingNewer = errors.New("there is no newer release to take")
	// ErrUpdateHostCannotUpdate refuses to update a host whose agent cannot
	// update itself, because no helper has been installed on it.
	ErrUpdateHostCannotUpdate = errors.New("this host cannot update itself: its agent does not offer it, which it does only once the update helper is installed on the host; run \"sudo zoomies updates helper install\" there, or update it by hand")
	// ErrUpdateRolloutHalted refuses to start an update while a rollout is halted
	// after a failure, which is waiting for a person to look.
	ErrUpdateRolloutHalted = errors.New("the rollout is halted after a failed update; resume it or cancel it before starting another")
	// ErrUpdateFenced refuses an update while this controller may not act: the
	// fleet is fenced for recovery, or another controller holds the database's
	// lease. A restored copy must not replace the binary of a live deployment.
	ErrUpdateFenced = errors.New("this controller may not act right now, so it starts no update")
)
