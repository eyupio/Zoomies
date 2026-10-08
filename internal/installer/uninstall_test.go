package installer

import (
	"bytes"
	"context"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func uninstallOpts(t *testing.T) UninstallOptions {
	t.Helper()
	// The update helper's units and root's folder for it are the test's own,
	// so that no test reaches the host's, and systemctl is recorded.
	return UninstallOptions{
		ConfigDir:      t.TempDir(),
		StateDir:       t.TempDir(),
		Out:            &bytes.Buffer{},
		In:             strings.NewReader(""),
		helperUnitDir:  t.TempDir(),
		helperStateDir: filepath.Join(t.TempDir(), "zoomies-update"),
		run:            (&recordingRunner{}).run,
	}
}

func TestUninstallItemsListsWhatIsThere(t *testing.T) {
	opts := uninstallOpts(t)
	writeFile(t, opts.ConfigDir, "zoomies.yaml", "server: {}\n")
	writeFile(t, opts.ConfigDir, "encryption.key", "key\n")
	writeFile(t, opts.StateDir, "zoomies.db", "")
	opts.BinaryPath = writeFile(t, t.TempDir(), "zoomies", "#!/bin/sh\n")

	byPath := map[string]RemovalItem{}
	for _, it := range UninstallItems(opts) {
		byPath[it.Path] = it
	}

	for _, path := range []string{
		filepath.Join(opts.ConfigDir, "zoomies.yaml"),
		filepath.Join(opts.ConfigDir, "encryption.key"),
		filepath.Join(opts.StateDir, "zoomies.db"),
		opts.StateDir,
		opts.BinaryPath,
	} {
		it, ok := byPath[path]
		if !ok {
			t.Errorf("%s is not listed", path)
			continue
		}
		if !it.Present {
			t.Errorf("%s exists but was listed as already gone", path)
		}
	}

	// The two irreplaceable things must say what losing them costs.
	if note := byPath[filepath.Join(opts.ConfigDir, "encryption.key")].Note; !strings.Contains(note, "decrypt") {
		t.Errorf("the key's note should say what cannot be decrypted without it, got %q", note)
	}
	if note := byPath[filepath.Join(opts.StateDir, "zoomies.db")].Note; !strings.Contains(note, "audit") {
		t.Errorf("the database's note should say what is in it, got %q", note)
	}
	if note := byPath[opts.BinaryPath].Note; !strings.Contains(note, "last") {
		t.Errorf("the binary goes last so a failed run can be finished, got %q", note)
	}
}

func TestUninstallItemsMarksWhatIsAlreadyGone(t *testing.T) {
	opts := uninstallOpts(t)
	for _, it := range UninstallItems(opts) {
		if it.Path == opts.StateDir || it.Path == opts.ConfigDir {
			continue // t.TempDir creates these
		}
		if it.Present {
			t.Errorf("%s should not be present in an empty temp directory", it.Path)
		}
	}
}

func TestUninstallItemsRespectsKeepConfig(t *testing.T) {
	opts := uninstallOpts(t)
	opts.KeepConfig = true
	writeFile(t, opts.ConfigDir, "zoomies.yaml", "")

	for _, it := range UninstallItems(opts) {
		if it.What == "configuration" {
			if !strings.Contains(it.Note, "kept") {
				t.Fatalf("a kept config must say so: %+v", it)
			}
			return
		}
	}
	t.Fatal("the configuration was not listed at all")
}

func TestDeploymentItemsIncludesTheRecordedContainerImage(t *testing.T) {
	opts := uninstallOpts(t)
	rec := DeploymentRecord{
		Deployment: DeploymentDocker,
		Image:      "ghcr.io/eyupio/zoomies:dev",
	}
	for _, item := range deploymentItems(rec, opts) {
		if item.What == "container image" && item.Path == rec.Image {
			return
		}
	}
	t.Fatal("uninstall summary did not include the recorded container image")
}

func TestUninstallDoesNothingWhenNothingIsInstalled(t *testing.T) {
	opts := uninstallOpts(t)
	if err := os.RemoveAll(opts.ConfigDir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(opts.StateDir); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	opts.Out = &out

	if err := Uninstall(context.Background(), opts); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !strings.Contains(out.String(), "not installed here") {
		t.Fatalf("it should say so plainly:\n%s", out.String())
	}
}

func TestUninstallRefusesWithoutConfirmation(t *testing.T) {
	opts := uninstallOpts(t)
	writeFile(t, opts.ConfigDir, "zoomies.yaml", "")
	opts.NonInteractive = true

	err := Uninstall(context.Background(), opts)
	if err == nil {
		t.Fatal("an unattended uninstall must be asked for explicitly")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("the error should say how to confirm, got: %v", err)
	}
	if !exists(filepath.Join(opts.ConfigDir, "zoomies.yaml")) {
		t.Fatal("nothing may be removed before the confirmation")
	}
}

func TestUninstallRemovesAndReportsLineByLine(t *testing.T) {
	opts := uninstallOpts(t)
	opts.Yes = true
	opts.NonInteractive = true
	writeFile(t, opts.ConfigDir, "zoomies.yaml", "")
	writeFile(t, opts.ConfigDir, "encryption.key", "")
	writeFile(t, opts.StateDir, "zoomies.db", "")
	binary := writeFile(t, t.TempDir(), "zoomies", "#!/bin/sh\n")
	opts.BinaryPath = binary
	var out bytes.Buffer
	opts.Out = &out

	if err := Uninstall(context.Background(), opts); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	for _, path := range []string{
		filepath.Join(opts.ConfigDir, "zoomies.yaml"),
		filepath.Join(opts.ConfigDir, "encryption.key"),
		opts.StateDir,
		binary,
	} {
		if exists(path) {
			t.Errorf("%s should be gone", path)
		}
	}
	report := out.String()
	for _, want := range []string{"removed", "zoomies.yaml", "encryption.key", "Done"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report should mention %q:\n%s", want, report)
		}
	}

	// Safe to re-run: everything is already gone and that is not an error.
	if err := Uninstall(context.Background(), opts); err != nil {
		t.Fatalf("second Uninstall: %v", err)
	}
}

func TestUninstallLeavesWhatIsNotItsAndSaysSo(t *testing.T) {
	opts := uninstallOpts(t)
	opts.Yes = true
	opts.NonInteractive = true
	opts.KeepConfig = true
	writeFile(t, opts.ConfigDir, "zoomies.yaml", "# edited by hand\n")
	writeFile(t, opts.ConfigDir, "fullchain.pem", "a certificate the operator put there")
	var out bytes.Buffer
	opts.Out = &out

	if err := Uninstall(context.Background(), opts); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !exists(filepath.Join(opts.ConfigDir, "zoomies.yaml")) {
		t.Fatal("the config was to be kept")
	}
	if !exists(filepath.Join(opts.ConfigDir, "fullchain.pem")) {
		t.Fatal("a file Zoomies did not write must be left alone")
	}
	report := out.String()
	if !strings.Contains(report, "left in place") || !strings.Contains(report, "fullchain.pem") {
		t.Fatalf("the report must name what was left behind:\n%s", report)
	}
}

// The helper is root's, so leaving it behind would leave a root unit waiting
// on a folder nobody writes; and the previous binary is a whole release kept
// beside the one being removed.
func TestUninstallRemovesTheHelperAndThePreviousBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the update helper is a pair of systemd units")
	}
	opts := uninstallOpts(t)
	runner := &recordingRunner{}
	opts.run = runner.run
	opts.Yes, opts.NonInteractive = true, true
	writeFile(t, opts.ConfigDir, "zoomies.yaml", "")
	binDir := t.TempDir()
	opts.BinaryPath = writeFile(t, binDir, "zoomies", "#!/bin/sh\n")
	previous := writeFile(t, binDir, "zoomies"+PreviousSuffix, "#!/bin/sh\n")
	units := []string{writeFile(t, opts.helperUnitDir, UpdatePathUnit, "[Path]\n"), writeFile(t, opts.helperUnitDir, UpdateServiceUnit, "[Service]\n")}
	if err := os.Mkdir(opts.helperStateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, opts.helperStateDir, "state.json", "{}")
	configPointer := writeFile(t, opts.ConfigDir, "update-helper.json", "{}")

	listed := map[string]RemovalItem{}
	for _, it := range UninstallItems(opts) {
		listed[it.What] = it
	}
	if it := listed["update helper"]; !it.Present || it.Path != units[0] {
		t.Errorf("the helper is not listed as there: %+v", it)
	}
	if it := listed["previous binary"]; !it.Present || it.Path != previous {
		t.Errorf("the previous binary is not listed as there: %+v", it)
	}

	var out bytes.Buffer
	opts.Out = &out
	if err := Uninstall(context.Background(), opts); err != nil {
		t.Fatalf("Uninstall: %v\n%s", err, out.String())
	}
	if !runner.ran("systemctl", "disable", "--now", UpdatePathUnit) {
		t.Errorf("the helper's path unit was not stopped and disabled: %v", runner.lines())
	}
	for _, path := range append(units, opts.helperStateDir, configPointer, previous, opts.BinaryPath) {
		if exists(path) {
			t.Errorf("%s should be gone:\n%s", path, out.String())
		}
	}
}

