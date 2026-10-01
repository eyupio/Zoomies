package docs

import (
	"crypto/rand"
	"encoding/base64"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/cryptox"
)

// The demo page is the front door for somebody who has not yet decided to
// install anything, and the first thing they do with it is paste the block.
// A block that has stopped working is the project's worst first impression and
// nothing else would notice: the docs build checks that links resolve, not that
// a command runs, and the demo takes a binary, a free port and a browser to run
// for real.
//
// So these tests hold the block to the code the way the rest of this package
// holds a reference page to its source. They do not run it. They read what it
// says, build the configuration the controller would build from the environment
// it sets, and judge that with the controller's own validator -- which is what
// turns a renamed setting or a new refusal into a failed build rather than a
// visitor's first error.

const (
	demoPage       = "../../docs/demo.md"
	homePage       = "../../docs/index.md"
	quickstartPage = "../../docs/quickstart.md"
	mkdocsConfig   = "../../mkdocs.yml"
	installScript  = "../../install.sh"
	commandTable   = "../../cmd/zoomies/main.go"

	// How a Markdown link to the demo page is written, and what the hero's says.
	demoLink  = "](demo.md)"
	demoLabel = "Try the demo fleet (no GitHub needed)"
)

// readText reads a file for a test, which cannot go on without it.
func readText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// fencedBlocks is every ``` fence in a Markdown document, with the language it
// names and what is inside it.
func fencedBlocks(markdown string) (langs, bodies []string) {
	open := false
	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "```") {
			if open {
				bodies[len(bodies)-1] += line + "\n"
			}
			continue
		}
		if open {
			open = false
			continue
		}
		open = true
		langs = append(langs, strings.TrimSpace(strings.TrimPrefix(trimmed, "```")))
		bodies = append(bodies, "")
	}
	return langs, bodies
}

// demoBlock is the demo page's block to paste, with the page it is on. When the
// page has more than one it is the first that the other tests judge, so that a
// page which breaks the one-block rule fails once, in the test that holds it,
// rather than in all of them.
func demoBlock(t *testing.T) (page, block string) {
	t.Helper()
	page = readText(t, demoPage)
	_, bodies := fencedBlocks(page)
	if len(bodies) == 0 {
		t.Fatal("docs/demo.md has no fenced block; the block to paste is the point of the page")
	}
	return page, bodies[0]
}

// logicalLines joins a shell block's lines the way the shell reads them: a
// trailing backslash continues the line, and a comment is not a line.
func logicalLines(block string) []string {
	var out []string
	var cur string
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimRight(line, " \t")
		if strings.HasSuffix(line, `\`) {
			cur += strings.TrimSuffix(line, `\`) + " "
			continue
		}
		cur += line
		if strings.TrimSpace(cur) != "" && !strings.HasPrefix(strings.TrimSpace(cur), "#") {
			out = append(out, strings.TrimSpace(cur))
		}
		cur = ""
	}
	return out
}

// shellWords splits a line into words as far as this block needs: white space
// separates them, except inside quotes or a $( ) substitution.
func shellWords(line string) []string {
	var words []string
	var cur strings.Builder
	var quote rune
	depth := 0
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case quote != 0:
			cur.WriteRune(r)
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
			cur.WriteRune(r)
		case r == '$' && i+1 < len(runes) && runes[i+1] == '(':
			depth++
			cur.WriteString("$(")
			i++
		case r == ')' && depth > 0:
			depth--
			cur.WriteRune(r)
		case unicode.IsSpace(r) && depth == 0:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return words
}

var assignment = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)

// opensslKey is the one command substitution the block may use. The test stands
// in for it with what it prints -- 32 random bytes, base64 encoded -- rather than
// running it, so that the key the controller is judged against is the shape
// `openssl rand -base64 32` really produces.
const opensslKey = "$(openssl rand -base64 32)"

// demoController is the line of the block that starts the controller: the
// variables set in front of it, and the command they are set for.
func demoController(t *testing.T, block string) (env map[string]string, command []string) {
	t.Helper()
	found := 0
	for _, line := range logicalLines(block) {
		words := shellWords(line)
		i := 0
		vars := map[string]string{}
		for ; i < len(words); i++ {
			m := assignment.FindStringSubmatch(words[i])
			if m == nil {
				break
			}
			value := strings.Trim(m[2], `"'`)
			if strings.Contains(value, opensslKey) {
				raw := make([]byte, cryptox.KeyLen)
				if _, err := rand.Read(raw); err != nil {
					t.Fatalf("making a key to stand in for openssl's: %v", err)
				}
				value = strings.ReplaceAll(value, opensslKey, base64.StdEncoding.EncodeToString(raw))
			}
			if strings.Contains(value, "$(") || strings.Contains(value, "`") || strings.Contains(value, "$") {
				t.Fatalf("%s=%s: the test can stand in for %s and for nothing else, so a new substitution needs teaching to it first", m[1], m[2], opensslKey)
			}
			vars[m[1]] = value
		}
		if len(vars) == 0 {
			continue
		}
		found++
		env, command = vars, words[i:]
	}
	if found != 1 {
		t.Fatalf("the demo block has %d lines that set variables for a command; the test reads exactly one, the line that starts the controller", found)
	}
	return env, command
}

