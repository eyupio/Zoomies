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
	ActionRunnersRead Action = "runners.read"
	// There is no runners.create: a runner is created by the scheduler, in
	// response to a queued job, and no route asks for one. A scope nothing
	// checks is a scope an operator can grant believing it does something.
	ActionRunnersDrain  Action = "runners.drain"
	ActionRunnersDelete Action = "runners.delete"
)

// Job actions. Cancellation is deliberately separate: GitHub cancels the
// whole workflow run, not only the job selected in Zoomies.
const (
	ActionProvisioningWrite Action = "provisioning.write"
	ActionJobsRead          Action = "jobs.read"
	ActionJobsCancel        Action = "jobs.cancel"
	ActionJobsRerun         Action = "jobs.rerun"
)
const ActionUsageRead Action = "usage.read"

// ActionProblemsApply is making the change a problem proposes. It is its own
// action, and an operator's, because it changes the fleet; and it is only the first
// of two checks: the change is a pool's or a host's update, and it needs that
// update's action as well, so a token narrowed to one resource cannot be used to
// change another by naming a problem about it.
const ActionProblemsApply Action = "problems.apply"

// Host actions.
const (
	ActionHostsRead   Action = "hosts.read"
	ActionHostsWrite  Action = "hosts.write"
	ActionHostsCordon Action = "hosts.cordon"
	ActionHostsDelete Action = "hosts.delete"
	// ActionHostsAccept is deciding that a host's warning is deliberate, and taking
	// the decision back. It changes what Zoomies counts, never the host. Operator
	// for both halves: revoking only ever makes Zoomies stricter.
	ActionHostsAccept Action = "hosts.accept"
	// ActionHostsCheck is asking a host's agent to run its read-only OS checks
	// now. It changes nothing on the host, but it spends the host's CPU and forks
	// a few dozen processes there, so a viewer never can, and it has a scope of
	// its own so a token can be given this and nothing else about hosts.
	ActionHostsCheck Action = "hosts.check"
	// ActionHostsUpdate is asking a host's agent to have its update helper replace the
	// agent's binary with this controller's release. It replaces software that runs
	// as root on a machine the controller does not own, and a failed one leaves the
	// host without an agent until somebody reaches it, so it is an administrator's
	// and not an operator's, and has a scope of its own so a token can be given
	// cordoning without it.
	ActionHostsUpdate Action = "hosts.update"
)

// Installation actions. Verifying credentials is an operator action because it
// is a read-only probe; changing them is not.
const (
	ActionInstallationsRead   Action = "installations.read"
	ActionInstallationsWrite  Action = "installations.write"
	ActionInstallationsDelete Action = "installations.delete"
	ActionInstallationsVerify Action = "installations.verify"
	// ActionInstallationsExport takes an installation's whole history away,
	// and with a passphrase its private key too. It is admin, as changing the
	// key is, because the archive is the credential once it is sealed for
	// somewhere else.
	ActionInstallationsExport Action = "installations.export"
)

// Provider and machine actions. A provider's credential reaches a hypervisor,
// so configuring one is admin-class as an installation is; pausing is operator,
// because the person on call at three in the morning has to be able to press
// the kill switch without waiting for somebody with an admin account.
//
// Draining a machine is separated from deleting one for the reason runners are:
// a drain finishes the work that is on it, and a delete destroys a machine
// somebody is paying for.
const (
	ActionProvidersRead   Action = "providers.read"
	ActionProvidersWrite  Action = "providers.write"
	ActionProvidersDelete Action = "providers.delete"
	ActionProvidersPause  Action = "providers.pause"
	ActionMachinesRead    Action = "machines.read"
	ActionMachinesDrain   Action = "machines.drain"
	ActionMachinesDelete  Action = "machines.delete"
)

// Assistant actions cover the providers the in-UI assistant talks to. Both
// are admin: which model answers, and with what key, is the administrator's
// until the step-up slice makes it cost more than an admin session.
const (
	// ActionAssistantOwn grants only the owner-scoped personal endpoints.
	ActionAssistantOwn   Action = "assistant.own"
	ActionAssistantRead  Action = "assistant.read"
	ActionAssistantWrite Action = "assistant.write"
	// ActionAssistantChat is asking the default model a question. It is admin
	// until the limits and the redaction of slice 7c exist: a question goes to
	// whatever provider the administrator chose, a hosted one included, and costs
	// what that provider charges.
	ActionAssistantChat Action = "assistant.chat"
)

// Webhook actions cover the delivery log and the reachability test.
const (
	ActionWebhooksRead Action = "webhooks.read"
	ActionWebhooksTest Action = "webhooks.test"
)

// Audit actions.
const ActionAuditRead Action = "audit.read"

