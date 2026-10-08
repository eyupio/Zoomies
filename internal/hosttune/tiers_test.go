package hosttune

import (
	"context"
	"strings"
	"testing"
)

func TestKernelChecksAppearInEveryTierAndCompareVersions(t *testing.T) {
	e, f := fixture()
	f.commands["uname -r"] = "6.5.0-1-generic"
	f.put("/lib/modules/6.8.0-2-generic/test", "")
	for _, tier := range []Tier{Safe, Aggressive, Dedicated} {
		r := e.Run(context.Background(), tier)
		if !r.RebootPending {
			t.Fatalf("%s missed reboot", tier)
		}
		found := false
		for _, x := range r.Results {
			if x.ID == "kernel.running" && x.Status == Warn {
				found = true
			}
		}
		if !found {
			t.Fatal("missing kernel check")
		}
	}
	if versionCompare("6.10.0", "6.8.0") <= 0 {
		t.Fatal("lexical kernel comparison")
	}
}
func TestDedicatedDependencyRefusalAndSnapGuard(t *testing.T) {
	e, f := fixture()
	f.commands["systemctl list-dependencies --reverse --all --plain --no-pager ModemManager.service"] = "ModemManager.service\nother.service"
	if !strings.Contains(unitGuard(context.Background(), e, "ModemManager.service"), "other.service") {
		t.Fatal("outside dependency accepted")
	}
	f.commands["snap list"] = "Name Version\ncore22 1\napplication 2"
	if !strings.Contains(unitGuard(context.Background(), e, "snapd.service"), "non-core") {
		t.Fatal("snap dependency accepted")
	}
}
func TestAptTimersRequireAnImplementedSecurityUpdateWindow(t *testing.T) {
	e, _ := fixture()
	for _, u := range []string{"apt-daily.timer", "apt-daily-upgrade.timer"} {
		if !strings.Contains(unitGuard(context.Background(), e, u), "maintenance") {
			t.Fatal("timer unguarded")
		}
	}
}
func TestDedicatedRequiresConsentAndNeverLeaksIntoSafe(t *testing.T) {
	e, f := fixture()
	for _, r := range e.Run(context.Background(), Safe).Results {
		if r.Tier == Dedicated {
			t.Fatal("dedicated default")
		}
	}
	if e.Tune(context.Background(), TuneOptions{Dedicated: true, In: strings.NewReader("yes\n")}) == nil {
		t.Fatal("weak dedicated confirmation")
	}
	if f.writes != 0 {
		t.Fatal("unconfirmed mutation")
	}
}
func TestHWERejectsThirdPartyPackagesAndUpgrades(t *testing.T) {
	for _, plan := range []string{"Inst linux-image [old] (new Ubuntu:24.04/noble)", "Inst linux-image (new PPA:kernel)", "Remv linux-image-old"} {
		e, f := fixture()
		f.commands["apt-get --simulate --no-install-recommends install linux-generic-hwe-24.04"] = plan
		if _, err := planHWE(context.Background(), e, Result{ID: "kernel.hwe-install"}); err == nil {
			t.Fatalf("accepted %s", plan)
		}
	}
}
func TestEveryDedicatedCheckHasAnIsolatedDetectionPath(t *testing.T) {
	for _, c := range dedicatedChecks() {
		t.Run(c.ID, func(t *testing.T) {
			e, f := fixture()
			if c.Detect(context.Background(), e).Status == "" {
				t.Fatal("missing status")
			}
			if f.writes != 0 {
				t.Fatal("detection writes")
			}
		})
	}
}

func TestDedicatedCompanionSocketIsRecordedAndGuarded(t *testing.T) {
	e, f := fixture()
	for _, u := range []string{"multipathd.service", "multipathd.socket"} {
		f.commands["systemctl show "+u+" --property=LoadState --property=UnitFileState --property=ActiveState"] = "LoadState=loaded\nUnitFileState=enabled\nActiveState=active"
		f.commands["systemctl list-dependencies --reverse --all --plain --no-pager "+u] = u
	}
	c, err := e.Plan(context.Background(), Result{ID: "service.multipathd", Recommended: "disabled and masked"})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Units) != 2 {
		t.Fatal("companion state missing")
	}
	c.Phase = "applied"
	if e.verifyRevert(context.Background(), c) == nil {
		t.Fatal("later unit edits were ignored")
	}
	for _, u := range c.Units {
		f.commands["systemctl show "+u.Name+" --property=LoadState --property=UnitFileState --property=ActiveState"] = "LoadState=loaded\nUnitFileState=masked\nActiveState=inactive"
	}
	if err := e.verifyRevert(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	f.commands["systemctl list-dependencies --reverse --all --plain --no-pager multipathd.socket"] = "multipathd.socket\nbackup.service"
	if reason := familyGuard(context.Background(), e, "multipathd.service"); !strings.Contains(reason, "backup.service") {
		t.Fatal("socket dependent not guarded")
	}
}
func TestCompletedCloudInitAcceptsCompactJSON(t *testing.T) {
	e, f := fixture()
	f.commands["cloud-init status --format json"] = `{"status":"done"}`
	f.commands["systemctl list-dependencies --reverse --all --plain --no-pager cloud-init.service"] = "cloud-init.service"
	if reason := unitGuard(context.Background(), e, "cloud-init.service"); reason != "" {
		t.Fatal(reason)
	}
}

// A host Zoomies cannot tune (a container install, a non-root run) still needs
// to be told what to type, so the finding carries the command rather than only
// the name of a flag that would not work there.
func TestAnAvailableHWEKernelCarriesTheCommandToInstallIt(t *testing.T) {
	e, f := fixture()
	f.commands["apt-cache policy linux-generic-hwe-24.04"] = "linux-generic-hwe-24.04:\n  Installed: (none)\n  Candidate: 6.14.0.1\n"
	r := detectHWE(context.Background(), e)
	if r.Status != Warn || r.Command != "sudo apt-get install --no-install-recommends linux-generic-hwe-24.04" {
		t.Fatalf("got %s %q", r.Status, r.Command)
	}
	f.commands["dpkg-query -W -f=${Status} linux-generic-hwe-24.04"] = "install ok installed"
	if r := detectHWE(context.Background(), e); r.Command != "" {
		t.Fatalf("an installed kernel should not suggest installing it: %q", r.Command)
	}
}
