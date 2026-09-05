package backend

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

func TestPodmanKindAndDefaults(t *testing.T) {
	b, err := NewPodman(DockerOptions{Logger: quietLogger()})
	if err != nil {
		t.Fatalf("NewPodman: %v", err)
	}
	if b.Kind() != store.BackendPodman {
		t.Fatalf("kind = %q", b.Kind())
	}
	// Autodetection must land on a Podman socket, not Docker's.
	if !strings.Contains(b.api.Endpoint(), "podman") {
		t.Fatalf("endpoint = %q, want a podman socket", b.api.Endpoint())
	}
}

func TestPodmanRefusesDinD(t *testing.T) {
	b, err := NewPodman(DockerOptions{Host: "unix:///run/podman/podman.sock", Logger: quietLogger()})
	if err != nil {
		t.Fatalf("NewPodman: %v", err)
	}
	spec := jitSpec()
	spec.DockerMode = store.DockerDinD

	_, err = b.Create(context.Background(), spec)
	if err == nil {
		t.Fatal("podman accepted docker-in-docker")
	}
	// The operator needs both alternatives, not just a refusal.
	if !strings.Contains(err.Error(), "docker backend") || !strings.Contains(err.Error(), "nested") {
		t.Fatalf("unhelpful message: %v", err)
	}
}

func TestPodmanFlavorDiffersFromDocker(t *testing.T) {
	p, d := podmanFlavor(), dockerFlavor()
	if p.supportsDinD {
		t.Fatal("podman must not claim docker-in-docker support")
	}
	if d.mountSuffix != "" {
		t.Fatalf("docker needs no mount suffix, got %q", d.mountSuffix)
	}
	if p.mountSuffix != ":z" {
		t.Fatalf("podman mount suffix = %q, want :z for SELinux relabelling", p.mountSuffix)
	}

	spec := jitSpec()
	spec.DockerMode = store.DockerHostSocket
	cfg := buildRunnerConfig(spec, p, containerOptions{
		Now: time.Now(), HostSocket: "/run/user/1000/podman/podman.sock",
	})
	want := "/run/user/1000/podman/podman.sock:/var/run/docker.sock"
	if !slices.Contains(cfg.HostConfig.Binds, want) {
		t.Fatalf("binds = %v, want %q", cfg.HostConfig.Binds, want)
	}
	// The rest of the container contract is shared with Docker on purpose.
	if cfg.User != "runner" || !slices.Contains(cfg.HostConfig.CapDrop, "ALL") {
		t.Fatalf("podman lost the shared security defaults: %+v", cfg.HostConfig)
	}
}

func TestPodmanProbe(t *testing.T) {
	t.Run("unavailable explains the socket unit", func(t *testing.T) {
		b, err := NewPodman(DockerOptions{Host: "unix:///nonexistent/zoomies/podman.sock", Logger: quietLogger()})
		if err != nil {
			t.Fatalf("NewPodman: %v", err)
		}
		info := b.Probe(context.Background())
		if info.Available {
			t.Fatal("a missing socket must not report as available")
		}
		if info.Kind != store.BackendPodman {
			t.Fatalf("kind = %q", info.Kind)
		}
		if !strings.Contains(info.Detail, "podman.socket") {
			t.Fatalf("detail must mention the socket unit: %q", info.Detail)
		}
	})

	t.Run("available never claims dind", func(t *testing.T) {
		f := newFakeEngine(t, map[string]http.HandlerFunc{
			"GET " + v + "/_ping":   func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("OK")) },
			"GET " + v + "/version": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, VersionInfo{Version: "5.2.0"}) },
			"GET " + v + "/info": func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, 200, SystemInfo{ServerVersion: "5.2.0", Rootless: true})
			},
		})
		b, err := NewPodman(DockerOptions{Host: "tcp://" + f.Listener.Addr().String(), Logger: quietLogger()})
		if err != nil {
			t.Fatalf("NewPodman: %v", err)
		}
		info := b.Probe(context.Background())
		if !info.Available || info.Version != "5.2.0" || !info.Rootless {
			t.Fatalf("info = %+v", info)
		}
		if info.SupportsDinD {
			t.Fatal("podman must never report docker-in-docker support")
		}
	})
}
