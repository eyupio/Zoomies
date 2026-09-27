package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238's default, and the only one every authenticator app implements.
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters. They are RFC 6238's defaults and they are fixed: every
// authenticator app implements exactly these, and the ones that accept
// others do not all say so when they silently ignore them.
const (
	totpPeriod = 30 * time.Second
	totpDigits = 6
	// totpSkew is how many steps either side of now a code is accepted for.
	// One step covers a phone clock thirty seconds adrift and a code typed
	// as it rolled over; more widens the window an overheard code is good in.
	totpSkew = 1
	// totpSecretBytes is 160 bits, the HMAC-SHA-1 block RFC 4226 recommends.
	totpSecretBytes = 20
)

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newTOTPSecret mints a random authenticator secret, base32 without padding,
// which is what an otpauth:// address and a person typing it by hand expect.
func newTOTPSecret() string {
	b := make([]byte, totpSecretBytes)
	if _, err := rand.Read(b); err != nil {
		panic("zoomies: entropy source unavailable: " + err.Error())
	}
	return totpEncoding.EncodeToString(b)
}

// totpStep is the RFC 6238 time step t falls in.
func totpStep(t time.Time) int64 { return t.Unix() / int64(totpPeriod/time.Second) }

// totpCode is the RFC 4226 HOTP value for a step: HMAC-SHA-1 of the counter,
// dynamically truncated to totpDigits decimal digits.
func totpCode(secret string, step int64) (string, error) {
	key, err := totpEncoding.DecodeString(strings.ToUpper(strings.TrimRight(secret, "=")))
	if err != nil {
		return "", fmt.Errorf("decoding the authenticator secret: %w", err)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, value%1_000_000), nil
}

// matchTOTP returns the step a code is valid for within the skew window
// around now, or false. Every candidate is compared, in constant time, so the
// time taken does not say which step nearly matched.
func matchTOTP(secret, code string, now time.Time) (int64, bool) {
	code = normaliseCode(code)
	if len(code) != totpDigits {
		return 0, false
	}
	current := totpStep(now)
	var found int64
	ok := false
	for d := -totpSkew; d <= totpSkew; d++ {
		want, err := totpCode(secret, current+int64(d))
		if err != nil {
			return 0, false
		}
		if hmac.Equal([]byte(want), []byte(code)) && !ok {
			found, ok = current+int64(d), true
		}
	}
	return found, ok
}

// normaliseCode strips what people type around a code: spaces, the hyphen
// some apps show in the middle, and case, which matters for recovery codes.
func normaliseCode(code string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == ' ' || r == '-' || r == '\t':
			return -1
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return r
	}, strings.TrimSpace(code))
}

// totpURI is the otpauth:// address an authenticator app scans. The label
// names the instance as well as the account, so somebody with accounts on a
// staging and a production controller can tell the two entries apart.
func totpURI(secret, issuer, account string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(totpDigits))
	q.Set("period", fmt.Sprint(int(totpPeriod/time.Second)))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// Recovery codes.
const (
	// RecoveryCodeCount is how many are issued at once.
	RecoveryCodeCount = 10
	// recoveryCodeChars gives each code about 49 bits: ten characters from
	// a 31-letter alphabet, shown as two groups of five. Guessing one is
	// bounded by the same sign-in limits as a password.
	recoveryCodeChars = 10
)

var recoveryAlphabet = []byte("abcdefghjkmnpqrstuvwxyz23456789")

// newRecoveryCodes mints a set of recovery codes, formatted for reading
// aloud or writing down: lowercase, no 0/o, 1/l/i confusion, and a hyphen in
// the middle that normaliseCode ignores.
func newRecoveryCodes() []string {
	out := make([]string, RecoveryCodeCount)
	for i := range out {
		b := make([]byte, recoveryCodeChars)
		raw := make([]byte, recoveryCodeChars)
		if _, err := rand.Read(raw); err != nil {
			panic("zoomies: entropy source unavailable: " + err.Error())
		}
		for j, r := range raw {
			// 256 is not a multiple of the alphabet's length, so reject
			// the tail rather than bias the first few letters.
			for int(r) >= 256-256%len(recoveryAlphabet) {
				var one [1]byte
				if _, err := rand.Read(one[:]); err != nil {
					panic("zoomies: entropy source unavailable: " + err.Error())
				}
				r = one[0]
			}
			b[j] = recoveryAlphabet[int(r)%len(recoveryAlphabet)]
		}
		out[i] = string(b[:5]) + "-" + string(b[5:])
	}
	return out
}

// looksLikeRecoveryCode tells the two kinds of second factor apart by shape,
// so one field on the sign-in page can take either.
func looksLikeRecoveryCode(code string) bool {
	return len(normaliseCode(code)) == recoveryCodeChars
}
