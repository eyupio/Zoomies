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

// codedExit carries a command's documented exit code through report: why's 2
// for a job this fleet never saw and 3 for a class the fleet could not
// decide, kennel check's 4 for findings. It is the same seam doctorExit uses,
// so a command-specific outcome never has to be a string the dispatcher
// parses back.
type codedExit struct {
	code int
	msg  string
}

func (w *codedExit) Error() string { return w.msg }

const (
	codedExitNotFound = 2
	codedExitUnknown  = 3
)

// githubJobURL is the two shapes of URL a person has in their clipboard: the
// run, or a job inside it. GitHub's run ID keys a run only within one
// repository, which is why the repository travels with it to the list.
var githubJobURL = regexp.MustCompile(`^https?://github\.com/([^/]+/[^/]+)/actions/runs/(\d+)(?:/jobs?/(\d+))?`)

// runWhy answers "why did this job fail, stall or run slow" from the
// controller's explanation and nothing else: the CLI lays the answer out,
// it does not reason about the fleet, so it and the drawer cannot disagree.
func runWhy(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies why <job> | --latest-failed", "Why a job failed, stalled or is waiting.")
	latestFailed := fs.Bool("latest-failed", false, "explain the newest failed job instead of a named one")
	repo := fs.String("repo", "", "with --latest-failed, only this repository (owner/name)")
	pool := fs.String("pool", "", "with --latest-failed, only jobs this pool claimed (a pool ID)")
	logs := fs.Int("logs", 12, "how many of the runner's last lines to quote, ending on the one that decided the class")
	noLogs := fs.Bool("no-logs", false, "quote none of the runner's output")
	cf := registerClientFlags(fs, true)
	fs.example(
		"zoomies why job_01abc",
		"zoomies why https://github.com/acme/widgets/actions/runs/1234/job/5678",
		"zoomies why --latest-failed --repo acme/widgets",
		"zoomies why job_01abc --output json",
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	var arg string
	if *latestFailed {
		if err := fs.noMoreArgs(); err != nil {
			return err
		}
	} else {
		id, err := fs.oneArg("a job ID, a GitHub run or job URL, or --latest-failed")
		if err != nil {
			return err
		}
		arg = id
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}

	id, err := resolveJob(ctx, client, arg, *latestFailed, *repo, *pool)
	if err != nil {
		return err
	}
	q := url.Values{}
	if *noLogs {
		q.Set("logs", "0")
	} else {
		q.Set("logs", strconv.Itoa(*logs))
	}
	var why explanationItem
	raw, err := client.get(ctx, "/jobs/"+url.PathEscape(id)+"/explanation", q, &why)
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.status == 404 {
			return &codedExit{code: codedExitNotFound, msg: fmt.Sprintf("no job %s here; `zoomies jobs list` shows what this fleet saw", id)}
		}
		return err
	}
	// A controller from before why answers without a class. Laying that
	// out as "Class:  ()" would read as a diagnosis, and exit 0 as success.
	if why.Class == "" {
		return errors.New("this controller's explanation carries no class, so it predates zoomies why; upgrade the controller")
	}
	if p.structured() {
		if err := p.emit(raw); err != nil {
			return err
		}
	} else {
		printWhy(p, &why)
	}
	if why.Class == "unknown" {
		return &codedExit{code: codedExitUnknown}
	}
	return nil
}