// Context is source data, not fleet metadata. These actions are only the
// coarse role/scope gate; ContextAccess also requires repository membership.
const (
	ActionContextRead      Action = "context.read"
	ActionContextConfigure Action = "context.configure"
	// ActionContextManage is only the coarse gate for installation owners. It
	// grants nothing by itself: every handler also checks that the caller owns
	// the installation (or holds context.configure).
	ActionContextManage Action = "context.manage"
	// ActionContextPublish is the coarse gate on writing notes about a
	// repository. Like context.read it grants nothing alone: ContextPublishAccess
	// also needs membership and, for a connection, its owner's publish consent.
	ActionContextPublish Action = "context.publish"
)

// Kennel Club says which of the fleet's repositories fall short of a standard
// that affects running CI or the fleet itself. Reading it is every role's. Asking
// for a repository to be read again spends GitHub requests the scheduler shares,
// and waiving a finding is a recorded decision with a reason and an owner, so both
// are an operator's.
//
// Waiving an error is a fourth action and not a second check inside a handler,
// for the reason the policy is one table: an error is a stranger already running
// code on the fleet, the decision that this is acceptable is an administrator's,
// and a scoped token has to be able to be given the one without the other.
//
// Tracking is split the same way, and for the same reason. Telling Kennel Club to
// stop looking at a repository silences every error it would have raised there, so
// it is an administrator's; telling it to start again can only make it stricter, so
// it is an operator's.
const (
	ActionKennelGuidancePreview Action = "kennel.guidance_preview"
	ActionKennelGuidancePR      Action = "kennel.guidance_pr"
	ActionKennelRead            Action = "kennel.read"
	ActionKennelRecheck         Action = "kennel.recheck"
	ActionKennelWaive           Action = "kennel.waive"
	ActionKennelWaiveError      Action = "kennel.waive_error"
	ActionKennelTrack           Action = "kennel.track"
	ActionKennelUntrack         Action = "kennel.untrack"
)

// Migrations move a repository's workflows onto this fleet, which means
// opening pull requests in repositories Zoomies does not own. Reading a plan
// is an operator's job rather than a viewer's because it costs a burst of
// GitHub quota the scheduler shares; opening the pull requests is the same
// weight as changing a pool.
const (
	ActionMigrationsRead  Action = "migrations.read"
	ActionMigrationsWrite Action = "migrations.write"
)

// Updates. Reading what an update would take is every role's: it names a public
// release, the build this controller runs and a sentence about why one has not
// been taken, and nothing of the fleet's. It is the first viewer-readable route
// that carries the platform-scoped mode and soak, which say what the controller
// will do and open nothing.
//
// Asking GitHub now is an administrator's, as every other check that spends the
// controller's requests is. Updating the controller is the platform role's: it
// replaces the binary of the process every fleet on the instance depends on, and
// restarts it, which is a decision about the instance and not about one fleet.
//
// A rollout (starting, resuming or cancelling one) is an administrator's, as
// one host's update is: it walks the fleet's hosts through that same update, one
// at a time, and touches nothing of the instance. Its scope is its own, so that a
// token that may update one host is not one that may update every host.
const (
	ActionUpdatesRead    Action = "updates.read"
	ActionUpdatesCheck   Action = "updates.check"
	ActionUpdatesApply   Action = "updates.apply"
	ActionUpdatesRollout Action = "updates.rollout"
)

// Account and credential actions.
const (
	ActionUsersRead   Action = "users.read"
	ActionUsersWrite  Action = "users.write"
	ActionTokensRead  Action = "tokens.read"
	ActionTokensWrite Action = "tokens.write"
	// ActionTokensOwn is creating, listing and revoking your own API tokens,
	// never at a role above your own. Everybody signed in has it: the CLI and
	// a script of one's own are ordinary work, and asking an administrator to
	// mint a credential for somebody else to hold was the worse habit.
	// tokens.read and tokens.write remain everybody's tokens.
	ActionTokensOwn  Action = "tokens.own"
	ActionJoinsRead  Action = "joins.read"
	ActionJoinsWrite Action = "joins.write"
	// The OAuth clients that may ask for an MCP connection, and every
	// person's connections. Administrator, as API tokens are: a client an
	// administrator creates is a way in that somebody else will use.
	ActionMCPClientsRead  Action = "mcp_clients.read"
	ActionMCPClientsWrite Action = "mcp_clients.write"
)

