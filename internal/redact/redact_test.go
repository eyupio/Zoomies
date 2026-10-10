package redact

import (
	"strings"
	"testing"
	"time"
)

// Each shape a credential is known to take is found, and what surrounds it is left
// as it was, so that the answer can still say where in a log it was.
func TestEachKnownShapeOfCredentialIsReplacedAndItsSurroundingsAreNot(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"a GitHub token", "pushed with ghp_abcdefghijklmnopqrstuvwxyz0123456789 today", "pushed with " + Credential + " today"},
		{"a GitHub token run up against other text", "export_ghp_abcdefghijklmnopqrstuvwxyz0123456789", "export_" + Credential},
		{"a fine-grained GitHub token", "github_pat_11ABCDEFG0abcdefghijklmnop_qrstuvwxyz", Credential},
		{"an OpenAI key", "key sk-proj-abcdefghijklmnop1234", "key " + Credential},
		{"an Anthropic key", "sk-ant-api03-abcdefghijklmnop", Credential},
		{"an AWS key id", "id AKIAABCDEFGHIJKLMNOP end", "id " + Credential + " end"},
		{"a Google key", "AIza" + strings.Repeat("a", 35), Credential},
		{"a Slack token", "xoxb-1234567890-abcdefghij", Credential},
		{"an npm token", "npm_" + strings.Repeat("a", 36), Credential},
		{"a JWT", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijk", Credential},
		{"a private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIabc\n-----END RSA PRIVATE KEY----- after", Credential + " after"},
		{"a private key that is cut off", "-----BEGIN PRIVATE KEY-----\nMIIabc", Credential},
		{"a password in a URL", "cloning https://bot:hunter2hunter2@github.com/o/r.git", "cloning https://" + Credential + "@github.com/o/r.git"},
		{"an Authorization header", "Authorization: Bearer abcdefghijklmnopqrstuvwx", "Authorization: " + Credential},
		{"a token scheme header", "authorization: token abcdefghijklmnopqrstuvwx", "authorization: " + Credential},
		{"a bearer token alone", "sent Bearer abcdefghijklmnopqrstuvwx to it", "sent Bearer " + Credential + " to it"},
		{"a setting called password", "DB_PASSWORD=correct-horse-battery", "DB_PASSWORD=" + Credential},
		{"a quoted secret", `client_secret: "s3cr3t value here"`, "client_secret: " + Credential},
		{"an api key setting", "api_key = 'abcd1234'", "api_key = " + Credential},
		{"a json field", `{"access_token":"abcdef123456"}`, `{"access_token":` + Credential + `}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, res := Text(tc.in)
			if got != tc.want {
				t.Errorf("Text(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if res.Credentials != 1 || res.Emails != 0 {
				t.Errorf("result = %+v, want one credential", res)
			}
		})
	}
}

// The addresses of the people who wrote the commits are hidden, because they are
// personal and a model does not need them to explain a failure.
func TestEmailAddressesAreReplacedAndCountedApartFromCredentials(t *testing.T) {
	got, res := Text("Author: Ada Lovelace <ada@example.co.uk>, reviewed by bob.smith+ci@corp.example.com")
	want := "Author: Ada Lovelace <" + Email + ">, reviewed by " + Email
	if got != want || res.Emails != 2 || res.Credentials != 0 || res.Total() != 2 {
		t.Errorf("got %q with %+v", got, res)
	}
}

// What looks like a secret by its name but is not one is left alone: a count of
// tokens, a flag, a value the system masked, a reference to a secret, and prose.
func TestWhatIsNotASecretIsLeftAlone(t *testing.T) {
	for _, in := range []string{
		"input_tokens: 1234",
		`{"runners":3,"input_tokens":1234}`,
		"(tokens=4096)",
		"the task-queue-worker-name-long is a pool, not a key",
		"max_tokens=4096",
		"secrets: inherit",
		"has_token: true",
		"GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}",
		"password: ***",
		"password: ******",
		`password: "abc"`,
		"password: 'abc'",
		"token: {{vault.token}}",
		"password=abc",
		"token=${TOKEN_VALUE}",
		"token: $(cat token.txt)",
		"token: {{ vault.token }}",
		"password: [redacted credential]",
		"the token was refreshed and the secret rotated",
		"a bearer of bad news, handed over in good faith",
		"the sk-1 prefix and a gh_ prefix are not keys",
		"runner-7f3a joined pool ci-large at 10.0.0.5 (host build-1.internal)",
		"user@host is not an address, and neither is @mention",
		"https://github.com/o/r/actions/runs/123",
		"token configuration-management-system is prose",
	} {
		got, res := Text(in)
		if got != in || res.Total() != 0 {
			t.Errorf("Text(%q) = %q with %+v, want it unchanged", in, got, res)
		}
	}
}

// A span is replaced once: a token inside a setting is one credential, and
// redacting the result again changes nothing.
func TestASpanIsReplacedOnceAndRedactingTwiceChangesNothing(t *testing.T) {
	in := "TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123456789 and https://u:p4ssw0rd@h/x and Authorization: Bearer abcdefghijklmnopqrstuvwx"
	once, res := Text(in)
	if res.Credentials != 3 || res.Emails != 0 {
		t.Errorf("result = %+v, want three credentials in %q", res, once)
	}
	if strings.Contains(once, "ghp_") || strings.Contains(once, "p4ssw0rd") || strings.Contains(once, "abcdefghijklmnopqrstuvwx") {
		t.Errorf("a secret survived: %q", once)
	}
	twice, res2 := Text(once)
	if twice != once || res2.Total() != 0 {
		t.Errorf("redacting again gave %q with %+v", twice, res2)
	}
}

func TestResultsAddUp(t *testing.T) {
	a := Result{Credentials: 2, Emails: 1}
	b := Result{Credentials: 1, Emails: 4}
	if got := a.Add(b); got != (Result{3, 5}) || got.Total() != 8 {
		t.Errorf("sum = %+v", got)
	}
}

// Hostile text cannot make redaction slow in the way that matters: the time taken
// grows with the size of the text and not faster. It is checked by comparing a text
// with one eight times its size, which holds on any machine and under the race
// detector, where a fixed limit on seconds does not.
func TestHostileTextTakesTimeInProportionToItsSize(t *testing.T) {
	best := func(in string) time.Duration {
		d := time.Duration(1<<63 - 1)
		for i := 0; i < 2; i++ {
			started := time.Now()
			Text(in)
			d = min(d, time.Since(started))
		}
		return max(d, 5*time.Millisecond)
	}
	for name, unit := range map[string]string{
		"plain text":              "a",
		"near misses of a key":    "sk-",
		"settings with no value":  "password=",
		"key headers never ended": "-----BEGIN PRIVATE KEY-----",
		"half an address":         "a@",
		"half a url":              "http://a:",
		"quotes never closed":     `token="`,
	} {
		small := strings.Repeat(unit, 8<<10/len(unit))
		large := strings.Repeat(unit, 8*8<<10/len(unit))
		ts, tl := best(small), best(large)
		// Linear is eight times; the bound is a good deal more than that so that
		// noise cannot fail it, and far less than what quadratic would take (64).
		if tl > 30*ts {
			t.Errorf("%s: %d bytes took %v and %d bytes took %v", name, len(small), ts, len(large), tl)
		}
	}
}
