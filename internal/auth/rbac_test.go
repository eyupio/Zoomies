package auth

import (
	"errors"
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
		ActionRunnersRead, ActionRunnersDrain, ActionRunnersDelete,
		ActionJobsRead,
		ActionJobsCancel, ActionProvisioningWrite,
		ActionHostsRead, ActionHostsCordon, ActionHostsDelete, ActionHostsAccept, ActionHostsCheck,
		ActionInstallationsRead, ActionInstallationsWrite, ActionInstallationsDelete,
		ActionAuditRead,
		ActionUsersRead, ActionUsersWrite,
		ActionTokensRead, ActionTokensWrite,
		ActionSettingsRead, ActionSettingsWrite,
		ActionMetricsRead, ActionEventsRead, ActionLogsRead, ActionJoinsWrite,
		ActionDiagnosticsRead, ActionUpdatesRead,
	} {
		if !required.Known() {
			t.Errorf("%s is missing from the RBAC table", required)
		}
	}
}

// ownCredentials are the write actions a viewer holds because they act on
// the viewer's own credentials and nothing of the fleet's: a token minted this
// way carries no more than the viewer does.
var ownCredentials = map[Action]bool{ActionTokensOwn: true}

// ownershipChecked are write actions a viewer holds only as the coarse gate:
// every handler behind them also requires the caller to own what they change
// (context.manage: an installation an administrator delegated; context.publish:
// membership of the repository and, for a connection, its owner's consent to
// write there), so the role
// alone reaches nothing. Adding to this list is a security decision; the route
// walk in internal/api shows which handlers carry the second check.
var ownershipChecked = map[Action]bool{ActionContextManage: true, ActionContextPublish: true}

