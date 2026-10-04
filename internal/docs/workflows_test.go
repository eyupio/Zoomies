package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const workflowDir = "../../.github/workflows"

func workflowFiles(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(workflowDir)
	if err != nil {
		t.Fatalf("reading the workflows: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(workflowDir, e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		out[e.Name()] = string(b)
	}
	if len(out) == 0 {
		t.Fatal("no workflows found; this test is looking in the wrong place")
	}
	return out
}

// A tag is a name somebody else can move. Pinning to a commit is what makes a
// third-party action's code the code that was reviewed, and the version in the
// comment is what makes the pin legible and lets Dependabot move both together.
//
// CLAUDE.md has said this is enforced for some time and nothing enforced it.
var (
	usesLine = regexp.MustCompile(`(?m)^\s*(?:-\s+)?uses:\s*(\S+)\s*(#.*)?$`)
	pinned   = regexp.MustCompile(`^[^@]+@[0-9a-f]{40}$`)
)

func TestEveryActionIsPinnedToACommitWithItsVersion(t *testing.T) {
	for name, body := range workflowFiles(t) {
		for _, m := range usesLine.FindAllStringSubmatch(body, -1) {
			ref, comment := m[1], strings.TrimSpace(m[2])
			// A local action -- ./.github/actions/... -- is this repository's
			// own code and is already pinned by being in the commit.
			if strings.HasPrefix(ref, "./") {
				continue
			}
			if !pinned.MatchString(ref) {
				t.Errorf("%s: %q is not pinned to a commit; a tag is a name somebody else can move", name, ref)
				continue
			}
			if comment == "" {
				t.Errorf("%s: %q has no version comment, so nobody can tell what it is pinned to", name, ref)
			}
		}
	}
}

// A workflow that grants a permission at the top grants it to every job in it.
// The release workflow publishes a release from one job and images from
// others, and giving all of them both is how a build job ends up able to
// replace an artefact it has no business touching.
//
// A workflow with one job is left alone: there, the top of the file *is* the
// job, and demanding the same words a level down would be ceremony.
var jobLine = regexp.MustCompile(`(?m)^  [A-Za-z][A-Za-z0-9_-]*:$`)

func TestNoMultiJobWorkflowGrantsWriteToEveryJob(t *testing.T) {
	for name, body := range workflowFiles(t) {
		head, jobs, found := strings.Cut(body, "\njobs:")
		if !found {
			t.Errorf("%s has no jobs block", name)
			continue
		}
		if len(jobLine.FindAllString(jobs, -1)) < 2 {
			continue
		}
		perms, _, ok := strings.Cut(head, "\nenv:")
		if !ok {
			perms = head
		}
		idx := strings.Index(perms, "\npermissions:")
		if idx < 0 {
			continue
		}
		for _, line := range strings.Split(perms[idx:], "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasSuffix(line, ": write") {
				continue
			}
			t.Errorf("%s has more than one job and grants %q to all of them; move it to the job that needs it", name, line)
		}
	}
}

// The release workflow's own rules, each of which exists because of something
// that can turn a release tag into a lie about the bytes it names.
func TestTheReleaseWorkflowGuardsItsTag(t *testing.T) {
	body := workflowFiles(t)["release.yml"]
	if body == "" {
		t.Fatal("release.yml is missing")
	}
	for _, want := range []struct {
		needle, why string
	}{
		{"workflow_dispatch:", "a release must be startable by hand when the tag's own run cannot be"},
		{"prerelease:", "a tag with a hyphen is this project saying the release is not finished"},
		{"Refuse to rebuild a published release", "a full release tag must stay immutable once people can download it"},
		{"attest-build-provenance", "a checksum says the bytes match; provenance says where they came from"},
	} {
		if !strings.Contains(body, want.needle) {
			t.Errorf("release.yml has no %q: %s", want.needle, want.why)
		}
	}
}

