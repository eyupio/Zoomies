package config

import (
	"strings"
	"testing"
)

// The agent refuses a heartbeat under a second when it starts, and the
// controller starts one inside itself. A sub-second value that the Settings
// page accepted was therefore stored as restart-required and then stopped the
// controller coming back up, with only `zoomies config unset` to recover.
func TestAHeartbeatIntervalTheAgentWouldRefuseIsRefusedWhenSet(t *testing.T) {
	for _, tc := range []struct {
		value   string
		wantErr string
	}{
		{"500ms", "too short"},
		{"1s", ""},
		{"30s", ""},
		{"0", ""}, // zero means the agent's own default
	} {
		c := Default()
		_, err := c.SetValueString("agent.heartbeat_interval", tc.value)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%q: unexpected error: %v", tc.value, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%q: error = %v, want one mentioning %q", tc.value, err, tc.wantErr)
		}
	}
}
