package aicontext

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxSnapshotBytes = 16 << 20
	MaxSourceBytes   = 12 << 20
	MaxFileBytes     = 1 << 20
	MaxFiles         = 5000
	MaxResponseBytes = 24000
)

type Manifest struct {
	SchemaVersion int           `json:"schema_version"`
	Repository    RepositoryKey `json:"repository"`
	SourceBranch  string        `json:"source_branch"`
	SourceCommit  string        `json:"source_commit"`
	ConfigHash    string        `json:"config_hash"`
	GeneratedAt   time.Time     `json:"generated_at"`
	Manager       string        `json:"manager"`
	Generator     string        `json:"generator"`
}

type File struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Content string `json:"content"`
}

type Snapshot struct {
	Manifest Manifest `json:"manifest"`
	Files    []File   `json:"files"`
}

func Decode(r io.Reader) (*Snapshot, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxSnapshotBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxSnapshotBytes || !utf8.Valid(b) {
		return nil, fmt.Errorf("context exceeds the snapshot limit or is not UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	var s Snapshot
	if err := decoder.Decode(&s); err != nil {
		return nil, fmt.Errorf("read context snapshot: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("context must contain exactly one JSON snapshot")
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *Snapshot) Validate() error {
	if s == nil {
		return fmt.Errorf("context snapshot is missing")
	}
	m := s.Manifest
	if err := m.Repository.Validate(); err != nil {
		return err
	}
	if m.SchemaVersion != SchemaVersion || !commitPattern.MatchString(m.SourceCommit) || !digestPattern.MatchString(m.ConfigHash) {
		return fmt.Errorf("context has an unsupported schema or missing commit/configuration identity")
	}
	if !ValidBranch(m.SourceBranch) || m.SourceBranch == OutputBranch || m.GeneratedAt.IsZero() || m.Manager != "zoomies" || m.Generator == "" || len(m.Generator) > 100 {
		return fmt.Errorf("context must identify its source branch, generation time and Zoomies generator")
	}
	if len(s.Files) == 0 || len(s.Files) > MaxFiles {
		return fmt.Errorf("context must contain between 1 and %d files", MaxFiles)
	}
	seen := make(map[string]bool, len(s.Files))
	var total int
	for _, f := range s.Files {
		if !SafeSourcePath(f.Path) || seen[f.Path] {
			return fmt.Errorf("context contains an unsafe or duplicate file path")
		}
		if len(f.Content) > MaxFileBytes || !utf8.ValidString(f.Content) || strings.ContainsRune(f.Content, 0) || Hash([]byte(f.Content)) != f.SHA256 {
			return fmt.Errorf("context file fails size, text or integrity validation")
		}
		seen[f.Path] = true
		total += len(f.Content)
		if total > MaxSourceBytes {
			return fmt.Errorf("context exceeds the total source limit")
		}
	}
	return nil
}

// Match pins all identities, not just a friendly repository name supplied by
// a workflow. A digest proves byte integrity, not who was allowed to publish.
func (s *Snapshot) Match(key RepositoryKey, branch, commit, configHash string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	m := s.Manifest
	if m.Repository != key || m.SourceBranch != branch || m.SourceCommit != commit || m.ConfigHash != configHash {
		return fmt.Errorf("context does not match the requested repository, branch, commit and configuration")
	}
	return nil
}

type FileSummary struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
	Lines int    `json:"lines"`
}

func (s *Snapshot) Overview() []FileSummary {
	result := make([]FileSummary, 0, len(s.Files))
	for _, f := range s.Files {
		lines := len(strings.Split(f.Content, "\n"))
		if f.Content == "" {
			lines = 0
		}
		result = append(result, FileSummary{f.Path, len(f.Content), lines})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}

type Excerpt struct {
	Path       string `json:"path"`
	Text       string `json:"text"`
	Offset     int    `json:"offset"`
	NextOffset *int   `json:"next_offset"`
}

// Read uses UTF-8 byte offsets and a compact source string. Line-per-object
// encoding triples the cost of broad reviews; metadata belongs to the page.
func (s *Snapshot) Read(p string, offset, budget int) (Excerpt, error) {
	if offset < 0 || budget < 4 || budget > MaxResponseBytes {
		return Excerpt{}, fmt.Errorf("use a non-negative offset and a response budget between 4 and %d bytes", MaxResponseBytes)
	}
	for _, f := range s.Files {
		if f.Path != p {
			continue
		}
		if offset > len(f.Content) || (offset < len(f.Content) && !utf8.RuneStart(f.Content[offset])) {
			return Excerpt{}, fmt.Errorf("source offset must be a UTF-8 boundary inside the file")
		}
		end := min(offset+budget, len(f.Content))
		for end < len(f.Content) && !utf8.RuneStart(f.Content[end]) {
			end--
		}
		result := Excerpt{Path: p, Text: f.Content[offset:end], Offset: offset}
		if end < len(f.Content) {
			result.NextOffset = &end
		}
		return result, nil
	}
	return Excerpt{}, fmt.Errorf("file is not in this snapshot")
}

type Match struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type SearchPage struct {
	Matches    []Match `json:"matches"`
	NextOffset *int    `json:"next_offset"`
}

// Search is literal and bounded. Source text is never a regular expression
// or a command, and one broad query cannot expand into the whole repository.
func (s *Snapshot) Search(query, prefix string, offset, limit int) (SearchPage, error) {
	if strings.TrimSpace(query) == "" || len(query) > 128 || !utf8.ValidString(query) || offset < 0 || offset > 1000000 || limit < 1 || limit > 12 {
		return SearchPage{}, fmt.Errorf("use a literal query of 1–128 bytes, a non-negative offset and 1–12 matches")
	}
	if prefix != "" && (!SafeSourcePath(strings.TrimSuffix(prefix, "/"))) {
		return SearchPage{}, fmt.Errorf("search prefix must stay inside the repository")
	}
	files := append([]File(nil), s.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	result := SearchPage{Matches: make([]Match, 0, limit)}
	query = strings.ToLower(query)
	seen := 0
	for _, file := range files {
		if !strings.HasPrefix(file.Path, prefix) {
			continue
		}
		for number, line := range strings.Split(file.Content, "\n") {
			if !strings.Contains(strings.ToLower(line), query) {
				continue
			}
			if seen < offset {
				seen++
				continue
			}
			if len(result.Matches) == limit {
				next := offset + len(result.Matches)
				result.NextOffset = &next
				return result, nil
			}
			// Rune windows preserve UTF-8, including on a minified source line.
			runes := []rune(line)
			lower := []rune(strings.ToLower(line))
			at := strings.Index(string(lower), query)
			start := 0
			if at > 0 {
				start = max(0, utf8.RuneCountInString(string(lower)[:at])-40)
			}
			end := min(len(runes), start+160)
			result.Matches = append(result.Matches, Match{file.Path, number + 1, string(runes[start:end])})
		}
	}
	return result, nil
}
