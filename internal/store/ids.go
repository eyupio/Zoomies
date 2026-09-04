package store

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

// ID prefixes. Every identifier the operator can see is prefixed so that a
// copied ID is self-describing when it turns up in a log line or a bug report.
const (
	PrefixInstallation = "ins"
	PrefixPool         = "pool"
	PrefixHost         = "host"
	PrefixRunner       = "run"
	PrefixJob          = "job"
	PrefixAudit        = "aud"
	PrefixUser         = "usr"
	PrefixSession      = "ses"
	PrefixToken        = "tok"
	PrefixJoin         = "join"
	PrefixScaling      = "scl"
	PrefixDelivery     = "whd"
)

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewID returns a random, URL-safe, double-click-selectable identifier such as
// "pool_k3f9qz2mx7ab". Randomness (not a counter) keeps IDs unguessable, which
// matters because some of them appear in URLs handed to a browser.
func NewID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("zoomies: entropy source unavailable: " + err.Error())
	}
	return prefix + "_" + idEncoding.EncodeToString(b[:])
}

// HasPrefix reports whether id looks like an ID minted for the given kind.
func HasPrefix(id, prefix string) bool {
	return strings.HasPrefix(id, prefix+"_")
}

// NewSecret returns n bytes of base32 entropy, used for tokens that are shown
// to a human once and stored only as a hash.
func NewSecret(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("zoomies: entropy source unavailable: " + err.Error())
	}
	return idEncoding.EncodeToString(b)
}
