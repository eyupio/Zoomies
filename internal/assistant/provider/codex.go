package provider

import (
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

// Codex answers by running Codex, the person's own signed-in copy, on the machine
// the controller runs on, so that somebody with a ChatGPT plan can use it through
// Eli without Zoomies holding the login. See cli.go for what the tools share.
//
// Unlike Claude Code, Codex has no way to run with every tool off. It is run in
// its read-only sandbox, in an empty directory, with nothing of the controller in
// its environment, and what it says about running a command or changing a file is
// ignored: it is not shown, and nothing Eli does follows from it. That is a
// narrower promise than "it cannot read a file", and the documentation says so.
type Codex struct {
	cfg Config
}

// NewCodex returns the adapter. cfg.Command, when set, is the binary to run.
func NewCodex(cfg Config) *Codex { return &Codex{cfg: cfg} }

// ErrCodexNotFound is Codex not being on this machine.
var ErrCodexNotFound = errors.New("the machine the controller runs on has no Codex: " +
	"install it there, sign in as the user the controller runs as with `codex login`, and test again")

var errCodexNotSignedIn = errors.New("nobody is signed in to Codex on this machine: " +
	"sign in as the user the controller runs as with `codex login`, and test again")

var errCodexAPIKey = errors.New("this machine’s Codex is signed in with an API key, not with a ChatGPT plan; " +
	"sign in with your ChatGPT account (`codex login`), or use the OpenAI provider for an API key")

func codexBinary(override string) (string, error) {
	return cliBinary(override, "codex", codexCandidates(), ErrCodexNotFound)
}

// codexCandidates are the places Codex's installers put it, for a service whose PATH
// does not include them. A variable so a test is not at the mercy of a Codex that
// happens to be installed where it runs.
var codexCandidates = func() []string {
	var out []string
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, ".local", "bin", "codex"), filepath.Join(home, ".npm-global", "bin", "codex"))
	}
	return append(out, "/usr/local/bin/codex", "/opt/homebrew/bin/codex")
}

// codexEnv is Codex's environment: where it lives and where its sign-in is kept
// (CODEX_HOME), and no key. An API key in the environment would make it bill that
// account and not the person's plan, which is not what was asked for.
func codexEnv(environ []string) []string { return cliEnv(environ, "CODEX_HOME") }

