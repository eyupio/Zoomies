package github

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/eyupio/zoomies/internal/prrepair"
	gh "github.com/google/go-github/v88/github"
)

// ErrRepairUnsupported identifies a run that cannot be admitted as a PR repair.
var ErrRepairUnsupported = errors.New("unsupported PR repair target")

type RepairActor struct {
	ID       int64
	Login    string
	CanWrite bool
}
type RepairPull struct {
	Number        int
	AuthorID      int64
	Author        string
	Branch        string
	HeadSHA       string
	Title         string
	Body          string
	URL           string
	DefaultBranch string
	Fork          bool
	Open          bool
}
type RepairClient interface {
	RepairActor(context.Context, string, string) (RepairActor, error)
	RepairPull(context.Context, string, int) (RepairPull, error)
	RepairPullForRun(context.Context, string, int64) (RepairPull, error)
	RepairSource(context.Context, string, RepairPull, bool) (*RepairSource, error)
	RepairFailures(context.Context, string, RepairPull, int64) ([]string, error)
	CommitRepair(context.Context, string, RepairPull, prrepair.Plan) (string, error)
	RepairComment(context.Context, string, int, int64, string) (int64, error)
	RepairChecks(context.Context, string, string) (string, error)
}
type RepairSource struct {
	Snapshot prrepair.Snapshot
	files    map[string]repairTreeEntry
	client   *appClient
	repo     string
	commit   string
}
type repairTreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

func (c *appClient) repairRequest(ctx context.Context, method, p string, body, out any) error {
	req, err := c.asInstallation.NewRequest(ctx, method, p, body)
	if err != nil {
		return err
	}
	resp, err := c.asInstallation.Do(req, out)
	if err != nil {
		return c.fail("Eli GitHub operation", resp, err)
	}
	return nil
}
func repairRepo(repo string) (string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}
	return "repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}
