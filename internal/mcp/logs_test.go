package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// logAPI serves one log body for any download and remembers what was asked for.
type logAPI struct {
	body io.Reader
	got  string
}

func (l *logAPI) Stream(_ context.Context, path, _ string) (io.ReadCloser, error) {
	l.got = path
	return io.NopCloser(l.body), nil
}

func (l *logAPI) Call(context.Context, string, string, url.Values) ([]byte, error) {
	return nil, refusal{404}
}

func runnerLog(t *testing.T, l *logAPI, lines int) (framing, block string) {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"runner_id": "run_1", "lines": lines})
	content, err := getRunnerLog(context.Background(), l, args)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 2 {
		t.Fatalf("want a notice and a block, got %+v", content)
	}
	return content[0].Text, content[1].Text
}

// numbered is a log of n short lines, generated as it is read so that a test of
// the size limit does not have to hold the whole thing itself.
func numbered(t *testing.T, n int) io.Reader {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pr.Close() })
	go func() {
		for i := 1; i <= n; i++ {
			if _, err := pw.Write([]byte("line " + strconv.Itoa(i) + "\n")); err != nil {
				return
			}
		}
		_ = pw.Close()
	}()
	return pr
}

// The tool is for diagnosing a job that failed, and the lines that matter are
// the last ones. Reading a log from its start and keeping the end of the
// first 32 MiB hands the model output from long before the failure, so the
// controller is asked for the end itself.
func TestRunnerLogAsksTheControllerForItsEndRatherThanReadingFromTheStart(t *testing.T) {
	l := &logAPI{body: strings.NewReader("only line\n")}
	runnerLog(t, l, 50)
	if !strings.HasPrefix(l.got, "/runners/run_1/logs/download") || !strings.Contains(l.got, "tail=50") {
		t.Errorf("the download asked for %q, want it to carry tail=50", l.got)
	}
}

// A controller that predates the tail parameter sends the whole log. What the
// tool keeps of it is the first 32 MiB, so the last lines it shows are not the
// end of the log, and saying they are sends the model diagnosing from
// unrelated output.
func TestRunnerLogSaysWhenWhatItReadIsNotTheEndOfTheLog(t *testing.T) {
	// A little over the limit: 32 MiB is about 3.4 million of these lines.
	l := &logAPI{body: numbered(t, 4_000_000)}
	framing, block := runnerLog(t, l, 5)

	if !strings.Contains(framing, "not the end of the log") {
		t.Errorf("the notice does not say the tail is not the log's end:\n%s", framing)
	}
	if strings.Contains(block, "line 4000000") {
		t.Errorf("the block reached past what the tool read:\n%s", block)
	}
}

func TestRunnerLogDoesNotClaimATruncationForALogItReadWhole(t *testing.T) {
	l := &logAPI{body: numbered(t, 1000)}
	framing, block := runnerLog(t, l, 5)
	if strings.Contains(framing, "not the end") {
		t.Errorf("a whole log was described as cut:\n%s", framing)
	}
	if !strings.HasSuffix(block, "line 1000") || !strings.Contains(framing, "last 5 of 1000 lines") {
		t.Errorf("the ordinary framing changed:\n%s\n%s", framing, block)
	}
}

// The lines limit bounds how many lines come back, not how long they are:
// 2000 lines of 16 KiB would otherwise put some 32 MB of untrusted text into
// the model's context in one block.
func TestRunnerLogBoundsWhatItReturnsInBytes(t *testing.T) {
	long := strings.Repeat("x", 16<<10)
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		b.WriteString(long)
		b.WriteString(strconv.Itoa(i))
		b.WriteString("\n")
	}
	l := &logAPI{body: strings.NewReader(b.String())}
	framing, block := runnerLog(t, l, 2000)

	if len(block) > 256<<10 {
		t.Errorf("the block is %d bytes, over the %d it is held to", len(block), 256<<10)
	}
	if !strings.HasSuffix(block, "1999") {
		t.Errorf("shortening kept the wrong end of the log; it must keep the last lines:\n...%s", block[max(0, len(block)-40):])
	}
	if !strings.Contains(framing, "shortened") {
		t.Errorf("the notice does not say the block was shortened:\n%s", framing)
	}
}
