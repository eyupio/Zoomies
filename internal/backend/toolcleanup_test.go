package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func cleanupRunner(t *testing.T, shared string) ContainerInspect {
	t.Helper()
	farm := toolFarmDir(shared, "runner-501")
	if err := os.MkdirAll(filepath.Join(farm, "go/1.27.1/x64/api"), 0755); err != nil {
		t.Fatal(err)
	}
	return ContainerInspect{ID: "runner-id", Image: "sha256:immutable", Config: &ContainerConfig{
		Image:  "runner:moving-tag",
		Labels: map[string]string{LabelManaged: "true", LabelRole: roleRunner, LabelName: "runner-501", LabelToolFarm: farm},
	}}
}

// Inject the unlinkat failure so this regression also runs in root CI jobs.
// The fake daemon models the helper clearing container-owned descendants.
func TestToolCleanupRecoversPermissionFailure(t *testing.T) {
	// Recovery uses a POSIX bind mount and shell. A Windows drive-letter
	// path contains the ':' separator that this mount's safety check rejects.
	requirePOSIX(t)
	for _, scenario := range []string{"success", "auto-removed", "auto-removing", "still auto-removing", "exit failure", "wait failure", "start failure", "cancelled", "still denied", "stale helper", "foreign helper"} {
		t.Run(scenario, func(t *testing.T) {
			shared := t.TempDir()
			runner := cleanupRunner(t, shared)
			farm := runner.Config.Labels[LabelToolFarm]
			var created ContainerCreateRequest
			var deleted []string
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := newFakeEngine(t, map[string]http.HandlerFunc{
				"GET " + v + "/containers/{id}/json": func(w http.ResponseWriter, r *http.Request) {
					if scenario == "stale helper" || scenario == "foreign helper" {
						labels := map[string]string{LabelRole: roleToolCleanup, LabelToolFarm: farm}
						if scenario == "foreign helper" {
							labels[LabelRole] = roleRunner
						}
						writeJSON(w, 200, ContainerInspect{ID: "stale", Config: &ContainerConfig{Labels: labels}})
						return
					}
					w.WriteHeader(404)
				},
				"POST " + v + "/containers/create": func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Query().Get("name") != toolCleanupName(runner) {
						t.Error("helper name is not stable for retry")
					}
					if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
						t.Error(err)
					}
					writeJSON(w, 201, map[string]string{"Id": "helper"})
				},
				"POST " + v + "/containers/helper/start": func(w http.ResponseWriter, r *http.Request) {
					if scenario == "start failure" {
						w.WriteHeader(500)
						return
					}
					if scenario == "cancelled" {
						cancel()
					}
					w.WriteHeader(204)
				},
				"POST " + v + "/containers/helper/wait": func(w http.ResponseWriter, r *http.Request) {
					if scenario == "wait failure" {
						w.WriteHeader(500)
						return
					}
					if scenario == "auto-removed" {
						w.WriteHeader(404)
						return
					}
					code := 0
					if scenario == "exit failure" {
						code = 1
					}
					writeJSON(w, 200, map[string]int{"StatusCode": code})
				},
				"DELETE " + v + "/containers/{id}": func(w http.ResponseWriter, r *http.Request) {
					deleted = append(deleted, r.PathValue("id"))
					// The helper is AutoRemove: once it exits the daemon is
					// already removing it, and a DELETE that arrives first is
					// told so rather than doing it.
					if scenario == "still auto-removing" && r.PathValue("id") == "helper" {
						writeJSON(w, http.StatusConflict, map[string]string{"message": "removal of container helper is already in progress"})
						return
					}
					if scenario == "auto-removing" && r.PathValue("id") == "helper" {
						if len(deleted) == 1 {
							writeJSON(w, http.StatusConflict, map[string]string{"message": "removal of container helper is already in progress"})
							return
						}
						w.WriteHeader(404)
						return
					}
					w.WriteHeader(204)
				},
			})
			b := dockerBackendFor(t, f, DockerOptions{SharedDir: shared})
			b.removalWait = 200 * time.Millisecond
			calls := 0
			err := b.removeRunnerToolFarm(ctx, runner, func(path string) error {
				calls++
				if path != farm {
					t.Fatalf("cleanup touched %s", path)
				}
				if calls == 1 || scenario == "still denied" {
					return &os.PathError{Op: "unlinkat", Path: filepath.Join(farm, "go/1.27.1/x64/api/go1.txt"), Err: os.ErrPermission}
				}
				return os.RemoveAll(path)
			})
			wantSuccess := scenario == "success" || scenario == "auto-removed" || scenario == "auto-removing" || scenario == "still auto-removing" || scenario == "stale helper"
			if (err == nil) != wantSuccess {
				t.Fatalf("cleanup = %v", err)
			}
			if scenario == "foreign helper" {
				if len(deleted) != 0 || created.Image != "" {
					t.Fatal("foreign helper was touched")
				}
				return
			}
			if !slices.Contains(deleted, "helper") {
				t.Fatal("helper was not removed, including after cancellation")
			}
			if scenario == "stale helper" && (len(deleted) != 2 || deleted[0] != "stale") {
				t.Fatalf("stale helper not removed first: %v", deleted)
			}
			if created.Image != runner.Image || created.User != "0:0" || created.WorkingDir != "/" {
				t.Fatalf("unsafe helper identity: %+v", created)
			}
			hc := created.HostConfig
			if !slices.Equal(hc.Binds, []string{farm + ":/cleanup"}) || hc.NetworkMode != "none" || hc.Privileged || !hc.ReadonlyRootfs {
				t.Fatalf("helper has excessive access: %+v", hc)
			}
			if !slices.Equal(hc.CapDrop, []string{"ALL"}) || !slices.Equal(hc.CapAdd, []string{"DAC_OVERRIDE", "FOWNER"}) || !slices.Equal(hc.SecurityOpt, []string{"no-new-privileges"}) {
				t.Fatalf("capabilities = %+v", hc)
			}
			if !hc.AutoRemove || hc.Memory == 0 || hc.NanoCPUs == 0 || hc.PidsLimit == nil {
				t.Fatal("helper is not bounded and automatically removed")
			}
			if len(created.Env) != 0 || created.Labels[LabelManaged] != "" {
				t.Fatal("helper inherited runner credentials or reconciliation labels")
			}
			if wantSuccess && calls != 2 {
				t.Fatal("helper success was not verified on the host")
			}
		})
	}
}

