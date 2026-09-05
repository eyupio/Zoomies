package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

const ciWorkflow = "jobs:\n  build:\n    runs-on: ubuntu-latest\n"

func migrationFake(t *testing.T) (*FakeGitHub, Client) {
	t.Helper()
	f := NewFake()
	t.Cleanup(f.Close)
	f.AddWorkflow("acme/widgets", ".github/workflows/ci.yml", ciWorkflow)
	f.AddWorkflow("acme/widgets", ".github/workflows/release.yaml", "jobs:\n  ship:\n    runs-on: ubuntu-22.04\n")
	// Not a workflow GitHub runs, and not one to open a pull request about.
	f.AddWorkflow("acme/widgets", ".github/workflows/nested/old.yml", ciWorkflow)
	f.AddWorkflow("acme/widgets", ".github/workflows/README.md", "notes")
	f.AddRepo("acme/site")
	return f, f.Client("acme", store.TargetOrg)
}

func TestListRepositoriesForAnOrg(t *testing.T) {
	f, c := migrationFake(t)
	f.SetDefaultBranch("acme/site", "master")

	repos, err := c.ListRepositories(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("repos = %+v, want both", repos)
	}
	byName := map[string]Repository{}
	for _, r := range repos {
		byName[r.FullName] = r
	}
	if got := byName["acme/widgets"].DefaultBranch; got != "main" {
		t.Errorf("acme/widgets default branch = %q, want main", got)
	}
	// A repository that never renamed its default branch is the case a
	// migration silently breaks on if the branch is guessed.
	if got := byName["acme/site"].DefaultBranch; got != "master" {
		t.Errorf("acme/site default branch = %q, want master", got)
	}
}

func TestListRepositoriesForARepoInstallation(t *testing.T) {
	f, _ := migrationFake(t)
	c := f.Client("acme/widgets", store.TargetRepo)

	repos, err := c.ListRepositories(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	if len(repos) != 1 || repos[0].FullName != "acme/widgets" {
		t.Fatalf("repos = %+v, want just the installation's own repository", repos)
	}
}

func TestListWorkflows(t *testing.T) {
	_, c := migrationFake(t)

	got, err := c.ListWorkflows(context.Background(), "acme/widgets")
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	var paths []string
	for _, w := range got {
		paths = append(paths, w.Path)
	}
	want := ".github/workflows/ci.yml,.github/workflows/release.yaml"
	if strings.Join(paths, ",") != want {
		t.Fatalf("paths = %v, want %q: only the yaml files at the top of the directory", paths, want)
	}
	if got[0].Content != ciWorkflow {
		t.Errorf("content = %q, want the file as it is on the default branch", got[0].Content)
	}
	if got[0].SHA == "" {
		t.Error("no blob sha, so an update could not be made safe against a concurrent push")
	}
}

// Most repositories in a large organisation have no workflows at all. That is
// an ordinary answer, not a broken installation, and it has its own error so a
// caller can tell the two apart.
func TestListWorkflowsWithoutAny(t *testing.T) {
	_, c := migrationFake(t)
	_, err := c.ListWorkflows(context.Background(), "acme/site")
	if !errors.Is(err, ErrNoWorkflows) {
		t.Fatalf("err = %v, want ErrNoWorkflows", err)
	}
}

func TestOpenPullRequest(t *testing.T) {
	f, c := migrationFake(t)
	before, _ := f.FileContent("acme/widgets", ".github/workflows/ci.yml")
	after := strings.Replace(before, "ubuntu-latest", "zoomies-linux-x64", 1)

	branch := BranchName(time.Date(2026, 3, 1, 9, 30, 0, 0, time.UTC))
	pr, err := c.OpenPullRequest(context.Background(), PullRequestRequest{
		Repo:          "acme/widgets",
		Head:          branch,
		Title:         "Move CI onto Zoomies runners",
		Body:          "One job moved.",
		CommitMessage: "Move CI onto Zoomies runners",
		Files: []FileChange{{
			Path:    ".github/workflows/ci.yml",
			Content: after,
			SHA:     blobSHA(before),
		}},
	})
	if err != nil {
		t.Fatalf("OpenPullRequest: %v", err)
	}
	if pr.Number != 1 || !strings.HasSuffix(pr.HTMLURL, "/acme/widgets/pull/1") {
		t.Errorf("pull request = %+v", pr)
	}
	if pr.Branch != branch {
		t.Errorf("branch = %q, want %q", pr.Branch, branch)
	}
	if got, _ := f.FileContent("acme/widgets", ".github/workflows/ci.yml"); got != after {
		t.Errorf("the file on GitHub is %q, want the rewritten one", got)
	}
	// The default branch is left alone; the change lives on its own branch.
	if branches := f.Branches("acme/widgets"); len(branches) != 2 {
		t.Errorf("branches = %v, want main plus the migration branch", branches)
	}
}

// The wizard reads a file, the operator thinks about it, and somebody pushes to
// the file in between. Committing anyway would silently revert their change.
func TestOpenPullRequestRefusesAStaleFile(t *testing.T) {
	_, c := migrationFake(t)
	_, err := c.OpenPullRequest(context.Background(), PullRequestRequest{
		Repo:  "acme/widgets",
		Head:  "zoomies/migrate-runners-stale",
		Title: "Move CI onto Zoomies runners",
		Files: []FileChange{{
			Path:    ".github/workflows/ci.yml",
			Content: "jobs: {}\n",
			SHA:     blobSHA("something else entirely"),
		}},
	})
	if err == nil {
		t.Fatal("a commit against a stale sha was accepted")
	}
	if !strings.Contains(err.Error(), "has changed since it was read") {
		t.Fatalf("err = %v, want it to say the file moved under us", err)
	}
}

func TestOpenPullRequestRefusesAnExistingBranch(t *testing.T) {
	_, c := migrationFake(t)
	req := PullRequestRequest{
		Repo:  "acme/widgets",
		Head:  "zoomies/migrate-runners-once",
		Title: "Move CI onto Zoomies runners",
		Files: []FileChange{{Path: ".github/workflows/new.yml", Content: "jobs: {}\n"}},
	}
	if _, err := c.OpenPullRequest(context.Background(), req); err != nil {
		t.Fatalf("the first attempt failed: %v", err)
	}
	// Reusing the branch would rewrite a pull request somebody may already be
	// reviewing, so it has to fail rather than force.
	if _, err := c.OpenPullRequest(context.Background(), req); err == nil {
		t.Fatal("the same branch was created twice")
	}
}

// A 403 here is almost always the three permissions the App was never given,
// and the generic runner-permission hint would send the operator to a settings
// page that is already correct.
func TestOpenPullRequestExplainsAForbidden(t *testing.T) {
	f, c := migrationFake(t)
	f.SetError("/git/refs", http.StatusForbidden, "Resource not accessible by integration")

	_, err := c.OpenPullRequest(context.Background(), PullRequestRequest{
		Repo:  "acme/widgets",
		Head:  "zoomies/migrate-runners-denied",
		Title: "Move CI onto Zoomies runners",
		Files: []FileChange{{Path: ".github/workflows/new.yml", Content: "jobs: {}\n"}},
	})
	if err == nil {
		t.Fatal("expected a failure")
	}
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("err = %v, want it to wrap ErrForbidden", err)
	}
	for _, want := range []string{"Contents", "Pull requests", "Workflows"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name the %q permission", err, want)
		}
	}
}

