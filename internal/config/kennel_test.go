package config

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// A feature that reads other people's repositories does not start doing it
// because a release added the ability to, and the budget it is given is the one
// the documentation quotes.
func TestKennelClubIsOffByDefaultAndSpendsAFifthOfTheRequestLimit(t *testing.T) {
	k := Default().Kennel
	if k.AgentGuidance {
		t.Error("agent guidance reads are on without opting in")
	}
	if k.WorkflowChecks {
		t.Error("workflow content reads are on without opting in")
	}
	if k.RepositorySetup {
		t.Error("repository setup reads are on without opting in")
	}
	if k.Enabled {
		t.Error("Kennel Club is on in a configuration nobody has touched")
	}
	if k.Scope != KennelScopeServed {
		t.Errorf("scope = %q, want %q: the wider scope multiplies the requests", k.Scope, KennelScopeServed)
	}
	if k.RefreshInterval != 24*time.Hour {
		t.Errorf("refresh interval = %s, want a day", k.RefreshInterval)
	}
	if k.APIBudgetPercent != 20 {
		t.Errorf("budget = %d%%, want 20%%", k.APIBudgetPercent)
	}
	if len(k.DisabledChecks) != 0 {
		t.Errorf("checks turned off by default: %v", k.DisabledChecks)
	}
	for _, f := range Default().Validate() {
		if strings.HasPrefix(f.Code, "kennel.") {
			t.Errorf("the defaults raise %s: %s", f.Code, f.Title)
		}
	}
}