func TestTheReleaseWorkflowStartsFromEachPublishedRelease(t *testing.T) {
	body := workflowFiles(t)["release.yml"]
	if body == "" {
		t.Fatal("release.yml is missing")
	}
	for _, want := range []string{
		"release:\n    types: [published]",
		"github.event.release.tag_name || inputs.tag",
		`draft: ${{ github.event_name == 'workflow_dispatch' }}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("release.yml is missing %q, so creating a GitHub release may not build and attach its artifacts", want)
		}
	}
	if strings.Contains(body, "\n  push:\n") {
		t.Error("release.yml still listens for tag pushes as well as release publication, so creating a release can start two competing builds")
	}
}

func TestTheReleaseWorkflowOnlyLocksManualRebuildsOfPublishedFullReleases(t *testing.T) {
	body := workflowFiles(t)["release.yml"]
	if body == "" {
		t.Fatal("release.yml is missing")
	}
	for _, want := range []string{
		"--json isDraft,isPrerelease",
		`elif .isPrerelease then "prerelease"`,
		`EVENT: ${{ github.event_name }}`,
		`if [ "$EVENT" = "release" ]; then`,
		`if [ "$PRERELEASE" = "true" ]; then`,
		`else`,
		`::error::$TAG is already published.`,
		`exit 1`,
		`prerelease) echo "$TAG is already published as a prerelease; it will be replaced." ;;`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("release.yml is missing %q, so a published prerelease is still treated as final", want)
		}
	}
}

// Both images say which release they are. The runner images carried no version
// at all, which made them the images an operator is most likely to be holding
// when something is wrong and least able to identify.
func TestEveryPublishedImageSaysWhatItIs(t *testing.T) {
	// Per published target, not per file, and following FROM.
	//
	// Checking that the file mentioned each label was enough to pass while
	// deploy/Dockerfile's `agent` target carried none at all: the controller's
	// labels satisfied every search. That went unnoticed because nothing
	// published the agent image, and stopped being harmless the moment
	// something did.
	//
	// Which targets are published is not a list kept here -- it is whatever the
	// workflows name, so a new image is covered on the day it is added.
	// Inheritance counts: Dockerfile.runner's `runner` declares no labels and
	// needs none, because it is FROM runner-base, which does.
	required := []string{
		"org.opencontainers.image.source",
		"org.opencontainers.image.version",
		"org.opencontainers.image.revision",
		"org.opencontainers.image.created",
		"org.opencontainers.image.licenses",
	}

	published := map[string]bool{}
	for name, body := range workflowFiles(t) {
		for _, m := range regexp.MustCompile(`(?m)^\s*target:\s*(\S+)\s*$`).FindAllStringSubmatch(body, -1) {
			published[m[1]] = true
		}
		_ = name
	}
	if len(published) == 0 {
		t.Fatal("no workflow names a build target; this test is looking at the wrong thing")
	}

	// Every target in every Dockerfile: what it is FROM, and the text of its
	// own section.
	type stage struct{ from, body string }
	stages := map[string]stage{}
	for _, file := range []string{"../../deploy/Dockerfile", "../../deploy/Dockerfile.runner"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		body := string(b)
		marks := regexp.MustCompile(`(?m)^FROM\s+(\S+)(?:\s+AS\s+(\S+))?\s*$`).FindAllStringSubmatchIndex(body, -1)
		for i, m := range marks {
			if m[4] < 0 {
				continue // an unnamed stage cannot be a build target
			}
			end := len(body)
			if i+1 < len(marks) {
				end = marks[i+1][0]
			}
			stages[body[m[4]:m[5]]] = stage{from: body[m[2]:m[3]], body: body[m[0]:end]}
		}
	}

	// labelled walks a target and everything it is FROM, since a label on a
	// base image is on the images built from it.
	labelled := func(name, label string) bool {
		for seen := map[string]bool{}; ; {
			st, ok := stages[name]
			if !ok || seen[name] {
				return false
			}
			if strings.Contains(st.body, label) {
				return true
			}
			seen[name] = true
			name = st.from
		}
	}

	var checked int
	for name := range published {
		if _, ok := stages[name]; !ok {
			t.Errorf("a workflow builds target %q, which no Dockerfile defines", name)
			continue
		}
		checked++
		for _, label := range required {
			if !labelled(name, label) {
				t.Errorf("the %s image has no %s label, so a registry listing says nothing about what "+
					"it is and `docker inspect` cannot answer which build is running", name, label)
			}
		}
		// A label written from an ARG that is out of scope after FROM ships
		// silently empty, which reads as "no version" rather than as a mistake.
		if st := stages[name]; strings.Contains(st.body, `image.version="${VERSION}"`) && !strings.Contains(st.body, "ARG VERSION") {
			t.Errorf("the %s image labels a version from an ARG it never re-declares after FROM, "+
				"so the label ships empty", name)
		}
	}
	if checked == 0 {
		t.Fatal("no published target was checked")
	}
}

// :latest belongs to the release workflow, and to nothing else.
//
// Both workflows used to write it. Whichever ran last won, so a merge to main
// overwrote the :latest a release had just published and an operator who pulled
// it got an unreleased build stamped main-sha-abc1234. That is worse than it
// sounds: an agent is installed from a release asset and no release carries a
// main- version, so a controller on that image cannot match any agent it
// enrols, and shows every host as a different build for as long as it runs.
//
// main publishes :dev instead, which says what it is. This asserts the split
// both ways, because either half alone is the same bug wearing a new name.
func TestOnlyTheReleaseWorkflowPublishesLatest(t *testing.T) {
	files := workflowFiles(t)

	ci, ok := files["ci.yml"]
	if !ok {
		t.Fatal("no ci.yml")
	}
	release, ok := files["release.yml"]
	if !ok {
		t.Fatal("no release.yml")
	}

	// The two workflows spell an image name differently -- ci.yml through an
	// env var, release.yml in full -- so each is asserted the way it is
	// actually written. Checking release.yml's spelling against ci.yml is a
	// check that can never fire, which is what the first draft of this test
	// did.
	for _, img := range []struct{ ref, name string }{
		{"CONTROLLER_IMAGE", "ghcr.io/eyupio/zoomies"},
		{"AGENT_IMAGE", "ghcr.io/eyupio/zoomies-agent"},
	} {
		if strings.Contains(ci, "env."+img.ref+" }}:latest") {
			t.Errorf("ci.yml publishes %s:latest; that tag is the release workflow's, and a merge to main "+
				"would overwrite the release an operator asked for", img.name)
		}
		// main has to publish something, or its builds are reachable only by
		// commit.
		if !strings.Contains(ci, "env."+img.ref+" }}:dev") {
			t.Errorf("ci.yml does not publish %s:dev", img.name)
		}
		if !strings.Contains(release, img.name+":latest") {
			t.Errorf("release.yml no longer publishes %s:latest, so nothing does and the tag goes stale", img.name)
		}
		if !strings.Contains(release, img.name+":$REF") {
			t.Errorf("release.yml does not publish %s under its own tag", img.name)
		}
	}

	// The env vars the assertions above are written in terms of have to be the
	// images they are believed to be.
	for _, want := range []string{
		"CONTROLLER_IMAGE: ghcr.io/eyupio/zoomies\n",
		"AGENT_IMAGE: ghcr.io/eyupio/zoomies-agent\n",
	} {
		if !strings.Contains(ci, want) {
			t.Errorf("ci.yml does not define %q, so the checks above are testing a name nothing uses", strings.TrimSuffix(want, "\n"))
		}
	}

	// Runner tags are emitted from one loop for both repositories. They obey the
	// same contract: dev is main, latest is a full release.
	if strings.Contains(ci, `echo "$image:latest"`) {
		t.Error("ci.yml publishes a runner :latest tag; that tag is the release workflow's")
	}
	if !strings.Contains(ci, `echo "$image:dev"`) {
		t.Error("ci.yml does not publish the default runner variant under :dev")
	}
	if !strings.Contains(release, `echo "ghcr.io/eyupio/$image:latest"`) {
		t.Error("release.yml no longer publishes runner :latest tags")
	}
	for _, want := range []string{
		"RUNNER_IMAGE: ghcr.io/eyupio/zoomies-runner\n",
		"RUNNER_DOCKER_IMAGE: ghcr.io/eyupio/zoomies-runner-docker\n",
		"RUNNER_FULL_IMAGE: ghcr.io/eyupio/zoomies-runner-full\n",
	} {
		if !strings.Contains(ci, want) {
			t.Errorf("ci.yml does not define %q", strings.TrimSuffix(want, "\n"))
		}
	}

	// runner-full is published for the catalogue's Full rows only, and both
	// manifest jobs learn which those are from the marker the build leg
	// leaves. A job that stopped reading it would build the image on every
	// release and tag it on none.
	for name, wf := range map[string]string{"ci.yml": ci, "release.yml": release} {
		for _, want := range []string{
			`touch "$out/full"`,
			`[ -f "$dir/full" ] && images="$images zoomies-runner-full"`,
			"target: runner-full",
		} {
			if !strings.Contains(wf, want) {
				t.Errorf("%s is missing %q, so zoomies-runner-full is built or tagged by nothing", name, want)
			}
		}
	}
	if cleanup := workflowFiles(t)["cleanup-packages.yml"]; !strings.Contains(cleanup, "- zoomies-runner-full\n") {
		t.Error("cleanup-packages.yml does not prune zoomies-runner-full, whose untagged versions are several gigabytes each")
	}

	// And the gate that keeps a prerelease from becoming what :latest means.
	if !strings.Contains(release, `if [ "$PRERELEASE" != "true" ]; then`) {
		t.Error("release.yml no longer gates :latest on the release not being a prerelease")
	}
}

func TestMainPublishesAnInstallableDevBinary(t *testing.T) {
	ci := workflowFiles(t)["ci.yml"]
	for _, want := range []string{
		"dev-binaries:",
		"github.event_name == 'push' && github.ref == 'refs/heads/main'",
		"make dist VERSION=${{ steps.build.outputs.version }}",
		"gh release create dev",
		"--prerelease --latest=false",
		// A documentation merge leaves the channel where it was rather than
		// republishing every binary with nothing in them different.
		"needs.changes.outputs.publish == 'true'",
	} {
		if !strings.Contains(ci, want) {
			t.Errorf("ci.yml is missing %q, so --version dev is not a complete rolling binary channel", want)
		}
	}
	// The three properties that make the publish survive GitHub's asset
	// upload, which has taken nine minutes a file and then failed on the next
	// one. Each is a line somebody could drop as tidying, and the channel
	// would go back to needing a clean run of 250 MB to publish at all.
	for _, want := range []struct{ code, why string }{
		{"is already published", "a re-run re-uploads what the release already holds, so a failed publish never makes progress"},
		{"could not be uploaded after three attempts", "one interrupted transfer fails the publish outright"},
		{"the dev release is missing:", "a release short of a binary is published as though it were whole"},
	} {
		if !strings.Contains(ci, want.code) {
			t.Errorf("ci.yml lost %q, so %s", want.code, want.why)
		}
	}
	// A release is the same transfer with more at stake.
	if !strings.Contains(workflowFiles(t)["release.yml"], "preserve_order: true") {
		t.Error("release.yml uploads its assets all at once, which is how a release ships without its binaries")
	}
}

func TestMainPublishingRunsCannotCancelOneAnother(t *testing.T) {
	ci := workflowFiles(t)["ci.yml"]
	// Only a pull request is ever cancelled. Main's push, a dispatch and the
	// recovery run all publish, so none of them may be the one that is replaced.
	want := "cancel-in-progress: ${{ github.event_name == 'pull_request' }}"
	if !strings.Contains(ci, want) {
		t.Fatalf("ci.yml is missing %q; a newer main run can cancel the publisher and leave :dev stale", want)
	}
}

// A pull request's checks have to stop when the pull request does.
//
// Closing or merging one used to cancel nothing by itself: its checks run on
// refs/pull/N/merge and main's on refs/heads/main, so no run ever arrived in the
// group they were holding, and a merged pull request kept its fleet slots while
// the commit it had become queued behind them. The fix is a `closed` run that
// takes over the pull request's group and then does nothing, and each part of it
// is a line somebody could drop as tidying: the event, the group keyed on the
// pull request, the expression that cancels only a pull request, and the guard
// on every job.
//
// cancel-merged-checks.yml is the closed handler itself, so a guard on its job
// would switch it off.
func TestAClosedPullRequestStopsItsOwnChecksAndStartsNothing(t *testing.T) {
	handlers := map[string]bool{"cancel-merged-checks.yml": true}
	const (
		wantGroup  = "${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}"
		wantCancel = "${{ github.event_name == 'pull_request' }}"
	)
	type workflow struct {
		On          yaml.Node `yaml:"on"`
		Concurrency struct {
			Group            string `yaml:"group"`
			CancelInProgress string `yaml:"cancel-in-progress"`
		} `yaml:"concurrency"`
		Jobs map[string]struct {
			If string `yaml:"if"`
		} `yaml:"jobs"`
	}

	files := workflowFiles(t)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	var checked int
	for _, name := range names {
		var wf workflow
		if err := yaml.Unmarshal([]byte(files[name]), &wf); err != nil {
			t.Errorf("parsing %s: %v", name, err)
			continue
		}
		types, runsOnPullRequests := pullRequestTypes(wf.On)
		if !runsOnPullRequests || handlers[name] {
			continue
		}
		checked++

		for _, want := range []string{"opened", "synchronize", "reopened", "closed"} {
			if !slices.Contains(types, want) {
				t.Errorf("%s: pull_request does not list %q, so closing or merging a pull request cannot cancel its checks "+
					"(listing types replaces GitHub's defaults, which are opened, synchronize and reopened)", name, want)
			}
		}
		if wf.Concurrency.Group != wantGroup {
			t.Errorf("%s: concurrency group is %q, want %q; a group keyed on anything else is one the closed run does not join",
				name, wf.Concurrency.Group, wantGroup)
		}
		if wf.Concurrency.CancelInProgress != wantCancel {
			t.Errorf("%s: cancel-in-progress is %q, want %q; anything else cancels a run on main, which may be publishing, "+
				"or fails to cancel the pull request's", name, wf.Concurrency.CancelInProgress, wantCancel)
		}
		for id, job := range wf.Jobs {
			if problem := closedGuardProblem(job.If); problem != "" {
				t.Errorf("%s: job %s %s", name, id, problem)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no workflow runs on pull_request; this test is looking at the wrong thing")
	}
}

// pullRequestTypes reads a workflow's `on:` and says whether it runs on
// pull_request, and for which activity types if it says.
func pullRequestTypes(on yaml.Node) (types []string, found bool) {
	switch on.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(on.Content); i += 2 {
			if on.Content[i].Value != "pull_request" {
				continue
			}
			var trigger struct {
				Types []string `yaml:"types"`
			}
			// A trigger written with no value is null, which decodes to nothing.
			_ = on.Content[i+1].Decode(&trigger)
			return trigger.Types, true
		}
	case yaml.SequenceNode:
		for _, n := range on.Content {
			if n.Value == "pull_request" {
				return nil, true
			}
		}
	case yaml.ScalarNode:
		return nil, on.Value == "pull_request"
	}
	return nil, false
}

// closedGuard is what keeps the run a closed pull request starts from starting
// anything: the closed event exists only to cancel the run in flight.
const closedGuard = "github.event.action != 'closed'"

// closedGuardProblem says what is wrong with a job's condition, or returns
// nothing when the guard covers the whole of it.
func closedGuardProblem(cond string) string {
	c := strings.TrimSpace(cond)
	if c == "" {
		return "has no condition, so a closed pull request starts it"
	}
	c = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(c, "${{"), "}}"))
	// A status function may lead. It has to: GitHub skips a job whose
	// dependency was skipped only when the condition has none, so a job that
	// begins !cancelled() is the one that would run on a closed event's skipped
	// `changes`.
	c = strings.TrimPrefix(c, "!cancelled() && ")
	rest, ok := strings.CutPrefix(c, closedGuard)
	if !ok || (rest != "" && !strings.HasPrefix(rest, " && ")) {
		return "does not lead with " + closedGuard + ", so a closed pull request can start it"
	}
	// && binds tighter than ||, so `guard && a || b` is `(guard && a) || b` and
	// still runs the job when b holds.
	if hasTopLevelOr(rest) {
		return "has an || outside parentheses, so the guard covers only the operand before it"
	}
	return ""
}

// hasTopLevelOr reports whether an expression has an || outside every
// parenthesis and every string literal.
func hasTopLevelOr(expr string) bool {
	depth, quoted := 0, false
	for i := 0; i < len(expr); i++ {
		switch ch := expr[i]; {
		case ch == '\'':
			// An escaped quote inside a literal is two of them, which toggles
			// twice and leaves the state where it was.
			quoted = !quoted
		case quoted:
		case ch == '(':
			depth++
		case ch == ')':
			depth--
		case ch == '|' && depth == 0 && i+1 < len(expr) && expr[i+1] == '|':
			return true
		}
	}
	return false
}

// The check above is only as good as its reading of a condition, and the two
// mistakes it exists for look fine at a glance.
func TestTheClosedGuardCheckRejectsAGuardThatDoesNotCoverTheJob(t *testing.T) {
	for _, tc := range []struct {
		name, cond string
		ok         bool
	}{
		{"a bare guard", "github.event.action != 'closed'", true},
		{"a guard and a condition", "github.event.action != 'closed' && needs.changes.outputs.go == 'true'", true},
		{"an || under parentheses", "github.event.action != 'closed' && (needs.changes.outputs.go == 'true' || needs.changes.outputs.web == 'true')", true},
		{"a status function in the expression syntax",
			"${{ !cancelled() && github.event.action != 'closed' && (github.event_name == 'pull_request' || github.ref != 'refs/heads/main') && !contains(needs.*.result, 'failure') }}", true},
		{"no condition", "", false},
		{"no guard", "needs.changes.outputs.go == 'true'", false},
		{"a guard that is not first", "needs.changes.outputs.go == 'true' && github.event.action != 'closed'", false},
		{"the || the guard does not cover", "github.event.action != 'closed' && needs.changes.outputs.go == 'true' || needs.changes.outputs.web == 'true'", false},
		{"a status function and no guard", "${{ !cancelled() && needs.images.outputs.publish == 'true' }}", false},
		{"a parenthesis inside a literal", "github.event.action != 'closed' && github.head_ref != '(' || github.ref != 'x'", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := closedGuardProblem(tc.cond) == ""; got != tc.ok {
				t.Errorf("closedGuardProblem(%q) accepted = %v, want %v", tc.cond, got, tc.ok)
			}
		})
	}
}

// Nothing that releases or deploys may be cancelled by the run that follows it.
//
// A cancelled release leaves a tag with half its assets, and a cancelled Pages
// deployment leaves the site on the one before. Neither workflow has a newer
// run to blame for that, so neither is allowed to be told to cancel one.
func TestReleasesAndDeploysAreNeverCancelled(t *testing.T) {
	files := workflowFiles(t)
	if release := files["release.yml"]; strings.Contains(release, "cancel-in-progress: true") {
		t.Error("release.yml cancels in-progress runs; a cancelled release can leave a tag with half its assets")
	}
	var docs struct {
		Jobs map[string]struct {
			Concurrency struct {
				Group            string `yaml:"group"`
				CancelInProgress string `yaml:"cancel-in-progress"`
			} `yaml:"concurrency"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(files["docs.yml"]), &docs); err != nil {
		t.Fatalf("parsing docs.yml: %v", err)
	}
	deploy := docs.Jobs["deploy"].Concurrency
	if deploy.Group != "pages" || deploy.CancelInProgress != "false" {
		t.Errorf("docs.yml's deploy job has concurrency group %q with cancel-in-progress %q, want pages and false; "+
			"Pages takes one deployment at a time and a cancelled one leaves the site on the version before",
			deploy.Group, deploy.CancelInProgress)
	}
}