func TestServiceUserToRemoveOnlyEverRemovesOurOwn(t *testing.T) {
	// Never the account a human logs in with, whatever the flags say.
	if got := serviceUserToRemove(UninstallOptions{ServiceUser: "ada"}); got != "" {
		t.Fatalf("serviceUserToRemove = %q, want empty", got)
	}
	// Never for an install that lives somewhere else: those never created an
	// account, and this is what keeps a test with temporary directories from
	// deleting a real one.
	if got := serviceUserToRemove(UninstallOptions{ConfigDir: t.TempDir(), StateDir: t.TempDir()}); got != "" {
		t.Fatalf("serviceUserToRemove for a relocated install = %q, want empty", got)
	}
	if os.Geteuid() != 0 {
		if got := serviceUserToRemove(UninstallOptions{}); got != "" {
			t.Fatalf("a non-root uninstall cannot remove accounts, got %q", got)
		}
	}
}

func TestAskYesNo(t *testing.T) {
	cases := []struct {
		in     string
		prompt string
		want   bool
	}{
		{"yes\n", "Remove? ", true},
		{"y\n", "Remove? ", true},
		{"no\n", "Remove? ", false},
		{"\n", "Deregister? [Y/n]: ", true},
		{"\n", "Remove? ", false},
		{"", "Remove? ", false},
	}
	for _, tc := range cases {
		got, err := askYesNo(strings.NewReader(tc.in), &bytes.Buffer{}, tc.prompt)
		if err != nil {
			t.Fatalf("askYesNo(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("askYesNo(%q, %q) = %v, want %v", tc.in, tc.prompt, got, tc.want)
		}
	}
}

func TestRemoveServiceUserNeedsRoot(t *testing.T) {
	t.Skip("creating and removing system accounts changes the machine the tests run on; " +
		"exercised in the packaging tests that run in a throwaway container")
}

// Runner names carry no instance identity, so the "zoomies-" prefix used to
// be the whole test of ownership: uninstalling a staging controller deleted a
// production controller's idle runners on the same organisation.
func TestUninstallDeregistersOnlyTheRunnersThisDatabaseKnows(t *testing.T) {
	remote := []github.Runner{
		{ID: 1, Name: "zoomies-ours-by-id"},
		{ID: 2, Name: "zoomies-ours-by-name"},
		{ID: 3, Name: "zoomies-someone-elses-controller"},
		{ID: 4, Name: "hand-registered"},
	}
	local := []*store.Runner{
		{Name: "zoomies-renamed-on-github", GitHubRunnerID: 1},
		{Name: "zoomies-ours-by-name"},
	}
	got := fleetRunners(remote, local)
	ids := make([]int64, 0, len(got))
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	if !slices.Equal(ids, []int64{1, 2}) {
		t.Fatalf("would deregister %v, want only the two this database has rows for", ids)
	}
}

// On most installations the config directory and the state directory are one
// directory, and --keep-config has to survive that.
//
// config.StateDir and config.ConfigDir both answer ~/Library/Application
// Support/zoomies on macOS, and os.UserConfigDir()/zoomies for a non-root
// Linux install; only a root install separates /var/lib/zoomies from
// /etc/zoomies. Uninstall used RemoveAll on the state directory, so on every
// one of those it deleted the configuration too -- the zoomies.yaml just
// promised to be kept, the encryption key, and anything else in there -- and
// then reported only that the state directory had gone.
//
// The encryption key is the part that does not come back. It is the only copy,
// and without it the stored App private keys and webhook secrets in any backup
// the operator still holds cannot be decrypted.
//
// Every other test here gives ConfigDir and StateDir separate temporary
// directories, which is exactly why none of them could see this.
func TestUninstallWithOneSharedDirectoryKeepsTheConfigItPromisedToKeep(t *testing.T) {
	shared := t.TempDir()
	opts := UninstallOptions{
		ConfigDir:      shared,
		StateDir:       shared,
		Out:            &bytes.Buffer{},
		In:             strings.NewReader(""),
		Yes:            true,
		NonInteractive: true,
		KeepConfig:     true,
	}
	var out bytes.Buffer
	opts.Out = &out

	writeFile(t, shared, "zoomies.yaml", "server: {}\n")
	writeFile(t, shared, "encryption.key", "the only copy")
	writeFile(t, shared, "zoomies.db", "")
	// Something of the operator's own, which uninstall has never claimed.
	writeFile(t, shared, "fullchain.pem", "a certificate they put here")

	if err := Uninstall(context.Background(), opts); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	if !exists(filepath.Join(shared, "zoomies.yaml")) {
		t.Errorf("--keep-config was given and zoomies.yaml was deleted anyway:\n%s", out.String())
	}
	if !exists(filepath.Join(shared, "fullchain.pem")) {
		t.Errorf("a file that was never ours was deleted:\n%s", out.String())
	}
	// The database is still the thing being uninstalled.
	if exists(filepath.Join(shared, "zoomies.db")) {
		t.Errorf("the database survived, so nothing was uninstalled:\n%s", out.String())
	}
	// The key goes: it is not configuration, and leaving it behind is the
	// thing removing it at all is meant to prevent.
	if exists(filepath.Join(shared, "encryption.key")) {
		t.Errorf("the encryption key was left behind:\n%s", out.String())
	}
	// And the report has to name what survived, or the operator does not know
	// there is anything still to clean up.
	if report := out.String(); !strings.Contains(report, "fullchain.pem") {
		t.Errorf("the report did not name what was left behind:\n%s", report)
	}
}

// ---------------------------------------------------------------------------
// The one irreversible answer
// ---------------------------------------------------------------------------

// The volume is the database -- pools, runners, job history, the audit log --
// and deleting it cannot be undone. Every path into the question defaults to
// keeping it, and the one that would surprise somebody most is `--yes`: on
// nearly every other tool that means "yes to everything", and here it must
// still not mean "and delete my data".
func TestUninstallKeepsTheVolumeUnlessItIsAskedPlainly(t *testing.T) {
	yes, no := true, false

	for _, tc := range []struct {
		name  string
		opts  UninstallOptions
		input string
		want  bool
	}{
		{name: "the flag says so", opts: UninstallOptions{RemoveVolume: &yes}, want: true},
		{name: "the flag says not to", opts: UninstallOptions{RemoveVolume: &no}, want: false},
		{name: "--yes is not consent to lose the database", opts: UninstallOptions{Yes: true}, want: false},
		{name: "nobody is there to ask", opts: UninstallOptions{NonInteractive: true}, want: false},
		{name: "asked, and answered yes", input: "y\n", want: true},
		{name: "asked, and answered no", input: "n\n", want: false},
		{name: "asked, and just pressed return", input: "\n", want: false},
		{name: "asked, and the input ended", input: "", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			opts := tc.opts
			opts.Out = out
			opts.In = strings.NewReader(tc.input)

			got, err := wantsVolumeRemoved(opts, newUI(out), DeploymentRecord{})
			if err != nil {
				t.Fatalf("wantsVolumeRemoved: %v", err)
			}
			if got != tc.want {
				t.Errorf("wantsVolumeRemoved = %v, want %v", got, tc.want)
			}
		})
	}
}

