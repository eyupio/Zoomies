package github

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	gh "github.com/google/go-github/v88/github"
)

// Git objects are immutable in the fake, too. Reads pinned to yesterday's
// commit must not see files somebody added to the default branch today.
type fakeContextGit struct {
	Trees                map[string]*gh.Tree
	Commits              map[string]*gh.Commit
	Blobs                map[string]string
	Modes                map[string]string
	Pulls                []*gh.PullRequest
	LoseNextPullResponse bool
}

func (r *fakeRepo) contextGit() *fakeContextGit {
	if r.git == nil {
		r.git = &fakeContextGit{Trees: map[string]*gh.Tree{}, Commits: map[string]*gh.Commit{}, Blobs: map[string]string{}, Modes: map[string]string{}}
	}
	return r.git
}

func (g *fakeContextGit) treeOf(files map[string]*gh.TreeEntry) *gh.Tree {
	groups := map[string]map[string]*gh.TreeEntry{}
	entries := []*gh.TreeEntry{}
	for p, e := range files {
		first, rest, nested := strings.Cut(p, "/")
		if !nested {
			copy := *e
			copy.Path = gh.Ptr(p)
			entries = append(entries, &copy)
			continue
		}
		if groups[first] == nil {
			groups[first] = map[string]*gh.TreeEntry{}
		}
		groups[first][rest] = e
	}
	for p, children := range groups {
		tree := g.treeOf(children)
		entries = append(entries, &gh.TreeEntry{Path: gh.Ptr(p), Type: gh.Ptr("tree"), Mode: gh.Ptr("040000"), SHA: tree.SHA})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].GetPath() < entries[j].GetPath() })
	encoded, _ := json.Marshal(entries)
	sha := blobSHA(string(encoded))
	tree := &gh.Tree{SHA: gh.Ptr(sha), Entries: entries}
	g.Trees[sha] = tree
	return tree
}

func (g *fakeContextGit) flatten(sha string, prefix string, out map[string]*gh.TreeEntry) {
	tree := g.Trees[sha]
	if tree == nil {
		return
	}
	for _, e := range tree.Entries {
		p := prefix + e.GetPath()
		if e.GetType() == "tree" {
			g.flatten(e.GetSHA(), p+"/", out)
		} else {
			copy := *e
			copy.Path = gh.Ptr(p)
			out[p] = &copy
		}
	}
}

func (r *fakeRepo) pinContextSource() {
	g := r.contextGit()
	files := map[string]*gh.TreeEntry{}
	for p, c := range r.files {
		sha := blobSHA(c)
		g.Blobs[sha] = c
		mode := g.Modes[p]
		if mode == "" {
			mode = "100644"
		}
		files[p] = &gh.TreeEntry{Path: gh.Ptr(p), Mode: gh.Ptr(mode), Type: gh.Ptr("blob"), SHA: gh.Ptr(sha), Size: gh.Ptr(len(c))}
	}
	tree := g.treeOf(files)
	sha := blobSHA("source:" + tree.GetSHA())
	g.Commits[sha] = &gh.Commit{SHA: gh.Ptr(sha), Tree: tree, Message: gh.Ptr("Source commit")}
	r.branches[r.defaultBranch] = sha
}

func (f *FakeGitHub) SetFileMode(repo, path, mode string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repoLocked(repo).contextGit().Modes[path] = mode
}

func (f *FakeGitHub) registerContextGitRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /repos/{owner}/{repo}/git/commits/{sha}", f.contextGitRead)
	mux.HandleFunc("GET /repos/{owner}/{repo}/git/trees/{sha}", f.contextGitRead)
	mux.HandleFunc("GET /repos/{owner}/{repo}/git/blobs/{sha}", f.contextGitRead)
	mux.HandleFunc("POST /repos/{owner}/{repo}/git/trees", f.contextCreateTree)
	mux.HandleFunc("POST /repos/{owner}/{repo}/git/commits", f.contextCreateCommit)
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls", f.contextListPulls)
}

func (f *FakeGitHub) contextGitRead(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.canReadContentsLocked() {
		writeError(w, 404, "Not Found")
		return
	}
	repo := f.repoLocked(fullName(r))
	g := repo.contextGit()
	sha := r.PathValue("sha")
	switch {
	case strings.Contains(r.URL.Path, "/git/commits/"):
		if obj := g.Commits[sha]; obj != nil {
			writeJSON(w, 200, obj)
			return
		}
	case strings.Contains(r.URL.Path, "/git/trees/"):
		if obj := g.Trees[sha]; obj != nil {
			writeJSON(w, 200, obj)
			return
		}
	case strings.Contains(r.URL.Path, "/git/blobs/"):
		if content, ok := g.Blobs[sha]; ok {
			writeJSON(w, 200, map[string]any{"sha": sha, "encoding": "base64", "size": len(content), "content": base64.StdEncoding.EncodeToString([]byte(content))})
			return
		}
	}
	writeError(w, 404, "Not Found")
}

func (f *FakeGitHub) contextCreateTree(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Base    string          `json:"base_tree"`
		Entries []*gh.TreeEntry `json:"tree"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeError(w, 400, "invalid body")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	repo := f.repoLocked(fullName(r))
	g := repo.contextGit()
	if repo.archived || f.permissions["contents"] != "write" {
		writeError(w, 403, "Missing Contents write")
		return
	}
	files := map[string]*gh.TreeEntry{}
	g.flatten(in.Base, "", files)
	for _, e := range in.Entries {
		if strings.HasPrefix(e.GetPath(), ".github/workflows/") && f.permissions["workflows"] != "write" {
			writeError(w, 403, "Missing Workflows write")
			return
		}
		if e.Content != nil {
			sha := blobSHA(e.GetContent())
			g.Blobs[sha] = e.GetContent()
			e.SHA = gh.Ptr(sha)
			e.Size = gh.Ptr(len(e.GetContent()))
			e.Content = nil
		}
		files[e.GetPath()] = e
	}
	writeJSON(w, 201, g.treeOf(files))
}

func (f *FakeGitHub) contextCreateCommit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Message string   `json:"message"`
		Tree    string   `json:"tree"`
		Parents []string `json:"parents"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeError(w, 400, "invalid body")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	repo := f.repoLocked(fullName(r))
	g := repo.contextGit()
	if repo.archived || f.permissions["contents"] != "write" {
		writeError(w, 403, "Missing Contents write")
		return
	}
	if g.Trees[in.Tree] == nil {
		writeError(w, 422, "unknown tree")
		return
	}
	encoded, _ := json.Marshal(in)
	sha := blobSHA(string(encoded))
	obj := &gh.Commit{SHA: gh.Ptr(sha), Message: gh.Ptr(in.Message), Tree: g.Trees[in.Tree]}
	for _, p := range in.Parents {
		obj.Parents = append(obj.Parents, &gh.Commit{SHA: gh.Ptr(p)})
	}
	g.Commits[sha] = obj
	writeJSON(w, 201, obj)
}

func (f *FakeGitHub) contextListPulls(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*gh.PullRequest
	for _, pr := range f.repoLocked(fullName(r)).contextGit().Pulls {
		_, head, _ := strings.Cut(r.URL.Query().Get("head"), ":")
		if pr.GetHead().GetRef() == head && pr.GetBase().GetRef() == r.URL.Query().Get("base") {
			out = append(out, pr)
		}
	}
	if out == nil {
		out = []*gh.PullRequest{}
	}
	writeJSON(w, 200, out)
}

func (f *FakeGitHub) LoseNextContextPullResponse(repo string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repoLocked(repo).contextGit().LoseNextPullResponse = true
}
