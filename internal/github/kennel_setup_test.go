package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
	gh "github.com/google/go-github/v88/github"
)

func TestSetupInventoryRecognisesUsefulFilesWithoutReadingTheirContents(t *testing.T) {
	f := newFake(t)
	files := []string{"docs/README.rst", "LICENSE.txt", ".github/SECURITY.md", "CONTRIBUTING.md", ".github/CODE_OF_CONDUCT.md", ".github/ISSUE_TEMPLATE/bug.yml", ".github/PULL_REQUEST_TEMPLATE/change.md", "docs/CODEOWNERS", ".github/dependabot.yml", ".github/workflows/test.yml", "go.mod"}
	for _, p := range files {
		f.AddFile("acme/api", p, "untrusted contents")
	}
	got, err := f.Client("acme", store.TargetOrg).(KennelSetupReader).KennelSetup(context.Background(), "acme/api", "main")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"readme", "licence", "security", "contributing", "code_of_conduct", "issue_template", "pull_request_template", "codeowners", "dependency_updates", "workflows"} {
		if !got.Present[key] {
			t.Errorf("did not recognise %s", key)
		}
	}
	if !got.HasDependencies || got.Partial {
		t.Errorf("inventory = %+v", got)
	}
	for _, req := range f.Requests() {
		if req != "GET /repos/acme/api/git/trees/main" {
			t.Errorf("unexpected request: %s", req)
		}
	}
}

func TestSetupInventoryDoesNotCountEmptyMisplacedOrSymlinkFiles(t *testing.T) {
	f := newFake(t)
	for _, p := range []string{"examples/README.md", "docs/LICENSE.txt", ".github/codeowners", ".github/ISSUE_TEMPLATE/config.yml", "examples/package.json", ".github/workflows/test.YML"} {
		f.AddFile("acme/api", p, "x")
	}
	// A licence must ship at the root; a symlink is not a local policy file.
	f.AddFile("acme/api", "README.md", "")
	f.AddFile("acme/api", "SECURITY.md", "elsewhere")
	f.SetFileMode("acme/api", "SECURITY.md", "120000")
	got, err := f.Client("acme", store.TargetOrg).(KennelSetupReader).KennelSetup(context.Background(), "acme/api", "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Present) != 0 || got.HasDependencies {
		t.Errorf("invented setup from unsupported files: %+v", got)
	}
}

func TestSetupInventoryReportsAHiddenTreeAsUnavailableNotMissingFiles(t *testing.T) {
	f := newFake(t)
	f.AddFile("acme/api", "README.md", "x")
	f.SetPermissions(map[string]string{"metadata": "read"})
	got, err := f.Client("acme", store.TargetOrg).(KennelSetupReader).KennelSetup(context.Background(), "acme/api", "main")
	if !errors.Is(err, ErrNotFound) || got != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestSetupInventoryKeepsTruncationAndInspectionLimitsVisible(t *testing.T) {
	for _, size := range []int{1, 10001} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			entries := make([]*gh.TreeEntry, size)
			for i := range entries {
				entries[i] = &gh.TreeEntry{Path: gh.Ptr("README.md"), Type: gh.Ptr("blob"), Mode: gh.Ptr("100644"), Size: gh.Ptr(1)}
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/acme/api/git/trees/trunk" {
					t.Errorf("path = %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(&gh.Tree{SHA: gh.Ptr("tree"), Entries: entries, Truncated: gh.Ptr(size == 1)})
			}))
			defer srv.Close()
			gc, err := newGitHubClient(srv.Client(), srv.URL+"/", srv.URL+"/")
			if err != nil {
				t.Fatal(err)
			}
			got, err := newAppClient(gc, gc, "acme", store.TargetOrg, 1, "https://github.com").KennelSetup(context.Background(), "acme/api", "trunk")
			if err != nil || !got.Partial {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}
