package controller

import (
	"context"
	"slices"
	"sync"
	"time"
)

// problemsFlight lets callers that ask for the problems list at the same moment
// share one computation of it.
//
// The list is a few dozen queries, and every browser that opens the Overview
// asks for it, so eight people arriving together used to be eight computations
// and a queue of several seconds each. There is deliberately no time-to-live: a
// caller that arrives after a computation has finished starts a new one, so the
// answer is never older than the moment it was asked for, and a test that
// changes something and asks again sees the change.
type problemsFlight struct {
	mu     sync.Mutex
	active *problemsCall
}

type problemsCall struct {
	done  chan struct{}
	items []Problem
	err   error
}

// problemsTimeout bounds a shared computation. It is not any one caller's
// request, so it is not cut short when the caller that happened to start it
// gives up.
const problemsTimeout = 30 * time.Second

// SharedProblems is Problems for callers that may overlap: the API handler and
// the event stream. Callers that arrive while a computation is running wait for
// it rather than starting another, and each gets its own copy of the list.
func (c *Controller) SharedProblems(ctx context.Context) ([]Problem, error) {
	f := &c.problemsFlight
	f.mu.Lock()
	call := f.active
	if call == nil {
		call = &problemsCall{done: make(chan struct{})}
		f.active = call
		go func() {
			// Detached from the caller's cancellation but not from its values.
			run, cancel := context.WithTimeout(context.WithoutCancel(ctx), problemsTimeout)
			defer cancel()
			call.items, call.err = c.Problems(run)
			f.mu.Lock()
			f.active = nil
			f.mu.Unlock()
			close(call.done)
		}()
	}
	f.mu.Unlock()

	select {
	case <-call.done:
		return slices.Clone(call.items), call.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
