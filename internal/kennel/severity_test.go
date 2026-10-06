package kennel_test

import (
	"testing"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/kennel"
)

// This package does not import internal/config, so that it stays pure and
// small, but the severities it reports are the problems list's own and the UI
// renders both with one badge. This is what holds the two vocabularies equal.
func TestTheSeveritiesAreTheProblemListsOwn(t *testing.T) {
	pairs := []struct {
		kennel kennel.Severity
		config config.Severity
	}{
		{kennel.SeverityError, config.SeverityError},
		{kennel.SeverityWarning, config.SeverityWarning},
		{kennel.SeverityInfo, config.SeverityInfo},
	}
	for _, p := range pairs {
		if string(p.kennel) != string(p.config) {
			t.Errorf("kennel %q != config %q", p.kennel, p.config)
		}
	}
}
