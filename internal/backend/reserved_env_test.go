package backend

import (
	"slices"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
)

// config.ReservedRunnerEnv is the list of variables the validator refuses in
// runners.env, spelled out there because this package cannot be imported from
// config. This is the test that comment promises: the list is every variable
// runnerEnv writes for a runner's identity and credentials, so a new one here
// is a new one there.
func TestTheValidatorReservesEveryVariableTheBackendWritesForARunner(t *testing.T) {
	written := []string{
		EnvJITConfig, EnvUpstreamJITConfig, EnvRunnerURL, EnvRunnerToken,
		EnvRunnerName, EnvRunnerLabels, EnvRunnerGroup, EnvEphemeral,
		EnvNoDefaultLabels, EnvUpstreamRegistrationToken,
	}
	for _, name := range written {
		if !slices.Contains(config.ReservedRunnerEnv, name) {
			t.Errorf("%s is written for each runner but runners.env may still set it; add it to config.ReservedRunnerEnv", name)
		}
	}
	for _, name := range config.ReservedRunnerEnv {
		if !slices.Contains(written, name) {
			t.Errorf("config.ReservedRunnerEnv names %s, which no backend writes; remove it or add it here", name)
		}
	}
}
