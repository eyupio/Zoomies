package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestProcessBirthIdentityRejectsReusedPID(t *testing.T) {
	id, err := processIdentity(os.Getpid())
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("process birth identity needs matching PID namespace and proc mount")
		}
		t.Fatal(err)
	}

	if id == "" {
		t.Fatal("empty process identity")
	}
	meta := processMeta{PID: os.Getpid(), ProcessIdentity: id}
	if err := verifyProcessIdentity(meta, meta.PID); err != nil {
		t.Fatal(err)
	}
	meta.ProcessIdentity = "old-process"
	if err := verifyProcessIdentity(meta, meta.PID); err == nil {
		t.Fatal("reused PID trusted")
	}
}

// staleRunnerDir lays down a runner directory whose recorded PID is now held by
// the test process, as it would be after a reboot handed the old PID to an
// unrelated daemon of the same user.
func staleRunnerDir(t *testing.T, b *ProcessBackend, name, identity string) string {
	t.Helper()
	dir := b.runnerDir(name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	meta := processMeta{Name: name, RunnerID: "run_" + name, PID: os.Getpid(), ProcessIdentity: identity, StartedAt: time.Now()}
	if err := writeMeta(dir, meta); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, runnerPIDFile), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A PID whose birth identity differs from the recorded one is proof the runner
// is gone, not doubt about it. Treating it as doubt made List fail for the whole
// backend, so one stale directory hid every other process runner from the
// reconciler and could not itself be removed.
func TestAReusedPIDReadsAsAGoneRunnerRatherThanFreezingTheBackend(t *testing.T) {
	requireUnix(t)
	if _, err := processIdentity(os.Getpid()); err != nil {
		t.Skipf("process birth identity is unavailable here: %v", err)
	}
	b, _ := newStubProcessBackend(t)
	ctx := context.Background()
	dir := staleRunnerDir(t, b, "stale", "old-boot:old-start")

	st, err := b.Status(ctx, Handle(dir))
	if err != nil {
		t.Fatalf("status of a reused PID must not be an error: %v", err)
	}
	if st.Phase != PhaseExitUnknown {
		t.Fatalf("phase = %q, want exit_unknown", st.Phase)
	}

	ws, err := b.List(ctx)
	if err != nil {
		t.Fatalf("one stale directory must not fail the listing: %v", err)
	}
	if len(ws) != 1 || ws[0].Status.Phase != PhaseExitUnknown {
		t.Fatalf("list = %+v, want the stale runner reported as exit_unknown", ws)
	}

	// Reaching the next line at all shows nothing signalled the test process.
	if err := b.Stop(ctx, Handle(dir), time.Second); err != nil {
		t.Fatalf("there is nothing of ours to stop: %v", err)
	}
	if err := b.Remove(ctx, Handle(dir)); err != nil {
		t.Fatalf("a stale directory must be removable through Zoomies: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the directory survived Remove: %v", err)
	}
}

// Only a proven mismatch reads as gone. A runner recorded without an identity
// might still be ours, so it keeps failing closed rather than being adopted or
// signalled on the strength of a PID alone.
func TestARunnerWithNoRecordedIdentityStillFailsClosed(t *testing.T) {
	requireUnix(t)
	b, _ := newStubProcessBackend(t)
	dir := staleRunnerDir(t, b, "legacy", "")

	if _, err := b.Status(context.Background(), Handle(dir)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("status err = %v, want ErrUnavailable", err)
	}
	if err := b.Stop(context.Background(), Handle(dir), time.Second); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stop err = %v, want ErrUnavailable", err)
	}
}
