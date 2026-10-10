// Package mention decides whether a pull request comment asks Eli to repair it.
//
// It exists on its own, importing the standard library and nothing else, because
// two programs must agree on the answer: the controller that acts on a comment,
// and the hosted relay a shared Eli App would put in front of it. If they
// disagreed, a comment the relay drops would be one the controller would have
// acted on, or the reverse, and nobody could say which was right. One matcher,
// one table of cases, and a test that keeps the imports down.
//
// A match only says the comment is a command. It authorises nothing: the
// controller still checks who wrote it, whether they may write to the
// repository and whether the repository allows repairs.
package mention

import (
	"regexp"
	"strings"
)

// Handles are the names a comment can call Eli by without any set-up. Eli is
// not a GitHub account, so these are recognised from the text alone.
var Handles = []string{"eli", "zoomies"}

// pattern builds the expression for a line that starts a repair. The App's own
// slug is accepted as well: it is the one name GitHub autocompletes and links,
// so people who pick the bot from the suggestion list would otherwise type a
// command that silently does nothing. The verb must follow whitespace, which is
// what keeps `@zoomies-eyupio22 fix` from matching the slug `zoomies-eyupio2`.
func pattern(appSlug string) *regexp.Regexp {
	names := make([]string, 0, len(Handles)+1)
	for _, h := range Handles {
		names = append(names, regexp.QuoteMeta(h))
	}
	if slug := strings.TrimSpace(appSlug); slug != "" {
		names = append(names, regexp.QuoteMeta(slug))
	}
	return regexp.MustCompile(`(?i)^@(` + strings.Join(names, "|") + `)(?:\[bot\])?\s+(fix|repair)\b(.*)$`)
}

// Command returns the instruction that follows the mention, and whether the
// comment contains one. A command must begin a line outside a quote or a code
// fence, so quoting somebody else's request or pasting an example does not run
// it. appSlug is the GitHub App's own handle, or empty when it is not known.
func Command(body, appSlug string) (string, bool) {
	re := pattern(appSlug)
	fenced := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced || strings.HasPrefix(line, ">") {
			continue
		}
		if match := re.FindStringSubmatch(line); len(match) > 0 {
			return strings.TrimSpace(match[2] + match[3]), true
		}
	}
	return "", false
}
