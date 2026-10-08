package naming

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The catalogue in this package says which operating systems a pool may ask
// for. Four files outside Go have to say the same thing -- both workflows, the
// Makefile and the site -- and gen_catalogue.go writes all four, so the tests
// here are about a file somebody edited by hand rather than regenerating.
//
// They compare meaning rather than bytes, because that is the failure that
// matters: an operating system half-added, where either a pool can name an
// image nothing publishes, or an image is published that no pool can ask for.
// Neither shows up until a runner refuses to start.

// regenerate is what every failure in this file tells the reader to do.
const regenerate = "run `make generate` and commit the result"

func TestWorkflowMatricesCoverTheCatalogue(t *testing.T) {
	for _, wf := range []struct{ file, job string }{
		{"ci.yml", "runner-images"},
		{"release.yml", "runner-images"},
	} {
		t.Run(wf.file, func(t *testing.T) {
			got, arches := runnerMatrix(t, wf.file, wf.job)
			want := map[string]matrixEntry{}
			for _, img := range Images() {
				plats := make([]string, 0, len(img.Arches))
				for _, arch := range img.Arches {
					plats = append(plats, "linux/"+arch)
				}
				want[img.Tag()] = matrixEntry{
					Base: img.Base, Family: img.Family, OS: img.OS, Version: img.Version,
					Platforms: strings.Join(plats, ","), Full: img.Full,
				}
			}
			for tag, w := range want {
				g, ok := got[tag]
				if !ok {
					t.Errorf("%s builds no %s image, but the catalogue publishes one; %s", wf.file, tag, regenerate)
					continue
				}
				if g != w {
					t.Errorf("%s builds %s as %+v, but the catalogue says %+v; %s", wf.file, tag, g, w, regenerate)
				}
				// Each architecture is its own row, built on a machine of that
				// architecture, and the rows are merged into one manifest. A
				// platform with no row of its own would be named by the manifest
				// and built by nothing.
				if built := strings.Join(arches[tag], ","); built != w.Platforms {
					t.Errorf("%s builds %s on %s machines, but it is published for %s; %s", wf.file, tag, built, w.Platforms, regenerate)
				}
			}
			for tag := range got {
				if _, ok := want[tag]; !ok {
					t.Errorf("%s builds %s, which is not in the catalogue, so no pool can ask for it; %s", wf.file, tag, regenerate)
				}
			}
		})
	}
}

// matrixEntry is the part of a matrix row that has to agree with the catalogue.
//
// Platforms is in here because a variant's architectures are a claim the
// registry has to be able to satisfy: a catalogue row saying arm64 that the
// workflow does not build for arm64 gives a pool an image reference that
// resolves to nothing.
type matrixEntry struct {
	Base      string `yaml:"base"`
	Family    string `yaml:"family"`
	OS        string `yaml:"os"`
	Version   string `yaml:"version"`
	Platforms string `yaml:"platforms"`
	// Full is whether the row also builds runner-full. A catalogue row that
	// says so and a matrix that does not is a tag a pool can name and the
	// registry does not have.
	Full bool `yaml:"full"`
}

func runnerMatrix(t *testing.T, file, job string) (map[string]matrixEntry, map[string][]string) {
	t.Helper()
	var wf struct {
		Jobs map[string]struct {
			Strategy struct {
				Matrix struct {
					Include []struct {
						Tag         string `yaml:"tag"`
						Arch        string `yaml:"arch"`
						matrixEntry `yaml:",inline"`
					} `yaml:"include"`
				} `yaml:"matrix"`
			} `yaml:"strategy"`
		} `yaml:"jobs"`
	}
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", file))
	if err != nil {
		t.Fatalf("reading the workflow: %v", err)
	}
	if err := yaml.Unmarshal(b, &wf); err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}
	j, ok := wf.Jobs[job]
	if !ok {
		t.Fatalf("%s has no %s job", file, job)
	}
	out := map[string]matrixEntry{}
	arches := map[string][]string{}
	for _, row := range j.Strategy.Matrix.Include {
		if prev, ok := out[row.Tag]; ok && prev != row.matrixEntry {
			t.Errorf("%s's %s rows for %s disagree about what they build", file, job, row.Tag)
		}
		out[row.Tag] = row.matrixEntry
		arches[row.Tag] = append(arches[row.Tag], "linux/"+row.Arch)
	}
	if len(out) == 0 {
		t.Fatalf("%s's %s job builds nothing", file, job)
	}
	return out, arches
}

