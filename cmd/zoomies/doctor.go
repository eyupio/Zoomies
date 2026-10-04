package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/installer"
	"golang.org/x/term"
)

// doctorExit preserves doctor's documented 0/1/2 report outcome without
// changing exit codes for existing commands or printing a second error.
type doctorExit int

func (d doctorExit) Error() string { return "host health report" }
func tierValue(s string) (hosttune.Tier, error) {
	switch hosttune.Tier(s) {
	case hosttune.Safe, hosttune.Aggressive, hosttune.Dedicated:
		return hosttune.Tier(s), nil
	}
	return "", fmt.Errorf("tier must be safe, aggressive or dedicated")
}
func localDoctor(path string) (*hosttune.Engine, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	o := hosttune.LocalOptions(cfg.Agent.WorkDir)
	o.DockerHost = cfg.Agent.DockerHost
	if path == "" {
		if b, err := o.System.ReadFile(filepath.Join(config.SharedDir(), "host-health", "report.json")); err == nil {
			var r hosttune.Report
			if json.Unmarshal(b, &r) == nil && r.WorkDir != "" && !r.Container {
				o.WorkDir = r.WorkDir
			}
		}
	}
	return hosttune.New(o), nil
}
func runDoctor(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies doctor [--json] [--tier safe|aggressive|dedicated] [--host <id>]", "Check host OS settings and explain recommended changes. No host settings are changed.")
	interactive := fs.Bool("interactive", false, "offer individual local fixes after showing the report")
	js := fs.Bool("json", false, "print a machine-readable report")
	tier := fs.String("tier", "safe", "safe, aggressive or dedicated; kernel checks appear in every tier")
	watch := fs.Bool("watch", false, "continuously publish a read-only native host report every minute")
	reportFile := fs.String("report-file", "", "with --watch: observation JSON file, readable by the container")
	work := fs.String("work-dir", "", "host path to runner work directory")
	dockerHost := fs.String("docker-host", "", "Docker socket to inspect on this host")
	host := fs.String("host", "", "read a remote host's latest report through the controller")
	cfg := fs.String("config", "", "local Zoomies configuration file")
	cf := registerClientFlags(fs, false)
	fs.example("zoomies doctor", "zoomies doctor --json", "zoomies doctor --host host_...")
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	t, err := tierValue(*tier)
	if err != nil {
		return usagef("doctor", "%s", err)
	}
	if *watch {
		if *interactive || *host != "" || *js {
			return usagef("doctor", "--watch cannot be combined with --interactive, --host or --json")
		}
		if *reportFile == "" {
			return usagef("doctor", "--watch requires --report-file")
		}
		engine, err := localDoctor(*cfg)
		if err != nil {
			return err
		}
		if *work != "" {
			engine.WorkDir = *work
		}
		if *dockerHost != "" {
			engine.DockerHost = *dockerHost
		}
		if engine.Container {
			return fmt.Errorf("run the host health reporter using the native binary on the host")
		}
		return watchDoctor(ctx, e, engine, t, *reportFile)
	}
	if *interactive && (*js || *host != "") {
		return usagef("doctor", "--interactive requires a local, human-readable report")
	}
	var r hosttune.Report
	if *host != "" {
		client, err := cf.client()
		if err != nil {
			return err
		}
		var h struct {
			Doctor *hosttune.Report `json:"doctor"`
		}
		if _, err = client.get(ctx, "/hosts/"+url.PathEscape(*host), nil, &h); err != nil {
			return err
		}
		if h.Doctor == nil {
			return fmt.Errorf("this host has no doctor report yet; update its agent or run doctor on the host")
		}
		r = *h.Doctor
		r.Results = filterDoctor(r.Results, t)
	} else {
		engine, err := localDoctor(*cfg)
		if err != nil {
			return err
		}
		if *work != "" {
			engine.WorkDir = *work
		}
		if *dockerHost != "" {
			engine.DockerHost = *dockerHost
		}
		r = engine.Run(ctx, t)
	}
	if *js {
		err = json.NewEncoder(e.out).Encode(r)
	} else {
		printDoctor(e.out, r, *host != "")
	}
	if err != nil {
		return err
	}
	// A bare `zoomies doctor` at a terminal offers the fixes it can make;
	// --interactive goes straight to tune, which still asks about each one.
	if *host == "" && !*js && (*interactive || offerTune(e, r)) {
		a := []string{"--config", *cfg}
		if t == hosttune.Dedicated {
			a = append(a, "--dedicated")
		} else {
			a = append(a, "--tier", string(t))
		}
		if err = runTune(ctx, e, a); err != nil {
			return err
		}
		engine, err := localDoctor(*cfg)
		if err != nil {
			return err
		}
		fmt.Fprintln(e.out)
		r = engine.Run(ctx, t)
		printDoctor(e.out, r, false)
	}
	if r.ExitCode() != 0 {
		return doctorExit(r.ExitCode())
	}
	return nil
}

