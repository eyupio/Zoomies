package kennel

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The set of codes is closed. Operators alert on them and waivers are keyed by
// them, so a code appears when a stage ships and does not change afterwards;
// this list is what a pull request that adds one has to edit.
func TestTheRegistryIsTheClosedSetOfCodes(t *testing.T) {
	want := []Code{

		"exposure.public_repo_on_fleet",
		"exposure.public_repo_weak_pool",
		"exposure.fork_code_ran",
		"exposure.target_event_ran",
		"exposure.target_checkout_pr_head",
		"capacity.unserved_label",
		"capacity.job_hit_default_limit",
		"capacity.matrix_exceeds_pool",
		"setup.readme",
		"setup.licence",
		"setup.security",
		"setup.contributing",
		"setup.code_of_conduct",
		"setup.issue_template",
		"setup.pull_request_template",
		"setup.codeowners",
		"setup.dependency_updates",
		"setup.workflows",
		"ci.no_timeout",
		"ci.no_concurrency",
		"ci.action_not_pinned",
		"token.permissions_unset",
		"ci.workflow_unreadable",
		"ci.pins_without_updater",
		"ci.label_unserved",
		"ci.secret_on_command_line",
	}
	var got []Code
	seen := map[Code]bool{}
	for _, c := range Checks() {
		if seen[c.Code] {
			t.Errorf("%s is registered twice", c.Code)
		}
		seen[c.Code] = true
		got = append(got, c.Code)
		if c.Code.Area() != c.Area {
			t.Errorf("%s: area %q is not the prefix of its code", c.Code, c.Area)
		}
		if c.Severity.rank() == 0 {
			t.Errorf("%s has no valid severity", c.Code)
		}
		if strings.TrimSpace(c.Detects) == "" || len(c.Needs) == 0 || c.eval == nil {
			t.Errorf("%s is missing its description, its sources or its judgement", c.Code)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("registry = %v, want %v", got, want)
	}
}

// The catalogue says what a check takes before anything has been read, and the
// evaluator asks for what it needs as it goes. The first is a list in the
// registry and the second is the check's own code, so the two are held equal here:
// a check that began reading a new source without the catalogue saying so would
// have an operator grant a permission for it only after the fact.
func TestWhatTheCatalogueSaysACheckReadsIsWhatItAsksFor(t *testing.T) {
	for i := range checks {
		c := &checks[i]
		snap := positives[c.Code]()
		r := c.eval(&snap)
		got := map[Source]bool{}
		for _, src := range r.extra {
			got[src] = true
		}
		want := map[Source]bool{}
		for _, src := range c.Conditional {
			want[src] = true
		}
		if len(got) != len(want) {
			t.Errorf("%s asks for %v once it applies, and the registry says %v", c.Code, r.extra, c.Conditional)
			continue
		}
		for src := range got {
			if !want[src] {
				t.Errorf("%s asks for %s once it applies, and the registry does not say so", c.Code, src)
			}
		}
		for _, src := range c.Needs {
			if want[src] {
				t.Errorf("%s lists %s as both always needed and conditional", c.Code, src)
			}
		}
	}
}

// A check no test can make fire is a check nobody has seen work.
func TestEveryCheckCanFire(t *testing.T) {
	for _, c := range Checks() {
		mk, ok := positives[c.Code]
		if !ok {
			t.Errorf("%s has no positive fixture", c.Code)
			continue
		}
		if _, fired := finding(Evaluate(mk(), Policy{}), c.Code); !fired {
			t.Errorf("%s did not fire on its own positive fixture", c.Code)
		}
	}
	for code := range positives {
		if _, ok := Lookup(code); !ok {
			t.Errorf("a fixture for %s, which is not a registered code", code)
		}
	}
}

var american = regexp.MustCompile(`(?i)\b(organization|behavior|recognize|authorization|authorize|color|license|catalog|center)\w*`)

// The voice rules apply to every public sentence: British spelling, and no
// literal em dash in text that is read in a terminal as often as in a browser.
// Each sentence says something and ends like one.
func TestEverySentenceSaysWhatHappenedAndWhatToDoInHouseStyle(t *testing.T) {
	for _, c := range Checks() {
		f, _ := finding(Evaluate(positives[c.Code](), Policy{}), c.Code)
		for name, text := range map[string]string{"title": f.Title, "detail": f.Detail, "fix": f.Fix, "detects": c.Detects, "catalogue fix": c.Fix, "verify": c.Verify} {
			if strings.TrimSpace(text) == "" {
				t.Errorf("%s has no %s", c.Code, name)
			}
			if american.MatchString(text) {
				t.Errorf("%s %s uses an American spelling: %q", c.Code, name, text)
			}
			if strings.ContainsAny(text, "—–") {
				t.Errorf("%s %s contains a dash character: %q", c.Code, name, text)
			}
			if name != "title" && !strings.HasSuffix(text, ".") {
				t.Errorf("%s %s does not end like a sentence: %q", c.Code, name, text)
			}
		}
		if f.Severity.rank() == 0 {
			t.Errorf("%s fired without a severity", c.Code)
		}
	}
}

func TestANameInDisabledChecksIsOnlyKnownIfItIsACodeOrAnArea(t *testing.T) {
	for _, ok := range []string{"exposure", "capacity", "exposure.fork_code_ran"} {
		if !KnownCodeOrArea(ok) {
			t.Errorf("%q is not known", ok)
		}
	}
	for _, bad := range []string{"", "exposur", "Exposure", "exposure.nonsense", "storage"} {
		if KnownCodeOrArea(bad) {
			t.Errorf("%q is known", bad)
		}
	}
}

// The validator quotes Names back to someone who misspelt a name, so every one
// it lists has to be one the evaluator will accept, and nothing the evaluator
// accepts may be missing from it. Areas come first because that is the answer
// to "how do I turn all of these off", which is the commoner question.
func TestTheNamesOfferedForDisabledChecksAreExactlyTheOnesAccepted(t *testing.T) {
	names := Names()
	seen := map[string]bool{}
	for _, name := range names {
		if !KnownCodeOrArea(name) {
			t.Errorf("Names offers %q, which KnownCodeOrArea refuses", name)
		}
		if seen[name] {
			t.Errorf("Names offers %q twice", name)
		}
		seen[name] = true
	}
	for _, c := range Checks() {
		for _, want := range []string{string(c.Code), string(c.Area)} {
			if !seen[want] {
				t.Errorf("Names does not offer %q", want)
			}
		}
	}
	if len(names) < 2 || names[0] != string(AreaExposure) {
		t.Errorf("Names should start with the areas in registry order, got %v", names)
	}
}

func TestEverySourceACheckReadsIsOneThePageCanName(t *testing.T) {
	for _, c := range Checks() {
		for _, src := range c.Needs {
			if src != SourceFleet && src.Permission() == "" {
				t.Errorf("%s needs %s, which names no permission to grant", c.Code, src)
			}
		}
	}
}

// The problems list says one thing for Kennel Club, counts the repositories with
// an exposure error, and links to the list narrowed to errors. Those agree only
// while exposure is the one area whose checks can be an error. A check that could
// be an error in another area must narrow that link by area and count its own,
// so the day one is added this fails, and says why, instead of the drawer
// counting fewer repositories than the page it sends you to lists.
func TestOnlyExposureChecksCanBeErrors(t *testing.T) {
	for _, c := range Checks() {
		if c.Severity == SeverityError && c.Area != AreaExposure {
			t.Errorf("%s can be an error in the %s area: the kennel.exposure problem counts only exposure errors and links to every error (decision 0009)", c.Code, c.Area)
		}
	}
	// What escalation raises is exposure too: it only ever makes an exposure
	// warning an error.
	for _, c := range Checks() {
		if c.Code == CodePublicRepoOnFleet && c.Area != AreaExposure {
			t.Errorf("%s is raised to an error by escalate and is not in the exposure area", c.Code)
		}
	}
}

// A check's catalogue entry has to tell somebody what to change and how to see
// that it worked, because the catalog an agent fetches is built from exactly
// these sentences and an agent that reads "detects" alone will invent the rest.
func TestEveryCheckSaysHowToFixAndVerifyIt(t *testing.T) {
	for _, c := range Checks() {
		for name, text := range map[string]string{"fix": c.Fix, "verify": c.Verify} {
			if strings.TrimSpace(text) == "" {
				t.Errorf("%s: %s is empty", c.Code, name)
				continue
			}
			if !strings.HasSuffix(text, ".") {
				t.Errorf("%s: %s does not end a sentence: %q", c.Code, name, text)
			}
			if len(text) > 240 {
				t.Errorf("%s: %s is %d characters; keep it under 240", c.Code, name, len(text))
			}
		}
		want := "kennel-club.md#" + strings.ReplaceAll(string(c.Code), ".", "-")
		if c.Docs != want {
			t.Errorf("%s: docs = %q, want %q", c.Code, c.Docs, want)
		}
	}
}