// An agent host that wants a container needs an image that is an agent.
//
// deploy/Dockerfile has carried an `agent` target since it was written and no
// workflow named it, so the only way to run one was to run the controller image
// and override its command.
func TestTheAgentImageIsBuiltAndPublished(t *testing.T) {
	files := workflowFiles(t)
	for _, name := range []string{"ci.yml", "release.yml"} {
		body, ok := files[name]
		if !ok {
			t.Fatalf("no %s", name)
		}
		if !strings.Contains(body, "target: agent") {
			t.Errorf("%s builds no `agent` target, so deploy/Dockerfile's agent image is published by nothing", name)
		}
	}
}

func TestCIDogfoodsZoomiesWithRecoveryForEveryJob(t *testing.T) {
	files := workflowFiles(t)
	selector := `((github.event_name == 'workflow_dispatch' && inputs.runner == 'github') || (github.event_name == 'pull_request' && github.event.pull_request.head.repo.fork)) && 'ubuntu-latest' || 'zoomies-linux-x64'`
	runsOn := regexp.MustCompile(`(?m)^    runs-on: (.+)$`)
	for _, name := range []string{"ci.yml", "govulncheck.yml"} {
		body := files[name]
		for _, want := range []string{"default: zoomies", "options: [zoomies, github]"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %s", name, want)
			}
		}
		matches := runsOn.FindAllStringSubmatch(body, -1)
		if len(matches) == 0 {
			t.Fatalf("%s has no job runners", name)
		}
		// Match exceptions by job ID: a count alone lets the wrong job stop
		// dogfooding while still satisfying the test. Both exist to run on a
		// platform the Zoomies pool has no host for, which is the whole point
		// of each -- everything else, including playwright and installer,
		// dogfoods the pool because the runner image gives its user
		// passwordless sudo (see deploy/Dockerfile.runner).
		exceptions := map[string]string{}
		found := map[string]bool{}
		if name == "ci.yml" {
			exceptions["arm64"] = "ubuntu-24.04-arm"
			exceptions["drill"] = "ubuntu-latest"
			exceptions["windows"] = "windows-latest"
		}
		jobs := regexp.MustCompile(`(?m)^  ([a-zA-Z0-9_-]+):\n`).FindAllStringSubmatchIndex(body, -1)
		for i, job := range jobs {
			end := len(body)
			if i+1 < len(jobs) {
				end = jobs[i+1][0]
			}
			id := body[job[2]:job[3]]
			runner := runsOn.FindStringSubmatch(body[job[1]:end])
			if len(runner) == 0 {
				continue
			}
			if want, allowed := exceptions[id]; allowed {
				found[id] = true
				if runner[1] != want {
					t.Errorf("%s job %s must run on the GitHub-hosted %s, got %s", name, id, want, runner[1])
				}
			} else if !strings.Contains(runner[1], selector) {
				t.Errorf("%s job %s lacks fork protection and manual recovery: %s", name, id, runner[1])
			}
		}
		for id := range exceptions {
			if !found[id] {
				t.Errorf("%s is missing expected GitHub-hosted job %s", name, id)
			}
		}
	}
	ci := files["ci.yml"]
	if strings.Contains(ci, "useblacksmith/") {
		t.Error("CI still depends on Blacksmith-specific builders")
	}
	if !strings.Contains(ci, "if: ${{ !cancelled() && github.event.action != 'closed' && (github.event_name == 'pull_request'") {
		t.Error("Images must respect cancellation while allowing skipped PR publishing dependencies")
	}
}

