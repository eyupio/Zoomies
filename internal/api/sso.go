package api

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/controller"
)

// Single sign-on comes up when it can, not only when the process starts.
//
// Discovery is a network call to somebody else's service, and a controller
// that boots while that service is down -- a power cut that took both, an
// identity provider mid-upgrade -- used to leave single sign-on off until the
// next restart, which nobody makes because nothing said one was needed. The
// provider is now tried again in the background, backing off, and the problems
// list says so while it is failing.
const (
	// oidcRetryMin is the first wait after a failure, and each one after it
	// doubles up to oidcRetryMax: soon enough that a provider which blinked is
	// back before anybody notices, and rare enough that one which is down for
	// the afternoon is asked a dozen times an hour rather than every second.
	oidcRetryMin = 5 * time.Second
	oidcRetryMax = 5 * time.Minute
	// oidcDiscoveryTimeout bounds one attempt.
	oidcDiscoveryTimeout = 15 * time.Second
)

// ssoState is the provider, or why there is none, behind a lock: the retry
// loop writes it while every sign-in reads it.
type ssoState struct {
	mu       sync.RWMutex
	provider *auth.OIDCProvider
	err      error
	since    time.Time
	attempts int
}

// oidcProvider is single sign-on as it stands: nil while it is off or not yet
// working, which every method on the provider tolerates.
func (s *Server) oidcProvider() *auth.OIDCProvider {
	s.sso.mu.RLock()
	defer s.sso.mu.RUnlock()
	return s.sso.provider
}

// oidcFailure is why single sign-on is configured and not working, or nil.
func (s *Server) oidcFailure() error {
	s.sso.mu.RLock()
	defer s.sso.mu.RUnlock()
	return s.sso.err
}

// useOIDC installs a working provider and clears the problem.
func (s *Server) useOIDC(p *auth.OIDCProvider) {
	s.sso.mu.Lock()
	s.sso.provider, s.sso.err, s.sso.since, s.sso.attempts = p, nil, time.Time{}, 0
	s.sso.mu.Unlock()
	s.ctrl.SetSSOTrouble(nil)
}

// tryOIDC makes one attempt at discovery, and records what came of it. It
// reports whether another attempt could help.
func (s *Server) tryOIDC(ctx context.Context, next time.Duration) (retry bool) {
	cfg := s.cfg()
	actx, cancel := context.WithTimeout(ctx, oidcDiscoveryTimeout)
	defer cancel()
	p, err := auth.NewOIDC(actx, cfg.OIDC, cfg.Server.ExternalURL)
	if err == nil {
		if p != nil && s.oidcFailure() != nil {
			s.log.Info("single sign-on is working again", "issuer", cfg.OIDC.Issuer)
		}
		s.useOIDC(p)
		return false
	}
	retry = errors.Is(err, auth.ErrOIDCDiscovery)
	now := s.ctrl.Now()
	s.sso.mu.Lock()
	if s.sso.since.IsZero() {
		s.sso.since = now
	}
	s.sso.attempts++
	s.sso.provider, s.sso.err = nil, err
	trouble := &controller.SSOTrouble{
		Issuer: cfg.OIDC.Issuer, Reason: err.Error(), Since: s.sso.since,
		Attempts: s.sso.attempts, Retrying: retry, NextAttempt: now.Add(next),
	}
	s.sso.mu.Unlock()
	s.ctrl.SetSSOTrouble(trouble)
	s.log.Error("single sign-on is configured but could not be set up; password sign-in still works",
		"issuer", cfg.OIDC.Issuer, "error", err, "retrying", retry)
	return retry
}

// initOIDC discovers the identity provider, if there is one, once.
//
// A provider that is down when the controller boots must not stop the
// controller booting: password sign-in still works, and the sign-in page needs
// to come up to say so. retryOIDC picks it up from there.
func (s *Server) initOIDC() {
	if !s.cfg().OIDC.Enabled {
		return
	}
	s.tryOIDC(context.Background(), s.oidcRetry)
}

// retryOIDC tries discovery again, backing off, until it works, the failure is
// one that retrying cannot fix, or ctx ends.
func (s *Server) retryOIDC(ctx context.Context) {
	wait := s.oidcRetry
	for {
		err := s.oidcFailure()
		if err == nil || !errors.Is(err, auth.ErrOIDCDiscovery) {
			return
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		wait = min(wait*2, max(oidcRetryMax, s.oidcRetry))
		if !s.tryOIDC(ctx, wait) {
			return
		}
	}
}

// passwordLoginHidden says whether the sign-in page offers single sign-on
// alone. It needs single sign-on to be working at this moment as well as the
// setting: hiding the form while the provider is down would leave nobody a way
// in, and the setting says what an operator wants when there is a choice.
func (s *Server) passwordLoginHidden() bool {
	return s.cfg().OIDC.HidePasswordLogin && s.oidcProvider().Enabled()
}
