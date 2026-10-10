package provider

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"

	"github.com/eyupio/zoomies/internal/assistant"
)

// copilotAnswering records how it was run and prints an answer in two parts, as a
// tool that streams plain text does.
const copilotAnswering = `
printf '%s\n' "$@" > "$REC/args"
pwd > "$REC/cwd"
env > "$REC/env"
printf 'A runner is '
printf 'a machine.\n'
`

// The question is the argument of -p, with the conversation in it in order, and the
// answer is the text the tool prints.
func TestCopilotIsAskedWithPAndAnswersWithWhatItPrints(t *testing.T) {
	bin, rec := fakeTool(t, "copilot", copilotAnswering)
	req := codexRequest()
	req.Model = "gpt-test"
	s, err := NewCopilot(Config{Command: bin}).Chat(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	text, _, rerr, done := collect(t, s)
	_ = s.Close()
	if rerr != nil || !done || text != "A runner is a machine.\n" {
		t.Errorf("text = %q, done = %v, err = %v", text, done, rerr)
	}
	raw, _ := os.ReadFile(filepath.Join(rec, "args"))
	args := strings.SplitN(string(raw), "\n-s\n", 2)
	if len(args) != 2 || !strings.HasPrefix(args[0], "-p\n") {
		t.Fatalf("args = %q", raw)
	}
	prompt := args[0]
	for _, want := range []string{"You are Eli.", "User: What is a runner?", "Assistant: A machine.", "User: And an ephemeral one?"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt does not say %q:\n%s", want, prompt)
		}
	}
	if strings.Index(prompt, "What is a runner?") > strings.Index(prompt, "And an ephemeral one?") {
		t.Error("the conversation is out of order")
	}
	rest := strings.Split(strings.TrimSpace(args[1]), "\n")
	if !slices.Contains(rest, "--no-ask-user") {
		t.Errorf("it may ask the person something: %q", rest)
	}
	if i := slices.Index(rest, "--model"); i < 0 || rest[i+1] != "gpt-test" {
		t.Errorf("the model is not passed: %q", rest)
	}
}

// Copilot is never given the options that pre-approve its tools, so one that needs
// permission is not run: there is nobody to ask and nothing has said yes.
func TestCopilotIsNeverToldToApproveItsOwnTools(t *testing.T) {
	bin, rec := fakeTool(t, "copilot", copilotAnswering)
	s, _ := NewCopilot(Config{Command: bin}).Chat(context.Background(), codexRequest())
	collect(t, s)
	_ = s.Close()
	raw, _ := os.ReadFile(filepath.Join(rec, "args"))
	for _, bad := range []string{"--allow-all-tools", "--allow-all", "--yolo", "--allow-tool", "--allow-all-paths", "--allow-all-urls"} {
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, bad) {
				t.Errorf("args carry %q", bad)
			}
		}
	}
	if strings.Contains(string(raw), "--model") {
		t.Error("a model was passed though none was named")
	}
	cwd, _ := os.ReadFile(filepath.Join(rec, "cwd"))
	dir := strings.TrimSpace(string(cwd))
	if !strings.Contains(dir, "zoomies-eli-") {
		t.Errorf("run in %q, want a directory of its own", dir)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the directory %q was left behind", dir)
	}
}

// A token in the controller's environment outranks the person's stored sign-in, so
// none is passed: it would be used in place of their plan.
func TestCopilotIsGivenNoTokenAndNothingOfTheControllers(t *testing.T) {
	for _, k := range []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN", "ZOOMIES_ENCRYPTION_KEY"} {
		t.Setenv(k, "secret-"+k)
	}
	t.Setenv("COPILOT_HOME", "/home/svc/.copilot")
	bin, rec := fakeTool(t, "copilot", copilotAnswering)
	s, _ := NewCopilot(Config{Command: bin}).Chat(context.Background(), codexRequest())
	collect(t, s)
	_ = s.Close()
	env, _ := os.ReadFile(filepath.Join(rec, "env"))
	if strings.Contains(string(env), "secret-") {
		t.Errorf("the environment carries a secret:\n%s", env)
	}
	for _, want := range []string{"HOME=", "PATH=", "COPILOT_HOME=/home/svc/.copilot"} {
		if !strings.Contains(string(env), want) {
			t.Errorf("the environment lacks %q", want)
		}
	}
}

