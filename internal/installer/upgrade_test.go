package installer

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
)

func TestUpgradeSaysWhetherAMovingImageAdvanced(t *testing.T) {
	for _, tc := range []struct {
		name      string
		before    string
		after     string
		runBefore string
		runAfter  string
		want      string
	}{
		{"same image", "sha256:aaaaaaaaaaaaaaaa", "sha256:aaaaaaaaaaaaaaaa", "sha256:aaaaaaaaaaaaaaaa", "sha256:aaaaaaaaaaaaaaaa", "Service image unchanged"},
		{"new image", "sha256:aaaaaaaaaaaaaaaa", "sha256:bbbbbbbbbbbbbbbb", "sha256:aaaaaaaaaaaaaaaa", "sha256:bbbbbbbbbbbbbbbb", "Service image updated"},
		// The tag was pulled before the upgrade while the service still ran
		// an older image. Saying "did not advance" there hid an upgrade that
		// recreated the service onto a new build and migrated its database.
		{"tag pulled earlier", "sha256:bbbbbbbbbbbbbbbb", "sha256:bbbbbbbbbbbbbbbb", "sha256:aaaaaaaaaaaaaaaa", "sha256:bbbbbbbbbbbbbbbb", "Service image updated: aaaaaaaaaaaa -> bbbbbbbbbbbb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, _ := upgradeFixture(t, DeploymentCompose)
			var out bytes.Buffer
			opts.Out = &out
			inspects, running, recreates := 0, 0, 0
			opts.run = func(_ context.Context, name string, args ...string) (string, error) {
				line := name + " " + strings.Join(args, " ")
				switch {
				case strings.Contains(line, " up "):
					recreates++
					return "", nil
				case strings.Contains(line, "config --images"):
					return opts.Image, nil
				case strings.Contains(line, "image inspect"):
					inspects++
					if inspects == 1 {
						return tc.before, nil
					}
					return tc.after, nil
				case strings.Contains(line, "{{.Image}}"):
					running++
					if running == 1 {
						return tc.runBefore, nil
					}
					return tc.runAfter, nil
				case name == "docker" && len(args) > 0 && args[0] == "inspect":
					return "true", nil
				default:
					return "", nil
				}
			}
			if err := Upgrade(context.Background(), opts); err != nil {
				t.Fatal(err)
			}
			if tc.runBefore != tc.runAfter && strings.Contains(out.String(), "did not advance") {
				t.Fatalf("output = %q, says nothing changed on an upgrade that moved the service", out.String())
			}
			if !strings.Contains(out.String(), tc.want) || !strings.Contains(out.String(), opts.Image) {
				t.Fatalf("output = %q, want %q and image", out.String(), tc.want)
			}
			wantRecreates := 1
			if tc.runBefore == tc.after {
				wantRecreates = 0
			}
			if recreates != wantRecreates {
				t.Fatalf("recreated the service %d times, want %d", recreates, wantRecreates)
			}
		})
	}
}

