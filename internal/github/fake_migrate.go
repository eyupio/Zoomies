package github

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
)

// The fake's half of the migration surface: repository contents, refs and pull
// requests.
//
// It enforces the rules that actually bite when a migration goes wrong. A
// branch that already exists is a 422, not a silent overwrite. A file committed
// with a stale blob SHA is a 409, which is what stops the wizard from
// clobbering a change somebody pushed while the review screen was open. A
// repository with no .github/workflows is a 404 on the directory, which is the
// common case in a large organisation and must not read as a broken App.

// fakeRepo is one repository's contents, as far as the migration cares.
type fakeRepo struct {
	defaultBranch string
	// files maps a repository-relative path to its contents.
	files map[string]string
	// branches maps a branch name to the commit it points at.
	branches map[string]string
	// pulls counts the pull requests opened, which is where the next number
	// comes from.
	pulls int
}

// AddWorkflow puts a workflow file in a repository, creating the repository if
// this is the first thing in it.
func (f *FakeGitHub) AddWorkflow(repo, path, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addRepoLocked(repo)
	r := f.repoLocked(repo)
	r.files[path] = content
}

// SetDefaultBranch names a repository's default branch. It is "main" until
// something says otherwise.
func (f *FakeGitHub) SetDefaultBranch(repo, branch string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addRepoLocked(repo)
	r := f.repoLocked(repo)
	head := r.branches[r.defaultBranch]
	r.defaultBranch = branch
	r.branches = map[string]string{branch: head}
}

// FileContent returns what a repository's file holds now, which is how a test
// checks what a migration actually committed.
func (f *FakeGitHub) FileContent(repo, path string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.contents[repo]
	if !ok {
		return "", false
	}
	content, ok := r.files[path]
	return content, ok
}

// Branches returns a repository's branch names, sorted.
func (f *FakeGitHub) Branches(repo string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.contents[repo]
	if !ok {
		return nil
	}
	out := slices.Collect(maps.Keys(r.branches))
	sort.Strings(out)
	return out
}

// repoLocked returns the repository's contents, creating them on first use.
// The caller holds f.mu.
func (f *FakeGitHub) repoLocked(repo string) *fakeRepo {
	if f.contents == nil {
		f.contents = map[string]*fakeRepo{}
	}
	r, ok := f.contents[repo]
	if !ok {
		r = &fakeRepo{
			defaultBranch: "main",
			files:         map[string]string{},
			branches:      map[string]string{"main": blobSHA("commit:" + repo)},
		}
		f.contents[repo] = r
	}
	return r
}

// blobSHA is a stand-in for git's object hash: stable for the same content,
// which is all the fake needs to enforce "the SHA you committed against is the
// one that is there".
func blobSHA(content string) string {
	sum := sha1.Sum([]byte(content))
	return hex.EncodeToString(sum[:])
}

func (f *FakeGitHub) registerMigrationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /repos/{owner}/{repo}", f.getRepo)
	mux.HandleFunc("GET /repos/{owner}/{repo}/contents/{path...}", f.getContents)
	mux.HandleFunc("PUT /repos/{owner}/{repo}/contents/{path...}", f.putContents)
	mux.HandleFunc("GET /repos/{owner}/{repo}/git/ref/{ref...}", f.getRef)
	mux.HandleFunc("POST /repos/{owner}/{repo}/git/refs", f.createRef)
	mux.HandleFunc("POST /repos/{owner}/{repo}/pulls", f.createPull)
}

func fullName(r *http.Request) string {
	return r.PathValue("owner") + "/" + r.PathValue("repo")
}

