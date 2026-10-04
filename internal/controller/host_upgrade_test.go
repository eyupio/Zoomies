package controller

import (
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

func TestHostUpgradeCommandsMatchTheControllerWithoutDowngradingAheadAgents(t *testing.T) {
	for _, tt := range []struct {
		name, agent, controller, want string
		embedded, incompatible        bool
	}{
		{"older release", "1.0", "1.2.3", "--version 'v1.2.3'", false, false},
		{"development", "main-sha-1234567", "main-sha-abcdef0", "--version 'dev'", false, false},
		{"matching", "1.2.3", "1.2.3", "", false, false},
		{"newer agent", "2.0", "1.2.3", "", false, false},
		{"local controller", "1.0", "local-dirty", "", false, false},
		{"embedded", "1.0", "1.2.3", "", true, false},
		{"protocol mismatch", "1.2.3", "1.2.3", "--version 'v1.2.3'", false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd, _, note := hostUpgrade(&store.Host{Version: tt.agent, Embedded: tt.embedded, Incompatible: tt.incompatible}, tt.controller)
			if tt.want == "" {
				if cmd != "" {
					t.Fatalf("unexpected command %q", cmd)
				}
			} else if !strings.Contains(cmd, tt.want) || !strings.Contains(cmd, "zoomies upgrade --mode agent") {
				t.Fatalf("command %q", cmd)
			}
			if strings.Contains(cmd, "join-token") {
				t.Fatal("upgrade must not enrol again")
			}
			if tt.name == "local controller" && !strings.Contains(note, "unpublished") {
				t.Fatalf("missing explanation: %s", note)
			}
		})
	}
}
