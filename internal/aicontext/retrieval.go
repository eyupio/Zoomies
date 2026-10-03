package aicontext

import (
	"encoding/json"
	"fmt"
)

// Page repeats identity once. Offsets on later pages require this commit so a
// refresh cannot silently stitch two different versions into editing evidence.
type Page struct {
	Commit     string        `json:"commit"`
	Snapshot   string        `json:"snapshot"`
	Files      []FileSummary `json:"files,omitempty"`
	Excerpts   []Excerpt     `json:"excerpts,omitempty"`
	Matches    []Match       `json:"matches,omitempty"`
	NextOffset *int          `json:"next_offset,omitempty"`
	Total      int           `json:"total,omitempty"`
}

func EncodedPage(p Page, budget int) ([]byte, error) {
	if budget < 1024 || budget > MaxResponseBytes {
		return nil, fmt.Errorf("use an encoded response budget between 1024 and %d bytes", MaxResponseBytes)
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if len(b) > budget {
		return nil, fmt.Errorf("response exceeds the encoded budget; choose fewer files or a larger budget")
	}
	return b, nil
}

func (s *Snapshot) FilePage(digest string, offset, limit, budget int) ([]byte, error) {
	if offset < 0 || limit < 1 || limit > 100 {
		return nil, fmt.Errorf("use a non-negative offset and 1–100 files")
	}
	files := s.Overview()
	if offset > len(files) {
		return nil, fmt.Errorf("offset is outside this snapshot")
	}
	p := Page{Commit: s.Manifest.SourceCommit, Snapshot: digest, Total: len(files)}
	end := min(offset+limit, len(files))
	for end >= offset {
		p.Files = files[offset:end]
		p.NextOffset = nil
		if end < len(files) {
			next := end
			p.NextOffset = &next
		}
		if b, err := EncodedPage(p, budget); err == nil {
			if end == offset && end < len(files) {
				break
			}
			return b, nil
		}
		end--
	}
	return nil, fmt.Errorf("response budget is too small for this file metadata")
}

func (s *Snapshot) ReadPage(digest string, paths []string, offset, budget int) ([]byte, error) {
	if len(paths) < 1 || len(paths) > 6 || (len(paths) > 1 && offset != 0) {
		return nil, fmt.Errorf("choose 1–6 files; byte offsets apply to a single file")
	}
	seen := map[string]bool{}
	p := Page{Commit: s.Manifest.SourceCommit, Snapshot: digest}
	for _, path := range paths {
		if !SafeSourcePath(path) || seen[path] {
			return nil, fmt.Errorf("choose unique safe source paths")
		}
		seen[path] = true
		excerpt, err := s.Read(path, offset, 4)
		if err != nil {
			return nil, err
		}
		p.Excerpts = append(p.Excerpts, excerpt)
	}
	// Budget the fully escaped envelope, not just the source bytes. Round-robin
	// growth gives each selected file evidence even if the first file is huge.
	for chunk := 1024; chunk >= 4; chunk /= 2 {
		for {
			changed := false
			for i, path := range paths {
				old := p.Excerpts[i]
				if old.NextOffset == nil {
					continue
				}
				next, err := s.Read(path, offset, min(MaxResponseBytes, len(old.Text)+chunk))
				if err != nil {
					return nil, err
				}
				if next.Text == old.Text {
					continue
				}
				p.Excerpts[i] = next
				if _, err := EncodedPage(p, budget); err != nil {
					p.Excerpts[i] = old
					continue
				}
				changed = true
			}
			if !changed {
				break
			}
		}
	}
	return EncodedPage(p, budget)
}

func (s *Snapshot) SearchResult(digest, query, prefix string, offset, limit, budget int) ([]byte, error) {
	result, err := s.Search(query, prefix, offset, limit)
	if err != nil {
		return nil, err
	}
	p := Page{Commit: s.Manifest.SourceCommit, Snapshot: digest, Matches: result.Matches, NextOffset: result.NextOffset}
	for {
		if b, err := EncodedPage(p, budget); err == nil {
			return b, nil
		}
		if len(p.Matches) <= 1 {
			return nil, fmt.Errorf("response budget is too small for this search match")
		}
		p.Matches = p.Matches[:len(p.Matches)-1]
		next := offset + len(p.Matches)
		p.NextOffset = &next
	}
}
