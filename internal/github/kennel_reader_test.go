package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

func kennelReader(t *testing.T, f *FakeGitHub, target string, kind store.TargetType) RepoReader {
	t.Helper()
	r, ok := f.Client(target, kind).(RepoReader)
	if !ok {
		t.Fatal("the client does not implement RepoReader")
	}
	return r
}

// The whole exposure story rests on one comparison: where the run ran, and where
// its head commit is. A pull request from a fork has a head somewhere else.
func TestARunIsFromAForkWhenItsHeadIsInAnotherRepository(t *testing.T) {
	f := newFake(t)
	f.AddRepo("acme/api")
	f.AddRepo("stranger/api")
	base, fork := f.RepositoryID("acme/api"), f.RepositoryID("stranger/api")
	f.SetRunTrigger("acme/api", 1, "pull_request", fork)
	f.SetRunTrigger("acme/api", 2, "pull_request", base)
	f.SetRunTrigger("acme/api", 3, "pull_request", 0) // the fork has since been deleted
	f.SetRunTrigger("acme/api", 4, "pull_request_target", fork)
	r := kennelReader(t, f, "acme", store.TargetOrg)

	for _, tt := range []struct {
		run   int64
		event string
		fork  bool
	}{
		{1, "pull_request", true},
		{2, "pull_request", false},
		{3, "pull_request", true},
		{4, "pull_request_target", true},
	} {
		got, err := r.KennelRun(context.Background(), "acme/api", tt.run)
		if err != nil {
			t.Fatalf("run %d: %v", tt.run, err)
		}
		if got.Event != tt.event || got.FromFork() != tt.fork || got.RepositoryID != base || got.ID != tt.run {
			t.Errorf("run %d = %+v (fork %v), want %s, fork %v", tt.run, got, got.FromFork(), tt.event, tt.fork)
		}
	}
}

// A run nobody has described is a push to the repository itself, which is what
// most runs are, and is never a fork's.
func TestARunNobodyDescribedIsNotFromAFork(t *testing.T) {
	f := newFake(t)
	f.AddRepo("acme/api")
	job := f.AddQueuedJob("acme/api", "ci", "build", []string{"self-hosted"})
	got, err := kennelReader(t, f, "acme", store.TargetOrg).KennelRun(context.Background(), "acme/api", job.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FromFork() || got.Event != "push" {
		t.Errorf("run = %+v, want a push from the repository itself", got)
	}
}

func TestARunThatDoesNotExistIsNotFound(t *testing.T) {
	f := newFake(t)
	f.AddRepo("acme/api")
	_, err := kennelReader(t, f, "acme", store.TargetOrg).KennelRun(context.Background(), "acme/api", 404)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// Without Actions read the answer is a refusal, not an absence, and the
// controller reports the first as a permission to grant and skips the second.
func TestAnAppWithoutActionsReadIsRefusedNotTold404(t *testing.T) {
	f := newFake(t)
	f.AddRepo("acme/api")
	f.SetRunTrigger("acme/api", 1, "pull_request", 0)
	f.SetPermissions(map[string]string{"metadata": "read"})
	_, err := kennelReader(t, f, "acme", store.TargetOrg).KennelRun(context.Background(), "acme/api", 1)
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("err = %v, want ErrForbidden", err)
	}
	f.SetPermissions(map[string]string{"actions": "read"})
	if _, err := kennelReader(t, f, "acme", store.TargetOrg).KennelRun(context.Background(), "acme/api", 1); err != nil {
		t.Errorf("with Actions read: %v", err)
	}
}

// The reader must hand the controller the time to come back at, so that Kennel
// Club stands down for as long as GitHub asked and no longer.
func TestAQuotaRefusalArrivesWithItsResetTime(t *testing.T) {
	f := newFake(t)
	f.AddRepo("acme/api")
	f.SetRunTrigger("acme/api", 1, "push", f.RepositoryID("acme/api"))
	reset := time.Now().Add(7 * time.Minute).Truncate(time.Second)
	f.SetRateLimit(5000, 0, reset)
	f.SetError("/actions/runs/1", http.StatusForbidden, "API rate limit exceeded")
	_, err := kennelReader(t, f, "acme", store.TargetOrg).KennelRun(context.Background(), "acme/api", 1)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want a rate limit", err)
	}
	if at, ok := RetryAfterRateLimit(err, time.Now()); !ok || !at.Equal(reset.UTC()) {
		t.Errorf("resume at %v (%v), want %v", at, ok, reset.UTC())
	}
}

