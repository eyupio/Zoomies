package config

import (
	"slices"
	"testing"
)

// The split between the two audiences is a judgement about each key, and the
// judgement is the thing worth holding: a key is the platform's when changing
// it changes what the process binds, trusts, stores, logs or dials from its
// own machine, or how much of that machine it spends. A key added to one of
// these sections without a thought about which audience owns it lands in the
// wrong one, and the first person to find out is an operator who cannot
// change something they could change yesterday.
func TestEverySettingBelongsToTheAudienceItsSectionSays(t *testing.T) {
	platformSections := []string{"server", "security", "log", "backup", "retention", "limits", "updates", "capacity_demand", "status"}
	// The embedded agent is the controller's own half of agent.*: where this
	// process puts its work and which daemon it dials. The rest of agent.*
	// sizes and labels the fleet's runners.
	platformAgentKeys := map[string]bool{
		"agent.embedded": true, "agent.backend": true,
		"agent.work_dir": true, "agent.docker_host": true,
	}

	for _, st := range Settings() {
		// Bootstrap and local are settled by where they are read, not by
		// whose they are.
		if st.Scope == ScopeBootstrap || st.Scope == ScopeLocal {
			continue
		}
		section := st.Section()
		want := false
		switch {
		case slices.Contains(platformSections, section):
			want = true
		case st.Key == "metrics.public":
			want = true
		case platformAgentKeys[st.Key]:
			want = true
		}
		if got := st.Platform(); got != want {
			whose := map[bool]string{true: "the platform's", false: "the fleet's"}
			t.Errorf("%s is %s, and should be %s", st.Key, whose[got], whose[want])
		}
	}
}

// The scope says who may change a key, not where it lives. If a platform key
// stopped being stored, the export, the import and the file seed would each
// quietly start carrying only half an instance's configuration.
func TestAPlatformSettingIsStoredLikeAnyOther(t *testing.T) {
	var platform int
	for _, st := range Settings() {
		if !st.Platform() {
			continue
		}
		platform++
		if !st.Stored() {
			t.Errorf("%s is the platform's and is not stored; the export would leave it behind", st.Key)
		}
	}
	if platform == 0 {
		t.Fatal("no setting is platform-scoped, so this proves nothing")
	}

	// StoredSettings is what the database layer and the export walk, and it
	// has to carry both audiences.
	var sawPlatform, sawFleet bool
	for _, st := range StoredSettings() {
		if st.Platform() {
			sawPlatform = true
		} else {
			sawFleet = true
		}
	}
	if !sawPlatform || !sawFleet {
		t.Errorf("StoredSettings carries platform=%v fleet=%v; it must carry both", sawPlatform, sawFleet)
	}
}

// A reader of the settings page sees the scope as a word. Keeping the set
// closed means the UI and the OpenAPI enum can be trusted to list them all.
func TestEverySettingHasAKnownScope(t *testing.T) {
	known := map[Scope]bool{ScopeInstance: true, ScopePlatform: true, ScopeBootstrap: true, ScopeLocal: true}
	for _, st := range Settings() {
		if !known[st.Scope] {
			t.Errorf("%s has scope %q, which nothing knows how to render", st.Key, st.Scope)
		}
	}
}
