package backend

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

func tmpfsSpec(cfg store.TmpfsConfig) Spec {
	s := jitSpec()
	s.Tmpfs = cfg
	return s
}

// The default for every existing pool: nothing is mounted over the runner's
// folders, and the request carries no Tmpfs key at all, so an upgrade changes no
// container the daemon is asked to make.
func TestARunnerWithNoTmpfsGetsNoTmpfsMounts(t *testing.T) {
	for _, fl := range []flavor{dockerFlavor(), podmanFlavor()} {
		cfg := buildRunnerConfig(jitSpec(), fl, containerOptions{Now: time.Now()})
		if cfg.HostConfig.Tmpfs != nil {
			t.Errorf("%s: tmpfs = %v, want none", fl.kind, cfg.HostConfig.Tmpfs)
		}
		body, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "Tmpfs") {
			t.Errorf("%s: the create request mentions Tmpfs: %s", fl.kind, body)
		}
	}
}

// Docker and Podman take the same map, and neither is given a uid: that number
// is read in the mount's user namespace, which rootless Podman shifts.
func TestTheWorkFolderIsMountedInMemoryOnDockerAndPodman(t *testing.T) {
	spec := tmpfsSpec(store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true, SizeMB: 2048}})
	for _, fl := range []flavor{dockerFlavor(), podmanFlavor()} {
		got := buildRunnerConfig(spec, fl, containerOptions{Now: time.Now()}).HostConfig.Tmpfs
		want := map[string]string{RunnerWorkMount: "size=2048m,rw,exec,nosuid,nodev,mode=1777"}
		if len(got) != 1 || got[RunnerWorkMount] != want[RunnerWorkMount] {
			t.Errorf("%s: tmpfs = %v, want %v", fl.kind, got, want)
		}
		if strings.Contains(got[RunnerWorkMount], "uid=") || strings.Contains(got[RunnerWorkMount], "noexec") {
			t.Errorf("%s: options %q must carry neither a uid nor noexec", fl.kind, got[RunnerWorkMount])
		}
	}
}

// Only the work folder is the default an operator opts into; /tmp is its own
// choice.
func TestTmpIsMountedOnlyWhenAskedFor(t *testing.T) {
	work := buildRunnerConfig(tmpfsSpec(store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true, SizeMB: 1024}}),
		dockerFlavor(), containerOptions{Now: time.Now()}).HostConfig.Tmpfs
	if _, ok := work[RunnerTmpMount]; ok {
		t.Errorf("tmpfs = %v, want /tmp left on disk", work)
	}

	both := buildRunnerConfig(tmpfsSpec(store.TmpfsConfig{
		Work: store.TmpfsMount{Enabled: true, SizeMB: 1024},
		Tmp:  store.TmpfsMount{Enabled: true, SizeMB: 512},
	}), dockerFlavor(), containerOptions{Now: time.Now()}).HostConfig.Tmpfs
	if both[RunnerTmpMount] != "size=512m,rw,exec,nosuid,nodev,mode=1777" || both[RunnerWorkMount] == "" {
		t.Errorf("tmpfs = %v, want both folders", both)
	}

	tmpOnly := buildRunnerConfig(tmpfsSpec(store.TmpfsConfig{Tmp: store.TmpfsMount{Enabled: true, SizeMB: 512}}),
		dockerFlavor(), containerOptions{Now: time.Now()}).HostConfig.Tmpfs
	if _, ok := tmpOnly[RunnerWorkMount]; ok || tmpOnly[RunnerTmpMount] == "" {
		t.Errorf("tmpfs = %v, want only /tmp", tmpOnly)
	}
}

// A mount with no size of its own is fitted to the runner's memory limit, so the
// default can never be a tmpfs bigger than the cgroup it is charged to.
func TestAnAutomaticTmpfsIsFittedToTheRunnersMemoryLimit(t *testing.T) {
	cases := []struct {
		name     string
		memoryMB int64
		want     string
	}{
		{"a small limit shrinks it to half", 4096, "size=2048m,rw,exec,nosuid,nodev,mode=1777"},
		{"a roomy limit takes the default", 32768, "size=4096m,rw,exec,nosuid,nodev,mode=1777"},
		{"no limit takes the default", 0, "size=4096m,rw,exec,nosuid,nodev,mode=1777"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := tmpfsSpec(store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true}})
			spec.Resources.MemoryMB = tc.memoryMB
			got := buildRunnerConfig(spec, dockerFlavor(), containerOptions{Now: time.Now()}).HostConfig.Tmpfs
			if got[RunnerWorkMount] != tc.want {
				t.Errorf("tmpfs = %q, want %q", got[RunnerWorkMount], tc.want)
			}
		})
	}
}