// The page offers one block to paste, and it is a shell block.
//
// "One block" is the promise the page makes, and it is worth holding: a second
// block is a second thing to get wrong, and it is exactly the addition -- a
// cleanup command, a Docker variant -- that gets made by somebody who has not
// read this file, and that none of the tests below would look at.
func TestTheDemoPageHasOneBlockToPaste(t *testing.T) {
	langs, bodies := fencedBlocks(readText(t, demoPage))
	if len(bodies) != 1 {
		t.Fatalf("docs/demo.md has %d fenced blocks; it is meant to have exactly one, the block to paste. "+
			"Put anything else in a sentence, or on a page of its own", len(bodies))
	}
	if langs[0] != "sh" {
		t.Errorf("the demo block is tagged %q; it is a shell block, and the site highlights it and offers to copy it as one", langs[0])
	}
	if strings.TrimSpace(bodies[0]) == "" {
		t.Error("the demo block is empty")
	}
}

// The block's configuration is one the controller starts on.
//
// The controller refuses to start on any error-severity finding, and some of
// those refusals exist for exactly the shape the demo has -- authentication off
// is an error the moment an external URL or a public bind is added, which is
// why the block must not set either. A new refusal, or a setting renamed out
// from under a variable, would otherwise be found by the first visitor to paste
// it.
func TestTheDemoBlockStartsAControllerThatPassesItsOwnValidation(t *testing.T) {
	// A developer's own Zoomies on the same machine must not be able to change
	// the answer, and the defaults that look at the state directory read these.
	t.Setenv("ZOOMIES_STATE_DIR", t.TempDir())
	t.Setenv("ZOOMIES_CONFIG_DIR", t.TempDir())

	_, block := demoBlock(t)
	env, command := demoController(t, block)

	if len(command) != 2 || command[0] != "zoomies" || command[1] != "controller" {
		t.Errorf("the block starts %q; the demo is a controller started by hand", strings.Join(command, " "))
	}

	// Effective is the controller's own assembly -- defaults, then the
	// environment given -- for a caller that is not the controller process, so the
	// variables are read from this map and not from the real environment.
	cfg, _, err := config.Effective("", nil, nil, env)
	if err != nil {
		t.Fatalf("the controller would not read the environment the block sets: %v", err)
	}
	for _, f := range cfg.Validate().Errors() {
		t.Errorf("the controller would refuse to start the demo: %s -- %s Fix: %s", f.Title, f.Detail, f.Fix)
	}

	// The key is not a setting the validator reads; the controller parses it
	// after validation, and refuses a key it cannot.
	if _, err := cryptox.ParseKey(cfg.Security.EncryptionKey); err != nil {
		t.Errorf("the controller would not accept the key the block makes: %v", err)
	}

	// Seeding is read from the process's environment by the controller itself,
	// and only a value that parses as true turns it on.
	seed, ok := env[controller.SeedEnvVar]
	if !ok {
		t.Fatalf("the block does not set %s, so the controller would start with an empty fleet", controller.SeedEnvVar)
	}
	if on, err := strconv.ParseBool(seed); err != nil || !on {
		t.Errorf("%s=%s does not turn demo seeding on", controller.SeedEnvVar, seed)
	}
}

