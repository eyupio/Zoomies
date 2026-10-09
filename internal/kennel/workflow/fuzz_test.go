package workflow

import (
	"encoding/json"
	"testing"
)

// A workflow file is written by whoever can open a pull request against a
// served repository, so the parser is fuzzed: it must return, within its
// limits, for any bytes, and what it returns must marshal.
func FuzzInspect(f *testing.F) {
	for _, seed := range []string{
		"on: push\njobs:\n  a:\n    runs-on: x\n    steps:\n      - uses: actions/checkout@v4\n      - run: echo ${{ secrets.T }}\n",
		"on: pull_request_target\njobs:\n  a:\n    runs-on: x\n    steps:\n      - uses: actions/checkout@v4\n        with:\n          ref: ${{ github.head_ref }}\n",
		"jobs: {a: {runs-on: [self-hosted, linux], steps: []}}",
		"jobs: &j {a: {runs-on: x, steps: []}}",
		"",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		facts, err := Inspect(data)
		if err != nil {
			return
		}
		if facts.Jobs == 0 {
			t.Fatal("a workflow without jobs was accepted")
		}
		if _, err := json.Marshal(facts); err != nil {
			t.Fatal(err)
		}
	})
}