func TestCopilotRunsThatEndBadlyAreExplained(t *testing.T) {
	for _, tc := range []struct {
		name, script, want string
	}{
		{"nobody signed in", `echo "Error: not authenticated. Run copilot login" >&2; exit 1`, "nobody is signed in"},
		{"an old tool", `echo "error: unknown option '--no-ask-user'" >&2; exit 1`, "copilot update"},
		{"something else", `echo "rate limit reached" >&2; echo "second line" >&2; exit 1`, "the run stopped in GitHub Copilot: rate limit reached"},
		{"not authenticated alone", `echo "not authenticated" >&2; exit 1`, "nobody is signed in"},
		{"authentication required alone", `echo "authentication required" >&2; exit 1`, "nobody is signed in"},
		{"no authentication alone", `echo "no authentication found" >&2; exit 1`, "nobody is signed in"},
		{"not logged in alone", `echo "you are not logged in" >&2; exit 1`, "nobody is signed in"},
		{"nothing said and failed", `exit 4`, "without an answer"},
		{"nothing said and fine", `exit 0`, "without an answer"},
		{"only blank space", `printf '\n  \n'`, "without an answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, _ := fakeTool(t, "copilot", tc.script+"\n")
			s, err := NewCopilot(Config{Command: bin}).Chat(context.Background(), codexRequest())
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

// Text that arrives in pieces is passed on whole, and a character cut by the end of
// a read is held until the rest of it comes, not turned into a broken one.
func TestPlainTextIsPassedOnWithoutCuttingACharacterInTwo(t *testing.T) {
	const said = "héllo 日本語 🐕 done\n"
	var got strings.Builder
	answered, finished := readText(iotest.OneByteReader(strings.NewReader(said)), func(ev assistant.Event) bool {
		if !utf8.ValidString(ev.Delta) {
			t.Errorf("a piece is not whole characters: %q", ev.Delta)
		}
		got.WriteString(ev.Delta)
		return true
	})
	if got.String() != said || !answered || finished {
		t.Errorf("got %q, answered = %v, finished = %v", got.String(), answered, finished)
	}
	// A reader that has gone away stops the reading.
	_, finished = readText(strings.NewReader("abc"), func(assistant.Event) bool { return false })
	if !finished {
		t.Error("reading went on after nobody was listening")
	}
	_, finished = readText(io.NopCloser(strings.NewReader("")), func(assistant.Event) bool { return true })
	if finished {
		t.Error("an empty answer is not the end of anything")
	}
}

func TestCopilotNotBeingInstalledIsSaidWithWhatToDo(t *testing.T) {
	old := copilotCandidates
	copilotCandidates = func() []string { return []string{"/nonexistent/copilot"} }
	t.Cleanup(func() { copilotCandidates = old })
	t.Setenv("PATH", t.TempDir())
	_, err := NewCopilot(Config{}).Chat(context.Background(), codexRequest())
	if !errors.Is(err, ErrCopilotNotFound) || !strings.Contains(err.Error(), "copilot login") {
		t.Errorf("err = %v", err)
	}
	if _, err := NewCopilot(Config{}).Check(context.Background()); !errors.Is(err, ErrCopilotNotFound) {
		t.Errorf("check err = %v", err)
	}
}

// Copilot cannot say whether anyone is signed in without being asked something, so
// Test asks for one word. Its failure says to sign in, and an empty reply is not a pass.
func TestCopilotCheckAsksForOneWord(t *testing.T) {
	for _, tc := range []struct {
		name, ask, want string
	}{
		{"signed in", `echo OK`, ""},
		{"not signed in", `echo "not logged in" >&2; exit 1`, "nobody is signed in"},
		{"an empty reply", `exit 0`, "answered with nothing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, rec := fakeTool(t, "copilot", `
case "$1" in
  version) echo "GitHub Copilot CLI 1.2.3" ;;
  -p) printf '%s\n' "$@" > "$REC/ask"; `+tc.ask+` ;;
esac
`)
			got, err := NewCopilot(Config{Command: bin, Model: "m"}).Check(context.Background())
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("err = %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("err = %v, want one with %q", err, tc.want)
			}
			if tc.want == "" && (got.Model != "GitHub Copilot CLI 1.2.3" || got.UsageReported) {
				t.Errorf("result = %+v", got)
			}
			ask, _ := os.ReadFile(filepath.Join(rec, "ask"))
			if !strings.Contains(string(ask), "single word OK") || !strings.Contains(string(ask), "--no-ask-user") {
				t.Errorf("it asked %q", ask)
			}
		})
	}
}

// A Copilot that cannot say which version it is is not one that can be asked.
func TestCopilotCheckNeedsTheToolToSayWhichVersionItIs(t *testing.T) {
	bin, rec := fakeTool(t, "copilot", `
case "$1" in
  version) echo "broken" >&2; exit 1 ;;
  *) echo asked > "$REC/asked"; echo OK ;;
esac
`)
	_, err := NewCopilot(Config{Command: bin}).Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no version") {
		t.Errorf("err = %v", err)
	}
	if _, serr := os.Stat(filepath.Join(rec, "asked")); serr == nil {
		t.Error("it was asked something though it could not say its version")
	}
}
