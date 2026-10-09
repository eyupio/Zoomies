package config

import (
	"strings"
	"testing"
	"time"
)

// Update history is what an operator reads to learn why a host is behind, so
// it outlives the job history around it. The floor is there because a value of
// a few seconds is a duration and also an instruction to delete every finished
// attempt on the next pass.
func TestTheRetentionKeyHasAFloor(t *testing.T) {
	if got := Default().Retention.UpdateAttempts; got != 90*24*time.Hour {
		t.Errorf("the default update history window = %s, want 90 days", got)
	}

	for _, tc := range []struct {
		value   string
		wantErr string
	}{
		{"1h", "too short"},
		{"23h", "too short"},
		{"24h", ""},
		{"2160h", ""},
		{"0", ""}, // keep every attempt
	} {
		c := Default()
		_, err := c.SetValueString("retention.update_attempts", tc.value)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%q: unexpected error: %v", tc.value, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%q: error = %v, want one mentioning %q", tc.value, err, tc.wantErr)
		}
	}

	s, ok := LookupSetting("retention.update_attempts")
	if !ok {
		t.Fatal("retention.update_attempts is not in the settings registry")
	}
	if s.Floor != 24*time.Hour || s.Env != "ZOOMIES_RETENTION_UPDATE_ATTEMPTS" {
		t.Errorf("registry row has floor %s and env %s, want 24h and ZOOMIES_RETENTION_UPDATE_ATTEMPTS", s.Floor, s.Env)
	}
}
