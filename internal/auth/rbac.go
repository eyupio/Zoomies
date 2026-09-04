package auth

import (
	"fmt"
	"slices"
	"strings"

	"github.com/eyupio/zoomies/internal/store"
)

// Action names one thing the HTTP API lets an identity do.
//
// Every route in docs/api-surface.md maps onto exactly one action, and
// actionRoles below gives each action its minimum role. That table -- not a
// scattering of role checks in handlers -- is the authorisation policy, and a
// test walks the whole list, so a new endpoint cannot ship without somebody
// deciding who may call it.
//
// The string form is "<resource>.<verb>". The scope form an API token carries
// is "<resource>:<verb>", which Action.Scope renders.
type Action string

// Pool actions.
const (
	ActionPoolsRead   Action = "pools.read"
	ActionPoolsWrite  Action = "pools.write"
	ActionPoolsDelete Action = "pools.delete"
)

// Runner actions. Draining is separated from deleting because draining never
// interrupts a running job and deleting can.
const (
	ActionRunnersRead   Action = "runners.read"
	ActionRunnersCreate Action = "runners.create"
	ActionRunnersDrain  Action = "runners.drain"
	ActionRunnersDelete Action = "runners.delete"
)

// Job actions. Jobs are observed, never mutated, so there is only a read.
const ActionJobsRead Action = "jobs.read"

// Host actions.
const (
	ActionHostsRead   Action = "hosts.read"
	ActionHostsWrite  Action = "hosts.write"
	ActionHostsCordon Action = "hosts.cordon"
	ActionHostsDelete Action = "hosts.delete"
)

// Installation actions. Verifying credentials is an operator action because it
// is a read-only probe; changing them is not.
const (
	ActionInstallationsRead   Action = "installations.read"
	ActionInstallationsWrite  Action = "installations.write"
	ActionInstallationsDelete Action = "installations.delete"
	ActionInstallationsVerify Action = "installations.verify"
)

// Webhook actions cover the delivery log and the reachability test.
const (
	ActionWebhooksRead Action = "webhooks.read"
	ActionWebhooksTest Action = "webhooks.test"
)

// Audit actions.
const ActionAuditRead Action = "audit.read"

// Migrations move a repository's workflows onto this fleet, which means
// opening pull requests in repositories Zoomies does not own. Reading a plan
// is an operator's job rather than a viewer's because it costs a burst of
// GitHub quota the scheduler shares; opening the pull requests is the same
// weight as changing a pool.
const (
	ActionMigrationsRead  Action = "migrations.read"
	ActionMigrationsWrite Action = "migrations.write"
)

// Account and credential actions.
const (
	ActionUsersRead   Action = "users.read"
	ActionUsersWrite  Action = "users.write"
	ActionTokensRead  Action = "tokens.read"
	ActionTokensWrite Action = "tokens.write"
	ActionJoinsRead   Action = "joins.read"
	ActionJoinsWrite  Action = "joins.write"
)

// Instance actions.
const (
	ActionSettingsRead  Action = "settings.read"
	ActionSettingsWrite Action = "settings.write"
	ActionMetricsRead   Action = "metrics.read"
	ActionEventsRead    Action = "events.read"
	ActionLogsRead      Action = "logs.read"
	ActionStatsRead     Action = "stats.read"
)

// actionRoles is the authorisation policy in one table.
//
// The rule behind it, from docs/security.md: a viewer reads everything except
// secret values; an operator additionally acts on the fleet and manages pools;
// an admin additionally manages users, tokens, installations, join tokens and
// settings. Deleting a host is an admin action rather than an operator one
// because it removes a machine's whole history, not just a runner.
var actionRoles = map[Action]store.Role{
	ActionPoolsRead:   store.RoleViewer,
	ActionPoolsWrite:  store.RoleOperator,
	ActionPoolsDelete: store.RoleOperator,

	ActionRunnersRead:   store.RoleViewer,
	ActionRunnersCreate: store.RoleOperator,
	ActionRunnersDrain:  store.RoleOperator,
	ActionRunnersDelete: store.RoleOperator,

	ActionJobsRead: store.RoleViewer,

	ActionHostsRead:   store.RoleViewer,
	ActionHostsWrite:  store.RoleOperator,
	ActionHostsCordon: store.RoleOperator,
	ActionHostsDelete: store.RoleAdmin,

	ActionInstallationsRead:   store.RoleViewer,
	ActionInstallationsWrite:  store.RoleAdmin,
	ActionInstallationsDelete: store.RoleAdmin,
	ActionInstallationsVerify: store.RoleOperator,

	ActionWebhooksRead: store.RoleViewer,
	ActionWebhooksTest: store.RoleOperator,

	ActionAuditRead: store.RoleViewer,

	ActionMigrationsRead:  store.RoleOperator,
	ActionMigrationsWrite: store.RoleOperator,

	ActionUsersRead:   store.RoleAdmin,
	ActionUsersWrite:  store.RoleAdmin,
	ActionTokensRead:  store.RoleAdmin,
	ActionTokensWrite: store.RoleAdmin,
	ActionJoinsRead:   store.RoleAdmin,
	ActionJoinsWrite:  store.RoleAdmin,

	ActionSettingsRead:  store.RoleAdmin,
	ActionSettingsWrite: store.RoleAdmin,
	ActionMetricsRead:   store.RoleViewer,
	ActionEventsRead:    store.RoleViewer,
	ActionLogsRead:      store.RoleViewer,
	ActionStatsRead:     store.RoleViewer,
}

