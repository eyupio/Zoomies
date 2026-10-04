package github

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestActionsTokensAreBoundToTheirIssuerAudienceAndLifetime(t *testing.T) {
	issuer := NewFakeActionsIssuer(t)
	verifier := NewActionsTokenVerifier(issuer.URL)
	const audience = "https://zoomies.example.com/api/v1/ai-context/uploads"

	claims, err := verifier.Verify(t.Context(), issuer.Sign(t, map[string]any{"aud": audience, "repository_id": "42", "sha": "abc", "jti": "one"}), audience)
	if err != nil {
		t.Fatal(err)
	}
	if claims.RepositoryID != "42" || claims.SHA != "abc" || claims.ID != "one" {
		t.Fatalf("claims %+v", claims)
	}

	for name, token := range map[string]string{
		"another controller's audience": issuer.Sign(t, map[string]any{"aud": "https://elsewhere.example.com/api/v1/ai-context/uploads"}),
		"expired":                       issuer.Sign(t, map[string]any{"aud": audience, "exp": time.Now().Add(-time.Minute).Unix()}),
		"another issuer":                issuer.Sign(t, map[string]any{"aud": audience, "iss": "https://token.actions.example.com"}),
		"no token ID":                   issuer.Sign(t, map[string]any{"aud": audience, "jti": ""}),
	} {
		if _, err := verifier.Verify(t.Context(), token, audience); !errors.Is(err, ErrActionsToken) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}

	forged := NewFakeActionsIssuer(t)
	token := forged.Sign(t, map[string]any{"aud": audience, "iss": issuer.URL})
	if _, err := verifier.Verify(t.Context(), token, audience); !errors.Is(err, ErrActionsToken) {
		t.Errorf("a token signed with someone else's key was accepted: %v", err)
	}
}

// A controller that cannot reach GitHub's key endpoint has not seen a bad
// token; it has not been able to look. Reporting it as one would send the
// operator to the workflow instead of the controller's network.
func TestAnUnreachableIssuerIsNotABadToken(t *testing.T) {
	issuer := NewFakeActionsIssuer(t)
	token := issuer.Sign(t, map[string]any{"aud": "https://zoomies.example.com/api/v1/ai-context/uploads", "iss": "http://127.0.0.1:1"})
	verifier := NewActionsTokenVerifier("http://127.0.0.1:1")
	_, err := verifier.Verify(t.Context(), token, "https://zoomies.example.com/api/v1/ai-context/uploads")
	if !errors.Is(err, ErrActionsKeysUnavailable) || errors.Is(err, ErrActionsToken) {
		t.Fatalf("Verify against an unreachable issuer = %v; want ErrActionsKeysUnavailable alone", err)
	}
	// The operator is told which host to let the controller reach.
	var keys *ActionsKeysError
	if !errors.As(err, &keys) || keys.Host != "127.0.0.1:1" {
		t.Fatalf("the error does not name the issuer's host: %v", err)
	}
}

func TestEachGitHubHostHasItsOwnActionsIssuer(t *testing.T) {
	// An Enterprise Server signs its own tokens; trusting GitHub.com's issuer
	// for it, or the reverse, would let one vouch for the other's runs.
	for host, want := range map[string]string{
		"github.com":       "https://token.actions.githubusercontent.com",
		"ghes.example.org": "https://ghes.example.org/_services/token",
	} {
		if got := ActionsIssuerFor(host); got != want {
			t.Errorf("ActionsIssuerFor(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestATokensIssuerIsReadOnlyFromAWellFormedToken(t *testing.T) {
	issuer := NewFakeActionsIssuer(t)
	got, err := UnverifiedActionsIssuer(issuer.Sign(t, map[string]any{"iss": "https://ghes.example.org/_services/token/"}))
	if err != nil || got != "https://ghes.example.org/_services/token" {
		t.Fatalf("issuer = %q, %v", got, err)
	}
	for name, raw := range map[string]string{
		"not a JWT":   "token",
		"bad payload": "a.!!!.c",
		"no issuer":   "e30.e30.sig",
		"oversized":   strings.Repeat("a", 17<<10) + ".e30.sig",
	} {
		if _, err := UnverifiedActionsIssuer(raw); !errors.Is(err, ErrActionsToken) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
