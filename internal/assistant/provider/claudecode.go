package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/assistant"
)

// ClaudeCode answers by running Claude Code, the person's own signed-in copy, on
// the machine the controller runs on.
//
// It exists so that somebody with a Claude subscription can use it through Eli
// without Zoomies ever holding it. The sign-in lives in Claude Code's own store
// and is made through Anthropic's own flow; this package never reads it, never
// asks for a key and never sends one. It runs the binary as published, with
// every tool turned off, in an empty directory and with an environment of its
// own, so the model cannot read a file, run a command or reach a server on this
// machine, whatever it is asked.
//
// Who may use it is not decided here: Anthropic's terms are that each person
// uses their own subscription, and the controller refuses anybody else.
type ClaudeCode struct {
	cfg Config
}

// NewClaudeCode returns the adapter. cfg.Command, when set, is the binary to
// run, and otherwise it is looked for.
func NewClaudeCode(cfg Config) *ClaudeCode { return &ClaudeCode{cfg: cfg} }

// claudeSlots bounds how many copies run at once. Each is a process of its own
// that can hold a few hundred megabytes, and the questions of one controller are
// few; the rest wait their turn.
var claudeSlots = make(chan struct{}, 2)

// claudeAliases are the model names Claude Code documents. They follow Claude Code
// as it is updated, which a full model name does not.
var claudeAliases = []string{"sonnet", "opus", "haiku", "fable"}

// ErrClaudeNotFound is Claude Code not being on this machine.
var ErrClaudeNotFound = errors.New("the machine the controller runs on has no Claude Code: " +
	"install it there, sign in as the user the controller runs as with `claude auth login`, and test again")

var errClaudeNotSignedIn = errors.New("nobody is signed in to Claude Code on this machine: " +
	"sign in as the user the controller runs as with `claude auth login`, and test again")

// claudeBinary finds the executable: the one named, or one on the PATH, or one
// where its installers put it.
func claudeBinary(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if p, err := exec.LookPath("claude"); err == nil {
		return p, nil
	}
	for _, c := range claudeCandidates() {
		if st, err := os.Stat(c); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return c, nil
		}
	}
	return "", ErrClaudeNotFound
}

// claudeCandidates are the places Claude Code's installers put it, for a service
// whose PATH does not include them. A variable so a test is not at the mercy of a
// Claude Code that happens to be installed where it runs.
var claudeCandidates = func() []string {
	var out []string
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, ".local", "bin", "claude"), filepath.Join(home, ".claude", "local", "claude"))
	}
	return append(out, "/usr/local/bin/claude", "/opt/homebrew/bin/claude")
}

// claudeEnvKeep is the only part of this process's environment Claude Code is
// given: where it lives and where its sign-in is kept, and nothing of the
// controller's. In particular no ZOOMIES_ variable and no Anthropic key: a key
// in the environment would make it bill an API account instead of the person's
// subscription, which is not what was asked for.
var claudeEnvKeep = map[string]bool{
	"HOME": true, "PATH": true, "USER": true, "LOGNAME": true, "LANG": true, "LC_ALL": true,
	"TZ": true, "TMPDIR": true, "XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true,
	"XDG_CACHE_HOME": true, "XDG_STATE_HOME": true, "CLAUDE_CONFIG_DIR": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "HTTPS_PROXY": true, "https_proxy": true,
	"NO_PROXY": true, "no_proxy": true,
}

func claudeEnv(environ []string) []string {
	var out []string
	for _, kv := range environ {
		if k, _, ok := strings.Cut(kv, "="); ok && claudeEnvKeep[k] {
			out = append(out, kv)
		}
	}
	return out
}