// offerTune asks whether to review the fixes tune can make. It asks only when a
// person is there to answer and there is something to fix; a script or a cron
// job running doctor must never block on a prompt or change the host.
func offerTune(e *env, r hosttune.Report) bool {
	f, ok := e.in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) || !isTerminal(e.out) {
		return false
	}
	n := actionableCount(r)
	if n == 0 {
		return false
	}
	ui := installer.PaletteFor(e.out)
	fmt.Fprintln(e.out)
	return strings.EqualFold(askLine(e.in, e.out, ui.Bold(fmt.Sprintf("Apply %d safe fix(es) with tune? [y/N] ", n))), "y")
}
func filterDoctor(rs []hosttune.Result, t hosttune.Tier) []hosttune.Result {
	out := []hosttune.Result{}
	for _, r := range rs {
		if r.Tier == hosttune.Dedicated && t != hosttune.Dedicated {
			continue
		}
		if r.Tier == hosttune.Aggressive && t == hosttune.Safe {
			continue
		}
		out = append(out, r)
	}
	return out
}
func printDoctor(w io.Writer, r hosttune.Report, remote bool) {
	ui := installer.PaletteFor(w)
	warnings, errs, skipped := r.Counts()
	ui.Title(w, "Host health", fmt.Sprintf("%s · %s · checked %s", r.OS, r.Distro, r.CheckedAt.Format(time.RFC3339)))
	summary := fmt.Sprintf("%d warning(s), %d error(s), %d skipped check(s)", warnings, errs, skipped)
	switch {
	case errs > 0:
		ui.Fail(w, "%s", summary)
	case warnings > 0:
		ui.Warn(w, "%s", summary)
	default:
		ui.Done(w, "%s", summary)
	}
	if r.RebootPending {
		ui.Hint(w, "Reboot pending. Drain this host before a planned reboot; Zoomies will not reboot it.")
	}
	if remote {
		ui.Hint(w, "Latest agent report; tuning must be run locally on that host.")
	}
	if time.Since(r.CheckedAt) > 10*time.Minute {
		ui.Hint(w, "This report is older than 10 minutes; it may no longer describe the host.")
	}
	// Findings come first and in full, because they are what the reader came
	// for; checks that pass are one line each so they do not bury them.
	section := func(label string, keep func(hosttune.Status) bool, detail bool) {
		var rows []hosttune.Result
		for _, x := range r.Results {
			if keep(x.Status) {
				rows = append(rows, x)
			}
		}
		if len(rows) == 0 {
			return
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, ui.Bold(fmt.Sprintf("%s (%d)", label, len(rows))))
		for _, x := range rows {
			printCheck(w, ui, x, detail)
		}
	}
	section("Needs attention", func(s hosttune.Status) bool { return s == hosttune.Warn || s == hosttune.Error }, true)
	section("Skipped", func(s hosttune.Status) bool { return s == hosttune.Skip }, true)
	section("Passing", func(s hosttune.Status) bool {
		return s != hosttune.Warn && s != hosttune.Error && s != hosttune.Skip
	}, false)
	if warnings > 0 {
		fmt.Fprintln(w)
		ui.Hint(w, "Fix safely: sudo zoomies tune")
		ui.Hint(w, "or: sudo zoomies doctor --interactive")
	}
	if skipped > 0 {
		ui.Hint(w, "Skipped checks explain which host access or platform support is missing.")
	}
}

