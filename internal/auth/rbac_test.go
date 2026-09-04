package auth

import (
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// readVerbs are the actions that only look at something. Everything else
// changes the fleet, an account or a credential.
var readVerbs = map[string]bool{"read": true}

// TestEveryActionHasARole is the test that catches a new endpoint being added
// without anybody deciding who may call it: an action that is not in the RBAC
// table fails here rather than silently denying every request in production.
func TestEveryActionHasARole(t *testing.T) {
	actions := AllActions()
	if len(actions) == 0 {
		t.Fatal("AllActions is empty")
	}
	seen := map[string]bool{}
	for _, a := range actions {
		if !a.Known() {
			t.Errorf("%s is not in the RBAC table", a)
			continue
		}
		if role := a.MinRole(); !role.Valid() {
			t.Errorf("%s maps to %q, which is not a role", a, role)
		}
		res, verb := a.Resource(), a.Verb()
		if res == "" || verb == "" {
			t.Errorf("%s is not shaped like <resource>.<verb>", a)
		}
		if want := res + ":" + verb; a.Scope() != want {
			t.Errorf("%s.Scope() = %q; want %q", a, a.Scope(), want)
		}
		if seen[string(a)] {
			t.Errorf("%s is listed twice", a)
		}
		seen[string(a)] = true
	}

	// The actions the API surface promises. Losing one of these means an
	// endpoint has no policy behind it.
	for _, required := range []Action{
		ActionPoolsRead, ActionPoolsWrite, ActionPoolsDelete,
		ActionRunnersRead, ActionRunnersCreate, ActionRunnersDrain, ActionRunnersDelete,
		ActionJobsRead,
		ActionHostsRead, ActionHostsCordon, ActionHostsDelete,
		ActionInstallationsRead, ActionInstallationsWrite, ActionInstallationsDelete,
		ActionAuditRead,
		ActionUsersRead, ActionUsersWrite,
		ActionTokensRead, ActionTokensWrite,
		ActionSettingsRead, ActionSettingsWrite,
		ActionMetricsRead, ActionEventsRead, ActionLogsRead, ActionJoinsWrite,
	} {
		if !required.Known() {
			t.Errorf("%s is missing from the RBAC table", required)
		}
	}
}

// TestRoleAuthority walks the full action list for every role. A viewer must
// not be able to perform any write action -- that single assertion is what
// stops a new mutating endpoint from being given away for free.
func TestRoleAuthority(t *testing.T) {
	roles := []store.Role{store.RoleViewer, store.RoleOperator, store.RoleAdmin}
	for _, role := range roles {
		for _, a := range AllActions() {
			id := &Identity{Kind: KindUser, ID: "usr_1", Name: "test", Role: role}
			got := id.Can(a)
			want := role.AtLeast(a.MinRole())
			if got != want {
				t.Errorf("%s may do %s = %v; want %v (minimum role %s)", role, a, got, want, a.MinRole())
			}
			if role == store.RoleViewer && got && !readVerbs[a.Verb()] {
				t.Errorf("a viewer may perform the write action %s; every mutating action needs operator or admin", a)
			}
			if !got && Explain(id, a) == "" {
				t.Errorf("%s is denied %s with no explanation", role, a)
			}
			if got && Explain(id, a) != "" {
				t.Errorf("%s is allowed %s but Explain returned %q", role, a, Explain(id, a))
			}
		}
	}
}

func TestSecretsStayWithAdmins(t *testing.T) {
	// Reading a user list, a token list, a join token or the settings can
	// expose credentials or their metadata, so none of them is a viewer read.
	for _, a := range []Action{ActionUsersRead, ActionTokensRead, ActionJoinsRead, ActionSettingsRead} {
		if a.MinRole() != store.RoleAdmin {
			t.Errorf("%s needs %s; want admin", a, a.MinRole())
		}
	}
}

func TestUnknownActionIsDenied(t *testing.T) {
	admin := &Identity{Kind: KindUser, Role: store.RoleAdmin}
	if admin.Can("pools.nuke") {
		t.Error("an unknown action was allowed; authorisation must fail closed")
	}
	if msg := Explain(admin, "pools.nuke"); !strings.Contains(msg, "not an action") {
		t.Errorf("Explain for an unknown action = %q", msg)
	}
}

func TestNilIdentityIsDenied(t *testing.T) {
	var id *Identity
	if Allowed(id, ActionJobsRead) {
		t.Error("a nil identity was allowed to read jobs")
	}
	if msg := Explain(id, ActionJobsRead); !strings.Contains(msg, "signed in") {
		t.Errorf("Explain for a nil identity = %q", msg)
	}
}

func TestScopeNarrowing(t *testing.T) {
	cases := []struct {
		name   string
		scopes []string
		action Action
		want   bool
	}{
		{"empty scopes fall back to the role", nil, ActionPoolsDelete, true},
		{"exact match", []string{"pools:write"}, ActionPoolsWrite, true},
		{"other resource", []string{"pools:write"}, ActionRunnersDrain, false},
		{"other verb", []string{"pools:write"}, ActionPoolsDelete, false},
		{"acting implies reading", []string{"pools:write"}, ActionPoolsRead, true},
		{"reading does not imply acting", []string{"pools:read"}, ActionPoolsWrite, false},
		{"resource wildcard", []string{"runners:*"}, ActionRunnersDelete, true},
		{"resource wildcard is still one resource", []string{"runners:*"}, ActionPoolsWrite, false},
		{"global wildcard", []string{"*"}, ActionPoolsDelete, true},
		{"case and spacing are forgiven", []string{" Pools:Write "}, ActionPoolsWrite, true},
		{"several scopes", []string{"jobs:read", "runners:drain"}, ActionRunnersDrain, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := &Identity{Kind: KindToken, Name: "ci", Role: store.RoleOperator, Scopes: tc.scopes}
			if got := id.Can(tc.action); got != tc.want {
				t.Fatalf("Can(%s) with scopes %v = %v; want %v", tc.action, tc.scopes, got, tc.want)
			}
		})
	}
}

