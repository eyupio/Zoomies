package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/version"
)

// roundTripFunc answers every request with one canned response, which is enough
// for a checker that only ever asks GitHub one question.
type roundTripFunc func(*http.Request) *http.Response

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r), nil }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

// withVersion stamps a version for the duration of one test, because the whole
// question this feature answers is "what was this binary built from".
func withVersion(t *testing.T, v string) {
	t.Helper()
	prev := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = prev })
}

// githubStub stands in for api.github.com. What it answers can be changed
// between two checks, and it remembers every address it was asked for.
type githubStub struct {
	status  int
	body    string
	asked   []string
	headers []http.Header
}

// stubGitHub points the controller's HTTP client at a githubStub, which is how
// a test sees what a check asked for without a network.
func (h *harness) stubGitHub(status int, body string) *githubStub {
	h.t.Helper()
	s := &githubStub{status: status, body: body}
	h.c.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
		s.asked = append(s.asked, r.URL.String())
		s.headers = append(s.headers, r.Header.Clone())
		return jsonResponse(s.status, s.body)
	})}
	return s
}

// unreachable is a network with no route to GitHub, which is what a controller
// in an air-gapped fleet that forgot to switch the check off sees.
type unreachable struct{}

func (unreachable) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp: no route to host")
}

// inMode sets updates.mode on the running configuration, as the Settings page
// does.
func (h *harness) inMode(mode string) {
	h.t.Helper()
	h.c.live.Update(func(c *config.Config) { c.Updates.Mode = mode })
}

// completeAssets is what a release carries to be installable on the system the
// tests run on, which is the one the controller asks Newest about. It is built
// from runtime rather than written out so that the same fixtures hold on an
// arm64 runner.
func completeAssets(t *testing.T) []string {
	t.Helper()
	binary := updates.AssetName(runtime.GOOS, runtime.GOARCH)
	if binary == "" {
		t.Skipf("no release is published for %s/%s, so none is complete for it", runtime.GOOS, runtime.GOARCH)
	}
	return []string{"checksums.txt", binary}
}

// releaseEntry is one entry of GitHub's release list. A release published at
// the zero time is given the null GitHub gives one that is not published yet.
func releaseEntry(tag string, published time.Time, assets ...string) map[string]any {
	files := make([]map[string]any, 0, len(assets))
	for _, name := range assets {
		files = append(files, map[string]any{"name": name})
	}
	var publishedAt any
	if !published.IsZero() {
		publishedAt = published.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"tag_name":     tag,
		"html_url":     "https://example.invalid/releases/" + tag,
		"published_at": publishedAt,
		"prerelease":   false,
		"draft":        false,
		"assets":       files,
	}
}

