package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/assistant"
)

// fakeClaude writes an executable that stands in for Claude Code: the script is
// what it does, and `rec` is a directory it may write what it was given into.
func fakeClaude(t *testing.T, script string) (bin, rec string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake Claude Code is a shell script")
	}
	dir := t.TempDir()
	rec = filepath.Join(dir, "rec")
	if err := os.Mkdir(rec, 0o755); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "claude")
	body := "#!/bin/sh\nREC='" + rec + "'\n" + script
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, rec
}

// answering is a script that records how it was run and answers as Claude Code
// does with partial messages on.
const answering = `
printf '%s\n' "$@" > "$REC/args"
pwd > "$REC/cwd"
env > "$REC/env"
cat > "$REC/stdin"
echo '{"type":"system","subtype":"init","session_id":"s"}'
echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}}'
echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":", Eli"}}}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"Hello, Eli"}]}}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"Hello, Eli","usage":{"input_tokens":12,"output_tokens":3}}'
`

func collect(t *testing.T, s assistant.Stream) (text string, usage *assistant.Usage, err error, done bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		ev, ok := s.Next(ctx)
		if !ok {
			return
		}
		text += ev.Delta
		if ev.Usage != nil {
			usage = ev.Usage
		}
		if ev.Err != nil {
			err = ev.Err
			return
		}
		if ev.Done {
			done = true
			return
		}
	}
}

