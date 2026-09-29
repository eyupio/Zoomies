package controller

import (
	"context"
	"errors"
	"fmt"
)

// ErrLimitReached marks a refusal because one of the limits.* ceilings has
// been reached. The API answers it with a 409 and the stable code
// limit_reached, since the request is well formed and would succeed on an
// instance with room: it is the state of the fleet that refuses it, not the
// request.
var ErrLimitReached = errors.New("limit reached")

// LimitError is ErrLimitReached with the setting that refused, so the message
// an operator reads names the key they would change -- or the one they would
// ask whoever runs the instance to change, since every limit is platform-scoped.
type LimitError struct {
	Setting string
	Limit   int
	What    string
	// Free says how to make room without changing the setting. Empty is
	// "remove one first", which is right for everything stored.
	Free string
}

func (e *LimitError) Error() string {
	free := e.Free
	if free == "" {
		free = "remove one first"
	}
	return fmt.Sprintf("this instance already holds %d %s, the most %s allows; %s, or ask whoever runs this instance to raise %s",
		e.Limit, e.What, e.Setting, free, e.Setting)
}

func (e *LimitError) Unwrap() error { return ErrLimitReached }

// admit refuses when a ceiling is set and adding n more would cross it. n is
// one for everything created a request at a time; the pools import is the
// caller that creates several at once, and checking them one at a time would
// let the whole batch through against a count that has not moved yet.
func admit(setting, what string, limit, n int, count func() (int, error)) error {
	if limit <= 0 || n <= 0 {
		return nil
	}
	held, err := count()
	if err != nil {
		return fmt.Errorf("counting %s for %s: %w", what, setting, err)
	}
	if held+n > limit {
		le := &LimitError{Setting: setting, Limit: limit, What: what}
		if n > 1 {
			// "Already holds" is only true of the ceiling, not of this
			// instance, when the batch is what crosses it.
			le.Free = fmt.Sprintf("this request adds %d more, so leave some out or remove one first", n)
		}
		return le
	}
	return nil
}

// AdmitPool says whether one more pool fits under limits.pools.
func (c *Controller) AdmitPool(ctx context.Context) error { return c.AdmitPools(ctx, 1) }

// AdmitPools says whether n more pools fit under limits.pools together.
func (c *Controller) AdmitPools(ctx context.Context, n int) error {
	return admit("limits.pools", "pools", c.cfg().Limits.Pools, n, func() (int, error) {
		return c.st.CountPools(ctx)
	})
}

// AdmitJoinToken says whether one more outstanding join token fits under
// limits.join_tokens.
func (c *Controller) AdmitJoinToken(ctx context.Context) error {
	return admit("limits.join_tokens", "outstanding join tokens", c.cfg().Limits.JoinTokens, 1, func() (int, error) {
		return c.st.CountOutstandingJoinTokens(ctx)
	})
}

// admitHost says whether one more host fits under limits.hosts.
func (c *Controller) admitHost(ctx context.Context) error {
	return admit("limits.hosts", "hosts", c.cfg().Limits.Hosts, 1, func() (int, error) {
		return c.st.CountHosts(ctx)
	})
}
