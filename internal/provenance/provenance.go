// Package provenance keeps third-party names out of the tree.
//
// Everything committed here is original work that stands on its own, so no
// file -- source, docs, workflows, fixtures, commit messages -- may name the
// products, skills, commands or rule prefixes of whatever a feature was
// compared with. The terms themselves are never written whole anywhere in the
// repository, this package included: they are assembled at init from pieces,
// and the owner supplies the pieces out of band.
package provenance

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fragments is the one place the forbidden terms come from. Each inner slice
// is joined into one term, so that no single string here is a term. Empty
// until the owner supplies the pieces; the tests of the mechanism seed their
// own.
var fragments = [][]string{}

var terms = assemble(fragments)

func assemble(parts [][]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range parts {
		t := strings.ToLower(strings.Join(p, ""))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Terms returns the forbidden terms, lower-cased, deduplicated and sorted.
func Terms() []string { return append([]string(nil), terms...) }

// SetTermsForTest replaces the terms until the test ends, so the mechanism
// can be tested without a real term anywhere in the tree.
func SetTermsForTest(t testing.TB, replacement []string) {
	t.Helper()
	previous := terms
	terms = assemble(split(replacement))
	t.Cleanup(func() { terms = previous })
}

func split(whole []string) [][]string {
	out := make([][]string, len(whole))
	for i, w := range whole {
		out[i] = []string{w}
	}
	return out
}

// Hit is one forbidden term on one line of one file.
type Hit struct {
	Path string
	Line int
	Term string
}

// Scan reads r line by line and reports every term it holds, whatever the
// case. A line holding two terms is two hits; a term twice on one line is
// one.
func Scan(path string, r io.Reader) []Hit {
	var hits []Hit
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		lower := strings.ToLower(sc.Text())
		for _, t := range terms {
			if strings.Contains(lower, t) {
				hits = append(hits, Hit{Path: path, Line: line, Term: t})
			}
		}
	}
	return hits
}

// ScanTree walks root and scans every file skip does not exclude, reporting
// hits with paths relative to root, in path order.
func ScanTree(root string, skip func(path string, d fs.DirEntry) bool) ([]Hit, error) {
	var hits []Hit
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if skip(rel, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if binary(f) {
			return nil
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		hits = append(hits, Scan(rel, f)...)
		return nil
	})
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Path != hits[j].Path {
			return hits[i].Path < hits[j].Path
		}
		return hits[i].Line < hits[j].Line
	})
	return hits, err
}

// ScanTracked scans every file git tracks under root -- a repository's own
// writing, and nothing that merely sits beside it: a developer's .env, a
// deployment's data directory, a build's output -- keeping the size and binary
// filters, with paths relative to root in git's order.
func ScanTracked(root string) ([]Hit, error) {
	cmd := exec.Command("git", "-C", root, "ls-files", "-z")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("provenance: listing tracked files: %w", err)
	}
	var hits []Hit
	for _, rel := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if rel == "" {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxFileBytes {
			// Deleted but not yet committed, a symlink, or too large to be prose.
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		if !binary(f) {
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				f.Close()
				return nil, err
			}
			hits = append(hits, Scan(rel, f)...)
		}
		f.Close()
	}
	return hits, nil
}

// maxFileBytes is the most a scanned file may hold: prose and source are far
// smaller, and anything larger is a bundle, an archive or a database.
const maxFileBytes = 1 << 20

// DefaultSkip leaves out what is not this repository's own writing: version
// control, installed and built dependencies, a built site, and files too large
// or too binary to be prose.
func DefaultSkip(path string, d fs.DirEntry) bool {
	if d.IsDir() {
		switch d.Name() {
		case ".git", "node_modules", "webdist", "site":
			return true
		}
		return false
	}
	info, err := d.Info()
	if err != nil {
		return true
	}
	return info.Size() > maxFileBytes
}

// binary reports whether the file's first 512 bytes hold a NUL, which no text
// file does and every image, font and archive does early.
func binary(f *os.File) bool {
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	for _, b := range buf[:n] {
		if b == 0 {
			return true
		}
	}
	return false
}
