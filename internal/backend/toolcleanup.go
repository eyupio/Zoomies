package backend

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const roleToolCleanup = "toolcache-cleanup"

// This mount contains only one stopped runner's disposable cache. In
// particular, the symlinks into its pool's kept cache must be unlinked, never
// followed. Include dotfiles, but not . or .., and never remove the mountpoint.
const toolCleanupScript = `rm -rf -- /cleanup/* /cleanup/.[!.]* /cleanup/..?*`

// removeRunnerToolFarm is called after Stop, while the runner still retains
// its labels for retries. A setup action can create uid-1001 (or root) 0755
// directories below our 0777 farm. An unprivileged agent cannot unlink their
// contents, and chmod on the farm cannot fix that. Ask the existing daemon to
// remove only those contents in the runner's already-local immutable image.
// removeAll is injectable so tests exercise permission failures even as root.
func (b *DockerBackend) removeRunnerToolFarm(ctx context.Context, runner ContainerInspect, removeAll func(string) error) error {
	farm := runner.Config.Labels[LabelToolFarm]
	err := removeAll(farm)
	if err == nil || !errors.Is(err, os.ErrPermission) {
		return err
	}
	if validationErr := b.validateToolCleanup(runner); validationErr != nil {
		return errors.Join(err, validationErr)
	}
	b.log.Info("recovering runner tool cache permissions", "runner", runner.Config.Labels[LabelName], "path", farm)
	if cleanupErr := b.cleanToolFarmInContainer(ctx, runner); cleanupErr != nil {
		return errors.Join(err, cleanupErr)
	}
	// The helper cannot unlink its own mountpoint. Verify the actual filesystem
	// result and remove the now-empty directory as the agent before losing the
	// runner's labels. A successful helper exit alone is not proof of cleanup.
	return removeAll(farm)
}

func (b *DockerBackend) validateToolCleanup(runner ContainerInspect) error {
	labels := runner.Config.Labels
	name, farm := labels[LabelName], labels[LabelToolFarm]
	if labels[LabelManaged] != "true" || labels[LabelRole] != roleRunner || name == "" || runner.ID == "" || runner.Image == "" {
		return errors.New("refusing tool cache recovery without a managed runner identity and immutable image")
	}
	if b.sharedDir == "" || !filepath.IsAbs(farm) || filepath.Clean(farm) != toolFarmDir(b.sharedDir, name) || strings.ContainsAny(farm, ":\n\r") {
		return errors.New("refusing tool cache recovery outside this runner's disposable cache")
	}
	// The shared root may itself be an operator-configured symlink, but none
	// of the cache components below it may redirect the daemon's bind mount.
	for _, path := range []string{filepath.Join(b.sharedDir, "cache"), filepath.Join(b.sharedDir, "cache", "tool-runs"), farm} {
		fi, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("checking tool cache recovery path: %w", err)
		}
		if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing tool cache recovery through a non-directory or symlink: %s", path)
		}
	}
	return nil
}

func toolCleanupName(runner ContainerInspect) string {
	sum := sha256.Sum256([]byte(runner.ID + "\x00" + runner.Config.Labels[LabelToolFarm]))
	return fmt.Sprintf("zoomies-toolcleanup-%x", sum[:12])
}

func (b *DockerBackend) cleanToolFarmInContainer(ctx context.Context, runner ContainerInspect) (resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	farm := runner.Config.Labels[LabelToolFarm]
	name := toolCleanupName(runner)
	// A cancelled create can have reached the daemon despite losing its reply.
	// Recover that helper on the next attempt, without touching a foreign name.
	old, err := b.api.ContainerInspect(ctx, name)
	if err == nil {
		if old.Config == nil || old.Config.Labels[LabelRole] != roleToolCleanup || old.Config.Labels[LabelToolFarm] != farm {
			return errors.New("refusing to replace an unrelated tool cache cleanup container")
		}
		if err := b.removeContainer(ctx, old.ID); err != nil && !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("removing previous tool cache cleanup container: %w", err)
		}
	} else if !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("checking previous tool cache cleanup container: %w", err)
	}

	pids := int64(32)
	cfg := ContainerCreateRequest{
		Image:       runner.Image,
		User:        "0:0",
		Entrypoint:  []string{"/bin/sh", "-c", toolCleanupScript},
		WorkingDir:  "/",
		Healthcheck: &HealthConfig{Test: []string{"NONE"}},
		Labels:      map[string]string{LabelRole: roleToolCleanup, LabelToolFarm: farm},
		HostConfig: &HostConfig{
			Binds:          []string{farm + ":/cleanup" + b.fl.mountSuffix},
			NetworkMode:    "none",
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			CapAdd:         []string{"DAC_OVERRIDE", "FOWNER"},
			SecurityOpt:    []string{"no-new-privileges"},
			AutoRemove:     true,
			RestartPolicy:  RestartPolicy{Name: "no"},
			NanoCPUs:       nanoCPUs(1),
			Memory:         128 * 1024 * 1024,
			MemorySwap:     128 * 1024 * 1024,
			PidsLimit:      &pids,
			LogConfig:      runnerLogConfig(b.fl),
		},
	}
	id, err := b.api.ContainerCreate(ctx, name, cfg)
	if err != nil {
		return fmt.Errorf("creating tool cache cleanup container: %w", err)
	}
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer stop()
		// The helper is AutoRemove, so the daemon starts removing it the
		// moment it exits; a DELETE that meets that removal waits it out
		// rather than failing the runner's cleanup over a container that is
		// already on its way.
		if err := b.removeContainer(cleanupCtx, id); err != nil && !errors.Is(err, ErrNotFound) {
			resultErr = errors.Join(resultErr, fmt.Errorf("removing tool cache cleanup container: %w", err))
		}
	}()
	if err := b.api.ContainerStart(ctx, id); err != nil {
		return fmt.Errorf("starting tool cache cleanup container: %w", err)
	}
	code, err := b.api.ContainerWait(ctx, id)
	if errors.Is(err, ErrNotFound) {
		// AutoRemove can win the race with Wait. The caller still verifies that
		// the cache was emptied; a missing helper cannot hide a cleanup failure.
		return nil
	}
	if err != nil {
		return fmt.Errorf("waiting for tool cache cleanup container: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("tool cache cleanup container exited with status %d", code)
	}
	return nil
}