// Instance actions.
const (
	ActionSettingsRead  Action = "settings.read"
	ActionSettingsWrite Action = "settings.write"
	ActionMetricsRead   Action = "metrics.read"
	ActionEventsRead    Action = "events.read"
	ActionLogsRead      Action = "logs.read"
	ActionStatsRead     Action = "stats.read"
	// ActionDiagnosticsRead covers the support bundle, which is every other
	// read gathered into one document. It is admin rather than viewer because
	// the weakest role that covers all of it is the strongest role inside it:
	// the bundle carries the settings section, and settings.read is admin. A
	// viewer-readable bundle would hand out the one section this project has
	// always kept behind an admin.
	ActionDiagnosticsRead Action = "diagnostics.read"
	// ActionRecoveryWrite lifts the fence a restore sets. It is its own action
	// rather than settings.write because it is not a setting: it is a promise
	// that somebody has looked at a recovered fleet and decided it may act on
	// the world again, and it wants to be visible in an audit log under a name
	// that says so.
	ActionRecoveryWrite Action = "recovery.write"
	// The backups. Reading one is reading the whole database -- every
	// account's password hash and every sealed credential -- so backups.read
	// is admin like settings.read is, and a token scoped to it can download
	// the fleet. backups.restore is its own action rather than backups.write
	// because it is the one that replaces the fleet: a token that may take
	// copies should not be one that may put one back.
	ActionBackupsRead    Action = "backups.read"
	ActionBackupsWrite   Action = "backups.write"
	ActionBackupsRestore Action = "backups.restore"
)

// actionRoles is the authorisation policy in one table.
//
// The rule behind it, from docs/security.md: a viewer reads everything except
// secret values; an operator additionally acts on the fleet and manages pools;
// an admin additionally manages users, tokens, installations, join tokens and
// settings. Deleting a host is an admin action rather than an operator one
// because it removes a machine's whole history, not just a runner.
var actionRoles = map[Action]store.Role{
	ActionKennelGuidancePreview: store.RoleAdmin,
	ActionKennelGuidancePR:      store.RoleAdmin,
	ActionKennelRead:            store.RoleViewer,
	ActionKennelRecheck:         store.RoleOperator,
	ActionKennelWaive:           store.RoleOperator,
	ActionKennelWaiveError:      store.RoleAdmin,
	ActionKennelTrack:           store.RoleOperator,
	ActionKennelUntrack:         store.RoleAdmin,

	ActionContextRead:      store.RoleViewer,
	ActionContextConfigure: store.RoleAdmin,
	ActionContextManage:    store.RoleViewer,
	ActionContextPublish:   store.RoleViewer,

	ActionPoolsRead:   store.RoleViewer,
	ActionPoolsWrite:  store.RoleOperator,
	ActionPoolsDelete: store.RoleOperator,

	ActionRunnersRead:   store.RoleViewer,
	ActionRunnersDrain:  store.RoleOperator,
	ActionRunnersDelete: store.RoleOperator,

	ActionProvisioningWrite: store.RoleOperator,
	ActionJobsRead:          store.RoleViewer,
	ActionJobsCancel:        store.RoleOperator,
	// Operator rather than viewer: a re-run spends the organisation's GitHub
	// minutes and can have whatever side effects the workflow has, which is
	// the same bar cancelling one clears.
	ActionJobsRerun: store.RoleOperator,
	ActionUsageRead: store.RoleViewer,
	// Operator: it makes a change, as the pool and host updates it stands in for do.
	ActionProblemsApply: store.RoleOperator,

	ActionHostsRead:   store.RoleViewer,
	ActionHostsWrite:  store.RoleOperator,
	ActionHostsCordon: store.RoleOperator,
	ActionHostsDelete: store.RoleAdmin,
	ActionHostsAccept: store.RoleOperator,
	ActionHostsCheck:  store.RoleOperator,
	ActionHostsUpdate: store.RoleAdmin,

	ActionInstallationsRead:   store.RoleViewer,
	ActionInstallationsWrite:  store.RoleAdmin,
	ActionInstallationsDelete: store.RoleAdmin,
	ActionInstallationsVerify: store.RoleOperator,
	ActionInstallationsExport: store.RoleAdmin,

	ActionWebhooksRead: store.RoleViewer,
	ActionWebhooksTest: store.RoleOperator,

	ActionAssistantOwn:   store.RoleViewer,
	ActionAssistantRead:  store.RoleAdmin,
	ActionAssistantWrite: store.RoleAdmin,
	ActionAssistantChat:  store.RoleAdmin,

	ActionProvidersRead:   store.RoleViewer,
	ActionProvidersWrite:  store.RoleAdmin,
	ActionProvidersDelete: store.RoleAdmin,
	ActionProvidersPause:  store.RoleOperator,
	ActionMachinesRead:    store.RoleViewer,
	ActionMachinesDrain:   store.RoleOperator,
	ActionMachinesDelete:  store.RoleAdmin,

	ActionAuditRead: store.RoleViewer,

	ActionUpdatesRead:    store.RoleViewer,
	ActionUpdatesCheck:   store.RoleAdmin,
	ActionUpdatesApply:   store.RolePlatform,
	ActionUpdatesRollout: store.RoleAdmin,

	ActionMigrationsRead:  store.RoleOperator,
	ActionMigrationsWrite: store.RoleOperator,

	ActionUsersRead:   store.RoleAdmin,
	ActionUsersWrite:  store.RoleAdmin,
	ActionTokensRead:  store.RoleAdmin,
	ActionTokensWrite: store.RoleAdmin,
	ActionTokensOwn:   store.RoleViewer,
	ActionJoinsRead:   store.RoleAdmin,
	ActionJoinsWrite:  store.RoleAdmin,

	ActionMCPClientsRead:  store.RoleAdmin,
	ActionMCPClientsWrite: store.RoleAdmin,

	ActionSettingsRead:  store.RoleAdmin,
	ActionSettingsWrite: store.RoleAdmin,
	ActionMetricsRead:   store.RoleViewer,
	ActionEventsRead:    store.RoleViewer,
	ActionLogsRead:      store.RoleViewer,
	ActionStatsRead:     store.RoleViewer,

	ActionDiagnosticsRead: store.RoleAdmin,
	// The fence and the backups belong to whoever runs the process, not to
	// whoever runs the fleet. A backup is the whole database -- every
	// account's password hash and every sealed credential, under the key
	// this host holds -- and lifting the fence is a decision about whether
	// a restored instance may act on the world again. Neither is the
	// fleet's to take, and a fleet that wants its own data has the
	// per-installation export instead.
	ActionRecoveryWrite:  store.RolePlatform,
	ActionBackupsRead:    store.RolePlatform,
	ActionBackupsWrite:   store.RolePlatform,
	ActionBackupsRestore: store.RolePlatform,
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
	if scopesAllow(id.Scopes, a) {
		return true
	}
	for _, wider := range impliedBy[a] {
		if scopesAllow(id.Scopes, wider) {
			return true
		}
	}
	return false
}

