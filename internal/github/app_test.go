package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// testKey is generated once: 2048-bit RSA generation is the slowest thing in
// this package's tests.
var testKey = sync.OnceValue(func() []byte {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
})

func newFake(t *testing.T) *FakeGitHub {
	t.Helper()
	f := NewFake()
	t.Cleanup(f.Close)
	return f
}

// eachTarget runs fn for both target kinds, since every Actions endpoint has
// an org half and a repo half and only one of them is ever exercised by hand.
func eachTarget(t *testing.T, fn func(t *testing.T, f *FakeGitHub, c Client, target string, kind store.TargetType)) {
	t.Helper()
	cases := []struct {
		target string
		kind   store.TargetType
	}{
		{"acme", store.TargetOrg},
		{"acme/widgets", store.TargetRepo},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			f := newFake(t)
			fn(t, f, f.Client(tc.target, tc.kind), tc.target, tc.kind)
		})
	}
}

func TestClientTargetAndWebURL(t *testing.T) {
	eachTarget(t, func(t *testing.T, _ *FakeGitHub, c Client, target string, kind store.TargetType) {
		gotTarget, gotKind := c.Target()
		if gotTarget != target || gotKind != kind {
			t.Fatalf("Target() = %q/%q, want %q/%q", gotTarget, gotKind, target, kind)
		}
		if want := "https://github.com/" + target; c.WebURL() != want {
			t.Fatalf("WebURL() = %q, want %q", c.WebURL(), want)
		}
	})
}

func TestCreateJITConfig(t *testing.T) {
	eachTarget(t, func(t *testing.T, f *FakeGitHub, c Client, _ string, _ store.TargetType) {
		ctx := context.Background()
		cfg, err := c.CreateJITConfig(ctx, JITRequest{Name: "zoomies-linux-abcd", Labels: []string{"Linux", "gpu"}})
		if err != nil {
			t.Fatalf("CreateJITConfig: %v", err)
		}
		if cfg.Encoded == "" || cfg.RunnerID == 0 || cfg.Name != "zoomies-linux-abcd" {
			t.Fatalf("jit config = %+v", cfg)
		}

		runners := f.Runners()
		if len(runners) != 1 || runners[0].Name != "zoomies-linux-abcd" {
			t.Fatalf("fake runners = %+v", runners)
		}

		// GitHub rejects a reused runner name, and so must the fake: a pool
		// that mints colliding names has to fail loudly.
		_, err = c.CreateJITConfig(ctx, JITRequest{Name: "zoomies-linux-abcd", Labels: []string{"linux"}})
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("duplicate runner name accepted: %v", err)
		}
	})
}

func TestCreateJITConfigRequiresLabels(t *testing.T) {
	f := newFake(t)
	c := f.Client("acme", store.TargetOrg)
	_, err := c.CreateJITConfig(context.Background(), JITRequest{Name: "r1"})
	if err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Fatalf("got %v, want a message about labels", err)
	}
	if _, err := c.CreateJITConfig(context.Background(), JITRequest{Labels: []string{"linux"}}); err == nil {
		t.Fatal("unnamed runner accepted")
	}
}

