package machine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUsageMeasuresDeltasAndAvailableMemoryWithoutSleeping(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "proc"), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "proc", name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("meminfo", "MemTotal: 8388608 kB\nMemFree: 1024 kB\nMemAvailable: 4194304 kB\n")
	write("stat", "cpu 100 0 0 100 0 0 0 0 99 0\ncpu0 0\ncpu1 0\n")
	write("loadavg", "5.25 3.10 1.00 3/512 4242\n")
	s := UsageSampler{root: root}
	first := s.Sample(2, 8192)
	if first.CPUPercent != nil || first.MemoryAvailableMB == nil || *first.MemoryAvailableMB != 4096 {
		t.Fatalf("first sample must report available memory but no CPU delta: %+v", first)
	}
	// The load average needs no delta, but it does need the CPU count to be
	// the machine's: a figure of 5 means something different beside 2 cores
	// than beside 64, so it is reported only once procfs has been checked
	// against the caller's view, which the first sample establishes.
	if first.LoadAverage1 == nil || *first.LoadAverage1 != 5.25 {
		t.Fatalf("load average = %v, want 5.25 from the first field of /proc/loadavg", first.LoadAverage1)
	}
	write("stat", "cpu 125 0 0 175 0 0 0 0 124 0\ncpu0 0\ncpu1 0\n")
	u := s.Sample(2, 8192)
	if u.CPUPercent == nil || *u.CPUPercent != 25 {
		t.Fatalf("CPU = %v, want 25%% (guest already counted)", u.CPUPercent)
	}
	if u.LoadAverage1 == nil || *u.LoadAverage1 != 5.25 {
		t.Fatalf("load average = %v on the second sample, want 5.25", u.LoadAverage1)
	}
	// A reboot or CPU topology change starts a new baseline, not an enormous
	// negative utilisation that attracts every queued job.
	write("stat", "cpu 1 0 0 1 0 0 0 0\ncpu0 0\ncpu1 0\n")
	if s.Sample(2, 8192).CPUPercent != nil {
		t.Fatal("counter reset became a valid CPU sample")
	}
	if got := s.Sample(1, 2048); got.CPUPercent != nil || got.MemoryAvailableMB != nil || got.LoadAverage1 != nil {
		t.Fatalf("physical host figures were attributed to a smaller cgroup: %+v", got)
	}
}

// A load average is read from the same file every Linux kernel writes, and the
// two ways it can be wrong are the two ways any procfs figure can be: absent,
// or not a number. Neither may become a reading of zero, because zero load is
// the one value that would let an overwhelmed host look idle.
func TestLoadAverageRejectsWhatItCannotRead(t *testing.T) {
	for _, raw := range []string{"", "x y z", "-1 0 0 1/2 3", "NaN 0 0 1/2 3", "1.5"} {
		if _, ok := loadAverage(raw); ok {
			t.Errorf("accepted %q", raw)
		}
	}
	if v, ok := loadAverage("0.00 0.01 0.05 1/100 200"); !ok || v != 0 {
		t.Fatalf("a measured idle host was rejected: %v %v", v, ok)
	}
}

func TestMissingAndInvalidUsageNeverBecomeIdleReadings(t *testing.T) {
	s := UsageSampler{root: t.TempDir()}
	if u := s.Sample(2, 8192); u.CPUPercent != nil || u.MemoryAvailableMB != nil {
		t.Fatal("missing procfs became measured usage")
	}
	for _, raw := range []string{"", "cpu broken 1 2 3\ncpu0 0", "cpu 1 2 3 4"} {
		if _, _, _, ok := cpuTicks(raw); ok {
			t.Fatalf("accepted %q", raw)
		}
	}
	for _, raw := range []string{"MemTotal: 8192 kB\nMemFree: 4096 kB", "MemTotal: 8192 kB\nMemAvailable: 9000 kB", "MemTotal: 8192 kB\nMemAvailable: -1 kB"} {
		if _, _, ok := availableMemory(raw); ok {
			t.Fatalf("accepted %q", raw)
		}
	}
	if _, available, ok := availableMemory("MemTotal: 8192 kB\nMemAvailable: 0 kB"); !ok || available != 0 {
		t.Fatal("a measured full host was treated as unmeasured")
	}
}

