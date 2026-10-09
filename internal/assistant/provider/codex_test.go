package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/assistant"
)

// fakeTool writes an executable called name that stands in for a vendor's tool: the
// script is what it does, and $REC is a directory it may record what it was given in.
func fakeTool(t *testing.T, name, script string) (bin, rec string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake tools are shell scripts")
	}
	dir := t.TempDir()
	rec = filepath.Join(dir, "rec")
	if err := os.Mkdir(rec, 0o755); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nREC='"+rec+"'\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, rec
}

// codexAnswering is a script that records how it was run and answers as `codex exec
// --json` does, including the lines about a command it ran and a file it changed,
// which are what an agent with tools produces.
const codexAnswering = `
printf '%s\n' "$@" > "$REC/args"
pwd > "$REC/cwd"
env > "$REC/env"
cat > "$REC/stdin"
echo '{"type":"thread.started","thread_id":"t"}'
echo '{"type":"turn.started"}'
echo '{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"bash -lc ls","status":"in_progress"}}'
echo '{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"bash -lc ls","aggregated_output":"SECRET-LISTING","status":"completed"}}'
echo '{"type":"item.completed","item":{"id":"item_2","type":"file_change","changes":[{"path":"x","kind":"add"}],"status":"completed"}}'
echo '{"type":"item.completed","item":{"id":"item_3","type":"reasoning","text":"thinking about it"}}'
echo '{"type":"item.completed","item":{"id":"item_4","type":"agent_message","text":"A runner is a machine."}}'
echo '{"type":"item.completed","item":{"id":"item_5","type":"agent_message","text":"An ephemeral one lives for one job."}}'
echo '{"type":"turn.completed","usage":{"input_tokens":24,"cached_input_tokens":20,"output_tokens":7,"reasoning_output_tokens":0}}'
`

func codexRequest() assistant.Request {
	return assistant.Request{
		System: "You are Eli.",
		Messages: []assistant.Message{
			{Role: assistant.RoleUser, Content: "What is a runner?"},
			{Role: assistant.RoleAssistant, Content: "A machine."},
			{Role: assistant.RoleUser, Content: "And an ephemeral one?"},
		},
	}
}

// Only what the agent says as its answer reaches Eli. What a tool did, and what it
// printed, is ignored: nothing Eli shows or does follows from it.
func TestCodexAnswersWithWhatTheAgentSaidAndNothingItsToolsDid(t *testing.T) {
	bin, rec := fakeTool(t, "codex", codexAnswering)
	s, err := NewCodex(Config{Command: bin}).Chat(context.Background(), codexRequest())
	if err != nil {
		t.Fatal(err)
	}
	text, usage, rerr, done := collect(t, s)
	_ = s.Close()
	if rerr != nil || !done {
		t.Fatalf("err = %v, done = %v", rerr, done)
	}
	if text != "A runner is a machine.\n\nAn ephemeral one lives for one job." {
		t.Errorf("text = %q", text)
	}
	for _, leaked := range []string{"SECRET-LISTING", "bash -lc", "thinking about it"} {
		if strings.Contains(text, leaked) {
			t.Errorf("the answer carries %q, which a tool did or the agent thought", leaked)
		}
	}
	if usage == nil || usage.InputTokens != 24 || usage.OutputTokens != 7 || !usage.Reported {
		t.Errorf("usage = %+v", usage)
	}
	stdin, _ := os.ReadFile(filepath.Join(rec, "stdin"))
	for _, want := range []string{"You are Eli.", "User: What is a runner?", "Assistant: A machine.", "User: And an ephemeral one?", "Do not run commands"} {
		if !strings.Contains(string(stdin), want) {
			t.Errorf("the prompt does not say %q:\n%s", want, stdin)
		}
	}
}