// releaseList is GitHub's answer to the release list request.
func releaseList(entries ...map[string]any) string {
	if entries == nil {
		entries = []map[string]any{}
	}
	b, err := json.Marshal(entries)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// The distinction the honesty of this feature rests on: a controller built from
// main is stamped with a commit and is normally *ahead* of the newest release,
// so it has nothing to compare and must say nothing.
func TestReleaseVersionAcceptsOnlyABuildFromARelease(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"0.2-beta", "0.2-beta", true},
		{"v0.2-beta", "0.2-beta", true},
		{"1.4.0", "1.4.0", true},
		{"v1.4.0", "1.4.0", true},
		// What ci.yml stamps on an image built from main.
		{"main-sha-abc1234", "", false},
		// What the Makefile's git describe gives once commits land on a tag.
		{"v0.2-beta-5-gabc1234", "", false},
		{"v0.2-beta-5-gabc1234-dirty", "", false},
		{"dev", "", false},
		{"", "", false},
	} {
		got, ok := releaseVersion(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("releaseVersion(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestDevelopmentCommitAcceptsOnlyPublishedMainBuilds(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"main-sha-abc1234", "abc1234", true},
		{"main-sha-0123456789abcdef", "0123456789abcdef", true},
		{"main-sha-short", "", false},
		{"dev", "", false},
		{"1.0.0", "", false},
	} {
		got, ok := developmentCommit(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("developmentCommit(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestUpdateProblemSaysNothingWithoutAReleaseToCompare(t *testing.T) {
	h := newHarness(t)

	t.Run("before the first check has answered", func(t *testing.T) {
		withVersion(t, "0.1-alpha")
		if got := h.c.updateProblems(); len(got) != 0 {
			t.Fatalf("problems = %v, want none", got)
		}
	})

	h.c.mu.Lock()
	h.c.release = &releaseState{Tag: "v0.2-beta", URL: "https://example.invalid/r", At: time.Now()}
	h.c.mu.Unlock()

	t.Run("a build from main is ahead, not behind", func(t *testing.T) {
		withVersion(t, "main-sha-abc1234")
		if got := h.c.updateProblems(); len(got) != 0 {
			t.Fatalf("problems = %v, want none: a main build has nothing to compare", got)
		}
	})

	t.Run("already on the current release", func(t *testing.T) {
		withVersion(t, "0.2-beta")
		if got := h.c.updateProblems(); len(got) != 0 {
			t.Fatalf("problems = %v, want none", got)
		}
	})
}

func TestUpdateProblemNamesBothVersions(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "0.1-alpha")
	h.c.mu.Lock()
	h.c.release = &releaseState{Tag: "v0.2-beta", URL: "https://example.invalid/r", At: time.Now()}
	h.c.mu.Unlock()

	got := h.c.updateProblems()
	if len(got) != 1 {
		t.Fatalf("problems = %v, want one", got)
	}
	p := got[0]
	if p.Code != "controller.update_available" {
		t.Fatalf("code = %q", p.Code)
	}
	// Both versions have to appear: the operator decides, and cannot without
	// knowing what they are on as well as what is current.
	if !strings.Contains(p.Title, "v0.2-beta") || !strings.Contains(p.Title, "0.1-alpha") {
		t.Fatalf("title = %q, want both versions named", p.Title)
	}
	if !strings.Contains(p.Fix, "https://example.invalid/r") {
		t.Fatalf("fix = %q, want the release notes linked", p.Fix)
	}
}

func TestDevelopmentUpdateProblemNamesRunningAndMainCommits(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "main-sha-abc1234")
	h.c.mu.Lock()
	h.c.development = &developmentState{SHA: "def5678901234567", URL: "https://example.invalid/commit/def5678", At: time.Now()}
	h.c.mu.Unlock()

	got := h.c.updateProblems()
	if len(got) != 1 {
		t.Fatalf("problems = %v, want one", got)
	}
	p := got[0]
	if p.Code != "controller.development_update_available" || !strings.Contains(p.Title, "abc1234") || !strings.Contains(p.Title, "def5678") {
		t.Fatalf("problem = %+v, want both development commits", p)
	}
	if !strings.Contains(p.Detail, "actually published") || !strings.Contains(p.Fix, "zoomies upgrade") {
		t.Fatalf("problem does not explain the stale channel: %+v", p)
	}

	h.c.mu.Lock()
	h.c.development.SHA = "abc1234fffffffffffffffffffffffff"
	h.c.mu.Unlock()
	if got := h.c.updateProblems(); len(got) != 0 {
		t.Fatalf("matching development build reported as stale: %v", got)
	}
}

// Switching the check off has to switch the notice off too, or an air-gapped
// fleet keeps being told about a release it deliberately stopped asking for.
func TestUpdateProblemIsSilentWhenTheCheckIsOff(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "0.1-alpha")
	h.c.mu.Lock()
	h.c.release = &releaseState{Tag: "v0.2-beta", At: time.Now()}
	h.c.mu.Unlock()

	h.c.live.Update(func(c *config.Config) { c.Updates.CheckInterval = 0 })

	if got := h.c.updateProblems(); len(got) != 0 {
		t.Fatalf("problems = %v, want none", got)
	}
}

func TestCheckForReleaseRecordsWhatGitHubSaid(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "0.1-alpha")
	h.c.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
		if r.URL.String() != latestReleaseURL {
			t.Errorf("asked %s, want %s", r.URL, latestReleaseURL)
		}
		return jsonResponse(http.StatusOK, `{"tag_name":"v0.2-beta","html_url":"https://example.invalid/r"}`)
	})}

	h.c.checkForRelease(h.ctx)

	got := h.c.latestRelease()
	if got == nil || got.Tag != "v0.2-beta" || got.URL != "https://example.invalid/r" {
		t.Fatalf("release = %+v", got)
	}
}

