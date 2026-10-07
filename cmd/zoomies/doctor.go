package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	fs := newFlagSet(e, "zoomies doctor [--verbose] [--json] [--tier safe|aggressive|dedicated] [--host <id>]", "Check host OS settings and explain recommended changes. No host settings are changed.")
	verbose := fs.Bool("verbose", false, "show all checks, values and explanations")
	interactive := fs.Bool("interactive", false, "offer individual local fixes after showing the report")
	force := fs.Bool("force", false, "with --interactive: where an approved fix needs Docker restarted, take this host out of service, wait for running jobs to finish, restart Docker and bring the host back (see zoomies tune --force)")
	wait := fs.Duration("wait", hosttune.DefaultMaintenanceWait, "with --force, how long running jobs are given to finish")
	killRunning := fs.Bool("kill-running", false, "with --force, stop containers still running when --wait is over, which ends their jobs")
	background := fs.Bool("background", false, "with --force: start a background task that keeps the host in service and keeps trying until it is quiet, then restarts Docker (see zoomies tune --background)")
	giveUp := fs.Duration("give-up-after", hosttune.DefaultGiveUp, "with --background, how long the task keeps looking for a quiet moment")
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
		if *interactive || *host != "" || *js || *verbose {
			return usagef("doctor", "--watch cannot be combined with --interactive, --host, --json or --verbose")
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
	if (*force || *killRunning || *background || fs.changed("wait") || fs.changed("give-up-after")) && !*interactive {
		return usagef("doctor", "--force, --wait, --kill-running, --background and --give-up-after change the host, so they apply only with --interactive")
	}
	if (fs.changed("wait") || *killRunning || *background) && !*force {
		return usagef("doctor", "--wait, --kill-running and --background only apply to a maintenance restart; add --force")
	}
	if *background && (*killRunning || fs.changed("wait")) {
		return usagef("doctor", "a background restart waits for a quiet host and never stops a job, so --kill-running and --wait do not apply to it")
	}
	if fs.changed("give-up-after") && !*background {
		return usagef("doctor", "--give-up-after applies to --background")
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
			var refused *apiError
			if errors.As(err, &refused) && refused.status == http.StatusUnauthorized {
				// The one thing this command needs that a person cannot be expected to
				// have to hand is a token, and the host's page makes one for exactly
				// this: read-only, for hosts, over in a quarter of an hour.
				return fmt.Errorf("%w\n  the host's page in the web UI has this command with a token that lasts 15 minutes already in it", err)
			}
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
	} else if *verbose {
		printDoctor(e.out, r, *host != "")
	} else {
		installer.PaletteFor(e.out).Title(e.out, "Host health", strings.TrimSpace(r.OS+" "+r.Distro))
		printDoctorBrief(e.out, r, 0)
		details := "zoomies doctor --verbose"
		if *host != "" {
			details = "zoomies doctor --host <host-id> --verbose"
		}
		installer.PaletteFor(e.out).Hint(e.out, "Details: %s", details)
		if time.Since(r.CheckedAt) > 10*time.Minute {
			installer.PaletteFor(e.out).Hint(e.out, "Report older than 10 minutes; observations may have changed.")
		}
		if *host != "" {
			installer.PaletteFor(e.out).Hint(e.out, "Latest agent report; run tuning locally on that host.")
		}
	}
	if err != nil {
		return err
	}
	// Only an explicit interactive request enters tuning. A routine health
	// check finishes without another menu or approval conversation.
	if *host == "" && !*js && *interactive {
		a := []string{"--config", *cfg, "--work-dir", *work, "--docker-host", *dockerHost}
		if t == hosttune.Dedicated {
			a = append(a, "--dedicated")
		} else {
			a = append(a, "--tier", string(t))
		}
		if *force {
			a = append(a, "--force")
			switch {
			case *background:
				a = append(a, "--background", "--give-up-after", giveUp.String())
			default:
				a = append(a, "--wait", wait.String())
			}
			if *killRunning {
				a = append(a, "--kill-running")
			}
		}
		if err = runTune(ctx, e, a); err != nil {
			return err
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
		fmt.Fprintln(e.out)
		r = engine.Run(ctx, t)
		printDoctorBrief(e.out, r, 0)
	}
	if r.ExitCode() != 0 {
		return doctorExit(r.ExitCode())
	}
	return nil
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
	if n := acceptedCount(r); n > 0 {
		summary += fmt.Sprintf(", %d accepted", n)
	}
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
	section := func(label string, keep func(hosttune.Result) bool, detail bool) {
		var rows []hosttune.Result
		for _, x := range r.Results {
			if keep(x) {
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
	// An accepted warning is a decision an operator made on the controller, not a
	// pass, so it gets a section of its own: the sections then add up to the
	// summary line, and nobody has to wonder where a warning went.
	section("Needs attention", func(x hosttune.Result) bool {
		return !x.Accepting() && (x.Status == hosttune.Warn || x.Status == hosttune.Error)
	}, true)
	section("Accepted", hosttune.Result.Accepting, true)
	section("Skipped", func(x hosttune.Result) bool { return x.Status == hosttune.Skip }, true)
	section("Passing", func(x hosttune.Result) bool {
		return x.Status != hosttune.Warn && x.Status != hosttune.Error && x.Status != hosttune.Skip
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
		if x.Accepting() {
			title += "  " + ui.Dim("[accepted]")
		} else if x.Actionable && !x.Optional {
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
	if a := x.Accepted; a != nil {
		// Person-written, so it goes through plainCell like the host's own text.
		field("accepted", fmt.Sprintf("by %s on %s, until %s: %s", a.By, a.At.Format("2 Jan 2006"), a.ExpiresAt.Format("2 Jan 2006"), a.Reason))
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

// Upgrade ends with observations, never a tuning menu. Software approval and
// permission to change the OS remain separate even at an interactive terminal.
func upgradeDoctor(ctx context.Context, e *env, cfg *config.Config) {
	options := hosttune.LocalOptions(cfg.Agent.WorkDir)
	options.DockerHost = cfg.Agent.DockerHost
	if b, err := options.System.ReadFile(filepath.Join(config.SharedDir(), "host-health", "report.json")); err == nil {
		var r hosttune.Report
		if json.Unmarshal(b, &r) == nil && r.WorkDir != "" && !r.Container {
			options.WorkDir = r.WorkDir
		}
	}
	engine := hosttune.New(options)
	r := engine.Run(ctx, hosttune.Safe)
	printUpgradeHealth(e.out, r)
}

func printUpgradeHealth(w io.Writer, r hosttune.Report) {
	ui := installer.PaletteFor(w)
	warnings, errs, skipped := r.Counts()
	switch {
	case warnings+errs > 0:
		message := fmt.Sprintf("Host health: %s, %s", countOf(warnings, "warning"), countOf(errs, "error"))
		if errs > 0 {
			ui.Fail(w, "%s", message)
		} else {
			ui.Warn(w, "%s", message)
		}
		ui.Hint(w, "Review: zoomies doctor")
	case skipped > 0:
		ui.Warn(w, "Host health: %d checks unavailable", skipped)
		ui.Hint(w, "Review: zoomies doctor")
	default:
		ui.Done(w, "Host health checks passed")
	}
	if r.RebootPending {
		ui.Hint(w, "Reboot pending; drain this host before a planned reboot.")
	}
}

const briefRows = 3

func actionableCount(r hosttune.Report) (n int) {
	for _, x := range r.Results {
		if x.Status == hosttune.Warn && x.Actionable && !x.Optional && !x.Accepting() {
			n++
		}
	}
	return n
}

// printDoctorBrief is the report as a person skims it: one line when all is
// well, and otherwise only the checks that are not, each on a single line with
// what to change. The verbose report keeps the complete explanations.
func printDoctorBrief(w io.Writer, r hosttune.Report, fresh int) {
	ui := installer.PaletteFor(w)
	warnings, errs, skipped := r.Counts()
	skippedNote := func() {
		if skipped > 0 {
			ui.Hint(w, "%d checks unavailable; see --verbose for details.", skipped)
		}
	}
	acceptedNote := func() {
		if n := acceptedCount(r); n > 0 {
			ui.Hint(w, "%s accepted by an operator; see --verbose for who and why.", countOf(n, "check"))
		}
	}
	if warnings+errs == 0 {
		ui.Done(w, "No warnings or errors")
		acceptedNote()
		skippedNote()
		return
	}
	head := fmt.Sprintf("%s, %s", countOf(warnings, "warning"), countOf(errs, "error"))
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
		if (x.Status != hosttune.Warn && x.Status != hosttune.Error) || x.Accepting() {
			continue
		}
		if shown == briefRows {
			fmt.Fprintf(w, "     %s\n", ui.Dim(fmt.Sprintf("...and %d more", warnings+errs-shown)))
			break
		}
		shown++
		text := plainCell(x.Title)
		if x.Recommended != "" {
			text += ": " + plainCell(x.Recommended)
		}
		if x.Status == hosttune.Warn && x.Actionable && !x.Optional {
			text += "  [fixable]"
		}
		for _, line := range wrapText(text, termWidth(w)-5) {
			fmt.Fprintf(w, "     %s\n", line)
		}
	}
	if r.RebootPending {
		ui.Hint(w, "A reboot is pending. Drain this host first; Zoomies will not reboot it.")
	}
	acceptedNote()
	skippedNote()
	if actionableCount(r) > 0 {
		ui.Hint(w, "Review fixes: sudo zoomies tune")
	}
}

// acceptedCount is how many warnings an operator has accepted. Only a report
// fetched from the controller can have any; a local run never does.
func acceptedCount(r hosttune.Report) int {
	n := 0
	for _, x := range r.Results {
		if x.Accepting() {
			n++
		}
	}
	return n
}