func TestScopesNeverWidenARole(t *testing.T) {
	// A viewer token asking for an admin scope is still a viewer.
	id := &Identity{Kind: KindToken, Name: "ci", Role: store.RoleViewer, Scopes: []string{"*"}}
	if id.Can(ActionUsersWrite) {
		t.Error("a wildcard scope let a viewer manage users; scopes narrow, they never widen")
	}
}

func TestExplainNamesWhatIsMissing(t *testing.T) {
	token := &Identity{Kind: KindToken, Name: "ci", Role: store.RoleViewer}
	msg := Explain(token, ActionPoolsWrite)
	if msg != "this action needs the operator role; your token has viewer" {
		t.Errorf("Explain = %q; want it to name the required role and the one held", msg)
	}

	scoped := &Identity{Kind: KindToken, Name: "ci", Role: store.RoleOperator, Scopes: []string{"jobs:read"}}
	msg = Explain(scoped, ActionPoolsWrite)
	if !strings.Contains(msg, `"pools:write"`) || !strings.Contains(msg, "jobs:read") {
		t.Errorf("Explain = %q; want it to name the missing scope and the ones held", msg)
	}

	user := &Identity{Kind: KindUser, Name: "alice", Role: store.RoleViewer}
	if msg := Explain(user, ActionUsersWrite); !strings.Contains(msg, "your account has viewer") {
		t.Errorf("Explain for a user = %q; want it to say 'account', not 'token'", msg)
	}
}

func TestValidateScopes(t *testing.T) {
	if err := ValidateScopes([]string{"pools:read", "runners:*", "*", ""}); err != nil {
		t.Errorf("ValidateScopes on valid input: %v", err)
	}
	err := ValidateScopes([]string{"pools:read", "pool:read"})
	if err == nil {
		t.Fatal("a misspelled scope was accepted")
	}
	if !strings.Contains(err.Error(), "pool:read") || !strings.Contains(err.Error(), "pools:read") {
		t.Errorf("the error should name the bad scope and list the valid ones: %v", err)
	}
}

func TestSystemAndAgentIdentities(t *testing.T) {
	if !SystemIdentity().Can(ActionRunnersCreate) {
		t.Error("the system identity cannot create runners, which is its whole job")
	}
	agent := AgentIdentity(&store.Host{ID: "host_1", Name: "builder"}, "10.0.0.5")
	if agent.Can(ActionPoolsWrite) {
		t.Error("an agent identity may edit pools; agents are not fleet operators")
	}
	if agent.Kind != KindAgent || agent.Name != "builder" {
		t.Errorf("agent identity = %+v", agent)
	}
}