// printCheck is one check: its status and name, then (for findings) what it
// found, what would be better, and why it matters, each on a labelled line so a
// long rationale wraps under its own label rather than across a table.
func printCheck(w io.Writer, ui installer.Palette, x hosttune.Result, detail bool) {
	title := fmt.Sprintf("%s  %s", ui.Bold(plainCell(x.Title)), ui.Dim("("+x.ID+")"))
	switch x.Status {
	case hosttune.Error:
		ui.Fail(w, "%s", title)
	case hosttune.Warn:
		if x.Actionable && !x.Optional {
			title += "  " + ui.Green("[fixable]")
		}
		ui.Warn(w, "%s", title)
	case hosttune.Skip:
		ui.Doing(w, "%s", title)
	default:
		ui.Done(w, "%s  %s", title, ui.Dim(plainCell(x.Current)))
		return
	}
	if !detail {
		return
	}
	// Wrapped to the terminal with a hanging indent: a phone is 40 columns or
	// fewer, and an unwrapped line there is cut mid-word or scrolls sideways.
	width := termWidth(w) - 5
	field := func(label, text string) {
		if text = strings.TrimSpace(plainCell(text)); text == "" {
			return
		}
		for i, line := range wrapText(label+": "+text, width) {
			if i > 0 {
				line = "  " + line
			}
			fmt.Fprintf(w, "     %s\n", line)
		}
	}
	field("current", x.Current)
	field("better", x.Recommended)
	field("why", x.Rationale)
	field("detail", x.Reason)
}

// termWidth is the columns available, from the terminal itself or $COLUMNS,
// never below 30 so wrapping stays sane and never above 100 so it stays readable.
func termWidth(w io.Writer) int {
	n := 80
	if f, ok := w.(*os.File); ok {
		if c, _, err := term.GetSize(int(f.Fd())); err == nil && c > 0 {
			n = c
		}
	} else if c, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && c > 0 {
		n = c
	}
	return min(max(n, 30), 100)
}