func TestCreateJITConfigUnknownRunnerGroup(t *testing.T) {
	f := newFake(t)
	c := f.Client("acme", store.TargetOrg)
	_, err := c.CreateJITConfig(context.Background(), JITRequest{
		Name: "r1", Labels: []string{"linux"}, RunnerGroupID: 99,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestListAndDeleteRunners(t *testing.T) {
	eachTarget(t, func(t *testing.T, f *FakeGitHub, c Client, _ string, _ store.TargetType) {
		ctx := context.Background()
		orphan := f.AddRunner("registered-by-hand", []string{"linux"})

		runners, err := c.ListRunners(ctx)
		if err != nil {
			t.Fatalf("ListRunners: %v", err)
		}
		if len(runners) != 1 || runners[0].ID != orphan.ID {
			t.Fatalf("runners = %+v", runners)
		}
		if !slices.Contains(runners[0].Labels, "self-hosted") || runners[0].Status != "online" {
			t.Fatalf("runner detail lost: %+v", runners[0])
		}

		if err := c.DeleteRunner(ctx, orphan.ID); err != nil {
			t.Fatalf("DeleteRunner: %v", err)
		}
		if got := f.Runners(); len(got) != 0 {
			t.Fatalf("runner survived deletion: %+v", got)
		}

		// Deleting one GitHub has already forgotten is success: the desired
		// end state has been reached.
		if err := c.DeleteRunner(ctx, orphan.ID); err != nil {
			t.Fatalf("second DeleteRunner: %v, want nil for an already-gone runner", err)
		}
	})
}

func TestJITRunnerIsReportedEphemeral(t *testing.T) {
	f := newFake(t)
	c := f.Client("acme", store.TargetOrg)
	if _, err := c.CreateJITConfig(context.Background(), JITRequest{Name: "r1", Labels: []string{"linux"}}); err != nil {
		t.Fatalf("CreateJITConfig: %v", err)
	}
	runners, err := c.ListRunners(context.Background())
	if err != nil {
		t.Fatalf("ListRunners: %v", err)
	}
	if len(runners) != 1 || !runners[0].Ephemeral {
		t.Fatalf("JIT runner not reported ephemeral: %+v", runners)
	}
}

func TestRegistrationAndRemoveTokens(t *testing.T) {
	eachTarget(t, func(t *testing.T, _ *FakeGitHub, c Client, _ string, _ store.TargetType) {
		ctx := context.Background()
		reg, err := c.CreateRegistrationToken(ctx)
		if err != nil {
			t.Fatalf("CreateRegistrationToken: %v", err)
		}
		if reg.Token == "" || !reg.ExpiresAt.After(time.Now()) {
			t.Fatalf("registration token = %+v", reg)
		}
		rm, err := c.CreateRemoveToken(ctx)
		if err != nil {
			t.Fatalf("CreateRemoveToken: %v", err)
		}
		if rm.Token == "" {
			t.Fatalf("remove token = %+v", rm)
		}
	})
}

func TestListRunnerGroups(t *testing.T) {
	f := newFake(t)
	f.AddRunnerGroup("gpu")

	org, err := f.Client("acme", store.TargetOrg).ListRunnerGroups(context.Background())
	if err != nil {
		t.Fatalf("ListRunnerGroups: %v", err)
	}
	if len(org) != 2 || org[0].Name != "Default" || org[1].Name != "gpu" {
		t.Fatalf("org groups = %+v", org)
	}

	// Runner groups are an organisation concept; a repo target reports none
	// rather than failing, so the pool form can show an empty picker.
	repo, err := f.Client("acme/widgets", store.TargetRepo).ListRunnerGroups(context.Background())
	if err != nil || len(repo) != 0 {
		t.Fatalf("repo groups = %+v, err = %v", repo, err)
	}
}

func TestRateLimit(t *testing.T) {
	f := newFake(t)
	reset := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	f.SetRateLimit(5000, 4321, reset)

	rl, err := f.Client("acme", store.TargetOrg).RateLimit(context.Background())
	if err != nil {
		t.Fatalf("RateLimit: %v", err)
	}
	if rl.Limit != 5000 || rl.Remaining != 4321 || !rl.ResetAt.Equal(reset) {
		t.Fatalf("rate limit = %+v, want reset %v", rl, reset)
	}
}

func TestListQueuedJobs(t *testing.T) {
	eachTarget(t, func(t *testing.T, f *FakeGitHub, c Client, _ string, kind store.TargetType) {
		ctx := context.Background()
		queued := f.AddQueuedJob("acme/widgets", "CI", "build", []string{"self-hosted", "linux"})
		running := f.AddQueuedJob("acme/widgets", "CI", "test", []string{"self-hosted", "linux"})
		f.StartJob(running.ID, "zoomies-linux-abcd")
		done := f.AddQueuedJob("acme/widgets", "CI", "deploy", []string{"self-hosted", "linux"})
		f.CompleteJob(done.ID, "success")

		jobs, err := c.ListQueuedJobs(ctx)
		if err != nil {
			t.Fatalf("ListQueuedJobs: %v", err)
		}
		if len(jobs) != 1 {
			t.Fatalf("got %d jobs, want only the queued one: %+v", len(jobs), jobs)
		}
		j := jobs[0]
		if j.ID != queued.ID || j.RunID != queued.RunID || j.Repo != "acme/widgets" {
			t.Fatalf("job = %+v, want %+v", j, queued)
		}
		if j.WorkflowName != "CI" || j.JobName != "build" || j.HTMLURL == "" || j.QueuedAt.IsZero() {
			t.Fatalf("job detail lost: %+v", j)
		}
		if !slices.Equal(j.Labels, []string{"self-hosted", "linux"}) {
			t.Fatalf("labels = %v", j.Labels)
		}
	})
}

func TestListQueuedJobsOrgSpansRepositories(t *testing.T) {
	f := newFake(t)
	f.AddQueuedJob("acme/widgets", "CI", "build", []string{"linux"})
	f.AddQueuedJob("acme/gadgets", "Release", "package", []string{"linux"})
	// A repository the installation can see but which has queued nothing.
	f.AddRepo("acme/quiet")

	jobs, err := f.Client("acme", store.TargetOrg).ListQueuedJobs(context.Background())
	if err != nil {
		t.Fatalf("ListQueuedJobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs across the org, want 2: %+v", len(jobs), jobs)
	}
}

func TestListQueuedJobsRepoTargetIgnoresOtherRepos(t *testing.T) {
	f := newFake(t)
	f.AddQueuedJob("acme/widgets", "CI", "build", []string{"linux"})
	f.AddQueuedJob("acme/gadgets", "CI", "build", []string{"linux"})

	jobs, err := f.Client("acme/widgets", store.TargetRepo).ListQueuedJobs(context.Background())
	if err != nil {
		t.Fatalf("ListQueuedJobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Repo != "acme/widgets" {
		t.Fatalf("repo target leaked other repositories: %+v", jobs)
	}
	// A repo target must not need the installation-repositories endpoint.
	for _, req := range f.Requests() {
		if strings.Contains(req, "/installation/repositories") {
			t.Fatalf("repo target listed the installation's repositories: %v", f.Requests())
		}
	}
}

func TestListQueuedJobsIsBounded(t *testing.T) {
	f := newFake(t)
	for i := range maxPollRuns + 25 {
		f.AddQueuedJob("acme/widgets", "CI", fmt.Sprintf("build-%d", i), []string{"linux"})
	}
	jobs, err := f.Client("acme", store.TargetOrg).ListQueuedJobs(context.Background())
	if err != nil {
		t.Fatalf("ListQueuedJobs: %v", err)
	}
	if len(jobs) != maxPollRuns {
		t.Fatalf("got %d jobs, want the poll capped at %d", len(jobs), maxPollRuns)
	}
	// One repo listing + one run listing + one job listing per inspected run.
	if got := len(f.Requests()); got > maxPollRuns+4 {
		t.Fatalf("poll made %d requests, which is more than the bound allows", got)
	}
}

func TestListQueuedJobsSkipsRepositoriesWithoutActions(t *testing.T) {
	f := newFake(t)
	f.AddQueuedJob("acme/gadgets", "CI", "build", []string{"linux"})
	f.AddQueuedJob("acme/widgets", "CI", "build", []string{"linux"})
	// A repository with Actions disabled 404s; that is normal in a large org
	// and must not abort the sweep.
	f.SetError("/repos/acme/gadgets/actions/", http.StatusNotFound, "Not Found")

	jobs, err := f.Client("acme", store.TargetOrg).ListQueuedJobs(context.Background())
	if err != nil {
		t.Fatalf("ListQueuedJobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Repo != "acme/widgets" {
		t.Fatalf("jobs = %+v", jobs)
	}
}

func TestErrorClassification(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		message string
		setup   func(*FakeGitHub)
		want    error
		hints   []string
	}{
		{
			name:    "not found",
			status:  http.StatusNotFound,
			message: "Not Found",
			want:    ErrNotFound,
		},
		{
			name:    "forbidden names the permission",
			status:  http.StatusForbidden,
			message: "Resource not accessible by integration",
			want:    ErrForbidden,
			hints:   []string{"organization_self_hosted_runners", "workflow_job", "actions"},
		},
		{
			name:    "rate limited",
			status:  http.StatusForbidden,
			message: "API rate limit exceeded for installation",
			setup:   func(f *FakeGitHub) { f.SetRateLimit(5000, 0, time.Now().Add(time.Hour)) },
			want:    ErrRateLimited,
		},
		{
			name:    "secondary rate limit",
			status:  http.StatusTooManyRequests,
			message: "You have exceeded a secondary rate limit",
			want:    ErrRateLimited,
		},
		{
			name:    "bad credentials",
			status:  http.StatusUnauthorized,
			message: "Bad credentials",
			hints:   []string{"App ID or private key"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			if tc.setup != nil {
				tc.setup(f)
			}
			f.SetError("/actions/runners", tc.status, tc.message)

			_, err := f.Client("acme", store.TargetOrg).ListRunners(context.Background())
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			for _, hint := range tc.hints {
				if !strings.Contains(err.Error(), hint) {
					t.Errorf("error %q does not mention %q", err, hint)
				}
			}
		})
	}
}

func TestForbiddenOnRepoTargetNamesAdministration(t *testing.T) {
	f := newFake(t)
	f.SetError("/actions/runners", http.StatusForbidden, "Resource not accessible by integration")
	_, err := f.Client("acme/widgets", store.TargetRepo).ListRunners(context.Background())
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("got %v, want ErrForbidden", err)
	}
	if !strings.Contains(err.Error(), "administration") {
		t.Fatalf("repo 403 should name the Administration permission: %v", err)
	}
	if strings.Contains(err.Error(), "organization_self_hosted_runners") {
		t.Fatalf("repo 403 named an organisation permission: %v", err)
	}
}

func TestContextCancellationIsNotMisclassified(t *testing.T) {
	f := newFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := f.Client("acme", store.TargetOrg).ListRunners(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestProbe(t *testing.T) {
	f := newFake(t)
	info, err := f.Client("acme", store.TargetOrg).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.ID != f.AppID() || info.Slug != "zoomies-fake" || info.Owner != "acme" {
		t.Fatalf("app info = %+v", info)
	}
	if info.Permissions["organization_self_hosted_runners"] != "write" {
		t.Fatalf("permissions = %v", info.Permissions)
	}
	if !slices.Contains(info.Events, "workflow_job") {
		t.Fatalf("events = %v", info.Events)
	}
	if missing := info.MissingRequirements(store.TargetOrg); len(missing) != 0 {
		t.Fatalf("a correctly configured App reported %v", missing)
	}
}

func TestProbeReportsMissingPermission(t *testing.T) {
	f := newFake(t)
	f.SetPermissions(map[string]string{"metadata": "read"})
	f.SetEvents("push")

	info, err := f.Client("acme", store.TargetOrg).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	missing := strings.Join(info.MissingRequirements(store.TargetOrg), "; ")
	for _, want := range []string{"Self-hosted runners", "not granted", "Actions", "workflow_job"} {
		if !strings.Contains(missing, want) {
			t.Errorf("missing requirements %q does not mention %q", missing, want)
		}
	}
	if repo := info.MissingRequirements(store.TargetRepo); !strings.Contains(strings.Join(repo, ";"), "Administration") {
		t.Errorf("repo requirements = %v", repo)
	}
}

func TestProbeFailsWhenRunnersAreUnreadable(t *testing.T) {
	// Permissions that look right in /app but 403 in practice are the whole
	// reason Probe makes a real call.
	f := newFake(t)
	f.SetError("/actions/runners", http.StatusForbidden, "Resource not accessible by integration")
	_, err := f.Client("acme", store.TargetOrg).Probe(context.Background())
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("got %v, want ErrForbidden", err)
	}
}

// ---------------------------------------------------------------------------
// Factory
// ---------------------------------------------------------------------------

func installationFor(f *FakeGitHub, apiBase string, kind store.TargetType, target string) *store.Installation {
	return &store.Installation{
		ID:             "ins_test",
		AppID:          f.AppID(),
		InstallationID: f.InstallationID(),
		Target:         target,
		TargetType:     kind,
		APIBaseURL:     apiBase,
	}
}

func TestAppFactoryAgainstFake(t *testing.T) {
	f := newFake(t)
	factory := NewAppFactory(f.Server().Client())

	c, err := factory.For(context.Background(), installationFor(f, f.URL(), store.TargetOrg, "acme"), testKey())
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if _, err := c.Probe(context.Background()); err != nil {
		t.Fatalf("Probe through the factory: %v", err)
	}

	// A bare host is GitHub Enterprise Server, so every call must carry the
	// /api/v3 prefix and the installation token must be minted there too.
	var sawToken bool
	for _, req := range f.Requests() {
		if !strings.Contains(req, " /api/v3/") {
			t.Fatalf("request %q did not use the GHES api path", req)
		}
		if strings.Contains(req, "/access_tokens") {
			sawToken = true
		}
	}
	if !sawToken {
		t.Fatalf("no installation token was minted: %v", f.Requests())
	}
}

func TestAppFactoryValidation(t *testing.T) {
	f := newFake(t)
	factory := NewAppFactory(f.Server().Client())
	ctx := context.Background()

	tests := []struct {
		name string
		inst *store.Installation
		key  []byte
		want string
	}{
		{"nil installation", nil, testKey(), "no installation"},
		{
			"no app id",
			&store.Installation{ID: "ins_1", Target: "acme", TargetType: store.TargetOrg},
			testKey(), "app_id",
		},
		{
			"no key",
			installationFor(f, f.URL(), store.TargetOrg, "acme"),
			nil, "private key",
		},
		{
			"key is not a key",
			installationFor(f, f.URL(), store.TargetOrg, "acme"),
			[]byte("-----BEGIN RSA PRIVATE KEY-----\nnope\n-----END RSA PRIVATE KEY-----\n"),
			"PEM-encoded RSA key",
		},
		{
			"repo target without a repo",
			installationFor(f, f.URL(), store.TargetRepo, "acme"),
			testKey(), "owner/name",
		},
		{
			"unknown target type",
			&store.Installation{ID: "ins_1", AppID: 1, InstallationID: 2, Target: "acme", TargetType: "team"},
			testKey(), "target type",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := factory.For(ctx, tc.inst, tc.key)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestAppFactoryWebURLForEnterprise(t *testing.T) {
	f := newFake(t)
	factory := NewAppFactory(f.Server().Client())
	c, err := factory.For(context.Background(),
		installationFor(f, "https://ghes.example.com", store.TargetRepo, "acme/widgets"), testKey())
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if want := "https://ghes.example.com/acme/widgets"; c.WebURL() != want {
		t.Fatalf("WebURL = %q, want %q", c.WebURL(), want)
	}
}

func TestNormalizeAPIBaseURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "https://api.github.com/"},
		{"https://github.com", "https://api.github.com/"},
		{"https://api.github.com", "https://api.github.com/"},
		{"ghes.example.com", "https://ghes.example.com/api/v3/"},
		{"https://ghes.example.com", "https://ghes.example.com/api/v3/"},
		{"https://ghes.example.com/", "https://ghes.example.com/api/v3/"},
		{"https://ghes.example.com/api/v3", "https://ghes.example.com/api/v3/"},
		{"https://ghes.example.com/api/v3/", "https://ghes.example.com/api/v3/"},
	}
	for _, tc := range tests {
		got, err := NormalizeAPIBaseURL(tc.in)
		if err != nil {
			t.Fatalf("NormalizeAPIBaseURL(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("NormalizeAPIBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEnterpriseDetection(t *testing.T) {
	if IsEnterprise("https://api.github.com/") {
		t.Error("api.github.com reported as enterprise")
	}
	if !IsEnterprise("https://ghes.example.com/api/v3/") {
		t.Error("GHES not reported as enterprise")
	}
	if got := WebURLForAPI("https://api.github.com/"); got != "https://github.com" {
		t.Errorf("WebURLForAPI = %q", got)
	}
	if got := WebURLForAPI("https://ghes.example.com/api/v3/"); got != "https://ghes.example.com" {
		t.Errorf("WebURLForAPI = %q", got)
	}
}

func TestFakeServesBothAPILayouts(t *testing.T) {
	// The same fake has to answer github.com-shaped and GHES-shaped calls, or
	// the enterprise path would go untested everywhere else.
	f := newFake(t)
	for _, base := range []string{f.URL(), f.URL() + "/api/v3"} {
		inst := installationFor(f, base, store.TargetOrg, "acme")
		c, err := NewAppFactory(f.Server().Client()).For(context.Background(), inst, testKey())
		if err != nil {
			t.Fatalf("For(%q): %v", base, err)
		}
		if _, err := c.RateLimit(context.Background()); err != nil {
			t.Fatalf("RateLimit against %q: %v", base, err)
		}
	}
}
