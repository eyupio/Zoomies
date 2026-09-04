package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// flagHelp is returned when the operator asked for help rather than made a
// mistake. It exits 0: `zoomies pools list --help | less` should not look like
// a failure to a shell.
var flagHelp = errors.New("help requested")

// flagSet is a flag.FlagSet with the usage text this CLI prints. Every command
// builds one; there are no global flags, so `zoomies runners list --help`
// describes exactly the flags that command accepts and no others.
type flagSet struct {
	*flag.FlagSet
	// usageLine is the synopsis, e.g. "zoomies runners drain <runner-id>".
	usageLine string
	// summary is the one-line description printed above the flags.
	summary string
	// examples are printed after the flags, and are worth more than the flags.
	examples []string
	out      io.Writer
}

// newFlagSet builds a flag set that writes to stderr and prints a synopsis, a
// summary and the flags when asked.
func newFlagSet(e *env, usageLine, summary string) *flagSet {
	fs := &flagSet{
		FlagSet:   flag.NewFlagSet(commandName(usageLine), flag.ContinueOnError),
		usageLine: usageLine,
		summary:   summary,
		out:       e.err,
	}
	fs.SetOutput(e.err)
	fs.Usage = func() { fs.printUsage(fs.out) }
	return fs
}

// commandName extracts "pools list" from "zoomies pools list [--output ...]".
//
// It is derived from the usage line rather than passed separately so that the
// name in an error message and the name in the synopsis cannot drift: there is
// one string, and it is the one the operator was shown.
func commandName(usageLine string) string {
	var words []string
	for _, w := range strings.Fields(usageLine) {
		if w == "zoomies" {
			continue
		}
		if strings.HasPrefix(w, "-") || strings.HasPrefix(w, "<") || strings.HasPrefix(w, "[") {
			break
		}
		words = append(words, w)
	}
	return strings.Join(words, " ")
}

// example records a line worth copying. Examples go after the flags because
// that is where an operator's eye lands last and stays.
func (fs *flagSet) example(lines ...string) { fs.examples = append(fs.examples, lines...) }

func (fs *flagSet) printUsage(w io.Writer) {
	if fs.summary != "" {
		fmt.Fprintf(w, "%s\n\n", fs.summary)
	}
	fmt.Fprintf(w, "Usage:\n  %s\n", fs.usageLine)

	var flags []*flag.Flag
	fs.VisitAll(func(f *flag.Flag) { flags = append(flags, f) })
	if len(flags) > 0 {
		sort.Slice(flags, func(i, j int) bool { return flags[i].Name < flags[j].Name })
		fmt.Fprintln(w, "\nFlags:")
		for _, f := range flags {
			name := "--" + f.Name
			if f.DefValue != "" && f.DefValue != "false" {
				name += "=" + f.DefValue
			}
			fmt.Fprintf(w, "  %-26s %s\n", name, f.Usage)
		}
	}
	if len(fs.examples) > 0 {
		fmt.Fprintln(w, "\nExamples:")
		for _, ex := range fs.examples {
			fmt.Fprintf(w, "  %s\n", ex)
		}
	}
}

// parse parses args, translating a help request into flagHelp and any other
// failure into a usage error so that both exit with the right code.
//
// Arguments are permuted first. The standard flag package stops parsing at the
// first non-flag word, which would make `zoomies runners drain run_k3f9 --url
// https://...` treat the URL as a second runner ID -- a footgun with real
// consequences on a command that acts on runners.
func (fs *flagSet) parse(args []string) error {
	// -h and --help are handled by flag itself, but it writes the default
	// usage to the output we set; ours is better, so print it and stop.
	if err := fs.FlagSet.Parse(fs.permute(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// flag has already called Usage for us.
			return flagHelp
		}
		return &usageError{cmd: fs.Name(), err: err}
	}
	return nil
}

// permute moves flags ahead of positional arguments, leaving the order within
// each group alone.
//
// A flag that takes a value carries the following word with it. Whether a flag
// takes one is asked of the flag set itself, so a boolean stays boolean and an
// unknown flag is left where it is for flag.Parse to complain about by name.
func (fs *flagSet) permute(args []string) []string {
	flags := make([]string, 0, len(args))
	positional := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			// Everything after it is positional by definition.
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)

		name := strings.TrimLeft(arg, "-")
		if before, _, found := strings.Cut(name, "="); found {
			// --flag=value carries its own value.
			name = before
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue // unknown; flag.Parse will name it
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			continue // --verbose takes no value
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	if len(positional) == 0 {
		return flags
	}
	// The separator matters: without it a positional that begins with a dash
	// -- a runner named "-x", or an argument after an explicit "--" -- would be
	// re-read as a flag once it had been moved to the end.
	return append(append(flags, "--"), positional...)
}

