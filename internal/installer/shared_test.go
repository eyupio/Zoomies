package installer

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
	"gopkg.in/yaml.v3"
)

// The container deployment mounts the shared folder at the path it has on the
// host. At any other path, the folder the embedded agent hands the host's
// daemon for a runner's tool cache would be one the daemon cannot see.
func TestTheComposeFileMountsTheSharedFolderAtItsOwnPath(t *testing.T) {
	body, err := RenderComposeFile(ComposeFileSpecFor(containerPlan(t)))
	if err != nil {
		t.Fatalf("RenderComposeFile: %v", err)
	}
	var doc struct {
		Services map[string]struct {
			Volumes []string `yaml:"volumes"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("parsing: %v", err)
	}
	want := SharedHostDir + ":" + SharedHostDir
	for name, svc := range doc.Services {
		found := false
		for _, v := range svc.Volumes {
			if v == want {
				found = true
			}
		}
		if !found {
			t.Errorf("service %s does not mount %q: %v", name, want, svc.Volumes)
		}
	}
	// A path inside the container, so slash-separated whichever OS runs the
	// installer's tests.
	if SharedHostDir != path.Join(ContainerStateDir, "shared") {
		t.Errorf("SharedHostDir = %s, but the container's own shared folder is %s/shared", SharedHostDir, ContainerStateDir)
	}
}

// The layout is created parents first and reported, and a second run creates
// nothing: an upgrade that finds the folder already there changes nothing.
func TestPrepareSharedDirCreatesTheLayoutOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	created, err := PrepareSharedDir(dir, -1, -1)
	if err != nil {
		t.Fatalf("PrepareSharedDir: %v", err)
	}
	if len(created) != len(config.SharedLayout)+1 {
		t.Errorf("created %v, want the folder and every folder of the layout", created)
	}
	for _, sub := range config.SharedLayout {
		if fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(sub))); err != nil || !fi.IsDir() {
			t.Errorf("%s was not created: %v", sub, err)
		}
	}
	if again, _ := PrepareSharedDir(dir, -1, -1); len(again) != 0 {
		t.Errorf("a second run created %v", again)
	}
	if problems := SharedDirProblems(dir, -1); len(problems) != 0 {
		t.Errorf("problems = %v, want none", problems)
	}
}

// The update folder is made only by the helper's install. In the layout, every
// agent start would make it, on every host and in mode off, and a host would
// look ready for a helper its owner never installed.
func TestTheSharedLayoutDoesNotMakeTheUpdateFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	if _, err := PrepareSharedDir(dir, -1, -1); err != nil {
		t.Fatalf("PrepareSharedDir: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "update")); !os.IsNotExist(err) {
		t.Errorf("preparing the shared folder made the update folder (%v)", err)
	}
}

// What is missing is named folder by folder, so an upgrade can say exactly
// what it would add.
func TestSharedDirProblemsNamesWhatIsMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	problems := SharedDirProblems(dir, -1)
	if len(problems) != len(config.SharedLayout) {
		t.Fatalf("problems = %v, want one per folder of the layout", problems)
	}
	for _, p := range problems {
		if !strings.HasSuffix(p, " is missing") {
			t.Errorf("problem %q does not say what is missing", p)
		}
	}
	// The owner is checked only for an account that has to write.
	if uid := os.Getuid(); uid >= 0 {
		if got := SharedDirProblems(dir, uid+1); len(got) != len(config.SharedLayout)+1 {
			t.Errorf("problems for another uid = %v, want the owner named as well", got)
		}
	}
}

// A controller whose runners all run on other hosts keeps no caches, so it is
// given neither the folder nor the mount -- nor the question at upgrade.
func TestAControllerWithoutRunnersIsNotGivenTheSharedFolder(t *testing.T) {
	p := containerPlan(t)
	p.Mode, p.Embedded = ModeController, false
	body, err := RenderComposeFile(ComposeFileSpecFor(p))
	if err != nil {
		t.Fatalf("RenderComposeFile: %v", err)
	}
	if strings.Contains(body, SharedHostDir+":"+SharedHostDir) {
		t.Errorf("a controller that runs no runners mounts the shared folder:\n%s", body)
	}
}
