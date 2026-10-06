package aicontext

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// A run of the managed workflow that does not publish leaves the repository's
// context stale, and until now Zoomies could say only that: stale. It started the
// workflow again after half an hour whatever had gone wrong, so a run that fails
// the same way every time was started, failed and started again, and the person
// looking at the card was never told the one thing that would have helped -- why.
//
// Diagnose turns what GitHub reports about the last run into a Cause from a
// closed set, with the sentences an operator needs and a policy for whether
// starting the workflow again could possibly help. It is pure, like the
// scheduler: the controller reads the run and passes the facts in, so each
// failure seen in the field is a table row here rather than a story.

// Cause is why a run did not publish. It is a closed set on purpose: a reader
// of the API or the problems list can switch on it, and nothing a repository
// controls -- a log line, a step name -- ever becomes text Zoomies shows.
type Cause string

const (
	// CauseArtifactQuota is the account's Actions artifact storage being full.
	// The generator succeeded; only the hand-off between its two jobs was refused.
	CauseArtifactQuota Cause = "artifact_quota"
	// CauseArtifactUpload is the same hand-off failing for any other reason.
	CauseArtifactUpload Cause = "artifact_upload_failed"
	// CauseRunnerUnavailable is GitHub never giving the job a hosted runner: it
	// was queued, nothing ran, and it was cancelled when it timed out.
	CauseRunnerUnavailable Cause = "runner_unavailable"
	// CauseStartupFailed is GitHub refusing to start the run at all.
	CauseStartupFailed Cause = "startup_failed"
	// CauseSetupFailed is a step before generation -- checkout, Node, or the pinned
	// generator's install -- failing.
	CauseSetupFailed Cause = "setup_failed"
	// CauseOversizedFile is an earlier generator refusing the whole run because a
	// file was over the size limit, which current ones list as omitted instead.
	CauseOversizedFile Cause = "oversized_file"
	// CauseTooMuchSource is the repository's eligible source exceeding a limit
	// that only an exclusion can bring it under.
	CauseTooMuchSource Cause = "too_much_source"
	// CauseGenerationRefused is the generator stopping on one of its safety checks.
	CauseGenerationRefused Cause = "generation_refused"
	// CausePublicationRefused is the last job failing to update the output branch.
	CausePublicationRefused Cause = "publication_refused"
	// CauseDeliveryFailed is a Zoomies-only upload not reaching the controller.
	CauseDeliveryFailed Cause = "delivery_failed"
	// CauseTimedOut is a job reaching its time limit after it had started.
	CauseTimedOut Cause = "timed_out"
	// CauseUnknown is a failure Zoomies does not recognise.
	CauseUnknown Cause = "unknown"
)

// Causes lists every cause, in the order the documentation gives them.
var Causes = []Cause{
	CauseArtifactQuota, CauseArtifactUpload, CauseRunnerUnavailable, CauseStartupFailed,
	CauseSetupFailed, CauseOversizedFile, CauseTooMuchSource, CauseGenerationRefused,
	CausePublicationRefused, CauseDeliveryFailed, CauseTimedOut, CauseUnknown,
}

// Action says which control on the page is the way out, so the UI can make it the
// primary button rather than leaving an operator to find it.
type Action string

const (
	ActionNone Action = ""
	// ActionRepair is Reinstall / repair.
	ActionRepair Action = "repair"
	// ActionExclusions is the repository's exclusion settings.
	ActionExclusions Action = "exclusions"
)

// Outcome is what became of the run Diagnose was shown.
type Outcome string

const (
	// OutcomeRunning is a run still queued or working. Starting another would
	// cancel it -- the workflow's concurrency group replaces a run in progress --
	// so nothing may be started, and nothing has failed.
	OutcomeRunning Outcome = "running"
	// OutcomeSucceeded is a run that finished well. If the context is still stale
	// the cause is not the run.
	OutcomeSucceeded Outcome = "succeeded"
	// OutcomeSuperseded is a run somebody or something cancelled. A newer push
	// cancels the one before it, so this says nothing about the workflow.
	OutcomeSuperseded Outcome = "superseded"
	// OutcomeFailed is a run that did not publish, with a Diagnosis.
	OutcomeFailed Outcome = "failed"
)