// noMoreArgs refuses trailing arguments rather than ignoring them, because a
// silently dropped argument is how somebody drains the wrong runner.
func (fs *flagSet) noMoreArgs() error {
	if fs.NArg() > 0 {
		return usagef(fs.Name(), "unexpected argument %q", fs.Arg(0))
	}
	return nil
}

// oneArg requires exactly one positional argument and names it in the error.
func (fs *flagSet) oneArg(what string) (string, error) {
	switch fs.NArg() {
	case 0:
		return "", usagef(fs.Name(), "needs %s", what)
	case 1:
		return fs.Arg(0), nil
	default:
		return "", usagef(fs.Name(), "takes one %s, but %d were given", what, fs.NArg())
	}
}

// atLeastOneArg requires one or more positional arguments.
func (fs *flagSet) atLeastOneArg(what string) ([]string, error) {
	if fs.NArg() == 0 {
		return nil, usagef(fs.Name(), "needs at least one %s", what)
	}
	return fs.Args(), nil
}

// changed reports whether a flag was given on the command line, as opposed to
// left at its default. Partial updates depend on the difference: PATCHing a
// pool must send the fields the operator typed and nothing else.
func (fs *flagSet) changed(name string) bool {
	seen := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			seen = true
		}
	})
	return seen
}

// ---------------------------------------------------------------------------
// Flag value types
// ---------------------------------------------------------------------------

// listValue collects a repeatable, comma-separated flag: --state busy --state
// idle and --state busy,idle mean the same thing, because both are what people
// type.
type listValue []string

func (l *listValue) String() string {
	if l == nil {
		return ""
	}
	return strings.Join(*l, ",")
}

func (l *listValue) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*l = append(*l, part)
		}
	}
	return nil
}

// kvValue collects repeatable key=value pairs: --label arch=arm64 --label
// tier=fast, or one flag with both separated by a comma.
type kvValue map[string]string

func (m kvValue) String() string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}

func (m kvValue) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, val, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return fmt.Errorf("%q is not key=value, for example arch=arm64", part)
		}
		m[strings.TrimSpace(k)] = strings.TrimSpace(val)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Paging
// ---------------------------------------------------------------------------

// pageFlags are the four query parameters every list route accepts. They are
// declared in one place so that --limit means the same thing on runners, jobs
// and the audit log, and so that a new list command cannot forget one.
type pageFlags struct {
	limit  *int
	offset *int
	sort   *string
	order  *string
}

func registerPageFlags(fs *flagSet, defaultLimit int) *pageFlags {
	return &pageFlags{
		limit:  fs.Int("limit", defaultLimit, "rows to return (max 500)"),
		offset: fs.Int("offset", 0, "rows to skip, for paging"),
		sort:   fs.String("sort", "", "column to sort by; an unknown one falls back to the default"),
		order:  fs.String("order", "", "asc or desc"),
	}
}

// apply adds whichever of them the operator set. Sending an empty sort would
// be sending a value, and the server would have to guess what it meant.
func (pf *pageFlags) apply(q url.Values) {
	if pf.limit != nil && *pf.limit > 0 {
		q.Set("limit", strconv.Itoa(*pf.limit))
	}
	if pf.offset != nil && *pf.offset > 0 {
		q.Set("offset", strconv.Itoa(*pf.offset))
	}
	if pf.sort != nil && *pf.sort != "" {
		q.Set("sort", *pf.sort)
	}
	if pf.order != nil && *pf.order != "" {
		q.Set("order", *pf.order)
	}
}

// addList adds a repeatable query parameter the OpenAPI document declares as
// style: form, explode: true -- that is, one key repeated, never a joined
// string, which is what the server parses.
func addList(q url.Values, key string, values []string) {
	for _, v := range values {
		if v != "" {
			q.Add(key, v)
		}
	}
}

// footer says how much of a list was shown, but only when there is more to see.
// A count under every table would be noise; a count when 50 of 812 rows came
// back is the difference between a complete answer and a misleading one.
func (p *printer) footer(shown, total, offset int) {
	if total <= shown+offset {
		return
	}
	fmt.Fprintf(p.out, "\nShowing %d-%d of %d. Use --limit and --offset for the rest.\n", offset+1, offset+shown, total)
}
