package installer

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"github.com/eyupio/zoomies/internal/updates/channel"
)

// A session leader with no terminal that opens a terminal without O_NOCTTY
// takes it as its controlling terminal, and the helper, started by systemd, is
// exactly such a leader. A terminal handed to it as a request would then be
// able to signal root's helper and read from it. The service cannot make a
// device node, so the case is a terminal swapped in between the look and the
// open. The test needs a session of its own, to tell whether it acquired a
// terminal, and a mount namespace of its own, because a pseudo-terminal opens
// only through its own devpts and so is bind-mounted over the request; it runs
// itself again as a child with both, which takes root.
func TestTheHelperNeverTakesATerminalItIsHandedAsARequest(t *testing.T) {
	if os.Getenv("ZOOMIES_TEST_TERMINAL_CHILD") == "1" {
		terminalChild(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestTheHelperNeverTakesATerminalItIsHandedAsARequest$", "-test.v")
	cmd.Env = append(os.Environ(), "ZOOMIES_TEST_TERMINAL_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Cloneflags: syscall.CLONE_NEWNS}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Skipf("cannot start a child in a mount namespace of its own (it takes root): %v", err)
	}
	if strings.Contains(string(out), "--- SKIP") {
		t.Skipf("the child could not set the case up: %s", lastLine(string(out)))
	}
	if err != nil {
		t.Fatalf("the child failed: %v\n%s", err, out)
	}
}

// terminalChild runs in a fresh session with no controlling terminal, in a
// mount namespace of its own.
func terminalChild(t *testing.T) {
	// Private, so the bind mount below never reaches the host's namespace.
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		t.Skipf("cannot make the child's mounts private: %v", err)
	}
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminals here: %v", err)
	}
	defer master.Close()
	var unlock int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		t.Skipf("cannot unlock the pseudo-terminal: %v", errno)
	}
	var n uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); errno != 0 {
		t.Skipf("cannot name the pseudo-terminal: %v", errno)
	}
	pts := "/dev/pts/" + strconv.Itoa(int(n))

	dir, root := updateRoot(t)
	path := filepath.Join(dir, channel.RequestFile)
	plant(t, path, wellFormedRequest)
	was := betweenLookAndOpen
	betweenLookAndOpen = func(string) {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		plant(t, path, "")
		if err := syscall.Mount(pts, path, "", syscall.MS_BIND, ""); err != nil {
			t.Skipf("cannot bind-mount the pseudo-terminal over the request: %v", err)
		}
		t.Cleanup(func() { _ = syscall.Unmount(path, syscall.MNT_DETACH) })
	}
	defer func() { betweenLookAndOpen = was }()

	// The refusal has to come after the open, or the terminal was never opened
	// and the test proves nothing.
	if _, err := readRequest(root, testServiceUID, ownedByService); err == nil || !strings.Contains(err.Error(), "changed while it was being opened") {
		t.Fatalf("a terminal swapped in for the request should be opened and then refused, got: %v", err)
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err == nil {
		tty.Close()
		t.Fatal("reading the request made the terminal handed to it this session's controlling terminal")
	}
	if !errors.Is(err, syscall.ENXIO) {
		t.Fatalf("cannot tell whether a controlling terminal was taken: %v", err)
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