func TestContainerBuildsStampTheirRunnerChannel(t *testing.T) {
	workflows := workflowFiles(t)
	for _, tc := range []struct {
		file, stamp string
		count       int
	}{
		{"ci.yml", "RUNNER_IMAGE_TAG=dev", 2},
		{"release.yml", "RUNNER_IMAGE_TAG=${{ needs.setup.outputs.tag }}", 4},
	} {
		if got := strings.Count(workflows[tc.file], tc.stamp); got != tc.count {
			t.Errorf("%s: found %d runner tag stamps, want %d controller/agent build paths", tc.file, got, tc.count)
		}
	}
	dockerfile, err := os.ReadFile("../../deploy/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerfile), "-X github.com/eyupio/zoomies/internal/naming.RunnerImageTag=${RUNNER_IMAGE_TAG}") {
		t.Error("container build does not pass its runner tag to the binary")
	}
}

// Every binary `make dist` builds is a binary the dev channel publishes.
//
// The publish names its files rather than globbing them, because a glob
// cannot tell a missing build from a platform nobody asked for: the job that
// found dist/ short of a binary published the rest and called it a release.
// Naming them buys that check and costs this one -- a platform added to the
// Makefile and not to the workflow would be built on every push to main and
// published nowhere, which nothing else would say.
func TestTheDevChannelPublishesEveryBinaryDistBuilds(t *testing.T) {
	makefile, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatalf("reading the Makefile: %v", err)
	}
	platforms := regexp.MustCompile(`for platform in ([^;]+); do`).FindSubmatch(makefile)
	if platforms == nil {
		t.Fatal("the dist target no longer lists its platforms in a loop this test can read")
	}
	ci := workflowFiles(t)["ci.yml"]
	for _, platform := range strings.Fields(string(platforms[1])) {
		os_, arch, ok := strings.Cut(platform, "/")
		if !ok {
			t.Errorf("%q is not an os/arch", platform)
			continue
		}
		name := "dist/zoomies_" + os_ + "_" + arch
		if os_ == "windows" {
			name += ".exe"
		}
		if !strings.Contains(ci, name) {
			t.Errorf("make dist builds %s and the dev release does not publish it", name)
		}
	}
}

