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
	PrefixJobEvent     = "jev"
	PrefixController   = "ctl"
	PrefixProvider     = "prv"
	// PrefixProviderSetup names a short-lived Proxmox connection enrolment.
	PrefixProviderSetup = "pvs"
	// PrefixBackupRemote names an offsite backup destination kept in the
	// database, so a row id in a log line says what it is without a lookup.
	PrefixBackupRemote = "bkr"
	PrefixMachine      = "mach"
	// PrefixMachineOp is minted per attempt rather than per machine: it is the
	// operation ID an operator quotes when a create is stuck, and two attempts
	// sharing one would make a log line ambiguous about which of them it came
	// from.
	PrefixMachineOp = "mop"
	// The OAuth rows behind /mcp: a client that may ask for a grant, a
	// request waiting at the consent screen, and a grant -- one person's MCP
	// connection -- that every code and token descends from.
	PrefixOAuthClient  = "oac"
	PrefixOAuthRequest = "oar"
	PrefixOAuthGrant   = "ocg"
	PrefixAIContext    = "aic"
	// PrefixAIArtifact names one version of an assistant-written note.
	PrefixAIArtifact = "aia"
	// The Kennel Club rows: a repository whose standards are being checked,
	// and a waiver a person recorded about one of its findings.
	PrefixKennelRepository = "kcr"
	PrefixKennelWaiver     = "kcw"
	// PrefixHostAcceptance names an operator's acceptance of one host's check.
	PrefixHostAcceptance = "hca"
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

// generatedIDLength is how many characters NewID's random part always has:
// eight bytes of base32 with no padding. It is a property of NewID, so a
// change to the byte count above has to change this too, and a test holds the
// two together.
const generatedIDLength = 13

// LooksGenerated reports whether id has the exact shape NewID produces: a
// prefix, an underscore, and thirteen characters of the base32 alphabet.
//
// It exists for the one caller that needs to tell a fixture apart from a real
// row. The demo seed names its rows with readable identifiers such as
// "pool_demolinux", and a check that only looked for the "demo" prefix would
// also match a real identifier that happened to start that way -- which, at
// four letters over a 32-character alphabet, one row in a million does. A
// fixture is never this shape, so the shape is what settles it.
func LooksGenerated(id string) bool {
	_, rest, ok := strings.Cut(id, "_")
	if !ok || len(rest) != generatedIDLength {
		return false
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
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