// When the question is skipped because nobody is there to answer it, the
// operator is told the volume was kept and how to ask for it to go. A silent
// skip leaves somebody believing they uninstalled everything.
func TestUninstallSaysItKeptTheVolumeWhenItCouldNotAsk(t *testing.T) {
	out := &bytes.Buffer{}
	opts := UninstallOptions{NonInteractive: true, Out: out, In: strings.NewReader("")}

	if _, err := wantsVolumeRemoved(opts, newUI(out), DeploymentRecord{Volume: "zoomies-data"}); err != nil {
		t.Fatalf("wantsVolumeRemoved: %v", err)
	}
	said := out.String()
	if !strings.Contains(said, "zoomies-data") {
		t.Errorf("the note does not name the volume that was kept:\n%s", said)
	}
	if !strings.Contains(said, "--volumes") {
		t.Errorf("the note does not say how to delete it either:\n%s", said)
	}
}

// The prompt has to say what is about to be lost. "Delete the volume?" on its
// own is a question an operator answers wrongly once.
func TestUninstallVolumePromptNamesWhatIsInIt(t *testing.T) {
	out := &bytes.Buffer{}
	opts := UninstallOptions{Out: out, In: strings.NewReader("n\n")}

	if _, err := wantsVolumeRemoved(opts, newUI(out), DeploymentRecord{}); err != nil {
		t.Fatalf("wantsVolumeRemoved: %v", err)
	}
	asked := out.String()
	for _, want := range []string{"database", "cannot be undone"} {
		if !strings.Contains(asked, want) {
			t.Errorf("the prompt never says %q:\n%s", want, asked)
		}
	}
	// The default is spelled out rather than left to capitalisation alone.
	if !strings.Contains(asked, "[y/N]") {
		t.Errorf("the prompt does not show that keeping the volume is the default:\n%s", asked)
	}
}

