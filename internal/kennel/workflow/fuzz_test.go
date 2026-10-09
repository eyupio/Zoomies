package workflow

import (
	"strings"
	"testing"
)

// A workflow file is written by whoever can open a pull request against the
// repository, and the collector parses it on the controller. The invariant worth
// fuzzing is not what the counts are but that no input gets past the parser's
// limits to make it panic, run away or answer differently the second time.
func FuzzInspect(f *testing.F) {
	for _, seed := range []string{
		"on: pull_request\njobs:\n  build:\n    runs-on: self-hosted\n    steps:\n      - uses: actions/checkout@v4\n      - run: echo hello\n",
		"on: push\npermissions: {}\njobs:\n  a:\n    uses: acme/ci/.github/workflows/test.yml@" + strings.Repeat("c", 40) + "\n",
		"jobs: &jobs {a: {runs-on: linux, steps: []}}",
		"jobs: {a: {runs-on: linux, steps: []}}\n---\njobs: {}",
		"on: [pull_request, pull_request_target]\njobs: {a: {runs-on: x, timeout-minutes: ${{ inputs.t }}, steps: [{uses: 'docker://alpine'}]}}",
		"jobs: " + strings.Repeat("[", 80) + strings.Repeat("]", 80),
		"",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := Inspect(data)
		if err != nil {
			if got != (Inspection{}) {
				t.Fatalf("an error came with counts: %+v", got)
			}
			return
		}
		for _, n := range []int{got.NoTimeout, got.NoConcurrency, got.FirstPartyUnpinned, got.OtherUnpinned, got.PermissionsUnset} {
			if n < 0 {
				t.Fatalf("a negative count: %+v", got)
			}
		}
		if again, err := Inspect(data); err != nil || again != got {
			t.Fatalf("the same bytes gave %+v then %+v (%v)", got, again, err)
		}
	})
}
