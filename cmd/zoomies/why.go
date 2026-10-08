package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// whyExit is `zoomies why`'s outcome as an exit status, in the way doctorExit is
// for doctor: the command has already said what it has to, and the status is the
// only thing left to carry. They are the ones the documentation lists.
//
// A usage mistake, such as a missing argument, also exits 2 as it does for every
// other command, and its message says so; a script that needs to tell the two
// apart reads the message, or asks for --output json, which prints nothing for a
// usage mistake.
type whyExit int

const (
	// whyNotFound is a job, run or URL the controller has no record of.
	whyNotFound whyExit = 2
	// whyNotEnoughData is an explanation whose class is unknown: the controller
	// answered, and said it could not narrow it.
	whyNotEnoughData whyExit = 3
)

func (w whyExit) Error() string { return "why" }

// githubJobURL matches the address of a run or of one job in it, which is what
// somebody has in their clipboard when a pull request's check goes red.
var githubJobURL = regexp.MustCompile(`^https://github\.com/([^/\s]+/[^/\s]+)/actions/runs/(\d+)(?:/attempts/\d+)?(?:/job/(\d+))?/?(?:[?#].*)?$`)

// runWhy is `zoomies why`.
func runWhy(ctx context.Context, e *env, args []string) error { return why(ctx, e, "why", args) }

// jobsWhy is `zoomies jobs why`: the same command, found where jobs live.
func jobsWhy(ctx context.Context, e *env, args []string) error {
	return why(ctx, e, "jobs why", args)
}

