package auth

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// rfcSecret is RFC 6238 appendix B's SHA-1 seed, "12345678901234567890",
// in base32.
const rfcSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

// Appendix B's vectors, cut to six digits: the last six of each eight-digit
// value, which is what every authenticator app shows.
func TestTOTPMatchesTheRFC6238Vectors(t *testing.T) {
	for _, tc := range []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	} {
		got, err := totpCode(rfcSecret, totpStep(time.Unix(tc.unix, 0)))
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("code at %d = %s, want %s", tc.unix, got, tc.want)
		}
	}
}

func TestACodeIsAcceptedOneStepEitherSideAndNoFurther(t *testing.T) {
	now := time.Unix(1111111111, 0)
	step := totpStep(now)
	for _, tc := range []struct {
		offset int64
		want   bool
	}{
		{-2, false}, {-1, true}, {0, true}, {1, true}, {2, false},
	} {
		code, _ := totpCode(rfcSecret, step+tc.offset)
		got, ok := matchTOTP(rfcSecret, code, now)
		if ok != tc.want {
			t.Errorf("a code %d steps away: accepted = %v, want %v", tc.offset, ok, tc.want)
		}
		if ok && got != step+tc.offset {
			t.Errorf("a code %d steps away matched step %d, want %d", tc.offset, got, step+tc.offset)
		}
	}
}

func TestACodeIsReadTheWayPeopleTypeIt(t *testing.T) {
	now := time.Unix(1111111111, 0)
	for _, tc := range []struct {
		typed string
		want  bool
	}{
		{"050471", true},
		{" 050 471 ", true},
		{"050-471", true},
		{"50471", false},
		{"0504711", false},
		{"", false},
		{"abcdef", false},
	} {
		if _, ok := matchTOTP(rfcSecret, tc.typed, now); ok != tc.want {
			t.Errorf("matchTOTP(%q) = %v, want %v", tc.typed, ok, tc.want)
		}
	}
}

func TestANewSecretIsA160BitBase32Key(t *testing.T) {
	a, b := newTOTPSecret(), newTOTPSecret()
	if a == b {
		t.Fatal("two secrets were the same")
	}
	if len(a) != 32 || strings.ContainsAny(a, "=abcdefghijklmnopqrstuvwxyz01") {
		t.Fatalf("secret %q is not 32 characters of unpadded upper-case base32", a)
	}
	if _, err := totpCode(a, 1); err != nil {
		t.Fatalf("a minted secret does not decode: %v", err)
	}
}

// The label names the instance as well as the account, and every parameter
// is spelled out, because an app that assumes a default it does not state
// is an app that shows the wrong code.
func TestTheProvisioningAddressSaysEverythingAnAppNeeds(t *testing.T) {
	raw := totpURI(rfcSecret, "Zoomies", "ada (ci.example.com)")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "otpauth" || u.Host != "totp" || u.Path != "/Zoomies:ada (ci.example.com)" {
		t.Fatalf("address = %s", raw)
	}
	q := u.Query()
	for k, want := range map[string]string{"secret": rfcSecret, "issuer": "Zoomies", "algorithm": "SHA1", "digits": "6", "period": "30"} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
}

func TestRecoveryCodesAreDistinctReadableAndRecognisedByShape(t *testing.T) {
	codes := newRecoveryCodes()
	if len(codes) != RecoveryCodeCount {
		t.Fatalf("%d codes, want %d", len(codes), RecoveryCodeCount)
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if seen[c] {
			t.Fatalf("code %s issued twice", c)
		}
		seen[c] = true
		if len(c) != 11 || c[5] != '-' || strings.ContainsAny(c, "01ilo") {
			t.Errorf("code %q is not two groups of five unambiguous characters", c)
		}
		for _, typed := range []string{c, strings.ToUpper(c), strings.ReplaceAll(c, "-", " ")} {
			if !looksLikeRecoveryCode(typed) {
				t.Errorf("%q is not recognised as a recovery code", typed)
			}
		}
	}
	if looksLikeRecoveryCode("123456") {
		t.Error("an authenticator code was taken for a recovery code")
	}
}