// For a container deployment the environment file is the only copy of the
// encryption key, and the data volume is sealed with it. Uninstall keeps the
// volume unless it is asked plainly, so it must keep the key that opens it:
// otherwise "keeping it lets a later install pick up exactly where this left
// off" is false, and the operator finds out when every stored GitHub App key
// and webhook secret fails to decrypt.
func TestUninstallKeepsTheKeyForAVolumeItKeeps(t *testing.T) {
	yes, no := true, false

	for _, tc := range []struct {
		name         string
		removeVolume *bool
		keyKept      bool
	}{
		{name: "the volume is kept when nobody says otherwise", removeVolume: nil, keyKept: true},
		{name: "the volume is kept when the flag says not to delete it", removeVolume: &no, keyKept: true},
		{name: "the key goes with the volume it sealed", removeVolume: &yes, keyKept: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := uninstallOpts(t)
			opts.Yes = true
			opts.NonInteractive = true
			opts.RemoveVolume = tc.removeVolume
			var out bytes.Buffer
			opts.Out = &out

			deployDir := t.TempDir()
			envFile := writeFile(t, deployDir, ".env", "ZOOMIES_ENCRYPTION_KEY=the-only-copy\n")
			backup := writeFile(t, deployDir, ".env.bak.20260101-000000", "ZOOMIES_ENCRYPTION_KEY=the-only-copy\n")
			compose := writeFile(t, deployDir, ComposeFileName, "services: {}\n")
			// Names nothing on this machine can have, so the docker calls
			// uninstall makes are refused rather than acted on.
			rec := DeploymentRecord{
				Deployment: DeploymentDocker,
				Directory:  deployDir,
				Container:  "zoomies-uninstall-test-none",
				Volume:     "zoomies-uninstall-test-none",
				EnvFile:    envFile,
			}
			if _, err := WriteDeploymentRecord(opts.ConfigDir, rec); err != nil {
				t.Fatal(err)
			}

			if err := Uninstall(context.Background(), opts); err != nil {
				t.Fatalf("Uninstall: %v", err)
			}

			for _, path := range []string{envFile, backup} {
				if got := exists(path); got != tc.keyKept {
					t.Errorf("%s exists = %v, want %v\n%s", path, got, tc.keyKept, out.String())
				}
			}
			// Nothing else needs to survive: the compose file is regenerated
			// and the record describes a deployment that is gone.
			if exists(compose) || exists(DeploymentRecordPath(opts.ConfigDir)) {
				t.Errorf("the compose file and the deployment record should still be removed\n%s", out.String())
			}
			if tc.keyKept {
				if report := out.String(); !strings.Contains(report, "encryption key") || !strings.Contains(report, envFile) {
					t.Errorf("the report should say the environment file was left, and why:\n%s", report)
				}
			}
		})
	}
}

// The plan is printed before the volume question is asked, so what it says
// about the key has to be true for both answers.
func TestDeploymentItemsSayTheKeyStaysWithAKeptVolume(t *testing.T) {
	yes := true
	rec := DeploymentRecord{Deployment: DeploymentDocker, EnvFile: "/etc/zoomies/.env"}

	for _, tc := range []struct {
		name   string
		remove *bool
		want   string
	}{
		{"undecided", nil, "kept unless the data volume is deleted"},
		{"the volume is going", &yes, "removed with the data volume"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := uninstallOpts(t)
			opts.RemoveVolume = tc.remove
			for _, item := range deploymentItems(rec, opts) {
				if item.What == "environment file" {
					if !strings.Contains(item.Note, "encryption key") || !strings.Contains(item.Note, tc.want) {
						t.Fatalf("note = %q, want it to mention the key and %q", item.Note, tc.want)
					}
					return
				}
			}
			t.Fatal("the environment file is not in the plan")
		})
	}
}
