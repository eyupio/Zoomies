package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/aicontext"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

type uploadFixture struct {
	h      *harness
	issuer *github.FakeActionsIssuer
	// dotcom is GitHub.com's issuer; on a GitHub.com fixture it is issuer.
	dotcom   *github.FakeActionsIssuer
	repo     store.AIContextRepository
	commit   string
	snapshot []byte
	cookie   string
}

const (
	uploadBase    = "https://zoomies.example.com"
	uploadBigFile = "assets/bundle.js"
)

// zoomiesOnlyFixture sets a repository up for Zoomies-only output the way an
// owner would -- draft, reviewed setup PR, merge -- and builds the snapshot
// its workflow would upload for the current head of the trusted branch.
func zoomiesOnlyFixture(t *testing.T) *uploadFixture {
	t.Helper()
	return zoomiesOnlyFixtureOn(t, "github.com")
}

// zoomiesOnlyFixtureOn is zoomiesOnlyFixture for a repository on host, whose
// Actions tokens come from an issuer of its own unless host is GitHub.com.
func zoomiesOnlyFixtureOn(t *testing.T, host string) *uploadFixture {
	t.Helper()
	h, inst, _ := migrationHarness(t)
	h.cfg.Server.ExternalURL = uploadBase
	dotcom, issuer := github.NewFakeActionsIssuer(t), github.NewFakeActionsIssuer(t)
	if host == "github.com" {
		issuer = dotcom
	}
	h.ctrl.SetActionsIssuerFor(func(h string) string {
		switch h {
		case "github.com":
			return dotcom.URL
		case host:
			return issuer.URL
		}
		return github.ActionsIssuerFor(h)
	})
	reader, cookie := h.user("upload-reader", store.RoleViewer)

	discovery, err := h.ctrl.DiscoverAIContext(h.ctx, inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	selected := discovery.Repositories[0]
	draft := store.AIContextRepository{Key: aicontext.RepositoryKey{GitHubHost: host, InstallationID: inst.ID, RepositoryID: selected.ID}, FullName: selected.FullName, Config: aicontext.DefaultConfig(selected.DefaultBranch)}
	draft.Config.Destination, draft.Config.UploadURL = aicontext.Zoomies, aicontext.UploadURLFor(uploadBase)
	if err := h.st.CreateAIContextRepository(h.ctx, &draft); err != nil {
		t.Fatal(err)
	}
	if err := h.st.ReplaceAIContextMembers(h.ctx, draft.ID, []string{reader.ID}); err != nil {
		t.Fatal(err)
	}
	plan, err := h.ctrl.PreviewAIContextSetup(h.ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = h.ctrl.CreateAIContextSetupPR(h.ctx, draft.ID, controller.AIContextSetupApproval{Revision: plan.Revision, PlanHash: plan.PlanHash})
	if err != nil {
		t.Fatal(err)
	}
	if !h.gh.MergeContextPull(draft.FullName, plan.Setup.PRNumber) {
		t.Fatal("merge failed")
	}
	h.gh.AddFile(draft.FullName, "src/main.go", "package widgets\n")
	// A file no snapshot can carry, for the tests of what an upload may omit.
	h.gh.AddFile(draft.FullName, uploadBigFile, strings.Repeat("a", aicontext.MaxFileBytes+7))
	client, err := h.ctrl.ClientFor(h.ctx, inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := client.(github.ContextSetupClient).ReadContextSetup(h.ctx, draft.FullName, draft.Config.SourceBranch)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := h.st.GetAIContextRepository(h.ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := stored.Config.Hash()
	snapshot := aicontext.Snapshot{Manifest: aicontext.Manifest{SchemaVersion: 1, Repository: draft.Key, SourceBranch: draft.Config.SourceBranch, SourceCommit: source.Commit, ConfigHash: hash, GeneratedAt: time.Now().UTC(), Manager: "zoomies", Generator: "repomix@1.18.1"}, Files: []aicontext.File{{Path: "src/main.go", Content: "package widgets\n", SHA256: aicontext.Hash([]byte("package widgets\n"))}}}
	body, _ := json.Marshal(snapshot)
	return &uploadFixture{h: h, issuer: issuer, dotcom: dotcom, repo: *stored, commit: source.Commit, snapshot: body, cookie: cookie}
}

// claims are what GitHub would put in the token of the managed workflow's
// run on the trusted branch; each test changes one of them.
func (f *uploadFixture) claims() map[string]any {
	return map[string]any{
		"aud":              aicontext.UploadURLFor(uploadBase),
		"repository":       f.repo.FullName,
		"repository_id":    jsonNumber(f.repo.Key.RepositoryID),
		"ref":              "refs/heads/" + f.repo.Config.SourceBranch,
		"sha":              f.commit,
		"event_name":       "push",
		"job_workflow_ref": f.repo.FullName + "/.github/workflows/zoomies-ai-context.yml@refs/heads/" + f.repo.Config.SourceBranch,
		"jti":              "jti-" + time.Now().Format(time.RFC3339Nano),
	}
}

func jsonNumber(n int64) string { b, _ := json.Marshal(n); return string(b) }

func (f *uploadFixture) upload(claims map[string]any, body []byte) *response {
	return f.uploadSignedBy(f.issuer, claims, body)
}

func (f *uploadFixture) uploadSignedBy(issuer *github.FakeActionsIssuer, claims map[string]any, body []byte) *response {
	return f.h.do(request{method: http.MethodPost, path: "/api/v1/ai-context/uploads", rawBody: string(body),
		token: issuer.Sign(f.h.t, claims)})
}

func TestAZoomiesOnlyUploadIsVerifiedAgainstGitHubBeforeAnyoneCanReadIt(t *testing.T) {
	f := zoomiesOnlyFixture(t)
	read := "/api/v1/ai-context/source/" + f.repo.ID + "/read?path=src/main.go"

	// Before any upload, there is nothing to read: Zoomies-only output has no
	// branch to fall back on.
	if r := f.h.do(request{method: http.MethodGet, path: read, cookie: f.cookie}); r.status == http.StatusOK {
		t.Fatal("source was served before anything was uploaded")
	}

	claims := f.claims()
	f.upload(claims, f.snapshot).mustStatus(t, http.StatusAccepted, "the managed workflow's upload")
	r := f.h.do(request{method: http.MethodGet, path: read, cookie: f.cookie})
	r.mustStatus(t, http.StatusOK, "reading the uploaded context")
	if !strings.Contains(string(r.body), "package widgets") {
		t.Fatal("uploaded source missing")
	}

	// The same token again is a replay, whatever it carries.
	f.upload(claims, f.snapshot).mustStatus(t, http.StatusConflict, "a replayed token")
}

func TestUploadsFromAnythingButTheManagedWorkflowOnItsTrustedBranchAreRefused(t *testing.T) {
	f := zoomiesOnlyFixture(t)
	for name, change := range map[string]struct {
		claim  string
		value  any
		status int
	}{
		"a pull request run":            {"event_name", "pull_request", http.StatusForbidden},
		"another branch":                {"ref", "refs/heads/feature", http.StatusForbidden},
		"another workflow file":         {"job_workflow_ref", f.repo.FullName + "/.github/workflows/ci.yml@refs/heads/" + f.repo.Config.SourceBranch, http.StatusForbidden},
		"a reusable workflow elsewhere": {"job_workflow_ref", "evil/repo/.github/workflows/zoomies-ai-context.yml@refs/heads/" + f.repo.Config.SourceBranch, http.StatusForbidden},
		"another repository":            {"repository_id", "999999", http.StatusNotFound},
		"another controller's audience": {"aud", "https://elsewhere.example.com" + aicontext.UploadPath, http.StatusUnauthorized},
		"a commit that is not the head": {"sha", strings.Repeat("d", 40), http.StatusForbidden},
	} {
		claims := f.claims()
		claims[change.claim] = change.value
		f.upload(claims, f.snapshot).mustStatus(t, change.status, name)
	}

	// A self-consistent snapshot whose file is not the blob in that commit.
	var forged aicontext.Snapshot
	if err := json.Unmarshal(f.snapshot, &forged); err != nil {
		t.Fatal(err)
	}
	forged.Files[0].Content = "package widgets // injected\n"
	forged.Files[0].SHA256 = aicontext.Hash([]byte(forged.Files[0].Content))
	body, _ := json.Marshal(forged)
	f.upload(f.claims(), body).mustStatus(t, http.StatusForbidden, "content that is not in the trusted commit")

	if r := f.h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/source/" + f.repo.ID + "/read?path=src/main.go", cookie: f.cookie}); r.status == http.StatusOK {
		t.Fatal("a refused upload became readable source")
	}
}

// A controller that cannot reach GitHub's signing keys says so as a 503 with
// the host to allow, not a 401 that would send the operator to the workflow.
func TestAnUploadTheControllerCannotCheckIsUnavailableNotUnauthorised(t *testing.T) {
	f := zoomiesOnlyFixture(t)
	f.h.ctrl.SetActionsIssuer("http://127.0.0.1:1")
	claims := f.claims()
	claims["iss"] = "http://127.0.0.1:1"
	r := f.upload(claims, f.snapshot)
	r.mustStatus(t, http.StatusServiceUnavailable, "an upload whose token could not be checked")
	if !strings.Contains(string(r.body), "127.0.0.1:1") {
		t.Fatalf("the refusal does not name the host to allow: %s", r.body)
	}
}

func TestAnEnterpriseServerUploadIsCheckedAgainstThatServersOwnKeys(t *testing.T) {
	f := zoomiesOnlyFixtureOn(t, "ghes.example.org")
	if workflow, _ := f.h.gh.FileContent(f.repo.FullName, aicontext.WorkflowPath); !strings.Contains(workflow, "upload-artifact@c6a366c94c3e0affe28c06c8df20a878f24da3cf") {
		t.Fatal("the merged setup did not use the Enterprise Server workflow")
	}
	// GitHub.com's issuer vouches for GitHub.com's repositories. A token it
	// signed with this repository's numeric ID is about some other repository
	// that happens to share it, and must not reach this one.
	f.uploadSignedBy(f.dotcom, f.claims(), f.snapshot).mustStatus(t, http.StatusNotFound, "a GitHub.com token for an Enterprise Server repository")
	f.upload(f.claims(), f.snapshot).mustStatus(t, http.StatusAccepted, "the Enterprise Server workflow's upload")
	r := f.h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/source/" + f.repo.ID + "/read?path=src/main.go", cookie: f.cookie})
	r.mustStatus(t, http.StatusOK, "reading the uploaded context")
}

func TestATokenFromAnIssuerZoomiesDoesNotKnowIsNeverFetched(t *testing.T) {
	f := zoomiesOnlyFixture(t)
	// A stranger can sign a token naming any issuer it likes. Following it
	// would let anyone make the controller fetch keys from an address of
	// their choosing, so only known issuers are ever contacted.
	fetched := false
	stranger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fetched = true }))
	t.Cleanup(stranger.Close)
	claims := f.claims()
	claims["iss"] = stranger.URL
	f.upload(claims, f.snapshot).mustStatus(t, http.StatusUnauthorized, "a token from an unknown issuer")
	claims["iss"] = github.ActionsIssuerFor("unknown.example.org")
	f.upload(claims, f.snapshot).mustStatus(t, http.StatusUnauthorized, "a token from an Enterprise Server with no repository here")
	if fetched {
		t.Fatal("the controller fetched keys from an issuer the token named")
	}
}

