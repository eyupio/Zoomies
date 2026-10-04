package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// ActionsIssuer is the issuer of GitHub.com's Actions OIDC tokens. A token from
// any other issuer is not a statement GitHub made about a workflow run.
const ActionsIssuer = "https://token.actions.githubusercontent.com"

// ActionsClaims are the parts of an Actions OIDC token Zoomies decides on. Each
// is something GitHub asserts about the run, not something the workflow chose:
// a step can ask for a token but cannot change what it says.
type ActionsClaims struct {
	ID                string
	Expiry            time.Time
	Repository        string `json:"repository"`
	RepositoryID      string `json:"repository_id"`
	RepositoryOwnerID string `json:"repository_owner_id"`
	Ref               string `json:"ref"`
	SHA               string `json:"sha"`
	EventName         string `json:"event_name"`
	JobWorkflowRef    string `json:"job_workflow_ref"`
	RunID             string `json:"run_id"`
	RunAttempt        string `json:"run_attempt"`
}

// ActionsTokenVerifier checks an Actions OIDC token's signature against the
// issuer's published keys, and its issuer, audience and expiry.
type ActionsTokenVerifier struct {
	issuer string
	once   sync.Once
	keys   oidc.KeySet
}

// NewActionsTokenVerifier verifies tokens from issuer; production passes
// ActionsIssuer, and tests a local one. The keys are fetched on first use and
// cached by go-oidc, which refetches when it sees a key it does not know --
// GitHub rotates them.
func NewActionsTokenVerifier(issuer string) *ActionsTokenVerifier {
	return &ActionsTokenVerifier{issuer: strings.TrimRight(issuer, "/")}
}

// ErrActionsKeysUnavailable is a token that could not be checked because
// GitHub's signing keys could not be fetched. It says nothing about the token,
// so it must not read as a refusal of it: the workflow's log should send the
// operator to the controller's network, not to the workflow.
var ErrActionsKeysUnavailable = errors.New("GitHub's Actions signing keys could not be fetched")

// fetchRecordingKeys notes whether the key set failed to fetch, which the
// verifier would otherwise flatten into a signature error: go-oidc formats the
// key set's error with %v, so its type does not survive. The prefix it checks is
// go-oidc's own, and TestAnUnreachableIssuerIsNotABadToken pins it.
type fetchRecordingKeys struct {
	inner   oidc.KeySet
	fetched error
}

func (k *fetchRecordingKeys) VerifySignature(ctx context.Context, jwt string) ([]byte, error) {
	payload, err := k.inner.VerifySignature(ctx, jwt)
	if err != nil && strings.HasPrefix(err.Error(), "fetching keys") {
		k.fetched = err
	}
	return payload, err
}

// ErrActionsToken is returned for any token that is not a valid, current
// Actions token for this audience. The cause is wrapped for the log only.
var ErrActionsToken = errors.New("the upload's GitHub Actions OIDC token is not valid for this controller")

// Verify returns the claims of a token minted for audience. The audience is
// the controller's own upload address, so a token minted for one Zoomies is
// useless against another.
func (v *ActionsTokenVerifier) Verify(ctx context.Context, raw, audience string) (*ActionsClaims, error) {
	v.once.Do(func() {
		// Its own context: the key set outlives the request that created it.
		v.keys = oidc.NewRemoteKeySet(context.WithoutCancel(ctx), v.issuer+"/.well-known/jwks")
	})
	keys := &fetchRecordingKeys{inner: v.keys}
	verifier := oidc.NewVerifier(v.issuer, keys, &oidc.Config{ClientID: audience, SupportedSigningAlgs: []string{oidc.RS256}})
	token, err := verifier.Verify(ctx, raw)
	if err != nil {
		if keys.fetched != nil {
			return nil, fmt.Errorf("%w: %v", ErrActionsKeysUnavailable, keys.fetched)
		}
		return nil, fmt.Errorf("%w: %v", ErrActionsToken, err)
	}
	var claims ActionsClaims
	if err := token.Claims(&claims); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrActionsToken, err)
	}
	var id struct {
		JTI string `json:"jti"`
	}
	if err := token.Claims(&id); err != nil || id.JTI == "" || len(id.JTI) > 200 {
		return nil, fmt.Errorf("%w: it carries no token ID, so it could be replayed", ErrActionsToken)
	}
	claims.ID, claims.Expiry = id.JTI, token.Expiry
	return &claims, nil
}