// All of it is live because the loop is always running and asks on every pass:
// a switch that waited for a restart would be no use to somebody who turned the
// feature on to try it and wants it off again now. Each key is the fleet's own
// to change, not the platform's.
func TestEveryKennelSettingIsLiveAndTheFleetsToChange(t *testing.T) {
	want := []string{
		"kennel.agent_guidance", "kennel.api_budget_percent", "kennel.disabled_checks", "kennel.enabled",
		"kennel.refresh_interval", "kennel.repository_setup", "kennel.scope", "kennel.workflow_checks",
	}
	var got []string
	for _, s := range Settings() {
		if s.Section() != "kennel" {
			continue
		}
		got = append(got, s.Key)
		if !s.Live {
			t.Errorf("%s waits for a restart", s.Key)
		}
		if s.Scope != ScopeInstance {
			t.Errorf("%s has scope %q, want the fleet's", s.Key, s.Scope)
		}
		if !strings.HasPrefix(s.Env, "ZOOMIES_KENNEL_") {
			t.Errorf("%s is overridden by %s", s.Key, s.Env)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("the kennel settings are %v, want %v", got, want)
	}
	if !slices.Contains(SectionOrder, "kennel") {
		t.Error("kennel is not in SectionOrder, so the Settings page would put it nowhere")
	}
}

// The Settings page offers the registry's choices and the validator accepts a
// list of its own, so a choice added to one and not the other would be offered
// to somebody, saved, and then refused at the next start. Each choice has to be
// one the validator lets through, and the two lists have to be the same two.
func TestEveryScopeTheSettingsPageOffersIsOneTheValidatorAccepts(t *testing.T) {
	s, ok := LookupSetting("kennel.scope")
	if !ok {
		t.Fatal("kennel.scope is not registered")
	}
	if want := []string{KennelScopeServed, KennelScopeInstallation}; !slices.Equal(s.Choices, want) {
		t.Errorf("the page offers %v, want %v", s.Choices, want)
	}
	for _, choice := range s.Choices {
		c := Default()
		if _, err := c.SetValueString("kennel.scope", choice); err != nil {
			t.Errorf("%q is offered and refused when saved: %v", choice, err)
		}
		if hasCode(c.Validate(), "kennel.scope") {
			t.Errorf("%q is offered and refused by the validator", choice)
		}
	}
}

// Each of these makes the setting do something other than what its owner
// wrote, so each is refused when it is saved. The boundary values are the other
// half of the table: a refusal that was one off would turn away the budget the
// documentation says is allowed.
func TestAKennelValueThatWouldDoSomethingElseIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*Kennel)
		code string // empty: no finding
	}{
		{"an unknown scope", func(k *Kennel) { k.Scope = "everything" }, "kennel.scope"},
		{"a refresh that never happens", func(k *Kennel) { k.RefreshInterval = 0 }, "kennel.refresh_interval"},
		{"a refresh a minute short of the floor", func(k *Kennel) { k.RefreshInterval = time.Hour - time.Minute }, "kennel.refresh_interval"},
		{"a refresh exactly at the floor", func(k *Kennel) { k.RefreshInterval = time.Hour }, ""},
		{"a budget of nothing", func(k *Kennel) { k.APIBudgetPercent = 0 }, "kennel.api_budget"},
		{"a budget one under the floor", func(k *Kennel) { k.APIBudgetPercent = KennelBudgetMinPercent - 1 }, "kennel.api_budget"},
		{"a budget exactly at the floor", func(k *Kennel) { k.APIBudgetPercent = KennelBudgetMinPercent }, ""},
		{"a budget exactly at the ceiling", func(k *Kennel) { k.APIBudgetPercent = KennelBudgetMaxPercent }, ""},
		{"a budget one over the ceiling", func(k *Kennel) { k.APIBudgetPercent = KennelBudgetMaxPercent + 1 }, "kennel.api_budget"},
		{"a misspelt area", func(k *Kennel) { k.DisabledChecks = []string{"exposur"} }, "kennel.unknown_check"},
		{"a code of an area that has no such check", func(k *Kennel) { k.DisabledChecks = []string{"exposure.nonsense"} }, "kennel.unknown_check"},
		{"a known area", func(k *Kennel) { k.DisabledChecks = []string{"exposure"} }, ""},
		{"a known check", func(k *Kennel) { k.DisabledChecks = []string{"capacity.unserved_label"} }, ""},
		{"one good name and one bad", func(k *Kennel) { k.DisabledChecks = []string{"capacity", "nope"} }, "kennel.unknown_check"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.set(&c.Kennel)
			var raised []string
			for _, f := range c.Validate() {
				if strings.HasPrefix(f.Code, "kennel.") {
					raised = append(raised, f.Code)
					if f.Severity != SeverityError {
						t.Errorf("%s is a %s, and an unusable value should stop the save", f.Code, f.Severity)
					}
				}
			}
			switch {
			case tc.code == "" && len(raised) > 0:
				t.Errorf("raised %v for a value that is allowed", raised)
			case tc.code != "" && !slices.Equal(raised, []string{tc.code}):
				t.Errorf("raised %v, want only %s", raised, tc.code)
			}
		})
	}
}

// The sentence is for somebody who typed a name and was refused: it says which
// name, and offers the names that would have worked, taken from the registry.
func TestAMisspeltCheckNameIsAnsweredWithTheNamesThatWouldWork(t *testing.T) {
	c := Default()
	c.Kennel.DisabledChecks = []string{"exposur", "capacity", "fork"}
	f := find(c.Validate(), "kennel.unknown_check")
	if f.Code == "" {
		t.Fatal("no finding for a misspelt name")
	}
	if f.Setting != "kennel.disabled_checks" {
		t.Errorf("the finding names %q, not the setting to change", f.Setting)
	}
	for _, want := range []string{`"exposur"`, `"fork"`, "exposure.fork_code_ran", "capacity"} {
		if !strings.Contains(f.Title+" "+f.Fix, want) {
			t.Errorf("the sentence does not mention %s: %q / %q", want, f.Title, f.Fix)
		}
	}
	if strings.Contains(f.Title, `"capacity"`) {
		t.Errorf("the good name is quoted as if it were wrong: %q", f.Title)
	}
}

