package auth

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
)

var testPolicy = OAuthPolicy{Issuer: "https://zoomies.test", OpenRegistration: true}

const verifier = "a-verifier-that-is-long-enough-to-be-accepted-by-rfc-7636-0123456789"

// A native client listens wherever it could get a port, so a loopback
// redirect matches whatever the port; everything else is exact, because
// "close enough" is how an authorisation code ends up somewhere it should not.
func TestRedirectMatching(t *testing.T) {
	for _, tc := range []struct {
		registered, requested string
		want                  bool
	}{
		{"https://claude.ai/api/mcp/auth_callback", "https://claude.ai/api/mcp/auth_callback", true},
		{"https://claude.ai/api/mcp/auth_callback", "https://claude.ai/api/mcp/auth_callback/", false},
		{"https://claude.ai/api/mcp/auth_callback", "https://claude.ai:8443/api/mcp/auth_callback", false},
		{"https://claude.ai/api/mcp/auth_callback", "https://evil.example/api/mcp/auth_callback", false},
		{"http://localhost/callback", "http://localhost:3118/callback", true},
		{"http://127.0.0.1/callback", "http://127.0.0.1:50000/callback", true},
		{"http://[::1]/callback", "http://[::1]:50000/callback", true},
		{"http://localhost/callback", "http://127.0.0.1:3118/callback", false},
		{"http://localhost/callback", "http://localhost:3118/other", false},
		{"http://localhost/callback", "http://localhost:3118/callback?x=1", false},
		{"http://localhost/callback", "https://localhost:3118/callback", false},
	} {
		if got := redirectMatches(tc.registered, tc.requested); got != tc.want {
			t.Errorf("redirectMatches(%q, %q) = %v, want %v", tc.registered, tc.requested, got, tc.want)
		}
	}
}

func TestRedirectURIsMustBeHTTPSOrLoopback(t *testing.T) {
	for _, tc := range []struct {
		uri string
		ok  bool
	}{
		{"https://claude.ai/api/mcp/auth_callback", true},
		{"http://localhost:8080/callback", true},
		{"http://127.0.0.1/callback", true},
		{"http://zoomies.example/callback", false},
		{"claude://callback", false},
		{"https://claude.ai/cb#frag", false},
		{"https://user@claude.ai/cb", false},
		{"/relative", false},
	} {
		_, err := checkRedirectURIs([]string{tc.uri})
		if (err == nil) != tc.ok {
			t.Errorf("%q: err=%v, want ok=%v", tc.uri, err, tc.ok)
		}
	}
}

func TestPKCEIsS256AndChecksTheVerifierShape(t *testing.T) {
	challenge := PKCEChallenge(verifier)
	if !validChallenge(challenge) || !verifyPKCE(verifier, challenge) {
		t.Fatal("a verifier must match its own S256 challenge")
	}
	for _, v := range []string{verifier + "x", "short", strings.Repeat("a", 129), strings.Repeat("a", 42) + " "} {
		if verifyPKCE(v, challenge) {
			t.Errorf("%q must not verify", v)
		}
	}
}

// An absent resource means the MCP endpoint; the case of the scheme and host
// and a trailing slash are forgiven, as the specification asks; anything
// else is a different audience.
func TestResourceIndicator(t *testing.T) {
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{"", true},
		{"https://zoomies.test/mcp", true},
		{"HTTPS://Zoomies.Test/mcp/", true},
		{"https://zoomies.test", false},
		{"https://zoomies.test/mcp#x", false},
		{"https://other.test/mcp", false},
	} {
		got, err := CheckResource(testPolicy, tc.in)
		if (err == nil) != tc.ok {
			t.Errorf("%q: err=%v, want ok=%v", tc.in, err, tc.ok)
		}
		if err == nil && got != "https://zoomies.test/mcp" {
			t.Errorf("%q must canonicalise to the endpoint, got %q", tc.in, got)
		}
	}
}