// Codex has no way to run without tools, so it is run in its read-only sandbox, with
// nothing saved and none of the machine's own configuration or rules, from an empty
// directory, and never with a flag that widens any of that.
func TestCodexIsRunReadOnlyInAnEmptyDirectory(t *testing.T) {
	bin, rec := fakeTool(t, "codex", codexAnswering)
	req := codexRequest()
	req.Model = "gpt-test"
	s, err := NewCodex(Config{Command: bin}).Chat(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	collect(t, s)
	_ = s.Close()
	raw, _ := os.ReadFile(filepath.Join(rec, "args"))
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for _, want := range []string{"exec", "--json", "--skip-git-repo-check", "--ephemeral", "--ignore-user-config", "--ignore-rules"} {
		if !slices.Contains(args, want) {
			t.Errorf("args lack %q: %q", want, args)
		}
	}
	if i := slices.Index(args, "--sandbox"); i < 0 || args[i+1] != "read-only" {
		t.Errorf("the sandbox is not read-only: %q", args)
	}
	if i := slices.Index(args, "--model"); i < 0 || args[i+1] != "gpt-test" {
		t.Errorf("the model is not passed: %q", args)
	}
	if args[len(args)-1] != "-" {
		t.Errorf("the prompt is not read from standard input: %q", args)
	}
	for _, bad := range []string{"--yolo", "--dangerously-bypass-approvals-and-sandbox", "--full-auto", "workspace-write", "danger-full-access"} {
		if slices.Contains(args, bad) {
			t.Errorf("args carry %q: %q", bad, args)
		}
	}
	cwd, _ := os.ReadFile(filepath.Join(rec, "cwd"))
	dir := strings.TrimSpace(string(cwd))
	if !strings.Contains(dir, "zoomies-eli-") {
		t.Errorf("run in %q, want a directory of its own", dir)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the directory %q was left behind", dir)
	}

	// Without a model named, none is passed and Codex chooses.
	bin, rec = fakeTool(t, "codex", codexAnswering)
	s, _ = NewCodex(Config{Command: bin}).Chat(context.Background(), codexRequest())
	collect(t, s)
	_ = s.Close()
	raw, _ = os.ReadFile(filepath.Join(rec, "args"))
	if strings.Contains(string(raw), "--model") {
		t.Errorf("a model was passed though none was named: %q", raw)
	}
}

// Codex is given where it lives and where its sign-in is kept, and nothing of the
// controller's: a key in the environment would bill that account and not the plan.
func TestCodexIsGivenNoKeyAndNothingOfTheControllers(t *testing.T) {
	t.Setenv("ZOOMIES_ENCRYPTION_KEY", "controller-secret")
	t.Setenv("OPENAI_API_KEY", "sk-should-not-pass")
	t.Setenv("CODEX_API_KEY", "sk-should-not-pass")
	t.Setenv("CODEX_HOME", "/home/svc/.codex")
	bin, rec := fakeTool(t, "codex", codexAnswering)
	s, err := NewCodex(Config{Command: bin}).Chat(context.Background(), codexRequest())
	if err != nil {
		t.Fatal(err)
	}
	collect(t, s)
	_ = s.Close()
	env, _ := os.ReadFile(filepath.Join(rec, "env"))
	for _, bad := range []string{"ZOOMIES_", "OPENAI_API_KEY", "CODEX_API_KEY", "controller-secret"} {
		if strings.Contains(string(env), bad) {
			t.Errorf("the environment carries %q", bad)
		}
	}
	for _, want := range []string{"HOME=", "PATH=", "CODEX_HOME=/home/svc/.codex"} {
		if !strings.Contains(string(env), want) {
			t.Errorf("the environment lacks %q", want)
		}
	}
}

func codexLines(lines ...string) []assistant.Event {
	p := &codexParse{}
	var out []assistant.Event
	for _, l := range lines {
		out = append(out, p.line([]byte(l))...)
	}
	return out
}

func TestCodexLinesAreReadAsAnswersUsageAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		lines   []string
		text    string
		usage   bool
		done    bool
		errPart string
	}{
		{"a message and the end", []string{
			`{"type":"item.completed","item":{"type":"agent_message","text":"Hi"}}`,
			`{"type":"turn.completed","usage":{"input_tokens":3,"output_tokens":1}}`}, "Hi", true, true, ""},
		{"an end with no usage", []string{
			`{"type":"item.completed","item":{"type":"agent_message","text":"Hi"}}`,
			`{"type":"turn.completed"}`}, "Hi", false, true, ""},
		{"tool lines only are no answer", []string{
			`{"type":"item.completed","item":{"type":"command_execution","command":"ls"}}`,
			`{"type":"item.completed","item":{"type":"mcp_tool_call","server":"s"}}`,
			`{"type":"item.completed","item":{"type":"web_search","query":"q"}}`,
			`{"type":"turn.completed","usage":{"input_tokens":3,"output_tokens":1}}`}, "", false, false, "without an answer"},
		{"an empty message is nothing", []string{
			`{"type":"item.completed","item":{"type":"agent_message","text":""}}`}, "", false, false, ""},
		{"a failed turn", []string{
			`{"type":"turn.failed","error":{"message":"the model is overloaded"}}`}, "", false, false, "the model is overloaded"},
		{"a failed turn that says to sign in", []string{
			`{"type":"turn.failed","error":{"message":"Not logged in. Run codex login"}}`}, "", false, false, "codex login"},
		{"an unauthorised failure", []string{
			`{"type":"turn.failed","error":{"message":"unexpected status 401 Unauthorized"}}`}, "", false, false, "nobody is signed in"},
		{"a failed turn with nothing to say falls back to the trouble reported before", []string{
			`{"type":"error","message":"quota exceeded"}`,
			`{"type":"turn.failed","error":{}}`}, "", false, false, "quota exceeded"},
		{"401 alone is not being signed in", []string{
			`{"type":"turn.failed","error":{"message":"status 401"}}`}, "", false, false, "nobody is signed in"},
		{"codex login alone says to sign in", []string{
			`{"type":"turn.failed","error":{"message":"please run codex login"}}`}, "", false, false, "nobody is signed in"},
		{"unauthorized alone says to sign in", []string{
			`{"type":"turn.failed","error":{"message":"Unauthorized"}}`}, "", false, false, "nobody is signed in"},
		{"log in again says to sign in", []string{
			`{"type":"turn.failed","error":{"message":"Your session expired, log in again"}}`}, "", false, false, "nobody is signed in"},
		{"sign in again says to sign in", []string{
			`{"type":"turn.failed","error":{"message":"please sign in again"}}`}, "", false, false, "nobody is signed in"},
		{"trouble that is recovered from is not the end", []string{
			`{"type":"error","message":"Reconnecting... 1/5"}`,
			`{"type":"item.completed","item":{"type":"agent_message","text":"Hi"}}`,
			`{"type":"turn.completed"}`}, "Hi", false, true, ""},
		{"lines it does not know are nothing", []string{
			`not json`, ``, `{"type":"something.new","x":1}`, `[1,2]`}, "", false, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var text string
			var usage, done bool
			var err error
			for _, ev := range codexLines(tc.lines...) {
				text += ev.Delta
				usage = usage || ev.Usage != nil
				done = done || ev.Done
				if ev.Err != nil {
					err = ev.Err
				}
			}
			if text != tc.text || usage != tc.usage || done != tc.done {
				t.Errorf("text = %q usage = %v done = %v, want %q %v %v", text, usage, done, tc.text, tc.usage, tc.done)
			}
			switch {
			case tc.errPart == "" && err != nil:
				t.Errorf("unexpected error %v", err)
			case tc.errPart != "" && (err == nil || !strings.Contains(err.Error(), tc.errPart)):
				t.Errorf("err = %v, want one with %q", err, tc.errPart)
			}
		})
	}
}

