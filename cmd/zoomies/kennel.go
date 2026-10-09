package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/kennel"
	"github.com/eyupio/zoomies/internal/kennel/offline"
)

// kennelExitFindings is kennel check's exit code when findings remain at the
// severity asked for: a script can tell "something to fix" from "it broke".
const kennelExitFindings = 4

func runKennel(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "kennel", "Kennel Club: how the repositories this fleet serves measure up against what affects CI and the fleet.", []*subcommand{
		{"overview", "", "The Overview: standings, counts and the repositories to open first", kennelOverview},
		{"repositories", "[--state s] [--severity s] [--code c] [--q text]", "The repositories, narrowed by the list's own filters", kennelRepositories},
		{"repository", "<id> [--prompts]", "One repository: its standing, what could be read, and each finding", kennelRepository},
		{"checks", "", "What is checked, and what is turned off", kennelChecks},
		{"recheck", "<id>", "Ask for a repository to be read again when the budget allows", kennelRecheck},
		{"check", "[path] [--controller owner/name]", "Run the workflow checks over a checkout on this machine; nothing leaves it unless a controller is named", kennelCheck},
	}, args)
}

func kennelOverview(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies kennel overview", "The Kennel Club Overview, in a terminal.")
	cf := registerClientFlags(fs, true)
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	client, p, err := kennelClient(e, cf)
	if err != nil {
		return err
	}
	var v controller.KennelOverviewView
	raw, err := client.get(ctx, "/kennel", nil, &v)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	if !v.Enabled {
		fmt.Fprintln(e.out, "Kennel Club is off. An administrator can turn it on under Settings → Configuration → kennel.enabled.")
		return nil
	}
	fmt.Fprintf(e.out, "%d repositories tracked, %d not tracked.\n", v.Repositories, v.NotTracked)
	p.keyValues([][2]string{
		{"Best in show", strconv.Itoa(v.States.BestInShow)},
		{"Needs attention", strconv.Itoa(v.States.Attention)},
		{"Partly checked", strconv.Itoa(v.States.Partial)},
		{"Pending", strconv.Itoa(v.States.Pending)},
		{"Open findings", fmt.Sprintf("%d errors, %d warnings, %d notes, %d waived", v.Counts.Error, v.Counts.Warning, v.Counts.Info, v.Counts.Waived)},
	})
	if len(v.Attention) > 0 {
		fmt.Fprintln(e.out, "\nNeeds attention:")
		for _, a := range v.Attention {
			fmt.Fprintf(e.out, "  %-16s %-40s %d errors, %d warnings\n", a.ID, a.Name, a.Counts.Error, a.Counts.Warning)
		}
	}
	if len(v.DisabledChecks) > 0 {
		fmt.Fprintf(e.out, "\nTurned off: %s\n", strings.Join(v.DisabledChecks, ", "))
	}
	return nil
}

