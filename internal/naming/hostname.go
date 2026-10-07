package naming

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxHostNameLength bounds a host's name. Generous: names such as
// zoomies-12vcpu-31gb-debian-12-zoomies are common and a cloud hostname can
// run to sixty characters, so the limit is only there to refuse a pasted
// paragraph rather than store it.
const MaxHostNameLength = 128

// ValidateHostName reports what is wrong with the name a host asks to be called,
// in a sentence the API can hand straight back to whoever is enrolling or
// renaming it. It is the one rule for both: an agent chooses its name at join
// and an operator may change it afterwards, and a name that one door refuses
// and the other lets through is no rule at all.
//
// A host name is otherwise free text, unlike a pool's or a runner's, because it
// is what the operator sees and nothing downstream is built from it. What is
// refused is what could do harm once it is in a sentence: the problems list
// quotes commands in backticks and the UI draws each pair as a command with a
// copy button, so a name carrying a pair would put text of the agent's choosing
// in front of every administrator as something to copy and run. Control
// characters would break a line of that text in two for the same reason.
//
// The sentence never repeats the name it refuses. It is returned over the API
// and written to logs, and a refused name is exactly the text nobody has vouched
// for.
func ValidateHostName(name string) error {
	switch {
	case strings.TrimSpace(name) == "":
		return errors.New("a host needs a name; it is how it appears everywhere else")
	case utf8.RuneCountInString(name) > MaxHostNameLength:
		return fmt.Errorf("a host name is at most %d characters", MaxHostNameLength)
	case strings.ContainsFunc(name, unicode.IsControl):
		return errors.New("a host name cannot contain control characters or line breaks")
	case strings.Contains(name, "`"):
		return errors.New("a host name cannot contain a backtick, because the UI draws whatever sits between two of them " +
			"as a command with a copy button; leave it out")
	}
	return nil
}

// ForSentence is a host's name made safe to put inside a sentence the UI will
// render: a backtick becomes an apostrophe and a control character a space.
//
// ValidateHostName keeps new names from arriving with either, but it cannot
// reach one that is already stored -- joins were not checked before it existed,
// and a host that is working must go on working rather than be refused at its
// next join -- so every sentence that names a host goes through this on the way
// out. A name the rule accepts comes back unchanged, which is what keeps a host
// called one thing on its page and in every sentence about it.
//
// The name stays readable rather than being replaced by a placeholder: the
// sentence is there to say which machine it is about, and the apostrophes are
// enough to show an operator that the name is odd.
func ForSentence(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '`':
			return '\''
		case unicode.IsControl(r):
			return ' '
		}
		return r
	}, name)
}
