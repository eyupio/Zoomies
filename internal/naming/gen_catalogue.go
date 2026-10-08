//go:build ignore

// Command gen_catalogue writes the runner image catalogue everywhere outside Go
// that has to agree with it.
//
// internal/naming's catalogue is the list of operating systems a pool may ask
// for. Four other files have to say the same thing: the Makefile, so a
// contributor can build one variant by hand; both workflows, so every variant
// is actually built and published; and docs/naming.md, so an operator can see
// what is on offer. Kept by hand, that is four places to remember and a runner
// that will not start when somebody forgets one -- swapping Fedora 42 for
// Fedora 43 should be a row in images.go, not an archaeology exercise.
//
// So the four are generated. Each file carries a begin/end marker and this
// command rewrites what is between them, leaving everything else alone: these
// are files people edit for other reasons, and a generator that owned the whole
// file would own decisions it has no opinion about.
//
// Run it from the repository root after editing the catalogue:
//
//	go run internal/naming/gen_catalogue.go
//
// The tests in internal/naming fail, naming this command, when a file has
// drifted -- which is the case where somebody edited one of them by hand.
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/eyupio/zoomies/internal/naming"
)

// The markers each generated block sits between. The comment syntax differs by
// file type; the marker word does not, so `grep -r zoomies:catalogue` finds
// every one of them.
const (
	marker      = "zoomies:catalogue"
	beginMarker = marker + "-begin"
	endMarker   = marker + "-end"
)

func main() {
	root, err := repoRoot()
	if err != nil {
		log.Fatal(err)
	}
	images := naming.Images()
	for _, f := range []struct {
		path string
		body string
	}{
		{"Makefile", makefileBlock(images)},
		{filepath.Join(".github", "workflows", "ci.yml"), matrixBlock(images)},
		{filepath.Join(".github", "workflows", "release.yml"), matrixBlock(images)},
		{filepath.Join("docs", "naming.md"), docsBlock(images)},
	} {
		path := filepath.Join(root, f.path)
		if err := rewrite(path, f.body); err != nil {
			log.Fatalf("%s: %v", f.path, err)
		}
		fmt.Println("wrote the catalogue into", f.path)
	}
}

// rewrite replaces the marked block in path with body, leaving the markers and
// their indentation exactly as they were.
func rewrite(path, body string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	begin, end := -1, -1
	for i, line := range lines {
		switch {
		case strings.Contains(line, beginMarker):
			if begin >= 0 {
				return fmt.Errorf("two %s markers", beginMarker)
			}
			begin = i
		case strings.Contains(line, endMarker):
			if end >= 0 {
				return fmt.Errorf("two %s markers", endMarker)
			}
			end = i
		}
	}
	if begin < 0 || end < 0 || end < begin {
		return fmt.Errorf("no %s ... %s block to write into", beginMarker, endMarker)
	}
	out := append([]string{}, lines[:begin+1]...)
	out = append(out, strings.Split(body, "\n")...)
	out = append(out, lines[end:]...)
	return os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o644)
}

// makefileBlock renders the variant table `make image-runner` drives.
func makefileBlock(images []naming.Image) string {
	var tags []string
	def := ""
	width := 0
	for _, img := range images {
		tags = append(tags, img.Tag())
		if img.Default {
			def = img.Tag()
		}
		width = max(width, len(img.Tag()))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "RUNNER_VARIANTS := %s\n", strings.Join(tags, " "))
	fmt.Fprintf(&b, "RUNNER_VARIANT_DEFAULT := %s\n\n", def)
	b.WriteString("# base | family | os | version\n")
	for _, img := range images {
		fmt.Fprintf(&b, "variant.%-*s := %s %s %s %s\n",
			width, img.Tag(), img.Base, img.Family, img.OS, img.Version)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// matrixBlock renders the `strategy.matrix` both workflows build from: one row
// per variant and architecture, because each architecture is built on a
// machine of its own kind and the rows are then merged into one manifest. The
// arm64 images used to be cross-built under QEMU on an x64 builder, and its
// aarch64 emulation segfaulted in ldconfig on Ubuntu 22.04's glibc; a native
// arm64 builder has no emulator to fail. `platforms` stays the variant's whole
// set on every row, since it is what the manifest has to cover. The rows are
// column-aligned because they are read as a table, not as YAML.
func matrixBlock(images []naming.Image) string {
	tagWidth, baseWidth, osWidth, versionWidth := 0, 0, 0, 0
	for _, img := range images {
		tagWidth = max(tagWidth, len(img.Tag()))
		baseWidth = max(baseWidth, len(quote(img.Base))+1)
		osWidth = max(osWidth, len(img.OS)+1)
		versionWidth = max(versionWidth, len(quote(img.Version))+1)
	}
	var b strings.Builder
	b.WriteString("        include:\n")
	for _, img := range images {
		for _, arch := range img.Arches {
			fmt.Fprintf(&b, "          - { tag: %-*s arch: %-6s base: %-*s family: %s, os: %-*s version: %-*s platforms: %s%s }\n",
				tagWidth+1, img.Tag()+",",
				arch+",",
				baseWidth, quote(img.Base)+",",
				img.Family,
				osWidth, img.OS+",",
				versionWidth, quote(img.Version)+",",
				quote(platforms(img)),
				defaultField(img)+fullField(img))
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// platforms renders a variant's architectures as the `platforms:` value buildx
// takes. It comes from the catalogue rather than the job, because the variants
// are not all publishable for the same set -- and a workflow that asked for one
// nothing can build would fail the whole matrix rather than the row that means
// it.
func platforms(img naming.Image) string {
	out := make([]string, 0, len(img.Arches))
	for _, arch := range img.Arches {
		out = append(out, "linux/"+arch)
	}
	return strings.Join(out, ",")
}

// defaultField marks the one variant that carries the unqualified tags. Only
// the default row has the key at all: a `default: false` on every other row
// would be four more things to keep true for no reader's benefit.
func defaultField(img naming.Image) string {
	if img.Default {
		return ", default: true"
	}
	return ""
}

// fullField marks a variant that is also built as runner-full, on the same
// terms as defaultField: only the rows that are carry the key.
func fullField(img naming.Image) string {
	if img.Full {
		return ", full: true"
	}
	return ""
}

// quote renders a YAML scalar that has to stay a string. A version like 24.04
// is a float to YAML, and an image reference carries a colon.
func quote(s string) string { return `"` + s + `"` }

// docsBlock renders the table on the site that says what an operator may ask
// for.
func docsBlock(images []naming.Image) string {
	var b strings.Builder
	b.WriteString("| Tag | Base | Architectures | `zoomies-runner-full` |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, img := range images {
		full := "-"
		if img.Full {
			full = "yes"
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s |\n", img.Tag(), img.Base, strings.Join(img.Arches, ", "), full)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// repoRoot walks up from the working directory looking for go.mod, so that the
// command works both from the repository root and from the package it lives in.
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
			return "", fmt.Errorf("no go.mod above %s: run this from the repository", dir)
		}
		dir = parent
	}
}