// A scan is the first thing an operator runs, and a 403 there used to be
// decorated with the runner permissions -- which are set correctly, so the
// operator checks them, finds nothing wrong, and concludes Zoomies is broken.
// Reading needs the same three permissions writing does, and says so.
func TestListWorkflowsExplainsAForbidden(t *testing.T) {
	f, c := migrationFake(t)
	f.SetError("/contents/", http.StatusForbidden, "Resource not accessible by integration")

	_, err := c.ListWorkflows(context.Background(), "acme/widgets")
	if err == nil {
		t.Fatal("expected a failure")
	}
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("err = %v, want it to wrap ErrForbidden", err)
	}
	for _, want := range []string{"Contents", "Pull requests", "Workflows"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name the %q permission", err, want)
		}
	}
	if strings.Contains(err.Error(), "Self-hosted runners") {
		t.Errorf("err = %v, sends the operator to the runner permissions, which are not the problem", err)
	}
}

// The migration permissions are asked for when the App is created, because
// adding one afterwards is held by GitHub until the account's owner accepts it
// -- and until they do the wizard cannot read a workflow at all. An App made
// from this manifest can run the wizard the day it is installed.
func TestManifestGrantsWhatTheMigrationNeeds(t *testing.T) {
	for _, org := range []string{"", "acme"} {
		b, err := Manifest(ManifestOptions{
			Name: "zoomies", URL: "https://z.example", WebhookURL: "https://z.example/w",
			Organization: org,
		})
		if err != nil {
			t.Fatalf("Manifest: %v", err)
		}
		var m struct {
			Permissions map[string]string `json:"default_permissions"`
		}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("decode manifest: %v", err)
		}
		info := &AppInfo{Permissions: m.Permissions}
		if missing := info.MissingForMigration(); len(missing) != 0 {
			t.Errorf("an App created for org=%q still cannot migrate: %v", org, missing)
		}
	}
}

func TestMissingForMigration(t *testing.T) {
	full := &AppInfo{Permissions: map[string]string{
		"contents": "write", "pull_requests": "write", "workflows": "write",
	}}
	if got := full.MissingForMigration(); len(got) != 0 {
		t.Fatalf("MissingForMigration = %v, want none", got)
	}

	partial := &AppInfo{Permissions: map[string]string{"contents": "read"}}
	got := partial.MissingForMigration()
	if len(got) != 3 {
		t.Fatalf("MissingForMigration = %v, want all three named", got)
	}
	if !strings.Contains(got[0], "only read") {
		t.Errorf("%q does not say what the permission currently is", got[0])
	}
	if !strings.Contains(got[1], "not granted") {
		t.Errorf("%q does not say the permission is absent", got[1])
	}
}

func TestBranchNameIsUniquePerRun(t *testing.T) {
	at := time.Date(2026, 3, 1, 9, 30, 0, 0, time.UTC)
	if got := BranchName(at); got != "zoomies/migrate-runners-20260301-093000" {
		t.Fatalf("BranchName = %q", got)
	}
	if BranchName(at) == BranchName(at.Add(time.Second)) {
		t.Error("two runs a second apart would collide on the same branch")
	}
}
