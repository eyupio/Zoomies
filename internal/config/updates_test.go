package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// An upgrade does not start updating a fleet because the release it installed
// learned how to: the mode is off until somebody chooses otherwise, and the wait
// that protects the unattended mode is already a day by the time anyone gets
// there. A fresh install raises nothing about a feature nobody asked for, or the
// list would teach people to stop reading it.
func TestUpdatesDefaultToOffWithADaySoak(t *testing.T) {
	u := Default().Updates
	if u.Mode != "off" {
		t.Errorf("mode = %q, want off: replacing the running binary is a choice, never a default", u.Mode)
	}
	if u.Soak != 24*time.Hour {
		t.Errorf("soak = %s, want a day", u.Soak)
	}
	if u.CheckInterval != 24*time.Hour {
		t.Errorf("check interval = %s, want a day", u.CheckInterval)
	}
	for _, f := range Default().Validate() {
		if strings.HasPrefix(f.Code, "updates.") {
			t.Errorf("the defaults raise %s: %s", f.Code, f.Title)
		}
	}
}

// The environment has always lower-cased an enum, so the same word typed into
// zoomies.yaml has to mean the same thing rather than stop the controller
// starting over a capital letter.
func TestUpdatesModeFromYAMLIsLowerCasedAndTrimmed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zoomies.yaml")
	if err := os.WriteFile(path, []byte("updates:\n  mode: \" Auto \"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Updates.Mode != "auto" {
		t.Fatalf("mode = %q, want auto", c.Updates.Mode)
	}
	if hasCode(c.Validate(), "updates.mode") {
		t.Error("a mode that only needed tidying was refused")
	}

	// A blank is not a choice of anything, and the one safe thing it can mean is
	// the default: a template that renders an empty value must not turn updating
	// on, or stop the controller starting.
	if err := os.WriteFile(path, []byte("updates:\n  mode: \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Updates.Mode != "off" {
		t.Errorf("an empty mode became %q, want off", c.Updates.Mode)
	}
}

// The refusal quotes the choices because the person reading it has just typed
// one that does not exist -- "automatic" is the likeliest -- and a finding that
// only says "unknown" sends them to the documentation to learn what they could
// have typed.
func TestAnUnknownUpdatesModeIsAnErrorThatNamesTheChoices(t *testing.T) {
	c := Default()
	c.Updates.Mode = "automatic"
	f := find(c.Validate(), "updates.mode")
	if f.Code == "" {
		t.Fatal("an unknown mode raised no finding")
	}
	if f.Severity != SeverityError || f.Setting != "updates.mode" {
		t.Errorf("finding = %+v, want an error about updates.mode", f)
	}
	if !strings.Contains(f.Title, `"automatic"`) {
		t.Errorf("the title does not quote what was written: %q", f.Title)
	}
	for _, choice := range []string{"off", "manual", "auto"} {
		if !strings.Contains(f.Fix, choice) {
			t.Errorf("the fix does not name %q: %q", choice, f.Fix)
		}
	}
}

// The settings layer refuses a negative duration at a write, so a negative soak
// can only arrive from the file, and the file is what has to be tried. It is an
// error whatever the mode: a value that is wrong while updating is off is still
// wrong on the day somebody switches it on.
func TestANegativeSoakFromYAMLIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zoomies.yaml")
	if err := os.WriteFile(path, []byte("updates:\n  soak: -1h\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Updates.Mode != "off" {
		t.Fatalf("mode = %q; this case is about a mode that is off", c.Updates.Mode)
	}
	f := find(c.Validate(), "updates.soak_negative")
	if f.Code == "" {
		t.Fatal("a negative soak raised no finding")
	}
	if f.Severity != SeverityError || f.Setting != "updates.soak" || f.Fix == "" {
		t.Errorf("finding = %+v, want an error about updates.soak with a fix", f)
	}
}

// updates.check_interval is the air-gap switch. At zero the controller never
// learns that a release exists, so a mode that acts on one is a setting that
// quietly does nothing -- and that is worth a warning only when a mode asks for
// it. An air-gapped fleet that leaves the mode off is configured correctly.
func TestAModeWithoutAReleaseCheckIsAWarning(t *testing.T) {
	for _, mode := range []string{"manual", "auto"} {
		t.Run(mode, func(t *testing.T) {
			c := Default()
			c.Updates.Mode, c.Updates.CheckInterval = mode, 0
			f := find(c.Validate(), "updates.mode_without_check")
			if f.Code == "" {
				t.Fatalf("%s with the release check off raised no finding", mode)
			}
			if f.Severity != SeverityWarning || f.Setting != "updates.mode" || f.Fix == "" {
				t.Errorf("finding = %+v, want a warning about updates.mode with a fix", f)
			}
			if !strings.Contains(f.Fix, "updates.check_interval") {
				t.Errorf("the fix does not say how to give the mode a release to act on: %q", f.Fix)
			}
		})
	}

	c := Default()
	c.Updates.Mode, c.Updates.CheckInterval = "off", 0
	if hasCode(c.Validate(), "updates.mode_without_check") {
		t.Error("an air-gapped fleet with updating off was warned about its release check")
	}
	c = Default()
	c.Updates.Mode = "manual"
	if hasCode(c.Validate(), "updates.mode_without_check") {
		t.Error("a mode beside a working release check was warned about it")
	}
}

// Unattended updating is never silent: the rule in this package's CLAUDE.md is
// that a default which surprises people gets an info finding, and a controller
// that replaces itself with nobody pressing anything is the largest surprise on
// offer. manual has somebody pressing a button, so it has nothing to say.
func TestAutoRaisesAnInfoFindingNamingTheSetting(t *testing.T) {
	c := Default()
	c.Updates.Mode = "auto"
	f := find(c.Validate(), "updates.auto")
	if f.Code == "" {
		t.Fatal("auto raised no finding")
	}
	if f.Severity != SeverityInfo || f.Setting != "updates.mode" {
		t.Errorf("finding = %+v, want an info finding naming updates.mode", f)
	}
	if f.Title != strings.ToLower(f.Title) || strings.HasSuffix(f.Title, ".") {
		t.Errorf("an info title is lower case with no full stop: %q", f.Title)
	}

	c.Updates.Mode = "manual"
	for _, f := range c.Validate() {
		if strings.HasPrefix(f.Code, "updates.") {
			t.Errorf("manual raises %s: %s", f.Code, f.Title)
		}
	}
}

// The soak is the only wait between a release being published and every host
// running it, so removing it from the unattended mode is said out loud. It
// applies to auto alone -- pressing the button is the soak for manual -- so a
// zero beside any other mode is a legitimate answer and raises nothing.
func TestAutoWithNoSoakIsAWarning(t *testing.T) {
	c := Default()
	c.Updates.Mode, c.Updates.Soak = "auto", 0
	f := find(c.Validate(), "updates.auto_without_soak")
	if f.Code == "" {
		t.Fatal("auto with no soak raised no finding")
	}
	if f.Severity != SeverityWarning || f.Setting != "updates.soak" || f.Fix == "" {
		t.Errorf("finding = %+v, want a warning about updates.soak with a fix", f)
	}

	for _, tc := range []struct {
		name string
		mode string
		soak time.Duration
	}{
		{"auto with a soak", "auto", 24 * time.Hour},
		{"manual with none, because pressing the button is the soak", "manual", 0},
		{"off with none", "off", 0},
	} {
		c := Default()
		c.Updates.Mode, c.Updates.Soak = tc.mode, tc.soak
		if hasCode(c.Validate(), "updates.auto_without_soak") {
			t.Errorf("%s raised updates.auto_without_soak", tc.name)
		}
	}
}

// Both are the platform's: whether this process may replace its own binary is a
// decision about the machine it runs on, not about the fleet's runners. And both
// are live, because an operator who switches updating off has to find it off on
// the next pass and not after a restart -- which an update would itself be.
func TestTheUpdateModeAndSoakArePlatformSettingsThatApplyAtOnce(t *testing.T) {
	for key, env := range map[string]string{
		"updates.mode": "ZOOMIES_UPDATE_MODE",
		"updates.soak": "ZOOMIES_UPDATE_SOAK",
	} {
		s, ok := LookupSetting(key)
		if !ok {
			t.Errorf("%s is not registered, so there is no way to change it on the Settings page", key)
			continue
		}
		if s.Scope != ScopePlatform || !s.Platform() {
			t.Errorf("%s has scope %q, want the platform's", key, s.Scope)
		}
		if !s.Live {
			t.Errorf("%s waits for a restart", key)
		}
		if s.Env != env {
			t.Errorf("%s is overridden by %s, want %s", key, s.Env, env)
		}
		if s.Section() != "updates" {
			t.Errorf("%s is in section %q, so it would not sit beside updates.check_interval", key, s.Section())
		}
	}
}

// The Settings page offers the registry's choices and the validator accepts a
// list of its own, so a choice added to one and not the other would be offered,
// saved, and then refused at the next start. Each choice has to be one the
// validator lets through, and the two lists have to be the same.
func TestEveryUpdateModeTheSettingsPageOffersIsOneTheValidatorAccepts(t *testing.T) {
	s, ok := LookupSetting("updates.mode")
	if !ok {
		t.Fatal("updates.mode is not registered")
	}
	if s.Kind != KindEnum {
		t.Fatalf("updates.mode is a %s, want an enum, so the page draws a menu", s.Kind)
	}
	if want := []string{"off", "manual", "auto"}; !slices.Equal(s.Choices, want) {
		t.Errorf("the page offers %v, want %v", s.Choices, want)
	}
	for _, choice := range s.Choices {
		if !strings.Contains(s.Summary, choice) {
			t.Errorf("the summary does not say what %q does", choice)
		}
		c := Default()
		if _, err := c.SetValueString("updates.mode", choice); err != nil {
			t.Errorf("%q is offered and refused when saved: %v", choice, err)
		}
		if hasCode(c.Validate(), "updates.mode") {
			t.Errorf("%q is offered and refused by the validator", choice)
		}
	}

	// And the refusal reads as a sentence, which is why the label is what it is:
	// "an update mode" would read "a update mode".
	c := Default()
	_, err := c.SetValueString("updates.mode", "dev")
	if err == nil {
		t.Fatal("a mode that is not one of the choices was accepted")
	}
	for _, want := range []string{`"dev" is not a release update mode`, "off, manual or auto"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
}

// Zero is a real answer for a soak -- take a release the moment it is seen,
// which is what a fleet that runs its own staging first wants -- and no floor
// stands between an operator and a short one. The warning is the guard, not the
// settings layer.
func TestTheSoakHasNoFloorAndZeroIsAnAnswer(t *testing.T) {
	c := Default()
	for _, written := range []string{"0", "1m", "72h"} {
		if _, err := c.SetValueString("updates.soak", written); err != nil {
			t.Errorf("a soak of %s was refused: %v", written, err)
		}
	}
	if c.Updates.Soak != 72*time.Hour {
		t.Errorf("soak = %s after the last write, want 72h", c.Updates.Soak)
	}
	if _, err := c.SetValueString("updates.soak", "tomorrow"); err == nil {
		t.Error("a soak that is not a duration was accepted")
	}
}

// Parts of the update work that install something have not shipped, so every
// place that describes the modes says so in the same words. The sentence is one
// constant so that removing it, when the installing arrives, is one search; the
// docs quote it and are held to the same text here.
func TestEveryPlaceThatPromisesAnUpdateSaysThisReleaseOnlyShowsOne(t *testing.T) {
	mode, ok := LookupSetting("updates.mode")
	if !ok {
		t.Fatal("updates.mode is not a setting")
	}
	c := Default()
	c.Updates.Mode = "auto"
	auto := find(c.Validate(), "updates.auto")
	c.Updates.Mode = "weekly"
	invalid := find(c.Validate(), "updates.mode")

	for _, tc := range []struct{ where, text string }{
		{"the updates.mode summary", mode.Summary},
		{"the updates.auto detail", auto.Detail},
		{"the updates.mode fix", invalid.Fix},
	} {
		if !strings.Contains(tc.text, updatesNotInstalledYet) {
			t.Errorf("%s = %q, want it to say %q", tc.where, tc.text, updatesNotInstalledYet)
		}
	}
	if strings.Contains(auto.Title, updatesNotInstalledYet) {
		t.Errorf("the updates.auto title carries the clause; a title is short, so it belongs in the detail")
	}

	for _, page := range []string{"configuration.md", "problem-codes.md"} {
		body, err := os.ReadFile(filepath.Join("..", "..", "docs", page))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(string(body), updatesNotInstalledYet); got < 1 {
			t.Errorf("docs/%s never says %q", page, updatesNotInstalledYet)
		}
	}
}

// "Once it has been public for 0s" reads as a wait, when the whole point of a
// soak of nothing is that there is none.
func TestTheAutoNoticeDoesNotCallNoSoakAWait(t *testing.T) {
	c := Default()
	c.Updates.Mode, c.Updates.Soak = "auto", 0
	got := find(c.Validate(), "updates.auto").Detail
	if strings.Contains(got, "0s") || !strings.Contains(got, "as soon as it is seen") {
		t.Errorf("detail = %q, want the release taken as soon as it is seen and no 0s", got)
	}

	c.Updates.Soak = 36 * time.Hour
	if got := find(c.Validate(), "updates.auto").Detail; !strings.Contains(got, "once it has been public for 36h") {
		t.Errorf("detail = %q, want the wait named when there is one", got)
	}
}
