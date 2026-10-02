// Package aicontext defines commit-pinned context, independently of the fleet.
package aicontext

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	WorkflowPath    = ".github/workflows/zoomies-ai-context.yml"
	ConfigPath      = "zoomies-ai-context.config.json"
	OutputBranch    = "zoomies-ai-context"
	OutputDirectory = ".zoomies/ai-context"
	ArtifactName    = "zoomies-ai-context"
	SchemaVersion   = 1
)

type Destination string

const (
	Repository Destination = "repository"
	Zoomies    Destination = "zoomies"
	Both       Destination = "both"
)

// RepositoryKey is stable across renames and distinct across Enterprise hosts.
type RepositoryKey struct {
	GitHubHost     string `json:"github_host"`
	InstallationID string `json:"installation_id"`
	RepositoryID   int64  `json:"repository_id"`
}

func (k RepositoryKey) Validate() error {
	u, err := url.Parse("https://" + k.GitHubHost)
	if err != nil || u.Host != k.GitHubHost || u.User != nil || u.Path != "" ||
		u.RawQuery != "" || u.Fragment != "" || k.GitHubHost == "" ||
		k.GitHubHost != strings.ToLower(k.GitHubHost) || strings.ContainsAny(k.GitHubHost, "\\ \t\r\n") {
		return fmt.Errorf("name the GitHub host without a scheme, path or credentials")
	}
	if k.InstallationID == "" || len(k.InstallationID) > 100 || k.RepositoryID <= 0 {
		return fmt.Errorf("context needs a known installation and GitHub repository ID")
	}
	return nil
}

type Config struct {
	SourceBranch  string      `json:"source_branch"`
	Destination   Destination `json:"destination"`
	Exclude       []string    `json:"exclude"`
	KeepSnapshots int         `json:"keep_snapshots"`
}

func DefaultConfig(branch string) Config {
	return Config{SourceBranch: branch, Destination: Both, KeepSnapshots: 3,
		Exclude: []string{"**/.env*", "**/node_modules/**", "**/.git/**", "**/dist/**", "**/vendor/**", "**/*.pem", "**/*.key", "**/*.db", ".zoomies/**"}}
}

func ValidBranch(s string) bool {
	if s == "" || len(s) > 255 || s == "@" || strings.HasPrefix(s, "-") || strings.HasSuffix(s, ".") ||
		strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") || strings.ContainsAny(s, " ~^:?*[\\\t\r\n") ||
		strings.Contains(s, "..") || strings.Contains(s, "@{") || strings.Contains(s, "//") || !utf8.ValidString(s) {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

func (c Config) Validate() error {
	if !ValidBranch(c.SourceBranch) || c.SourceBranch == OutputBranch {
		return fmt.Errorf("choose a valid source branch other than the generated context branch")
	}
	if c.Destination != Repository && c.Destination != Zoomies && c.Destination != Both {
		return fmt.Errorf("choose repository, zoomies or both as the context destination")
	}
	if c.KeepSnapshots < 1 || c.KeepSnapshots > 100 {
		return fmt.Errorf("keep between 1 and 100 context snapshots")
	}
	if len(c.Exclude) > 100 {
		return fmt.Errorf("use at most 100 exclusion patterns")
	}
	for _, pattern := range c.Exclude {
		if pattern == "" || len(pattern) > 256 || !utf8.ValidString(pattern) || strings.ContainsAny(pattern, "\x00\r\n\\") || strings.HasPrefix(pattern, "/") {
			return fmt.Errorf("exclusions must be relative single-line patterns of at most 256 characters")
		}
		for _, part := range strings.Split(pattern, "/") {
			if part == ".." {
				return fmt.Errorf("exclusions cannot leave the repository")
			}
		}
	}
	return nil
}

func (c Config) Hash() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return Hash(b), nil
}

func Hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// SafeSourcePath is defence in depth after generator exclusions. It refuses
// common credential/container-state files, not every possible secret.
func SafeSourcePath(p string) bool {
	if p == "" || len(p) > 1024 || !utf8.ValidString(p) || path.Clean(p) != p ||
		strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\:\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		lower := strings.ToLower(part)
		if part == "." || part == ".." || lower == ".git" || lower == "node_modules" || lower == ".zoomies" || strings.HasPrefix(lower, ".env") {
			return false
		}
	}
	for _, suffix := range []string{".pem", ".key", ".p12", ".pfx", ".db", ".sqlite", ".sqlite3"} {
		if strings.HasSuffix(strings.ToLower(p), suffix) {
			return false
		}
	}
	return true
}
