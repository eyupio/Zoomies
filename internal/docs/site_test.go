package docs

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The partial's own comment explains the attribute it leaves out, so the check
// has to read the markup and not the explanation.
var jinjaComment = regexp.MustCompile(`(?s)\{#.*?#\}`)

// The site's header names the latest release. Material's script used to fill
// it in from the GitHub API and keep the answer in the tab's sessionStorage,
// which a browser restores with the tab: a tab first opened when V1.1.0 was
// the newest release said V1.1.0 two releases later. The facts are resolved
// when the site is built now, and these are the pieces that keep that true.

func TestTheHeaderFactsAreWrittenAtBuildTimeNotFetchedByTheTheme(t *testing.T) {
	partial, err := os.ReadFile("../../overrides/partials/source.html")
	if err != nil {
		t.Fatalf("the source partial override is missing: %v", err)
	}
	markup := jinjaComment.ReplaceAllString(string(partial), "")
	if strings.Contains(markup, "data-md-component") {
		t.Error("overrides/partials/source.html carries data-md-component, so Material's script will mount on it and replace the built facts with whatever the tab cached")
	}
	if !strings.Contains(markup, "md-source__fact--") {
		t.Error("overrides/partials/source.html renders no facts, so the header names no release at all")
	}
	config, err := os.ReadFile("../../mkdocs.yml")
	if err != nil {
		t.Fatalf("reading mkdocs.yml: %v", err)
	}
	if !strings.Contains(string(config), "- hooks/source.py") {
		t.Error("mkdocs.yml does not register hooks/source.py, so nothing resolves the facts the partial renders")
	}
}

func TestTheSiteIsRebuiltAndPublishedWhenAReleaseIsPublished(t *testing.T) {
	body := workflowFiles(t)["docs.yml"]
	if body == "" {
		t.Fatal("docs.yml is missing")
	}
	for _, want := range []struct {
		needle, why string
	}{
		{"release:\n    types: [published]", "a release that does not rebuild the site leaves the header naming the one before"},
		{`gh workflow run docs.yml --repo "$REPO" --ref main`, "a release must request a run whose ref is main, not just check out main in a tag run"},
		{"GITHUB_TOKEN: ${{ github.token }}", "anonymous API requests share a rate limit a shared runner's address can already have spent"},
		{`grep -q "md-source__fact--version" site/index.html`, "an offline build is allowed to succeed without the facts; the published site is not"},
	} {
		if !strings.Contains(body, want.needle) {
			t.Errorf("docs.yml has no %q: %s", want.needle, want.why)
		}
	}
}

// v1.3.1 built successfully but Pages rejected its tag before Deploy could
// start. Checking out main cannot change the run's deployment identity.
func TestReleaseRefreshKeepsTagsOutOfThePagesEnvironment(t *testing.T) {
	body := workflowFiles(t)["docs.yml"]
	_, jobs, _ := strings.Cut(body, "\njobs:\n")
	refresh, rest, ok := strings.Cut(jobs, "\n  build:\n")
	if !ok {
		t.Fatal("the website workflow has no build job")
	}
	build, deploy, ok := strings.Cut(rest, "\n  deploy:\n")
	if !ok {
		t.Fatal("the website workflow has no deploy job")
	}
	for _, want := range []string{
		"if: github.event.action != 'closed' && github.event_name == 'release' && github.event.repository.has_pages",
		"actions: write",
		"GH_TOKEN: ${{ github.token }}",
	} {
		if !strings.Contains(refresh, want) {
			t.Errorf("release refresh is missing %q", want)
		}
	}
	if strings.Contains(refresh, "environment:") || strings.Contains(refresh, "actions/checkout@") {
		t.Error("release refresh must dispatch main without deploying or executing tag contents")
	}
	if !strings.Contains(build, "if: github.event.action != 'closed' && github.event_name != 'release'") {
		t.Error("release tags still build a Pages artefact instead of leaving it to the main-branch run")
	}
	if !strings.Contains(deploy, "if: github.event.action != 'closed' && github.event.repository.has_pages && github.ref == 'refs/heads/main' && github.event_name != 'pull_request'") {
		t.Error("Pages must only deploy from main, including when a release requested the run")
	}
	if strings.Contains(build, "actions: write") || strings.Contains(deploy, "actions: write") {
		t.Error("only the release refresh job needs permission to dispatch a workflow")
	}
}