// impliedBy lists, for an action, the wider actions whose scope also grants
// it. A token scoped to everybody's tokens was minted to manage tokens, and
// refusing it its own would break every such token the day tokens.own arrived.
var impliedBy = map[Action][]Action{
	ActionTokensOwn: {ActionTokensRead, ActionTokensWrite},
	// A token minted to waive errors is minted to waive, and the route asks for
	// kennel.waive first; refusing it that would make the wider scope useless.
	ActionKennelWaive: {ActionKennelWaiveError},
	// The same for tracking: the route asks for kennel.track, and a token minted to
	// stop tracking was minted to be able to use it.
	ActionKennelTrack: {ActionKennelUntrack},
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

// ManageWithin refuses an account change that would reach past the caller's
// own role: granting a role the caller does not hold, or acting on an account
// that outranks it. Without it the platform role separated nothing -- an
// administrator could create a platform account, promote their own, or reset
// the platform's password and sign in as it. It is MintWithin's rule for
// accounts: authority is handed on, never made up.
func ManageWithin(by *Identity, roles ...store.Role) error {
	if by == nil {
		return Invalid("an account can only be changed by an authenticated caller")
	}
	for _, role := range roles {
		if !by.Role.AtLeast(role) {
			return Invalid("the %s role cannot grant or change a %s account; ask somebody with the %s role", by.Role, role, role)
		}
	}
	return nil
}

// MintWithin refuses a token that would carry more than the identity minting
// it. A token is a way to hand authority on, and handing on more than one
// holds is how a narrowly scoped credential that leaked turns into an
// unscoped one that never expires: the role may not exceed the caller's, and
// a caller narrowed by scopes may only mint a token narrowed at least as far.
//
// The check is made against the actions themselves rather than the scope
// strings, so "pools:*" covers "pools:write", any scope on a resource covers
// reading it, and a new action needs no change here.
func MintWithin(by *Identity, role store.Role, scopes []string) error {
	if by == nil {
		return Invalid("a token can only be minted by an authenticated caller")
	}
	if !by.Role.AtLeast(role) {
		return Invalid("a %s cannot mint a %s token; a token carries no more than the caller that made it", by.Role, role)
	}
	if len(by.Scopes) == 0 {
		return nil
	}
	if len(scopes) == 0 {
		return Invalid("this token is limited to %s, so the tokens it mints need scopes within that; an unscoped token would carry the whole role",
			strings.Join(by.Scopes, ", "))
	}
	var beyond []string
	for _, a := range AllActions() {
		if scopesAllow(scopes, a) && !scopesAllow(by.Scopes, a) {
			beyond = append(beyond, a.Scope())
		}
	}
	if len(beyond) > 0 {
		return Invalid("this token is limited to %s and cannot mint one that reaches %s",
			strings.Join(by.Scopes, ", "), strings.Join(beyond, ", "))
	}
	return nil
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