// AllActions returns every action, sorted. The UI's token editor lists the
// scopes from here, so the list a human sees can never drift from the list the
// server enforces.
func AllActions() []Action {
	out := make([]Action, 0, len(actionRoles))
	for a := range actionRoles {
		out = append(out, a)
	}
	slices.Sort(out)
	return out
}

// Known reports whether a is an action this server enforces.
func (a Action) Known() bool { _, ok := actionRoles[a]; return ok }

// Resource is the noun an action acts on, e.g. "pools".
func (a Action) Resource() string { r, _, _ := strings.Cut(string(a), "."); return r }

// Verb is what the action does to its resource, e.g. "read".
func (a Action) Verb() string { _, v, _ := strings.Cut(string(a), "."); return v }

// Scope renders the action as the scope string an API token carries.
func (a Action) Scope() string { return a.Resource() + ":" + a.Verb() }

// MinRole returns the least privileged role that may perform a.
//
// An action that is not in the table returns admin, which keeps the display
// honest, but Allowed refuses unknown actions outright: failing closed matters
// more than being able to name the role.
func (a Action) MinRole() store.Role {
	if r, ok := actionRoles[a]; ok {
		return r
	}
	return store.RoleAdmin
}

// String makes Action printable in log lines and error messages.
func (a Action) String() string { return string(a) }

// Allowed reports whether id may perform a.
//
// Two gates apply. The role must reach the action's minimum, and -- when the
// identity carries a non-empty scope list -- one of its scopes must cover the
// action. An empty scope list means "whatever the role allows", which is what
// every user session and most tokens have.
func Allowed(id *Identity, a Action) bool {
	if id == nil {
		return false
	}
	want, known := actionRoles[a]
	if !known {
		return false
	}
	if !id.Role.AtLeast(want) {
		return false
	}
	if len(id.Scopes) == 0 {
		return true
	}
	return scopesAllow(id.Scopes, a)
}

// scopesAllow reports whether any scope in the list covers a.
//
// "*" covers everything, "pools:*" covers one resource, and any scope on a
// resource implies reading that resource -- a token that may drain runners but
// could not list them would be useless.
func scopesAllow(scopes []string, a Action) bool {
	want := a.Scope()
	for _, s := range scopes {
		s = strings.ToLower(strings.TrimSpace(s))
		switch s {
		case "", "-":
			continue
		case "*", "*:*":
			return true
		}
		if s == want {
			return true
		}
		res, verb, ok := strings.Cut(s, ":")
		if !ok || res != a.Resource() {
			continue
		}
		if verb == "*" || a.Verb() == "read" {
			return true
		}
	}
	return false
}

// ValidateScopes checks a token's requested scopes against the action list, so
// a typo is rejected at creation time rather than silently granting nothing.
func ValidateScopes(scopes []string) error {
	valid := map[string]bool{"*": true, "*:*": true}
	resources := map[string]bool{}
	for _, a := range AllActions() {
		valid[a.Scope()] = true
		resources[a.Resource()] = true
	}
	for r := range resources {
		valid[r+":*"] = true
	}
	var bad []string
	for _, s := range scopes {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if !valid[s] {
			bad = append(bad, s)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	all := make([]string, 0, len(actionRoles))
	for _, a := range AllActions() {
		all = append(all, a.Scope())
	}
	return fmt.Errorf("unknown scope %s: valid scopes are %s, or <resource>:* , or *",
		strings.Join(bad, ", "), strings.Join(all, ", "))
}

// Explain returns the sentence a 403 should carry, or "" when the action is
// allowed. It names the role or scope that is missing, because "forbidden" on
// its own tells an operator nothing about what to change.
func Explain(id *Identity, a Action) string {
	if id == nil {
		return "this action needs you to be signed in"
	}
	want, known := actionRoles[a]
	if !known {
		return fmt.Sprintf("%q is not an action this server knows about", string(a))
	}
	if !id.Role.AtLeast(want) {
		return fmt.Sprintf("this action needs the %s role; your %s has %s",
			want, id.subject(), id.Role)
	}
	if len(id.Scopes) > 0 && !scopesAllow(id.Scopes, a) {
		return fmt.Sprintf("this action needs the %q scope; your %s is limited to %s",
			a.Scope(), id.subject(), strings.Join(id.Scopes, ", "))
	}
	return ""
}