// What is typed reaches Claude Code on standard input, the answer streams back
// once and not twice, and the usage is the one it reported.
func TestClaudeCodeStreamsTheAnswerOnceWithItsUsage(t *testing.T) {
	bin, rec := fakeClaude(t, answering)
	p := NewClaudeCode(Config{Command: bin, Model: "sonnet"})
	s, err := p.Chat(context.Background(), assistant.Request{
		System: "You are Eli.",
		Messages: []assistant.Message{
			{Role: assistant.RoleUser, Content: "What is a runner?"},
			{Role: assistant.RoleAssistant, Content: "A machine."},
			{Role: assistant.RoleUser, Content: "And an ephemeral one?"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	text, usage, rerr, done := collect(t, s)
	_ = s.Close()
	if rerr != nil || !done || text != "Hello, Eli" {
		t.Errorf("text = %q, done = %v, err = %v", text, done, rerr)
	}
	if usage == nil || usage.InputTokens != 12 || usage.OutputTokens != 3 || !usage.Reported {
		t.Errorf("usage = %+v", usage)
	}
	stdin, _ := os.ReadFile(filepath.Join(rec, "stdin"))
	for _, want := range []string{"User: What is a runner?", "Assistant: A machine.", "User: And an ephemeral one?"} {
		if !strings.Contains(string(stdin), want) {
			t.Errorf("the conversation does not say %q:\n%s", want, stdin)
		}
	}
	if strings.Index(string(stdin), "What is a runner?") > strings.Index(string(stdin), "And an ephemeral one?") {
		t.Error("the conversation is out of order")
	}
}

// Claude Code is run with every way of doing anything turned off, whatever the
// question: no tool, no MCP server, nothing from files on this machine, nothing
// kept, nobody to ask. These are the flags the safety of the whole kind rests on.
func TestClaudeCodeIsRunWithEveryToolOffInAnEmptyDirectory(t *testing.T) {
	bin, rec := fakeClaude(t, answering)
	s, err := NewClaudeCode(Config{Command: bin}).Chat(context.Background(), assistant.Request{
		System: "You are Eli.", Model: "opus",
		Messages: []assistant.Message{{Role: assistant.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, s)
	cwd, _ := os.ReadFile(filepath.Join(rec, "cwd"))
	_ = s.Close()

	raw, _ := os.ReadFile(filepath.Join(rec, "args"))
	args := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	pair := func(flag, value string) bool {
		i := slices.Index(args, flag)
		return i >= 0 && i+1 < len(args) && args[i+1] == value
	}
	for _, flag := range []string{"-p", "--strict-mcp-config", "--safe-mode", "--no-session-persistence", "--verbose", "--include-partial-messages"} {
		if !slices.Contains(args, flag) {
			t.Errorf("Claude Code was run without %s: %v", flag, args)
		}
	}
	// An empty value is the whole point of the first: every built-in tool is off.
	for flag, value := range map[string]string{"--tools": "", "--permission-prompts": "none", "--output-format": "stream-json", "--system-prompt": "You are Eli.", "--model": "opus"} {
		if !pair(flag, value) {
			t.Errorf("Claude Code was run without %s %q: %v", flag, value, args)
		}
	}
	for _, dangerous := range []string{"--dangerously-skip-permissions", "--allow-dangerously-skip-permissions", "--permission-mode", "--allowedTools", "--allowed-tools", "--bare", "--mcp-config", "--add-dir"} {
		if slices.Contains(args, dangerous) {
			t.Errorf("Claude Code was run with %s", dangerous)
		}
	}
	// It ran in a directory of its own, which is gone once the answer is.
	dir := strings.TrimSpace(string(cwd))
	if dir == "" || dir == "/" || dir == os.TempDir() {
		t.Errorf("it ran in %q, not a directory of its own", dir)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Errorf("%s was left behind", dir)
	}
}

// Nothing of the controller's reaches Claude Code but what it needs to find its
// own sign-in: not a Zoomies setting and not an Anthropic key, which would make
// it bill a different account from the subscription that was asked for.
func TestClaudeCodeIsGivenNothingOfTheControllersEnvironment(t *testing.T) {
	t.Setenv("ZOOMIES_ENCRYPTION_KEY", "super-secret")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-not-for-this")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "token")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws")
	t.Setenv("HOME", "/home/the-service")
	bin, rec := fakeClaude(t, answering)
	s, err := NewClaudeCode(Config{Command: bin}).Chat(context.Background(), assistant.Request{
		Messages: []assistant.Message{{Role: assistant.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, s)
	_ = s.Close()
	env, _ := os.ReadFile(filepath.Join(rec, "env"))
	for _, forbidden := range []string{"ZOOMIES_", "ANTHROPIC_", "CLAUDE_CODE_OAUTH_TOKEN", "AWS_", "super-secret", "sk-ant"} {
		if strings.Contains(string(env), forbidden) {
			t.Errorf("Claude Code was given %s", forbidden)
		}
	}
	if !strings.Contains(string(env), "HOME=/home/the-service") {
		t.Errorf("Claude Code was not told where its sign-in is: %s", env)
	}
}

func parseAll(lines ...string) []assistant.Event {
	var p claudeParse
	var out []assistant.Event
	for _, l := range lines {
		out = append(out, p.line([]byte(l))...)
	}
	return out
}

// Each line Claude Code prints is one of a few things, and anything else is
// nothing: a newer version may print more than this knows.
func TestClaudeCodeLinesAreReadOneByOne(t *testing.T) {
	delta := `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"a"}}}`
	whole := `{"type":"assistant","message":{"content":[{"type":"text","text":"whole"},{"type":"tool_use","name":"x"}]}}`
	ok := `{"type":"result","subtype":"success","is_error":false,"result":"final","usage":{"input_tokens":1,"output_tokens":2}}`
	for _, tc := range []struct {
		name  string
		lines []string
		want  []assistant.Event
	}{
		{"a delta is text", []string{delta}, []assistant.Event{{Delta: "a"}}},
		{"a whole message is text when nothing streamed", []string{whole}, []assistant.Event{{Delta: "whole"}}},
		{"a whole message repeats the deltas and is not said twice", []string{delta, whole}, []assistant.Event{{Delta: "a"}}},
		{"a result with no text before it is the text", []string{ok}, []assistant.Event{
			{Delta: "final"}, {Usage: &assistant.Usage{InputTokens: 1, OutputTokens: 2, Reported: true}}, {Done: true}}},
		{"a result after text only ends it", []string{delta, ok}, []assistant.Event{
			{Delta: "a"}, {Usage: &assistant.Usage{InputTokens: 1, OutputTokens: 2, Reported: true}}, {Done: true}}},
		{"a result with no usage says none", []string{`{"type":"result","subtype":"success","result":"x"}`}, []assistant.Event{{Delta: "x"}, {Done: true}}},
		{"a line that is not JSON, or not known, is nothing", []string{"hello", "", "{", `{"type":"rate_limit_event"}`, `{"type":"system"}`, `[1]`}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseAll(tc.lines...); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// What Claude Code says when it cannot answer is the answer's error. The two
// it says most often are given their remedy, and the rest are shown shortened.
func TestClaudeCodeFailuresSayWhatToDo(t *testing.T) {
	for _, tc := range []struct{ said, contains string }{
		{"Not logged in · Please run /login", "claude auth login"},
		{"Invalid API key · Please run /login", "claude auth login"},
		// Each of the three phrases is enough alone; a version may drop the others.
		{"Not logged in", "claude auth login"},
		{"Please run /login", "claude auth login"},
		{"Invalid API key", "claude auth login"},
		{"Claude AI usage limit reached|1760000000", "usage limit reached"},
		{"", "the answer failed in Claude Code"},
		{strings.Repeat("x", 1000), "..."},
	} {
		ev := parseAll(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":` + quote(tc.said) + `}`)
		if len(ev) != 1 || ev[0].Err == nil || !strings.Contains(ev[0].Err.Error(), tc.contains) {
			t.Errorf("%q gave %+v, want an error with %q", tc.said, ev, tc.contains)
			continue
		}
		if len(ev[0].Err.Error()) > 400 {
			t.Errorf("an error of %d characters", len(ev[0].Err.Error()))
		}
	}
	// A result that says it is an error, in any word, is not an answer.
	if ev := parseAll(`{"type":"result","subtype":"error_max_turns","is_error":false,"result":"x"}`); len(ev) != 1 || ev[0].Err == nil {
		t.Errorf("a result of another subtype was taken as an answer: %+v", ev)
	}
}

func quote(s string) string {
	b := strings.Builder{}
	b.WriteByte('"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// A Claude Code that dies, or that is too old to take the flags, says why in
// words about what to do, and never echoes the question.
func TestClaudeCodeThatStopsSaysWhy(t *testing.T) {
	for _, tc := range []struct {
		name, script, contains string
	}{
		{"too old", "echo 'error: unknown option --safe-mode' >&2\nexit 1", "claude update"},
		{"not signed in", "echo 'Not logged in' >&2\nexit 1", "claude auth login"},
		{"another complaint", "echo 'the disk is on fire' >&2\nexit 2", "the disk is on fire"},
		{"silence", "exit 1", "stopped in Claude Code"},
		{"nothing said and no error", "exit 0", "ended in Claude Code without an answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, _ := fakeClaude(t, "cat > /dev/null\n"+tc.script)
			s, err := NewClaudeCode(Config{Command: bin}).Chat(context.Background(), assistant.Request{
				Messages: []assistant.Message{{Role: assistant.RoleUser, Content: "a private question"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _, rerr, done := collect(t, s)
			_ = s.Close()
			if done || rerr == nil || !strings.Contains(rerr.Error(), tc.contains) || strings.Contains(rerr.Error(), "private question") {
				t.Errorf("done = %v, err = %v; want an error with %q", done, rerr, tc.contains)
			}
		})
	}
}

// A Claude Code that is not there is said to be missing, wherever it was looked for.
func TestClaudeCodeThatIsNotInstalledIsSaidToBeMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	old := claudeCandidates
	claudeCandidates = func() []string { return []string{"/nonexistent/claude"} }
	t.Cleanup(func() { claudeCandidates = old })
	_, err := NewClaudeCode(Config{}).Chat(context.Background(), assistant.Request{})
	if !errors.Is(err, ErrClaudeNotFound) {
		t.Errorf("err = %v", err)
	}
	if _, err := NewClaudeCode(Config{}).Check(context.Background()); !errors.Is(err, ErrClaudeNotFound) {
		t.Errorf("check err = %v", err)
	}
}

// A copy where its installers put it is found though the service's PATH has no
// idea of it, which is how a service is usually started.
func TestClaudeCodeIsFoundWhereItIsInstalled(t *testing.T) {
	bin, _ := fakeClaude(t, "")
	t.Setenv("PATH", t.TempDir())
	old := claudeCandidates
	claudeCandidates = func() []string { return []string{"/nonexistent/claude", bin} }
	t.Cleanup(func() { claudeCandidates = old })
	got, err := claudeBinary("")
	if err != nil || got != bin {
		t.Errorf("found %q, %v", got, err)
	}
}

// The check spends nothing: it reads the version and how Claude Code is signed
// in, and it turns away anything that is not a subscription.
func TestClaudeCodeCheckReadsHowItIsSignedIn(t *testing.T) {
	script := func(status string, code int) string {
		return `
case "$1" in
  --version) echo "2.1.300 (Claude Code)" ;;
  auth) echo '` + status + `'; exit ` + string(rune('0'+code)) + ` ;;
esac
`
	}
	for _, tc := range []struct {
		name, status string
		code         int
		wantErr      string
	}{
		{"a subscription", `{"loggedIn":true,"authMethod":"claude.ai"}`, 0, ""},
		{"a long-lived token from a subscription", `{"loggedIn":true,"authMethod":"oauth_token"}`, 0, ""},
		{"an API key", `{"loggedIn":true,"authMethod":"api_key"}`, 0, "api key"},
		{"a cloud provider", `{"loggedIn":true,"authMethod":"third_party"}`, 0, "third party"},
		{"nobody", `{"loggedIn":false,"authMethod":"none"}`, 1, "claude auth login"},
		// A version that says "none" and exits cleanly is still nobody.
		{"nobody, said politely", `{"loggedIn":false,"authMethod":"none"}`, 0, "nobody is signed in"},
		{"nothing readable and a failure", `oops`, 1, "claude auth login"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, _ := fakeClaude(t, script(tc.status, tc.code))
			res, err := NewClaudeCode(Config{Command: bin}).Check(context.Background())
			if tc.wantErr == "" {
				if err != nil || res.Model != "Claude Code 2.1.300 (Claude Code)" || res.UsageReported {
					t.Errorf("res = %+v, err = %v", res, err)
				}
				return
			}
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.wantErr) {
				t.Errorf("err = %v, want one with %q", err, tc.wantErr)
			}
		})
	}
}

// Closing a stream that is still running ends the process, takes its directory
// away and gives its place to the next question, however long it had to go.
func TestClosingAClaudeCodeStreamEndsTheProcessAndFreesItsPlace(t *testing.T) {
	bin, rec := fakeClaude(t, "pwd > \"$REC/cwd\"\ncat > /dev/null\nsleep 60\n")
	s, err := NewClaudeCode(Config{Command: bin}).Chat(context.Background(), assistant.Request{
		Messages: []assistant.Message{{Role: assistant.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(claudeSlots) != 1 {
		t.Errorf("slots in use = %d, want 1", len(claudeSlots))
	}
	started := time.Now()
	_ = s.Close()
	if time.Since(started) > 10*time.Second {
		t.Errorf("closing took %v", time.Since(started))
	}
	if len(claudeSlots) != 0 {
		t.Errorf("slots in use after closing = %d", len(claudeSlots))
	}
	cwd, _ := os.ReadFile(filepath.Join(rec, "cwd"))
	if dir := strings.TrimSpace(string(cwd)); dir != "" {
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("%s was left behind", dir)
		}
	}
	// Closing again is nothing.
	_ = s.Close()
}

// A reader that gives up while Claude Code is still talking does not leave it
// stuck with nowhere to put what it says.
func TestAReaderThatStopsDoesNotHoldTheProcess(t *testing.T) {
	bin, _ := fakeClaude(t, `cat > /dev/null
i=0
while [ $i -lt 400 ]; do
  echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"word "}}}'
  i=$((i+1))
done
sleep 5
`)
	s, err := NewClaudeCode(Config{Command: bin}).Chat(context.Background(), assistant.Request{
		Messages: []assistant.Message{{Role: assistant.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	closed := make(chan struct{})
	go func() { _ = s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("closing a stream nobody was reading hung")
	}
}

// No more than two run at once; a third waits for a place, and gives up when the
// person who asked does.
func TestOnlyTwoClaudeCodesRunAtOnce(t *testing.T) {
	bin, _ := fakeClaude(t, "cat > /dev/null\nsleep 30\n")
	open := func(ctx context.Context) (assistant.Stream, error) {
		return NewClaudeCode(Config{Command: bin}).Chat(ctx, assistant.Request{
			Messages: []assistant.Message{{Role: assistant.RoleUser, Content: "hi"}},
		})
	}
	a, err := open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := open(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a third run was not made to wait: %v", err)
	}
	_ = a.Close()
	c, err := open(context.Background())
	if err != nil {
		t.Fatalf("a place that was given back was not given out: %v", err)
	}
	_ = b.Close()
	_ = c.Close()
}

func TestClaudeCodeOffersItsOwnModelAliases(t *testing.T) {
	got, err := NewClaudeCode(Config{}).Models(context.Background())
	if err != nil || !slices.Equal(got, []string{"sonnet", "opus", "haiku", "fable"}) {
		t.Errorf("models = %v, %v", got, err)
	}
	// What it hands out is its own copy.
	got[0] = "changed"
	if again, _ := NewClaudeCode(Config{}).Models(context.Background()); again[0] != "sonnet" {
		t.Error("the list can be changed from outside")
	}
}