// The Makefile builds the same variants by hand, because a contributor
// debugging a Fedora runner wants `make image-runner RUNNER_VARIANT=fedora-42`
// and not a workflow run. It drifts from the catalogue just as easily.
func TestMakefileBuildsTheCatalogue(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatalf("reading the Makefile: %v", err)
	}
	// The rows are column-aligned, so compare on fields rather than on the
	// exact spacing a contributor is free to adjust.
	rows := map[string]string{}
	var listed []string
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) >= 2 && fields[0] == "RUNNER_VARIANTS" && fields[1] == ":=":
			listed = fields[2:]
		case len(fields) >= 2 && strings.HasPrefix(fields[0], "variant.") && fields[1] == ":=":
			rows[strings.TrimPrefix(fields[0], "variant.")] = strings.Join(fields[2:], " ")
		}
	}
	if len(listed) == 0 {
		t.Fatal("the Makefile no longer lists RUNNER_VARIANTS")
	}
	for _, img := range Images() {
		want := strings.Join([]string{img.Base, img.Family, img.OS, img.Version}, " ")
		got, ok := rows[img.Tag()]
		if !ok {
			t.Errorf("the Makefile has no row building %s; %s", img.Tag(), regenerate)
			continue
		}
		if got != want {
			t.Errorf("the Makefile builds %s as %q, but the catalogue says %q; %s", img.Tag(), got, want, regenerate)
		}
		if !slices.Contains(listed, img.Tag()) {
			t.Errorf("%s has a row but is not in RUNNER_VARIANTS, so `make images-runner` skips it; %s", img.Tag(), regenerate)
		}
	}
	for _, tag := range listed {
		if _, ok := FindImage(splitTag(tag)); !ok {
			t.Errorf("the Makefile builds %q, which is not in the catalogue; %s", tag, regenerate)
		}
	}
}

// splitTag turns an image tag back into the OS and version FindImage takes.
func splitTag(tag string) (string, string) {
	os, version, _ := strings.Cut(tag, "-")
	return os, ExpandVersion(os, version)
}

// The site's table is what an operator reads before choosing a pool's platform,
// and it is the copy nothing else checks: a stale row here sends somebody to
// name an image that was swapped out two releases ago.
func TestDocsListTheCatalogue(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "naming.md"))
	if err != nil {
		t.Fatalf("reading the naming page: %v", err)
	}
	rows := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) != 4 {
			continue
		}
		tag := strings.Trim(strings.TrimSpace(cells[0]), "`")
		if _, ok := FindImage(splitTag(tag)); !ok {
			continue
		}
		rows[tag] = strings.Trim(strings.TrimSpace(cells[1]), "`") + " " + strings.TrimSpace(cells[2]) + " " + strings.TrimSpace(cells[3])
	}
	for _, img := range Images() {
		full := "-"
		if img.Full {
			full = "yes"
		}
		want := img.Base + " " + strings.Join(img.Arches, ", ") + " " + full
		switch got, ok := rows[img.Tag()]; {
		case !ok:
			t.Errorf("the naming page does not list %s, which the catalogue publishes; %s", img.Tag(), regenerate)
		case got != want:
			t.Errorf("the naming page lists %s as %q, but the catalogue says %q; %s", img.Tag(), got, want, regenerate)
		}
	}
	if len(rows) != len(Images()) {
		t.Errorf("the naming page lists %d variants, the catalogue has %d; %s", len(rows), len(Images()), regenerate)
	}
}

// The catalogue is also named in prose, on the configuration page, where a
// generated block would read as a list bolted into a sentence. So it is
// checked instead: an operator told to name an image needs the list of images
// beside the setting that takes one.
// TestTheLandingSurfacesNameEveryVariant holds the two hand-maintained pages
// that list the catalogue in prose.
//
// The generator writes four files and docs/naming.md is the only page among
// them, so README.md and docs/index.md would otherwise go silently stale the
// day a variant is added or a version moves -- and both are the first thing a
// reader meets, which is the worst place to be wrong about what is published.
// A test rather than a fifth generator target because these are sentences
// written for a person, not a table: what must not drift is the set of names,
// not the words around them.
func TestTheLandingSurfacesNameEveryVariant(t *testing.T) {
	for _, page := range []string{
		filepath.Join("..", "..", "README.md"),
		filepath.Join("..", "..", "docs", "index.md"),
	} {
		b, err := os.ReadFile(page)
		if err != nil {
			t.Fatalf("reading %s: %v", page, err)
		}
		for _, img := range Images() {
			name := PrettyOS(img.OS) + " " + img.Version
			if !strings.Contains(string(b), name) {
				t.Errorf("%s never names %q, so the page a reader meets first no longer says what is published",
					filepath.Base(page), name)
			}
		}
	}
}

func TestConfigurationPageNamesEveryVariant(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "configuration.md"))
	if err != nil {
		t.Fatalf("reading the configuration page: %v", err)
	}
	for _, img := range Images() {
		if !strings.Contains(string(b), "`"+img.Tag()+"`") {
			t.Errorf("the configuration page never mentions the %s image, so nothing tells an operator they may ask for it", img.Tag())
		}
	}
}