// resolveJob turns what the person typed into a job ID: an ID as it is, a
// GitHub URL through the jobs list by repository and run, --latest-failed
// through the failed list. Nothing found is why's own exit 2, with the
// command that shows what the fleet did see.
func resolveJob(ctx context.Context, client *apiClient, arg string, latestFailed bool, repo, pool string) (string, error) {
	type listed struct {
		ID          string `json:"id"`
		GitHubJobID int64  `json:"github_job_id"`
	}
	q := url.Values{"include_steps": {"false"}}
	wantJob := int64(0)
	var hint string
	switch {
	case latestFailed:
		q.Set("failed", "true")
		if repo != "" {
			q.Set("repo", repo)
		}
		if pool != "" {
			q.Set("pool_id", pool)
		}
		hint = "no failed job matches; `zoomies jobs list --failed` shows what this fleet saw"
	case githubJobURL.MatchString(arg):
		m := githubJobURL.FindStringSubmatch(arg)
		q.Set("repo", m[1])
		q.Set("run_id", m[2])
		if m[3] != "" {
			wantJob, _ = strconv.ParseInt(m[3], 10, 64)
		}
		hint = fmt.Sprintf("no job matches %s; `zoomies jobs list --repo %s` shows what this fleet saw", arg, m[1])
	default:
		return arg, nil
	}
	var page listResponse[listed]
	if _, err := client.get(ctx, "/jobs", q, &page); err != nil {
		return "", err
	}
	for _, j := range page.Items {
		if wantJob == 0 || j.GitHubJobID == wantJob {
			return j.ID, nil
		}
	}
	return "", &codedExit{code: codedExitNotFound, msg: hint}
}

// printWhy lays the explanation out for a person: the sentence, the class,
// the facts, the lines, the steps. Every string came from the controller,
// which scrubbed it, and goes through plain again here because the terminal
// is the one place a stray escape does damage.
func printWhy(p *printer, why *explanationItem) {
	fmt.Fprintln(p.out, plain(why.Summary))
	if why.Detail != "" {
		fmt.Fprintln(p.out, plain(why.Detail))
	}
	if why.Fix != "" {
		fmt.Fprintln(p.out, p.paint(colourYellow, "Fix: ")+plain(why.Fix))
	}
	class := fmt.Sprintf("%s (%s)", plain(why.Class), plain(why.Confidence))
	if why.ConfidenceReason != "" {
		class += ": " + plain(why.ConfidenceReason)
	}
	fmt.Fprintln(p.out)
	fmt.Fprintln(p.out, p.paint(colourYellow, "Class: ")+class)
	if why.ProblemCode != "" {
		fmt.Fprintln(p.out, p.paint(colourYellow, "Code: ")+plain(why.ProblemCode))
	}
	if len(why.Evidence) > 0 {
		fmt.Fprintln(p.out)
		fmt.Fprintln(p.out, p.paint(colourYellow, "Evidence:"))
		for _, ev := range why.Evidence {
			line := "  " + plain(ev.Label) + ": " + plain(ev.Value)
			if ev.Unit != "" {
				line += " " + plain(ev.Unit)
			}
			if ev.Ref != "" {
				line += "  (" + plain(ev.Ref) + ")"
			}
			fmt.Fprintln(p.out, line)
		}
	}
	if why.LogExcerpt != nil {
		fmt.Fprintln(p.out)
		if n := len(why.LogExcerpt.Lines); n > 0 {
			fmt.Fprintf(p.out, "%s lines %d to %d of the runner's output:\n", p.paint(colourYellow, "Output:"), why.LogExcerpt.Lines[0].N, why.LogExcerpt.Lines[n-1].N)
			for _, l := range why.LogExcerpt.Lines {
				mark := "  "
				if l.Decisive {
					mark = "> "
				}
				fmt.Fprintf(p.out, "%s%d  %s\n", mark, l.N, plain(l.Text))
			}
		}
		if why.LogExcerpt.Note != "" {
			fmt.Fprintln(p.out, "  "+plain(why.LogExcerpt.Note))
		}
	}
	var read []string
	n := 0
	for _, s := range why.NextSteps {
		if s.Kind == "read" && strings.HasPrefix(s.Link, "https://") {
			read = append(read, plain(s.Link))
			continue
		}
		if n == 0 {
			fmt.Fprintln(p.out)
			fmt.Fprintln(p.out, p.paint(colourYellow, "Next:"))
		}
		n++
		line := fmt.Sprintf("  %d. %s", n, plain(s.Text))
		if s.Link != "" {
			line += "  (" + plain(s.Link) + ")"
		}
		fmt.Fprintln(p.out, line)
	}
	for _, r := range read {
		fmt.Fprintln(p.out, p.paint(colourYellow, "Read: ")+r)
	}
}