func kennelRepositories(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies kennel repositories [flags]", "The repositories Kennel Club looks at, narrowed by the list's own filters.")
	filters := map[string]*string{}
	for _, f := range []struct{ name, help string }{
		{"state", "only this standing: pending, partial, attention or best_in_show"},
		{"severity", "only repositories with an open finding of this severity"},
		{"code", "only repositories with an open finding of this check"},
		{"q", "only repositories whose name contains this"},
		{"installation", "only this installation's repositories"},
		{"incomplete", "true keeps the repositories only partly checked"},
		{"waived", "true keeps the repositories with a waived finding, false the ones with none"},
		{"active", "all, or only the repositories the fleet is serving"},
		{"tracked", "true keeps the repositories being tracked, false the ones that are not"},
	} {
		filters[f.name] = fs.String(f.name, "", f.help)
	}
	page := fs.Int("page", 1, "which page")
	perPage := fs.Int("per-page", 50, "how many per page")
	cf := registerClientFlags(fs, true)
	fs.example("zoomies kennel repositories --state attention", "zoomies kennel repositories --code ci.no_timeout --output json")
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	client, p, err := kennelClient(e, cf)
	if err != nil {
		return err
	}
	q := url.Values{"page": {strconv.Itoa(*page)}, "per_page": {strconv.Itoa(*perPage)}}
	for name, v := range filters {
		if *v != "" {
			q.Set(name, *v)
		}
	}
	var pg struct {
		Items []controller.KennelRepositoryView `json:"items"`
		Total int                               `json:"total"`
	}
	raw, err := client.get(ctx, "/kennel/repositories", q, &pg)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	rows := make([][]string, 0, len(pg.Items))
	for _, r := range pg.Items {
		rows = append(rows, []string{r.ID, r.Name, string(r.State), strconv.Itoa(r.Counts.Error), strconv.Itoa(r.Counts.Warning), strconv.Itoa(r.Counts.Info), p.relTimePtr(r.EvaluatedAt)})
	}
	p.table([]string{"ID", "REPOSITORY", "STANDING", "ERRORS", "WARNINGS", "INFO", "EVALUATED"}, rows)
	if pg.Total > len(pg.Items) {
		fmt.Fprintf(e.out, "\nShowing %d of %d repositories; --page %d for more.\n", len(pg.Items), pg.Total, *page+1)
	}
	return nil
}

func kennelRepository(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies kennel repository <id>", "One repository: its standing, what could be read, and each finding.")
	prompts := fs.Bool("prompts", false, "print each finding's prompt for a coding agent")
	cf := registerClientFlags(fs, true)
	fs.example("zoomies kennel repository kcr_k3f9qz2m", "zoomies kennel repository kcr_k3f9qz2m --prompts")
	if err := fs.parse(args); err != nil {
		return err
	}
	id, err := fs.oneArg("a repository ID (kcr_…)")
	if err != nil {
		return err
	}
	client, p, err := kennelClient(e, cf)
	if err != nil {
		return err
	}
	var v kennelRepositoryDoc
	raw, err := client.get(ctx, "/kennel/repositories/"+url.PathEscape(id), nil, &v)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	p.keyValues([][2]string{
		{"Repository", v.Name},
		{"Standing", string(v.State)},
		{"Visibility", v.Visibility},
		{"Evaluated", p.relTimePtr(v.EvaluatedAt)},
		{"Open findings", fmt.Sprintf("%d errors, %d warnings, %d notes, %d waived", v.Counts.Error, v.Counts.Warning, v.Counts.Info, v.Counts.Waived)},
	})
	if len(v.Coverage) > 0 {
		fmt.Fprintln(e.out, "\nWhat could be read:")
		for _, c := range v.Coverage {
			line := fmt.Sprintf("  %-12s %s", c.Source, c.State)
			if c.Reason != "" {
				line += ": " + c.Reason
			}
			fmt.Fprintln(e.out, line)
		}
	}
	paths := make(map[string]string, len(v.Files))
	for _, f := range v.Files {
		paths[f.SHA] = f.Path
	}
	findings := v.Findings
	if !*prompts {
		for i := range findings {
			findings[i].Prompt = ""
		}
	}
	if len(findings) == 0 {
		fmt.Fprintln(e.out, "\nNo open findings.")
	}
	printKennelFindings(e, findings, paths)
	return nil
}

// kennelRepositoryDoc is the repository route's document as the CLI reads it:
// the view, with each finding able to carry where it came from.
type kennelRepositoryDoc struct {
	controller.KennelRepositoryView
	Findings []offline.Finding `json:"findings"`
}

func kennelChecks(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies kennel checks", "What Kennel Club checks, and what is turned off.")
	cf := registerClientFlags(fs, true)
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	client, p, err := kennelClient(e, cf)
	if err != nil {
		return err
	}
	var checks []controller.KennelCheckInfo
	raw, err := client.get(ctx, "/kennel/checks", nil, &checks)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	rows := make([][]string, 0, len(checks))
	for _, c := range checks {
		rows = append(rows, []string{string(c.Code), string(c.Area), string(c.Severity), p.yesNo(c.Disabled, true)})
	}
	p.table([]string{"CODE", "AREA", "SEVERITY", "DISABLED"}, rows)
	return nil
}