// Scorecard's publishing service verifies the workflow that produced a result
// before it accepts one, and one of its rules is that the job ran on a
// GitHub-hosted Ubuntu runner. The move of CI onto the fleet took this job with
// it, and every push to main afterwards failed the same way: the publish was
// refused with "invalid runner label", no SARIF was written, and the
// code-scanning upload found nothing to upload. It is the one workflow that
// cannot dogfood, so this is the one runs-on that must not follow the others.
func TestScorecardPublishesFromAGitHubHostedRunner(t *testing.T) {
	body := workflowFiles(t)["scorecard.yml"]
	if body == "" {
		t.Fatal("scorecard.yml is missing")
	}
	if !strings.Contains(body, "publish_results: true") {
		t.Fatal("scorecard.yml no longer publishes its results; the README badge and the public score come from that publish")
	}
	if !strings.Contains(body, "    runs-on: ubuntu-latest\n") {
		t.Error("scorecard.yml's analysis job does not run on ubuntu-latest; the publishing service refuses every other runner label (https://github.com/ossf/scorecard-action#workflow-restrictions), and without the publish no SARIF is written for the upload step")
	}
	if strings.Contains(body, "runs-on: zoomies-linux-x64") {
		t.Error("scorecard.yml's job runs on the fleet's label; this workflow is scored by a service that only accepts results from GitHub's runners")
	}
}

