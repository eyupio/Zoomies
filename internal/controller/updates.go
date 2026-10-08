package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/version"
)

// The release check asks github.com which release of Zoomies is current, so an
// operator learns that their controller is behind from the UI rather than from
// a changelog they were not reading.
//
// It is deliberately the only thing in Zoomies that talks to github.com
// regardless of github.api_base_url: the releases of this software live there
// whichever GitHub a fleet is pointed at. An Enterprise Server deployment with
// no route to github.com should switch it off, and updates.check_interval: 0
// says so plainly.
//
// What it asks depends on updates.mode. Off asks for the latest release, as it
// always has. Manual and auto read the last ten instead, because "latest" is
// GitHub's pick and not necessarily one that can be installed -- a release is
// public before its files have all been uploaded -- and a single answer leaves
// nothing to fall back to. A fleet that has not asked to update never requests
// the list.
const (
	// latestReleaseURL is the API redirect GitHub maintains for the newest
	// release that is neither a draft nor a prerelease.
	latestReleaseURL = "https://api.github.com/repos/eyupio/zoomies/releases/latest"
	// releaseListURL is the ten most recent releases. Ten covers about a month at
	// the current cadence, which is far more than an update chooses between, and
	// one request a day is a small share of the limit on requests that do not
	// sign in.
	releaseListURL = "https://api.github.com/repos/eyupio/zoomies/releases?per_page=10"
	mainCommitURL  = "https://api.github.com/repos/eyupio/zoomies/commits/main"
	// updateCheckTimeout bounds the request. Nothing waits on this, so it can
	// afford to be patient, but not to hold a housekeeping pass open.
	updateCheckTimeout = 15 * time.Second
	// releaseListMaxBytes bounds how much of the list is read. Ten releases with
	// their notes and assets run to a few hundred kilobytes; this is the ceiling
	// the installer puts on reading the same endpoint, and a body beyond it is not
	// ten releases.
	releaseListMaxBytes = 4 << 20
	// releaseAskCooldown is the least time between two requests the button lets
	// through. The request carries no credentials, so the limit it spends is
	// shared with everything else on the controller's address, and a button
	// pressed twice is one question.
	releaseAskCooldown = time.Minute
)

// releaseState is what the last successful check learned.
//
// A state is replaced as a whole and never edited, so one that has been read can
// be kept, slices and all, without holding the lock.
type releaseState struct {
	// Tag is the release's tag, e.g. "v0.2-beta". While updating is on it is the
	// newest release in Releases that an update could take, always of the form
	// vX.Y.Z, and it is empty when the list holds none.
	Tag string
	// URL is its release page, so the UI can link to the notes.
	URL string
	// At is when this was learned, so a stale answer can be recognised.
	At time.Time
	// Releases is the list the check read, whole and in GitHub's order, so that
	// the rule in internal/updates can be asked again later -- as the clock moves
	// on and a soak ends -- without asking GitHub. It is nil when the check asked
	// only for the latest release, and an empty slice when GitHub's list was
	// empty: a list never read and a list that came back empty are different
	// things to tell an operator.
	Releases []updates.Release
}

// developmentState is what the last successful main-branch check learned.
type developmentState struct {
	SHA string
	URL string
	At  time.Time
}

// developmentCommit returns the commit stamped into a published main build.
func developmentCommit(v string) (string, bool) {
	const prefix = "main-sha-"
	sha := strings.TrimPrefix(strings.TrimSpace(v), prefix)
	if sha == v || len(sha) < 7 {
		return "", false
	}
	for _, r := range sha {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return "", false
		}
	}
	return sha, true
}

// releaseVersion reports the release this binary was built from, and whether it
// was built from one at all.
//
// This is the whole honesty of the feature. A controller running :dev or
// :main is stamped main-sha-abc1234 and is usually *ahead* of the newest
// release, so telling it that a release is available would be telling it to
// downgrade. Only a build that came from a release tag has anything to compare.
//
// The join command needs the same answer -- an agent is installed from a
// release asset, so a controller that is not a release cannot pin a host to
// itself -- so the rule lives in internal/version and this is its name here.
func releaseVersion(v string) (string, bool) { return version.Release(v) }

// updateMode is updates.mode as the rule in internal/updates reads it. The
// configuration refuses a word it does not know when it loads, so one arriving
// here is a bug, and the reading that asks for the least is the safe one: the
// list is only ever requested because somebody chose a mode that needs it.
func (c *Controller) updateMode() updates.Mode {
	switch mode := updates.Mode(c.cfg().Updates.Mode); mode {
	case updates.ModeManual, updates.ModeAuto:
		return mode
	}
	return updates.ModeOff
}