// Claude Code names itself by the URL of a document it publishes. The
// document is fetched, must name itself, and becomes a client that can be
// listed and revoked; one that claims another's URL is refused.
func TestClientIDMetadataDocuments(t *testing.T) {
	const id = "https://claude.ai/oauth/claude-code-client-metadata"
	docs := map[string]string{
		id:                             `{"client_id":"` + id + `","client_name":"Claude Code","redirect_uris":["http://localhost/callback","http://127.0.0.1/callback"],"token_endpoint_auth_method":"none"}`,
		"https://impostor.example/doc": `{"client_id":"` + id + `","client_name":"Claude Code","redirect_uris":["https://impostor.example/cb"]}`,
		"https://secret.example/doc":   `{"client_id":"https://secret.example/doc","redirect_uris":["https://secret.example/cb"],"token_endpoint_auth_method":"private_key_jwt"}`,
	}
	fetches := 0
	cfg := config.Default()
	c := newClock()
	st, err := store.Open(t.Context(), store.Options{Path: ":memory:", Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, cfg, events.New(), WithClock(c.Now), WithClientMetadataFetcher(func(_ context.Context, u string) ([]byte, error) {
		fetches++
		if d, ok := docs[u]; ok {
			return []byte(d), nil
		}
		return nil, errors.New("it answered 404 Not Found")
	}))
	ctx := t.Context()

	begin := func(clientID, redirect string) (*store.OAuthRequest, error) {
		return s.BeginAuthorization(ctx, testPolicy, AuthorizeParams{
			ClientID: clientID, RedirectURI: redirect, ResponseType: "code",
			CodeChallenge: PKCEChallenge(verifier), CodeChallengeMethod: "S256",
		})
	}
	if _, err := begin(id, "http://localhost:3118/callback"); err != nil {
		t.Fatalf("Claude Code's document must be accepted: %v", err)
	}
	cl, err := st.GetOAuthClientByClientID(ctx, id)
	if err != nil || cl.Kind != store.OAuthClientMetadata || cl.Name != "Claude Code" {
		t.Fatalf("the document must become a listed client, got %+v %v", cl, err)
	}
	if _, err := begin(id, "http://127.0.0.1:4000/callback"); err != nil || fetches != 1 {
		t.Errorf("a second sign-in within the hour must use what was fetched, fetches=%d err=%v", fetches, err)
	}
	for _, bad := range []string{"https://impostor.example/doc", "https://secret.example/doc", "https://missing.example/doc"} {
		if _, err := begin(bad, "https://impostor.example/cb"); err == nil {
			t.Errorf("%s must be refused", bad)
		}
	}
	if _, err := s.store.RevokeOAuthClient(ctx, cl.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := begin(id, "http://localhost:3118/callback"); err == nil {
		t.Error("a revoked metadata client must stay refused")
	}
}

func TestMetadataClientIDsAreHTTPSURLsWithAPath(t *testing.T) {
	for id, want := range map[string]bool{
		"https://claude.ai/oauth/claude-code-client-metadata": true,
		"https://claude.ai":                    false,
		"https://claude.ai/":                   false,
		"http://claude.ai/doc":                 false,
		"https://claude.ai/doc#x":              false,
		"https://user@claude.ai/doc":           false,
		"oac_abcdefghijklm":                    false,
		"https://claude.ai/a/../metadata.json": false,
	} {
		if got := IsMetadataClientID(id); got != want {
			t.Errorf("IsMetadataClientID(%q) = %v, want %v", id, got, want)
		}
	}
}

// The fetch is the one request this process makes because an unauthenticated
// caller asked; it must not reach this machine or its network.
func TestMetadataFetchRefusesPrivateAddresses(t *testing.T) {
	for addr, public := range map[string]bool{
		"127.0.0.1": false, "10.1.2.3": false, "192.168.1.1": false, "169.254.169.254": false,
		"100.64.0.1": false, "::1": false, "fd00::1": false, "0.0.0.0": false,
		// The ranges a shorter list once let through: the rest of "this
		// network", benchmarking, reserved, and IPv6 addresses that carry a
		// private IPv4 destination -- the metadata service behind NAT64 is
		// the metadata service.
		"0.1.2.3": false, "198.18.0.1": false, "240.0.0.1": false, "255.255.255.255": false,
		"64:ff9b::a9fe:a9fe": false, "2002:7f00:1::": false, "::ffff:10.0.0.1": false, "fec0::1": false,
		"1.1.1.1": true, "160.79.104.1": true, "2606:4700::1111": true, "64:ff9b::101:101": true,
	} {
		err := dialTarget(addr)
		if (err == nil) != public {
			t.Errorf("dialTarget(%s) = %v, want public=%v", addr, err, public)
		}
	}
	if _, err := fetchPublic(t.Context(), "https://127.0.0.1/doc", false); err == nil || !strings.Contains(err.Error(), "not a public address") {
		t.Errorf("a loopback document must be refused before it is dialled, got %v", err)
	}
}

// Codes and access tokens expire; an access token for a disabled account
// stops working without anybody revoking it.
func TestMCPCredentialsExpire(t *testing.T) {
	s, st, c := newService(t)
	ctx := t.Context()
	u := addUser(t, st, "olive", store.RoleOperator, nil)
	id := &Identity{Kind: KindUser, ID: u.ID, Name: u.Username, Role: u.Role}
	cl, err := s.RegisterClient(ctx, testPolicy, ClientRegistration{RedirectURIs: []string{ClaudeCallback}}, "203.0.113.1")
	if err != nil {
		t.Fatal(err)
	}
	approve := func() string {
		r, err := s.BeginAuthorization(ctx, testPolicy, AuthorizeParams{ClientID: cl.ClientID, RedirectURI: ClaudeCallback,
			ResponseType: "code", CodeChallenge: PKCEChallenge(verifier), CodeChallengeMethod: "S256"})
		if err != nil {
			t.Fatal(err)
		}
		d, err := s.ApproveAuthorization(ctx, testPolicy, id, r.ID, store.RoleOperator)
		if err != nil {
			t.Fatal(err)
		}
		back, _ := url.Parse(d.RedirectTo)
		return back.Query().Get("code")
	}
	exchange := func(code string) (*TokenResponse, error) {
		return s.Token(ctx, testPolicy, ClientCredentials{ClientID: cl.ClientID}, TokenRequest{
			GrantType: "authorization_code", Code: code, CodeVerifier: verifier, RedirectURI: ClaudeCallback}, "203.0.113.1")
	}

	stale := approve()
	c.Advance(mcpCodeTTL + time.Second)
	if _, err := exchange(stale); err == nil {
		t.Error("an expired code must be refused")
	}

	pair, err := exchange(approve())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.AuthenticateMCP(ctx, testPolicy, pair.AccessToken, ""); err != nil || got.Kind != KindConnection || got.Role != store.RoleOperator || got.UserID != u.ID {
		t.Fatalf("a fresh access token resolves to the connection, got %+v %v", got, err)
	}
	c.Advance(mcpAccessTTL + time.Second)
	if _, err := s.AuthenticateMCP(ctx, testPolicy, pair.AccessToken, ""); !errors.Is(err, ErrMCPTokenExpired) {
		t.Errorf("an expired access token must say so, got %v", err)
	}
	next, err := s.Token(ctx, testPolicy, ClientCredentials{ClientID: cl.ClientID}, TokenRequest{GrantType: "refresh_token", RefreshToken: pair.RefreshToken}, "")
	if err != nil {
		t.Fatalf("the refresh token outlives the access token: %v", err)
	}
	if err := s.SetUserDisabled(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateMCP(ctx, testPolicy, next.AccessToken, ""); !errors.Is(err, ErrAccountDisabled) {
		t.Errorf("a disabled account's connection must stop, got %v", err)
	}
	if _, err := s.Token(ctx, testPolicy, ClientCredentials{ClientID: cl.ClientID}, TokenRequest{GrantType: "refresh_token", RefreshToken: next.RefreshToken}, ""); err == nil {
		t.Error("a disabled account's connection must not be refreshed")
	}

	// The retention pass clears what has expired and the self-registered
	// clients that never finished signing in.
	orphan, _ := s.RegisterClient(ctx, testPolicy, ClientRegistration{RedirectURIs: []string{ClaudeCallback}}, "203.0.113.2")
	c.Advance(mcpRefreshTTL + 48*time.Hour)
	if _, err := st.PruneOAuth(ctx, c.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetOAuthClient(ctx, orphan.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a client that never completed a sign-in must be pruned, got %v", err)
	}
	if _, err := st.GetOAuthClient(ctx, cl.ID); err != nil {
		t.Errorf("a client that was used must be kept: %v", err)
	}
}

// A consent is a person's, in a browser: a token cannot approve one.
func TestConsentNeedsAPerson(t *testing.T) {
	s, _, _ := newService(t)
	for _, id := range []*Identity{nil, {Kind: KindToken, ID: "tok_x", Role: store.RoleAdmin}, DevIdentity("")} {
		if _, err := s.ApproveAuthorization(t.Context(), testPolicy, id, "oar_x", store.RoleViewer); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%v must be refused as not a person, got %v", id, err)
		}
	}
}