// codexArgs is how Codex is run. The prompt is on standard input ("-"). The sandbox
// is the read-only one, nothing is saved, and neither the person's own
// configuration nor their rules files are read, so what a Codex on this machine
// has been set up to do cannot widen any of it. There is no approval flag: exec has
// nobody to ask, and a command the sandbox does not allow does not run.
func codexArgs(model string) []string {
	args := []string{
		"exec", "--json", "--sandbox", "read-only", "--skip-git-repo-check", "--ephemeral",
		"--ignore-user-config", "--ignore-rules",
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	return append(args, "-")
}

// toolFreePrompt is the conversation as text for a tool that takes one prompt, with
// what the assistant is told about itself first and a plain request, which is all
// it is, not to use the machine.
func toolFreePrompt(system string, messages []assistant.Message) string {
	var b strings.Builder
	if system != "" {
		b.WriteString(system)
		b.WriteString("\n\n")
	}
	b.WriteString(claudePrompt(messages))
	b.WriteString("\nAnswer in plain text from what you know. Do not run commands, read files or search the web.\n")
	return b.String()
}

// Chat implements assistant.Provider.
func (p *Codex) Chat(ctx context.Context, req assistant.Request) (assistant.Stream, error) {
	bin, err := codexBinary(p.cfg.Command)
	if err != nil {
		return nil, err
	}
	model := req.Model
	if model == "" {
		model = p.cfg.Model
	}
	parse := &codexParse{}
	return startCLI(ctx, cliCommand{
		tool:   "Codex",
		bin:    bin,
		args:   codexArgs(model),
		env:    codexEnv(os.Environ()),
		stdin:  toolFreePrompt(req.System, req.Messages),
		reader: readLines(parse),
		exit:   parse.exit,
	})
}

// codexParse reads the lines of `codex exec --json`, one at a time. It is a value
// so a test can feed it lines.
type codexParse struct {
	sawText bool
	// failed is the last error Codex reported without ending the turn, kept for a
	// run that then ends badly with nothing else to say.
	failed string
}

type codexLine struct {
	Type string `json:"type"`
	Item struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error   *struct{ Message string } `json:"error"`
	Message string                    `json:"message"`
}

func (c *codexParse) answered() bool { return c.sawText }

// line interprets one line. Only what the agent says as its answer is kept. The
// commands it ran, the files it changed, its tool calls, searches, plans and
// reasoning are ignored on purpose: Eli takes an answer and does nothing on the
// strength of what a tool did. A line it does not understand is nothing, because a
// newer Codex may print more than this knows.
func (c *codexParse) line(raw []byte) []assistant.Event {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var l codexLine
	if json.Unmarshal(raw, &l) != nil {
		return nil
	}
	switch l.Type {
	case "item.completed":
		if l.Item.Type != "agent_message" || l.Item.Text == "" {
			return nil
		}
		text := l.Item.Text
		if c.sawText {
			// An answer that is said in more than one message reads as paragraphs.
			text = "\n\n" + text
		}
		c.sawText = true
		return []assistant.Event{{Delta: text}}
	case "turn.completed":
		if !c.sawText {
			return []assistant.Event{{Err: errors.New("Codex finished the turn without an answer")}}
		}
		var out []assistant.Event
		if l.Usage != nil {
			out = append(out, assistant.Event{Usage: &assistant.Usage{InputTokens: l.Usage.InputTokens, OutputTokens: l.Usage.OutputTokens, Reported: true}})
		}
		return append(out, assistant.Event{Done: true})
	case "turn.failed":
		said := ""
		if l.Error != nil {
			said = l.Error.Message
		}
		if said == "" {
			said = c.failed
		}
		return []assistant.Event{{Err: codexFailure(said)}}
	case "error":
		// Codex reports some trouble it then recovers from, such as a dropped
		// connection it retries, so this alone does not end the answer.
		c.failed = l.Message
	}
	return nil
}

// codexFailure is what Codex said when it could not answer, for the person.
func codexFailure(said string) error {
	said = strings.TrimSpace(said)
	lower := strings.ToLower(said)
	switch {
	case strings.Contains(lower, "codex login"), strings.Contains(lower, "not logged in"), strings.Contains(lower, "unauthorized"),
		strings.Contains(lower, "401"), strings.Contains(lower, "log in again"), strings.Contains(lower, "sign in again"):
		return errCodexNotSignedIn
	case said == "":
		return errors.New("the answer failed in Codex")
	}
	return fmt.Errorf("the answer failed in Codex: %s", clip(said, 300))
}

// exit is a run that ended badly with nothing said as an answer. Codex's standard
// error is its progress, which says little about why, so what it reported in the
// stream comes first.
func (c *codexParse) exit(err error, stderr string) error {
	lower := strings.ToLower(stderr)
	switch {
	case c.failed != "":
		return codexFailure(c.failed)
	case strings.Contains(lower, "unexpected argument"), strings.Contains(lower, "unknown option"), strings.Contains(lower, "unrecognized"):
		return errors.New("this version of Codex does not accept the options Zoomies needs to run it safely; update it and test again")
	case strings.Contains(lower, "codex login"), strings.Contains(lower, "not logged in"):
		return errCodexNotSignedIn
	}
	return fmt.Errorf("the run stopped in Codex without an answer (%v)", err)
}

// Check implements assistant.Provider. It spends no usage: it asks Codex which
// version it is and whether anyone is signed in, and says so if it is an API key.
func (p *Codex) Check(ctx context.Context) (assistant.CheckResult, error) {
	start := time.Now()
	bin, err := codexBinary(p.cfg.Command)
	if err != nil {
		return assistant.CheckResult{}, err
	}
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = codexEnv(os.Environ())
		cmd.Dir = os.TempDir()
		return cmd.CombinedOutput()
	}
	ver, err := run("--version")
	if err != nil {
		return assistant.CheckResult{}, fmt.Errorf("no version from Codex: %w", err)
	}
	status, err := run("login", "status")
	if err != nil {
		return assistant.CheckResult{}, errCodexNotSignedIn
	}
	// The text of the status is not shown: for an API key it can carry part of the key.
	if strings.Contains(strings.ToLower(string(status)), "api key") {
		return assistant.CheckResult{}, errCodexAPIKey
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(ver)), "\n")
	return assistant.CheckResult{Model: first, Latency: time.Since(start), UsageReported: false}, nil
}