// omittingSnapshot is the fixture's snapshot with the oversized file listed
// rather than carried, as the generator now writes it.
func (f *uploadFixture) omittingSnapshot(t *testing.T, edit func(*aicontext.Omitted)) []byte {
	t.Helper()
	var snapshot aicontext.Snapshot
	if err := json.Unmarshal(f.snapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Omitted = []aicontext.Omitted{{Path: uploadBigFile, Bytes: aicontext.MaxFileBytes + 7, Reason: aicontext.OmittedTooLarge}}
	if edit != nil {
		edit(&snapshot.Omitted[0])
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestAnUploadThatOmitsAFileTellsEveryReaderWhatIsMissing(t *testing.T) {
	f := zoomiesOnlyFixture(t)
	f.upload(f.claims(), f.omittingSnapshot(t, nil)).mustStatus(t, http.StatusAccepted, "an upload that lists an oversized file")
	source := "/api/v1/ai-context/source/" + f.repo.ID

	r := f.h.do(request{method: http.MethodGet, path: source + "/overview", cookie: f.cookie})
	r.mustStatus(t, http.StatusOK, "the overview")
	var overview struct {
		Total        int `json:"total"`
		OmittedTotal int `json:"omitted_total"`
		Files        []struct {
			Path    string `json:"path"`
			Bytes   int    `json:"bytes"`
			Omitted string `json:"omitted"`
		} `json:"files"`
	}
	if err := json.Unmarshal(r.body, &overview); err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, file := range overview.Files {
		if file.Path == uploadBigFile {
			listed = file.Omitted == aicontext.OmittedTooLarge && file.Bytes == aicontext.MaxFileBytes+7
		}
	}
	if !listed || overview.OmittedTotal != 1 || overview.Total != 2 {
		t.Fatalf("the overview does not show the omitted file: %s", r.body)
	}

	r = f.h.do(request{method: http.MethodGet, path: source + "/read?path=" + uploadBigFile, cookie: f.cookie})
	r.mustStatus(t, http.StatusOK, "reading an omitted file")
	if !strings.Contains(string(r.body), `"omitted":"too_large"`) || !strings.Contains(string(r.body), `"text":""`) || strings.Contains(string(r.body), "aaaa") {
		t.Fatalf("a read of an omitted file should say why there is no text: %s", r.body)
	}

	r = f.h.do(request{method: http.MethodGet, path: source + "/search?query=widgets", cookie: f.cookie})
	r.mustStatus(t, http.StatusOK, "a search")
	if !strings.Contains(string(r.body), `"omitted_total":1`) {
		t.Fatalf("a search does not say the context is incomplete: %s", r.body)
	}
}

func TestAnUploadCannotListFilesTheTrustedCommitDoesNotHave(t *testing.T) {
	f := zoomiesOnlyFixture(t)
	for name, edit := range map[string]func(*aicontext.Omitted){
		"a file that is not in the commit":        func(o *aicontext.Omitted) { o.Path = "assets/never-existed.js" },
		"a size that is not the file's size":      func(o *aicontext.Omitted) { o.Bytes = aicontext.MaxFileBytes + 99 },
		"a carried file claimed to be omitted":    func(o *aicontext.Omitted) { o.Path, o.Bytes = "src/main.go", 16 },
		"a file under the limit labelled as such": func(o *aicontext.Omitted) { o.Bytes = 10 },
	} {
		f.upload(f.claims(), f.omittingSnapshot(t, edit)).mustNotSucceed(t, name)
	}
	if r := f.h.do(request{method: http.MethodGet, path: "/api/v1/ai-context/source/" + f.repo.ID + "/overview", cookie: f.cookie}); r.status == http.StatusOK {
		t.Fatal("a refused upload became readable context")
	}
}

func (r *response) mustNotSucceed(t *testing.T, what string) {
	t.Helper()
	if r.status >= 200 && r.status < 300 {
		t.Fatalf("%s was accepted (status %d): %s", what, r.status, truncate(r.body))
	}
}
