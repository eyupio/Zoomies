package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	return cliBinary(override, "claude", claudeCandidates(), ErrClaudeNotFound)
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

// claudeEnv is the only part of this process's environment Claude Code is given:
// where it lives and where its sign-in is kept, and nothing of the controller's. In
// particular no ZOOMIES_ variable and no Anthropic key: a key in the environment
// would make it bill an API account instead of the person's subscription, which is
// not what was asked for.
func claudeEnv(environ []string) []string { return cliEnv(environ, "CLAUDE_CONFIG_DIR") }

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
	model := req.Model
	if model == "" {
		model = p.cfg.Model
	}
	parse := &claudeParse{}
	return startCLI(ctx, cliCommand{
		tool:   "Claude Code",
		bin:    bin,
		args:   claudeArgs(req.System, model),
		env:    claudeEnv(os.Environ()),
		stdin:  claudePrompt(req.Messages),
		reader: readLines(parse),
		exit:   claudeExit,
	})
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

// answered is whether anything was said as an answer.
func (c *claudeParse) answered() bool { return c.sawText }

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