// I/O wait is read from the same counters as CPU, and is only a figure beside a
// CPU delta: the two are shares of the same interval.
func TestUsageMeasuresIOWaitFromTheSameDeltasAsCPU(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "proc"), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "proc", name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("meminfo", "MemTotal: 8388608 kB\nMemFree: 1024 kB\nMemAvailable: 4194304 kB\n")
	// user nice system idle iowait: 100 ticks in all, none of them waiting.
	write("stat", "cpu 50 0 0 50 0 0 0 0 0 0\ncpu0 0\ncpu1 0\n")
	s := UsageSampler{root: root}
	if first := s.Sample(2, 8192); first.IOWaitPercent != nil {
		t.Fatalf("the first sample has no delta to take a share of: %v", *first.IOWaitPercent)
	}
	// 100 more ticks, 40 of them waiting on disk.
	write("stat", "cpu 80 0 0 80 40 0 0 0 0 0\ncpu0 0\ncpu1 0\n")
	u := s.Sample(2, 8192)
	if u.IOWaitPercent == nil || *u.IOWaitPercent != 40 {
		t.Fatalf("io wait = %v, want 40%% of the interval", u.IOWaitPercent)
	}
	// iowait is part of what CPUPercent calls busy: 100 - idle share (30/140).
	if u.CPUPercent == nil {
		t.Fatal("no CPU figure beside the I/O wait")
	}

	// A kernel whose cpu line stops before iowait reports nothing, not zero.
	write("stat", "cpu 90 0 0 90\ncpu0 0\ncpu1 0\n")
	if u := s.Sample(2, 8192); u.IOWaitPercent != nil {
		t.Fatalf("a line with no iowait counter reported %v", *u.IOWaitPercent)
	}

	// A counter that goes backwards is a different machine than last time.
	write("stat", "cpu 200 0 0 200 80 0 0 0 0 0\ncpu0 0\ncpu1 0\n")
	_ = s.Sample(2, 8192)
	write("stat", "cpu 300 0 0 300 10 0 0 0 0 0\ncpu0 0\ncpu1 0\n")
	if u := s.Sample(2, 8192); u.IOWaitPercent != nil {
		t.Fatalf("a counter that went backwards reported %v", *u.IOWaitPercent)
	}
}

// The memory valve looks at the host before it lends anything, and what it
// needs from the look is free memory and free swap: the first decides whether a
// loan can be made, the second whether swap can be offered instead.
func TestAHostsMemoryNowIsReadWithItsSwap(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "proc"), 0o700); err != nil {
		t.Fatal(err)
	}
	meminfo := "MemTotal: 8388608 kB\nMemFree: 1024 kB\nMemAvailable: 3145728 kB\nSwapTotal: 4194304 kB\nSwapFree: 2097152 kB\n"
	if err := os.WriteFile(filepath.Join(root, "proc", "meminfo"), []byte(meminfo), 0o600); err != nil {
		t.Fatal(err)
	}
	s := UsageSampler{root: root}
	got, ok := s.Memory(8192)
	if !ok || got != (Memory{TotalMB: 8192, AvailableMB: 3072, SwapTotalMB: 4096, SwapFreeMB: 2048}) {
		t.Fatalf("memory = %+v, %v; want 3072 MB available and 2048 of 4096 MB of swap free", got, ok)
	}

	// A host with no swap says so with zeroes, which is a measurement.
	if err := os.WriteFile(filepath.Join(root, "proc", "meminfo"), []byte("MemTotal: 8388608 kB\nMemAvailable: 3145728 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Memory(8192); !ok || got.SwapTotalMB != 0 || got.SwapFreeMB != 0 || got.AvailableMB != 3072 {
		t.Fatalf("memory = %+v, %v; want free memory read and no swap", got, ok)
	}
}

// The figures are a physical machine's. An agent in a smaller cgroup, or one
// talking to a remote daemon, must not have them attributed to the machine its
// runners share, or a loan would be made out of memory that is not theirs.
func TestAnotherMachinesMemoryIsNotTakenForTheHosts(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "proc"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "proc", "meminfo"), []byte("MemTotal: 8388608 kB\nMemAvailable: 3145728 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := UsageSampler{root: root}
	if got, ok := s.Memory(2048); ok {
		t.Fatalf("memory = %+v for a smaller machine, want nothing", got)
	}
	// And a host with no procfs at all is not guessed at.
	if _, ok := (&UsageSampler{root: t.TempDir()}).Memory(8192); ok {
		t.Fatal("a host with no /proc/meminfo reported memory")
	}
}
