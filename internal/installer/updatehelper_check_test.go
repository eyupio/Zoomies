//go:build helpercheck && unix

package installer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// This is the one step of test/upgrade/helper-check.sh that is Go: running the
// update helper once, as `zoomies updates helper run` does after its two root
// gates. Everything around it is real and is laid out by the script: root's
// pointer, the installed binary it names, the deployment, the request, a
// systemctl on PATH that records what it is asked, and the release the binary
// downloads. The engine is the installed binary's own `zoomies upgrade`, run as
// the helper's child process.
//
// The gates are what a check run without root cannot pass: the command refuses
// any uid but 0, root's state is at a fixed path under /var/lib, and every
// folder above the binary up to / must be root's. So this stands where the
// command would, with the seams the helper's own tests use: the walk above the
// binary stops at the script's folder rather than at /, and the update folder
// and the request are taken as the service account's (the pointer records the
// same uid), because a test cannot chown its files to another account. Every
// other check on the pointer and the binary is made on the files' real owner.
//
// It has a build tag of its own so that `go test ./...` never runs it: it needs
// the world the script makes, and answers nothing without it.
func TestTheHelperAnswersTheRequestTheCheckLaidOut(t *testing.T) {
	stateDir, top, releases := os.Getenv("HELPER_CHECK_STATE_DIR"), os.Getenv("HELPER_CHECK_TOP"), os.Getenv("HELPER_CHECK_RELEASES")
	if stateDir == "" || top == "" || releases == "" {
		t.Fatal("HELPER_CHECK_STATE_DIR, HELPER_CHECK_TOP and HELPER_CHECK_RELEASES are not set; run test/upgrade/helper-check.sh, which lays out what they name")
	}
	// The release host, on a port of its own, so two checks at once never meet.
	// The helper hands the engine its own environment, which is where root's
	// ZOOMIES_BASE_URL comes from on a real host.
	srv := httptest.NewServer(http.FileServer(http.Dir(releases)))
	defer srv.Close()
	t.Setenv("ZOOMIES_BASE_URL", srv.URL)

	opts, err := helperOptionsFromPointer(stateDir, fileOwner, top)
	if err != nil {
		t.Fatalf("the helper refused root's pointer: %v", err)
	}
	opts.ownerOf, opts.Out = ownedByService, os.Stdout
	if err := RunUpdateHelper(context.Background(), opts); err != nil {
		t.Fatalf("the helper could not answer: %v", err)
	}
}
