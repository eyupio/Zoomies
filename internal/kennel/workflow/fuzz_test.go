package workflow

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// keysAndNull is what a marshalled Facts is allowed to say in letters: its
// field names and the null of an absent location. Everything else in it is
// digits and punctuation, and must stay so.
var keysAndNull = regexp.MustCompile(`"[a-z_]+":|null`)

// A workflow file is written by whoever can open a pull request against a
// served repository, so the parser is fuzzed: it must return, within its
// limits, for any bytes, and what it returns must marshal without carrying
// any of the input's text. The type makes that true today; the fuzz target is
// what catches a field added later that carries a name or an expression.
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
		out, err := json.Marshal(facts)
		if err != nil {
			t.Fatal(err)
		}
		if leaked := inputTextIn(string(data), keysAndNull.ReplaceAllString(string(out), "")); leaked != "" {
			t.Fatalf("the facts carry %q from the input: %s", leaked, out)
		}
	})
}

// inputTextIn returns the first run of four or more printable bytes of the
// input, holding at least one letter, that appears in the marshalled facts.
func inputTextIn(input, marshalled string) string {
	for _, run := range strings.FieldsFunc(input, func(r rune) bool { return r < 0x21 || r > 0x7e }) {
		if len(run) >= 4 && strings.ContainsFunc(run, func(r rune) bool { return r >= 'A' && r <= 'z' }) && strings.Contains(marshalled, run) {
			return run
		}
	}
	return ""
}

// The guard above is only worth having if it fires: a marshalled document
// that did carry a line of the input must be caught.
func TestTheFuzzGuardCatchesInputTextInTheFacts(t *testing.T) {
	input := "jobs:\n  build:\n    runs-on: secret-pool\n"
	if got := inputTextIn(input, `{"jobs":1,"runs_on":"secret-pool"}`); got != "secret-pool" {
		t.Fatalf("leak = %q, want secret-pool", got)
	}
	if got := inputTextIn(input, `{"jobs":1,"line":12}`); got != "" {
		t.Fatalf("a document of numbers was taken for a leak: %q", got)
	}
}