// RunFacts is what GitHub said about one run, in the fewest words that let
// Diagnose decide. It names no GitHub type so that this package stays independent
// of the transport.
type RunFacts struct {
	// Status is queued, in_progress or completed; Conclusion is set once completed.
	Status     string
	Conclusion string
	Jobs       []JobFacts
}

// JobFacts is one job of the run.
type JobFacts struct {
	// Name is the job's id in the managed workflow: generate, publish or upload.
	Name       string
	Conclusion string
	Steps      []StepFacts
	// Errors are the job log's error lines, as the reader bounded them. They are
	// only ever matched against known phrases and never copied into a Diagnosis;
	// the log of a run is written by whatever the run executed.
	Errors []string
}

// StepFacts is one step of a job.
type StepFacts struct {
	Name       string
	Conclusion string
}

// Diagnosis is why the last run failed and what to do about it. The first five
// fields are decided here; the rest are filled in by the controller, which knows
// which run it read, when, and what it will do next.
type Diagnosis struct {
	Cause  Cause  `json:"cause"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
	Action Action `json:"action,omitempty"`

	// Commit is the trusted-branch commit the run was for.
	Commit string `json:"commit,omitempty"`
	RunURL string `json:"run_url,omitempty"`
	// FailedAt is when the run ended; ObservedAt is when Zoomies last read it.
	FailedAt   time.Time  `json:"failed_at"`
	ObservedAt time.Time  `json:"observed_at"`
	Retry      *RetryPlan `json:"retry,omitempty"`
}

// RetryPlan is what Zoomies will do about it without being asked.
type RetryPlan struct {
	// Automatic is whether Zoomies will start the workflow again on its own.
	Automatic bool `json:"automatic"`
	// NextAt is when it will next try, if it will.
	NextAt *time.Time `json:"next_at,omitempty"`
	// AttemptsLeft is how many more times it will start the workflow for this
	// commit before it leaves it to a person.
	AttemptsLeft int `json:"attempts_left"`
}

// Summary is the diagnosis in two sentences, for the places that carry one line
// of text: the stale card's failure and the problem's title.
func (d Diagnosis) Summary() string {
	return d.Title + ". " + d.Fix
}

// Policy is whether starting the workflow again could change the outcome, and
// how patiently. A deterministic failure -- an oversized file, a refused safety
// check -- would fail identically, and starting it only spends runner minutes and
// buries the one run that says why under bot-dispatched copies of itself.
type Policy struct {
	// Automatic is whether a fresh run could plausibly publish.
	Automatic bool
	// After is how long to leave it before each attempt.
	After time.Duration
	// Attempts is how many runs Zoomies will start for one commit.
	Attempts int
}

const (
	// quotaWindow is how often GitHub recalculates artifact storage: "every 6-12
	// hours", in its own message. Trying more often than the short end of that
	// cannot succeed, and the long end is covered by a second attempt.
	quotaWindow = 6 * time.Hour
	// ordinaryWait is the spacing Zoomies has always used between starts.
	ordinaryWait = 30 * time.Minute
)

// Policy returns the cause's retry policy.
func (c Cause) Policy() Policy {
	switch c {
	case CauseArtifactQuota:
		// Three attempts at six hours cover eighteen: one recalculation to see an
		// operator's clean-up, and a second for the slow end of GitHub's range.
		return Policy{Automatic: true, After: quotaWindow, Attempts: 3}
	case CauseRunnerUnavailable:
		// GitHub-side and passing; worth more patience than the default, because
		// each try costs nothing when no runner is given.
		return Policy{Automatic: true, After: ordinaryWait, Attempts: 4}
	case CauseStartupFailed:
		return Policy{Automatic: true, After: quotaWindow, Attempts: 2}
	case CauseSetupFailed, CauseArtifactUpload, CauseDeliveryFailed, CauseTimedOut, CauseUnknown:
		return Policy{Automatic: true, After: ordinaryWait, Attempts: 2}
	case CausePublicationRefused:
		// The message is the same for a refusal and for a GitHub API blip, so one
		// more try is fair; after that it is the branch or the token.
		return Policy{Automatic: true, After: ordinaryWait, Attempts: 1}
	default:
		// oversized_file, too_much_source, generation_refused: the workflow, the
		// configuration or the repository has to change first.
		return Policy{}
	}
}

// Diagnose reads the facts of the newest run for a commit. A Diagnosis comes back
// only for OutcomeFailed.
func Diagnose(f RunFacts) (Outcome, *Diagnosis) {
	if f.Status != "completed" && f.Conclusion == "" {
		return OutcomeRunning, nil
	}
	switch f.Conclusion {
	case "success":
		return OutcomeSucceeded, nil
	case "cancelled", "skipped", "neutral", "stale":
		return OutcomeSuperseded, nil
	case "startup_failure":
		return OutcomeFailed, diagnosis(CauseStartupFailed)
	}
	i := FirstFailedJob(f.Jobs)
	if i < 0 {
		return OutcomeFailed, diagnosis(CauseUnknown)
	}
	return OutcomeFailed, diagnosis(causeOfJob(f.Jobs[i]))
}

// FirstFailedJob is the index of the first job that did not finish well, or -1.
// A job skipped because the one before it failed is not the cause. The controller
// uses it to read the log of the job Diagnose will look at, and no other.
func FirstFailedJob(jobs []JobFacts) int {
	for i, j := range jobs {
		switch j.Conclusion {
		case "failure", "timed_out", "cancelled", "startup_failure":
			return i
		}
	}
	return -1
}

// causeOfJob decides from the log first and from the shape of the job second,
// because the log is the only thing that tells a full quota from any other upload
// failure and is also the thing that may be missing: it expires, and a job that
// never started has none.
func causeOfJob(j JobFacts) Cause {
	if !j.Started() {
		return CauseRunnerUnavailable
	}
	for _, line := range j.Errors {
		if c, ok := causeOfLine(line); ok {
			return c
		}
	}
	step := failedStep(j)
	switch {
	case j.Conclusion == "timed_out":
		return CauseTimedOut
	case strings.Contains(step, "upload-artifact"):
		return CauseArtifactUpload
	case strings.Contains(step, "Generate"):
		return CauseGenerationRefused
	case strings.Contains(step, "checkout"), strings.Contains(step, "setup-node"), strings.Contains(step, "Install pinned generator"):
		return CauseSetupFailed
	case j.Name == "publish" && strings.Contains(step, "publish"):
		return CausePublicationRefused
	case j.Name == "upload" && strings.Contains(step, "Zoomies"):
		return CauseDeliveryFailed
	case j.Conclusion == "cancelled":
		// A started job that was cancelled, in a run that failed: it ran out of
		// time, which GitHub reports as a cancellation when the limit is the job's.
		return CauseTimedOut
	}
	return CauseUnknown
}

// causeOfLine matches the phrases the managed workflow and GitHub's own artifact
// action print. They are fixed text from pinned code, so matching is by
// substring; nothing is parsed out of them.
func causeOfLine(line string) (Cause, bool) {
	switch {
	case strings.Contains(line, "Artifact storage quota"):
		return CauseArtifactQuota, true
	case strings.Contains(line, "Context generation refused: A source file exceeds the context size limit"):
		return CauseOversizedFile, true
	case strings.Contains(line, "Context generation refused:") &&
		(strings.Contains(line, "add exclusions") || strings.Contains(line, "exceeds its limit")):
		return CauseTooMuchSource, true
	case strings.Contains(line, "Context generation refused"):
		return CauseGenerationRefused, true
	case strings.Contains(line, "Context publication refused"):
		return CausePublicationRefused, true
	case strings.Contains(line, "Zoomies refused the upload"),
		strings.Contains(line, "The upload to Zoomies could not complete"),
		strings.Contains(line, "GitHub did not issue an OIDC token"),
		strings.Contains(line, "The upload address must be an https URL"):
		return CauseDeliveryFailed, true
	case strings.Contains(line, "Failed to CreateArtifact"), strings.Contains(line, "Failed to FinalizeArtifact"):
		return CauseArtifactUpload, true
	}
	return "", false
}

// Started is whether any step of the job ran. A job that failed or was cancelled
// with none never had a runner: its steps are the runner's own report, and it has
// no log either.
func (j JobFacts) Started() bool {
	for _, s := range j.Steps {
		if s.Conclusion != "" && s.Conclusion != "skipped" {
			return true
		}
	}
	return false
}

// failedStep is the name of the step that failed, or the one a timeout cut off.
// It is only compared with known fragments and never shown.
func failedStep(j JobFacts) string {
	for _, s := range j.Steps {
		if s.Conclusion == "failure" || s.Conclusion == "cancelled" || s.Conclusion == "timed_out" {
			return s.Name
		}
	}
	return ""
}

func diagnosis(c Cause) *Diagnosis {
	d := Diagnosis{Cause: c}
	switch c {
	case CauseArtifactQuota:
		d.Title = "GitHub's artifact storage is full"
		d.Detail = "The workflow built the context, but GitHub refused to store the file that carries it from one job to the next, because the account's Actions artifact storage quota is full. A public repository is not affected; a private one shares its owner's quota with every other workflow that uploads artifacts, so whatever filled it is usually not this workflow. GitHub recalculates usage only every six to twelve hours."
		d.Fix = "Delete old artifacts, or give the workflows that upload the most a shorter retention-days. Space freed shows up at the next recalculation, not at once."
	case CauseArtifactUpload:
		d.Title = "GitHub would not store the hand-off artifact"
		d.Detail = "The context was built, but uploading the file that carries it between the workflow's jobs failed for a reason other than the quota. GitHub's message is on that step of the run."
		d.Fix = "Open the run to read it. A GitHub incident is the usual cause, and Zoomies will try again."
	case CauseRunnerUnavailable:
		d.Title = "GitHub did not provide a runner"
		d.Detail = "The run was queued, but GitHub never started its job on a hosted runner, so no step ran and the job was cancelled when it timed out. This is on GitHub's side and normally passes."
		d.Fix = "Nothing to change in Zoomies. If it keeps happening, check githubstatus.com and that the account's billing allows Actions to run on private repositories."
	case CauseStartupFailed:
		d.Title = "GitHub would not start the run"
		d.Detail = "GitHub rejected the run before any job began. Either it found the workflow file invalid, or the account's billing or the organisation's Actions policy does not allow it to run."
		d.Fix = "Open the run: GitHub gives its reason there. Check the account's Actions billing and the organisation's allowed-actions policy."
	case CauseSetupFailed:
		d.Title = "The generator could not be set up"
		d.Detail = "A step before generation failed: checking out the repository, installing Node, or installing the pinned generator from the npm registry. These are almost always an outage at GitHub or npm."
		d.Fix = "Open the run to see which step. Zoomies will try again; if it fails the same way each time, use Reinstall / repair."
	case CauseOversizedFile:
		d.Title = "The workflow refuses files over 1 MiB"
		d.Detail = "This workflow was written by an earlier Zoomies release, which stops the whole run when any file in the repository is over 1 MiB. Current releases list such a file as omitted and carry on."
		d.Fix = "Use Reinstall / repair to move the workflow to the current generator, or add an exclusion for the large file."
		d.Action = ActionRepair
	case CauseTooMuchSource:
		d.Title = "The repository is more than one snapshot can hold"
		d.Detail = "The generator refused to build a snapshot because the repository's eligible source is over a size or file-count limit, even with the largest files left out."
		d.Fix = "Add exclusions for generated, vendored or bulky directories in this repository's AI Context settings. Only you know what an assistant can do without."
		d.Action = ActionExclusions
	case CauseGenerationRefused:
		d.Title = "The generator refused to build the context"
		d.Detail = "The generator stopped on one of its safety checks. Its log says which: the managed configuration no longer matches what was reviewed, the checkout is not the commit, nothing eligible was left, or the secret scan changed what it would carry."
		d.Fix = "Open the run to see which. Starting it again would give the same answer, so Zoomies leaves it; Reinstall / repair re-reviews the configuration."
		d.Action = ActionRepair
	case CausePublicationRefused:
		d.Title = "The workflow could not publish the context"
		d.Detail = "The context was built, but the last job could not update the zoomies-ai-context branch. The usual causes are a workflow token that cannot write contents (a repository or organisation setting, or a ruleset that blocks creating or updating that branch), files on the branch that Zoomies does not own, or the trusted branch moving on while the job ran."
		d.Fix = "Allow the workflow to write to the zoomies-ai-context branch, and check nobody has committed to it. If something else owns the branch, use Reinstall / repair."
	case CauseDeliveryFailed:
		d.Title = "Zoomies could not receive the upload"
		d.Detail = "The context was built, but sending it to Zoomies failed. The controller has to be reachable from GitHub's runners over https at its upload address, and to accept GitHub's identity token for this repository."
		d.Fix = "Check that server.external_url is an https address GitHub can reach. Reinstall / repair re-aims the workflow at the current one."
	case CauseTimedOut:
		d.Title = "The workflow ran out of time"
		d.Detail = "A job reached its time limit after it had started: fifteen minutes to generate, ten to publish or upload. The repository may be more than the generator can read in that time, or the runner may have been slow."
		d.Fix = "Zoomies will try again. If it keeps happening, add exclusions for large directories."
	default:
		d.Cause = CauseUnknown
		d.Title = "The workflow failed"
		d.Detail = "The run failed in a way Zoomies does not recognise."
		d.Fix = "Open the run to read its log. Zoomies will try again."
	}
	return &d
}

// ArtifactUsage is what the repository's own artifacts hold, which is the part of
// the account's quota Zoomies can see. The quota is shared by everything the
// account owns, so it is a lower bound, and the diagnosis says so.
type ArtifactUsage struct {
	Count int
	Bytes int64
	// Largest holds the biggest names by total size, biggest first.
	Largest []ArtifactGroup
	// Partial is true when the listing was cut short, so the totals are at least
	// these.
	Partial bool
}

// ArtifactGroup is every unexpired artifact of one name.
type ArtifactGroup struct {
	Name  string
	Count int
	Bytes int64
}

// artifactName is what GitHub allows in an artifact's name, less what would be
// awkward in a sentence. A name outside it is described, not quoted: the name is
// chosen by whoever can run a workflow, and the text lands in a UI and in the
// output of an assistant's tool.
var artifactName = regexp.MustCompile(`^[A-Za-z0-9._~@+-]{1,100}$`)

// WithArtifactUsage adds what the repository's artifacts hold to a quota
// diagnosis, since "the storage is full" without "of what" sends an operator
// looking through every workflow. Any other cause is returned unchanged.
func (d Diagnosis) WithArtifactUsage(u ArtifactUsage) Diagnosis {
	if d.Cause != CauseArtifactQuota || u.Count == 0 {
		return d
	}
	atLeast := ""
	if u.Partial {
		atLeast = "at least "
	}
	sentence := fmt.Sprintf(" In this repository, %s%d unexpired artifacts hold %s", atLeast, u.Count, byteSize(u.Bytes))
	if len(u.Largest) > 0 {
		g := u.Largest[0]
		name := "an artifact with an unusual name"
		if artifactName.MatchString(g.Name) {
			name = g.Name
		}
		sentence += fmt.Sprintf("; %d of them, named %s, hold %s", g.Count, name, byteSize(g.Bytes))
	}
	d.Detail += sentence + ". Other repositories the account owns count towards the same quota."
	return d
}

// byteSize writes a size the way a person says it.
func byteSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%.0f KiB", float64(n)/(1<<10))
	}
}
