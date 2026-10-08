package docs

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// A spaced em dash is how generated prose punctuates a sentence it has not
// finished thinking about, and once a few are in the repository the next
// contributor copies them. The house style is a comma, a colon, a semicolon or
// parentheses, so this fails on the character wherever it is set apart from the
// words around it. A lone one is not caught on purpose: the UI uses it as the
// placeholder for an absent value, and web/unit/absent-value.test.ts guards that.
//
// The pattern spells the character as an escape, so this file is not its own
// first offender.
var spacedEmDash = regexp.MustCompile(`(^|\s)\x{2014}(\s|$)`)

var noEmDashSkippedDirs = map[string]bool{
	".git":              true,
	"node_modules":      true,
	"webdist":           true,
	"dist":              true,
	"build":             true,
	".svelte-kit":       true,
	"test-results":      true,
	"playwright-report": true,
}

func TestNoSpacedEmDashIsWrittenAnywhereInTheRepository(t *testing.T) {
	root := "../.."
	var offences []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if noEmDashSkippedDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		// The licence text is not ours to edit.
		if d.Name() == "LICENSE" || d.Name() == "package-lock.json" || d.Name() == "go.sum" {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > 4<<20 {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil || bytes.IndexByte(body, 0) >= 0 || !utf8.Valid(body) {
			return nil // a binary file: images, fonts, the brand guide's PDF
		}
		for i, line := range strings.Split(string(body), "\n") {
			if spacedEmDash.MatchString(line) {
				offences = append(offences, strings.TrimPrefix(filepath.ToSlash(path), "../../")+":"+strconv.Itoa(i+1))
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repository: %v", err)
	}
	if len(offences) > 0 {
		t.Errorf("a spaced em dash was written in %d file(s); use a comma, colon, semicolon or parentheses, or split the sentence:\n  %s",
			len(offences), strings.Join(offences, "\n  "))
	}
}
