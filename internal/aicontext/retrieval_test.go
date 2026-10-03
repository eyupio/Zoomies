package aicontext

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSourcePagesBoundEscapedBytesAndPreserveUTF8Continuations(t *testing.T) {
	text := strings.Repeat("\x01\"\\<😀\n", 5000)
	s := &Snapshot{Manifest: Manifest{SourceCommit: strings.Repeat("a", 40)}, Files: []File{{Path: "src/main.go", Content: text}}}
	var reconstructed strings.Builder
	offset := 0
	for {
		b, err := s.ReadPage("digest", []string{"src/main.go"}, offset, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) > 1024 {
			t.Fatal("encoded budget exceeded")
		}
		var page Page
		if err := json.Unmarshal(b, &page); err != nil {
			t.Fatal(err)
		}
		excerpt := page.Excerpts[0]
		if !utf8.ValidString(excerpt.Text) || excerpt.Offset != offset {
			t.Fatal("invalid continuation")
		}
		reconstructed.WriteString(excerpt.Text)
		if excerpt.NextOffset == nil {
			break
		}
		if *excerpt.NextOffset <= offset {
			t.Fatal("no progress")
		}
		offset = *excerpt.NextOffset
	}
	if reconstructed.String() != text {
		t.Fatal("continuations lost source bytes")
	}
}

func TestSourceMetadataAndSearchPagesAdvanceAtEncodedBoundaries(t *testing.T) {
	s := &Snapshot{}
	for i := 0; i < 100; i++ {
		s.Files = append(s.Files, File{Path: fmt.Sprintf("src/%03d-%s.go", i, strings.Repeat("x", 100)), Content: "needle\n"})
	}
	offset := 0
	count := 0
	for {
		b, err := s.FilePage("digest", offset, 100, 1024)
		if err != nil {
			t.Fatal(err)
		}
		var page Page
		json.Unmarshal(b, &page)
		count += len(page.Files)
		if len(b) > 1024 {
			t.Fatal("oversized metadata")
		}
		if page.NextOffset == nil {
			break
		}
		if *page.NextOffset <= offset {
			t.Fatal("no progress")
		}
		offset = *page.NextOffset
	}
	if count != 100 {
		t.Fatal("metadata skipped files", count)
	}
	b, err := s.SearchResult("digest", "needle", "src/", 0, 12, 1024)
	if err != nil {
		t.Fatal(err)
	}
	var page Page
	json.Unmarshal(b, &page)
	if len(b) > 1024 || len(page.Matches) == 0 || page.NextOffset == nil || *page.NextOffset != len(page.Matches) {
		t.Fatal("search pagination", string(b))
	}
	next, err := s.SearchResult("digest", "needle", "src/", *page.NextOffset, 12, 1024)
	if err != nil {
		t.Fatal(err)
	}
	var second Page
	json.Unmarshal(next, &second)
	if second.Matches[0].Path == page.Matches[0].Path {
		t.Fatal("search repeated first match")
	}
}

func TestSourcePacksRejectUnsafeDuplicateAndUnboundedSelections(t *testing.T) {
	s := &Snapshot{Files: []File{{Path: "a.go", Content: strings.Repeat("a", 10000)}, {Path: "b.go", Content: strings.Repeat("b", 10000)}}}
	for _, paths := range [][]string{nil, {"../a"}, {"a.go", "a.go"}, {"absent"}, {"a", "b", "c", "d", "e", "f", "g"}} {
		if _, err := s.ReadPage("digest", paths, 0, 1024); err == nil {
			t.Fatal("invalid pack accepted", paths)
		}
	}
	b, err := s.ReadPage("digest", []string{"a.go", "b.go"}, 0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	var page Page
	json.Unmarshal(b, &page)
	if len(page.Excerpts) != 2 || len(page.Excerpts[1].Text) < 4 || page.Excerpts[0].NextOffset == nil {
		t.Fatal("pack omitted evidence or truncation")
	}
}
