// Package cryptox holds the two cryptographic primitives Zoomies needs:
// authenticated encryption for secrets at rest, and password hashing.
//
// There is deliberately no key-management ceremony here. The instance key comes
// from the environment or a file with 0600 permissions, and every secret column
// in the database is sealed with it. Losing the key means re-entering the
// GitHub App private key -- which is recoverable -- rather than losing the
// fleet's state.
package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/argon2"
)

// KeyLen is the required instance key length in bytes.
const KeyLen = 32

// ErrNoKey is returned when a seal or open is attempted without a key.
var ErrNoKey = errors.New("cryptox: no encryption key configured")

// ErrBadKey is returned when a configured key is the wrong length or encoding.
var ErrBadKey = errors.New("cryptox: encryption key must be 32 bytes, base64 or hex encoded")

// Key is an instance encryption key.
type Key struct {
	aead cipher.AEAD
	raw  []byte
}

// NewKey wraps 32 raw bytes.
func NewKey(raw []byte) (*Key, error) {
	if len(raw) != KeyLen {
		return nil, ErrBadKey
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	dup := make([]byte, len(raw))
	copy(dup, raw)
	return &Key{aead: aead, raw: dup}, nil
}

// GenerateKey returns a fresh random instance key.
func GenerateKey() (*Key, error) {
	raw := make([]byte, KeyLen)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	return NewKey(raw)
}

// ParseKey accepts a base64 (standard or raw URL) or hex encoded 32-byte key.
// Surrounding whitespace is tolerated because keys get pasted into terminals.
func ParseKey(s string) (*Key, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, ErrNoKey
	}
	for _, dec := range []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
		hex.DecodeString,
	} {
		if raw, err := dec(s); err == nil && len(raw) == KeyLen {
			return NewKey(raw)
		}
	}
	return nil, ErrBadKey
}

// LoadKeyFile reads a key from a file, refusing world- or group-readable files.
func LoadKeyFile(path string) (*Key, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("cryptox: reading key file %s: %w", path, err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return nil, fmt.Errorf("cryptox: key file %s is mode %04o; it must not be readable by group or other (chmod 600 %s)",
			path, mode, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cryptox: reading key file %s: %w", path, err)
	}
	return ParseKey(string(b))
}

// WriteKeyFile writes a key to disk with 0600 permissions, creating parents.
func WriteKeyFile(path string, k *Key) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(k.Encode()+"\n"), 0o600)
}

// Encode renders the key as standard base64, the form the installer prints and
// zoomies.yaml stores.
func (k *Key) Encode() string { return base64.StdEncoding.EncodeToString(k.raw) }

// Seal encrypts plaintext, returning nonce||ciphertext. Nil input seals to nil
// so that an absent secret round-trips as absent rather than as empty bytes.
func (k *Key) Seal(plaintext []byte) ([]byte, error) {
	if k == nil {
		return nil, ErrNoKey
	}
	if plaintext == nil {
		return nil, nil
	}
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return k.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// SealString is Seal for text secrets.
func (k *Key) SealString(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	return k.Seal([]byte(s))
}

// Open decrypts a value produced by Seal.
func (k *Key) Open(sealed []byte) ([]byte, error) {
	if k == nil {
		return nil, ErrNoKey
	}
	if len(sealed) == 0 {
		return nil, nil
	}
	ns := k.aead.NonceSize()
	if len(sealed) < ns+k.aead.Overhead() {
		return nil, errors.New("cryptox: ciphertext is too short to be valid")
	}
	out, err := k.aead.Open(nil, sealed[:ns], sealed[ns:], nil)
	if err != nil {
		return nil, errors.New("cryptox: could not decrypt; the encryption key does not match the one this data was written with")
	}
	return out, nil
}

// OpenString is Open for text secrets.
func (k *Key) OpenString(sealed []byte) (string, error) {
	b, err := k.Open(sealed)
	return string(b), err
}

// ---------------------------------------------------------------------------
// Password hashing
// ---------------------------------------------------------------------------

// argon2id parameters. These target roughly 100 ms on a modest VM, which is the
// right order of magnitude for an interactive login on the kind of host this
// controller runs on.
const (
	argonTime    = 2
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashPassword returns a PHC-formatted argon2id hash.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("cryptox: password must not be empty")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum)), nil
}

// VerifyPassword reports whether password matches a PHC-formatted hash. It
// always runs the full KDF so that a wrong password and an unknown user take
// the same time.
func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var memory uint32
	var time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// DummyVerify burns the same CPU as VerifyPassword. Login handlers call it when
// the username does not exist, so that response timing does not enumerate users.
func DummyVerify(password string) {
	argon2.IDKey([]byte(password), make([]byte, argonSaltLen),
		argonTime, argonMemory, argonThreads, argonKeyLen)
}

// ---------------------------------------------------------------------------
// Token hashing
// ---------------------------------------------------------------------------

// HashToken returns the hex SHA-256 of a bearer token. Tokens carry full
// entropy from the start, so a fast hash is sufficient and keeps API
// authentication off the argon2 path.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ConstantTimeEqual compares two strings without leaking their contents through
// timing.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
