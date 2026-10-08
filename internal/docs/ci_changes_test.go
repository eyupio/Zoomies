package docs

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// changesScript is the shell that decides what CI runs for a change, taken
// out of ci.yml so the decisions can be asked about directly.
func changesScript(t *testing.T) string {
	t.Helper()
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				ID  string `yaml:"id"`
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(workflowFiles(t)["ci.yml"]), &wf); err != nil {
		t.Fatalf("parsing ci.yml: %v", err)
	}
	for _, step := range wf.Jobs["changes"].Steps {
		if step.ID == "filter" {
			return step.Run
		}
	}
	t.Fatal("ci.yml's changes job has no filter step")
	return ""
}

// decide runs the filter as a push to main, or a pull request, that changed
// these files, with a git that answers only the two questions it asks.
func decide(t *testing.T, event string, files ...string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	stub := "#!/bin/sh\ncase \"$1\" in fetch) exit 0 ;; diff) printf '%s\\n' " + strings.Join(files, " ") + " ;; esac\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "output")
	ref := "refs/pull/1/merge"
	if event == "push" {
		ref = "refs/heads/main"
	}
	cmd := exec.Command("sh", "-c", changesScript(t))
	cmd.Env = append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"EVENT="+event, "REF="+ref, "BASE=0123abc", "GITHUB_OUTPUT="+out)
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the filter failed for %v: %v\n%s", files, err, msg)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			got[k] = v
		}
	}
	return got
}

// A merge to main used to run everything, so a README edit rebuilt and
// republished every binary and image on the dev channel with nothing in them
// different, and held the fleet for an hour doing it. What a change runs is
// now what its files can affect, and only a change to what ships publishes.
func TestCIRunsOnlyWhatAChangeCanAffect(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the filter is POSIX shell, and runs on Linux in CI")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh to run the filter with")
	}
	all := `["controller-1","controller-2","api-1","api-2","store","rest"]`
	for _, tc := range []struct {
		name  string
		event string
		files []string
		want  map[string]string
	}{
		{"the README checks its own facts and nothing else", "push", []string{"README.md"},
			map[string]string{"go": "false", "web": "false", "deploy": "false", "installer": "false", "shards": `["docs"]`, "publish": "false"}},
		{"the install script is checked and upgraded through, not rebuilt into binaries or images", "push", []string{"install.sh"},
			map[string]string{"go": "false", "deploy": "false", "installer": "true", "shards": "[]", "publish": "false"}},
		{"a file nothing reads runs nothing", "push", []string{"LICENSE", ".gitignore"},
			map[string]string{"go": "false", "installer": "false", "shards": "[]", "publish": "false"}},
		{"Go on main is the whole suite and a publish", "push", []string{"internal/store/queries.go"},
			map[string]string{"go": "true", "shards": all, "publish": "true"}},
		{"the UI is embedded in the binary, so it publishes too", "push", []string{"web/src/lib/units.ts"},
			map[string]string{"go": "false", "web": "true", "publish": "true"}},
		{"an image input publishes, so the binaries and images keep naming one commit", "push", []string{"deploy/Dockerfile"},
			map[string]string{"deploy": "true", "shards": `["rest"]`, "publish": "true"}},
		{"a pull request never publishes", "pull_request", []string{"internal/store/queries.go"},
			map[string]string{"go": "true", "publish": "false"}},
		{"a change to CI itself runs everything", "pull_request", []string{".github/workflows/ci.yml"},
			map[string]string{"go": "true", "web": "true", "deploy": "true", "installer": "true", "shards": all}},
		{"an unknown kind of file runs too much rather than too little", "pull_request", []string{"something/new.xyz"},
			map[string]string{"go": "true", "shards": all}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := decide(t, tc.event, tc.files...)
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("%s for %v: %s=%s, want %s (all: %v)", tc.event, tc.files, k, got[k], want, got)
				}
			}
		})
	}
}