// What is refused is wrong; what is merely untidy is the setting its owner
// meant. A capital letter in a scope is not worth a stopped save, and a repeat
// in a list is the same instruction twice.
func TestUntidyKennelValuesAreTidiedNotRefused(t *testing.T) {
	c := Default()
	c.Kennel.Scope = "  Installation "
	c.Kennel.DisabledChecks = []string{"Exposure", " ", "exposure", " capacity.unserved_label "}
	c.normalize()

	if c.Kennel.Scope != KennelScopeInstallation {
		t.Errorf("scope = %q", c.Kennel.Scope)
	}
	if want := []string{"exposure", "capacity.unserved_label"}; !slices.Equal(c.Kennel.DisabledChecks, want) {
		t.Errorf("checks = %q, want %q", c.Kennel.DisabledChecks, want)
	}
	if hasCode(c.Validate(), "kennel.scope") || hasCode(c.Validate(), "kennel.unknown_check") {
		t.Error("a tidied value was still refused")
	}

	empty := Default()
	empty.Kennel.Scope = ""
	empty.normalize()
	if empty.Kennel.Scope != KennelScopeServed {
		t.Errorf("an empty scope became %q, want the default", empty.Kennel.Scope)
	}
}

// config.Live copies a snapshot shallowly while another goroutine may still be
// ranging over the old one, so tidying a list must build a new one.
func TestTidyingCheckNamesNeverWritesIntoTheListItWasGiven(t *testing.T) {
	in := []string{"Exposure", "CAPACITY"}
	out := normaliseCheckNames(in)
	if in[0] != "Exposure" || in[1] != "CAPACITY" {
		t.Errorf("the caller's list was changed: %q", in)
	}
	if len(out) != 2 || out[0] != "exposure" || out[1] != "capacity" {
		t.Errorf("tidied list = %q", out)
	}
	if got := normaliseCheckNames(nil); got != nil {
		t.Errorf("tidying nothing gave %q, want nothing", got)
	}
}

// Zero is always a legal duration to the settings layer, because switching a
// timer off is usually a real answer. Here it is not, so the floor refuses the
// short values at the write and the validator refuses zero.
func TestTheRefreshIntervalCannotBeSetBelowAnHourThroughTheSettingsLayer(t *testing.T) {
	c := Default()
	if _, err := c.SetValueString("kennel.refresh_interval", "30m"); err == nil {
		t.Error("30m was accepted")
	} else if !strings.Contains(err.Error(), "1h") {
		t.Errorf("the refusal does not say what is allowed: %v", err)
	}
	if _, err := c.SetValueString("kennel.refresh_interval", "6h"); err != nil {
		t.Errorf("6h was refused: %v", err)
	}
	if c.Kennel.RefreshInterval != 6*time.Hour {
		t.Errorf("interval = %s", c.Kennel.RefreshInterval)
	}
	if _, err := c.SetValueString("kennel.scope", "elsewhere"); err == nil {
		t.Error("a scope that is not one of the choices was accepted")
	}
}

func TestKennelClubReadsItsEnvironmentOverrides(t *testing.T) {
	t.Setenv("ZOOMIES_KENNEL_ENABLED", "true")
	t.Setenv("ZOOMIES_KENNEL_SCOPE", "installation")
	t.Setenv("ZOOMIES_KENNEL_REFRESH_INTERVAL", "12h")
	t.Setenv("ZOOMIES_KENNEL_API_BUDGET_PERCENT", "35")
	t.Setenv("ZOOMIES_KENNEL_DISABLED_CHECKS", "capacity, exposure.fork_code_ran")

	c := Default()
	if err := c.applyEnv(); err != nil {
		t.Fatalf("applyEnv: %v", err)
	}
	c.normalize()
	k := c.Kennel
	if !k.Enabled || k.Scope != KennelScopeInstallation || k.RefreshInterval != 12*time.Hour || k.APIBudgetPercent != 35 {
		t.Errorf("the environment was not read: %+v", k)
	}
	if want := []string{"capacity", "exposure.fork_code_ran"}; !slices.Equal(k.DisabledChecks, want) {
		t.Errorf("checks = %q, want %q", k.DisabledChecks, want)
	}
	if fs := c.Validate(); hasCode(fs, "kennel.scope") || hasCode(fs, "kennel.api_budget") || hasCode(fs, "kennel.refresh_interval") || hasCode(fs, "kennel.unknown_check") {
		t.Errorf("a valid override was refused: %v", fs)
	}
}
