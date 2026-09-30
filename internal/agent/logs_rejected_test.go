package agent

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/backend"
)

// rejectingTransport answers every log stream the way a controller does that
// has already forgotten it: the first write fails.
type rejectingTransport struct {
	*fakeTransport
}

func (rejectingTransport) OpenLogStream(context.Context, string) (io.WriteCloser, error) {
	return rejectedStream{}, nil
}

type rejectedStream struct{}

func (rejectedStream) Write([]byte) (int, error) { return 0, errors.New("stream unknown") }
func (rejectedStream) Close() error              { return nil }

// A viewer who closes the tab before the agent acts leaves a stream the
// controller no longer knows, and a cancel_logs that found nothing to cancel.
// Nothing else ends that stream, so without this the relay follows a job's
// output for hours and throws every byte away.
func TestLogRelayStopsFollowingOnceTheControllerRejectsTheStream(t *testing.T) {
	be := newFakeBackend("docker")
	pr, pw := io.Pipe()
	be.mu.Lock()
	be.logs = func() io.ReadCloser { return pr }
	be.mu.Unlock()

	relay := newLogRelay(rejectingTransport{newFakeTransport()}, testLogger())
	if err := relay.start(context.Background(), "stream-1", "wl-1", be, backend.LogOptions{Follow: true}); err != nil {
		t.Fatalf("start: %v", err)
	}
	// The runner's next line is the first the relay learns the stream is dead.
	go pw.Write([]byte("a line nobody is reading\n"))

	waitFn(t, "the abandoned stream to be released without a cancel", 5*time.Second, func() bool {
		relay.mu.Lock()
		defer relay.mu.Unlock()
		return len(relay.streams) == 0
	})
	// The source has been closed, so a further write has nowhere to go.
	if _, err := pw.Write([]byte("more\n")); err == nil {
		t.Fatal("the relay is still following the runner's log")
	}
}