// claudeArgs is how Claude Code is run. Every flag is there for a reason: the
// print mode and stream so there is something to read, no built-in tool and no
// MCP server so there is nothing to do, safe mode so no file on this machine
// (a CLAUDE.md, a hook, a plugin) changes that, no saved session so the question
// is not kept, and no prompts so nothing waits for a person who is not there.
func claudeArgs(system, model string) []string {
	args := []string{
		"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--tools", "",
		"--strict-mcp-config",
		"--safe-mode",
		"--no-session-persistence",
		"--permission-prompts", "none",
	}
	if system != "" {
		args = append(args, "--system-prompt", system)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	return append(args, "Reply to the last message of the conversation you are given on standard input.")
}

// claudePrompt is the conversation as text for standard input: Claude Code takes
// one prompt, and the controller keeps no session for it.
func claudePrompt(messages []assistant.Message) string {
	var b strings.Builder
	b.WriteString("This is the conversation so far. Reply as the assistant to the last message from the user.\n")
	for _, m := range messages {
		switch m.Role {
		case assistant.RoleUser:
			b.WriteString("\nUser: ")
		case assistant.RoleAssistant:
			b.WriteString("\nAssistant: ")
		default:
			continue
		}
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// Chat implements assistant.Provider.
func (p *ClaudeCode) Chat(ctx context.Context, req assistant.Request) (assistant.Stream, error) {
	bin, err := claudeBinary(p.cfg.Command)
	if err != nil {
		return nil, err
	}
	select {
	case claudeSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	dir, err := os.MkdirTemp("", "zoomies-eli-")
	if err != nil {
		<-claudeSlots
		return nil, fmt.Errorf("making a directory for Claude Code to run in: %w", err)
	}
	model := req.Model
	if model == "" {
		model = p.cfg.Model
	}
	cmd := exec.CommandContext(ctx, bin, claudeArgs(req.System, model)...)
	cmd.Dir = dir
	cmd.Env = claudeEnv(os.Environ())
	cmd.Stdin = strings.NewReader(claudePrompt(req.Messages))
	cmd.WaitDelay = 3 * time.Second
	ownGroup(cmd)
	stderr := &limitedBuffer{limit: 8 << 10}
	cmd.Stderr = stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		<-claudeSlots
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		<-claudeSlots
		return nil, fmt.Errorf("starting Claude Code: %w", err)
	}
	s := &claudeStream{cmd: cmd, dir: dir, events: make(chan assistant.Event, 16), done: make(chan struct{}), quit: make(chan struct{})}
	go s.read(out, stderr)
	return s, nil
}

// limitedBuffer keeps the first limit bytes written to it and drops the rest.
type limitedBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.limit - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// claudeStream is a question in progress: the process, and what it has said.
type claudeStream struct {
	cmd    *exec.Cmd
	dir    string
	events chan assistant.Event
	done   chan struct{}
	quit   chan struct{}
	once   sync.Once
}

// send hands an event to whoever is reading, unless they have closed the stream:
// a reader that stopped must not leave the process waiting for a place to put
// what it says.
func (s *claudeStream) send(ev assistant.Event) bool {
	select {
	case s.events <- ev:
		return true
	case <-s.quit:
		return false
	}
}

// Next implements assistant.Stream.
func (s *claudeStream) Next(ctx context.Context) (assistant.Event, bool) {
	select {
	case ev, ok := <-s.events:
		return ev, ok
	case <-ctx.Done():
		return assistant.Event{}, false
	}
}

// Close implements assistant.Stream: it ends the process if it is still running,
// waits for it, and gives the directory and the slot back.
func (s *claudeStream) Close() error {
	s.once.Do(func() {
		close(s.quit)
		_ = killGroup(s.cmd)
		<-s.done
		_ = os.RemoveAll(s.dir)
		<-claudeSlots
	})
	return nil
}

// read turns what Claude Code prints into events, and says how it ended.
func (s *claudeStream) read(out io.Reader, stderr *limitedBuffer) {
	defer close(s.done)
	defer close(s.events)
	var st claudeParse
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		for _, ev := range st.line(sc.Bytes()) {
			if !s.send(ev) {
				_ = killGroup(s.cmd)
				_ = s.cmd.Wait()
				return
			}
			if ev.Done || ev.Err != nil {
				// The answer is over; what is left is the process leaving.
				_ = killGroup(s.cmd)
				_ = s.cmd.Wait()
				return
			}
		}
	}
	err := s.cmd.Wait()
	switch {
	case st.sawText:
		s.send(assistant.Event{Done: true})
	case err != nil:
		s.send(assistant.Event{Err: claudeExit(err, stderr.String())})
	default:
		s.send(assistant.Event{Err: errors.New("the run ended in Claude Code without an answer")})
	}
}

// claudeParse reads the lines of Claude Code's stream-json output, one at a time,
// and says what each is. It is a value so a test can feed it lines.
type claudeParse struct {
	sawText bool
	sawPart bool
}

type claudeLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
	Event   struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// line interprets one line. A line it does not understand is nothing: a newer
// Claude Code may print more than this one knows, and that is not an error.
func (c *claudeParse) line(raw []byte) []assistant.Event {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var l claudeLine
	if json.Unmarshal(raw, &l) != nil {
		return nil
	}
	switch l.Type {
	case "stream_event":
		if l.Event.Type == "content_block_delta" && l.Event.Delta.Type == "text_delta" && l.Event.Delta.Text != "" {
			c.sawText, c.sawPart = true, true
			return []assistant.Event{{Delta: l.Event.Delta.Text}}
		}
	case "assistant":
		// With partial messages on, this repeats what the deltas said.
		if c.sawPart {
			return nil
		}
		var out []assistant.Event
		for _, b := range l.Message.Content {
			if b.Type == "text" && b.Text != "" {
				c.sawText = true
				out = append(out, assistant.Event{Delta: b.Text})
			}
		}
		return out
	case "result":
		if l.IsError || (l.Subtype != "" && l.Subtype != "success") {
			return []assistant.Event{{Err: claudeFailure(l.Result)}}
		}
		var out []assistant.Event
		if !c.sawText && l.Result != "" {
			c.sawText = true
			out = append(out, assistant.Event{Delta: l.Result})
		}
		if l.Usage != nil {
			out = append(out, assistant.Event{Usage: &assistant.Usage{InputTokens: l.Usage.InputTokens, OutputTokens: l.Usage.OutputTokens, Reported: true}})
		}
		return append(out, assistant.Event{Done: true})
	}
	return nil
}

// claudeFailure is what Claude Code said when it could not answer, for the
// person. The two it says most often are given their remedy; anything else is
// shown as it was said, shortened, because it is written for a person too.
func claudeFailure(said string) error {
	said = strings.TrimSpace(said)
	lower := strings.ToLower(said)
	switch {
	case strings.Contains(lower, "not logged in"), strings.Contains(lower, "/login"), strings.Contains(lower, "invalid api key"):
		return errClaudeNotSignedIn
	case said == "":
		return errors.New("the answer failed in Claude Code")
	}
	return fmt.Errorf("the answer failed in Claude Code: %s", clip(said, 300))
}

// claudeExit is a process that ended badly with nothing said in the stream. Its
// stderr is what it complained of, and is shown to a person who can act on it,
// which is the controller's operator.
func claudeExit(err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "unknown option"), strings.Contains(lower, "unknown argument"), strings.Contains(lower, "unrecognized"):
		return errors.New("this version of Claude Code does not accept the options Zoomies needs to run it safely; update it with `claude update` and test again")
	case strings.Contains(lower, "not logged in"), strings.Contains(lower, "/login"):
		return errClaudeNotSignedIn
	case stderr != "":
		first, _, _ := strings.Cut(stderr, "\n")
		return fmt.Errorf("the run stopped in Claude Code: %s", clip(first, 300))
	}
	return fmt.Errorf("the run stopped in Claude Code without an answer (%v)", err)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "..."
}