// With docker-in-docker the runner is given its half of the pair, and the tmpfs
// is charged to the runner's cgroup, so it is fitted to that half. Fitted to the
// whole it would promise room the runner does not have.
func TestADinDRunnersTmpfsIsFittedToItsHalfOfTheLimit(t *testing.T) {
	spec := tmpfsSpec(store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true}})
	spec.DockerMode = store.DockerDinD
	spec.ResourcesSource = store.AllocationFromHost
	spec.Resources = store.Resources{CPUs: 4, MemoryMB: 4096}
	cfg := buildRunnerConfig(spec, dockerFlavor(), containerOptions{Now: time.Now()})
	// The runner holds 2048 MB, so half of it is 1024.
	if got, want := cfg.HostConfig.Tmpfs[RunnerWorkMount], "size=1024m,rw,exec,nosuid,nodev,mode=1777"; got != want {
		t.Errorf("tmpfs = %q, want %q", got, want)
	}
}

// A host directory bound at the work folder wins: both at one path is an error
// the daemon raises only when the container is created.
func TestAWorkFolderBoundFromTheHostIsNotAlsoMountedInMemory(t *testing.T) {
	spec := tmpfsSpec(store.TmpfsConfig{
		Work: store.TmpfsMount{Enabled: true, SizeMB: 1024},
		Tmp:  store.TmpfsMount{Enabled: true, SizeMB: 512},
	})
	cfg := buildRunnerConfig(spec, dockerFlavor(), containerOptions{Now: time.Now(), WorkDirMount: "/srv/zoomies/runners/r1"})
	if _, ok := cfg.HostConfig.Tmpfs[RunnerWorkMount]; ok {
		t.Errorf("tmpfs = %v, want the bound work folder left alone", cfg.HostConfig.Tmpfs)
	}
	if cfg.HostConfig.Tmpfs[RunnerTmpMount] == "" {
		t.Errorf("tmpfs = %v, want /tmp still in memory", cfg.HostConfig.Tmpfs)
	}
}

// The setting travels to the agent inside the task, and an agent that predates
// it must still be able to read a task from a controller that has it.
func TestTheSpecCarriesTmpfsToTheAgentAndOmitsItWhenOff(t *testing.T) {
	off, err := json.Marshal(jitSpec())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(off), "tmpfs") {
		t.Errorf("a spec with nothing in memory still carries tmpfs: %s", off)
	}

	on := tmpfsSpec(store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true, SizeMB: 2048}})
	body, err := json.Marshal(on)
	if err != nil {
		t.Fatal(err)
	}
	var back Spec
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatal(err)
	}
	if back.Tmpfs != on.Tmpfs {
		t.Errorf("tmpfs = %+v after the round trip, want %+v", back.Tmpfs, on.Tmpfs)
	}
}

func dindTmpfsSpec(cfg store.TmpfsConfig) Spec {
	s := tmpfsSpec(cfg)
	s.DockerMode = store.DockerDinD
	return s
}

// The image store is on the sidecar, not the runner: it is where a dind job's
// pulls and builds are written, and it is a different container with its own
// memory limit.
func TestTheSidecarsImageStoreIsMountedInMemoryAndTheRunnersIsNot(t *testing.T) {
	spec := dindTmpfsSpec(store.TmpfsConfig{Daemon: store.TmpfsMount{Enabled: true, SizeMB: 6144}})
	spec.Resources = store.Resources{CPUs: 4, MemoryMB: 16384}
	side := buildDinDConfig(spec, dockerFlavor(), containerOptions{Now: time.Now(), DinDImage: DefaultDinDImage})
	if got, want := side.HostConfig.Tmpfs[DaemonStoreMount], "size=6144m,rw,exec,suid,dev,mode=0710"; got != want {
		t.Errorf("sidecar tmpfs = %q, want %q", got, want)
	}
	run := buildRunnerConfig(spec, dockerFlavor(), containerOptions{Now: time.Now()})
	if len(run.HostConfig.Tmpfs) != 0 {
		t.Errorf("runner tmpfs = %v, want none: the image store is the sidecar's", run.HostConfig.Tmpfs)
	}
}

