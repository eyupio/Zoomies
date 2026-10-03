package github

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"time"
)

// fakeT is the part of *testing.T the fake issuer uses, so this production
// package does not import testing.
type fakeT interface {
	Helper()
	Fatal(args ...any)
	Cleanup(func())
}

// FakeActionsIssuer stands in for token.actions.githubusercontent.com: it
// publishes a key set and signs tokens with the matching private key, so the
// verifier is exercised end to end without the network.
type FakeActionsIssuer struct {
	URL string
	key *rsa.PrivateKey
}

func NewFakeActionsIssuer(t fakeT) *FakeActionsIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/jwks" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(srv.Close)
	return &FakeActionsIssuer{URL: srv.URL, key: key}
}

// Sign mints a token with the given claims, defaulting the registered ones.
func (f *FakeActionsIssuer) Sign(t fakeT, claims map[string]any) string {
	t.Helper()
	now := time.Now()
	all := map[string]any{"iss": f.URL, "iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "jti": "jti-" + now.Format(time.RFC3339Nano)}
	for k, v := range claims {
		all[k] = v
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "test"})
	body, _ := json.Marshal(all)
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}
