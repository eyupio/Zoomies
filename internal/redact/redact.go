// Package redact removes what should not leave this machine from text that is about
// to be sent to a model: credentials, and the email addresses of the people who
// wrote commits.
//
// It is pure and imports only the standard library, so it can be held to what it
// promises by tests that need no controller. It recognises the shapes credentials
// are known to take. It cannot recognise a secret that has no shape, such as a
// password in a sentence, and the documentation says so wherever it is described.
package redact

import (
	"regexp"
	"strings"
)

// Credential and Email are what a redacted span is replaced with. They are words
// a person and a model can both read, so the answer can say something was hidden.
const (
	Credential = "[redacted credential]"
	Email      = "[redacted email]"
)

// rule is one thing to find.
type rule struct {
	re *regexp.Regexp
	// replace is an expansion template over the match's groups, so that the name of
	// a setting can stay readable while its value goes.
	replace string
	// email marks the rule that counts as an email and not a credential.
	email bool
	// keep, when set, is asked about each match and its groups, and a match it says
	// true to is left as it was: a value that is not a secret at all.
	keep func(groups []string) bool
}

// rules run in order. The ones that take a larger span (a private key, an address
// with a password in it, a setting and its value) come before the ones that would
// take a part of it, so that nothing is replaced twice.
var rules = []rule{
	{re: regexp.MustCompile(`(?i)-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`), replace: Credential},
	// A password in the address of a repository or a registry.
	{re: regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^\s/@:]+:[^\s/@]+@`), replace: "${1}" + Credential + "@"},
	// A setting whose name says it is a secret: the name stays and the value goes.
	// The value is a quoted string or a run with no space, at least four characters.
	{
		re:      regexp.MustCompile(`(?i)\b([a-z0-9_.-]*(?:password|passwd|secret|token|api[_-]?key|private[_-]?key|credential)[a-z0-9_.-]*)("?\s*[:=]\s*)("[^"\n]{4,}"|'[^'\n]{4,}'|[^\s"',;&}\])]{4,})`),
		replace: "${1}${2}" + Credential,
		keep:    func(g []string) bool { return notASecret(g[3]) },
	},
	// An Authorization header's value, whatever scheme it carries.
	{re: regexp.MustCompile(`(?i)\b(authorization\s*[:=]\s*)(?:(?:bearer|basic|token)\s+)?[a-z0-9._~+/=-]{16,}`), replace: "${1}" + Credential},
	{re: regexp.MustCompile(`(?i)\b(bearer)\s+[a-z0-9._~+/=-]{20,}`), replace: "${1} " + Credential},
	// GitHub's own prefixes are distinctive enough to need no word boundary, so a token
	// run up against other text is found as well.
	{re: regexp.MustCompile(`(?i)(?:gh[pousr]_[a-z0-9_]{10,}|github_pat_[a-z0-9_]{10,})`), replace: Credential},
	{re: regexp.MustCompile(`(?i)\bsk-(?:proj-|ant-)?[a-z0-9_-]{12,}`), replace: Credential},
	{re: regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`), replace: Credential},
	{re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`), replace: Credential},
	{re: regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), replace: Credential},
	{re: regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`), replace: Credential},
	{re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), replace: Credential},
	{re: regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`), replace: Email, email: true},
}

// notASecret is whether the value of a setting called something like "token" is
// plainly not one: a number (a count of tokens is not a token), a flag, a value the
// system has masked or already redacted, or a reference to a secret and not the
// secret itself.
func notASecret(v string) bool {
	v = strings.Trim(v, `"'`)
	lower := strings.ToLower(v)
	switch lower {
	case "true", "false", "null", "none", "nil", "inherit", "required", "optional":
		return true
	}
	if strings.HasPrefix(v, "[redacted") || strings.HasPrefix(v, "***") || strings.HasPrefix(v, "${") ||
		strings.HasPrefix(v, "{{") || strings.HasPrefix(v, "$(") {
		return true
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Result is what was found.
type Result struct {
	// Credentials and Emails are how many of each were replaced.
	Credentials, Emails int
}

// Total is every span that was replaced.
func (r Result) Total() int { return r.Credentials + r.Emails }

// Add is the sum of two results, for a conversation of many texts.
func (r Result) Add(o Result) Result {
	return Result{r.Credentials + o.Credentials, r.Emails + o.Emails}
}

// Text returns s with every span the rules recognise replaced, and how many there
// were of each kind.
func Text(s string) (string, Result) {
	var res Result
	for _, r := range rules {
		n := 0
		s = r.re.ReplaceAllStringFunc(s, func(m string) string {
			groups := r.re.FindStringSubmatch(m)
			if r.keep != nil && r.keep(groups) {
				return m
			}
			n++
			return string(r.re.ExpandString(nil, r.replace, m, r.re.FindStringSubmatchIndex(m)))
		})
		if r.email {
			res.Emails += n
		} else {
			res.Credentials += n
		}
	}
	return s, res
}