// A run that ends badly says what Codex reported in the stream when it did, and what
// to do about an old Codex or no sign-in when that is what its output says.
func TestCodexRunsThatEndBadlyAreExplained(t *testing.T) {
	for _, tc := range []struct {
		name, script, want string
	}{
		{"an error in the stream", `echo '{"type":"error","message":"quota exceeded"}'; exit 1`, "quota exceeded"},
		{"an old Codex", `echo "error: unexpected argument '--ignore-rules' found" >&2; exit 2`, "update it"},
		{"nobody signed in", `echo "Not logged in. Run codex login" >&2; exit 1`, "nobody is signed in"},
		{"nothing said", `exit 3`, "without an answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, _ := fakeTool(t, "codex", "cat >/dev/null\n"+tc.script+"\n")
			s, err := NewCodex(Config{Command: bin}).Chat(context.Background(), codexRequest())
			if err != nil {
				t.Fatal(err)
			}
			_, _, rerr, _ := collect(t, s)
			_ = s.Close()
			if rerr == nil || !strings.Contains(rerr.Error(), tc.want) {
				t.Errorf("err = %v, want one with %q", rerr, tc.want)
			}
		})
	}
}

func TestCodexNotBeingInstalledIsSaidWithWhatToDo(t *testing.T) {
	old := codexCandidates
	codexCandidates = func() []string { return []string{"/nonexistent/codex"} }
	t.Cleanup(func() { codexCandidates = old })
	t.Setenv("PATH", t.TempDir())
	_, err := NewCodex(Config{}).Chat(context.Background(), codexRequest())
	if !errors.Is(err, ErrCodexNotFound) || !strings.Contains(err.Error(), "codex login") {
		t.Errorf("err = %v", err)
	}
	if _, err := NewCodex(Config{}).Check(context.Background()); !errors.Is(err, ErrCodexNotFound) {
		t.Errorf("check err = %v", err)
	}
}