// Every ZOOMIES_ name the page writes is one the controller reads.
//
// The environment layer ignores a name it does not know, and says nothing. A
// typo in ZOOMIES_DISABLE_AUTH would start a controller with authentication on
// and a first-run page asking for a setup token, where the page promised a
// fleet to look at -- and nothing in the log would say why. The names that are
// settings are checked against the registry; the one that is not, the switch
// that seeds the demo, is checked against the constant the controller reads.
func TestEveryVariableTheDemoPageNamesIsOneTheControllerReads(t *testing.T) {
	page := readText(t, demoPage)

	known := map[string]bool{controller.SeedEnvVar: true}
	for _, s := range config.Settings() {
		if s.Env != "" {
			known[s.Env] = true
		}
	}

	named := map[string]bool{}
	for _, name := range regexp.MustCompile(`ZOOMIES_[A-Z0-9_]+`).FindAllString(page, -1) {
		named[name] = true
	}
	if len(named) == 0 {
		t.Fatal("the demo page names no ZOOMIES_ variable at all; the block moved, not the page")
	}

	var unknown []string
	for name := range named {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		t.Errorf("docs/demo.md names variables the controller does not read, which it would ignore without a word:\n  %s",
			strings.Join(unknown, "\n  "))
	}
}

// The demo keeps authentication off on loopback, and the page says so.
//
// With authentication off, whoever can reach the port is an administrator, and
// the block is a thing people paste into whatever shell they have open. It is
// safe to paste only because it binds loopback and sets no external URL, and it
// stays safe only if the page keeps saying that is why. A reader who copies the
// block onto a public bind has been told, on the same page, not to.
func TestTheDemoBlockKeepsAuthenticationOffOnLoopbackAndThePageSaysSo(t *testing.T) {
	page, block := demoBlock(t)
	env, _ := demoController(t, block)

	if off, err := strconv.ParseBool(env["ZOOMIES_DISABLE_AUTH"]); err != nil || !off {
		t.Error("the block no longer disables authentication; the page below it says it is off, and the first screen would be a setup token instead of a fleet")
	}
	host, _, err := net.SplitHostPort(env["ZOOMIES_BIND"])
	if err != nil || !config.LoopbackHost(host) {
		t.Errorf("ZOOMIES_BIND=%q is not a loopback address; with authentication off, that is an administrator console for everyone who can reach it", env["ZOOMIES_BIND"])
	}
	// Authentication off behind an external URL is refused by the validator, so
	// the validation test would fail too; this says why in the page's terms.
	for _, name := range []string{"ZOOMIES_EXTERNAL_URL", "ZOOMIES_TRUSTED_PROXIES"} {
		if _, set := env[name]; set {
			t.Errorf("the block sets %s, which tells the controller something forwards to it, and it refuses to run without authentication behind that", name)
		}
	}

	lower := strings.ToLower(page)
	for _, say := range []string{"authentication is off", "loopback"} {
		if !strings.Contains(lower, say) {
			t.Errorf("docs/demo.md no longer says %q, which is what keeps the block from being copied onto a public address", say)
		}
	}
}