func (f *FakeGitHub) getRepo(w http.ResponseWriter, r *http.Request) {
	full := fullName(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Contains(f.repos, full) {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	repo := f.repoLocked(full)
	owner, name, _ := SplitTarget(full)
	writeJSON(w, http.StatusOK, map[string]any{
		"full_name":      full,
		"name":           name,
		"owner":          map[string]any{"login": owner},
		"default_branch": repo.defaultBranch,
		"private":        true,
		"archived":       false,
		"html_url":       "https://github.com/" + full,
	})
}

func (f *FakeGitHub) getContents(w http.ResponseWriter, r *http.Request) {
	full := fullName(r)
	path := strings.Trim(r.PathValue("path"), "/")

	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Contains(f.repos, full) {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	repo := f.repoLocked(full)

	if content, ok := repo.files[path]; ok {
		writeJSON(w, http.StatusOK, contentEntry(path, content))
		return
	}

	// A directory: everything one level below the prefix.
	prefix := path + "/"
	var entries []map[string]any
	for p, content := range repo.files {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok || strings.Contains(rest, "/") {
			continue
		}
		entries = append(entries, contentEntry(p, content))
	}
	if len(entries) == 0 {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i]["path"].(string) < entries[j]["path"].(string)
	})
	writeJSON(w, http.StatusOK, entries)
}

func contentEntry(path, content string) map[string]any {
	return map[string]any{
		"type":     "file",
		"name":     path[strings.LastIndex(path, "/")+1:],
		"path":     path,
		"sha":      blobSHA(content),
		"size":     len(content),
		"encoding": "base64",
		"content":  base64.StdEncoding.EncodeToString([]byte(content)),
	}
}

func (f *FakeGitHub) putContents(w http.ResponseWriter, r *http.Request) {
	full := fullName(r)
	path := strings.Trim(r.PathValue("path"), "/")

	var body struct {
		Message string `json:"message"`
		Content string `json:"content"`
		SHA     string `json:"sha"`
		Branch  string `json:"branch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	decoded, err := base64.StdEncoding.DecodeString(body.Content)
	if err != nil {
		writeError(w, http.StatusBadRequest, "content is not base64")
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Contains(f.repos, full) {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	repo := f.repoLocked(full)
	if body.Branch != "" {
		if _, ok := repo.branches[body.Branch]; !ok {
			writeError(w, http.StatusNotFound, "Branch "+body.Branch+" not found")
			return
		}
	}
	if body.Message == "" {
		writeError(w, http.StatusUnprocessableEntity, "message is required")
		return
	}
	existing, exists := repo.files[path]
	switch {
	case exists && body.SHA == "":
		writeError(w, http.StatusUnprocessableEntity, path+" exists; a sha is required to update it")
		return
	case exists && body.SHA != blobSHA(existing):
		// The real failure this guards: somebody pushed to the file between the
		// wizard reading it and the operator approving the migration.
		writeError(w, http.StatusConflict, path+" has changed since it was read")
		return
	}
	repo.files[path] = string(decoded)
	writeJSON(w, http.StatusOK, map[string]any{
		"content": contentEntry(path, string(decoded)),
		"commit":  map[string]any{"sha": blobSHA(body.Message + string(decoded))},
	})
}

func (f *FakeGitHub) getRef(w http.ResponseWriter, r *http.Request) {
	full := fullName(r)
	ref := strings.Trim(r.PathValue("ref"), "/")
	branch := strings.TrimPrefix(ref, "heads/")

	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Contains(f.repos, full) {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	repo := f.repoLocked(full)
	sha, ok := repo.branches[branch]
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ref":    "refs/heads/" + branch,
		"object": map[string]any{"sha": sha, "type": "commit"},
	})
}

func (f *FakeGitHub) createRef(w http.ResponseWriter, r *http.Request) {
	full := fullName(r)
	var body struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	branch := strings.TrimPrefix(strings.TrimPrefix(body.Ref, "refs/"), "heads/")

	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Contains(f.repos, full) {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	repo := f.repoLocked(full)
	if branch == "" || body.SHA == "" {
		writeError(w, http.StatusUnprocessableEntity, "ref and sha are required")
		return
	}
	if _, exists := repo.branches[branch]; exists {
		writeError(w, http.StatusUnprocessableEntity, "Reference already exists")
		return
	}
	repo.branches[branch] = body.SHA
	writeJSON(w, http.StatusCreated, map[string]any{
		"ref":    "refs/heads/" + branch,
		"object": map[string]any{"sha": body.SHA, "type": "commit"},
	})
}

func (f *FakeGitHub) createPull(w http.ResponseWriter, r *http.Request) {
	full := fullName(r)
	var body struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Head  string `json:"head"`
		Base  string `json:"base"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Contains(f.repos, full) {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	repo := f.repoLocked(full)
	for _, branch := range []string{body.Head, body.Base} {
		if _, ok := repo.branches[branch]; !ok {
			writeError(w, http.StatusUnprocessableEntity, "Branch "+branch+" not found")
			return
		}
	}
	if strings.TrimSpace(body.Title) == "" {
		writeError(w, http.StatusUnprocessableEntity, "title is required")
		return
	}
	repo.pulls++
	number := repo.pulls
	writeJSON(w, http.StatusCreated, map[string]any{
		"number":   number,
		"title":    body.Title,
		"body":     body.Body,
		"head":     map[string]any{"ref": body.Head},
		"base":     map[string]any{"ref": body.Base},
		"html_url": fmt.Sprintf("https://github.com/%s/pull/%d", full, number),
	})
}
