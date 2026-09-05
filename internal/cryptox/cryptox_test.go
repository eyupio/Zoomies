package cryptox

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testKey(t *testing.T) *Key {
	t.Helper()
	k, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return k
}

// A key is pasted into a terminal from wherever it was written down, so every
// encoding the installer or a person is likely to produce has to parse, and
// anything that is not 32 bytes has to be refused rather than silently used.
func TestParseKeyAcceptsEveryEncodingTheInstallerMightProduce(t *testing.T) {
	raw := bytes.Repeat([]byte{0xAB}, KeyLen)
	cases := map[string]string{
		"standard base64": base64.StdEncoding.EncodeToString(raw),
		"raw base64":      base64.RawStdEncoding.EncodeToString(raw),
		"url base64":      base64.URLEncoding.EncodeToString(raw),
		"raw url base64":  base64.RawURLEncoding.EncodeToString(raw),
		"hex":             hex.EncodeToString(raw),
		"with whitespace": "  " + hex.EncodeToString(raw) + "\n",
	}
	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			k, err := ParseKey(encoded)
			if err != nil {
				t.Fatalf("ParseKey: %v", err)
			}
			if !bytes.Equal(k.raw, raw) {
				t.Fatal("the parsed key is not the bytes that were encoded")
			}
		})
	}

	for name, bad := range map[string]string{
		"too short":          base64.StdEncoding.EncodeToString(raw[:16]),
		"too long":           hex.EncodeToString(append(raw, 0x01)),
		"not encoded":        "definitely not a key",
		"empty is not a key": "",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseKey(bad)
			switch {
			case bad == "" && !errors.Is(err, ErrNoKey):
				t.Fatalf("an empty key = %v, want ErrNoKey", err)
			case bad != "" && !errors.Is(err, ErrBadKey):
				t.Fatalf("%q = %v, want ErrBadKey", bad, err)
			}
		})
	}
	if _, err := NewKey(raw[:31]); !errors.Is(err, ErrBadKey) {
		t.Fatalf("NewKey with 31 bytes = %v, want ErrBadKey", err)
	}
}

func TestEncodeRoundTripsThroughParse(t *testing.T) {
	k := testKey(t)
	again, err := ParseKey(k.Encode())
	if err != nil {
		t.Fatalf("ParseKey(Encode): %v", err)
	}
	if !bytes.Equal(k.raw, again.raw) {
		t.Fatal("Encode then ParseKey changed the key")
	}
}

// Seal and Open are what stand between a database backup and the GitHub App
// private key inside it.
func TestSealAndOpen(t *testing.T) {
	k := testKey(t)
	plain := []byte("-----BEGIN RSA PRIVATE KEY-----\nnot really\n")

	sealed, err := k.Seal(plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(sealed, []byte("not really")) {
		t.Fatal("the sealed value contains the plaintext")
	}
	got, err := k.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("Open = %q, want the plaintext back", got)
	}

	t.Run("every seal uses a fresh nonce", func(t *testing.T) {
		again, err := k.Seal(plain)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(sealed, again) {
			t.Fatal("two seals of the same plaintext were identical; the nonce is being reused")
		}
	})

	t.Run("a tampered byte is refused", func(t *testing.T) {
		for _, i := range []int{0, len(sealed) / 2, len(sealed) - 1} {
			bad := bytes.Clone(sealed)
			bad[i] ^= 0x01
			if _, err := k.Open(bad); err == nil {
				t.Fatalf("Open accepted a value with byte %d flipped", i)
			}
		}
	})

	t.Run("the wrong key is refused, in words an operator can act on", func(t *testing.T) {
		other := testKey(t)
		_, err := other.Open(sealed)
		if err == nil || !strings.Contains(err.Error(), "encryption key does not match") {
			t.Fatalf("Open with the wrong key = %v", err)
		}
	})

	t.Run("a truncated value is refused without panicking", func(t *testing.T) {
		if _, err := k.Open(sealed[:5]); err == nil {
			t.Fatal("Open accepted a value shorter than a nonce and a tag")
		}
	})

	t.Run("an absent secret stays absent", func(t *testing.T) {
		if s, err := k.Seal(nil); err != nil || s != nil {
			t.Fatalf("Seal(nil) = %v, %v; want nil, nil", s, err)
		}
		if s, err := k.SealString(""); err != nil || s != nil {
			t.Fatalf("SealString(\"\") = %v, %v; want nil, nil", s, err)
		}
		if p, err := k.Open(nil); err != nil || p != nil {
			t.Fatalf("Open(nil) = %v, %v; want nil, nil", p, err)
		}
		if p, err := k.OpenString(nil); err != nil || p != "" {
			t.Fatalf("OpenString(nil) = %q, %v; want empty", p, err)
		}
	})

	t.Run("a nil key refuses rather than panics", func(t *testing.T) {
		var none *Key
		if _, err := none.Seal(plain); !errors.Is(err, ErrNoKey) {
			t.Fatalf("Seal on a nil key = %v, want ErrNoKey", err)
		}
		if _, err := none.Open(sealed); !errors.Is(err, ErrNoKey) {
			t.Fatalf("Open on a nil key = %v, want ErrNoKey", err)
		}
	})
}

// The key file is the one secret on disk that unlocks every other; a file the
// group or the world can read is refused, and the file this package writes is
// one it would accept.
func TestKeyFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes do not carry the same meaning on Windows")
	}
	dir := t.TempDir()
	k := testKey(t)

	path := filepath.Join(dir, "nested", "key")
	if err := WriteKeyFile(path, k); err != nil {
		t.Fatalf("WriteKeyFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("the written key file is mode %04o, want 0600", mode)
	}
	loaded, err := LoadKeyFile(path)
	if err != nil {
		t.Fatalf("LoadKeyFile: %v", err)
	}
	if !bytes.Equal(loaded.raw, k.raw) {
		t.Fatal("the loaded key is not the one written")
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadKeyFile(path)
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("a world-readable key file was loaded: %v", err)
	}

	if _, err := LoadKeyFile(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a missing key file was loaded")
	}
}

// Passwords are hashed with argon2id in PHC form, so a hash is verifiable
// without any parameter stored beside it and a wrong password never matches.
func TestPasswordHashing(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$") {
		t.Fatalf("hash = %q, want PHC argon2id", hash)
	}
	if !VerifyPassword("correct horse battery staple", hash) {
		t.Fatal("the right password does not verify")
	}
	if VerifyPassword("correct horse battery stapler", hash) {
		t.Fatal("a wrong password verified")
	}
	if VerifyPassword("correct horse battery staple", "$argon2id$v=19$garbage") {
		t.Fatal("a malformed hash verified")
	}
	if VerifyPassword("anything", "") {
		t.Fatal("an empty hash verified")
	}
	if _, err := HashPassword(""); err == nil {
		t.Fatal("an empty password was hashed")
	}

	again, _ := HashPassword("correct horse battery staple")
	if again == hash {
		t.Fatal("two hashes of the same password were identical; the salt is being reused")
	}
}

// Tokens carry full entropy, so their hash is a plain SHA-256 that has to be
// the same every time the same token is presented.
func TestTokenHashing(t *testing.T) {
	a, b := HashToken("zt_abc"), HashToken("zt_abc")
	if a != b || len(a) != 64 {
		t.Fatalf("HashToken is not a stable hex SHA-256: %q, %q", a, b)
	}
	if HashToken("zt_abd") == a {
		t.Fatal("two different tokens hashed the same")
	}
	if !ConstantTimeEqual(a, b) || ConstantTimeEqual(a, "") {
		t.Fatal("ConstantTimeEqual gave the wrong answer")
	}
}