// checkForRelease asks GitHub for the current release and records it.
//
// With updates.mode off it asks for the latest release, and a development build
// asks about main in every mode, both exactly as they always have. Manual and
// auto read the release list instead and keep all of it, naming in Tag and URL
// the release an update could take.
//
// A failure is logged at debug and the previous answer stays until a later pass
// replaces it: a controller that cannot reach github.com is not a controller
// with a problem. Only a failed read of the list is returned, so that a person
// who asked for the check can be told it did not happen; the two older questions
// have always failed quietly and still do.
func (c *Controller) checkForRelease(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()
	if _, ok := developmentCommit(version.Version); ok {
		c.checkForMain(ctx)
		return nil
	}
	if c.updateMode() != updates.ModeOff {
		return c.checkForReleaseList(ctx)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		c.log.Debug("could not build the release check request", "error", err)
		return nil
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", version.UserAgent())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.log.Debug("could not ask GitHub which release is current", "error", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 404 is what a repository with no published release answers, and it is
		// not a failure: there is simply nothing to compare against yet.
		c.log.Debug("the release check was refused", "status", resp.StatusCode)
		return nil
	}

	var body struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		c.log.Debug("could not read the release check answer", "error", err)
		return nil
	}
	if strings.TrimSpace(body.TagName) == "" {
		return nil
	}

	c.mu.Lock()
	c.release = &releaseState{Tag: body.TagName, URL: body.HTMLURL, At: c.Now()}
	c.mu.Unlock()
	return nil
}

// checkForReleaseList reads GitHub's release list and records all of it.
//
// What it returns is written for the person who pressed the button, who cannot
// read the log: what failed, that nothing has changed, and what to try.
func (c *Controller) checkForReleaseList(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseListURL, nil)
	if err != nil {
		c.log.Debug("could not build the release list request", "error", err)
		return fmt.Errorf("could not build the request for the release list: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", version.UserAgent())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.log.Debug("could not ask GitHub for the release list", "error", err)
		return fmt.Errorf("could not reach GitHub for the release list (%w); check that this controller may make outbound HTTPS requests to api.github.com", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.log.Debug("the release list request was refused", "status", resp.StatusCode)
		return fmt.Errorf("GitHub answered %d to the request for the release list, so nothing has changed; try again later", resp.StatusCode)
	}

	var listed []githubRelease
	err = json.NewDecoder(io.LimitReader(resp.Body, releaseListMaxBytes)).Decode(&listed)
	if err == nil && listed == nil {
		// A null decodes without an error, and it is not the empty list [] that
		// GitHub answers when a repository has no releases.
		err = errors.New("the answer was null")
	}
	if err != nil {
		c.log.Debug("could not read the release list", "error", err)
		return errors.New("GitHub's answer was not a list of releases, so nothing has changed; try again later")
	}

	releases := make([]updates.Release, 0, len(listed))
	for _, g := range listed {
		releases = append(releases, g.release())
	}
	state := &releaseState{Releases: releases, At: c.Now()}
	// This controller's own system is the one asked about: it is the binary a
	// controller update installs, and the release the notice is about.
	if best, ok := updates.Newest(releases, runtime.GOOS, runtime.GOARCH); ok {
		state.Tag, state.URL = best.Tag, best.URL
	}

	c.mu.Lock()
	c.release = state
	c.mu.Unlock()
	return nil
}

// githubRelease is one entry of GitHub's release list as it arrives, cut down to
// what internal/updates reads. GitHub's names for it stop here, because that
// package takes no JSON.
//
// published_at is decoded as it comes. A null, a missing key and the zero date
// all become the zero time without an error, and updates.Newest declines a
// release it cannot date, so nothing here filters on it.
type githubRelease struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
	Assets      []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

func (g githubRelease) release() updates.Release {
	r := updates.Release{
		Tag: g.TagName, URL: g.HTMLURL, PublishedAt: g.PublishedAt,
		Prerelease: g.Prerelease, Draft: g.Draft,
	}
	for _, a := range g.Assets {
		r.Assets = append(r.Assets, a.Name)
	}
	return r
}

// CheckForReleases asks GitHub now instead of at the next scheduled check, for a
// person who asked for one.
//
// It refuses while updates.check_interval is 0, because that setting says this
// controller must never make the request, and it lets one request through a
// minute. A press inside the minute is not an error: what it would have learnt
// is what the last one learnt a moment ago, so it returns as though it had asked.
// The minute is counted from the request and not from its success, because a
// refusal from GitHub is a reason to ask less often.
func (c *Controller) CheckForReleases(ctx context.Context) error {
	if c.cfg().Updates.CheckInterval <= 0 {
		return ErrUpdateCheckDisabled
	}
	now := c.Now()
	c.mu.Lock()
	// The zero time is far enough back that the first press always asks.
	if now.Sub(c.releaseAsked) < releaseAskCooldown {
		c.mu.Unlock()
		return nil
	}
	c.releaseAsked = now
	c.mu.Unlock()
	return c.checkForRelease(ctx)
}

// checkForMain gives a moving dev build a source of truth. The registry tag
// can remain perfectly reachable while CI has failed to advance it, so asking
// the branch rather than the tag is what detects that failure mode.
func (c *Controller) checkForMain(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mainCommitURL, nil)
	if err != nil {
		c.log.Debug("could not build the main branch check request", "error", err)
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", version.UserAgent())
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.log.Debug("could not ask GitHub which commit is on main", "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.log.Debug("the main branch check was refused", "status", resp.StatusCode)
		return
	}
	var body struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		c.log.Debug("could not read the main branch check answer", "error", err)
		return
	}
	if strings.TrimSpace(body.SHA) == "" {
		return
	}
	c.mu.Lock()
	c.development = &developmentState{SHA: body.SHA, URL: body.HTMLURL, At: c.Now()}
	c.mu.Unlock()
}

// latestRelease returns what the last check learned, or nil.
func (c *Controller) latestRelease() *releaseState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.release
}

func (c *Controller) latestDevelopment() *developmentState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.development
}
