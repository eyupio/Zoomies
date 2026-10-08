package controller

import "errors"

// ErrUpdateCheckDisabled is the answer to asking for a release check while
// updates.check_interval is 0. That setting is the air-gap switch: it keeps the
// one request Zoomies makes to github.com that is not about the fleet from ever
// being made, and a button must not make it either.
var ErrUpdateCheckDisabled = errors.New("the release check is switched off, so this controller does not ask github.com which release is current; set updates.check_interval to a duration such as 24h to turn it on")
