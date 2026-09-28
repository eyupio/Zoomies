package controller

import (
	"fmt"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/config"
)

// SSOTrouble is single sign-on that is configured and not working, as the API
// -- which owns the identity provider -- reports it. The controller only
// carries it to the problems list, because that list is where an operator
// looks and the API has no list of its own.
type SSOTrouble struct {
	// Issuer is the identity provider that could not be set up.
	Issuer string
	// Reason is the last failure, written for the operator.
	Reason string
	// Since is when the first attempt failed.
	Since time.Time
	// Attempts is how many times discovery has been tried.
	Attempts int
	// Retrying says the API is trying again by itself, and NextAttempt when.
	// False is a failure no retry can fix: a setting is missing.
	Retrying    bool
	NextAttempt time.Time
}

// ssoBox is the one SSOTrouble in force, behind a lock because the API's
// retry loop writes it while a reconcile pass reads it.
type ssoBox struct {
	mu sync.Mutex
	t  *SSOTrouble
}

// SetSSOTrouble records that single sign-on is not working, or with nil that
// it is again. The problem it raises clears on the next pass after a nil.
func (c *Controller) SetSSOTrouble(t *SSOTrouble) {
	c.sso.mu.Lock()
	defer c.sso.mu.Unlock()
	if t != nil {
		cp := *t
		t = &cp
	}
	c.sso.t = t
}

// ssoProblems is oidc.unavailable while single sign-on is configured and down.
//
// It is an error rather than a warning because the people it strands are the
// ones who have no password: an account created by single sign-on cannot sign
// in at all until the provider answers.
func (c *Controller) ssoProblems() []Problem {
	c.sso.mu.Lock()
	t := c.sso.t
	c.sso.mu.Unlock()
	if t == nil {
		return nil
	}
	since := t.Since
	detail := t.Reason + ". Password sign-in still works, and the sign-in page shows its password form while this lasts; " +
		"accounts that only ever signed in through single sign-on cannot sign in until it is back."
	fix := "check that this host can reach " + t.Issuer + " and that oidc.issuer is right. "
	if t.Retrying {
		detail += fmt.Sprintf(" It has been tried %s, and is tried again by itself -- next at %s -- so it comes back without a restart once the provider answers.",
			plural(t.Attempts, "time"), t.NextAttempt.UTC().Format(time.RFC3339))
	} else {
		fix = "correct the oidc settings this names and restart the controller; this failure is not one that trying again can fix."
	}
	return []Problem{{
		Code:     "oidc.unavailable",
		Severity: config.SeverityError,
		Setting:  "oidc.issuer",
		Title:    "single sign-on is configured but not working",
		Detail:   detail,
		Fix:      fix,
		Since:    &since,
	}}
}
