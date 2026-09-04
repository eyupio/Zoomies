// Command zoomies is the whole product in one binary: the control plane, the
// agent that runs jobs, the installer, and a command-line client for a
// controller that is already running somewhere.
//
// The dispatch table in commands() is the entire surface. Each command owns its
// own flag set rather than sharing a global one, because a flag that only three
// of fifteen commands understand is a flag that misleads the other twelve.
//
// Three exit codes, and they mean different things to a script: 0 for success,
// 1 for "it ran and did not work", 2 for "that is not how you invoke this".
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/eyupio/zoomies/internal/installer"
)

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// Command groups, in the order the help prints them.
const (
	groupRun   = "Running Zoomies"
	groupFleet = "Your fleet (these talk to a controller over its API)"
	groupSetup = "Setting up and looking around"
)

// env is where a command's output goes. Passing it rather than writing to
// os.Stdout directly is what lets the tests assert on a table without a
// terminal, and what keeps machine-readable output on stdout while progress
// and diagnostics go to stderr.
type env struct {
	out io.Writer
	err io.Writer
	in  io.Reader
}

// usageError says the command was invoked wrongly rather than that it failed.
// It exits 2, which is the distinction a wrapper script cares about: a usage
// error will never succeed on a retry.
type usageError struct {
	cmd string
	err error
}

func (e *usageError) Error() string {
	if e.cmd == "" {
		return e.err.Error()
	}
	return e.cmd + ": " + e.err.Error()
}

func (e *usageError) Unwrap() error { return e.err }

func usagef(cmd, format string, a ...any) error {
	return &usageError{cmd: cmd, err: fmt.Errorf(format, a...)}
}

// command is one entry in the dispatch table.
type command struct {
	name  string
	group string
	// brief is one line, printed in the top-level help.
	brief string
	run   func(ctx context.Context, e *env, args []string) error
}

// commands is the dispatch table. It is a function rather than a package
// variable so that the closures below cannot be captured before the flag
// parsing they depend on has run.
func commands() []*command {
	return []*command{
		{"controller", groupRun, "Run the control plane, and an agent alongside it unless told otherwise", runController},
		{"agent", groupRun, "Run a runner host's agent, or join this host to a controller", runAgent},

		{"status", groupFleet, "The Overview, in a terminal", runStatus},
		{"pools", groupFleet, "What runners to make, and how many", runPools},
		{"runners", groupFleet, "The runners that exist right now", runRunners},
		{"jobs", groupFleet, "Job history, queue waits and outcomes", runJobs},
		{"hosts", groupFleet, "Agents, their capacity, and enrolment", runHosts},
		{"installations", groupFleet, "GitHub App installations", runInstallations},
		{"audit", groupFleet, "Who did what", runAudit},
		{"users", groupFleet, "User accounts", runUsers},
		{"tokens", groupFleet, "API tokens", runTokens},

		{"init", groupSetup, "Set this host up: service, backend, GitHub App, first admin", runInit},
		{"uninstall", groupSetup, "Remove Zoomies from this host", runUninstall},
		{"config", groupSetup, "Check a configuration file, or print the effective one", runConfig},
		{"healthcheck", groupSetup, "Probe a controller's /healthz; the container HEALTHCHECK", runHealthcheck},
		{"version", groupSetup, "Print the version", runVersion},
	}
}

func main() {
	// One place handles interruption for every long-running command: the
	// controller, the agent, a followed log tail. Each of them treats a
	// cancelled context as "stop tidily", so ctrl-C does the same thing
	// everywhere.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	e := &env{out: os.Stdout, err: os.Stderr, in: os.Stdin}
	os.Exit(dispatch(ctx, e, os.Args[1:]))
}

// dispatch runs one command and turns its error into an exit code.
func dispatch(ctx context.Context, e *env, args []string) int {
	// No arguments at all is not an error the operator made on purpose; it is
	// somebody finding out what this is. Print the help -- but to stderr, and
	// exit 2, so a script that reached here by accident notices.
	if len(args) == 0 {
		printHelp(e.err)
		return exitUsage
	}

	name := args[0]
	if isHelpWord(name) {
		printHelp(e.out)
		return exitOK
	}

	for _, c := range commands() {
		if c.name != name {
			continue
		}
		err := c.run(ctx, e, args[1:])
		return report(e, c.name, err)
	}

	fmt.Fprintf(e.err, "zoomies: %q is not a zoomies command.\n", name)
	if near := nearest(name); near != "" {
		fmt.Fprintf(e.err, "Did you mean %q?\n", near)
	}
	fmt.Fprintln(e.err, `Run "zoomies help" for the list.`)
	return exitUsage
}

