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

// The environment is the last word and the floor is not a courtesy of the
// Settings page: a deployment that sets the variable to an hour must be refused
// at start, naming the variable, and not deleted down to an hour of history.
func TestTheEnvironmentOverrideOfTheRetentionKeyKeepsTheFloor(t *testing.T) {
	override := func(value string) func(string) (string, bool) {
		return func(k string) (string, bool) {
			if k == "ZOOMIES_RETENTION_UPDATE_ATTEMPTS" {
				return value, true
			}
			return "", false
		}
	}

	c := Default()
	err := c.applyEnvFrom(override("1h"))
	if err == nil || !strings.Contains(err.Error(), "ZOOMIES_RETENTION_UPDATE_ATTEMPTS") || !strings.Contains(err.Error(), "too short") {
		t.Fatalf("an hour of update history from the environment: err = %v, want a refusal naming the variable", err)
	}
	if got := c.Retention.UpdateAttempts; got != 90*24*time.Hour {
		t.Errorf("a refused override left the window at %s, want the default 90 days", got)
	}

	c = Default()
	if err := c.applyEnvFrom(override("48h")); err != nil || c.Retention.UpdateAttempts != 48*time.Hour {
		t.Errorf("two days from the environment: window %s, err %v, want 48h and no error", c.Retention.UpdateAttempts, err)
	}
}
