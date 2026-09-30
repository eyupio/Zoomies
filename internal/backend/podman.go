package backend

// The Podman backend.
//
// Podman's API socket speaks the Docker Engine API, so the whole of docker.go
// applies unchanged and this file is the list of differences:
//
//   - the socket is found in a different place (DetectPodmanHost),
//   - rootless is the normal configuration rather than the hardened one, so an
//     unavailable-because-root Podman is worth commenting on rather than
//     celebrating,
//   - docker-in-docker is not supported: a privileged nested dockerd inside a
//     rootless Podman container does not work, and telling an operator that up
//     front is kinder than letting the sidecar crash-loop,
//   - bind mounts get an SELinux relabel suffix, because Podman's usual home is
//     a Fedora or RHEL host with SELinux enforcing, where an unlabelled mount
//     is silently unreadable inside the container.
//
// It embeds *DockerBackend rather than copying it, so a fix to the container
// lifecycle lands in both backends at once.

import (
	"context"
	"fmt"

	"github.com/eyupio/zoomies/internal/store"
)

// PodmanBackend runs runners as containers on a Podman service.
type PodmanBackend struct {
	*DockerBackend
}

func podmanFlavor() flavor {
	f := dockerFlavor()
	f.kind = store.BackendPodman
	f.displayName = "Podman"
	f.supportsDinD = false
	// :z relabels the host path so an SELinux-enforcing host lets the container
	// read it. It is shared rather than private (:Z) because the cache is
	// mounted into one runner after another. It applies to the directories
	// Zoomies creates -- the work and cache binds -- and never to the host
	// socket, which is the system's file to label.
	f.mountSuffix = ":z"
	// The usual cause of an unreachable Podman socket is that podman.socket has
	// never been enabled -- rootless Podman does not start it on install the
	// way Docker's own daemon starts itself.
	f.startHint = "systemctl --user enable --now podman.socket"
	return f
}

var _ Backend = (*PodmanBackend)(nil)

// NewPodman builds a Podman backend. An empty Host autodetects, preferring the
// per-user socket at $XDG_RUNTIME_DIR/podman/podman.sock.
//
// As with NewDocker, this does not contact the service: Probe reports on it.
func NewPodman(opts DockerOptions) (*PodmanBackend, error) {
	d, err := newContainerBackend(opts, podmanFlavor(), DetectPodmanHost, "unix:///run/podman/podman.sock")
	if err != nil {
		return nil, err
	}
	return &PodmanBackend{DockerBackend: d}, nil
}

// Kind identifies the implementation.
func (b *PodmanBackend) Kind() store.BackendKind { return store.BackendPodman }

// Probe reports on the Podman service. The Docker probe already names Podman
// and its own start command via the flavor's displayName and startHint, so
// there is nothing to add here beyond correcting Kind and SupportsDinD.
func (b *PodmanBackend) Probe(ctx context.Context) Info {
	info := b.DockerBackend.Probe(ctx)
	info.Kind = store.BackendPodman
	info.SupportsDinD = false
	return info
}

// Create goes through CreateWithResult, so both entry points give the same
// answer.
func (b *PodmanBackend) Create(ctx context.Context, spec Spec) (Handle, error) {
	r, err := b.CreateWithResult(ctx, spec)
	return r.Handle, err
}

// CreateWithResult refuses docker-in-docker and otherwise defers to the Docker
// path. The refusal lives here, not only in Create, because the agent calls this
// one for any backend that reports timings: the terse check further down would
// otherwise be the only text an operator ever saw, with no remedy in it.
func (b *PodmanBackend) CreateWithResult(ctx context.Context, spec Spec) (CreateResult, error) {
	if spec.DockerMode == store.DockerDinD {
		return CreateResult{}, fmt.Errorf("backend: pool %q asks for docker-in-docker, which the podman backend does not support; either move the pool to the docker backend, or drop docker_mode to none and let jobs use podman's own nested container support inside the runner image", spec.PoolName)
	}
	return b.DockerBackend.CreateWithResult(ctx, spec)
}