// The page's prose agrees with the block above it.
//
// Two facts are written twice -- the address to open and the file to delete --
// and each is the kind of edit that is made in one place: the port is changed
// for a clash on somebody's laptop, the database moved out of /tmp. The reader
// then opens an address nothing is listening on, or is told to delete a file
// that is not the one the demo wrote.
func TestTheDemoPageAgreesWithItsBlockAboutTheAddressAndTheFileToDelete(t *testing.T) {
	page, block := demoBlock(t)
	env, _ := demoController(t, block)

	_, port, err := net.SplitHostPort(env["ZOOMIES_BIND"])
	if err != nil {
		t.Fatalf("ZOOMIES_BIND=%q: %v", env["ZOOMIES_BIND"], err)
	}
	if open := "http://localhost:" + port; !strings.Contains(page, open) {
		t.Errorf("the block listens on port %s and the page does not tell the reader to open %s", port, open)
	}
	db := env["ZOOMIES_DB_PATH"]
	if remove := "rm -f " + db; !strings.Contains(page, remove) {
		t.Errorf("the block keeps the fleet in %s and the page does not tell the reader how to delete it (%q)", db, remove)
	}
}

// The block calls only what is there to be called.
//
// "--no-init" is what keeps the installer from starting `zoomies init` and its
// questions -- the very thing the page promises to skip -- and "controller" is
// the command the second line runs. Neither is checked by the docs build, and
// a renamed flag would leave a visitor in an interview they were told they
// would not meet.
func TestTheDemoBlockUsesAFlagTheInstallerHasAndACommandTheBinaryHas(t *testing.T) {
	_, block := demoBlock(t)
	lines := logicalLines(block)
	if len(lines) == 0 {
		t.Fatal("the demo block is empty")
	}
	if first := lines[0]; !strings.Contains(first, "https://zoomies.sh/install.sh | sh -s -- ") || !strings.Contains(first, "--no-init") {
		t.Errorf("the first line is %q; it should pipe the installer to sh with its arguments after `-s --`, and pass --no-init", first)
	}
	if !strings.Contains(readText(t, installScript), "--no-init)") {
		t.Error("install.sh has no --no-init option any more, so the demo's first line would start the interactive setup")
	}
	if !regexp.MustCompile(`\{"controller", group`).MatchString(readText(t, commandTable)) {
		t.Error("the binary has no `controller` command any more, so the demo's second line would fail")
	}
}

// The demo is offered where a visitor decides, not only where it is explained.
//
// It used to be one sentence in the middle of a paragraph, and a free way to
// look at the product that the page hides is a free way nobody takes. Each of
// these is a place somebody weighing up an install looks: the hero beside the
// command, the closing call to action, and the first step of the quick start.
func TestTheDemoIsOfferedFromTheHeroTheClosingCallToActionAndTheQuickStart(t *testing.T) {
	home := readText(t, homePage)

	// The hero is everything before the first section heading.
	hero, _, _ := strings.Cut(home, "\n## ")
	if !strings.Contains(hero, demoLink) {
		t.Error("the hero on docs/index.md does not link to the demo")
	}
	if !strings.Contains(hero, demoLabel) {
		t.Errorf("the hero's link to the demo no longer reads %q", demoLabel)
	}
	_, cta, found := strings.Cut(home, `class="zoomies-cta"`)
	if !found || !strings.Contains(cta, demoLink) {
		t.Error("the closing call to action on docs/index.md does not link to the demo")
	}

	// Step 1 is the stretch between its heading and the next.
	_, steps, _ := strings.Cut(readText(t, quickstartPage), "\n## 1. Install\n")
	step, _, _ := strings.Cut(steps, "\n## ")
	if !strings.Contains(step, demoLink) {
		t.Error("step 1 of the quick start does not link to the demo")
	}
}

// The demo page is in the navigation.
//
// llms.txt is written from the navigation (hooks/seo.py), and an assistant asked
// how to try Zoomies reads that file; the sitemap lists every page, so it needs
// nothing from here. A page left out of the navigation is also left out of the
// sidebar of every other page, which is where somebody who has just read the
// quick start would look for it.
func TestTheDemoPageIsInTheNavigationSoLlmsTxtListsIt(t *testing.T) {
	if !regexp.MustCompile(`(?m)^\s*- [^:\n]+: demo\.md\s*$`).MatchString(readText(t, mkdocsConfig)) {
		t.Error("mkdocs.yml has no navigation entry for demo.md")
	}
}
