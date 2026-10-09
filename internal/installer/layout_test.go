package installer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
)

// mountsShared reports whether a Compose file's zoomies service mounts the
// shared folder.
func mountsShared(body []byte) (bool, error) {
	m, err := composeServiceMounts(body)
	return m.targets[SharedHostDir], err
}

var sharedWant = wantMount{target: SharedHostDir, bind: SharedHostDir + ":" + SharedHostDir, why: "the shared folder"}

func addShared(body []byte) ([]byte, error) {
	return addComposeMounts(body, []wantMount{sharedWant}, "")
}

// fakeDeploymentCommands answers the commands a compose upgrade runs as a
// healthy host would, and records them.
func fakeDeploymentCommands(opts *UpgradeOptions, calls *[]string) {
	opts.run = func(_ context.Context, name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		*calls = append(*calls, line)
		switch {
		case strings.Contains(line, "config --images"):
			return opts.Image, nil
		case name == "docker" && len(args) > 0 && args[0] == "inspect":
			return "true", nil
		}
		return "", nil
	}
}

// withoutSharedMount rewrites the fixture's Compose file as a release before
// the shared folder wrote it, and empties the shared folder.
func withoutSharedMount(t *testing.T, opts *UpgradeOptions, rec DeploymentRecord) string {
	t.Helper()
	body, err := os.ReadFile(rec.ComposeFile())
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.Contains(line, SharedHostDir) {
			kept = append(kept, line)
		}
	}
	old := strings.Join(kept, "\n") + "\n# an operator's own note, kept by any edit\n"
	if err := os.WriteFile(rec.ComposeFile(), []byte(old), 0o640); err != nil {
		t.Fatal(err)
	}
	if mounted, err := mountsShared([]byte(old)); err != nil || mounted {
		t.Fatalf("the older file still mounts the shared folder (%v, %v):\n%s", mounted, err, old)
	}
	opts.shared.dir = filepath.Join(t.TempDir(), "shared")
	return old
}

