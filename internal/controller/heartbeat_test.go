package controller

import (
	"testing"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// The validator warns about a heartbeat interval from a constant of its own,
// because config sits below the store and cannot import it. This is what
// keeps the two the same number.
func TestTheValidatorAndTheStoreAgreeOnWhenAHostIsLost(t *testing.T) {
	if config.HostLostAfter != store.HeartbeatTimeout {
		t.Fatalf("config.HostLostAfter = %s but store.HeartbeatTimeout = %s; the heartbeat warning would name the wrong silence",
			config.HostLostAfter, store.HeartbeatTimeout)
	}
}