func TestDevelopmentBuildChecksMainInsteadOfTheLatestRelease(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "main-sha-abc1234")
	h.c.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
		if r.URL.String() != mainCommitURL {
			t.Errorf("asked %s, want %s", r.URL, mainCommitURL)
		}
		return jsonResponse(http.StatusOK, `{"sha":"def5678901234567","html_url":"https://example.invalid/commit/def5678"}`)
	})}

	h.c.checkForRelease(h.ctx)

	got := h.c.latestDevelopment()
	if got == nil || got.SHA != "def5678901234567" || got.URL != "https://example.invalid/commit/def5678" {
		t.Fatalf("development = %+v", got)
	}
	if got := h.c.latestRelease(); got != nil {
		t.Fatalf("release = %+v, want no release comparison for a main build", got)
	}
}

// A repository with no published release answers 404, and a controller that
// cannot reach GitHub answers nothing. Neither is a problem with the fleet, and
// neither may be mistaken for an answer.
func TestCheckForReleaseKeepsQuietWhenThereIsNoAnswer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"no release published", http.StatusNotFound, `{"message":"Not Found"}`},
		{"rate limited", http.StatusForbidden, `{"message":"API rate limit exceeded"}`},
		{"an answer with no tag", http.StatusOK, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.c.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
				return jsonResponse(tc.status, tc.body)
			})}
			h.c.checkForRelease(h.ctx)
			if got := h.c.latestRelease(); got != nil {
				t.Fatalf("release = %+v, want nothing recorded", got)
			}
		})
	}
}

// In mode off the controller asks the one question it always has. The list is
// larger, a fleet that has not opted in to updating has no use for what it
// carries, and off is the promise that nothing about what the controller sends
// to github.com has changed.
func TestAnOffModeAsksForTheLatestReleaseAndNeverTheList(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.3")
	h.inMode("off")
	gh := h.stubGitHub(http.StatusOK, `{"tag_name":"v1.3.4","html_url":"https://example.invalid/r"}`)

	if err := h.c.checkForRelease(h.ctx); err != nil {
		t.Fatalf("checkForRelease: %v", err)
	}

	// Spelt out and not read from the constant: the address is the promise.
	if want := []string{"https://api.github.com/repos/eyupio/zoomies/releases/latest"}; !slices.Equal(gh.asked, want) {
		t.Fatalf("asked for %v, want only %v", gh.asked, want)
	}
	got := h.c.latestRelease()
	if got == nil || got.Tag != "v1.3.4" || got.URL != "https://example.invalid/r" {
		t.Fatalf("release = %+v, want what releases/latest said", got)
	}
	if got.Releases != nil {
		t.Errorf("releases = %v, want no list recorded when none was read", got.Releases)
	}
}

// Choosing a release to install needs what releases/latest does not say: whether
// the release carries the files an update downloads, and when it was published.
// auto asks the same question as manual and adds a wait, so both read the list.
func TestAManualModeAsksForTheReleaseList(t *testing.T) {
	for _, mode := range []string{"manual", "auto"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			withVersion(t, "1.3.3")
			h.inMode(mode)
			gh := h.stubGitHub(http.StatusOK, releaseList())

			if err := h.c.checkForRelease(h.ctx); err != nil {
				t.Fatalf("checkForRelease: %v", err)
			}

			// Spelt out and not read from the constant, as in the off case.
			if want := []string{"https://api.github.com/repos/eyupio/zoomies/releases?per_page=10"}; !slices.Equal(gh.asked, want) {
				t.Fatalf("asked for %v, want only %v", gh.asked, want)
			}
		})
	}
}

// The configuration refuses a mode it does not know when it loads, so one
// arriving here is a bug, and the reading that asks for the least is the safe
// one: updating is something an operator turns on, never something a typo does.
func TestAModeNobodyDefinedIsReadAsOff(t *testing.T) {
	for _, mode := range []string{"automatic", ""} {
		t.Run(fmt.Sprintf("%q", mode), func(t *testing.T) {
			h := newHarness(t)
			withVersion(t, "1.3.3")
			h.inMode(mode)
			gh := h.stubGitHub(http.StatusOK, `{"tag_name":"v1.3.4","html_url":"https://example.invalid/r"}`)

			if err := h.c.checkForRelease(h.ctx); err != nil {
				t.Fatalf("checkForRelease: %v", err)
			}

			if want := []string{latestReleaseURL}; !slices.Equal(gh.asked, want) {
				t.Fatalf("asked for %v, want only %v", gh.asked, want)
			}
		})
	}
}