// Off is off: the sidecar's create carries no Tmpfs key at all, so an upgrade
// changes no container the daemon is asked to make.
func TestASidecarWithNoImageStoreInMemoryGetsNoTmpfs(t *testing.T) {
	spec := dindTmpfsSpec(store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true}})
	side := buildDinDConfig(spec, dockerFlavor(), containerOptions{Now: time.Now(), DinDImage: DefaultDinDImage})
	if side.HostConfig.Tmpfs != nil {
		t.Errorf("sidecar tmpfs = %v, want none", side.HostConfig.Tmpfs)
	}
	body, err := json.Marshal(side)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "Tmpfs") {
		t.Errorf("the sidecar's create request mentions Tmpfs: %s", body)
	}
}

// The store is fitted to the daemon's own limit. For a pool sized by its host
// that is the daemon's half of one slot's share; for a typed limit it is the
// whole of it, because a typed limit is given to both containers.
func TestTheImageStoreIsFittedToTheDaemonsOwnHalf(t *testing.T) {
	auto := dindTmpfsSpec(store.TmpfsConfig{Daemon: store.TmpfsMount{Enabled: true}})
	auto.ResourcesSource = store.AllocationFromHost
	auto.Resources = store.Resources{CPUs: 4, MemoryMB: 6001}
	side := buildDinDConfig(auto, dockerFlavor(), containerOptions{Now: time.Now(), DinDImage: DefaultDinDImage})
	// The daemon takes the remainder of an odd split: 6001 - 3000 = 3001, and the
	// store is half of what the daemon has, 1500.
	if got, want := side.HostConfig.Tmpfs[DaemonStoreMount], "size=1500m,rw,exec,suid,dev,mode=0710"; got != want {
		t.Errorf("automatic pool: sidecar tmpfs = %q, want %q", got, want)
	}

	typed := dindTmpfsSpec(store.TmpfsConfig{Daemon: store.TmpfsMount{Enabled: true}})
	typed.Resources = store.Resources{CPUs: 4, MemoryMB: 32768}
	side = buildDinDConfig(typed, dockerFlavor(), containerOptions{Now: time.Now(), DinDImage: DefaultDinDImage})
	if got, want := side.HostConfig.Tmpfs[DaemonStoreMount], "size=8192m,rw,exec,suid,dev,mode=0710"; got != want {
		t.Errorf("typed pool: sidecar tmpfs = %q, want %q", got, want)
	}
}

// The host's ceiling is applied after the folder is fitted to the runner's
// limit, so it lowers a size however the size was arrived at -- typed, or fitted
// -- and cannot raise one.
func TestAHostCeilingLowersEveryInMemoryFolderAndNeverRaisesOne(t *testing.T) {
	spec := dindTmpfsSpec(store.TmpfsConfig{
		Work:   store.TmpfsMount{Enabled: true},
		Tmp:    store.TmpfsMount{Enabled: true, SizeMB: 512},
		Daemon: store.TmpfsMount{Enabled: true, SizeMB: 6000},
	})
	spec.Resources = store.Resources{CPUs: 4, MemoryMB: 32768}
	spec.TmpfsMaxMB = 2048

	run := buildRunnerConfig(spec, dockerFlavor(), containerOptions{Now: time.Now()})
	// The fitted default is 4096 and is lowered; the typed 512 is under the ceiling.
	if got, want := run.HostConfig.Tmpfs[RunnerWorkMount], "size=2048m,rw,exec,nosuid,nodev,mode=1777"; got != want {
		t.Errorf("work = %q, want %q", got, want)
	}
	if got, want := run.HostConfig.Tmpfs[RunnerTmpMount], "size=512m,rw,exec,nosuid,nodev,mode=1777"; got != want {
		t.Errorf("tmp = %q, want %q", got, want)
	}
	side := buildDinDConfig(spec, dockerFlavor(), containerOptions{Now: time.Now(), DinDImage: DefaultDinDImage})
	if got, want := side.HostConfig.Tmpfs[DaemonStoreMount], "size=2048m,rw,exec,suid,dev,mode=0710"; got != want {
		t.Errorf("image store = %q, want %q", got, want)
	}

	// With no ceiling nothing is lowered.
	spec.TmpfsMaxMB = 0
	run = buildRunnerConfig(spec, dockerFlavor(), containerOptions{Now: time.Now()})
	if got, want := run.HostConfig.Tmpfs[RunnerWorkMount], "size=4096m,rw,exec,nosuid,nodev,mode=1777"; got != want {
		t.Errorf("without a ceiling, work = %q, want %q", got, want)
	}
}