func (c *appClient) RepairActor(ctx context.Context, repo, login string) (RepairActor, error) {
	base, err := repairRepo(repo)
	if err != nil {
		return RepairActor{}, err
	}
	var v struct {
		Permission string `json:"permission"`
		User       struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		} `json:"user"`
	}
	err = c.repairRequest(ctx, http.MethodGet, base+"/collaborators/"+url.PathEscape(login)+"/permission", nil, &v)
	return RepairActor{ID: v.User.ID, Login: v.User.Login, CanWrite: v.Permission == "write" || v.Permission == "maintain" || v.Permission == "admin"}, err
}
func (c *appClient) RepairPull(ctx context.Context, repo string, number int) (RepairPull, error) {
	base, err := repairRepo(repo)
	if err != nil {
		return RepairPull{}, err
	}
	var p struct {
		Number  int    `json:"number"`
		State   string `json:"state"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		User    struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo struct {
				FullName      string `json:"full_name"`
				DefaultBranch string `json:"default_branch"`
			} `json:"repo"`
		} `json:"head"`
	}
	if err := c.repairRequest(ctx, http.MethodGet, base+"/pulls/"+strconv.Itoa(number), nil, &p); err != nil {
		return RepairPull{}, err
	}
	out := RepairPull{Number: p.Number, AuthorID: p.User.ID, Author: p.User.Login, Branch: p.Head.Ref, HeadSHA: p.Head.SHA, Title: p.Title, Body: p.Body, URL: p.HTMLURL, DefaultBranch: p.Head.Repo.DefaultBranch, Fork: p.Head.Repo.FullName != repo, Open: p.State == "open"}
	if out.Number <= 0 || !out.Open || out.Fork || out.HeadSHA == "" || out.Branch == "" || out.Branch == out.DefaultBranch {
		return out, fmt.Errorf("%w: an Eli repair needs an open PR on a non-default branch in this repository; forks are not writable", ErrRepairUnsupported)
	}
	return out, nil
}
func (c *appClient) RepairPullForRun(ctx context.Context, repo string, run int64) (RepairPull, error) {
	base, err := repairRepo(repo)
	if err != nil {
		return RepairPull{}, err
	}
	var r struct {
		HeadSHA        string `json:"head_sha"`
		HeadRepository struct {
			FullName string `json:"full_name"`
		} `json:"head_repository"`
		Pulls []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	}
	if err := c.repairRequest(ctx, http.MethodGet, fmt.Sprintf("%s/actions/runs/%d", base, run), nil, &r); err != nil {
		return RepairPull{}, err
	}
	if r.HeadRepository.FullName != repo {
		return RepairPull{}, fmt.Errorf("%w: fork workflow runs are not writable", ErrRepairUnsupported)
	}
	if len(r.Pulls) != 1 {
		return RepairPull{}, fmt.Errorf("%w: the failed run must identify exactly one pull request", ErrRepairUnsupported)
	}
	p, err := c.RepairPull(ctx, repo, r.Pulls[0].Number)
	if err != nil {
		return p, err
	}
	if p.HeadSHA != r.HeadSHA {
		return p, ErrRepairUnsupported
	}
	return p, nil
}
func (c *appClient) RepairSource(ctx context.Context, repo string, p RepairPull, workflows bool) (*RepairSource, error) {
	base, err := repairRepo(repo)
	if err != nil {
		return nil, err
	}
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := c.repairRequest(ctx, http.MethodGet, base+"/git/commits/"+p.HeadSHA, nil, &commit); err != nil {
		return nil, err
	}
	var tree struct {
		Truncated bool              `json:"truncated"`
		Tree      []repairTreeEntry `json:"tree"`
	}
	if err := c.repairRequest(ctx, http.MethodGet, base+"/git/trees/"+commit.Tree.SHA+"?recursive=1", nil, &tree); err != nil {
		return nil, err
	}
	if tree.Truncated || len(tree.Tree) > 10000 {
		return nil, fmt.Errorf("repository inventory exceeds Eli's repair limit")
	}
	source := &RepairSource{client: c, repo: repo, commit: p.HeadSHA, files: map[string]repairTreeEntry{}, Snapshot: prrepair.Snapshot{Title: p.Title, Body: prrepair.Redact(p.Body[:min(len(p.Body), 8000)]), Branch: p.Branch, HeadSHA: p.HeadSHA}}
	for _, f := range tree.Tree {
		source.files[f.Path] = f
		if f.Type == "blob" && (f.Mode == "100644" || f.Mode == "100755") && prrepair.AllowedPath(f.Path, workflows) {
			source.Snapshot.Inventory = append(source.Snapshot.Inventory, f.Path)
		}
	}
	var changed []struct {
		Filename string `json:"filename"`
		Status   string `json:"status"`
	}
	if err := c.repairRequest(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d/files?per_page=100", base, p.Number), nil, &changed); err != nil {
		return nil, err
	}
	if len(changed) >= 100 {
		return nil, fmt.Errorf("the PR exceeds Eli's changed-file limit")
	}
	total := 0
	for _, f := range changed {
		if f.Status == "removed" || !prrepair.AllowedPath(f.Filename, workflows) {
			continue
		}
		file, err := source.ReadFile(ctx, f.Filename)
		if err != nil {
			return nil, err
		}
		total += len(file.Content)
		if total > prrepair.MaxSourceBytes {
			return nil, fmt.Errorf("PR source exceeds Eli's repair limit")
		}
		source.Snapshot.Files = append(source.Snapshot.Files, file)
	}
	return source, nil
}
func (s *RepairSource) ReadFile(ctx context.Context, name string) (prrepair.File, error) {
	// Git blobs never resolve symlinks; every parent must still be a tree.
	parts := strings.Split(name, "/")
	for i := 1; i < len(parts); i++ {
		if e, ok := s.files[strings.Join(parts[:i], "/")]; ok && e.Type != "tree" {
			return prrepair.File{}, fmt.Errorf("source path crosses a non-directory")
		}
	}
	e, ok := s.files[name]
	if !ok {
		return prrepair.File{Path: name, Mode: "100644"}, nil
	}
	if e.Type != "blob" || e.Mode != "100644" && e.Mode != "100755" {
		return prrepair.File{}, fmt.Errorf("source path is not a regular file")
	}
	base, _ := repairRepo(s.repo)
	var blob struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		Size     int    `json:"size"`
	}
	if err := s.client.repairRequest(ctx, http.MethodGet, base+"/git/blobs/"+e.SHA, nil, &blob); err != nil {
		return prrepair.File{}, err
	}
	if blob.Size > prrepair.MaxFileBytes || blob.Encoding != "base64" {
		return prrepair.File{}, fmt.Errorf("source file is too large or is not supported text")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(blob.Content, "\n", ""))
	if err != nil || len(raw) > prrepair.MaxFileBytes {
		return prrepair.File{}, fmt.Errorf("source file could not be decoded within the limit")
	}
	return prrepair.File{Path: name, Content: string(raw), Mode: e.Mode}, nil
}
func (c *appClient) RepairFailures(ctx context.Context, repo string, p RepairPull, jobID int64) ([]string, error) {
	if jobID > 0 {
		return c.ContextJobErrors(ctx, repo, jobID)
	}
	base, err := repairRepo(repo)
	if err != nil {
		return nil, err
	}
	var runs struct {
		Runs []struct {
			ID      int64  `json:"id"`
			HeadSHA string `json:"head_sha"`
			Status  string `json:"status"`
		} `json:"workflow_runs"`
	}
	if err := c.repairRequest(ctx, http.MethodGet, base+"/actions/runs?head_sha="+p.HeadSHA+"&per_page=10", nil, &runs); err != nil {
		return nil, err
	}
	out := []string{}
	for _, r := range runs.Runs {
		if r.HeadSHA != p.HeadSHA {
			continue
		}
		var jobs struct {
			Jobs []struct {
				ID         int64  `json:"id"`
				Name       string `json:"name"`
				Conclusion string `json:"conclusion"`
			} `json:"jobs"`
		}
		if err := c.repairRequest(ctx, http.MethodGet, fmt.Sprintf("%s/actions/runs/%d/jobs?per_page=100", base, r.ID), nil, &jobs); err != nil {
			return nil, err
		}
		for _, j := range jobs.Jobs {
			if j.Conclusion != "failure" && j.Conclusion != "timed_out" {
				continue
			}
			lines, err := c.ContextJobErrors(ctx, repo, j.ID)
			if err != nil {
				return nil, err
			}
			out = append(out, prrepair.Redact(j.Name+": "+strings.Join(lines, "\n")))
			if len(out) >= 5 {
				return out, nil
			}
		}
	}
	return out, nil
}
func (c *appClient) CommitRepair(ctx context.Context, repo string, p RepairPull, plan prrepair.Plan) (string, error) {
	fresh, err := c.RepairPull(ctx, repo, p.Number)
	if err != nil {
		return "", err
	}
	if fresh.HeadSHA != p.HeadSHA || fresh.Branch != p.Branch {
		return "", ErrSetupConflict
	}
	owner, name, _ := splitRepo(repo)
	parent, resp, err := c.asInstallation.Git.GetCommit(ctx, owner, name, p.HeadSHA)
	if err != nil {
		return "", c.fail("read repair parent", resp, err)
	}
	entries := []*gh.TreeEntry{}
	for _, f := range plan.Files {
		mode := f.Mode
		if mode == "" {
			mode = "100644"
		}
		entries = append(entries, &gh.TreeEntry{Path: gh.Ptr(f.Path), Mode: gh.Ptr(mode), Type: gh.Ptr("blob"), Content: gh.Ptr(f.Content)})
	}
	tree, resp, err := c.asInstallation.Git.CreateTree(ctx, owner, name, parent.GetTree().GetSHA(), entries)
	if err != nil {
		return "", c.fail("write repair tree", resp, err)
	}
	commit, resp, err := c.asInstallation.Git.CreateCommit(ctx, owner, name, gh.Commit{Message: gh.Ptr("Fix PR checks with Eli"), Tree: tree, Parents: []*gh.Commit{{SHA: gh.Ptr(p.HeadSHA)}}}, nil)
	if err != nil {
		return "", c.fail("write repair commit", resp, err)
	}
	// A concurrent push makes this divergent, so GitHub refuses it. Force is always false.
	_, resp, err = c.asInstallation.Git.UpdateRef(ctx, owner, name, "heads/"+p.Branch, gh.UpdateRef{SHA: commit.GetSHA(), Force: gh.Ptr(false)})
	if err != nil {
		return "", c.fail("advance repair branch without forcing", resp, err)
	}
	return commit.GetSHA(), nil
}
func (c *appClient) RepairComment(ctx context.Context, repo string, pull int, id int64, text string) (int64, error) {
	base, err := repairRepo(repo)
	if err != nil {
		return 0, err
	}
	method := http.MethodPost
	endpoint := fmt.Sprintf("%s/issues/%d/comments", base, pull)
	if id > 0 {
		method = http.MethodPatch
		endpoint = fmt.Sprintf("%s/issues/comments/%d", base, id)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	err = c.repairRequest(ctx, method, endpoint, map[string]string{"body": text}, &out)
	return out.ID, err
}
func (c *appClient) RepairChecks(ctx context.Context, repo, sha string) (string, error) {
	base, err := repairRepo(repo)
	if err != nil {
		return "", err
	}
	var checks struct {
		Total int `json:"total_count"`
		Runs  []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if err := c.repairRequest(ctx, http.MethodGet, base+"/commits/"+sha+"/check-runs?per_page=100", nil, &checks); err != nil {
		return "", err
	}
	var statuses struct {
		Total int    `json:"total_count"`
		State string `json:"state"`
	}
	if err := c.repairRequest(ctx, http.MethodGet, base+"/commits/"+url.PathEscape(sha)+"/status", nil, &statuses); err != nil {
		return "", err
	}
	if statuses.Total > 0 && (statuses.State == "failure" || statuses.State == "error") {
		return "failed", nil
	}
	if checks.Total == 0 && statuses.Total == 0 || statuses.Total > 0 && statuses.State != "success" {
		return "waiting", nil
	}
	if checks.Total > len(checks.Runs) {
		return "waiting", nil
	}
	for _, r := range checks.Runs {
		if r.Status != "completed" {
			return "waiting", nil
		}
		if r.Conclusion != "success" && r.Conclusion != "neutral" && r.Conclusion != "skipped" {
			return "failed", nil
		}
	}
	return "passed", nil
}

var _ RepairClient = (*appClient)(nil)