// wrapText breaks s at spaces to fit width; a word longer than the width gets a line to itself.
func wrapText(s string, width int) []string {
	var lines []string
	cur := ""
	for _, word := range strings.Fields(s) {
		if cur != "" && len(cur)+1+len(word) > width {
			lines = append(lines, cur)
			cur = word
			continue
		}
		if cur != "" {
			cur += " "
		}
		cur += word
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func plainCell(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, s)
}

func watchDoctor(ctx context.Context, e *env, engine *hosttune.Engine, t hosttune.Tier, path string) error {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		r := engine.Run(ctx, t)
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if err = engine.System.WriteFile(path, append(b, '\n'), 0644); err != nil {
			return err
		}
		w, n, s := r.Counts()
		fmt.Fprintf(e.out, "Host health report updated: %d warning(s), %d error(s), %d skipped check(s)\n", w, n, s)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func afterHostSetup(ctx context.Context, e *env, fresh, tune, noTune, interactive bool, work string) error {
	engine, err := localDoctor("")
	if err != nil {
		return err
	}
	if work != "" {
		engine.WorkDir = work
	}
	before, _ := hosttune.ReadReport(engine.System, engine.WorkDir)
	r := engine.Run(ctx, hosttune.Safe)
	printDoctor(e.out, r, false)
	if !fresh {
		if hosttune.NewWarnings(before, r) > 0 {
			fmt.Fprintln(e.out, "New host health warnings; review them with zoomies tune.")
		}
		return nil
	}
	fmt.Fprintln(e.out, "Aggressive and dedicated-host tuning are available separately with zoomies tune.")
	approved := tune && !noTune
	if !tune && !noTune && interactive {
		fmt.Fprint(e.out, "Apply recommended safe tuning? [y/N] ")
		scanner := bufio.NewScanner(e.in)
		if scanner.Scan() {
			approved = strings.EqualFold(strings.TrimSpace(scanner.Text()), "y")
		}
	}
	if approved {
		return engine.Tune(ctx, hosttune.TuneOptions{Tier: hosttune.Safe, Yes: true, Out: e.out, In: e.in, Actor: "installer"})
	}
	return nil
}

// briefRows is how many findings the post-upgrade summary lists before it says
// how many more there are; the full report is one keystroke away.
const briefRows = 6

// upgradeDoctor is the host-health step at the end of an upgrade. It says in a
// few lines whether the host needs attention and, when somebody is there to
// answer, offers the two things they can do about it. Upgrade never tunes on
// its own, including with --yes: tuning changes the OS, which an approval to
// upgrade the software does not cover, so it only ever runs from the menu.
func upgradeDoctor(ctx context.Context, e *env, cfg *config.Config, interactive bool) {
	options := hosttune.LocalOptions(cfg.Agent.WorkDir)
	options.DockerHost = cfg.Agent.DockerHost
	if b, err := options.System.ReadFile(filepath.Join(config.SharedDir(), "host-health", "report.json")); err == nil {
		var r hosttune.Report
		if json.Unmarshal(b, &r) == nil && r.WorkDir != "" && !r.Container {
			options.WorkDir = r.WorkDir
		}
	}
	engine := hosttune.New(options)
	before, _ := hosttune.ReadReport(engine.System, engine.WorkDir)
	r := engine.Run(ctx, hosttune.Safe)
	printDoctorBrief(e.out, r, hosttune.NewWarnings(before, r))
	warnings, errs, _ := r.Counts()
	if warnings+errs == 0 {
		return
	}
	ui := installer.PaletteFor(e.out)
	if !interactive {
		ui.Hint(e.out, "Full report: zoomies doctor    Safe fixes: sudo zoomies tune")
		return
	}
	for {
		fixable := actionableCount(r)
		fmt.Fprintln(e.out)
		if fixable > 0 {
			fmt.Fprintf(e.out, "  %s  review and apply %d safe fix(es), one at a time, with a way back\n", ui.Accent("[t]"), fixable)
		}
		fmt.Fprintf(e.out, "  %s  show the full doctor report\n", ui.Accent("[d]"))
		fmt.Fprintf(e.out, "  %s  finish\n", ui.Accent("[Enter]"))
		switch strings.ToLower(askLine(e.in, e.out, ui.Bold("What next? "))) {
		case "d":
			fmt.Fprintln(e.out)
			printDoctor(e.out, r, false)
		case "t":
			if fixable == 0 {
				continue
			}
			if err := runTune(ctx, e, nil); err != nil {
				fmt.Fprintln(e.err, "Tuning stopped:", err)
			}
			r = engine.Run(ctx, hosttune.Safe)
			fmt.Fprintln(e.out)
			printDoctorBrief(e.out, r, 0)
			if w, n, _ := r.Counts(); w+n == 0 {
				return
			}
		default:
			return
		}
	}
}

func actionableCount(r hosttune.Report) (n int) {
	for _, x := range r.Results {
		if x.Actionable && !x.Optional {
			n++
		}
	}
	return n
}

// printDoctorBrief is the report as a person skims it: one line when all is
// well, and otherwise only the checks that are not, each on a single line with
// what to change. The table printDoctor prints is for reading on purpose.
func printDoctorBrief(w io.Writer, r hosttune.Report, fresh int) {
	ui := installer.PaletteFor(w)
	warnings, errs, skipped := r.Counts()
	skippedNote := func() {
		if skipped > 0 {
			ui.Hint(w, "%d check(s) could not run here; zoomies doctor says why.", skipped)
		}
	}
	if warnings+errs == 0 {
		ui.Done(w, "All checks pass")
		skippedNote()
		return
	}
	head := fmt.Sprintf("%d to look at", warnings+errs)
	if fresh > 0 {
		head += fmt.Sprintf(", %d new since the last report", fresh)
	}
	if errs > 0 {
		ui.Fail(w, "%s", head)
	} else {
		ui.Warn(w, "%s", head)
	}
	shown := 0
	for _, x := range r.Results {
		if x.Status != hosttune.Warn && x.Status != hosttune.Error {
			continue
		}
		if shown == briefRows {
			fmt.Fprintf(w, "     %s\n", ui.Dim(fmt.Sprintf("...and %d more", warnings+errs-shown)))
			break
		}
		shown++
		line := fmt.Sprintf("     %s  %s", ui.Bold(plainCell(x.Title)), ui.Dim(plainCell(x.Current)))
		if x.Recommended != "" {
			line += ui.Dim(" -> ") + plainCell(x.Recommended)
		}
		if x.Actionable && !x.Optional {
			line += "  " + ui.Green("[fixable]")
		}
		fmt.Fprintln(w, line)
	}
	if r.RebootPending {
		ui.Hint(w, "A reboot is pending. Drain this host first; Zoomies will not reboot it.")
	}
	skippedNote()
}

// askLine reads one line a byte at a time, so nothing beyond it is consumed:
// the tune that may follow reads the same input, and a buffered reader here
// would swallow its first answer. End of input is an empty answer.
func askLine(in io.Reader, out io.Writer, prompt string) string {
	fmt.Fprint(out, prompt)
	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := in.Read(buf)
		if n == 1 {
			if buf[0] == '\n' {
				break
			}
			line = append(line, buf[0])
		}
		if err != nil {
			fmt.Fprintln(out)
			break
		}
	}
	return strings.TrimSpace(string(line))
}