// A build from main follows the head of main, whatever the mode. It is usually
// ahead of the newest release, so there is nothing in a list of releases to
// compare it with, and the mode must not change what it asks.
func TestADevelopmentBuildKeepsAskingAboutMainWhateverTheMode(t *testing.T) {
	for _, mode := range []string{"off", "manual", "auto"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			withVersion(t, "main-sha-abc1234")
			h.inMode(mode)
			gh := h.stubGitHub(http.StatusOK, `{"sha":"def5678901234567","html_url":"https://example.invalid/commit/def5678"}`)

			if err := h.c.checkForRelease(h.ctx); err != nil {
				t.Fatalf("checkForRelease: %v", err)
			}

			if want := []string{mainCommitURL}; !slices.Equal(gh.asked, want) {
				t.Fatalf("asked for %v, want only %v", gh.asked, want)
			}
			if got := h.c.latestDevelopment(); got == nil || got.SHA != "def5678901234567" {
				t.Errorf("development = %+v, want the head of main", got)
			}
			if got := h.c.latestRelease(); got != nil {
				t.Errorf("release = %+v, want no release comparison for a main build", got)
			}
		})
	}
}

// What the rule in internal/updates reads is decoded from GitHub's names for it
// -- tag_name, published_at, prerelease, draft and the name of each asset -- and
// all of it is kept, the entries that cannot be taken included: the status
// explains a release that was passed over, and a soak is counted afresh every
// time the clock moves on, neither of which is a reason to ask GitHub again.
func TestTheWholeListIsRecordedAndTheNewestReleaseThatCanBeTakenIsNamed(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.3")
	h.inMode("manual")
	assets := completeAssets(t)
	published := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

	candidate := releaseEntry("v1.3.7", published.Add(72*time.Hour), assets...)
	candidate["prerelease"] = true
	pulled := releaseEntry("v1.3.6", time.Time{}, assets...)
	pulled["draft"] = true
	h.stubGitHub(http.StatusOK, releaseList(
		candidate,
		pulled,
		releaseEntry("v1.3.4", published, assets...),
		releaseEntry("v1.3.2", published.Add(-240*time.Hour), assets[0]),
	))

	if err := h.c.checkForRelease(h.ctx); err != nil {
		t.Fatalf("checkForRelease: %v", err)
	}

	got := h.c.latestRelease()
	if got == nil {
		t.Fatal("nothing was recorded")
	}
	want := []updates.Release{
		{Tag: "v1.3.7", URL: "https://example.invalid/releases/v1.3.7", PublishedAt: published.Add(72 * time.Hour), Prerelease: true, Assets: assets},
		{Tag: "v1.3.6", URL: "https://example.invalid/releases/v1.3.6", Draft: true, Assets: assets},
		{Tag: "v1.3.4", URL: "https://example.invalid/releases/v1.3.4", PublishedAt: published, Assets: assets},
		{Tag: "v1.3.2", URL: "https://example.invalid/releases/v1.3.2", PublishedAt: published.Add(-240 * time.Hour), Assets: assets[:1]},
	}
	if len(got.Releases) != len(want) {
		t.Fatalf("recorded %d releases, want %d: %+v", len(got.Releases), len(want), got.Releases)
	}
	for i, w := range want {
		g := got.Releases[i]
		if g.Tag != w.Tag || g.URL != w.URL || !g.PublishedAt.Equal(w.PublishedAt) ||
			g.Prerelease != w.Prerelease || g.Draft != w.Draft || !slices.Equal(g.Assets, w.Assets) {
			t.Errorf("release %d = %+v, want %+v", i, g, w)
		}
	}
	// The prerelease is the newest and fully equipped, and the draft is newer
	// than what is named too; neither may be offered.
	if got.Tag != "v1.3.4" || got.URL != "https://example.invalid/releases/v1.3.4" {
		t.Errorf("named %s (%s), want v1.3.4, the newest that is neither a prerelease nor a draft nor incomplete", got.Tag, got.URL)
	}
	if got.At.IsZero() {
		t.Error("the answer carries no time, so a stale one could not be recognised")
	}
}