// Check implements assistant.Provider. It spends no usage: it asks Claude Code
// which version it is and how it is signed in, and says so if it is not signed in
// with a subscription.
func (p *ClaudeCode) Check(ctx context.Context) (assistant.CheckResult, error) {
	start := time.Now()
	bin, err := claudeBinary(p.cfg.Command)
	if err != nil {
		return assistant.CheckResult{}, err
	}
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = claudeEnv(os.Environ())
		cmd.Dir = os.TempDir()
		return cmd.Output()
	}
	ver, err := run("--version")
	if err != nil {
		return assistant.CheckResult{}, fmt.Errorf("no version from Claude Code: %w", err)
	}
	version := strings.TrimSpace(string(ver))
	status, err := run("auth", "status")
	var auth struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
	}
	_ = json.Unmarshal(status, &auth)
	if err != nil || (auth.AuthMethod == "" && !auth.LoggedIn) || auth.AuthMethod == "none" {
		return assistant.CheckResult{}, errClaudeNotSignedIn
	}
	switch auth.AuthMethod {
	case "claude.ai", "oauth_token", "":
	default:
		return assistant.CheckResult{}, fmt.Errorf("this machine\u2019s Claude Code is signed in with %s, not with a Claude subscription; "+
			"sign in with your Claude account (`claude auth login`), or use the Anthropic provider for an API key", strings.ReplaceAll(auth.AuthMethod, "_", " "))
	}
	return assistant.CheckResult{Model: "Claude Code " + version, Latency: time.Since(start), UsageReported: false}, nil
}

// Models implements assistant.ModelLister with Claude Code's own aliases.
func (p *ClaudeCode) Models(context.Context) ([]string, error) {
	return append([]string(nil), claudeAliases...), nil
}