func TestComposeMountsSharedReadsBothVolumeSyntaxes(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"short", "services:\n  zoomies:\n    volumes:\n      - /var/lib/zoomies/shared:/var/lib/zoomies/shared\n", true},
		{"short with a mode", "services:\n  zoomies:\n    volumes:\n      - /var/lib/zoomies/shared:/var/lib/zoomies/shared:rw\n", true},
		{"long", "services:\n  zoomies:\n    volumes:\n      - type: bind\n        source: /srv/shared\n        target: /var/lib/zoomies/shared\n", true},
		{"elsewhere", "services:\n  zoomies:\n    volumes:\n      - /var/lib/zoomies/shared:/shared\n", false},
		{"absent", "services:\n  zoomies:\n    image: zoomies\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mountsShared([]byte(tc.body))
			if err != nil || got != tc.want {
				t.Fatalf("composeMountsShared = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	if _, err := mountsShared([]byte("services:\n  other: {}\n")); err == nil {
		t.Error("a file with no zoomies service was read as one without the mount")
	}
}

// The Compose file is the operator's as much as the installer's. Adding the
// mount by writing the file again from the template would drop a service or a
// label they added; the edit has to leave everything else where it was.
func TestAddingTheSharedMountKeepsTheRestOfTheComposeFile(t *testing.T) {
	body := `# written by zoomies init
services:
  zoomies:
    image: ${ZOOMIES_IMAGE}
    labels:
      team: platform # who to ask
    volumes:
      - zoomies-data:/var/lib/zoomies
  sidecar:
    image: example/sidecar
volumes:
  zoomies-data: {}
`
	edited, err := addShared([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if mounted, err := mountsShared(edited); err != nil || !mounted {
		t.Fatalf("the edited file does not mount the shared folder (%v):\n%s", err, edited)
	}
	for _, keep := range []string{"# written by zoomies init", "team: platform # who to ask", "zoomies-data:/var/lib/zoomies", "example/sidecar", "${ZOOMIES_IMAGE}", "Added by zoomies upgrade"} {
		if !strings.Contains(string(edited), keep) {
			t.Errorf("the edit lost %q:\n%s", keep, edited)
		}
	}

	// A service with no volumes at all gets the list as well as the entry.
	edited, err = addShared([]byte("services:\n  zoomies:\n    image: zoomies\n"))
	if err != nil {
		t.Fatal(err)
	}
	if mounted, _ := mountsShared(edited); !mounted {
		t.Fatalf("no volumes list was added:\n%s", edited)
	}
}

// Nothing on the host changes without the operator's say, and each way of
// giving or withholding it lands where it should.
func TestAnUpgradeAddsTheSharedMountOnlyWithApproval(t *testing.T) {
	for _, tc := range []struct {
		name    string
		yes     bool
		tty     bool
		answer  string
		applied bool
		says    string
	}{
		{name: "--yes", yes: true, applied: true, says: "Added"},
		{name: "enter at the terminal", tty: true, answer: "\n", applied: true, says: "Add them now?"},
		{name: "yes at the terminal", tty: true, answer: "yes\n", applied: true, says: "Add them now?"},
		{name: "no at the terminal", tty: true, answer: "n\n", says: "zoomies upgrade --yes"},
		{name: "nobody at the terminal", tty: true, answer: "", says: "zoomies upgrade --yes"},
		{name: "unattended", says: "zoomies upgrade --yes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, rec := upgradeFixture(t, DeploymentCompose)
			old := withoutSharedMount(t, &opts, rec)
			var out bytes.Buffer
			var calls []string
			opts.Out = &out
			opts.AssumeYes = tc.yes
			opts.Interactive = tc.tty
			opts.In = strings.NewReader(tc.answer)
			fakeDeploymentCommands(&opts, &calls)

			// Declining is not a failure: the upgrade goes on, as it
			// always did, without what it offered.
			if err := Upgrade(context.Background(), opts); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), SharedHostDir) || !strings.Contains(out.String(), tc.says) {
				t.Errorf("output does not say %q about %s:\n%s", tc.says, SharedHostDir, out.String())
			}
			now, _ := os.ReadFile(rec.ComposeFile())
			backups, _ := filepath.Glob(rec.ComposeFile() + ".bak.*")
			_, folderErr := os.Stat(filepath.Join(opts.shared.dir, "cache", "tools"))
			if !tc.applied {
				if string(now) != old || len(backups) != 0 || folderErr == nil {
					t.Fatalf("changed the host without approval: backups %v, folder %v, file:\n%s", backups, folderErr, now)
				}
				return
			}
			if mounted, _ := mountsShared(now); !mounted {
				t.Errorf("the Compose file was not given the mount:\n%s", now)
			}
			if !strings.Contains(string(now), "an operator's own note") {
				t.Error("the edit dropped the operator's comment")
			}
			if len(backups) != 1 {
				t.Fatalf("backups = %v, want the file as it was kept once", backups)
			}
			if kept, _ := os.ReadFile(backups[0]); string(kept) != old {
				t.Error("the backup is not the file as it was")
			}
			if folderErr != nil {
				t.Errorf("the shared folder's layout was not created: %v", folderErr)
			}
			if !strings.Contains(strings.Join(calls, "\n"), " up ") {
				t.Error("the upgrade did not go on after adding the mount")
			}
		})
	}
}