// A release tag that does not begin with a lower-case v has to fail out loud.
// The setup job once ran only for tags matching `v*`, so `V1.1.0` matched
// nothing: every job was skipped, the run was green, and the release was
// published with no assets. Skipping is reserved for the rolling dev
// prerelease by name; everything else reaches the case statement that refuses
// it with an error annotation.
func TestTheReleaseWorkflowRefusesAMalformedTagOutLoud(t *testing.T) {
	body := workflowFiles(t)["release.yml"]
	if body == "" {
		t.Fatal("release.yml is missing")
	}
	if strings.Contains(body, "startsWith(github.event.release.tag_name, 'v')") {
		t.Error("release.yml skips the run for a tag that does not begin with v, which is how V1.1.0 was published with no assets and nobody was told")
	}
	if !strings.Contains(body, "github.event.release.tag_name != 'dev'") {
		t.Error("release.yml no longer skips the rolling dev prerelease by name, so publishing dev would try to turn a moving channel into a release")
	}
	if !strings.Contains(body, `*) echo "::error::$TAG is not a release tag; they begin with a lower-case v" && exit 1 ;;`) {
		t.Error("release.yml does not refuse a tag that fails the v* check with an error, so a malformed tag would be built or silently skipped")
	}
}

// The watchdog exists for the day the fleet cannot publish its own fix, so
// each property here is one whose loss would bring that day back.
//
// It runs on GitHub's machines, because a watchdog on the fleet is starved by
// the fault it is watching for. It recovers with runner=github, the one path
// that needs nothing from the fleet. It starts the recovery run before it
// cancels anything: the recovery run queues behind the stuck one, so the stuck
// one has to go -- but cancelling main's only publisher with no replacement in
// place is the failure the whole arrangement guards against. And it cancels
// push runs only, never a dispatched one, which is somebody's recovery.
func TestTheDevWatchdogRecoversMainWithoutTheFleet(t *testing.T) {
	body := workflowFiles(t)["dev-watchdog.yml"]
	if body == "" {
		t.Fatal("dev-watchdog.yml is missing; nothing notices when main's CI is starved and :dev goes stale")
	}
	if !strings.Contains(body, "    runs-on: ubuntu-latest\n") || strings.Contains(body, "zoomies-linux-x64\n") {
		t.Error("the watchdog does not run on ubuntu-latest; on the fleet it is starved by the very fault it watches for")
	}
	if !strings.Contains(body, "-f runner=github") {
		t.Error("the watchdog's recovery does not dispatch CI with runner=github; any other runner waits on the fleet")
	}
	if !strings.Contains(body, "event=push") {
		t.Error("the watchdog's cancellation is not limited to push runs; it could cancel a recovery run, its own included")
	}
	dispatch := strings.Index(body, "gh workflow run ci.yml")
	cancel := strings.Index(body, "/cancel\"")
	if dispatch < 0 || cancel < 0 || dispatch > cancel {
		t.Error("the watchdog does not start its recovery run before it cancels; cancelling main's publisher first can leave :dev with nothing to publish it")
	}
	if !strings.Contains(body, "\npermissions: {}\n") {
		t.Error("the watchdog grants permissions at the top; its jobs ask for what they need themselves")
	}
	// This repository's scheduled runs start five to six and a half hours
	// late, longer than the limit the watchdog enforces. Main's CI being
	// requested is what has to start it.
	if !strings.Contains(body, "  workflow_run:\n    workflows: [CI]\n    types: [requested]\n    branches: [main]\n") {
		t.Error("the watchdog is not started by main's CI being requested; on a schedule alone it arrives hours after the limit it enforces")
	}
	if !strings.Contains(body, "github.event.workflow_run.event == 'push'") {
		t.Error("the watchdog waits on dispatched CI runs too; a recovery run, its own included, would be watched and recovered in turn")
	}
	// Wait is skipped for a CI run that was not a push as well as for a tick,
	// so Watch deciding by "Wait was skipped" ran at once for every such run.
	if strings.Contains(body, "needs.wait.result == 'skipped'") || !strings.Contains(body, "github.event_name != 'workflow_run' || needs.wait.outputs.act == 'true'") {
		t.Error("Watch decides whether to run from Wait being skipped; a CI run that was not a push skips Wait too, and Watch must not act on it")
	}
	// The wait holds its job for up to the limit. A workflow-wide group would
	// queue every tick and every newer commit behind it; the group that stops
	// two recoveries starting at once belongs on the job that starts them.
	if strings.Contains(body, "\nconcurrency:") {
		t.Error("the watchdog has a workflow-wide concurrency group; a Wait holding it for hours queues the newest commit's watchdog behind a stale one")
	}
	// GitHub delays or drops scheduled runs when it is busiest, and it names
	// the top of the hour as that moment. The first tick, due at 18:00, never
	// ran.
	cron := regexp.MustCompile(`cron: "([^ "]+) `).FindStringSubmatch(body)
	if cron == nil {
		t.Fatal("the watchdog has no schedule")
	}
	for _, minute := range strings.Split(cron[1], ",") {
		if minute == "0" || minute == "30" || strings.HasPrefix(minute, "*") {
			t.Errorf("the watchdog's schedule fires at minute %q; GitHub delays or drops scheduled runs at the top of the hour, so pick minutes clear of :00 and :30", minute)
		}
	}
}