// The ceiling travels to the agent in the task, and a spec with none carries no
// key, so an agent that predates it reads the same document as before.
func TestTheHostCeilingTravelsInTheSpecAndIsOmittedWhenNone(t *testing.T) {
	plain, err := json.Marshal(tmpfsSpec(store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "tmpfs_max_mb") {
		t.Errorf("a spec with no ceiling carries one: %s", plain)
	}
	capped := tmpfsSpec(store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true}})
	capped.TmpfsMaxMB = 1024
	body, err := json.Marshal(capped)
	if err != nil {
		t.Fatal(err)
	}
	var back Spec
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatal(err)
	}
	if back.TmpfsMaxMB != 1024 {
		t.Errorf("ceiling = %d after the round trip, want 1024", back.TmpfsMaxMB)
	}
}

// A pool that gives its daemon most of a host-sized slot gets it in the real
// limits, and a typed limit is untouched by the share.
func TestPairLimitsFollowThePoolsDaemonShare(t *testing.T) {
	spec := Spec{Resources: store.Resources{CPUs: 10, MemoryMB: 10000}, ResourcesSource: store.AllocationFromHost, DaemonSharePercent: 75}
	r, d := pairLimits(spec)
	if d.MemoryMB != 7500 || r.MemoryMB != 2500 {
		t.Fatalf("host-sized pair = runner %d MB, daemon %d MB; want 2500 and 7500", r.MemoryMB, d.MemoryMB)
	}
	spec.ResourcesSource = ""
	r, d = pairLimits(spec)
	if r.MemoryMB != 10000 || d.MemoryMB != 10000 {
		t.Fatalf("typed pair = %d / %d MB; a typed limit goes to both in full", r.MemoryMB, d.MemoryMB)
	}
}

// Docker and Podman start every tmpfs from noexec,nosuid,nodev and merge the
// options they are given over that, so a mount that merely omits noexec is
// still noexec. Pools that turned the folders on found every `go test` dying
// with "permission denied" on a binary it had just built in /tmp. Each option
// that has to override a default is asserted by name.
func TestInMemoryFoldersOverrideTheNoexecDefaultOfATmpfs(t *testing.T) {
	has := func(opts, want string) bool {
		for _, o := range strings.Split(opts, ",") {
			if o == want {
				return true
			}
		}
		return false
	}
	for _, opts := range []string{tmpfsOptions, daemonTmpfsOptions} {
		if !has(opts, "exec") || has(opts, "noexec") {
			t.Errorf("options %q must say exec, or the runtime mounts the folder noexec", opts)
		}
	}
	for _, want := range []string{"suid", "dev"} {
		if !has(daemonTmpfsOptions, want) {
			t.Errorf("the image store's options %q must say %s: the nested daemon keeps setuid files and device nodes in image layers", daemonTmpfsOptions, want)
		}
	}
	if !has(tmpfsOptions, "nosuid") || !has(tmpfsOptions, "nodev") {
		t.Errorf("the runner's folders keep the runtime's nosuid and nodev: %q", tmpfsOptions)
	}
}

// Auto is decided where the runner's real limit is known: on a runner too small
// the folder is simply not mounted, which is what makes it safe to leave on for
// a pool whose hosts differ.
func TestAnAutomaticFolderIsNotMountedOnARunnerTooSmallForIt(t *testing.T) {
	spec := Spec{Tmpfs: store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true, Auto: true}, Tmp: store.TmpfsMount{Enabled: true, Auto: true}}}
	if got := tmpfsMounts(spec, 2048, false); got != nil {
		t.Errorf("a 2 GB runner was given %v, want disk", got)
	}
	got := tmpfsMounts(spec, 32768, false)
	if got[RunnerWorkMount] != "size=4096m,rw,exec,nosuid,nodev,mode=1777" || got[RunnerTmpMount] != "size=1024m,rw,exec,nosuid,nodev,mode=1777" {
		t.Errorf("a 32 GB runner was given %v", got)
	}
	// The host's own standard replaces the default an untyped folder is asked for.
	spec.TmpfsHost = store.HostTmpfs{WorkMB: 8192}
	if got := tmpfsMounts(spec, 65536, false); got[RunnerWorkMount] != "size=8192m,rw,exec,nosuid,nodev,mode=1777" {
		t.Errorf("a host standard of 8 GB gave %v", got)
	}
	// An older controller sends only the ceiling, and it still applies.
	old := Spec{Tmpfs: spec.Tmpfs, TmpfsMaxMB: 2560}
	if got := tmpfsMounts(old, 65536, false); got[RunnerWorkMount] != "size=2560m,rw,exec,nosuid,nodev,mode=1777" {
		t.Errorf("a ceiling from an older controller gave %v", got)
	}
}
