package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAnUnchangedImageKeepsTheControllerContainer(t *testing.T) {
	for _, deployment := range []Deployment{DeploymentCompose, DeploymentDocker} {
		t.Run(string(deployment), func(t *testing.T) {
			opts, rec := upgradeFixture(t, deployment)
			rec.Mode = ModeController
			var out bytes.Buffer
			opts.Out = &out
			var calls []string
			opts.run = func(_ context.Context, name string, args ...string) (string, error) {
				line := name + " " + strings.Join(args, " ")
				calls = append(calls, line)
				switch {
				case strings.Contains(line, "image inspect"), strings.Contains(line, "{{.Image}}"):
					return "sha256:current", nil
				case strings.Contains(line, "{{.State.Running}}"):
					return "true", nil
				}
				return "", nil
			}
			p := &upgradePlan{opts: opts, record: rec, image: opts.Image}
			var err error
			if deployment == DeploymentCompose {
				err = p.upgradeCompose(context.Background())
			} else {
				// No Docker API client is needed on this path: stopping or
				// replacing the existing container would fail this test.
				err = p.upgradeDocker(context.Background())
			}
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(calls, "\n")
			if !strings.Contains(joined, " pull ") || strings.Contains(joined, " up ") {
				t.Fatalf("upgrade must pull without recreating the container: %s", joined)
			}
			if !strings.Contains(out.String(), "keeping the running container") {
				t.Fatalf("output = %q, want the skipped restart explained", out.String())
			}
			stored, ok := ReadDeploymentRecord(opts.ConfigDir)
			if !ok || stored.Image != opts.Image {
				t.Fatalf("new image reference was not remembered: %+v", stored)
			}
			env, err := ParseEnvFile(rec.EnvFile)
			if err != nil || env["ZOOMIES_IMAGE"] != opts.Image || env["CUSTOM_SETTING"] != "leave me alone" {
				t.Fatalf("environment = %v, %v", env, err)
			}
			if info, err := os.Stat(rec.EnvFile); err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("environment permissions changed: %v, %v", info, err)
			}
		})
	}
}

func TestDockerUpgradeRestoresTheOldContainerWhenTheReplacementCannotStart(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failed start"}[fail], func(t *testing.T) {
			var calls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := r.URL.Path
				calls = append(calls, r.Method+" "+path+"?"+r.URL.RawQuery)
				switch {
				case strings.HasSuffix(path, "/zoomies/json"):
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "old", "Name": "/zoomies", "Config": map[string]any{"Image": stockAgentRepository + ":old"}, "HostConfig": map[string]any{"AutoRemove": false}, "State": map[string]any{"Running": true}})
				case strings.HasSuffix(path, "/zoomies-before-upgrade/json"):
					w.WriteHeader(404)
				case strings.HasSuffix(path, "/containers/create"):
					w.WriteHeader(201)
					_, _ = w.Write([]byte(`{"Id":"new"}`))
				case strings.HasSuffix(path, "/new/start") && fail:
					w.WriteHeader(500)
					_, _ = w.Write([]byte(`{"message":"cannot start"}`))
				case strings.HasSuffix(path, "/new/json"):
					_, _ = w.Write([]byte(`{"Id":"new","State":{"Running":true}}`))
				default:
					w.WriteHeader(204)
				}
			}))
			defer server.Close()
			opts, rec := upgradeFixture(t, DeploymentDocker)
			opts.DockerHost = server.URL
			opts.run = func(context.Context, string, ...string) (string, error) { return "", nil }
			err := Upgrade(context.Background(), opts)
			if (err != nil) != fail {
				t.Fatalf("fail=%v: %v", fail, err)
			}
			all := strings.Join(calls, "\n")
			if !strings.Contains(all, "/old/stop?t=1200") || !strings.Contains(all, "name=zoomies-before-upgrade") {
				t.Fatalf("missing graceful replacement: %s", all)
			}
			if fail {
				if !strings.Contains(all, "/old/rename?name=zoomies") || !strings.Contains(all, "/old/start?") || !strings.Contains(all, "/new?force=1&v=0") {
					t.Fatalf("rollback missing: %s", all)
				}
				for _, call := range calls {
					if strings.HasPrefix(call, "DELETE ") && strings.Contains(call, "/containers/old?") {
						t.Fatal("removed rollback container")
					}
				}
			} else if !strings.Contains(all, "/old?v=0") {
				t.Fatalf("old container not cleaned up safely: %s", all)
			}
			stored, _ := ReadDeploymentRecord(opts.ConfigDir)
			if fail && stored.Image != rec.Image {
				t.Fatal("failed upgrade recorded as successful")
			}
		})
	}
}
