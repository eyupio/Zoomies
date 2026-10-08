//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// openSetupLock opens the lock file as setup does. Every open is an open file
// description of its own, which is what flock arbitrates between, so a second
// open in this process meets the refusal that a second process would.
func openSetupLock(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// Setup and upgrade are separate commands that both replace a gateway's binary,
// and this lock is all that stops one interleaving with the other.
func TestASecondSetupLockIsRefusedWhileTheFirstIsHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.lock")
	release, err := tryLockSetup(openSetupLock(t, path))
	if err != nil {
		t.Fatalf("tryLockSetup: %v", err)
	}

	second := openSetupLock(t, path)
	if _, err := tryLockSetup(second); err == nil {
		t.Fatal("a second lock on the same file was granted while the first was held")
	}

	release()
	// The same descriptor is let in once the first lock is released, which is
	// what shows the refusal above came from the first holder and not from a
	// descriptor that could never have been locked.
	again, err := tryLockSetup(second)
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	again()
}

// A setup that is killed never calls its release. The kernel closes its files
// for it, which closing the holder's file stands in for here, and that has to
// be enough or an interrupted setup would block every retry.
func TestTheSetupLockIsReleasedWhenItsFileCloses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.lock")
	holder := openSetupLock(t, path)
	if _, err := tryLockSetup(holder); err != nil {
		t.Fatalf("tryLockSetup: %v", err)
	}
	holder.Close()

	again, err := tryLockSetup(openSetupLock(t, path))
	if err != nil {
		t.Fatalf("closing the file left the lock held: %v", err)
	}
	again()
}
