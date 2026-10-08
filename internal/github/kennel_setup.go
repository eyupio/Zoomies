package github

import (
	"context"
	"fmt"
	"path"
	"strings"
)

// KennelSetupReader is optional: clients without Contents access still provide
// exposure and capacity facts, and never need a new write capability.
type KennelSetupReader interface {
	KennelSetup(context.Context, string, string) (*KennelSetup, error)
}

// KennelSetup is a file inventory, not a judgement about the files' contents.
// No raw path or content survives the reduction at this boundary.
type KennelSetup struct {
	Present         map[string]bool
	HasDependencies bool
	Partial         bool
}

func (c *appClient) KennelSetup(ctx context.Context, repo, branch string) (*KennelSetup, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	if branch == "" {
		return nil, fmt.Errorf("github: repository has no default branch")
	}
	tree, resp, err := c.readClient().Git.GetTree(ctx, owner, name, branch, true)
	if err != nil {
		return nil, c.fail("read repository setup tree", resp, err)
	}
	if tree == nil || tree.GetSHA() == "" {
		return nil, fmt.Errorf("github: setup tree has no identity")
	}
	out := &KennelSetup{Present: map[string]bool{}, Partial: tree.GetTruncated()}
	// GitHub caps recursive trees at 100,000 entries. A smaller local cap bounds
	// inspection too; any omitted path leaves the inventory incomplete.
	entries := tree.Entries
	if len(entries) > 10000 {
		entries = entries[:10000]
		out.Partial = true
	}
	for _, e := range entries {
		if e.GetType() != "blob" || (e.GetMode() != "100644" && e.GetMode() != "100755") || e.GetSize() <= 0 {
			continue
		}
		p := e.GetPath()
		base := strings.ToLower(path.Base(p))
		dir := path.Dir(p)
		community := dir == "." || dir == ".github" || dir == "docs"
		if community {
			stem := strings.TrimSuffix(base, path.Ext(base))
			switch stem {
			case "readme":
				out.Present["readme"] = true
			case "license", "licence", "copying":
				if dir == "." {
					out.Present["licence"] = true
				}
			case "security":
				out.Present["security"] = true
			case "contributing":
				out.Present["contributing"] = true
			case "code_of_conduct":
				out.Present["code_of_conduct"] = true
			case "pull_request_template":
				out.Present["pull_request_template"] = true
			case "issue_template":
				out.Present["issue_template"] = true
			}
		}
		// CODEOWNERS and Actions paths are case-sensitive on GitHub.
		if p == ".github/CODEOWNERS" || p == "CODEOWNERS" || p == "docs/CODEOWNERS" {
			out.Present["codeowners"] = true
		}
		if dir == ".github/ISSUE_TEMPLATE" && (strings.HasSuffix(base, ".md") || strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml")) && base != "config.yml" && base != "config.yaml" {
			out.Present["issue_template"] = true
		}
		if (dir == ".github/PULL_REQUEST_TEMPLATE" || dir == "docs/PULL_REQUEST_TEMPLATE" || dir == "PULL_REQUEST_TEMPLATE") && strings.HasSuffix(base, ".md") {
			out.Present["pull_request_template"] = true
		}
		if dir == ".github/workflows" && (strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".yaml")) {
			out.Present["workflows"] = true
			out.HasDependencies = true
		}
		switch p {
		case ".github/dependabot.yml", ".github/dependabot.yaml", "renovate.json", "renovate.json5", ".renovaterc", ".renovaterc.json", ".github/renovate.json", ".github/renovate.json5":
			out.Present["dependency_updates"] = true
		}
		// Root manifests avoid treating vendored packages and example projects as
		// an obligation to run an update bot for this repository.
		if dir == "." {
			switch base {
			case "go.mod", "package.json", "cargo.toml", "pyproject.toml", "requirements.txt", "pipfile", "gemfile", "composer.json", "pom.xml", "build.gradle", "build.gradle.kts", "mix.exs", "pubspec.yaml", "packages.config":
				out.HasDependencies = true
			}
		}
	}
	return out, nil
}