// An event name is attacker-influenced text on its way to the controller. It is
// bounded here, and the controller then maps it through an allow-list.
func TestAnEventNameIsBoundedBeforeItLeavesTheReader(t *testing.T) {
	f := newFake(t)
	f.AddRepo("acme/api")
	f.SetRunTrigger("acme/api", 1, strings.Repeat("x", 100000), f.RepositoryID("acme/api"))
	got, err := kennelReader(t, f, "acme", store.TargetOrg).KennelRun(context.Background(), "acme/api", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Event) != maxKennelEvent {
		t.Errorf("event is %d bytes, want it bounded to %d", len(got.Event), maxKennelEvent)
	}
}

func TestARunIsNotReadForARepositoryThatIsNotOneOrAnIDThatIsNotPositive(t *testing.T) {
	f := newFake(t)
	r := kennelReader(t, f, "acme", store.TargetOrg)
	for _, tt := range []struct {
		repo string
		id   int64
	}{{"acme", 1}, {"", 1}, {"acme/api", 0}, {"acme/api", -3}} {
		if _, err := r.KennelRun(context.Background(), tt.repo, tt.id); err == nil {
			t.Errorf("KennelRun(%q, %d) succeeded", tt.repo, tt.id)
		}
	}
	if reqs := f.Requests(); len(reqs) != 0 {
		t.Errorf("a malformed request still reached GitHub: %v", reqs)
	}
}

// If GitHub does not say which repository a run ran in, it cannot be compared
// with its head, and calling it a fork on that evidence would be inventing one.
func TestARunWithNoRepositoryIsAnErrorNotAFork(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":1,"event":"pull_request"}`))
	}))
	defer srv.Close()
	c, err := newGitHubClient(srv.Client(), srv.URL+"/", srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	r := newAppClient(c, c, "acme", store.TargetOrg, 1, "https://github.com")
	if _, err := r.KennelRun(context.Background(), "acme/api", 1); err == nil || !strings.Contains(err.Error(), "did not say which repository") {
		t.Errorf("err = %v, want one saying GitHub did not name the repository", err)
	}
}

func TestTheListingSaysWhichRepositoriesArePublicPrivateAndInternal(t *testing.T) {
	f := newFake(t)
	f.SetVisibility("acme/open", "public")
	f.SetVisibility("acme/shut", "private")
	f.SetVisibility("acme/inside", "internal")
	f.AddRepo("acme/unsaid") // the fake's default, which is private
	got, err := kennelReader(t, f, "acme", store.TargetOrg).KennelRepositories(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"acme/open": "public", "acme/shut": "private", "acme/inside": "internal", "acme/unsaid": "private"}
	if len(got) != len(want) {
		t.Fatalf("listed %d repositories, want %d: %+v", len(got), len(want), got)
	}
	for _, r := range got {
		if r.Visibility != want[r.FullName] {
			t.Errorf("%s is %q, want %q", r.FullName, r.Visibility, want[r.FullName])
		}
		if r.Private != (want[r.FullName] != "public") {
			t.Errorf("%s: Private = %v disagrees with its visibility", r.FullName, r.Private)
		}
		if r.ID == 0 {
			t.Errorf("%s has no ID: it is the key Kennel Club keeps a repository by", r.FullName)
		}
	}
}

