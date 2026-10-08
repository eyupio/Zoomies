//go:build ignore

// Command gen_catalog writes the catalog of problem codes and Kennel checks
// into internal/catalog/catalog.json, where the binary embeds it and the docs
// site publishes it from, and the check list on docs/kennel-club.md.
//
// Run it from the repository root after editing docs/problem-codes.md or the
// Kennel registry:
//
//	go run internal/catalog/gen_catalog.go
//
// TestTheCatalogIsCurrent and TestTheKennelClubPageListsEveryCheck fail,
// naming this command, when either file has drifted.
package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/eyupio/zoomies/internal/catalog"
	"github.com/eyupio/zoomies/internal/kennel"
)

const (
	beginMarker = "<!-- zoomies:catalogue-begin -->"
	endMarker   = "<!-- zoomies:catalogue-end -->"
)

func main() {
	root, err := repoRoot()
	if err != nil {
		log.Fatal(err)
	}
	md, err := os.ReadFile(filepath.Join(root, "docs", "problem-codes.md"))
	if err != nil {
		log.Fatal(err)
	}
	c, err := catalog.Build(md, kennel.Checks(), gitHead(root))
	if err != nil {
		log.Fatal(err)
	}
	b, err := catalog.Marshal(c)
	if err != nil {
		log.Fatal(err)
	}
	out := filepath.Join(root, "internal", "catalog", "catalog.json")
	if err := os.WriteFile(out, b, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s (%d entries)\n", out, len(c.Entries))

	page := filepath.Join(root, "docs", "kennel-club.md")
	if err := rewriteBlock(page, checksBlock(kennel.Checks())); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("rewrote the check list on %s\n", page)
}

// checksBlock is one heading per check, so a link can land on it, and a
// definition list under each, because a table row has no anchor of its own.
func checksBlock(checks []kennel.Check) string {
	var b strings.Builder
	for _, c := range checks {
		// toc's slug would drop the dot (cino_timeout); the attr_list id is the
		// one the registry's Docs field and the catalog's links name.
		fmt.Fprintf(&b, "### `%s` { #%s }\n\n", c.Code, strings.ReplaceAll(string(c.Code), ".", "-"))
		fmt.Fprintf(&b, "Area\n:   %s\n\n", c.Area)
		fmt.Fprintf(&b, "Severity\n:   %s\n\n", c.Severity)
		fmt.Fprintf(&b, "Detects\n:   %s\n\n", c.Detects)
		fmt.Fprintf(&b, "Fix\n:   %s\n\n", c.Fix)
		fmt.Fprintf(&b, "Verify\n:   %s\n\n", c.Verify)
	}
	return b.String()
}

func rewriteBlock(path, body string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	s := string(raw)
	begin := strings.Index(s, beginMarker)
	end := strings.Index(s, endMarker)
	if begin < 0 || end < begin {
		return fmt.Errorf("%s: no %s / %s block to rewrite", path, beginMarker, endMarker)
	}
	s = s[:begin+len(beginMarker)] + "\n\n" + body + s[end:]
	return os.WriteFile(path, []byte(s), 0o644)
}

func gitHead(root string) string {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// repoRoot finds the directory holding go.mod, so the command works from the
// root or from this package's directory.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