// Testing Codex spends nothing: it asks which version it is and whether anyone is
// signed in, and refuses an API key, which is not the plan the person means.
func TestCodexCheckReadsHowItIsSignedIn(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		code         int
		wantErr      string
	}{
		{"a ChatGPT plan", "Logged in using ChatGPT", 0, ""},
		{"an API key", "Logged in using an API key - sk-proj-***abcd", 0, "API key"},
		{"nobody", "Not logged in", 1, "codex login"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, rec := fakeTool(t, "codex", `
case "$1" in
  --version) echo "codex-cli 0.99.0" ;;
  login) echo "$@" > "$REC/login"; echo '`+tc.status+`'; exit `+string(rune('0'+tc.code))+` ;;
  *) echo "$@" > "$REC/other"; exit 9 ;;
esac
`)
			got, err := NewCodex(Config{Command: bin}).Check(context.Background())
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("err = %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("err = %v, want one with %q", err, tc.wantErr)
			}
			if tc.wantErr == "" && (got.Model != "codex-cli 0.99.0" || got.UsageReported) {
				t.Errorf("result = %+v", got)
			}
			if err != nil && strings.Contains(err.Error(), "sk-proj") {
				t.Errorf("the error carries part of the key: %v", err)
			}
			if _, err := os.Stat(filepath.Join(rec, "other")); err == nil {
				t.Error("the check ran something else, which could spend the plan")
			}
		})
	}
}

// The answer is over when the turn is, whatever the process does next: a Codex that
// has said its last word and then lingers does not hold the answer, or a slot.
func TestCodexEndsTheAnswerAtTheEndOfTheTurnWithoutWaitingForTheProcess(t *testing.T) {
	bin, _ := fakeTool(t, "codex", `cat >/dev/null
echo '{"type":"item.completed","item":{"type":"agent_message","text":"Done"}}'
echo '{"type":"turn.completed"}'
sleep 60
`)
	s, err := NewCodex(Config{Command: bin}).Chat(context.Background(), codexRequest())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	text, _, rerr, done := collect(t, s)
	_ = s.Close()
	if rerr != nil || !done || text != "Done" {
		t.Errorf("text = %q, done = %v, err = %v", text, done, rerr)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("the answer waited %v for the process to leave", time.Since(start))
	}
}

// Whatever a tool prints after the end of the answer is not part of it.
func TestLinesAfterTheEndOfTheAnswerAreNotRead(t *testing.T) {
	out := strings.NewReader(`{"type":"item.completed","item":{"type":"agent_message","text":"One"}}
{"type":"turn.completed"}
{"type":"item.completed","item":{"type":"agent_message","text":"Two"}}
`)
	var text string
	answered, finished := readLines(&codexParse{})(out, func(ev assistant.Event) bool {
		text += ev.Delta
		return true
	})
	if text != "One" || !answered || !finished {
		t.Errorf("text = %q, answered = %v, finished = %v", text, answered, finished)
	}
	// Nor is anything after a failure.
	out = strings.NewReader(`{"type":"turn.failed","error":{"message":"boom"}}
{"type":"item.completed","item":{"type":"agent_message","text":"Late"}}
`)
	text = ""
	_, finished = readLines(&codexParse{})(out, func(ev assistant.Event) bool {
		text += ev.Delta
		return true
	})
	if text != "" || !finished {
		t.Errorf("after a failure: text = %q, finished = %v", text, finished)
	}
}
