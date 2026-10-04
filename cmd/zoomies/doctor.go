package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/installer"
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
	if *interactive {
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
		r = engine.Run(ctx, t)
		printDoctor(e.out, r, false)
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
	warnings, errs, skipped := r.Counts()
	fmt.Fprintf(w, "Host health: %d warning(s), %d error(s), %d skipped check(s)\n", warnings, errs, skipped)
	fmt.Fprintf(w, "%s · %s · checked %s\n", r.OS, r.Distro, r.CheckedAt.Format(time.RFC3339))
	if r.RebootPending {
		fmt.Fprintln(w, "Reboot pending. Drain this host before a planned reboot; Zoomies will not reboot it.")
	}
	if remote {
		fmt.Fprintln(w, "Latest agent report; tuning must be run locally on that host.")
	}
	if time.Since(r.CheckedAt) > 10*time.Minute {
		fmt.Fprintln(w, "This report is older than 10 minutes; it may no longer describe the host.")
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tCHECK (ID)\tCURRENT\tRECOMMENDED\tWHY / DETAILS")
	for _, x := range r.Results {
		detail := x.Rationale
		if x.Reason != "" {
			detail += " " + x.Reason
		}
		fmt.Fprintf(tw, "%s\t%s (%s)\t%s\t%s\t%s\n", strings.ToUpper(string(x.Status)), plainCell(x.Title), x.ID, plainCell(x.Current), plainCell(x.Recommended), plainCell(detail))
	}
	_ = tw.Flush()
	if warnings > 0 {
		fmt.Fprintln(w, "\nReview safe fixes with: sudo zoomies tune (or zoomies doctor --interactive).")
	}
	if skipped > 0 {
		fmt.Fprintln(w, "Skipped checks explain which host access or platform support is missing.")
	}
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