// TestRoleAuthority walks the full action list for every role. A viewer must
// not be able to perform any write action -- that single assertion is what
// stops a new mutating endpoint from being given away for free.
func TestRoleAuthority(t *testing.T) {
	roles := []store.Role{store.RoleViewer, store.RoleOperator, store.RoleAdmin, store.RolePlatform}
	for _, role := range roles {
		for _, a := range AllActions() {
			id := &Identity{Kind: KindUser, ID: "usr_1", Name: "test", Role: role}
			got := id.Can(a)
			want := role.AtLeast(a.MinRole())
			if got != want {
				t.Errorf("%s may do %s = %v; want %v (minimum role %s)", role, a, got, want, a.MinRole())
			}
			if role == store.RoleViewer && got && !readVerbs[a.Verb()] && !ownCredentials[a] && !ownershipChecked[a] {
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
	// The bundle is on that list because it contains the settings section: a
	// document assembled from admin-only material does not become viewer
	// material by being assembled.
	for _, a := range []Action{ActionUsersRead, ActionTokensRead, ActionJoinsRead, ActionSettingsRead, ActionDiagnosticsRead} {
		if a.MinRole() != store.RoleAdmin {
			t.Errorf("%s needs %s; want admin", a, a.MinRole())
		}
	}
}

// What an update would take is read out of this controller's own state and
// names nothing of the fleet's, so every role reads it: the operator who has to
// explain why a release has not been taken is the viewer's colleague, and a
// panel that hid the reason from half of them would be no use to either. The
// scope is a separate matter, and a token minted for something else does not
// reach it.
func TestEveryRoleMayReadWhatAnUpdateWouldTake(t *testing.T) {
	if got := ActionUpdatesRead.MinRole(); got != store.RoleViewer {
		t.Errorf("%s needs %s; want viewer", ActionUpdatesRead, got)
	}
	for _, role := range []store.Role{store.RoleViewer, store.RoleOperator, store.RoleAdmin, store.RolePlatform} {
		id := &Identity{Kind: KindUser, ID: "usr_1", Name: "test", Role: role}
		if !id.Can(ActionUpdatesRead) {
			t.Errorf("a %s may not read the update status", role)
		}
	}

	elsewhere := &Identity{Kind: KindToken, Name: "ci", Role: store.RolePlatform, Scopes: []string{"jobs:read"}}
	if elsewhere.Can(ActionUpdatesRead) {
		t.Error("a token scoped to jobs:read may read the update status")
	}
	if msg := Explain(elsewhere, ActionUpdatesRead); !strings.Contains(msg, `"updates:read"`) {
		t.Errorf("Explain = %q; want it to name the updates:read scope the token is missing", msg)
	}
	reads := &Identity{Kind: KindToken, Name: "dashboard", Role: store.RoleViewer, Scopes: []string{"updates:read"}}
	if !reads.Can(ActionUpdatesRead) {
		t.Error("a token scoped to updates:read may not read the update status")
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
	if !SystemIdentity().Can(ActionRunnersDelete) {
		t.Error("the system identity cannot remove runners, which is half its job")
	}
	agent := AgentIdentity(&store.Host{ID: "host_1", Name: "builder"}, "10.0.0.5")
	if agent.Can(ActionPoolsWrite) {
		t.Error("an agent identity may edit pools; agents are not fleet operators")
	}
	if agent.Kind != KindAgent || agent.Name != "builder" {
		t.Errorf("agent identity = %+v", agent)
	}
}

// A scope no route checks is one an operator can grant believing it does
// something. runners.create was such a scope: runners are created by the
// scheduler when a job queues, and no endpoint has ever asked for it.
func TestEveryActionIsOneSomethingChecks(t *testing.T) {
	for action := range actionRoles {
		if strings.TrimSpace(string(action)) == "" {
			t.Error("an empty action is in the role table")
		}
		if action == "runners.create" {
			t.Error("runners.create is back; no route checks it, so granting it means nothing")
		}
	}
}

// A token hands authority on, so it must not hand on more than its maker has:
// a leaked token narrowed to one resource would otherwise be one request away
// from an unscoped one that never expires.
func TestATokenCannotMintMoreThanItsMakerHolds(t *testing.T) {
	tests := []struct {
		name   string
		by     *Identity
		role   store.Role
		scopes []string
		wantOK bool
	}{
		{"a user mints anything up to their role", &Identity{Kind: KindUser, Role: store.RoleAdmin}, store.RoleAdmin, nil, true},
		{"an operator cannot mint an admin", &Identity{Kind: KindUser, Role: store.RoleOperator}, store.RoleAdmin, nil, false},
		{"an operator can mint a viewer", &Identity{Kind: KindUser, Role: store.RoleOperator}, store.RoleViewer, nil, true},
		{"a scoped token cannot mint an unscoped one", &Identity{Kind: KindToken, Role: store.RoleAdmin, Scopes: []string{"tokens:*"}}, store.RoleAdmin, nil, false},
		{"a scoped token cannot reach past its scopes", &Identity{Kind: KindToken, Role: store.RoleAdmin, Scopes: []string{"tokens:*"}}, store.RoleAdmin, []string{"pools:write"}, false},
		{"a scoped token can mint within its scopes", &Identity{Kind: KindToken, Role: store.RoleAdmin, Scopes: []string{"pools:*"}}, store.RoleAdmin, []string{"pools:write"}, true},
		{"a resource scope covers reading it", &Identity{Kind: KindToken, Role: store.RoleAdmin, Scopes: []string{"pools:write"}}, store.RoleViewer, []string{"pools:read"}, true},
		{"a wildcard is only minted by a wildcard", &Identity{Kind: KindToken, Role: store.RoleAdmin, Scopes: []string{"pools:*"}}, store.RoleViewer, []string{"*"}, false},
		{"a wildcard covers everything", &Identity{Kind: KindToken, Role: store.RoleAdmin, Scopes: []string{"*"}}, store.RoleAdmin, []string{"hosts:write"}, true},
		{"nobody mints nothing", nil, store.RoleViewer, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := MintWithin(tc.by, tc.role, tc.scopes)
			if tc.wantOK && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !tc.wantOK && err == nil {
				t.Fatal("allowed a token to carry more than its maker")
			}
			if err != nil && !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("a refusal has to be something the API answers 422 to, got %v", err)
			}
		})
	}
}

// The fence and the backups moved above the fleet's administrator. A backup
// is the whole database under the key this host holds, and lifting the fence
// decides whether a restored instance acts on the world again; on an instance
// operated by one team for another, neither is the fleet's to take.
func TestTheFenceAndTheBackupsNeedThePlatformRole(t *testing.T) {
	moved := []Action{
		ActionRecoveryWrite,
		ActionBackupsRead, ActionBackupsWrite, ActionBackupsRestore,
	}
	admin := &Identity{Kind: KindUser, ID: "usr_a", Name: "alex", Role: store.RoleAdmin}
	platform := &Identity{Kind: KindUser, ID: "usr_p", Name: "pat", Role: store.RolePlatform}

	for _, a := range moved {
		if a.MinRole() != store.RolePlatform {
			t.Errorf("%s needs %s; want platform", a, a.MinRole())
		}
		if admin.Can(a) {
			t.Errorf("an administrator may still %s", a)
		}
		if !platform.Can(a) {
			t.Errorf("a platform account may not %s", a)
		}
		// A refusal an operator cannot act on is a support ticket.
		if msg := Explain(admin, a); !strings.Contains(msg, "platform") {
			t.Errorf("refusing %s to an admin says %q, which does not name the role they are missing", a, msg)
		}
	}
}

// Everything else an administrator could do, they still can. This is the
// guard against the change reaching further than the four actions above.
func TestNothingElseMovedOutOfReachOfAnAdministrator(t *testing.T) {
	moved := map[Action]bool{
		ActionRecoveryWrite: true,
		ActionBackupsRead:   true, ActionBackupsWrite: true, ActionBackupsRestore: true,
	}
	admin := &Identity{Kind: KindUser, ID: "usr_a", Name: "alex", Role: store.RoleAdmin}
	for _, a := range AllActions() {
		if moved[a] {
			continue
		}
		if !admin.Can(a) {
			t.Errorf("an administrator may no longer %s, and this change was not meant to touch it", a)
		}
	}
}

// With authentication off there is no second audience: whoever reaches the
// socket has the process. An instance started that way -- `make dev`, the
// browser suite, a loopback trial -- must not answer 403 on its own backups.
func TestWithAuthenticationOffEverythingIsReachable(t *testing.T) {
	dev := DevIdentity("127.0.0.1")
	for _, a := range AllActions() {
		if !dev.Can(a) {
			t.Errorf("with authentication disabled, %s is refused", a)
		}
	}
}