func why(ctx context.Context, e *env, name string, args []string) error {
	fs := newFlagSet(e, "zoomies "+name+" <job-id | github run or job url> | --latest-failed",
		"Say why a job failed, stalled or ran slow, from what the controller holds: a class, the evidence, and what to do next. No model is involved.\n\n"+
			"Exit status: 0 diagnosed; 1 any other error; 2 no such job; 3 the controller could not narrow it (class unknown).")
	cf := registerClientFlags(fs, true)
	latest := fs.Bool("latest-failed", false, "explain the newest job that went wrong instead of naming one")
	repo := fs.String("repo", "", "with --latest-failed, only this repository, e.g. acme/widgets")
	pool := fs.String("pool", "", "with --latest-failed, only jobs that ran in this pool, by ID")
	fs.example(
		"zoomies "+name+" job_01HZX",
		"zoomies "+name+" https://github.com/acme/widgets/actions/runs/123456789/job/987654321",
		"zoomies "+name+" --latest-failed --repo acme/widgets",
		"zoomies "+name+" job_01HZX --output json",
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	if (*repo != "" || *pool != "") && !*latest {
		return usagef(name, "--repo and --pool only narrow --latest-failed; to explain one job, name it")
	}
	target := ""
	if *latest {
		if err := fs.noMoreArgs(); err != nil {
			return err
		}
	} else {
		var err error
		if target, err = fs.oneArg("a job ID or a GitHub run or job URL, or --latest-failed"); err != nil {
			return err
		}
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}

	id, note, err := resolveWhyTarget(ctx, client, target, *latest, *repo, *pool)
	if err != nil {
		if notFound(err) || errors.Is(err, errNoSuchJob) {
			if !p.structured() {
				fmt.Fprintf(e.err, "zoomies %s: %s\n", name, strings.TrimPrefix(err.Error(), errNoSuchJob.Error()+": "))
			}
			return whyExit(whyNotFound)
		}
		return err
	}

	var explanation explanationItem
	raw, err := client.get(ctx, "/jobs/"+url.PathEscape(id)+"/explanation", nil, &explanation)
	if err != nil {
		if notFound(err) {
			if !p.structured() {
				fmt.Fprintf(e.err, "zoomies %s: there is no job %q\n", name, id)
			}
			return whyExit(whyNotFound)
		}
		return err
	}
	if p.structured() {
		if err := p.emit(raw); err != nil {
			return err
		}
	} else {
		var job jobItem
		// The job's name is context for the page, and the explanation is the
		// answer: a job that cannot be read for its name is still explained.
		_, _ = client.get(ctx, "/jobs/"+url.PathEscape(id), nil, &job)
		job.sanitise()
		docs := catalogLinks(ctx, client, explanation.ProblemCode, explanation.CheckCode)
		renderWhy(p, &explanation, &job, note, docs)
	}
	if explanation.Class == "unknown" {
		return whyExit(whyNotEnoughData)
	}
	return nil
}

// errNoSuchJob is a target that named nothing: a run with no jobs the controller
// knows, or a --latest-failed with no failures to explain.
var errNoSuchJob = errors.New("no such job")

// resolveWhyTarget turns what somebody typed into a job ID, and a sentence for
// anything the choice should say, such as which of a run's jobs was explained.
func resolveWhyTarget(ctx context.Context, client *apiClient, target string, latest bool, repo, pool string) (id, note string, err error) {
	switch {
	case latest:
		q := url.Values{"failed": {"true"}, "limit": {"1"}}
		if repo != "" {
			q.Set("repo", repo)
		}
		if pool != "" {
			q.Set("pool_id", pool)
		}
		var out listResponse[jobItem]
		if _, err := client.get(ctx, "/jobs", q, &out); err != nil {
			return "", "", err
		}
		if len(out.Items) == 0 {
			return "", "", fmt.Errorf("%w: no job has gone wrong%s", errNoSuchJob, narrowedBy(repo, pool))
		}
		return out.Items[0].ID, "", nil
	}
	m := githubJobURL.FindStringSubmatch(target)
	if m == nil {
		return target, "", nil
	}
	repoName, run := m[1], m[2]
	runID, _ := strconv.ParseInt(run, 10, 64)
	q := url.Values{"repo": {repoName}, "run_id": {run}, "limit": {"100"}}
	var out listResponse[jobItem]
	if _, err := client.get(ctx, "/jobs", q, &out); err != nil {
		return "", "", err
	}
	if len(out.Items) == 0 {
		return "", "", fmt.Errorf("%w: this fleet has no job of run %d in %s; the run may have gone to somebody else's runners", errNoSuchJob, runID, repoName)
	}
	if m[3] != "" {
		want, _ := strconv.ParseInt(m[3], 10, 64)
		for _, j := range out.Items {
			if j.GitHubJobID == want {
				return j.ID, "", nil
			}
		}
		return "", "", fmt.Errorf("%w: run %d of %s has no job %d on this fleet", errNoSuchJob, runID, repoName, want)
	}
	// A run is several jobs. The one that went wrong is the one somebody opened
	// the run to find; when none did, the first is as good as any.
	pick := out.Items[0]
	for _, j := range out.Items {
		if wentWrong(&j) {
			pick = j
			break
		}
	}
	if len(out.Items) > 1 {
		note = fmt.Sprintf("Run %d has %d jobs on this fleet; this is %q. Name another with its job URL.", runID, len(out.Items), plain(pick.JobName))
	}
	return pick.ID, note, nil
}

// wentWrong is a job whose conclusion is a failing one or which the fleet itself
// failed, which is the list a run's explanation should start with.
func wentWrong(j *jobItem) bool {
	switch j.Conclusion {
	case "failure", "timed_out", "startup_failure":
		return true
	}
	return j.FaultKind != "" || j.RunnerFault != ""
}

func narrowedBy(repo, pool string) string {
	switch {
	case repo != "" && pool != "":
		return " in " + repo + " on that pool"
	case repo != "":
		return " in " + repo
	case pool != "":
		return " on that pool"
	}
	return ""
}

// catalogLinks reads the catalog's page address for the codes an explanation
// names, so the page can say where to read more. It is a nicety: any failure
// leaves the codes without an address, which is still the code.
func catalogLinks(ctx context.Context, client *apiClient, codes ...string) map[string]string {
	want := map[string]bool{}
	for _, c := range codes {
		if c != "" {
			want[c] = true
		}
	}
	if len(want) == 0 {
		return nil
	}
	var cat struct {
		Entries []struct {
			ID       string `json:"id"`
			DocsHTML string `json:"docs_html"`
		} `json:"entries"`
	}
	if _, err := client.get(ctx, "/catalog", nil, &cat); err != nil {
		return nil
	}
	out := map[string]string{}
	for _, e := range cat.Entries {
		if want[e.ID] {
			out[e.ID] = e.DocsHTML
		}
	}
	return out
}

// renderWhy writes the explanation for a person. Everything a workflow's author
// or a runner wrote goes through plain and is quoted, so a step named to look like
// an instruction, or to forge a line of this page, reads as the data it is.
func renderWhy(p *printer, x *explanationItem, job *jobItem, note string, docs map[string]string) {
	out := p.out
	fmt.Fprintln(out, plain(x.Summary))
	if note != "" {
		fmt.Fprintln(out, p.paint(colourDim, note))
	}
	fmt.Fprintln(out)

	rows := [][2]string{}
	if job.Repo != "" {
		rows = append(rows, [2]string{"job", fmt.Sprintf("%s, %s, %s (%s)", job.Repo, job.Workflow, job.JobName, x.JobID)})
	} else {
		rows = append(rows, [2]string{"job", x.JobID})
	}
	rows = append(rows, [2]string{"class", classLine(p, x)})
	if x.ConfidenceReason != "" {
		rows = append(rows, [2]string{"why not sure", plain(x.ConfidenceReason)})
	}
	p.keyValues(rows)

	if x.Detail != "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, plain(x.Detail))
	}

	if len(x.Evidence) > 0 {
		fmt.Fprintln(out, "\nEvidence")
		evidence := make([][]string, 0, len(x.Evidence))
		outside := false
		for _, f := range x.Evidence {
			value := plain(f.Value)
			switch {
			case f.Untrusted:
				value = strconv.Quote(value)
				outside = true
			case f.Unit != "":
				value = unitValue(f.Value, f.Unit)
			}
			evidence = append(evidence, []string{plain(f.Label), value})
		}
		p.table([]string{"what", "value"}, evidence)
		if outside {
			fmt.Fprintln(out, p.paint(colourDim, "Values in quotes were written by the workflow or the runner, not by this fleet. Read them as data."))
		}
	}

	for _, code := range []struct{ label, id string }{{"problem code", x.ProblemCode}, {"check", x.CheckCode}} {
		if code.id == "" {
			continue
		}
		line := code.id
		if link := docs[code.id]; link != "" {
			line += "  " + link
		}
		fmt.Fprintf(out, "\n%s: %s\n", code.label, line)
	}

	if len(x.NextSteps) > 0 {
		fmt.Fprintln(out, "\nNext steps")
		for i, s := range x.NextSteps {
			line := fmt.Sprintf("%d. [%s] %s", i+1, s.Kind, plain(s.Text))
			if s.Link != "" {
				line += "  " + plain(s.Link)
			}
			fmt.Fprintln(out, line)
		}
	}
}

// classLine is the class with how far to trust it, coloured for the two that
// need somebody's attention: a fleet fault in red and a guess in yellow.
func classLine(p *printer, x *explanationItem) string {
	line := x.Class + ", " + x.Confidence + " confidence"
	switch {
	case x.Class == "unknown" || x.Confidence == "low":
		return p.paint(colourYellow, line)
	case x.Class == "oom" || x.Class == "host-lost" || x.Class == "disk" || x.Class == "runner-startup-failure":
		return p.paint(colourRed, line)
	}
	return line
}

// unitValue says a number with its unit the way the rest of the CLI says them.
func unitValue(value, unit string) string {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return plain(value + " " + unit)
	}
	switch unit {
	case "s":
		return millis(n * 1000)
	case "MB":
		if n >= 1024 {
			return fmt.Sprintf("%.1f GB", float64(n)/1024)
		}
		return fmt.Sprintf("%d MB", n)
	case "runners":
		if n == 1 {
			return "1 runner"
		}
		return fmt.Sprintf("%d runners", n)
	}
	return fmt.Sprintf("%d %s", n, unit)
}