func upgradeFixture(t *testing.T, deployment Deployment) (UpgradeOptions, DeploymentRecord) {
	t.Helper()
	dir := t.TempDir()
	rec := DeploymentRecord{Deployment: deployment, Directory: dir, EnvFile: filepath.Join(dir, ".env"), Image: stockAgentRepository + ":v0.1", Mode: ModeAgent, Container: "zoomies", ComposeCommand: []string{"docker", "compose"}}
	if err := os.WriteFile(rec.EnvFile, []byte("# keep this comment\nZOOMIES_IMAGE="+rec.Image+"\nZOOMIES_JOIN_TOKEN=existing-credential\nCUSTOM_SETTING='leave me alone'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteDeploymentRecord(dir, rec); err != nil {
		t.Fatal(err)
	}
	// A deployment this release installed: its Compose file mounts the
	// shared folder and the folder is laid out, so the upgrade has nothing to
	// add and these tests see only the upgrade itself.
	if deployment == DeploymentCompose {
		rendered, err := RenderComposeFile(composeSpecFor(rec, deploymentSettings{cfg: config.Default(), read: true}, 0))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rec.ComposeFile(), []byte(rendered), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	shared := filepath.Join(dir, "shared")
	if _, err := PrepareSharedDir(shared, -1, -1); err != nil {
		t.Fatal(err)
	}
	return UpgradeOptions{ConfigDir: dir, Mode: ModeAgent, Image: stockAgentRepository + ":v9.0",
		shared: &sharedTarget{dir: shared, uid: -1, gid: -1}, socketGroup: func(string) int { return 0 }}, rec
}

func TestAnUnchangedImageStillStartsAStoppedController(t *testing.T) {
	opts, rec := upgradeFixture(t, DeploymentCompose)
	rec.Mode = ModeController
	var out bytes.Buffer
	opts.Out = &out
	recreated := false
	opts.run = func(_ context.Context, name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		switch {
		case strings.Contains(line, "image inspect"), strings.Contains(line, "{{.Image}}"):
			return "sha256:current", nil
		case strings.Contains(line, " up "):
			recreated = true
		case strings.Contains(line, "{{.State.Running}}"):
			return strconv.FormatBool(recreated), nil
		}
		return "", nil
	}
	p := &upgradePlan{opts: opts, record: rec, image: opts.Image}
	if err := p.upgradeCompose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !recreated || strings.Contains(out.String(), "keeping the running container") {
		t.Fatalf("a stopped controller was left alone: recreated=%v, output=%q", recreated, out.String())
	}
}

func TestComposeUpgradeKeepsConfigurationAndPullsBeforeRestarting(t *testing.T) {
	for _, fail := range []string{"", "pull", "up"} {
		t.Run(fail, func(t *testing.T) {
			opts, rec := upgradeFixture(t, DeploymentCompose)
			before, _ := os.ReadFile(rec.EnvFile)
			var calls []string
			opts.run = func(_ context.Context, name string, args ...string) (string, error) {
				line := name + " " + strings.Join(args, " ")
				calls = append(calls, line)
				if strings.Contains(line, "config --images") {
					return opts.Image, nil
				}
				if name == "systemctl" {
					return "", nil
				}
				if name == "docker" {
					if len(args) > 0 && args[0] == "inspect" {
						return "true", nil
					}
					return "", nil
				}
				if !strings.Contains(line, "--env-file "+rec.EnvFile) {
					t.Errorf("no recorded env file: %s", line)
				}
				if strings.Contains(line, "pull zoomies") {
					current, _ := os.ReadFile(rec.EnvFile)
					if string(current) != string(before) {
						t.Error("env changed before image pull succeeded")
					}
				}
				if fail != "" && strings.Contains(line, " "+fail+" ") {
					return "", errors.New("test refusal")
				}
				return "", nil
			}
			err := Upgrade(context.Background(), opts)
			if (err != nil) != (fail != "") {
				t.Fatalf("failure=%s: %v", fail, err)
			}
			after, _ := os.ReadFile(rec.EnvFile)
			want := string(before)
			if fail == "" {
				want = strings.ReplaceAll(want, rec.Image, opts.Image)
			}
			if string(after) != want {
				t.Fatalf("configuration changed unexpectedly: %q", after)
			}
			stored, ok := ReadDeploymentRecord(opts.ConfigDir)
			if !ok || (fail == "" && stored.Image != opts.Image) || (fail != "" && stored.Image != rec.Image) {
				t.Fatalf("record disagrees with result: %+v", stored)
			}
			joined := strings.Join(calls, "\n")
			for _, call := range calls {
				if strings.Contains(call, " up ") && !strings.Contains(call, "--timeout 1200") {
					t.Fatalf("upgrade or rollback can kill admitted work: %s", call)
				}
			}
			if fail == "pull" && strings.Contains(joined, " up ") {
				t.Fatal("restarted after failed pull")
			}
			if fail == "" && (!strings.Contains(joined, "--no-deps --force-recreate --timeout 1200 zoomies") || strings.Index(joined, "pull zoomies") > strings.Index(joined, " up ")) {
				t.Fatalf("unsafe ordering: %s", joined)
			}
		})
	}
}

func TestUpgradePreflightNeverPullsOrChangesAnExistingDeployment(t *testing.T) {
	opts, rec := upgradeFixture(t, DeploymentCompose)
	opts.Check = true
	before, _ := os.ReadFile(rec.EnvFile)
	opts.run = func(_ context.Context, name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		// Reading is allowed -- the image the file names, where the data
		// volume is -- and nothing else.
		if strings.Contains(line, "inspect --type container") {
			return "", nil
		}
		if !strings.Contains(line, "config --images") {
			t.Fatalf("preflight tried to change something: %s", line)
		}
		return opts.Image, nil
	}
	if err := Upgrade(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(rec.EnvFile)
	if string(after) != string(before) {
		t.Fatal("preflight changed environment")
	}
}

func TestUpgradeRefusesAnUnreadableRecordOrACustomImageBeforeChangingAnything(t *testing.T) {
	opts, rec := upgradeFixture(t, DeploymentCompose)
	opts.Image = ""
	rec.Image = "registry.example/custom:old"
	if _, err := WriteDeploymentRecord(opts.ConfigDir, rec); err != nil {
		t.Fatal(err)
	}
	opts.run = func(context.Context, string, ...string) (string, error) {
		t.Fatal("a refused upgrade ran a command")
		return "", nil
	}
	if err := Upgrade(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "--image") {
		t.Fatalf("custom image: %v", err)
	}
	if err := os.WriteFile(DeploymentRecordPath(opts.ConfigDir), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Upgrade(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "record is invalid") {
		t.Fatalf("invalid record: %v", err)
	}
}

func TestNativeUpgradeRestartsTheExistingAgentAndRefusesTheWrongBinary(t *testing.T) {
	requirePOSIX(t)
	for _, wrong := range []bool{false, true} {
		t.Run(map[bool]string{false: "matching", true: "wrong path"}[wrong], func(t *testing.T) {
			var calls []string
			shared := filepath.Join(t.TempDir(), "shared")
			if _, err := PrepareSharedDir(shared, -1, -1); err != nil {
				t.Fatal(err)
			}
			opts := UpgradeOptions{ConfigDir: t.TempDir(), Mode: ModeAgent, BinaryPath: "/custom/bin/zoomies",
				shared: &sharedTarget{dir: shared, uid: -1, gid: -1}}
			opts.run = func(_ context.Context, name string, args ...string) (string, error) {
				line := name + " " + strings.Join(args, " ")
				calls = append(calls, line)
				if strings.Contains(line, "LoadState") {
					return "loaded", nil
				}
				if strings.Contains(line, "ExecStart") {
					if wrong {
						return "{ path=/other/zoomies ; }", nil
					}
					return "{ path=/custom/bin/zoomies ; argv[]=/custom/bin/zoomies agent ; }", nil
				}
				return "", nil
			}
			err := Upgrade(context.Background(), opts)
			if (err != nil) != wrong {
				t.Fatalf("wrong=%v: %v", wrong, err)
			}
			restarted := strings.Contains(strings.Join(calls, "\n"), "systemctl restart zoomies-agent")
			if restarted == wrong {
				t.Fatalf("wrong service restart: %v", calls)
			}
		})
	}
}

func TestStockRunnerImagesAreRefreshedOnceWithoutChangingPinnedReferences(t *testing.T) {
	var pulled []string
	p := upgradePlan{opts: UpgradeOptions{DockerHost: "unix:///tmp/daemon.sock", Out: os.Stdout, run: func(_ context.Context, _ string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "image ls") {
			return "ghcr.io/eyupio/zoomies-runner:dev\nghcr.io/eyupio/zoomies-runner:dev\nprivate.example/custom:v2\nghcr.io/eyupio/zoomies-runner-docker:v1\nghcr.io/eyupio/zoomies-runner:<none>", nil
		}
		pulled = append(pulled, args[len(args)-1])
		return "", nil
	}}}
	if err := p.pullRunnerImages(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"ghcr.io/eyupio/zoomies-runner:dev", "ghcr.io/eyupio/zoomies-runner-docker:v1"}
	if !reflect.DeepEqual(pulled, want) {
		t.Fatalf("pulled %v", pulled)
	}
}

func TestNativeUpgradeRestartsBothInstalledServicesWithOneBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd native services")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	opts, rec := upgradeFixture(t, DeploymentNative)
	opts.Mode, opts.BinaryPath = "", "/custom/bin/zoomies"
	rec.Mode = ModeSingle
	if _, err := WriteDeploymentRecord(opts.ConfigDir, rec); err != nil {
		t.Fatal(err)
	}
	cfg := "server:\n  bind: " + strings.TrimPrefix(server.URL, "http://") + "\nagent:\n  embedded: false\n  backend: process\ndatabase:\n  path: " + filepath.Join(opts.ConfigDir, "missing.db") + "\n"
	if err := os.WriteFile(filepath.Join(opts.ConfigDir, "zoomies.yaml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	var restarts []string
	opts.run = func(_ context.Context, name string, args ...string) (string, error) {
		line := strings.Join(args, " ")
		if strings.Contains(line, "LoadState") {
			return "loaded", nil
		}
		if strings.Contains(line, "ExecStart") {
			return "{ path=/custom/bin/zoomies ; }", nil
		}
		if name == "systemctl" && len(args) > 1 && args[0] == "restart" {
			restarts = append(restarts, args[1])
		}
		return "", nil
	}
	if err := Upgrade(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restarts, []string{UnitController, UnitAgent}) {
		t.Fatalf("restarted %v", restarts)
	}
}

func TestNativeRunnerRefreshUsesTheDefaultSocketAndOnlyStockRepositories(t *testing.T) {
	var calls []string
	p := upgradePlan{unit: UnitAgent, nativeUnits: []string{UnitAgent}, opts: UpgradeOptions{ConfigDir: t.TempDir(), Out: &bytes.Buffer{}, run: func(_ context.Context, name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		calls = append(calls, line)
		if strings.Contains(line, "image ls") {
			return "ghcr.io/eyupio/zoomies-runner:dev\nghcr.io/eyupio/zoomies-runner-custom:dev\nghcr.io/eyupio/zoomies-runner-full:dev\n", nil
		}
		return "", nil
	}}}
	if err := p.pullRunnerImages(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || calls[1] != "docker pull ghcr.io/eyupio/zoomies-runner:dev" || calls[2] != "docker pull ghcr.io/eyupio/zoomies-runner-full:dev" {
		t.Fatalf("calls %v", calls)
	}
}

func TestUpgradeResultFollowsVerificationAndHealth(t *testing.T) {
	opts, out, _ := waitFixture(t, 0, "running", composeHealthCheck)
	opts.Doctor = func(_ context.Context, _ *config.Config) { out.WriteString("Health observed\n") }
	if err := Upgrade(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	last := -1
	for _, step := range []string{"2/4 Deployment", "3/4 Verify", "4/4 Host health", "Health observed", "Upgrade complete"} {
		at := strings.Index(text, step)
		if at <= last {
			t.Fatalf("out of order %q: %s", step, text)
		}
		last = at
	}
}

func TestUpgradeRestartsOnlyAHealthReporterUsingTheInstalledBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd reporter")
	}
	for _, matching := range []bool{false, true} {
		var restarted bool
		p := upgradePlan{opts: UpgradeOptions{BinaryPath: "/custom/zoomies", Out: &bytes.Buffer{}, run: func(_ context.Context, _ string, args ...string) (string, error) {
			line := strings.Join(args, " ")
			if strings.Contains(line, "ActiveState") {
				return "active", nil
			}
			if strings.Contains(line, "ExecStart") {
				if matching {
					return "{ path=/custom/zoomies ; }", nil
				}
				return "{ path=/another/zoomies ; }", nil
			}
			if args[0] == "restart" {
				restarted = true
			}
			return "", nil
		}}}
		if err := p.restartHealthReporter(context.Background()); err != nil {
			t.Fatal(err)
		}
		if restarted != matching {
			t.Fatalf("matching=%v restarted=%v", matching, restarted)
		}
	}
}