// GitHub gives a release that is not published no date, and encoding/json turns
// a null, a missing key and the zero date alike into the zero time without an
// error. Refusing the whole list over one such entry would leave a fleet deaf to
// every release for as long as it stayed among the first ten. It is
// internal/updates that declines to offer a release it cannot date, because the
// soak starts from that date, so the entry is kept and the rest is read.
func TestAReleaseWithNoDateIsKeptInTheListAndNeverNamed(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.3")
	h.inMode("manual")
	files := fmt.Sprintf(`[{"name":"checksums.txt"},{"name":%q}]`, completeAssets(t)[1])
	// By hand, because the three ways of leaving a date out are the point.
	h.stubGitHub(http.StatusOK, fmt.Sprintf(`[
		{"tag_name":"v1.3.6","html_url":"u6","published_at":null,"assets":%[1]s},
		{"tag_name":"v1.3.5","html_url":"u5","assets":%[1]s},
		{"tag_name":"v1.3.4","html_url":"u4","published_at":"0001-01-01T00:00:00Z","assets":%[1]s},
		{"tag_name":"v1.3.3","html_url":"u3","published_at":"2026-10-01T09:00:00Z","assets":%[1]s}
	]`, files))

	if err := h.c.checkForRelease(h.ctx); err != nil {
		t.Fatalf("an undated entry failed the check: %v", err)
	}

	got := h.c.latestRelease()
	if got == nil || len(got.Releases) != 4 {
		t.Fatalf("release = %+v, want all four entries kept", got)
	}
	for _, r := range got.Releases[:3] {
		if !r.PublishedAt.IsZero() {
			t.Errorf("%s has the date %s, want none", r.Tag, r.PublishedAt)
		}
	}
	if got.Tag != "v1.3.3" {
		t.Errorf("named %q, want v1.3.3: the three newer entries have no date to soak from", got.Tag)
	}
}

// GitHub's "latest" is the newest by date, and a release is public before its
// files have all been uploaded, so for a while the newest tag is one nothing can
// install. The notice used to name it. It names what the status would offer, so
// an operator is never told of one version in the banner and offered another in
// the panel under it.
func TestTheNoticeAndTheStatusNameTheSameRelease(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.3")
	h.inMode("manual")
	assets := completeAssets(t)
	published := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	h.stubGitHub(http.StatusOK, releaseList(
		// Binaries first and checksums last: this one is mid-upload.
		releaseEntry("v1.3.5", published.Add(2*time.Hour), assets[1]),
		releaseEntry("v1.3.4", published, assets...),
	))

	if err := h.c.checkForRelease(h.ctx); err != nil {
		t.Fatalf("checkForRelease: %v", err)
	}

	got := h.c.latestRelease()
	if got == nil || got.Tag != "v1.3.4" {
		t.Fatalf("release = %+v, want v1.3.4", got)
	}
	problems := h.c.updateProblems()
	if len(problems) != 1 || !strings.Contains(problems[0].Title, "v1.3.4") || strings.Contains(problems[0].Title, "v1.3.5") {
		t.Fatalf("problems = %+v, want one that names v1.3.4 and not v1.3.5", problems)
	}
	// The status is the rule in internal/updates applied to what was recorded.
	target := updates.Choose(updates.ChooseInput{
		Mode: updates.ModeManual, Now: h.c.Now(), Running: version.Version,
		Releases: got.Releases, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
	})
	if !target.Newer || target.Release.Tag != got.Tag {
		t.Errorf("the status would offer %q (newer: %v), but the notice names %q", target.Release.Tag, target.Newer, got.Tag)
	}
}

