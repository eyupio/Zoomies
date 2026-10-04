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
	SourceBranch    string      `json:"source_branch"`
	Destination     Destination `json:"destination"`
	Exclude         []string    `json:"exclude"`
	KeepSnapshots   int         `json:"keep_snapshots"`
	ReadmeBadge     *bool       `json:"readme_badge,omitempty"` // Legacy settings remain readable.
	SetupGeneration int64       `json:"setup_generation,omitempty"`
	Disabled        bool        `json:"disabled,omitempty"`
	// UploadURL is where a Zoomies-only workflow sends its snapshot, and the
	// audience its OIDC token is minted for. It is part of the hashed config,
	// so a workflow cannot be pointed at another controller without a
	// reviewed change. Only Zoomies-only output carries one.
	UploadURL string `json:"upload_url,omitempty"`
}

// UploadPath is where a controller accepts Zoomies-only uploads.
const UploadPath = "/api/v1/ai-context/uploads"

// UploadURLFor derives the upload address from server.external_url. It is
// empty when the controller has no https address GitHub's runners could
// reach, which is what makes Zoomies-only output unavailable there.
func UploadURLFor(externalURL string) string {
	base := strings.TrimRight(strings.TrimSpace(externalURL), "/")
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	return base + UploadPath
}

// DefaultConfig excludes credential paths and dependencies, then the bulky
// artefacts that most often tip a text file over the generator's 1 MiB limit.
// That limit is a refusal, not a skip, so an unlisted minified bundle, diagram
// export or backup fails the whole run until somebody amends the exclusions.
func DefaultConfig(branch string) Config {
	return Config{SourceBranch: branch, Destination: Both, KeepSnapshots: 3,
		Exclude: []string{"**/.env*", "**/node_modules/**", "**/.git/**", "**/dist/**", "**/vendor/**", "**/*.pem", "**/*.key", "**/*.db", ".zoomies/**",
			"**/*.min.js", "**/*.min.css", "**/*.map", "**/*.drawio", "**/*.log", "**/*.bak"}}
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
	if c.SetupGeneration < 0 || c.SetupGeneration > 1_000_000 {
		return fmt.Errorf("context setup generation is out of range")
	}
	if !ValidBranch(c.SourceBranch) || c.SourceBranch == OutputBranch {
		return fmt.Errorf("choose a valid source branch other than the generated context branch")
	}
	if c.Destination != Repository && c.Destination != Zoomies && c.Destination != Both {
		return fmt.Errorf("choose repository, zoomies or both as the context destination")
	}
	if c.Destination == Zoomies {
		u, err := url.Parse(c.UploadURL)
		if c.UploadURL == "" || len(c.UploadURL) > 512 || err != nil || u.Scheme != "https" || u.Host == "" ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(u.Path, UploadPath) ||
			strings.ContainsAny(c.UploadURL, " \t\r\n\"'") {
			return fmt.Errorf("zoomies-only output needs this controller's https upload address; set server.external_url to an https address GitHub can reach")
		}
	} else if c.UploadURL != "" {
		return fmt.Errorf("only zoomies-only output carries an upload address")
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

// CheckSourceFiles repeats generator exclusions at admission. A valid config
// hash must not reintroduce deliberately excluded files through a forged pack.
func (c Config) CheckSourceFiles(files []File) error {
	patterns := make([]*regexp.Regexp, 0, len(c.Exclude))
	for _, pattern := range c.Exclude {
		patterns = append(patterns, contextGlob(pattern))
	}
	for _, file := range files {
		if !SafeSourcePath(file.Path) {
			return fmt.Errorf("context contains an unsafe source path")
		}
		for _, pattern := range patterns {
			if pattern.MatchString(file.Path) {
				return fmt.Errorf("context contains a file excluded by its approved configuration")
			}
		}
	}
	return nil
}

// CheckSnapshotFiles applies CheckSourceFiles to everything a snapshot names,
// carried or omitted: an excluded path must not reappear as a list entry either.
func (c Config) CheckSnapshotFiles(s *Snapshot) error {
	files := make([]File, 0, len(s.Files)+len(s.Omitted))
	files = append(files, s.Files...)
	for _, o := range s.Omitted {
		files = append(files, File{Path: o.Path})
	}
	return c.CheckSourceFiles(files)
}

// Match Python fnmatchcase, including wildcards crossing directory separators.
// Patterns and paths are bounded before reaching this matcher.
func contextGlob(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("(?s)^")
	optionalDirectories := false
	for strings.HasPrefix(pattern, "**/") {
		optionalDirectories = true
		pattern = strings.TrimPrefix(pattern, "**/")
	}
	if optionalDirectories {
		b.WriteString("(?:.*/)?")
	}
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteByte('.')
		case '[':
			j := i + 1
			if j < len(pattern) && pattern[j] == '!' {
				j++
			}
			if j < len(pattern) && pattern[j] == ']' {
				j++
			}
			for j < len(pattern) && pattern[j] != ']' {
				j++
			}
			if j == len(pattern) {
				b.WriteString(`\[`)
				continue
			}
			group := strings.ReplaceAll(pattern[i+1:j], "]", `\]`)
			if strings.HasPrefix(group, "!") {
				group = "^" + group[1:]
			} else if strings.HasPrefix(group, "^") {
				group = `\^` + group[1:]
			}
			candidate := "[" + group + "]"
			if _, err := regexp.Compile(candidate); err != nil {
				// Invalid ranges fail closed rather than admit excluded source.
				b.WriteString(".*")
			} else {
				b.WriteString(candidate)
			}
			i = j
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	b.WriteByte('$')
	return regexp.MustCompile(b.String())
}
