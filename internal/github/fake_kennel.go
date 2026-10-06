package github

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
)

// What the fake needs to be able to say for Kennel Club: whether a repository is
// public, and what started a run and where its head commit lives.

// fakeRunTrigger is what a run says about how it began.
type fakeRunTrigger struct {
	event string
	// headRepositoryID is zero for a run whose head repository has been deleted,
	// which GitHub reports as null.
	headRepositoryID int64
}

func triggerKey(repo string, runID int64) string { return repo + "#" + strconv.FormatInt(runID, 10) }

// SetVisibility says whether a repository is "public", "private" or "internal".
// A repository the fake has not been told about is private, which is what it
// has always answered.
func (f *FakeGitHub) SetVisibility(fullName, visibility string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addRepoLocked(fullName)
	f.repoLocked(fullName).visibility = visibility
}

// RepositoryID is the numeric ID the fake gave a repository, so a test can say a
// run's head is in the same repository (the same ID) or in another (a different
// one).
func (f *FakeGitHub) RepositoryID(fullName string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addRepoLocked(fullName)
	return f.repoLocked(fullName).id
}

// SetRunTrigger gives a run the event that started it and the ID of the
// repository its head commit is in; zero means that repository was deleted. It
// makes the run readable even if no job of it was ever queued.
func (f *FakeGitHub) SetRunTrigger(repo string, runID int64, event string, headRepositoryID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addRepoLocked(repo)
	if f.runTriggers == nil {
		f.runTriggers = map[string]fakeRunTrigger{}
	}
	f.runTriggers[triggerKey(repo, runID)] = fakeRunTrigger{event: event, headRepositoryID: headRepositoryID}
}

// visibilityLocked is a repository's visibility, private unless told otherwise.
func (f *FakeGitHub) visibilityLocked(fullName string) string {
	if v := f.repoLocked(fullName).visibility; v != "" {
		return v
	}
	return "private"
}

// writeETagged answers like GitHub does for a resource it can revalidate: with
// an ETag, and with a 304 and no body when the caller already has this version.
// It must be called with f.mu held, as every handler is.
func (f *FakeGitHub) writeETagged(w http.ResponseWriter, r *http.Request, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sum := sha256.Sum256(b)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		f.notModified++
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

// NotModified is how many requests the fake has answered with a 304, which is
// how a test shows a repeated read really did not cost a full response.
func (f *FakeGitHub) NotModified() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.notModified
}
