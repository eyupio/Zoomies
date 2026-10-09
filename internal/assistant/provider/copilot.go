package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/assistant"
)

// Copilot answers by running GitHub Copilot's command line tool, the person's own
// signed-in copy, on the machine the controller runs on, so that somebody with a
// Copilot plan can use it through Eli without Zoomies holding the login. See cli.go
// for what the tools share.
//
// Copilot is run non-interactively with nobody to ask, and none of the options that
// pre-approve its tools is passed, so a tool that needs permission is not run. That
// is how it behaves and not a switch the tool offers to turn tools off, and the
// documentation says so. The question is an argument of the command, which a person
// with access to this machine's process list can read while it runs.
type Copilot struct {
	cfg Config
}

// NewCopilot returns the adapter. cfg.Command, when set, is the binary to run.
func NewCopilot(cfg Config) *Copilot { return &Copilot{cfg: cfg} }

// ErrCopilotNotFound is Copilot not being on this machine.
var ErrCopilotNotFound = errors.New("the machine the controller runs on has no GitHub Copilot command line tool: " +
	"install it there (npm install -g @github/copilot), sign in as the user the controller runs as with `copilot login`, and test again")

var errCopilotNotSignedIn = errors.New("nobody is signed in to GitHub Copilot on this machine: " +
	"sign in as the user the controller runs as with `copilot login`, and test again")

func copilotBinary(override string) (string, error) {
	return cliBinary(override, "copilot", copilotCandidates(), ErrCopilotNotFound)
}

// copilotCandidates are the places its installers put it, for a service whose PATH
// does not include them. A variable so a test is not at the mercy of a Copilot that
// happens to be installed where it runs.
var copilotCandidates = func() []string {
	var out []string
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, ".local", "bin", "copilot"), filepath.Join(home, ".npm-global", "bin", "copilot"))
	}
	return append(out, "/usr/local/bin/copilot", "/opt/homebrew/bin/copilot")
}

// copilotEnv is Copilot's environment: where it lives and where its sign-in is kept
// (COPILOT_HOME), and no token. COPILOT_GITHUB_TOKEN, GH_TOKEN and GITHUB_TOKEN
// outrank a stored sign-in, so one of them in the controller's environment would be
// used in place of the person's own plan.
func copilotEnv(environ []string) []string { return cliEnv(environ, "COPILOT_HOME") }

// copilotArgs is how Copilot is run: the prompt, only the answer printed, and no
// asking the person anything.
func copilotArgs(prompt, model string) []string {
	args := []string{"-p", prompt, "-s", "--no-ask-user"}
	if model != "" {
		args = append(args, "--model", model)
	}
	return args
}

// Chat implements assistant.Provider.
func (p *Copilot) Chat(ctx context.Context, req assistant.Request) (assistant.Stream, error) {
	bin, err := copilotBinary(p.cfg.Command)
	if err != nil {
		return nil, err
	}
	model := req.Model
	if model == "" {
		model = p.cfg.Model
	}
	return startCLI(ctx, cliCommand{
		tool:   "GitHub Copilot",
		bin:    bin,
		args:   copilotArgs(toolFreePrompt(req.System, req.Messages), model),
		env:    copilotEnv(os.Environ()),
		reader: readText,
		exit:   copilotExit,
	})
}

// copilotExit is a run that ended badly with nothing printed as an answer. Its
// standard error is what it complained of.
func copilotExit(err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "unknown option"), strings.Contains(lower, "unknown argument"), strings.Contains(lower, "unrecognized"):
		return errors.New("this version of the GitHub Copilot command line tool does not accept the options Zoomies needs; update it with `copilot update` and test again")
	case strings.Contains(lower, "copilot login"), strings.Contains(lower, "not logged in"), strings.Contains(lower, "not authenticated"),
		strings.Contains(lower, "authentication required"), strings.Contains(lower, "no authentication"):
		return errCopilotNotSignedIn
	case stderr != "":
		first, _, _ := strings.Cut(stderr, "\n")
		return fmt.Errorf("the run stopped in GitHub Copilot: %s", clip(first, 300))
	}
	return fmt.Errorf("the run stopped in GitHub Copilot without an answer (%v)", err)
}

// Check implements assistant.Provider. Copilot has no command that says whether
// anybody is signed in, so this asks it for one word, which spends one request of
// the plan and is the only way to know. The form says Test sends a short prompt.
func (p *Copilot) Check(ctx context.Context) (assistant.CheckResult, error) {
	start := time.Now()
	bin, err := copilotBinary(p.cfg.Command)
	if err != nil {
		return assistant.CheckResult{}, err
	}
	run := func(timeout time.Duration, args ...string) ([]byte, string, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = copilotEnv(os.Environ())
		cmd.Dir = os.TempDir()
		stderr := &limitedBuffer{limit: 8 << 10}
		cmd.Stderr = stderr
		out, err := cmd.Output()
		return out, stderr.String(), err
	}
	ver, _, err := run(20*time.Second, "version")
	if err != nil {
		return assistant.CheckResult{}, fmt.Errorf("no version from the GitHub Copilot command line tool: %w", err)
	}
	out, stderr, err := run(90*time.Second, copilotArgs("Reply with the single word OK.", p.cfg.Model)...)
	if err != nil {
		return assistant.CheckResult{}, copilotExit(err, stderr)
	}
	if strings.TrimSpace(string(out)) == "" {
		return assistant.CheckResult{}, errors.New("GitHub Copilot answered with nothing; sign in with `copilot login` as the user the controller runs as, and test again")
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(ver)), "\n")
	return assistant.CheckResult{Model: first, Latency: time.Since(start), UsageReported: false}, nil
}