// A deleted docker-compose.yml leaves nothing to bring the new image up with,
// so an upgrade that cannot write it again stops before it pulls anything --
// and one that may, writes it from what the deployment recorded.
func TestAMissingComposeFileIsWrittenAgainOrStopsTheUpgrade(t *testing.T) {
	for _, yes := range []bool{false, true} {
		t.Run(map[bool]string{false: "unattended", true: "--yes"}[yes], func(t *testing.T) {
			opts, rec := upgradeFixture(t, DeploymentCompose)
			if err := os.Remove(rec.ComposeFile()); err != nil {
				t.Fatal(err)
			}
			var calls []string
			var out bytes.Buffer
			opts.Out = &out
			opts.AssumeYes = yes
			fakeDeploymentCommands(&opts, &calls)

			err := Upgrade(context.Background(), opts)
			if !yes {
				if err == nil || !strings.Contains(err.Error(), "zoomies upgrade --yes") {
					t.Fatalf("err = %v, want one naming zoomies upgrade --yes", err)
				}
				for _, c := range calls {
					// Reading where the data is changes nothing.
					if !strings.Contains(c, " inspect ") {
						t.Fatalf("a stopped upgrade ran %v", calls)
					}
				}
				if _, err := os.Stat(rec.ComposeFile()); !os.IsNotExist(err) {
					t.Error("the file was written without approval")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(rec.ComposeFile())
			if err != nil {
				t.Fatal(err)
			}
			if mounted, _ := mountsShared(body); !mounted {
				t.Errorf("the file written again does not mount the shared folder:\n%s", body)
			}
		})
	}
}

// install.sh checks before it replaces the binary, so an unattended upgrade
// that is bound to stop for a missing Compose file has to stop at the check,
// with the old binary still in place.
func TestAnUnattendedCheckRefusesWhatTheRealRunCouldNotFinish(t *testing.T) {
	opts, rec := upgradeFixture(t, DeploymentCompose)
	if err := os.Remove(rec.ComposeFile()); err != nil {
		t.Fatal(err)
	}
	var calls []string
	fakeDeploymentCommands(&opts, &calls)
	opts.Check = true
	opts.NonInteractive = true
	if err := Upgrade(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "zoomies upgrade --yes") {
		t.Fatalf("unattended check = %v, want a refusal naming zoomies upgrade --yes", err)
	}
	opts.AssumeYes = true
	if err := Upgrade(context.Background(), opts); err != nil {
		t.Fatalf("a check with --yes refused: %v", err)
	}
	if _, err := os.Stat(rec.ComposeFile()); !os.IsNotExist(err) {
		t.Error("a check wrote the Compose file")
	}
}

// --check is what install.sh runs before it replaces the binary. It says what
// the upgrade will offer and changes none of it.
func TestAnUpgradeCheckReportsWhatIsMissingAndChangesNothing(t *testing.T) {
	opts, rec := upgradeFixture(t, DeploymentCompose)
	old := withoutSharedMount(t, &opts, rec)
	var calls []string
	var out bytes.Buffer
	opts.Out = &out
	opts.Check = true
	opts.AssumeYes = true
	fakeDeploymentCommands(&opts, &calls)
	if err := Upgrade(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "will offer") || !strings.Contains(out.String(), SharedHostDir) {
		t.Errorf("the check did not say what it would offer:\n%s", out.String())
	}
	if now, _ := os.ReadFile(rec.ComposeFile()); string(now) != old {
		t.Error("the check edited the Compose file")
	}
	if _, err := os.Stat(opts.shared.dir); !os.IsNotExist(err) {
		t.Error("the check created the shared folder")
	}
}

func TestANativeUpgradeCreatesTheSharedFolderWithApproval(t *testing.T) {
	requirePOSIX(t)
	shared := filepath.Join(t.TempDir(), "shared")
	opts := UpgradeOptions{ConfigDir: t.TempDir(), Mode: ModeAgent, BinaryPath: "/custom/bin/zoomies", AssumeYes: true,
		shared: &sharedTarget{dir: shared, uid: -1, gid: -1}, helperHost: noHelperHost(t.TempDir())}
	opts.run = func(_ context.Context, name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		if strings.Contains(line, "LoadState") {
			return "loaded", nil
		}
		if strings.Contains(line, "ExecStart") {
			return "{ path=/custom/bin/zoomies ; argv[]=/custom/bin/zoomies agent ; }", nil
		}
		return "", nil
	}
	if err := Upgrade(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if problems := SharedDirProblems(shared, -1); len(problems) != 0 {
		t.Fatalf("the shared folder is not ready: %v", problems)
	}
}

func TestAskApprovalTakesEnterAsYesAndNothingAsNo(t *testing.T) {
	for in, want := range map[string]bool{
		"\n": true, "y\n": true, "YES\n": true, "yes": true,
		"n\n": false, "no\n": false, "maybe\n": false, "": false,
	} {
		if got := askApproval(strings.NewReader(in), &bytes.Buffer{}, "? "); got != want {
			t.Errorf("askApproval(%q) = %v, want %v", in, got, want)
		}
	}
}

// A controller host that has no CLI file is offered one, named in the list of
// additions an upgrade asks approval for; one that already names a controller
// is not, and an agent-only host has no controller to name.
func TestAnUpgradeOffersTheCLIConnectionFileOnlyWhereItIsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli.yaml")
	t.Setenv("ZOOMIES_CLI_CONFIG", path)
	cfg := config.Default()
	cfg.Server.ExternalURL = "https://zoomies.example.com"
	s := deploymentSettings{cfg: cfg, read: true}
	p := &upgradePlan{record: DeploymentRecord{Mode: ModeController}}

	changes := p.cliConfigChanges(s)
	if len(changes) != 1 || !strings.Contains(changes[0].what, path) || !strings.Contains(changes[0].what, "https://zoomies.example.com") || changes[0].required {
		t.Fatalf("changes = %+v, want one optional addition naming the file and the controller", changes)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("offering the file wrote it")
	}
	if err := changes[0].apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(path); !strings.Contains(string(raw), "url: https://zoomies.example.com") {
		t.Errorf("approval must write the url: %s", raw)
	}
	if again := p.cliConfigChanges(s); len(again) != 0 {
		t.Errorf("a file that names a controller is not offered again: %+v", again)
	}

	p.record.Mode = ModeAgent
	_ = os.Remove(path)
	if got := p.cliConfigChanges(s); len(got) != 0 {
		t.Errorf("an agent host has no controller to name: %+v", got)
	}
}