func kennelRecheck(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies kennel recheck <id>", "Ask for a repository to be read again when the budget allows.")
	cf := registerClientFlags(fs, false)
	if err := fs.parse(args); err != nil {
		return err
	}
	id, err := fs.oneArg("a repository ID (kcr_…)")
	if err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	if _, err := client.post(ctx, "/kennel/repositories/"+url.PathEscape(id)+"/recheck", nil, nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Recheck asked for; the reads happen when the budget allows. Watch it with: zoomies kennel repository %s\n", id)
	return nil
}

func kennelClient(e *env, cf *clientFlags) (*apiClient, *printer, error) {
	client, err := cf.client()
	if err != nil {
		return nil, nil, err
	}
	p, err := cf.printer(e)
	if err != nil {
		return nil, nil, err
	}
	return client, p, nil
}

// kennelFleetFindings reads the named repository from the controller and
// returns the findings whose codes the offline check did not judge, each
// marked as the controller's.
func kennelFleetFindings(ctx context.Context, client *apiClient, name string, offlineCodes map[kennel.Code]bool) ([]offline.Finding, error) {
	var pg struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if _, err := client.get(ctx, "/kennel/repositories", url.Values{"q": {name}, "per_page": {"50"}}, &pg); err != nil {
		return nil, err
	}
	id := ""
	for _, it := range pg.Items {
		if it.Name == name {
			id = it.ID
		}
	}
	if id == "" {
		return nil, fmt.Errorf("the controller has no Kennel Club row for %s", name)
	}
	var v kennelRepositoryDoc
	if _, err := client.get(ctx, "/kennel/repositories/"+url.PathEscape(id), nil, &v); err != nil {
		return nil, err
	}
	var out []offline.Finding
	for _, f := range v.Findings {
		if offlineCodes[f.Code] {
			continue
		}
		f.From = "controller"
		out = append(out, f)
	}
	return out, nil
}

// severityRank orders severities for --severity: a floor, not a match.
func severityRank(s kennel.Severity) int {
	switch s {
	case kennel.SeverityError:
		return 3
	case kennel.SeverityWarning:
		return 2
	}
	return 1
}

// kennelCheck runs the evaluator over .github/workflows on disk. It takes the
// client flags so that a later --controller can join the fleet's findings in,
// and builds no client without it: the point of the command is that nothing
// is sent anywhere.
func kennelCheck(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies kennel check [path]", "Run Kennel Club's workflow checks over a checkout, with nothing sent anywhere.")
	output := fs.String("output", outputTable, "table or json")
	public := fs.Bool("public", false, "the repository is public, which makes some findings worse; private is assumed otherwise")
	codes := fs.String("code", "", "only these checks, comma-separated codes")
	severity := fs.String("severity", string(kennel.SeverityInfo), "report findings at this severity or worse: error, warning or info")
	prompts := fs.Bool("prompts", false, "render a prompt for a coding agent on each finding")
	controllerRepo := fs.String("controller", "", "owner/name: also ask the controller for this repository's fleet-dependent findings")
	cf := registerClientFlags(fs, false)
	fs.example(
		"zoomies kennel check",
		"zoomies kennel check --output json --prompts ~/src/widgets",
		"zoomies kennel check --severity warning --code ci.no_timeout,ci.action_not_pinned",
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	root := "."
	if fs.NArg() > 1 {
		return usagef(fs.Name(), "takes one path, but %d were given", fs.NArg())
	}
	if fs.NArg() == 1 {
		root = fs.Arg(0)
	}
	floor := severityRank(kennel.Severity(*severity))
	if *severity != string(kennel.SeverityError) && *severity != string(kennel.SeverityWarning) && *severity != string(kennel.SeverityInfo) {
		return usagef(fs.Name(), "--severity %q is not one; use error, warning or info", *severity)
	}
	if *output != outputTable && *output != outputJSON {
		return usagef(fs.Name(), "--output %q is not a format; use table or json", *output)
	}
	wanted := map[string]bool{}
	for _, c := range strings.Split(*codes, ",") {
		if c = strings.TrimSpace(c); c != "" {
			wanted[c] = true
		}
	}

	report, err := offline.Check(root, offline.Options{Public: *public, Prompts: *prompts})
	if errors.Is(err, offline.ErrNoWorkflows) {
		return &codedExit{code: exitError, msg: fmt.Sprintf("%s: %v", root, err)}
	}
	if err != nil {
		return err
	}
	kept := report.Findings[:0:0]
	for _, f := range report.Findings {
		if severityRank(f.Severity) < floor || (len(wanted) > 0 && !wanted[string(f.Code)]) {
			continue
		}
		kept = append(kept, f)
	}
	report.Findings = kept
	if *controllerRepo != "" {
		client, err := cf.client()
		if err != nil {
			return err
		}
		judged := map[kennel.Code]bool{}
		for _, c := range kennel.Checks() {
			judged[c.Code] = true
		}
		for _, n := range report.NotChecked {
			delete(judged, n.Code)
		}
		fleet, err := kennelFleetFindings(ctx, client, *controllerRepo, judged)
		if err != nil {
			return err
		}
		for _, f := range fleet {
			if severityRank(f.Severity) < floor || (len(wanted) > 0 && !wanted[string(f.Code)]) {
				continue
			}
			if !*prompts {
				f.Prompt = ""
			}
			report.Findings = append(report.Findings, f)
		}
	}

	if *output == outputJSON {
		b, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(e.out, string(b))
	} else {
		printKennelReport(e, report, *severity)
	}
	if len(report.Findings) > 0 {
		return &codedExit{code: kennelExitFindings}
	}
	return nil
}

func printKennelReport(e *env, r offline.Report, severity string) {
	paths := make(map[string]string, len(r.Files))
	for _, f := range r.Files {
		paths[f.SHA] = f.Path
	}
	fmt.Fprintf(e.out, "Read %d workflow files under %s, as a %s repository.\n", len(r.Files), r.Root, r.Visibility)
	for _, f := range r.Unreadable {
		fmt.Fprintf(e.out, "Could not read %s\n", pathOrUnusual(f.Path))
	}
	if len(r.Findings) == 0 {
		fmt.Fprintf(e.out, "Nothing found at %s or worse.\n", severity)
	}
	printKennelFindings(e, r.Findings, paths)
	if len(r.NotChecked) > 0 {
		fmt.Fprintln(e.out, "\nNot checked here:")
		for _, n := range r.NotChecked {
			fmt.Fprintf(e.out, "  %-36s %s\n", n.Code, n.Reason)
		}
	}
}

func printKennelFindings(e *env, findings []offline.Finding, paths map[string]string) {
	for _, f := range findings {
		from := ""
		if f.From != "" {
			from = "  (from the " + f.From + ")"
		}
		fmt.Fprintf(e.out, "\n%-8s %s  %s%s\n", strings.ToUpper(string(f.Severity)), f.Code, f.Title, from)
		fmt.Fprintf(e.out, "         %s\n", f.Detail)
		fmt.Fprintf(e.out, "         What to change: %s\n", f.Fix)
		for _, ev := range f.Evidence {
			if line := kennel.EvidenceText(ev, paths); line != "" {
				fmt.Fprintf(e.out, "         %s\n", line)
			}
		}
		if f.Prompt != "" {
			fmt.Fprintf(e.out, "\n%s\n", indent(f.Prompt, "         "))
		}
	}
}

func pathOrUnusual(p string) string {
	if p == "" {
		return "a workflow with an unusual name"
	}
	return p
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}