func TestARepositoryInstallationReadsItsOneRepositoryWithItsVisibility(t *testing.T) {
	f := newFake(t)
	f.SetVisibility("acme/api", "public")
	got, err := kennelReader(t, f, "acme/api", store.TargetRepo).KennelRepositories(context.Background(), 0)
	if err != nil || len(got) != 1 || got[0].Visibility != "public" || got[0].FullName != "acme/api" {
		t.Errorf("got %+v, %v", got, err)
	}
}

// An older GitHub Enterprise Server sends no visibility field. Reading it as
// empty would make a public repository invisible to every exposure check, which
// is the wrong direction to fail in, so it is worked out from private.
func TestAnOlderServerThatSendsNoVisibilityIsReadFromPrivate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"total_count":2,"repositories":[
			{"id":1,"full_name":"acme/open","private":false},
			{"id":2,"full_name":"acme/shut","private":true}]}`))
	}))
	defer srv.Close()
	c, err := newGitHubClient(srv.Client(), srv.URL+"/", srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	got, err := newAppClient(c, c, "acme", store.TargetOrg, 1, "https://github.com").KennelRepositories(context.Background(), 0)
	if err != nil || len(got) != 2 || got[0].Visibility != "public" || got[1].Visibility != "private" {
		t.Errorf("got %+v, %v", got, err)
	}
}

// The listing is the one thing Kennel Club asks of GitHub every refresh, and
// almost nothing changes between refreshes. The second read must be a 304, and
// the migration wizard's own reading of the same listing must not become one.
func TestTheRepositoryListingIsReadConditionallyForKennelClubAndNoOneElse(t *testing.T) {
	f := newFake(t)
	f.SetVisibility("acme/open", "public")
	c := f.Client("acme", store.TargetOrg)
	reader := c.(RepoReader)
	ctx := context.Background()

	first, err := reader.KennelRepositories(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := reader.KennelRepositories(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if f.NotModified() != 1 {
		t.Errorf("304s = %d after two Kennel Club reads, want 1: the second must have been conditional", f.NotModified())
	}
	if len(first) != 1 || len(second) != 1 || first[0] != second[0] {
		t.Errorf("a revalidated read gave a different answer: %+v then %+v", first, second)
	}

	for i := 0; i < 2; i++ {
		if _, err := c.ListRepositories(ctx, 0); err != nil {
			t.Fatal(err)
		}
	}
	if f.NotModified() != 1 {
		t.Errorf("304s = %d: the plain listing was made conditional, and the migration wizard and poller read as they always did", f.NotModified())
	}
}

// A repository that goes public must show up on the next read, not be hidden
// behind a remembered answer.
func TestARepositoryThatGoesPublicIsSeenOnTheNextRead(t *testing.T) {
	f := newFake(t)
	f.SetVisibility("acme/api", "private")
	r := kennelReader(t, f, "acme", store.TargetOrg)
	before, _ := r.KennelRepositories(context.Background(), 0)
	f.SetVisibility("acme/api", "public")
	after, err := r.KennelRepositories(context.Background(), 0)
	if err != nil || before[0].Visibility != "private" || after[0].Visibility != "public" {
		t.Errorf("before %+v, after %+v, %v", before, after, err)
	}
}

var idSegment = regexp.MustCompile(`/\d+`)

// normaliseKennelRequest turns a request the fake recorded into the form
// KennelEndpoints lists it in. It works by what a path is, not by where a
// segment falls, because the recorded path is the decoded one: a branch named
// release/1.0 is two segments there, and a fixed index would mistake the
// settings endpoints for runs.
func normaliseKennelRequest(req string) string {
	method, path, _ := strings.Cut(req, " ")
	path, _, _ = strings.Cut(path, "?")
	path = strings.TrimPrefix(path, "/api/v3")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case len(parts) >= 3 && parts[0] == "repos":
		rest := parts[3:]
		switch {
		case len(rest) >= 3 && rest[0] == "actions" && rest[1] == "runs":
			rest = []string{"actions", "runs", "{run}"}
		case len(rest) >= 3 && rest[0] == "git" && (rest[1] == "trees" || rest[1] == "blobs"):
			rest = []string{"git", rest[1], "{" + strings.TrimSuffix(rest[1], "s") + "}"}
		case len(rest) >= 3 && rest[0] == "branches" && rest[len(rest)-1] == "protection":
			rest = []string{"branches", "{branch}", "protection"}
		case len(rest) >= 3 && rest[0] == "rules" && rest[1] == "branches":
			rest = []string{"rules", "branches", "{branch}"}
		}
		parts = append([]string{"repos", "{owner}", "{repo}"}, rest...)
	case len(parts) >= 2 && parts[0] == "orgs":
		parts[1] = "{org}"
	}
	norm := method + " /" + strings.Join(parts, "/")
	return idSegment.ReplaceAllString(norm, "/{n}")
}

// Kennel Club calls the endpoints it documents and no others, and none of them
// is a write. A call added later has to change KennelEndpoints in the same
// change, which is where a reviewer, and the operator who reads the permissions
// page, will see it.
func TestKennelClubCallsOnlyTheEndpointsItDocuments(t *testing.T) {
	f := newFake(t)
	f.SetVisibility("acme/api", "public")
	f.SetVisibility("acme/internal", "private")
	f.SetRunTrigger("acme/api", 5, "pull_request", f.RepositoryID("acme/api")+1)
	f.AddRunnerGroup("fleet")
	// A branch with a slash, protected and ruled, so the normaliser is held to
	// the shape that breaks a fixed index.
	f.SetBranchProtection("acme/api", "release/1.0", actions("test")...)
	f.AddBranchRule("acme/api", "release/1.0", actions("lint")...)
	ctx := context.Background()
	for _, tc := range []struct {
		target string
		kind   store.TargetType
	}{{"acme", store.TargetOrg}, {"acme/api", store.TargetRepo}} {
		c := f.Client(tc.target, tc.kind)
		r := c.(RepoReader)
		if _, err := r.KennelRepositories(ctx, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := r.KennelRun(ctx, "acme/api", 5); err != nil {
			t.Fatal(err)
		}
		// Each repository makes the fork-policy read that is its own.
		sr := c.(KennelSettingsReader)
		if _, err := sr.KennelSettings(ctx, "acme/api", true); err != nil {
			t.Fatal(err)
		}
		if _, err := sr.KennelSettings(ctx, "acme/internal", false); err != nil {
			t.Fatal(err)
		}
		if _, err := sr.KennelProtection(ctx, "acme/api", "release/1.0"); err != nil {
			t.Fatal(err)
		}
		if tc.kind == store.TargetOrg {
			if _, err := c.ListRunnerGroups(ctx); err != nil {
				t.Fatal(err)
			}
		}
		// The budget is a share of what the installation reports, and this is how
		// it reports it.
		if _, err := c.RateLimit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	allowed := map[string]bool{}
	for _, e := range KennelEndpoints {
		allowed[e] = true
	}
	seen := map[string]bool{}
	for _, req := range f.Requests() {
		norm := normaliseKennelRequest(req)
		if !allowed[norm] {
			t.Errorf("Kennel Club made a request it does not document: %s (normalised %s)", req, norm)
		}
		if !strings.HasPrefix(req, "GET ") {
			t.Errorf("Kennel Club made a request that is not a GET: %s", req)
		}
		seen[norm] = true
	}
	if len(seen) == 0 {
		t.Fatal("no requests were recorded; the test proves nothing")
	}
	// An endpoint listed and never reached is a list that has drifted from the
	// code, and it would widen what the operator is told without anything using
	// it. The tree and blob reads are reached by the workflow and setup tests.
	for _, e := range KennelEndpoints {
		if strings.Contains(e, "/git/") {
			continue
		}
		if !seen[e] {
			t.Errorf("%s is documented and this test never reached it", e)
		}
	}
}