// A controller can be ahead of the newest release that can be taken: its own
// release is public before checksums.txt is uploaded, so the list's newest
// complete entry is the one before it. Telling that controller "the current
// release is v1.3.9" while the status and the gauge say it is ahead sent an
// operator to downgrade. Only a controller behind the release is told of one.
// With the mode off the one request made today is read as it always was, and
// that path stays a plain comparison of the two words.
func TestTheNoticeIsOnlyRaisedForABuildBehindTheRelease(t *testing.T) {
	assets := completeAssets(t)
	published := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, mode, running, body string
		want                      bool
	}{
		{"the build is ahead of the newest complete release", "manual", "1.4.0",
			releaseList(releaseEntry("v1.4.0", published.Add(time.Hour), assets[1]), releaseEntry("v1.3.9", published, assets...)), false},
		{"the build is the release", "manual", "1.3.9",
			releaseList(releaseEntry("v1.3.9", published, assets...)), false},
		{"the build is behind the release", "auto", "1.3.3",
			releaseList(releaseEntry("v1.3.9", published, assets...)), true},
		{"the mode is off and the words differ", "off", "1.4.0",
			`{"tag_name":"v1.3.9","html_url":"https://example.invalid/r"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			withVersion(t, tc.running)
			h.inMode(tc.mode)
			h.stubGitHub(http.StatusOK, tc.body)
			if err := h.c.checkForRelease(h.ctx); err != nil {
				t.Fatalf("checkForRelease: %v", err)
			}

			problems := h.c.updateProblems()
			if got := len(problems) == 1; got != tc.want {
				t.Errorf("problems = %+v, want a notice: %v", problems, tc.want)
			}
		})
	}
}

// A list in which nothing can be taken is an answer and not the absence of one:
// the release that used to be there is gone from it, and naming it would send an
// operator to something GitHub no longer offers. The list is kept all the same,
// empty or not, so that the status can say why there is nothing to take instead
// of saying that nothing was read.
func TestAListWithNothingToTakeReplacesTheLastAnswer(t *testing.T) {
	assets := completeAssets(t)
	published := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		body string
		kept int
	}{
		{"every release in it is incomplete", releaseList(releaseEntry("v1.3.5", published, assets[0])), 1},
		{"it is empty", releaseList(), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			withVersion(t, "1.3.3")
			h.inMode("manual")
			gh := h.stubGitHub(http.StatusOK, releaseList(releaseEntry("v1.3.4", published, assets...)))
			if err := h.c.checkForRelease(h.ctx); err != nil {
				t.Fatalf("the first check: %v", err)
			}
			if got := h.c.latestRelease(); got == nil || got.Tag != "v1.3.4" {
				t.Fatalf("release = %+v, want v1.3.4 before the list changes", got)
			}

			gh.body = tc.body
			if err := h.c.checkForRelease(h.ctx); err != nil {
				t.Fatalf("the second check: %v", err)
			}

			got := h.c.latestRelease()
			if got == nil {
				t.Fatal("the answer was dropped, so nothing says a list was read")
			}
			if got.Tag != "" || got.URL != "" {
				t.Errorf("named %q (%q), want no release: the list holds none that can be taken", got.Tag, got.URL)
			}
			if got.Releases == nil || len(got.Releases) != tc.kept {
				t.Errorf("releases = %#v, want %d kept and not nil: a list that came back empty is not a list that was never read", got.Releases, tc.kept)
			}
			if problems := h.c.updateProblems(); len(problems) != 0 {
				t.Errorf("problems = %v, want none when there is no release to name", problems)
			}
		})
	}
}

// The privacy page promises that the update check says nothing about an
// installation beyond the version it runs, in a User-Agent every call to GitHub
// carries. Asking for the list instead of the latest release is not a way
// round that, so the two questions are announced alike.
func TestTheListIsAskedForAsTheLatestReleaseIs(t *testing.T) {
	for _, tc := range []struct{ mode, body string }{
		{"off", `{"tag_name":"v1.3.4","html_url":"https://example.invalid/r"}`},
		{"manual", releaseList()},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			h := newHarness(t)
			withVersion(t, "1.3.3")
			h.inMode(tc.mode)
			gh := h.stubGitHub(http.StatusOK, tc.body)

			if err := h.c.checkForRelease(h.ctx); err != nil {
				t.Fatalf("checkForRelease: %v", err)
			}

			if len(gh.headers) != 1 {
				t.Fatalf("made %d requests, want 1", len(gh.headers))
			}
			if got := gh.headers[0].Get("User-Agent"); got != "zoomies/1.3.3" {
				t.Errorf("User-Agent = %q, want zoomies/1.3.3", got)
			}
			if got := gh.headers[0].Get("Accept"); got != "application/vnd.github+json" {
				t.Errorf("Accept = %q, want application/vnd.github+json", got)
			}
		})
	}
}

// A controller that cannot get an answer is not a controller with a problem, and
// the answer it already holds is still the best it has. The error is for whoever
// asked, and the button reports it too: a person who asked for a check is not
// told that it worked.
func TestAFailedListRequestKeepsTheLastAnswer(t *testing.T) {
	assets := completeAssets(t)
	published := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		fail func(h *harness, gh *githubStub)
	}{
		{"GitHub refuses on its rate limit", func(_ *harness, gh *githubStub) {
			gh.status, gh.body = http.StatusForbidden, `{"message":"API rate limit exceeded"}`
		}},
		{"GitHub is down", func(_ *harness, gh *githubStub) {
			gh.status, gh.body = http.StatusBadGateway, `<html>Bad Gateway</html>`
		}},
		{"there is no route to GitHub", func(h *harness, _ *githubStub) {
			h.c.httpClient = &http.Client{Transport: unreachable{}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			withVersion(t, "1.3.3")
			h.inMode("manual")
			gh := h.stubGitHub(http.StatusOK, releaseList(releaseEntry("v1.3.4", published, assets...)))
			if err := h.c.checkForRelease(h.ctx); err != nil {
				t.Fatalf("the first check: %v", err)
			}
			before := h.c.latestRelease()

			tc.fail(h, gh)

			if err := h.c.checkForRelease(h.ctx); err == nil {
				t.Error("a check that failed returned no error")
			}
			if got := h.c.latestRelease(); got != before {
				t.Errorf("release = %+v, want the last answer %+v left alone", got, before)
			}
			if err := h.c.CheckForReleases(h.ctx); err == nil {
				t.Error("the button reported a check that failed as having worked")
			}
			if got := h.c.latestRelease(); got != before {
				t.Errorf("release = %+v after the button, want the last answer %+v left alone", got, before)
			}
		})
	}
}

// GitHub answers in this shape or something is wrong with the answer: a proxy's
// error page, an object where the list should be, or a body too large to be ten
// releases. None of it is guessed at. The check fails, and the answer already
// held stays, which is the difference between a stale notice and a wrong one.
func TestAListOfAnUnexpectedShapeIsIgnored(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"an object where the list should be", `{}`},
		{"null", `null`},
		{"an error page from a proxy", `<html><body>Bad Gateway</body></html>`},
		{"entries that are not releases", `[1, 2]`},
		{"a list cut off part way", `[{"tag_name":"v1.3.4","assets":[{"name":"checksums.txt"}`},
		{"a list larger than the controller will read", `[{"tag_name":"v1.3.4","body":"` + strings.Repeat("x", 5<<20) + `"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			withVersion(t, "1.3.3")
			h.inMode("manual")
			before := &releaseState{Tag: "v1.3.4", URL: "https://example.invalid/releases/v1.3.4", At: time.Now()}
			h.c.mu.Lock()
			h.c.release = before
			h.c.mu.Unlock()
			h.stubGitHub(http.StatusOK, tc.body)

			if err := h.c.checkForRelease(h.ctx); err == nil {
				t.Error("an answer that is not a list of releases returned no error")
			}
			if got := h.c.latestRelease(); got != before {
				t.Errorf("release = %+v, want the last answer %+v left alone", got, before)
			}
		})
	}
}

// updates.check_interval 0 is the air-gap switch: it says this controller must
// never make the one request that is not about the fleet, and a button is not an
// exception to it. A refusal is not a check, either, so it does not use up the
// minute: once the switch is turned back on, the next press asks.
func TestCheckForReleasesRefusesWhenTheCheckIsOff(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.3")
	h.inMode("manual")
	h.c.live.Update(func(c *config.Config) { c.Updates.CheckInterval = 0 })
	gh := h.stubGitHub(http.StatusOK, releaseList())

	if err := h.c.CheckForReleases(h.ctx); !errors.Is(err, ErrUpdateCheckDisabled) {
		t.Fatalf("CheckForReleases = %v, want ErrUpdateCheckDisabled", err)
	}
	if len(gh.asked) != 0 {
		t.Fatalf("asked for %v with the check switched off, want nothing", gh.asked)
	}

	h.c.live.Update(func(c *config.Config) { c.Updates.CheckInterval = 24 * time.Hour })
	if err := h.c.CheckForReleases(h.ctx); err != nil {
		t.Fatalf("CheckForReleases once switched on: %v", err)
	}
	if len(gh.asked) != 1 {
		t.Errorf("asked %d times after the refusal, want the next press to ask once", len(gh.asked))
	}
}

// The request is unauthenticated, so the limit it spends is shared with
// everything else on the controller's address, and a button pressed twice is one
// question. A press inside the minute is not an error: what it would have learnt
// is what the last one learnt a moment ago. The wait is counted from the press
// that asked, so a press that was held back does not start it again.
func TestCheckForReleasesIsLimitedToOneRequestAMinute(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.3")
	h.inMode("manual")
	gh := h.stubGitHub(http.StatusOK, releaseList())

	if err := h.c.CheckForReleases(h.ctx); err != nil {
		t.Fatalf("the first press: %v", err)
	}
	if len(gh.asked) != 1 {
		t.Fatalf("asked %d times after the first press, want 1", len(gh.asked))
	}

	h.advance(59 * time.Second)
	if err := h.c.CheckForReleases(h.ctx); err != nil {
		t.Errorf("a press after 59 seconds returned %v, want nil", err)
	}
	if len(gh.asked) != 1 {
		t.Fatalf("asked %d times after 59 seconds, want no new request", len(gh.asked))
	}

	h.advance(2 * time.Second)
	if err := h.c.CheckForReleases(h.ctx); err != nil {
		t.Fatalf("a press after 61 seconds: %v", err)
	}
	if len(gh.asked) != 2 {
		t.Errorf("asked %d times after 61 seconds, want the press to ask again", len(gh.asked))
	}
}

// GitHub refusing the request is the case the limit is for: a rate-limited
// address that is asked again a second later is asked a second time while it is
// still refusing. So the minute is spent by asking and not by succeeding, and a
// press after a failure is held back like any other.
func TestAFailedRequestStillUsesUpTheMinuteAllowance(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.3")
	h.inMode("manual")
	gh := h.stubGitHub(http.StatusForbidden, `{"message":"API rate limit exceeded"}`)

	if err := h.c.CheckForReleases(h.ctx); err == nil {
		t.Fatal("a press that GitHub refused returned no error")
	}
	if len(gh.asked) != 1 {
		t.Fatalf("asked %d times after the first press, want 1", len(gh.asked))
	}

	h.advance(10 * time.Second)
	if err := h.c.CheckForReleases(h.ctx); err != nil {
		t.Errorf("a press inside the minute returned %v, want it held back without an error", err)
	}
	if len(gh.asked) != 1 {
		t.Errorf("asked %d times after a failure and a second press, want the second held back", len(gh.asked))
	}
}

// The request is made with no lock held, or every reader of the controller waits
// on GitHub for as long as the request takes, and the minute is taken before it
// is made, or a second press while it is in flight asks again. A request that
// presses the button and reads the answer from inside itself cannot be written
// to pass unless both are true: with the lock held it never returns, and with
// the stamp taken afterwards the inner press makes a second request.
func TestNoLockIsHeldAcrossTheRequestAndASecondPressDuringItIsHeldBack(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.3")
	h.inMode("manual")

	asked := 0
	var innerErr error
	var innerSaw *releaseState
	h.c.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
		asked++
		if asked == 1 {
			innerErr = h.c.CheckForReleases(h.ctx)
			innerSaw = h.c.latestRelease()
		}
		return jsonResponse(http.StatusOK, releaseList())
	})}

	done := make(chan error, 1)
	go func() { done <- h.c.CheckForReleases(h.ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the press: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the press never returned: a lock is held across the request")
	}

	if asked != 1 {
		t.Errorf("made %d requests, want the press during the first to be held back", asked)
	}
	if innerErr != nil {
		t.Errorf("the press during the request returned %v, want nil", innerErr)
	}
	if innerSaw != nil {
		t.Errorf("a press during the request saw %+v, want nothing learnt yet", innerSaw)
	}
}

// A clock that is stepped back leaves the last request in the future. Counted
// literally that is a negative wait, shorter than the minute, so the button would
// stay dead until the clock caught up with a time that was never real.
func TestAClockSteppedBackwardsDoesNotLockTheButton(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.3")
	h.inMode("manual")
	gh := h.stubGitHub(http.StatusOK, releaseList())

	if err := h.c.CheckForReleases(h.ctx); err != nil {
		t.Fatalf("the first press: %v", err)
	}
	h.advance(-time.Hour)
	if err := h.c.CheckForReleases(h.ctx); err != nil {
		t.Fatalf("a press after the clock moved back: %v", err)
	}
	if len(gh.asked) != 2 {
		t.Errorf("asked %d times, want a press after the clock moved back to ask again", len(gh.asked))
	}
}

// The scheduled pass is what asks in production, and it is not told which
// question to ask: it has to reach the list when updating is on. The harness
// answers for GitHub with an empty list, which a pass that went to the real
// network instead could not.
func TestTheBackgroundPassReadsTheListWhenUpdatingIsOn(t *testing.T) {
	h := newHarness(t)
	withVersion(t, "1.3.3")
	h.inMode("manual")

	h.c.housekeep(h.ctx, &housekeeping{})

	got := h.c.latestRelease()
	if got == nil || got.Releases == nil || len(got.Releases) != 0 {
		t.Fatalf("release = %+v, want the empty list the harness gave", got)
	}
}