// report turns a command's error into output and an exit code.
func report(e *env, name string, err error) int {
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, errFlagHelp):
		// The command printed its own usage in response to --help.
		return exitOK
	case errors.Is(err, context.Canceled):
		// Ctrl-C on a stream or a long poll. The operator knows; saying
		// "context canceled" to them would be noise.
		return exitOK
	case errors.Is(err, installer.ErrAborted):
		fmt.Fprintln(e.err, "Nothing was changed.")
		return exitError
	}

	var ue *usageError
	if errors.As(err, &ue) {
		fmt.Fprintf(e.err, "zoomies: %s\n", ue.Error())
		help := name
		if ue.cmd != "" {
			help = ue.cmd
		}
		fmt.Fprintf(e.err, "Run \"zoomies %s --help\" for what it takes.\n", help)
		return exitUsage
	}

	fmt.Fprintf(e.err, "zoomies %s: %s\n", name, err)
	return exitError
}

// printHelp writes the top-level help. It is grouped rather than alphabetical
// because the first question is "what am I trying to do", not "what letter does
// it start with".
func printHelp(w io.Writer) {
	fmt.Fprintln(w, "zoomies -- off the lead, on the job.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "A self-hosted GitHub Actions runner fleet controller.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  zoomies <command> [subcommand] [flags]")

	for _, group := range []string{groupRun, groupFleet, groupSetup} {
		fmt.Fprintf(w, "\n%s\n", group)
		for _, c := range commands() {
			if c.group == group {
				fmt.Fprintf(w, "  %-14s %s\n", c.name, c.brief)
			}
		}
	}

	fmt.Fprintln(w, `
Credentials for the fleet commands are looked for in this order:
  --url and --token
  ZOOMIES_URL and ZOOMIES_TOKEN
  `+cliConfigPath()+`

Examples:
  zoomies controller --config /etc/zoomies/zoomies.yaml
  zoomies status
  zoomies runners list --state busy --output json
  zoomies runners logs run_k3f9qz2m --follow
  zoomies agent join https://zoomies.example.com --token zoojoin_...

Run "zoomies <command> --help" for what each one takes.`)
}

// isHelpWord reports whether an argument is a request for help in any of the
// three spellings people actually type.
func isHelpWord(s string) bool {
	switch s {
	case "help", "-h", "--help":
		return true
	}
	return false
}

// nearest suggests a command for a near miss, so a typo costs one line rather
// than a trip to the help.
func nearest(typo string) string {
	typo = strings.ToLower(typo)
	best, bestScore := "", 3 // never suggest something more than two edits away
	for _, c := range commands() {
		if d := editDistance(typo, c.name); d < bestScore {
			best, bestScore = c.name, d
		}
	}
	return best
}

// editDistance is Levenshtein, iterative and small: it is only ever run against
// the fifteen command names above.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		copy(prev, cur)
	}
	return prev[len(b)]
}

// ---------------------------------------------------------------------------
// Subcommand plumbing
// ---------------------------------------------------------------------------

// subcommand is one verb under a grouped command, e.g. `pools list`.
type subcommand struct {
	name  string
	args  string // the positional arguments, for the usage line
	brief string
	run   func(ctx context.Context, e *env, args []string) error
}

// runGroup dispatches the second word of a grouped command and prints a useful
// list when there is not one, or when it is not recognised.
func runGroup(ctx context.Context, e *env, parent, summary string, subs []*subcommand, args []string) error {
	if len(args) == 0 {
		printGroupUsage(e.err, parent, summary, subs)
		return usagef(parent, "needs a subcommand")
	}
	if isHelpWord(args[0]) {
		printGroupUsage(e.out, parent, summary, subs)
		return nil
	}
	for _, s := range subs {
		if s.name == args[0] {
			return s.run(ctx, e, args[1:])
		}
	}
	names := make([]string, 0, len(subs))
	for _, s := range subs {
		names = append(names, s.name)
	}
	sort.Strings(names)
	return usagef(parent, "%q is not a `zoomies %s` subcommand; try one of: %s",
		args[0], parent, strings.Join(names, ", "))
}

func printGroupUsage(w io.Writer, parent, summary string, subs []*subcommand) {
	fmt.Fprintf(w, "%s\n\nUsage:\n  zoomies %s <subcommand> [flags]\n\nSubcommands:\n", summary, parent)
	for _, s := range subs {
		line := s.name
		if s.args != "" {
			line += " " + s.args
		}
		fmt.Fprintf(w, "  %-34s %s\n", line, s.brief)
	}
	fmt.Fprintf(w, "\nRun \"zoomies %s <subcommand> --help\" for the flags each one takes.\n", parent)
}