func TestToolCleanupDoesNotEscalateOtherErrors(t *testing.T) {
	for _, localErr := range []error{nil, errors.New("read-only filesystem")} {
		b := &DockerBackend{}
		runner := ContainerInspect{Config: &ContainerConfig{Labels: map[string]string{LabelToolFarm: "/farm"}}}
		if got := b.removeRunnerToolFarm(context.Background(), runner, func(string) error { return localErr }); got != localErr {
			t.Fatalf("got %v, want original error %v", got, localErr)
		}
	}
}

func TestToolCleanupRejectsUntrustedPaths(t *testing.T) {
	for _, scenario := range []string{"kept cache", "other runner", "relative", "symlink farm", "symlink parent", "unmanaged", "missing image"} {
		t.Run(scenario, func(t *testing.T) {
			shared := t.TempDir()
			runner := cleanupRunner(t, shared)
			farm := runner.Config.Labels[LabelToolFarm]
			switch scenario {
			case "kept cache":
				runner.Config.Labels[LabelToolFarm] = filepath.Join(shared, "cache", "tools")
			case "other runner":
				runner.Config.Labels[LabelToolFarm] = toolFarmDir(shared, "other")
			case "relative":
				runner.Config.Labels[LabelToolFarm] = "relative"
			case "unmanaged":
				delete(runner.Config.Labels, LabelManaged)
			case "missing image":
				runner.Image = ""
			case "symlink farm", "symlink parent":
				path := farm
				if scenario == "symlink parent" {
					path = filepath.Dir(farm)
				}
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Skip(err)
				}
			}
			b := &DockerBackend{sharedDir: shared}
			if err := b.validateToolCleanup(runner); err == nil {
				t.Fatal("unsafe recovery accepted")
			}
		})
	}
}

func TestToolCleanupScriptPreservesSymlinkTargetsAndRemovesDotfiles(t *testing.T) {
	requirePOSIX(t)
	farm, kept := t.TempDir(), t.TempDir()
	for _, name := range []string{"go/1.27.1/x64/api/go1.txt", ".hidden/tool", "..hidden/tool", "--option/tool", "space and ' quote/tool"} {
		path := filepath.Join(farm, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("temporary"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(kept, "do-not-delete")
	if err := os.WriteFile(marker, []byte("kept"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(kept, filepath.Join(farm, "shared-version")); err != nil {
		t.Fatal(err)
	}
	// Substitute a quoted positional parameter, never interpolate a host path
	// into the script. Production always uses the fixed /cleanup mountpoint.
	script := strings.ReplaceAll(toolCleanupScript, "/cleanup", `"$1"`)
	if out, err := exec.Command("/bin/sh", "-c", script, "cleanup-test", farm).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	entries, err := os.ReadDir(farm)
	if err != nil || len(entries) != 0 {
		t.Fatalf("farm not empty: %v, %v", entries, err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "kept" {
		t.Fatal("kept cache was touched")
	}
}
