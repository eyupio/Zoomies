package controller

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/naming"
	"github.com/eyupio/zoomies/internal/provider"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/version"
)

// Problem is one thing an operator should know about, in the shape the API's
// /problems endpoint returns.
//
// It carries the same four sentences a config.Finding does -- what is true,
// why it matters, and what to change -- because an operator reading the
// problems drawer should never have to go and look up what a code means.
type Problem struct {
	// Code is a stable identifier such as "host.unhealthy", suitable for
	// grouping or suppressing in an alerting rule.
	Code     string          `json:"code"`
	Severity config.Severity `json:"severity"`
	// Setting names the configuration key involved, when there is one.
	Setting string `json:"setting,omitempty"`
	// Source is the layer that set it: the file, this fleet's database, or a
	// ZOOMIES_* variable. It is carried because the fix below is otherwise
	// half an instruction -- an operator told to change a setting will edit
	// the configuration file, and a value stored in the database or pinned by
	// the environment is one the file cannot change.
	Source config.Source `json:"source,omitempty"`
	// Undo is the command that takes the value back out of that layer, for the
	// case the settings page cannot fix: a stored value that stops the
	// controller starting is unreachable from a page the controller serves.
	Undo   string `json:"undo,omitempty"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
	// TargetKind and TargetID let the UI link a problem to the pool, host,
	// runner or installation it is about.
	TargetKind string `json:"target_kind,omitempty"`
	TargetID   string `json:"target_id,omitempty"`
	// Alternatives are the choices the fix leaves open, when the fix is a
	// choice: for a pool no host can run, the backends its hosts do offer. They
	// are carried apart from the prose so the UI can put the change one click
	// away rather than leaving an operator to find the pool's edit form.
	Alternatives []string `json:"alternatives,omitempty"`
	// DaemonShare is the change pool.daemon_share_suggested proposes, carried
	// apart from the prose for the same reason: an operator who is told a
	// number should be able to make the change with one click, and a UI that had
	// to parse "--daemon-cpu-share 20" out of a sentence would be one reworded
	// sentence from applying the wrong figure.
	DaemonShare *DaemonShareChange `json:"daemon_share,omitempty"`
	// Remedy is the change the controller proposes, as the update that makes it:
	// see remedy.go. Absent when the fix is a decision rather than a change, or
	// when what would fix it has not been priced.
	Remedy *Remedy `json:"remedy,omitempty"`
	// Since is when the situation started, where that is knowable.
	Since *time.Time `json:"since,omitempty"`
	// Audience is whose problem this is. It is filled in by Problems() from
	// the code, in one pass, rather than at each of the thirty-odd places a
	// Problem is made -- a field set at the site is a field somebody forgets
	// at the site, which is the drift the settings registry exists to
	// prevent.
	Audience Audience `json:"audience"`
}

// DaemonShareChange is a pool's Docker sidecar share, as a percentage of the
// slot, for each resource a notice proposes moving. A resource it leaves out is
// one the notice has no advice for, or advice that would cost the pool its
// runners, and applying the change leaves that share where it is.
type DaemonShareChange struct {
	CPUPercent    int `json:"cpu_percent,omitempty"`
	MemoryPercent int `json:"memory_percent,omitempty"`
}

// Audience says which of an instance's two audiences a problem is for.
//
// An instance one team operates while another uses the fleet has two lists,
// not one. The platform's is what the process is doing wrong -- its lease,
// its loops, its backups, the release it could be running. The fleet's is
// what its own runners are doing wrong. Showing either audience the other's
// is a leak in one direction and noise in the other: a fleet told its
// controller's backup remote is unreadable can do nothing about it, and
// learns the instance has a backup remote.
type Audience string

const (
	// AudiencePlatform is for whoever runs the process.
	AudiencePlatform Audience = "platform"
	// AudienceFleet is for whoever runs the fleet.
	AudienceFleet Audience = "fleet"
	// AudienceBoth is for the handful that are genuinely both people's, of
	// which there is currently one: a list that could not be fully gathered
	// has to say so to whoever is reading it.
	AudienceBoth Audience = "both"
)

// For reports whether a problem belongs on the list this role is shown.
func (a Audience) For(platform bool) bool {
	// The platform sees everything, including the fleet's. The split exists to
	// keep the process's own troubles from the fleet, not to keep the fleet's
	// from whoever runs the process: platform is the higher role, it is the
	// account that installed every single-team instance there is, and a first
	// draft that read "platform sees platform problems" took every pool, host,
	// runner and job problem off the drawer of every operator running their
	// own fleet. A drill caught it; the unit tests here did not, because each
	// asked whether the platform saw its own rather than whether it still saw
	// theirs.
	if platform {
		return true
	}
	// The fleet sees its own and the ones that are both people's. An
	// unclassified problem is not theirs, which is the withholding default
	// audienceFor documents.
	return a == AudienceFleet || a == AudienceBoth
}

var problemAudience = map[string]Audience{
	// The process's own operation: its lease, its loops, the release it could
	// be running, the key it seals with, the backups it takes and the receiver
	// it posts capacity demand to. None of it is about the fleet's runners.
	"backup.failed":                           AudiencePlatform,
	"bootstrap.ignored":                       AudiencePlatform,
	"backup.remote_failed":                    AudiencePlatform,
	"backup.remote_insecure":                  AudiencePlatform,
	"backup.remote_plaintext":                 AudiencePlatform,
	"backup.remote_shadowed":                  AudiencePlatform,
	"backup.remote_unreadable":                AudiencePlatform,
	"backup.restore_failed":                   AudiencePlatform,
	"backup.restore_staged":                   AudiencePlatform,
	"capacity_demand.delivery_failed":         AudiencePlatform,
	"controller.development_update_available": AudiencePlatform,
	"controller.lease_lost":                   AudiencePlatform,
	// Single sign-on is the process reaching somebody else's service, as
	// the oidc.* findings from the validator are the platform's.
	"oidc.unavailable":            AudiencePlatform,
	"controller.loop_panicked":    AudiencePlatform,
	"controller.update_available": AudiencePlatform,
	// Updating the controller is the process replacing its own binary, through a
	// helper that root installed on its host. A fleet can neither install the
	// helper nor read the helper's journal.
	"controller.update_failed":         AudiencePlatform,
	"controller.update_helper_missing": AudiencePlatform,
	"crypto.key_mismatch":              AudiencePlatform,
	// The private-connection listener is the process's: it is one per
	// instance, it dials out from the controller's network, and the fix is that
	// network's egress, which a fleet cannot change.
	"tailcat.unavailable": AudiencePlatform,
	// The credential-minting limit is scheduler.registration_concurrency,
	// which only the platform can change. A fleet told its runners are being
	// held back at a number it cannot reach would go looking for hosts, which
	// is the wrong answer twice over.
	"scheduler.registration_throttled": AudiencePlatform,

	// Whoever is reading a truncated list has to be told it is truncated,
	// whichever list it is. A section quietly missing reads as a healthy
	// fleet, which is the one thing this must never say by accident.
	"controller.problems_partial": AudienceBoth,

	// The fleet's own: its pools, its hosts, its runners, its jobs, the
	// machines it rents and the App it runs on. An operator who uses the
	// fleet is the person who can act on every one of these.
	"host.cordoned_with_work":           AudienceFleet,
	"host.auto_pool_skipped":            AudienceFleet,
	"host.duplicate_agent":              AudienceFleet,
	"host.image_pull_failed":            AudienceFleet,
	"host.limits_unenforceable":         AudienceFleet,
	"host.limits_unverified":            AudienceFleet,
	"host.overprovisioned":              AudienceFleet,
	"host.resources_unknown":            AudienceFleet,
	"host.shared_folder_unmounted":      AudienceFleet,
	"host.runtime_recovering":           AudienceFleet,
	"host.throttled":                    AudienceFleet,
	"host.unhealthy":                    AudienceFleet,
	"host.os_health":                    AudienceFleet,
	"host.health_stale":                 AudienceFleet,
	"host.reboot_pending":               AudienceFleet,
	"host.version_behind":               AudienceFleet,
	"installation.unhealthy":            AudienceFleet,
	"jobs.oom_killed":                   AudienceFleet,
	"jobs.runner_lost":                  AudienceFleet,
	"jobs.label_advice":                 AudienceFleet,
	"jobs.unmatched":                    AudienceFleet,
	"ai_context.artifact_quota":         AudienceFleet,
	"ai_context.artifact_upload_failed": AudienceFleet,
	"ai_context.delivery_failed":        AudienceFleet,
	"ai_context.generation_refused":     AudienceFleet,
	"ai_context.oversized_file":         AudienceFleet,
	"ai_context.publication_refused":    AudienceFleet,
	"ai_context.run_failed":             AudienceFleet,
	"ai_context.runner_unavailable":     AudienceFleet,
	"ai_context.setup_failed":           AudienceFleet,
	"ai_context.startup_failed":         AudienceFleet,
	// A repository the fleet serves has a stranger's code running on it, which is
	// the fleet's to act on. Kennel Club's own reads failing is the process's: the
	// fleet cannot do anything about an installation's credentials or its budget.
	"kennel.exposure":                               AudienceFleet,
	"kennel.unavailable":                            AudiencePlatform,
	"ai_context.timed_out":                          AudienceFleet,
	"ai_context.too_much_source":                    AudienceFleet,
	"poller.paused":                                 AudienceFleet,
	"poller.stale":                                  AudienceFleet,
	"pool.auto_blocked":                             AudienceFleet,
	"pool.cache_above_disk":                         AudienceFleet,
	"pool.cache_memory_unbounded":                   AudienceFleet,
	"pool.cache_shared":                             AudienceFleet,
	"pool.dangerous":                                AudienceFleet,
	"pool.docker_client_missing":                    AudienceFleet,
	"pool.elastic_cpu_unsupported":                  AudienceFleet,
	"pool.elastic_memory_unsupported":               AudienceFleet,
	"pool.memory_ceiling_reached":                   AudienceFleet,
	"host.memory_pool_exhausted":                    AudienceFleet,
	"pool.github_rate_limited":                      AudienceFleet,
	"pool.history_unfit":                            AudienceFleet,
	"pool.host_overcommitted":                       AudienceFleet,
	"pool.max_above_room":                           AudienceFleet,
	"pool.no_eligible_host":                         AudienceFleet,
	"pool.profile_default":                          AudienceFleet,
	"pool.no_capacity":                              AudienceFleet,
	"pool.provision_timeout_short":                  AudienceFleet,
	"pool.repository_scale_up_deferred":             AudienceFleet,
	"pool.resources_unenforced":                     AudienceFleet,
	"pool.runner_group_public_repositories_blocked": AudienceFleet,
	"pool.runner_group_unresolved":                  AudienceFleet,
	"pool.runners_failing":                          AudienceFleet,
	"pool.size_strands_hosts":                       AudienceFleet,
	"pool.size_unlimited":                           AudienceFleet,
	"pool.tmpfs_host_off":                           AudienceFleet,
	"pool.tmpfs_memory_tight":                       AudienceFleet,
	"pool.tmpfs_suggested":                          AudienceFleet,
	"pool.tmpfs_auto_on_disk":                       AudienceFleet,
	"pool.daemon_share_suggested":                   AudienceFleet,
	"host.slots_below_capacity":                     AudienceFleet,
	"pool.minimum_overcharges":                      AudienceFleet,
	"pool.queue_wait_high":                          AudienceFleet,
	"jobs.workflow_failing":                         AudienceFleet,
	"jobs.duration_regressed":                       AudienceFleet,
	"pool.minimum_held_by_job":                      AudienceFleet,
	"host.work_concentrated":                        AudienceFleet,
	"pool.tmpfs_unsupported":                        AudienceFleet,
	"pool.daemon_share_unsupported":                 AudienceFleet,
	"provider.bootstrap_failed":                     AudienceFleet,
	"provider.contract_unsupported":                 AudienceFleet,
	"provider.credentials_refused":                  AudienceFleet,
	"provider.delete_pending":                       AudienceFleet,
	"provider.machine_failed":                       AudienceFleet,
	"provider.orphan_found":                         AudienceFleet,
	"provider.ownership_unverified":                 AudienceFleet,
	"provider.preflight_failed":                     AudienceFleet,
	"provider.provisioning_paused":                  AudienceFleet,
	"provider.quota_exhausted":                      AudienceFleet,
	"provider.template_unverified":                  AudienceFleet,
	"provider.unreachable":                          AudienceFleet,
	"provider.unservable":                           AudienceFleet,
	"provider.zone_missing":                         AudienceFleet,
	"proxmox.bridge_missing":                        AudienceFleet,
	"proxmox.credentials_refused":                   AudienceFleet,
	"proxmox.insecure_tls":                          AudienceFleet,
	"proxmox.node_missing":                          AudienceFleet,
	"proxmox.node_offline":                          AudienceFleet,
	"proxmox.privilege_missing":                     AudienceFleet,
	"proxmox.storage_inactive":                      AudienceFleet,
	"proxmox.storage_missing":                       AudienceFleet,
	"proxmox.storage_no_images":                     AudienceFleet,
	"proxmox.template_missing":                      AudienceFleet,
	"proxmox.template_no_agent":                     AudienceFleet,
	"proxmox.template_not_a_template":               AudienceFleet,
	"proxmox.unreachable":                           AudienceFleet,
	"proxmox.version_unqualified":                   AudienceFleet,
	"proxmox.vmid_range":                            AudienceFleet,
	"proxmox.vmid_range_reserved":                   AudienceFleet,
	"recovery.fenced":                               AudienceFleet,
	"runners.cleanup_failed":                        AudienceFleet,
	"runners.failed":                                AudienceFleet,
	"runners.not_progressing":                       AudienceFleet,
	"webhook.never_received":                        AudienceFleet,
	"webhook.rejected":                              AudienceFleet,
}

// audienceFor is whose a problem is, by its code.
//
// An unknown code is the platform's, which is the safer way to be wrong: a
// problem withheld from the fleet is a gap in their list, while one shown to
// them that should not have been is a fact about somebody else's machine that
// cannot be taken back. TestEveryProblemCodeHasAnAudience makes the question
// moot in CI, but the default decides what a build between the two does.
func audienceFor(code string) Audience {
	if a, ok := problemAudience[code]; ok {
		return a
	}
	return AudiencePlatform
}

// problemWindow is how far back rejected webhook deliveries are counted. An
// hour is long enough to catch a secret that was changed on one side only, and
// short enough that yesterday's fixed problem is not still on the list.
const problemWindow = time.Hour

// Problems aggregates everything currently wrong and every dangerous setting
// in effect, worst first.
//
// It returns an empty slice rather than nil when there is nothing to say,
// because the UI renders "nothing needs your attention" from exactly that.
//
// A section it cannot gather is reported *in* the list, as
// controller.problems_partial, rather than returned as an error: this is the
// page an operator opens when something is wrong, and one failing query used
// to turn the whole of it into a 500. An empty drawer reads as a healthy
// fleet, which is the one thing it must never say by accident.
// sharedFolderProblem is a host whose agent cannot hand runners the shared
// folder -- a containerised agent whose container does not mount it from the
// host, or a tool cache folder there the runner cannot write to -- raised only
// where it costs something: a pool that keeps a tool cache
// and could run there. Its runners start without the kept cache, which is
// slower and nothing worse; the problem is saying why.
func sharedFolderProblem(h *store.Host, info store.HostBackend, pools []*store.Pool) (Problem, bool) {
	if info.SharedFolder == "" {
		return Problem{}, false
	}
	var affected []string
	for _, p := range pools {
		if p.Enabled && p.Cache.Enabled && p.Cache.Tools && p.Backend == info.Kind && scheduler.HostCouldRun(h, p) {
			affected = append(affected, p.Name)
		}
	}
	if len(affected) == 0 {
		return Problem{}, false
	}
	return Problem{
		Code:     "host.shared_folder_unmounted",
		Severity: config.SeverityWarning,
		// The backend is in the title because a host can have one of these
		// per backend, and the UI keys a row by code, target and title.
		Title:      naming.ForSentence(h.Name) + " keeps no tool cache for its " + string(info.Kind) + " runners",
		Detail:     fmt.Sprintf("%s. %s start their runners there without the kept tool cache, so every job downloads its toolchains again.", info.SharedFolder, strings.Join(affected, ", ")),
		Fix:        "mount the folder as the detail says and restart the container; the next heartbeat clears this.",
		TargetKind: "host",
		TargetID:   h.ID,
	}, true
}

// agentUpgradeFix renders "here is how to upgrade an agent", or says plainly
// that there is nothing to upgrade to.
//
// An agent is installed from a published release asset. A controller built from
// main is stamped main-sha-abc1234 and no release carries it, so naming this
// controller's version -- which both of these problems used to do -- sent the
// operator to a download that does not exist. The command is in backticks
// because RemedyText hangs a copy button on each one.
func agentUpgradeFix(then string) string {
	tag, ok := version.Release(version.Version)
	if !ok {
		return "this controller is " + version.Version + ", a build from main rather than a release, " +
			"so there is no matching agent to install. Run a released controller and this clears."
	}
	return "on each host: `curl -fsSL https://zoomies.sh/install.sh | sh -s -- --no-init --version v" + tag + "` " +
		"then `sudo systemctl restart zoomies-agent`. " + then
}

func (c *Controller) Problems(ctx context.Context) ([]Problem, error) {
	out := make([]Problem, 0, 8)

	// Configuration: every warning and error the validator produced. These are
	// the settings that trade safety for convenience, and they are listed
	// whether or not anything has gone wrong yet. ForUI drops the handful that
	// only the CLI says, because they are expected in a normal deployment and
	// a list that is never clear stops being read.
	for _, f := range c.cfg().Validate().ForUI() {
		if f.Severity != config.SeverityError && f.Severity != config.SeverityWarning {
			continue
		}
		// Every validator finding is the platform's: each one names a
		// setting, and the settings that can be dangerous are the ones that
		// say what the process binds, trusts, stores and logs. Set here
		// rather than in the table below, because this is the one site all
		// of them come through.
		out = append(out, Problem{
			Code: f.Code, Severity: f.Severity, Setting: f.Setting,
			Source: f.Source, Undo: f.Undo,
			Title: f.Title, Detail: f.Detail, Fix: f.Fix,
			Audience: AudiencePlatform,
		})
	}

	// Every section below is gathered on its own, and a section that fails
	// costs its own contents and nothing else. The alternative -- what this
	// used to do -- is that one failing query returns a 500 and the operator
	// sees an empty problems drawer at the moment they most need it, which
	// reads as "nothing is wrong" rather than as "I could not look".
	var missing []string
	gather := func(what string, fn func(context.Context, *[]Problem) error) {
		if err := fn(ctx, &out); err != nil {
			missing = append(missing, what)
			c.log.Warn("a section of the problems list could not be gathered",
				"section", what, "error", err)
		}
	}

	gather("pool settings", func(ctx context.Context, out *[]Problem) error {
		pools, err := c.st.ListPools(ctx)
		if err != nil {
			return fmt.Errorf("listing pools: %w", err)
		}
		insts, err := c.st.ListInstallations(ctx)
		if err != nil {
			return fmt.Errorf("listing installations: %w", err)
		}
		installations := make(map[string]*store.Installation, len(insts))
		for _, i := range insts {
			installations[i.ID] = i
		}
		for _, p := range pools {
			// The same sentences the pool's own page shows, from the same place.
			*out = append(*out, PoolWarnings(p, installations[p.InstallationID], c.cfg())...)
		}
		return nil
	})

	gather("hosts", c.hostProblems)
	gather("installations", c.installationProblems)
	gather("the encryption key", c.keyProblems)
	gather("host versions", c.hostSkewProblems)
	gather("host resources", c.hostResourceProblems)
	gather("in-memory folders", c.tmpfsAdviceProblems)
	gather("in-memory folders by host", c.tmpfsHostProblems)
	gather("the memory valve", c.memoryValveProblems)
	gather("sidecar share by host", c.daemonShareHostProblems)
	gather("sidecar share", c.daemonShareAdviceProblems)
	gather("host capacity", c.hostCapacityAdviceProblems)
	gather("pool minimums", c.poolMinimumAdviceProblems)
	gather("pool queue waits", c.poolQueueWaitProblems)
	gather("failing workflows", c.workflowFailingProblems)
	gather("slower jobs", c.jobsSlowerProblems)
	gather("host concentration", c.hostConcentrationProblems)
	gather("runner profiles", c.runnerProfileProblems)
	gather("automatic pools", c.autoPoolProblems)
	gather("label advice", c.labelAdviceProblems)
	gather("host incidents", c.hostIncidentProblems)
	gather("host OS health", c.hostHealthProblems)
	out = append(out, c.fenceProblems()...)
	out = append(out, c.ssoProblems()...)
	out = append(out, c.bootstrapProblems()...)
	gather("webhook deliveries", c.webhookProblems)
	gather("jobs", c.jobProblems)
	out = append(out, c.PoolCapacityProblems()...)
	out = append(out, c.PoolRunnerGroupProblems()...)
	out = append(out, c.leaseProblems()...)
	out = append(out, c.registrationProblems(ctx)...)
	out = append(out, c.loopProblems()...)
	out = append(out, c.updateProblems()...)
	out = append(out, c.backupProblems(ctx)...)
	gather("AI Context", c.aiContextProblems)
	gather("Kennel Club", c.kennelProblems)
	gather("polling", c.pollerProblems)
	gather("runners", c.runnerProblems)
	gather("runner cleanup", c.cleanupProblems)
	gather("stuck runners", c.notProgressingProblems)
	gather("capacity-demand deliveries", c.capacityDeliveryProblems)
	gather("infrastructure providers", c.machineProblems)
	gather("the private connection", c.privateConnectionProblems)

	// An incomplete list says so, at the top, in the same shape as everything
	// else on it. A list that quietly drops a section is worse than an error,
	// because it is indistinguishable from a healthy fleet.
	if len(missing) > 0 {
		out = append(out, Problem{
			Code:     "controller.problems_partial",
			Severity: config.SeverityError,
			Title:    "this list is incomplete",
			Detail: "the controller could not read " + strings.Join(missing, ", ") +
				", so problems from " + section(len(missing)) + " are missing here.",
			Fix: "check the controller log for the query that failed, and that the database is readable and not out of disk.",
		})
	}

	// Whose each one is, in one pass over the finished list. Anything that
	// already said -- the validator's findings -- keeps what it said.
	for i := range out {
		if out[i].Audience == "" {
			out[i].Audience = audienceFor(out[i].Code)
		}
	}

	// Errors first, then warnings, then a stable order so the list does not
	// reshuffle itself between refreshes.
	slices.SortStableFunc(out, func(a, b Problem) int {
		if r := severityRank(a.Severity) - severityRank(b.Severity); r != 0 {
			return r
		}
		if r := strings.Compare(a.Code, b.Code); r != 0 {
			return r
		}
		return strings.Compare(a.Title, b.Title)
	})
	return out, nil
}

// machineProblems is everything wrong with the half of the fleet that spends
// money: a hypervisor that cannot be reached, a machine that never arrived, a
// resource nothing can account for, and the switches that are holding it all.
//
// Three of the codes change severity with the circumstances, on
// pool.no_capacity's pattern, and the reason is the same each time: the fault
// is the same, and whether anybody is waiting on it is what decides whether
// somebody should be woken up.
func (c *Controller) machineProblems(ctx context.Context, out *[]Problem) error {
	if !c.cfg().Provider.Enabled {
		return nil
	}
	providers, err := c.st.ListProviders(ctx)
	if err != nil {
		return fmt.Errorf("listing providers: %w", err)
	}
	if len(providers) == 0 {
		return nil
	}
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}
	pools = c.sizingPools(pools)
	queued, err := c.st.ListQueuedJobs(ctx)
	if err != nil {
		return fmt.Errorf("listing queued jobs: %w", err)
	}
	now := c.Now()
	troubles := c.providerTroubles()
	orphans := c.ProviderOrphans()
	unservable := unservableProviders(providers, pools, now)

	for _, p := range providers {
		machines, err := c.st.ListMachinesForProvider(ctx, p.ID)
		if err != nil {
			return fmt.Errorf("listing machines for provider %s: %w", p.Name, err)
		}
		c.providerTroubleProblems(out, p, troubles[p.ID], machines, len(queued))
		c.machineStateProblems(out, p, machines, now)
		if names := orphans[p.ID]; len(names) > 0 {
			*out = append(*out, Problem{
				Code:     "provider.orphan_found",
				Severity: config.SeverityError,
				Title:    fmt.Sprintf("%s has %s Zoomies cannot account for", p.Name, plural(len(names), "resource")),
				Detail: fmt.Sprintf("%s wear this fleet's naming, and no machine row names them: %s. "+
					"Nothing has been deleted and nothing will be.", plural(len(names), "resource"),
					strings.Join(names, ", ")),
				Fix: "review them on the provider's orphan tab. A resource left over from a lost database is yours " +
					"to remove by hand; one still doing work belongs to something else.",
				TargetKind: "provider", TargetID: p.ID,
			})
		}
		if slices.Contains(unservable, p.ID) {
			*out = append(*out, Problem{
				Code:     "provider.unservable",
				Severity: config.SeverityWarning,
				Title:    fmt.Sprintf("no pool could ever run work on %s's machines", p.Name),
				Detail: "the machines this provider offers match no enabled pool's backend, platform or host " +
					"selector, so anything it buys would sit idle and be paid for.",
				Fix: "give the provider's machines the labels a pool selects on, or change its backend and " +
					"platform to something a pool asks for.",
				TargetKind: "provider", TargetID: p.ID,
			})
		}
		if p.LastCheckAt == nil || p.LastCheckAt.Before(p.UpdatedAt) {
			*out = append(*out, Problem{
				Code:     "provider.template_unverified",
				Severity: config.SeverityWarning,
				Title:    fmt.Sprintf("%s has not been checked since its settings changed", p.Name),
				Detail: "nothing has confirmed that this provider's credential, template and placement still work. " +
					"The first machine it builds is where that would otherwise be discovered.",
				Fix:        "run the connection check on the provider's page, which changes nothing and says what it found.",
				TargetKind: "provider", TargetID: p.ID,
			})
		}
		if held := c.provisioningHeld(p, now); held != "" && !c.Fenced().Fenced {
			*out = append(*out, Problem{
				Code:     "provider.provisioning_paused",
				Severity: config.SeverityInfo,
				Title:    fmt.Sprintf("%s is not buying machines", p.Name),
				Detail:   held,
				Fix: "nothing is stranded: draining, deleting, recovery and the ownership sweep all continue. " +
					"Resume the provider when whatever this was for is over.",
				TargetKind: "provider", TargetID: p.ID,
			})
		}
		if _, err := c.providers.Get(p.Kind); err != nil {
			*out = append(*out, Problem{
				Code:     "provider.contract_unsupported",
				Severity: config.SeverityError,
				Title:    fmt.Sprintf("this build cannot work with %s", p.Name),
				Detail:   err.Error(),
				Fix: "upgrade whichever side is older. The machines this provider already owns stay visible, " +
					"drainable and deletable: a version mismatch never strands a running machine.",
				TargetKind: "provider", TargetID: p.ID,
			})
		}
	}
	return nil
}

// providerTroubleProblems turns the last failure a provider gave us into the
// entry an operator can act on.
func (c *Controller) providerTroubleProblems(out *[]Problem, p *store.Provider, t providerTrouble, machines []*store.Machine, queued int) {
	if t.Kind == "" {
		return
	}
	midOperation := 0
	for _, m := range machines {
		if m.State.Pending() {
			midOperation++
		}
	}
	since := t.At
	switch t.Kind {
	case provider.FailureUnreachable:
		// A hypervisor nobody is waiting on is a warning; one with a machine
		// half-built behind it is an outage, because that machine is being
		// paid for and cannot be finished or released.
		severity := config.SeverityWarning
		title := fmt.Sprintf("%s could not be reached", p.Name)
		if midOperation > 0 {
			severity = config.SeverityError
			title = fmt.Sprintf("%s could not be reached and %s waiting on it",
				p.Name, plural(midOperation, "machine")+" is")
		}
		*out = append(*out, Problem{
			Code: "provider.unreachable", Severity: severity, Title: title, Detail: t.Detail,
			Fix: firstNonEmpty(t.Remedy, "check the endpoint on the provider's page, and that this controller can "+
				"reach it: nothing is created or deleted while it cannot be."),
			TargetKind: "provider", TargetID: p.ID, Since: &since,
		})
	case provider.FailureAuth, provider.FailurePermission:
		*out = append(*out, Problem{
			Code:     "provider.credentials_refused",
			Severity: config.SeverityError,
			Title:    fmt.Sprintf("%s refused this controller's credential", p.Name),
			Detail:   t.Detail,
			Fix: firstNonEmpty(t.Remedy, "replace the credential on the provider's page. A credential that is valid "+
				"but not allowed names the privilege it is missing in the detail above."),
			TargetKind: "provider", TargetID: p.ID, Since: &since,
		})
	case provider.FailureQuota:
		// The pool.no_capacity circumstance: a provider that is full with
		// nothing queued is the system working, and the next machine released
		// clears it.
		severity := config.SeverityWarning
		if queued > 0 {
			severity = config.SeverityError
		}
		*out = append(*out, Problem{
			Code:     "provider.quota_exhausted",
			Severity: severity,
			Title:    fmt.Sprintf("%s has no capacity for another machine", p.Name),
			Detail:   t.Detail,
			Fix: firstNonEmpty(t.Remedy, "free capacity at the provider, or lower what this fleet asks of it. "+
				"Draining and deleting carry on, so a machine released here clears this on its own."),
			TargetKind: "provider", TargetID: p.ID, Since: &since,
		})
	}
}

// machineStateProblems says what is wrong with individual machines: the ones
// that never arrived, the ones nothing can prove we own, and the deletes that
// have not confirmed.
func (c *Controller) machineStateProblems(out *[]Problem, p *store.Provider, machines []*store.Machine, now time.Time) {
	deleteTimeout := c.cfg().Provider.DeleteTimeout
	for _, m := range machines {
		switch {
		case m.State == store.MachineQuarantined || m.OwnershipError != "":
			detail := m.OwnershipError
			if detail == "" {
				detail = m.Message
			}
			*out = append(*out, Problem{
				Code:     "provider.ownership_unverified",
				Severity: config.SeverityError,
				Title:    fmt.Sprintf("machine %s is quarantined", m.Name),
				Detail:   detail,
				Fix: "nothing will act on this machine until a person does. Check the provider's console, then " +
					"either release the row -- which forgets the resource without touching it -- or delete the " +
					"resource by hand.",
				TargetKind: "machine", TargetID: m.ID,
			})
		case m.State == store.MachineFailed && m.BootstrapError != "":
			*out = append(*out, Problem{
				Code:     "provider.bootstrap_failed",
				Severity: config.SeverityError,
				Title:    fmt.Sprintf("machine %s came up and its agent never did", m.Name),
				Detail:   m.BootstrapError,
				Fix: "the machine is running and is still being paid for. Check that the template carries the " +
					"zoomies agent, installed and disabled, with no agent.json in it -- two machines sharing one " +
					"agent identity is the failure that looks like a host flapping.",
				TargetKind: "machine", TargetID: m.ID,
			})
		case m.State == store.MachineFailed:
			*out = append(*out, Problem{
				Code:     "provider.machine_failed",
				Severity: config.SeverityWarning,
				Title:    fmt.Sprintf("machine %s never reached ready", m.Name),
				Detail:   firstNonEmpty(m.ProviderError, m.Message, "the machine was given up on without saying why"),
				Fix: "the row is kept because the resource behind it may still exist. Release it once you have " +
					"checked the provider's console.",
				TargetKind: "machine", TargetID: m.ID,
			})
		case m.State == store.MachineDeleting && deleteTimeout > 0 &&
			m.DeleteStartedAt != nil && now.Sub(*m.DeleteStartedAt) > deleteTimeout:
			since := *m.DeleteStartedAt
			*out = append(*out, Problem{
				Code:     "provider.delete_pending",
				Severity: config.SeverityWarning,
				// The threshold and the start time, not the running total: a
				// title that counted seconds was a different problem on every
				// pass, and the list is only sent when it changes.
				Title: fmt.Sprintf("machine %s has been deleting for over %s", m.Name, roundDuration(deleteTimeout)),
				Detail: fmt.Sprintf("%s has not yet confirmed that %s is gone, though the delete began at %s, and a resource nobody has confirmed "+
					"gone is a resource somebody may still be paying for.", p.Name, m.ResourceID, since.UTC().Format(time.RFC3339)),
				Fix:        "Zoomies keeps asking. If the provider's console shows it gone, the next sweep records it.",
				TargetKind: "machine", TargetID: m.ID, Since: &since,
			})
		}
	}
}

func (c *Controller) capacityDeliveryProblems(ctx context.Context, out *[]Problem) error {
	rows, err := c.st.ListCapacityDemandDeliveries(ctx)
	if err != nil {
		return fmt.Errorf("listing capacity-demand deliveries: %w", err)
	}
	for _, d := range rows {
		if d.DeliveredAt != nil || d.LastError == "" {
			continue
		}
		since := d.AttemptedAt
		*out = append(*out, Problem{Code: "capacity_demand.delivery_failed", Severity: config.SeverityWarning, Title: "external capacity provisioner did not accept the latest event", Detail: fmt.Sprintf("%s after %d attempts (HTTP %d): %s", d.EventType, d.Attempts, d.StatusCode, d.LastError), Fix: "check capacity_demand.destination_url, receiver availability, and the shared signing secret; Zoomies will retry on reconciliation.", TargetKind: "pool", TargetID: d.PoolID, Since: since})
	}
	return nil
}

func severityRank(s config.Severity) int {
	switch s {
	case config.SeverityError:
		return 0
	case config.SeverityWarning:
		return 1
	default:
		return 2
	}
}

func (c *Controller) hostProblems(ctx context.Context, out *[]Problem) error {
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return fmt.Errorf("listing hosts: %w", err)
	}
	queued, err := c.st.ListQueuedJobs(ctx)
	if err != nil {
		return fmt.Errorf("listing queued jobs: %w", err)
	}
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}
	pools = c.sizingPools(pools)
	poolByID := make(map[string]*store.Pool, len(pools))
	for _, p := range pools {
		poolByID[p.ID] = p
	}
	now := c.Now()

	for _, h := range hosts {
		// The name is the agent's to choose, and the fix below goes through
		// RemedyText, which turns a backtick pair into a command to copy.
		label := naming.ForSentence(h.Name)
		// Raised before health and cordon are considered, and without a
		// `continue`: two agents on one host is true whatever else that host
		// is, and it is very often the explanation for the other two.
		if h.AgentSessionAltAt != nil && now.Sub(*h.AgentSessionAltAt) < duplicateAgentWindow {
			since := *h.AgentSessionAltAt
			*out = append(*out, Problem{
				Code:     "host.duplicate_agent",
				Severity: config.SeverityWarning,
				Title:    fmt.Sprintf("two agents appear to be running as host %s", label),
				Detail: fmt.Sprintf("this host's credentials have been used by two agent sessions in turn %s. "+
					"An agent takes a new session each time it starts and never goes back to an old one, so "+
					"alternation means a second agent holds a copy of this host's token -- usually a cloned VM, "+
					"or a state directory copied to another machine. They are splitting this host's tasks between "+
					"them, so each sees only half of its own work.", plural(h.AgentSessionAlternations, "time")),
				Fix: fmt.Sprintf("find the second machine reporting as %s and stop its agent, then re-join it with "+
					"its own join token so it becomes a host of its own (zoomies agent join). Zoomies does not "+
					"refuse either session, because both are running real jobs and picking one would end the other's.", label),
				TargetKind: "host", TargetID: h.ID, Since: &since,
			})
		}
		if !h.Healthy(now) {
			since := h.LastHeartbeat
			severity := config.SeverityWarning
			// The time of the last heartbeat rather than how long ago it was:
			// the list is sent only when it changes, and "for 41s" is a new
			// problem every second the host stays silent.
			last := h.LastHeartbeat.UTC().Format(time.RFC3339)
			detail := fmt.Sprintf("no heartbeat since %s; the agent process may be stopped, or it cannot reach this controller.", last)
			if h.ActiveRunners > 0 {
				// Runners on a silent host are unaccounted for, which is worse
				// than a spare host being down.
				severity = config.SeverityError
				detail = fmt.Sprintf("no heartbeat since %s, with %s recorded on it, so their state is unknown.",
					last, plural(h.ActiveRunners, "runner"))
			}
			*out = append(*out, Problem{
				Code:       "host.unhealthy",
				Severity:   severity,
				Title:      fmt.Sprintf("host %s has stopped sending heartbeats", label),
				Detail:     detail,
				Fix:        fmt.Sprintf("check that the zoomies agent is running on %s and can reach %s.", label, c.controllerAddress()),
				TargetKind: "host", TargetID: h.ID, Since: &since,
			})
			continue
		}
		if !h.Cordoned {
			continue
		}
		// Only the queued work this host could take counts against it: a
		// cordoned arm64 host has nothing to do with a queue of jobs for a
		// pool it never offered, and blaming it sends an operator to uncordon
		// a machine that would change nothing. Fit is left out on purpose: a
		// cordoned host with queued work it has no room for is still worth
		// saying.
		couldRun := 0
		for _, j := range queued {
			if p := poolByID[j.PoolID]; p != nil && scheduler.HostOffers(h, p) && scheduler.HostIsPlatform(h, p) && scheduler.HostSelects(h, p) {
				couldRun++
			}
		}
		if couldRun > 0 {
			*out = append(*out, Problem{
				Code:       "host.cordoned_with_work",
				Severity:   config.SeverityWarning,
				Title:      fmt.Sprintf("host %s is cordoned with %s queued that it could run", label, plural(couldRun, "job")),
				Detail:     "a cordoned host keeps its runners but accepts no new ones, so its capacity is not available to the queue.",
				Fix:        fmt.Sprintf("uncordon %s on the Hosts page if the maintenance it was cordoned for is over.", label),
				TargetKind: "host", TargetID: h.ID,
			})
		}
	}
	return nil
}

// hostSkewProblems names the hosts running a different release from this
// controller.
//
// It is a warning rather than an error, and that is the judgement: a fleet
// mid-upgrade is *supposed* to look like this for a while, and an error for
// every host between two releases would train an operator to ignore the list.
// What it protects against is the fleet that stays this way -- a host somebody
// forgot, running last quarter's agent, whose odd behaviour has an explanation
// nobody has thought to look for.
//
// A host ahead of its controller gets its own sentence, because the fix is the
// other machine: that is the direction the policy calls unsupported, and
// telling an operator to upgrade the agent would be telling them to make it
// worse.
func (c *Controller) hostSkewProblems(ctx context.Context, out *[]Problem) error {
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return fmt.Errorf("listing hosts: %w", err)
	}
	var behind, ahead, differs []string
	for _, h := range hosts {
		// An incompatible host has a louder problem of its own, and saying
		// both would be two entries about one machine.
		if h.Incompatible {
			continue
		}
		switch version.CompareBuilds(h.Version, version.Version) {
		case version.SkewBehind:
			behind = append(behind, naming.ForSentence(h.Name))
		case version.SkewAhead:
			ahead = append(ahead, naming.ForSentence(h.Name))
		case version.SkewDiffers:
			differs = append(differs, naming.ForSentence(h.Name))
		}
	}
	if len(behind)+len(ahead)+len(differs) == 0 {
		return nil
	}

	var detail []string
	if len(behind) > 0 {
		detail = append(detail, fmt.Sprintf("%s behind: %s", plural(len(behind), "host"), strings.Join(behind, ", ")))
	}
	if len(ahead) > 0 {
		detail = append(detail, fmt.Sprintf("%s ahead of this controller: %s", plural(len(ahead), "host"), strings.Join(ahead, ", ")))
	}
	if len(differs) > 0 {
		detail = append(detail, fmt.Sprintf("%s on a build this controller cannot order against its own: %s",
			plural(len(differs), "host"), strings.Join(differs, ", ")))
	}
	// An agent is installed from a published release asset, so the version to
	// name here is only nameable when this controller came from a release.
	//
	// It used to name version.Version unconditionally. On a controller built
	// from main -- what the :dev and :main images are -- that is
	// main-sha-abc1234, and no release carries it: an operator following the
	// advice reached a 404 and came back to find the fleet exactly as it was.
	// Worse, that is precisely the fleet this problem fires on, because a build
	// from main cannot be ordered against a release at all, so every such host
	// lands in `differs`.
	//
	// The command is in backticks because RemedyText hangs a copy button on
	// each one, which is the difference between advice and something an
	// operator can act on at 3am.
	fix := agentUpgradeFix("The protocol still matches, so they are placing work as normal in the meantime.")
	if len(ahead) > 0 {
		fix = "upgrade this controller to the newest release in the fleet, then the agents: an agent ahead of its controller is the direction nothing is tested in. " + fix
	}
	*out = append(*out, Problem{
		Code:     "host.version_behind",
		Severity: config.SeverityWarning,
		Title:    "some hosts are running a different release from this controller",
		Detail: strings.Join(detail, "; ") + ". This controller is " + version.Version +
			". A fleet part-way through an upgrade looks like this and clears itself; one that stays this way has a host somebody has forgotten.",
		Fix: fix,
	})
	return nil
}

// hostResourceProblems is everything the resource model has to say about the
// hosts: the ones that have never said what machine they are, the ones given
// more slots than the machine can carry, the ones whose daemon cannot apply
// the limits their runners are given or has not said whether it can, the
// ones the controller has throttled -- and, after them, the pools whose
// limits nothing on their backend enforces.
//
// The unmeasured host used to be a note and is now a warning whenever default
// limits are on: a host that reports no CPU, no memory and no disk is placed
// by slots alone, exactly as every host was before the resource model existed,
// but its runners are also given no default limit, because their share of a
// machine nobody has measured cannot be computed -- so a pool with no limits
// of its own runs unlimited there, which is the shape defaults exist to stop.
// With defaults off nothing is different about such a host and it stays a
// note, so an operator can tell "old agent" from "broken agent".
//
// The process-pool warning is a promise that is not kept. A pool's `resources`
// become cgroup limits on Docker and Podman, including the docker-in-docker
// sidecar, and the process backend applies none of them: a runner there can
// use the whole machine. The reservation still holds the room -- the fleet
// does not oversubscribe -- but the room is bookkeeping, and a job that runs
// away takes the host with it.
func (c *Controller) hostResourceProblems(ctx context.Context, out *[]Problem) error {
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return fmt.Errorf("listing hosts: %w", err)
	}
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}
	pools = c.sizingPools(pools)
	defaults := c.cfg().Scheduler.DefaultRunnerLimits
	var unknown, unverified, throttled []string
	var throttledID string
	for _, h := range hosts {
		// An incompatible host is already excluded from placement and already
		// says so; a second entry about the same machine helps nobody.
		if h.Incompatible {
			continue
		}
		label := naming.ForSentence(h.Name)
		a := h.Allocatable()
		if !a.CPUsKnown && !a.MemoryKnown && !a.DiskKnown {
			unknown = append(unknown, label)
		}
		// A cordoned host is one an operator is already dealing with, and
		// nothing below places onto it until they are done.
		if h.Cordoned {
			continue
		}
		if h.Throttle.Active() {
			throttled = append(throttled, label+": "+scheduler.ThrottleReason(h))
			throttledID = h.ID
		}
		if p, ok := overprovisionedProblem(h, pools, defaults); ok {
			*out = append(*out, p)
		}
		for _, kind := range h.Backends {
			if kind == string(store.BackendProcess) {
				continue
			}
			// A host with no stored probe at all is left out: every live
			// host stores one on its first heartbeat, so a row without one
			// is a fixture, not an agent that failed to say.
			info, ok := h.BackendInfo.Find(store.BackendKind(kind))
			if !ok {
				continue
			}
			if p, ok := sharedFolderProblem(h, info, pools); ok {
				*out = append(*out, p)
			}
			if !info.Limits.Known {
				if defaults && (a.CPUsKnown || a.MemoryKnown) && !slices.Contains(unverified, label) {
					unverified = append(unverified, label)
				}
				continue
			}
			if p, ok := unenforceableProblem(h, info); ok {
				*out = append(*out, p)
			}
		}
	}
	if len(unknown) > 0 {
		p := Problem{
			Code:     "host.resources_unknown",
			Severity: config.SeverityInfo,
			Title:    "some hosts have not reported what machine they are",
			Detail: fmt.Sprintf("%s reporting no CPUs, memory or disk: %s. They are placed by slot count alone, which is how every host was placed before agents learnt to measure themselves, so nothing is wrong -- but a pool's resource limits cannot be fitted against a machine nobody has measured.",
				plural(len(unknown), "host"), strings.Join(unknown, ", ")),
			// Named the same way as host.version_behind, and for the same
			// reason: "upgrade the agent" is only advice if there is an agent
			// to upgrade to. The figures really do appear on the next
			// heartbeat with no re-join -- the agent measures on every beat --
			// so the only thing standing between this host and its size is a
			// build that has the code, and before v0.2-beta-plus none did.
			Fix: agentUpgradeFix("the figures appear on the next heartbeat, with no re-join."),
		}
		if defaults {
			p.Severity = config.SeverityWarning
			p.Detail = fmt.Sprintf("%s reporting no CPUs, memory or disk: %s. They are placed by slot count alone, and their runners are given no default CPU or memory limit, because a runner's share of a machine nobody has measured cannot be computed -- so a pool that sets no limits of its own runs unlimited there, and every runner on the host can take every core.",
				plural(len(unknown), "host"), strings.Join(unknown, ", "))
		}
		*out = append(*out, p)
	}
	if len(unverified) > 0 {
		*out = append(*out, Problem{
			Code:     "host.limits_unverified",
			Severity: config.SeverityInfo,
			Title:    "some hosts have not said whether their daemon can apply limits",
			Detail: fmt.Sprintf("%s whose agent predates the limits probe: %s. A runner there is given no default CPU or memory limit, because a limit sent to a daemon that cannot apply it fails the create, and the agent has not said whether its daemon can. Pools that set their own limits are unaffected.",
				plural(len(unverified), "host"), strings.Join(unverified, ", ")),
			Fix: agentUpgradeFix("the probe arrives with the next heartbeat, with no re-join, and defaults start on the next runner."),
		})
	}
	if len(throttled) > 0 {
		p := Problem{
			Code:     "host.throttled",
			Severity: config.SeverityWarning,
			Title:    "some hosts are throttled after sustained pressure",
			Detail:   strings.Join(throttled, ". ") + ".",
			Fix: "wait for it to lift, lower the host's capacity or the pools' limits so its runners fit the machine, " +
				"or lift it from the host card (Lift the throttle) once the cause is fixed. A lifted throttle comes back on the next heartbeat if the pressure is still there.",
		}
		if len(throttled) == 1 {
			p.TargetKind, p.TargetID = "host", throttledID
		}
		*out = append(*out, p)
	}

	var unenforced []*store.Pool
	var unsized []string
	for _, p := range pools {
		if p == nil || !p.Enabled {
			continue
		}
		// A pool that leaves its size to its host is given one slot's share
		// as a real cgroup limit -- unless the setting that hands it out is
		// off, and then it is given nothing at all. That is the one shape
		// where "automatic" and "unlimited" are the same thing, and it is
		// worth saying out loud rather than leaving an operator to read two
		// settings pages against each other.
		if !defaults && p.Automatic() && p.Backend != store.BackendProcess {
			unsized = append(unsized, p.Name)
		}
		if p.Backend != store.BackendProcess {
			continue
		}
		if p.Resources.CPUs > 0 || p.Resources.MemoryMB > 0 || p.Resources.DiskGB > 0 {
			unenforced = append(unenforced, p)
		}
	}
	if len(unsized) > 0 {
		*out = append(*out, Problem{
			Code:     "pool.size_unlimited",
			Severity: config.SeverityWarning,
			Title:    plural(len(unsized), "pool") + " leave their size to the host, and nothing is handing one out",
			Detail: fmt.Sprintf("%s set no CPU or memory limit, which normally means each runner is given one slot's share of the machine it lands on. scheduler.default_runner_limits is off, so that share is charged against the host and never applied: a host's worth of these runners can each take every core at once, which is the shape that stops the Docker daemon answering.",
				strings.Join(unsized, ", ")),
			Fix:        "turn scheduler.default_runner_limits back on, which is the default, or give each of these pools a size of its own.",
			TargetKind: "pool",
		})
	}
	// One problem per pool, each with its own target: the UI dismisses and
	// snoozes by code and target, so a warning that named no pool let muting
	// one pool's mute every other's, and gave none of them an "Open the pool"
	// link. The name is in the title for the drawer's row key, as it is for
	// pool.dangerous.
	for _, p := range unenforced {
		*out = append(*out, Problem{
			Code:     "pool.resources_unenforced",
			Severity: config.SeverityWarning,
			Title:    "pool " + p.Name + " sets resource limits its backend does not apply",
			Detail: fmt.Sprintf("%s runs on the process backend, which starts a runner as a plain process with no cgroup, so its CPU, memory and disk limits bind nothing. The scheduler still holds that much room on the host, so the fleet does not oversubscribe -- but a job that runs away can take the machine with it.",
				p.Name),
			Fix:        "move the pool to the docker or podman backend, where the same limits become cgroup limits, or clear them and rely on the host's capacity.",
			TargetKind: "pool",
			TargetID:   p.ID,
		})
	}
	return nil
}

// runnerProfileProblems is profileProblems over the fleet as it stands, with
// the pools sized as the scheduler sizes them.
func (c *Controller) runnerProfileProblems(ctx context.Context, out *[]Problem) error {
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return fmt.Errorf("listing hosts: %w", err)
	}
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}
	*out = append(*out, profileProblems(hosts, c.sizingPools(pools), c.cfg().Runners)...)
	return nil
}

func (c *Controller) installationProblems(ctx context.Context, out *[]Problem) error {
	insts, err := c.st.ListInstallations(ctx)
	if err != nil {
		return fmt.Errorf("listing installations: %w", err)
	}
	for _, i := range insts {
		if i.Healthy() {
			continue
		}
		*out = append(*out, Problem{
			Code:       "installation.unhealthy",
			Severity:   config.SeverityError,
			Title:      fmt.Sprintf("the GitHub App installation for %s is not usable", i.Target),
			Detail:     i.LastError,
			Fix:        "check the App's installation, permissions and private key on the Installations page; no runner can be created for this target until it works.",
			TargetKind: "installation", TargetID: i.ID, Since: i.LastCheckedAt,
		})
	}
	return nil
}

// fenceProblems says the fleet is held, which is otherwise invisible: a fenced
// controller looks exactly like a healthy one with nothing to do.
//
// It is the highest-consequence entry the drawer can carry, because everything
// else on it is a thing that went wrong and this is a thing somebody chose.
// The fix names the three checks a restore leaves undone rather than only the
// route that lifts it: an operator who lifts the fence without making them has
// spent the fence for nothing.
func (c *Controller) fenceProblems() []Problem {
	f := c.Fenced()
	if !f.Fenced {
		return nil
	}
	detail := "The scheduler is deciding as normal and applying none of it: no runner is created, drained or removed, nothing is reaped from GitHub, and the fallback poller is not sweeping. " +
		"Everything below and on the Overview is what this controller would do the moment the fence is lifted."
	if f.Reason != "" {
		detail = f.Reason + ". " + detail
	}
	return []Problem{{
		Code:     "recovery.fenced",
		Severity: config.SeverityError,
		Title:    "this fleet is held for recovery and is doing nothing",
		Detail:   detail,
		Fix: "check the three things a restore does not bring with it -- the external URL this fleet answers on, the agents, and the runners that were live when the backup was taken -- " +
			"then lift the fence with POST /api/v1/recovery/unfence. If another controller is still running on the original database, stop it first.",
	}}
}

// keyProblems answers "is this the key that sealed what is in the database?".
//
// It is a separate code from installation.unhealthy because it is a different
// fault with a different fix: an installation GitHub has revoked is fixed on
// GitHub, and a key that does not open its own database is fixed by putting
// the right key file back. The two are told apart by how many installations
// fail at once -- all of them, and immediately after a restore -- which is
// exactly the observation an operator makes last, if at all.
//
// It opens rather than compares fingerprints: a fingerprint says which key
// this is, and only opening says whether it works.
func (c *Controller) keyProblems(ctx context.Context, out *[]Problem) error {
	insts, err := c.st.ListInstallations(ctx)
	if err != nil {
		return fmt.Errorf("listing installations: %w", err)
	}
	var unreadable []string
	for _, i := range insts {
		if len(i.PrivateKeyEnc) == 0 {
			continue
		}
		if _, err := c.key.Open(i.PrivateKeyEnc); err != nil {
			unreadable = append(unreadable, i.Target)
		}
	}
	if len(unreadable) == 0 {
		return nil
	}
	*out = append(*out, Problem{
		Code:     "crypto.key_mismatch",
		Severity: config.SeverityError,
		Setting:  "security.encryption_key_file",
		Title:    "this instance's encryption key does not open its own database",
		Detail: fmt.Sprintf("the sealed GitHub App credentials for %s cannot be decrypted with the key this controller is running. "+
			"Nothing can authenticate to GitHub, so no runner can be created and no job will be claimed.", strings.Join(unreadable, ", ")),
		Fix: "put the encryption key that was in use when these installations were added back at security.encryption_key_file " +
			"(or pass it in ZOOMIES_ENCRYPTION_KEY) and restart. If the key is genuinely lost, delete and re-add the installations.",
	})
	return nil
}

func (c *Controller) webhookProblems(ctx context.Context, out *[]Problem) error {
	rejected, err := c.st.CountFailedDeliveries(ctx, c.Now().Add(-problemWindow))
	if err != nil {
		return fmt.Errorf("counting failed webhook deliveries: %w", err)
	}
	if rejected > 0 {
		// No Since: the only time to hand over is the edge of the window, which
		// is an hour before whenever this is asked. It moved on every pass, so
		// the list was re-sent each time, and it told the status page the
		// fault began an hour ago for as long as it stood.
		*out = append(*out, Problem{
			Code:     "webhook.rejected",
			Severity: config.SeverityWarning,
			Setting:  "github.webhook_path",
			Title:    fmt.Sprintf("%s rejected in the last hour", pluralDeliveries(rejected)),
			Detail:   "a rejected delivery is one whose signature did not verify. Either the App's webhook secret no longer matches the one Zoomies holds, or something other than GitHub is posting to this endpoint.",
			Fix:      "compare the webhook secret on the GitHub App with the one on the Installations page, then use GitHub's Redeliver button.",
		})
	}

	// Accepted deliveries only. A rejected one proves that something reached
	// the address, not that GitHub is delivering: the endpoint is public and
	// unauthenticated by necessity, so a single probe from a stranger would
	// otherwise stand in for a working webhook and take this warning -- and,
	// with the poller off, the error that says nothing is scaling -- off the
	// screen for the whole retention window. The poller stands down on the same
	// measure, and for the same reason.
	last, err := c.st.LastAcceptedDeliveryAt(ctx)
	if err != nil {
		return fmt.Errorf("reading the last accepted webhook delivery time: %w", err)
	}
	// An instance with no installation is not yet configured, and telling its
	// operator that no webhook has arrived would be noise on top of the setup
	// they have not finished. Once an installation exists, silence is a fault.
	insts, err := c.st.ListInstallations(ctx)
	if err != nil {
		return fmt.Errorf("listing installations: %w", err)
	}
	if last.IsZero() && len(insts) > 0 {
		p := Problem{
			Code:     "webhook.never_received",
			Severity: config.SeverityWarning,
			Setting:  "server.external_url",
			Title:    "no webhook has ever arrived, so scaling is running on the poller",
			Detail: fmt.Sprintf("Zoomies has never received a delivery that verified, so it is discovering queued jobs by polling GitHub every %s "+
				"instead of within a second of them being queued.", c.pollInterval()),
			Fix: fmt.Sprintf("point the App's webhook at %s and check that GitHub can reach it.", c.webhookURLOrPath()),
		}
		if !c.cfg().GitHub.PollFallback {
			// With no webhooks and no poller, nothing will ever start a runner.
			p.Severity = config.SeverityError
			p.Title = "no webhook has ever arrived and the fallback poller is off, so nothing is scaling"
			p.Detail = "Zoomies has never received a delivery that verified, and github.poll_fallback is false, so no queued job will ever be noticed."
			p.Fix = fmt.Sprintf("point the App's webhook at %s, or set github.poll_fallback to true.", c.webhookURLOrPath())
		}
		*out = append(*out, p)
	}
	return nil
}

// controllerAddress is how an agent reaches this controller, phrased for a
// message even when the external URL has not been set.
func (c *Controller) controllerAddress() string {
	if u := c.cfg().Server.ExternalURL; u != "" {
		return u
	}
	return "this controller (server.external_url is not set, so Zoomies cannot name the address)"
}

// webhookURLOrPath names the delivery target, falling back to the path when
// the external URL has not been configured -- which is itself one of the
// findings above, so the fix stays actionable either way.
func (c *Controller) webhookURLOrPath() string {
	if u := c.cfg().WebhookURL(); u != "" {
		return u
	}
	return c.cfg().GitHub.WebhookPath + " (set server.external_url so Zoomies can tell you the full URL)"
}

// PoolCapacityProblems reports the pools the scheduler wanted to grow and could
// not place anywhere. It is exported because the pool's own page shows it too:
// "why is this pool not running anything?" is asked on the pool, not only on
// the Overview.
//
// This is the failure that looks exactly like health: the pool is enabled, its
// labels match the queue, every host is connected, and no runner is ever
// created because none of those hosts offers the pool's backend or matches its
// host selector. Nothing else in the product says so -- a scaling event is only
// written when the size actually moved -- so a fleet in this state answers
// "why is nothing running?" with silence unless it is reported here.
// leaseProblems reports that this controller no longer holds the database's
// lease, which means another one has taken it and both are now scheduling.
//
// An error rather than a warning, and one that never clears by itself: there
// is no state this process can reach on its own that makes it safe again. Two
// controllers over one database mint runner credentials against each other,
// reclaim each other's hosts and reap each other's workloads, and every
// symptom of it looks like a bug somewhere else -- which is the reason to name
// it here rather than leave an operator to work it out.
// registrationProblems reports the runner creations this fleet held back at
// its own credential-minting limit.
//
// scheduler.registration_concurrency bounds outstanding credential requests
// per installation, and defaults to one. Everything past it is deferred to a
// later pass. That is a deliberate throttle and mostly a good one -- it keeps
// a thundering herd from spending an installation's whole GitHub quota in a
// second -- but until now it was the only limit in the fleet that said
// nothing when it bound.
//
// The shape of the failure it caused is why this exists. The pool reports
// jobs waiting and nowhere to run them. The operator reads that, looks at the
// hosts, sees them idle, and adds more. The new hosts do not help, because
// hosts were never what ran out, and nothing anywhere names the thing that
// did. A deferral is counted only after the scheduler has already chosen a
// host for the runner, so every one of these is a creation that had somewhere
// to go.
func (c *Controller) registrationProblems(ctx context.Context) []Problem {
	held, since := c.deferredMintsNow()
	if len(held) == 0 {
		return nil
	}
	limit := max(1, min(16, c.cfg().Scheduler.RegistrationConcurrency))

	names := map[string]string{}
	if insts, err := c.st.ListInstallations(ctx); err == nil {
		for _, in := range insts {
			names[in.ID] = in.Target
		}
	}

	out := make([]Problem, 0, len(held))
	for id, n := range held {
		target := names[id]
		if target == "" {
			target = id
		}
		p := Problem{
			Code:     "scheduler.registration_throttled",
			Severity: config.SeverityWarning,
			Title: fmt.Sprintf("%s is minting runner credentials one at a time",
				target),
			Detail: fmt.Sprintf("the last scheduling pass had a host chosen for %s and did not create %s, because %s already had %s in flight and scheduler.registration_concurrency is %d. The demand is kept and a later pass takes it, so this shows as runners appearing slowly rather than as anything failing -- and adding hosts will not change it, because hosts are not what ran out.",
				plural(n, "runner"), plural(n, "one"), target, plural(limit, "credential request"), limit),
			Fix:        fmt.Sprintf("raise scheduler.registration_concurrency if this installation's GitHub quota has room for it; it accepts 1 to 16. Leave it where it is if the fleet is deliberately gentle with %s's quota -- this says the throttle is working, not that it is wrong.", target),
			Setting:    "scheduler.registration_concurrency",
			TargetKind: "installation", TargetID: id,
		}
		if t, ok := since[id]; ok {
			at := t
			p.Since = &at
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Problem) int { return strings.Compare(a.TargetID, b.TargetID) })
	return out
}

func (c *Controller) leaseProblems() []Problem {
	held := c.leaseLost.Load()
	if held == nil {
		return nil
	}
	return []Problem{{
		Code:     "controller.lease_lost",
		Severity: config.SeverityError,
		Title:    "another controller has taken this database",
		Detail: fmt.Sprintf("this controller's lease is now held by %s, so two controllers are running against the same database. Both are scheduling: they will mint runners against each other, reclaim each other's hosts and remove each other's workloads.",
			held.Describe()),
		Fix: "stop one of them. If this one is the survivor, restart it with --takeover once the other is gone; a controller that has lost its lease does not take it back by itself, because two processes fighting over it is the same failure twice.",
	}}
}

// PoolRunnerGroupProblems reports the pools whose runners went into GitHub's
// default runner group because the group they asked for could not be resolved.
//
// It is a warning rather than a log line because runner-group policy can make
// a runner either broader than intended or completely ineligible. A pool put
// into a named group to fence its runners off and quietly placed in Default is
// wider than its operator asked for; a group that blocks public repositories
// leaves their matching jobs queued while its runners look healthy and idle.
func (c *Controller) PoolRunnerGroupProblems() []Problem {
	c.mu.Lock()
	notes := make([]runnerGroupNote, 0, len(c.runnerGroups))
	ids := make([]string, 0, len(c.runnerGroups))
	for id, n := range c.runnerGroups {
		ids = append(ids, id)
		notes = append(notes, n)
	}
	c.mu.Unlock()

	out := make([]Problem, 0, len(notes))
	for i, n := range notes {
		if n.PublicRepositoriesBlocked {
			out = append(out, Problem{
				Code:       "pool.runner_group_public_repositories_blocked",
				Severity:   config.SeverityWarning,
				Title:      fmt.Sprintf("pool %s: runner group %s blocks public repositories", n.PoolName, n.Group),
				Detail:     fmt.Sprintf("GitHub can register these runners and report them online, but group %s is not allowed to run jobs from public repositories. Matching jobs then remain queued while the runners appear idle.", n.Group),
				Fix:        fmt.Sprintf("in the GitHub organisation, open Settings > Actions > Runner groups > %s and enable Allow public repositories, or move this pool to a repository-scoped installation", n.Group),
				TargetKind: "pool", TargetID: ids[i],
			})
			continue
		}
		out = append(out, Problem{
			Code:     "pool.runner_group_unresolved",
			Severity: config.SeverityWarning,
			Title:    fmt.Sprintf("pool %s: its runners are registering in the default runner group", n.PoolName),
			Detail: fmt.Sprintf("%s, so GitHub is placing them in Default, which every repository the installation covers can reach. The pool asked for %s to keep its runners away from work that is not meant for them.",
				capitalise(n.Reason), n.Group),
			Fix: fmt.Sprintf("create the %s group on GitHub and give this pool's target access to it, or clear the pool's runner group; the runners already registered stay in Default until they are replaced.",
				n.Group),
			TargetKind: "pool", TargetID: ids[i],
		})
	}
	// A stable order, because the drawer is re-rendered on every pass and a
	// map's is not.
	slices.SortFunc(out, func(a, b Problem) int { return strings.Compare(a.TargetID, b.TargetID) })
	return out
}

func (c *Controller) PoolCapacityProblems() []Problem {
	plan, at := c.getLastPlan()
	if plan == nil || at.IsZero() {
		return nil
	}
	var out []Problem
	for _, pp := range plan.Pools {
		if pp.QuotaDeferredJobs > 0 {
			repositories := strings.Join(pp.QuotaDeferredRepositories, ", ")
			out = append(out, Problem{
				Code:     "pool.repository_scale_up_deferred",
				Severity: config.SeverityWarning,
				Title: fmt.Sprintf("pool %s deferred %s from scaling", pp.PoolName,
					plural(pp.QuotaDeferredJobs, "job")),
				Detail: fmt.Sprintf("The best-effort repository scale-up limit deferred runner creation for %s (%s). Compatible idle runners may still accept these jobs because GitHub controls assignment.",
					plural(len(pp.QuotaDeferredRepositories), "repository"), repositories),
				Fix:        "increase the pool repository scale-up limit or wait for that repository's active jobs to finish; use repository-specific pools and workflow labels if strict isolation is required",
				TargetKind: "pool", TargetID: pp.PoolID,
			})
		}
		if pp.Failing != "" {
			// The scheduler is holding the pool back because its runners keep
			// dying before they register. The failed runners themselves are
			// listed under runners.failed with their messages; this entry is
			// about the pool, and about the wait, which is otherwise invisible.
			severity := config.SeverityWarning
			if pp.QueuedMatched > 0 {
				severity = config.SeverityError
			}
			out = append(out, Problem{
				Code:       "pool.runners_failing",
				Severity:   severity,
				Title:      fmt.Sprintf("pool %s's runners are failing to start", pp.PoolName),
				Detail:     pp.Failing,
				Fix:        startFailureFix(pp.FailingFault),
				TargetKind: "pool", TargetID: pp.PoolID,
			})
		}
		if pp.Held != "" && pp.QueuedMatched > 0 {
			// The installation's hold is already raised as poller.paused, with
			// the moment it lifts. This entry is about the pool: it has jobs
			// waiting and looks healthy, and nothing else on the page says why
			// no runner is coming.
			out = append(out, Problem{
				Code:     "pool.github_rate_limited",
				Severity: config.SeverityWarning,
				Title:    fmt.Sprintf("pool %s is waiting on GitHub's rate limit", pp.PoolName),
				Detail:   pp.Held,
				Fix: "nothing to change: the hold lasts as long as GitHub asked and lifts on its own. " +
					"A hold that recurs means the installation's hourly quota is being spent elsewhere, " +
					"usually by another integration on the same App.",
				TargetKind: "pool", TargetID: pp.PoolID,
			})
		}
		if pp.HistoryUnfit != "" {
			out = append(out, Problem{
				Code:     "pool.history_unfit",
				Severity: config.SeverityWarning,
				Title:    fmt.Sprintf("no host can fit a job waiting on pool %s", pp.PoolName),
				Detail: capitalise(pp.HistoryUnfit) + ". The requirement is the job's own recent peaks, with a margin on memory. " +
					"It is placed as it always was, so it is likely to fail the way it did before; the jobs beside it are not held back for it.",
				Fix:        "add a larger host this pool can run on, or split the job so each part needs less.",
				TargetKind: "pool", TargetID: pp.PoolID,
			})
		}
		if pp.Blocked == "" {
			continue
		}
		// Jobs already waiting make this an outage rather than a warning about
		// a pool that is merely unable to reach its minimum -- unless the fleet
		// is simply full, which is the system working and which the next
		// finished job clears on its own.
		severity := config.SeverityWarning
		title := fmt.Sprintf("pool %s cannot start the runners it wants", pp.PoolName)
		switch {
		case pp.BlockedAtCapacity && pp.QueuedMatched > 0:
			title = fmt.Sprintf("pool %s has %s waiting for a host with room",
				pp.PoolName, plural(pp.QueuedMatched, "job"))
		case pp.QueuedMatched > 0:
			severity = config.SeverityError
			title = fmt.Sprintf("pool %s has %s waiting and nowhere to run them",
				pp.PoolName, plural(pp.QueuedMatched, "job"))
		}
		out = append(out, Problem{
			Code:         "pool.no_capacity",
			Severity:     severity,
			Title:        title,
			Detail:       pp.Blocked,
			Fix:          pp.BlockedFix,
			Alternatives: pp.BlockedAlternatives,
			TargetKind:   "pool", TargetID: pp.PoolID,
		})
	}
	return out
}

// plural writes "1 job" and "3 jobs". Titles are read as headings in the
// problems drawer, so "job(s)" is not an option there.
// section keeps the incomplete-list sentence grammatical without a plural
// helper that takes two nouns, which nothing else here needs.
func section(n int) string {
	if n == 1 {
		return "that section"
	}
	return "those sections"
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// pluralDeliveries is plural for the one noun whose plural is not an s.
func pluralDeliveries(n int) string {
	if n == 1 {
		return "1 webhook delivery"
	}
	return fmt.Sprintf("%d webhook deliveries", n)
}

// overprovisionedSlotMemoryMB is the least memory a slot should have behind
// it: two gigabytes, which is what a checkout, a compiler and a test run need
// between them before the machine starts swapping. A runner given less is
// not refused -- the limit is a warning about the host, not a rule on the
// pool -- but it is the size below which "the jobs crawl" is the usual
// report.
const overprovisionedSlotMemoryMB int64 = 2048

// overprovisionedProblem is host.overprovisioned for one measured host: more
// slots than the allocatable CPUs, or more than the allocatable memory in
// 2 GB slots, whichever the machine runs out of first.
//
// The fix names the largest capacity that fits, never below one, because a
// host of capacity zero is a host that takes nothing and the operator has a
// cordon for that. When even one slot does not fit -- under a core, or under
// 2 GB, after the reserve -- the sentence says the machine is too small to
// run a runner well, rather than pretending a capacity of one is the answer.
//
// A slot is two containers wherever a docker-in-docker pool places, typed or
// not. A pool that typed its own limits doubles the machine a slot has to be
// because the daemon is given what the job was promised and the machine
// carries both; a pool that left its size to the host still puts a runner and
// a daemon in that one slot, and stability over performance means each of
// them needs its own comfortable share rather than half of one -- see
// scheduler.ShareFloor. Either way a host sized comfortably for eight plain
// runners is over-provisioned at eight of those, and it is the count this
// warning exists to give, since nothing else on the Hosts page says a slot
// there is worth two.
func overprovisionedProblem(h *store.Host, pools []*store.Pool, defaults bool) (Problem, bool) {
	a := h.Allocatable()
	slots := h.Slots()
	if slots <= 0 || (!a.CPUsKnown && !a.MemoryKnown) {
		return Problem{}, false
	}
	pair, typed := dindPoolPlacesOn(h, pools)
	containers := int64(1)
	if pair != "" {
		containers = 2
	}
	needCPUs, needMemoryMB := slotNeed(h, pools)
	// The whole slot, which is not always two of the one container. The scheduler
	// divides a pair's slot by the pool's own shares, so a sidecar given 70% of the
	// CPU makes a slot of 1 / 0.3 cores before the thinner half has its core, and a
	// minimum is charged to both halves: counting two flat containers said "lower to
	// 3" where the room is 0 until the capacity is 2, and stayed silent where a 3 GB
	// minimum had already made every slot 6 GB.
	slotCPUs, slotMemoryMB := needCPUs*float64(containers), needMemoryMB*containers
	if f, ok := automaticPairFloor(h, pools); ok {
		slotCPUs, slotMemoryMB = max(slotCPUs, f.CPUs), max(slotMemoryMB, f.MemoryMB)
	}
	fits := math.MaxInt
	if a.CPUsKnown {
		fits = min(fits, max(1, int(math.Floor(a.CPUs/slotCPUs+1e-9))))
	}
	if a.MemoryKnown {
		fits = min(fits, max(1, int(float64(a.MemoryMB)/float64(slotMemoryMB)+1e-9)))
	}
	if slots <= fits {
		return Problem{}, false
	}
	var machine, allocatable []string
	if a.CPUsKnown {
		machine = append(machine, fmt.Sprintf("%d CPUs", h.CPUs))
		allocatable = append(allocatable, scheduler.FormatCPUs(a.CPUs)+" allocatable CPUs")
	}
	if a.MemoryKnown {
		machine = append(machine, fmt.Sprintf("%d MB of memory", h.MemoryMB))
		allocatable = append(allocatable, fmt.Sprintf("%d MB of allocatable memory", a.MemoryMB))
	}
	detail := fmt.Sprintf("%s has capacity %d on %s (%s, less the reserve)",
		naming.ForSentence(h.Name), slots, strings.Join(allocatable, " and "), strings.Join(machine, " and "))
	// The share is named only where it is given. A host offering only the
	// process backend, or a daemon that cannot apply the limit, or an agent
	// that has not said, gives its runners no default at all, and a sentence
	// about "each runner's default share" there would describe a limit that
	// does not exist.
	bindCPU, bindMemory := scheduler.DefaultsBind(h)
	var each []string
	if defaults {
		share := scheduler.HostShare(h)
		if a.CPUsKnown && bindCPU {
			each = append(each, scheduler.FormatCPUs(share.CPUs)+" CPUs")
		}
		if a.MemoryKnown && bindMemory {
			each = append(each, fmt.Sprintf("%d MB of memory", share.MemoryMB))
		}
	}
	switch {
	case len(each) > 0:
		detail += fmt.Sprintf(", so each runner's default share is %s; a runner with less than %s CPU or under %d MB crawls through a build, and %d of them together are what the machine was already too small for.",
			strings.Join(each, " and "), scheduler.FormatCPUs(needCPUs), needMemoryMB, slots)
	case defaults:
		detail += fmt.Sprintf(", and its runners are given no default limit here -- its daemon cannot apply one, or its agent has not said whether it can, or it runs only the process backend -- so nothing limits them: each of the %d can take the whole machine at once, which is the shape that stops Docker answering.", slots)
	default:
		detail += fmt.Sprintf(", and scheduler.default_runner_limits is off, so nothing limits its runners: each of the %d can take the whole machine at once, which is the shape that stops Docker answering.", slots)
	}
	// Which pool made a slot a pair, because "two containers" is not a thing
	// an operator can check against anything on the host's own card. The two
	// shapes are charged differently, so the sentence has to say which one
	// this host has.
	switch {
	case typed:
		detail += fmt.Sprintf(" %s runs docker in docker here with limits of its own, so each of its slots is two containers -- the runner and the daemon, each given what the pool asked for -- and the machine carries twice what the capacity reads.", pair)
	case pair != "":
		detail += fmt.Sprintf(" %s runs docker in docker here without limits of its own, so each of its slots is still two containers -- a runner and the daemon it shares the slot with -- and stability over performance means each of them needs its own comfortable share rather than half of one.", pair)
	}
	// Capacity is decided once, at join, and a heartbeat never rewrites it:
	// agent.capacity answers only for the embedded host, and a remote host
	// is resized on its card or with a PATCH. A join token's --capacity is
	// named for what it is -- the figure the host takes at its next join --
	// so an operator who edits zoomies.yaml on a remote host and restarts
	// the agent is not left wondering why the warning stayed.
	where := "lower the host's capacity to " + strconv.Itoa(fits)
	if h.Embedded {
		// agent.capacity is read when the embedded host first enrols and not again
		// (adoptEmbeddedCredentials returns before joining once its credentials are
		// recognised), so naming it here sent a single-VM operator to a setting that
		// does nothing for a host that already exists.
		where += " (on the host card, or zoomies hosts edit " + h.ID + " --capacity " + strconv.Itoa(fits) + "; agent.capacity only sets it when the host first enrols)"
	} else {
		where += " (on the host card, or zoomies hosts edit " + h.ID + " --capacity " + strconv.Itoa(fits) + "; --capacity on a fresh join token applies only at the host's next join)"
	}
	where += ", or add a host"
	tooSmall := (a.CPUsKnown && a.CPUs+1e-9 < slotCPUs) || (a.MemoryKnown && float64(a.MemoryMB) < float64(slotMemoryMB))
	fix := where + "."
	if tooSmall {
		wants := fmt.Sprintf("%s CPU and %d MB", scheduler.FormatCPUs(needCPUs), needMemoryMB)
		if containers > 1 {
			wants = fmt.Sprintf("%s CPU and %d MB between its runner and its sidecar",
				scheduler.FormatCPUs(slotCPUs), slotMemoryMB)
		}
		fix = fmt.Sprintf("the machine is too small to run a runner well: after its reserve it has %s to give, and one runner wants %s. %s, and give it lighter jobs or replace it with a larger machine.",
			strings.Join(allocatable, " and "), wants, where)
	}
	p := Problem{
		Code:       "host.overprovisioned",
		Severity:   config.SeverityWarning,
		Title:      "a host has more slots than its machine can carry",
		Detail:     detail,
		Fix:        fix,
		TargetKind: "host",
		TargetID:   h.ID,
	}
	return p, true
}

// slotNeed is what one container in a slot on h has to be given for
// host.overprovisioned to call the slot big enough: a core and
// overprovisionedSlotMemoryMB, lowered on a field only where every pool that
// can place on the host has a minimum below it.
//
// The core and 2 GB are a judgement made for runners nobody sized. A pool with
// a minimum has been sized -- its operator said what a runner of it will
// accept -- and a host whose slots each cover that minimum is a host the pool
// runs on as configured, not one too small for it. Warning there sent
// operators to lower a capacity their fleet was using. The largest need among
// the pools that reach the host is the one that counts, so a pool with no
// minimum beside one that has keeps the warning its own thin runners earn; a
// minimum never raises the bar, since it is a floor and not a size.
func slotNeed(h *store.Host, pools []*store.Pool) (cpus float64, memoryMB int64) {
	var reached bool
	for _, p := range pools {
		if p == nil || !p.Enabled {
			continue
		}
		if !scheduler.HostSelects(h, p) || !scheduler.HostOffers(h, p) || !scheduler.HostIsPlatform(h, p) {
			continue
		}
		c, m := 1.0, overprovisionedSlotMemoryMB
		if v := p.Resources.MinCPUs; v > 0 {
			c = min(c, max(v, store.MinRunnerCPUs))
		}
		if v := p.Resources.MinMemoryMB; v > 0 {
			m = min(m, max(v, store.MinRunnerMemoryMB))
		}
		if !reached {
			cpus, memoryMB, reached = c, m, true
			continue
		}
		cpus, memoryMB = max(cpus, c), max(memoryMB, m)
	}
	if !reached {
		return 1, overprovisionedSlotMemoryMB
	}
	return cpus, memoryMB
}

// automaticPairFloor is the largest slot an automatic docker-in-docker pool that
// reaches h will accept: the scheduler's own floor, with the pool's shares and
// minimum in it, so the warning and the refusal agree about what a slot needs.
// Typed pools are charged what they ask for and keep the flat judgement.
func automaticPairFloor(h *store.Host, pools []*store.Pool) (scheduler.Reservation, bool) {
	var out scheduler.Reservation
	var any bool
	for _, p := range pools {
		if p == nil || !p.Enabled || p.DockerMode != store.DockerDinD || p.Backend == store.BackendProcess || !p.Automatic() {
			continue
		}
		if !scheduler.HostSelects(h, p) || !scheduler.HostOffers(h, p) || !scheduler.HostIsPlatform(h, p) {
			continue
		}
		f := scheduler.ShareFloor(p)
		out.CPUs, out.MemoryMB, any = max(out.CPUs, f.CPUs), max(out.MemoryMB, f.MemoryMB), true
	}
	return out, any
}

// dindPoolPlacesOn names the first docker-in-docker pool that would place a
// runner on this host, or "" when none would, and whether that pool typed its
// own limits. It is what decides whether a slot here is one container or two.
//
// The question is which pools reach the host, not which have runners on it
// now: a capacity that the machine cannot carry is wrong before the first
// pair is placed, and an operator told about it only once the host is full
// has already had the jobs that found out for them. Availability is left out
// for the same reason -- a cordoned host is skipped by the caller, and a host
// that is merely unhealthy is still the size it is.
//
// Both shapes make a slot a pair now. A pool that typed its limits is charged
// for the sidecar a second time, and a pool that left its size to the host
// still puts two containers in one slot -- stability over performance means
// that slot has to be big enough for both of them at comfortableRunnerCPUs
// and comfortableRunnerMemoryMB each, not half of that each. Only which
// sentence explains it still depends on which pool made the slot a pair, so a
// typed match wins when both exist: it is charged twice what it asks for,
// which is the stronger and more specific fact.
func dindPoolPlacesOn(h *store.Host, pools []*store.Pool) (name string, typed bool) {
	for _, p := range pools {
		if p == nil || !p.Enabled || p.DockerMode != store.DockerDinD || p.Backend == store.BackendProcess {
			continue
		}
		if !scheduler.HostSelects(h, p) || !scheduler.HostOffers(h, p) || !scheduler.HostIsPlatform(h, p) {
			continue
		}
		if !p.Automatic() {
			return p.Name, true
		}
		if name == "" {
			name = p.Name
		}
	}
	return name, false
}

// unenforceableProblem is host.limits_unenforceable for one backend of one
// host: the daemon has said it cannot apply a CPU quota, a memory limit, a
// pids limit, or several of them. A CPU quota it cannot apply is refused at create, so a pool that sets
// one fails every runner it starts there; a memory limit is dropped, so the
// pool's own limit binds nothing. No default is given on that field either
// way, which is the one thing the controller can do about it on its own.
func unenforceableProblem(h *store.Host, info store.HostBackend) (Problem, bool) {
	if info.Limits.CPU && info.Limits.Memory && info.Limits.Pids {
		return Problem{}, false
	}
	var cannot []string
	if !info.Limits.CPU {
		cannot = append(cannot, "a CPU quota (a runner asking for one is refused at create)")
	}
	if !info.Limits.Memory {
		cannot = append(cannot, "a memory limit (a runner asking for one starts without it)")
	}
	if !info.Limits.Pids {
		cannot = append(cannot, "a pids limit (a pool's pids_limit is ignored there)")
	}
	daemon := string(info.Kind) + " daemon"
	if info.Rootless {
		daemon = "rootless " + daemon
	}
	fix := "on a cgroup v2 host, delegate the controllers to the user the daemon runs as: put " +
		"`[Service]` and `Delegate=cpu cpuset io memory pids` in a drop-in for its user slice (`sudo systemctl edit user@$(id -u).service`), " +
		"then `systemctl --user restart " + string(info.Kind) + "`. A host still on cgroup v1 needs cgroup v2 first (`systemd.unified_cgroup_hierarchy=1` on the kernel command line)."
	if !info.Rootless {
		fix = "the daemon runs as root, so the kernel or the cgroup mount is what is missing: a host on cgroup v1 needs cgroup v2 " +
			"(`systemd.unified_cgroup_hierarchy=1` on the kernel command line), and a kernel built without CFS bandwidth control or the memory controller cannot apply the limit at all."
	}
	return Problem{
		Code:     "host.limits_unenforceable",
		Severity: config.SeverityWarning,
		// The kind is in the title for the same reason as in
		// host.shared_folder_unmounted: docker and podman on one host are two
		// problems with one target.
		Title: "a host's " + string(info.Kind) + " daemon cannot apply the limits its runners are given",
		Detail: fmt.Sprintf("the %s on %s reports that it cannot apply %s. Its runners are given no default on that field, and a pool that sets one explicitly is the pool that fails or runs unlimited there.",
			daemon, naming.ForSentence(h.Name), strings.Join(cannot, " or ")),
		Fix:        fix,
		TargetKind: "host",
		TargetID:   h.ID,
	}, true
}

func (c *Controller) jobProblems(ctx context.Context, out *[]Problem) error {
	// Both of the next two read the same hundred faulted jobs, so it is one
	// query rather than two: asked twice on every pass it was most of what the
	// problems list cost. Each gets its own copy, because the first filters its
	// slice in place.
	faulted, _, err := c.st.ListJobs(ctx, store.JobFilter{FaultedOnly: true},
		store.Page{Limit: 100, Sort: "queued_at", Desc: true})
	if err != nil {
		return fmt.Errorf("listing jobs that lost their runner: %w", err)
	}
	if err := c.lostRunnerProblems(ctx, out, slices.Clone(faulted)); err != nil {
		return err
	}
	if err := c.oomKilledProblems(ctx, out, faulted); err != nil {
		return err
	}
	all, err := c.unmatchedQueuedJobs(ctx)
	if err != nil {
		return err
	}
	now := c.Now()
	var unmatched []scheduler.UnmatchedJob
	for _, u := range all {
		// A job on GitHub's own runners or a vendor's is theirs to run however
		// long it queues, and a job unclaimed for seconds may be another
		// provider's, about to start there. Neither is this fleet's problem.
		if hostedJob(u.Job.Labels) || now.Sub(u.Job.QueuedAt) < unmatchedGrace {
			continue
		}
		unmatched = append(unmatched, u)
	}
	if len(unmatched) == 0 {
		return nil
	}
	example := unmatched[0].Job
	labelNames := make([]string, len(example.Labels))
	for i, l := range example.Labels {
		labelNames[i] = workflowText(l)
	}
	labels := strings.Join(labelNames, ", ")
	// The scheduler's reason is only set for a job something less obvious than
	// its labels refused -- a pool advertising exactly those labels but on
	// another installation. Saying "if another provider serves those labels,
	// this is expected" about such a job would send the operator looking in
	// the wrong place entirely.
	tail := " If another runner provider serves those labels, this is expected."
	fix := "create or enable a pool advertising those labels, or change the workflow's runs-on; if another provider takes these jobs, nothing needs doing."
	if reason := unmatched[0].Reason; reason != "" {
		tail = fmt.Sprintf(" %s.", capitalise(reason))
		fix = "point the workflow at a pool on the installation covering that repository, or add a pool there; a pool only ever runs work in its own GitHub target."
	}
	// A job that asks for a size class by name is the one case where the label
	// is this fleet's own, so "if another provider serves those labels" is the
	// wrong thing to say about it: it is asking for something automatic pools
	// make, and the question is why no pool carries it.
	// Not where the scheduler has said why: a pool that carries the label exists,
	// on another installation, and that is what to fix.
	if class, ok := scheduler.RequestedClass(example.Labels); ok && unmatched[0].Reason == "" {
		why, remedy := c.namedClassWhy(class)
		tail = fmt.Sprintf(" It asks for %s by name and no enabled pool carries that label: %s. A job that names its class is never moved to another.", class.Label(), why)
		fix = remedy
	}
	*out = append(*out, Problem{
		Code:     "jobs.unmatched",
		Severity: config.SeverityWarning,
		Title:    fmt.Sprintf("no enabled pool here claims %s", plural(len(unmatched), "queued job")),
		Detail: fmt.Sprintf("if they are meant for this fleet, nothing will run them. The oldest is %s in %s, asking for [%s], queued since %s.%s",
			workflowText(example.JobName), workflowText(example.Repo), labels, example.QueuedAt.UTC().Format(time.RFC3339), tail),
		Fix:        fix,
		TargetKind: "job", TargetID: example.ID, Since: &example.QueuedAt,
	})
	return nil
}

// workflowText makes a name a workflow author wrote safe to put in a problem. A job's
// name and the labels it asked for are written by whoever can open a pull request
// against a repository this fleet serves, and a problem's text goes to the browser --
// where a run of backticks is shown as a command with a copy button -- to the CLI's
// terminal, and to an agent over MCP. Control and direction-changing characters are
// replaced, backticks are made apostrophes, and it is cut at a length no real name
// reaches.
func workflowText(s string) string {
	return workflowTextN(s, 80)
}

// workflowTextN is workflowText with the cut chosen by the caller: a problem's
// sentence stops at 80 runes, a quoted log line at the store's own limit.
func workflowTextN(s string, longest int) string {
	var b strings.Builder
	runes := 0
	for _, r := range s {
		if runes == longest {
			b.WriteString("...")
			break
		}
		switch {
		case r == '`':
			r = '\''
		case unicode.IsControl(r) || isBidiControl(r):
			r = ' '
		}
		b.WriteRune(r)
		runes++
	}
	return b.String()
}

// isBidiControl is every character that changes the direction text is shown in, or
// where a line ends: the Arabic letter mark, the left-to-right and right-to-left marks,
// the embeddings and overrides, the isolates, and the line and paragraph separators.
// None is a control character to unicode.IsControl, and each can reorder what a
// terminal or a browser shows of a name a workflow author wrote.
func isBidiControl(r rune) bool {
	return r == '\u061c' || r == '\u200e' || r == '\u200f' ||
		(r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069') ||
		r == '\u2028' || r == '\u2029'
}

// capitalise upper-cases the first letter of a sentence written to be joined
// onto another. The reasons are written lowercase so that they read inside a
// clause; here one begins a sentence of its own.
func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// namedClassWhy says why nothing answers a job that asks for a size class by
// name, and what to do about it. The class label is this fleet's own, so "if
// another provider serves those labels" is the wrong thing to say about it: it
// is asking for something automatic pools make, and the question is why no pool
// carries it.
func (c *Controller) namedClassWhy(class store.SizeClass) (why, fix string) {
	why = "automatic pools are off, so the only pools that carry it are ones you made"
	switch c.autoMode() {
	case scheduler.SizeOn:
		why = fmt.Sprintf("there is no enabled pool for the %s class, which means no %s host is enrolled that counts (a host that is cordoned, silent or paused does not), or you paused that pool", class, class)
	case scheduler.SizeShadow:
		why = "automatic pools are only being watched, so none has been made"
	}
	fix = fmt.Sprintf("enrol a %s host or enable scheduler.auto_pools, or take %s out of the workflow's runs-on to let the job go to the class its history says.", class, class.Label())
	return why, fix
}

// duplicateAgentWindow is how long after the last alternation the duplicate is
// still reported. It ages the problem out on its own: once the second agent is
// stopped the sessions stop swapping, and an operator who has fixed it should
// not have to clear anything by hand.
const duplicateAgentWindow = time.Hour

// unmatchedGrace is how long a queued job no pool here claims is given before
// it is reported. GitHub's own runners take a job within seconds and another
// provider within its own scale-up delay, so a job still unclaimed after this
// long is either meant for this fleet and mislabelled, or nobody's at all.
const unmatchedGrace = 2 * time.Minute

// lostRunnerProblems reports jobs whose runner stopped under them in the last
// hour. GitHub records these as failures like any test failure, and a team
// that sees "CI is flaky" when the fleet is killing their jobs will blame the
// wrong thing; this is where the fleet owns up.
func (c *Controller) lostRunnerProblems(ctx context.Context, out *[]Problem, faulted []*store.Job) error {
	since := c.Now().Add(-problemWindow)
	recent := faulted[:0]
	for _, j := range faulted {
		// Filtered here rather than in SQL because the moment that matters
		// is when the runner went, and the nearest stored stamp to that is
		// the job's completion -- or, for a job GitHub still thinks is
		// running, now.
		if j.CompletedAt == nil || j.CompletedAt.After(since) {
			recent = append(recent, j)
		}
	}
	if len(recent) == 0 {
		return nil
	}
	example := recent[0]
	at := example.StartedAt
	if at == nil {
		at = &example.QueuedAt
	}
	title := fmt.Sprintf("%d jobs lost the runners they were running on in the last hour", len(recent))
	if len(recent) == 1 {
		title = "1 job lost the runner it was running on in the last hour"
	}
	// The dominant category, which is the sentence an operator can act on.
	// "Fourteen jobs lost their runners" is a bad afternoon; "fourteen, twelve
	// of them out of memory" is a memory limit to raise, and the second is the
	// whole reason the category exists.
	detail := fmt.Sprintf("GitHub records these as ordinary failures. The most recent is %s in %s: %s.",
		workflowText(example.JobName), workflowText(example.Repo), example.RunnerFault)
	fix := "open the job for its timeline and the runner for its last output; a runner that dies mid-job has usually run out of memory or disk, or was removed with force. Re-run the workflow once the cause is fixed."
	if kind, n := dominantFault(recent); n > 0 {
		if n == len(recent) {
			detail = fmt.Sprintf("Every one of them is the same fault. %s", detail)
		} else {
			detail = fmt.Sprintf("%d of them are the same fault. %s", n, detail)
		}
		if f := kind.Fix(); f != "" {
			fix = f + " Re-run the workflows once the cause is fixed."
		}
	}
	*out = append(*out, Problem{
		Code:       "jobs.runner_lost",
		Severity:   config.SeverityWarning,
		Title:      title,
		Detail:     detail,
		Fix:        fix,
		TargetKind: "job", TargetID: example.ID, Since: at,
	})
	return nil
}

// oomKilledProblems reports, host by host, the jobs the kernel killed
// something in for memory in the last hour.
//
// jobs.runner_lost already counts a runner killed outright; this is its own
// entry because the kill that matters most is the one that entry cannot see --
// a step killed with exit 137 under a runner that lived on, which GitHub
// records as a failed step like any test and no runner was lost for. It is per
// host because the fix is: a host that keeps killing jobs is too small for the
// work its pools send it.
func (c *Controller) oomKilledProblems(ctx context.Context, out *[]Problem, faulted []*store.Job) error {
	since := c.Now().Add(-problemWindow)
	byHost := map[string][]*store.Job{}
	var hosts []string
	for _, j := range faulted {
		if !j.OOMKilled || (j.CompletedAt != nil && !j.CompletedAt.After(since)) {
			continue
		}
		host := j.HostID
		if host == "" {
			host = "an unrecorded host"
		}
		if byHost[host] == nil {
			hosts = append(hosts, host)
		}
		byHost[host] = append(byHost[host], j)
	}
	slices.Sort(hosts)
	history := c.cfg().Scheduler.HistorySizing == scheduler.HistoryOn
	for _, host := range hosts {
		jobs := byHost[host]
		example := jobs[0]
		title := fmt.Sprintf("1 job was killed for memory on host %s in the last hour", host)
		if len(jobs) > 1 {
			title = fmt.Sprintf("%d jobs were killed for memory on host %s in the last hour", len(jobs), host)
		}
		detail := fmt.Sprintf("The most recent is %s in %s", workflowText(example.JobName), workflowText(example.Repo))
		if example.PeakMemoryMB > 0 {
			detail += fmt.Sprintf(", which was measured using up to %s before it was killed", formatJobMB(example.PeakMemoryMB))
		}
		detail += ". GitHub records it as an ordinary failure."
		fix := "give the pool a larger minimum memory or memory_mb, or set scheduler.history_sizing to on so the next run of the job is placed on a host with room for what it needed."
		if history {
			fix = "nothing to change for the next run: scheduler.history_sizing places it on a host with room for half as much again. If it keeps happening, the pool needs a larger host."
		}
		*out = append(*out, Problem{
			Code:       "jobs.oom_killed",
			Severity:   config.SeverityWarning,
			Title:      title,
			Detail:     detail,
			Fix:        fix,
			TargetKind: "job", TargetID: example.ID, Since: example.CompletedAt,
		})
	}
	return nil
}

// dominantFault names the category most of a set of failures share, and how
// many share it, or an empty kind when no category has a majority.
//
// A majority rather than a mode: two out of nine is the commonest category and
// says nothing, and a fix offered on that basis sends somebody to change a
// setting that is not the problem. A category has to be most of what is
// happening before the panel names a remedy for it.
func dominantFault(jobs []*store.Job) (store.FaultKind, int) {
	counts := map[store.FaultKind]int{}
	for _, j := range jobs {
		if j.FaultKind != "" {
			counts[j.FaultKind]++
		}
	}
	var best store.FaultKind
	var n int
	for kind, c := range counts {
		if c > n {
			best, n = kind, c
		}
	}
	if n*2 <= len(jobs) {
		return "", 0
	}
	return best, n
}

// startFailureFix is what to do about a pool whose runners will not start: the
// category's own remedy where the scheduler classified one, and the general
// advice where it did not.
//
// The general advice is a list of the usual causes, which is what this said for
// every pool before there were categories. It is still the right thing to say
// when nothing narrowed it, and the wrong thing to say when something did --
// being handed three possibilities when the fleet already knows which one it is
// is how an operator learns not to read the fix line.
func startFailureFix(kind store.FaultKind) string {
	if fix := kind.Fix(); fix != "" {
		return fix
	}
	return "the failed runners are on the Runners page with their reasons; the usual causes are an image " +
		"that cannot be pulled, a host whose agent cannot reach GitHub, or a runner version that does " +
		"not exist. The pool tries again on its own, less often with each failure."
}

// unmatchedQueuedJobs prefers the last scheduler decision, which is computed
// against the pools as they are now; it falls back to the flag stored on each
// job when no pass has run yet, so a fresh controller still reports honestly.
//
// The fallback carries no reason. The stored flag records that nothing claimed
// the job, not what refused it, and inventing one from a row would be a guess.
func (c *Controller) unmatchedQueuedJobs(ctx context.Context) ([]scheduler.UnmatchedJob, error) {
	if plan, at := c.getLastPlan(); plan != nil && !at.IsZero() {
		return plan.Unmatched, nil
	}
	jobs, _, err := c.st.ListJobs(ctx, store.JobFilter{
		States:        []store.JobState{store.JobQueued},
		UnmatchedOnly: true,
	}, store.Page{Limit: 100, Sort: "queued_at"})
	if err != nil {
		return nil, fmt.Errorf("listing unmatched jobs: %w", err)
	}
	out := make([]scheduler.UnmatchedJob, 0, len(jobs))
	for _, j := range jobs {
		// Nothing claims a job an operator removed from the queue either, and
		// the scheduler's own pass leaves those out. Reporting them here would
		// make a controller that has not yet run a pass raise a problem about
		// work somebody has already decided not to run.
		if j.RemovedFromQueue() {
			continue
		}
		out = append(out, scheduler.UnmatchedJob{Job: j})
	}
	return out, nil
}

// loopProblems reports every background loop that has panicked since the
// process started. The loop was restarted, so the fleet is still being run;
// the entry is here because a crash is a bug, and a bug that fixes itself
// after a minute is one nobody would otherwise report.
func (c *Controller) loopProblems() []Problem {
	panics := c.LoopPanics()
	names := slices.Sorted(maps.Keys(panics))
	out := make([]Problem, 0, len(names))
	for _, name := range names {
		p := panics[name]
		title := fmt.Sprintf("the %s loop crashed and was restarted", name)
		if p.Count > 1 {
			title = fmt.Sprintf("the %s loop has crashed %d times and was restarted each time", name, p.Count)
		}
		at := p.At
		out = append(out, Problem{
			Code:     "controller.loop_panicked",
			Severity: config.SeverityError,
			Title:    title,
			Detail:   "the last panic said: " + p.Last,
			Fix: "this is a bug in Zoomies. The stack is in the controller's log under \"controller loop panicked\"; " +
				"please report it with that stack. Restarting the controller clears this entry.",
			Since: &at,
		})
	}
	return out
}

// pollerProblems says whether the safety net is still there.
//
// The fallback poller is what catches a fleet whose webhooks have stopped
// arriving, and its two failure modes are silent by construction: a sweep that
// has stopped happening looks exactly like a sweep with nothing to find, and an
// installation standing down from GitHub's rate limit looks exactly like an
// installation with no queued jobs. Both are only visible in the log, and
// nobody reads the log of a fleet that appears to be fine.
func (c *Controller) pollerProblems(ctx context.Context, out *[]Problem) error {
	if !c.PollerEnabled() {
		// Off is a choice, and the configuration validator already names it.
		// Saying it twice would be the drawer disagreeing with itself.
		return nil
	}
	now := c.Now()

	// Two intervals of grace, the same window pollOnce uses to decide an
	// installation's webhooks are fresh: one missed tick is a slow query, and
	// a threshold that fires on one would cry wolf on every busy controller.
	if last := c.LastPollAt(); !last.IsZero() && now.Sub(last) > 2*c.pollInterval()+c.pollInterval()/2 {
		since := last
		*out = append(*out, Problem{
			Code:     "poller.stale",
			Severity: config.SeverityWarning,
			Setting:  "github.poll_interval",
			Title:    "the fallback poller has stopped sweeping",
			Detail: fmt.Sprintf("the last sweep finished at %s, and the interval is %s. Until it resumes, a job is only noticed if its webhook arrives.",
				last.UTC().Format(time.RFC3339), c.pollInterval()),
			Fix:   "check the controller's log for the error that ended the sweep; a controller that cannot reach GitHub or its own database logs it there.",
			Since: &since,
		})
	}

	// The hold is per installation -- GitHub's quota is -- so this names the
	// installation rather than the fleet. One organisation spending its hour
	// does not stop the others being polled, and an operator told "the poller
	// is paused" would go looking for a fleet-wide fault that is not there.
	held := c.heldInstallations(now)
	if len(held) == 0 {
		return nil
	}
	insts, err := c.st.ListInstallations(ctx)
	if err != nil {
		return fmt.Errorf("listing installations to name the ones being held: %w", err)
	}
	targets := make(map[string]string, len(insts))
	for _, i := range insts {
		targets[i.ID] = i.Target
	}
	for _, id := range slices.Sorted(maps.Keys(held)) {
		name := targets[id]
		if name == "" {
			name = id
		}
		*out = append(*out, Problem{
			Code:       "poller.paused",
			Severity:   config.SeverityWarning,
			Title:      "GitHub is rate-limiting " + name,
			Detail:     "every background sweep is standing down from this installation until " + held[id].UTC().Format(time.RFC3339) + ". Webhooks still arrive; nothing polls for what they miss until then.",
			Fix:        "nothing to do -- it clears itself. If it keeps happening, the App is spending its hourly quota: raise github.poll_interval, or narrow what this installation covers.",
			TargetKind: "installation",
			TargetID:   id,
		})
	}
	return nil
}

// formatAge renders a duration the way an operator says one out loud.
func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return plural(int(d.Seconds()), "second")
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	default:
		return plural(int(d.Hours()), "hour")
	}
}

func (c *Controller) runnerProblems(ctx context.Context, out *[]Problem) error {
	// The count comes from the store's aggregate, not from a page of rows: a
	// page is capped, and "100 runners in the failed state" on a fleet with
	// four hundred understates the day it is having.
	counts, err := c.st.CountRunnersByPool(ctx)
	if err != nil {
		return fmt.Errorf("counting failed runners: %w", err)
	}
	total := 0
	for _, pc := range counts {
		total += pc.Failed
	}
	if total == 0 {
		return nil
	}
	failed, _, err := c.st.ListRunners(ctx, store.RunnerFilter{
		States: []store.RunnerState{store.RunnerFailed},
	}, store.Page{Limit: 1, Sort: "created_at", Desc: true})
	if err != nil {
		return fmt.Errorf("listing failed runners: %w", err)
	}
	if len(failed) == 0 {
		return nil
	}
	example := failed[0]
	*out = append(*out, Problem{
		Code:       "runners.failed",
		Severity:   config.SeverityWarning,
		Title:      fmt.Sprintf("%s in the failed state", plural(total, "runner")),
		Detail:     fmt.Sprintf("the most recent is %s: %s", example.Name, example.Message),
		Fix:        "look at the runner's logs on the Runners page; failed runners are cleaned up automatically but the cause is not.",
		TargetKind: "runner", TargetID: example.ID, Since: &example.CreatedAt,
	})
	return nil
}

// cleanupProblems names the runners Zoomies could not finish taking away.
//
// This is a different fault from runners.failed and worth its own code. A
// failed runner is a job that did not run; a runner that will not clean up is
// something *left behind* -- a container holding disk on a host, or a
// registration sitting on somebody's organisation -- and it does not go away
// on its own. Neither had anywhere to be said before: the task result was
// dropped because the row was already terminal, and a failed registration
// delete was a log line.
func (c *Controller) cleanupProblems(ctx context.Context, out *[]Problem) error {
	stuck, err := c.st.RunnersWithFailedCleanup(ctx)
	if err != nil {
		return fmt.Errorf("listing runners whose cleanup failed: %w", err)
	}
	if len(stuck) == 0 {
		return nil
	}
	example := stuck[0]
	// A registration left on GitHub and a container left on a host need
	// different things done about them, so the fix says which this is -- and
	// a registration GitHub still calls busy needs neither, at least at
	// first: GitHub refuses to delete a runner it believes is running a job
	// with no override, so there is nothing Zoomies can do but recheck.
	//
	// The halves are read apart rather than the joined sentence searched,
	// because a row can have both, and the container is the one that needs
	// somebody: a search that found "running a job" first used to tell the
	// operator only that GitHub was catching up, while the thing left behind
	// was a container on their host.
	hostName := c.hostName(ctx, example.HostID)
	onHost, onGitHub := example.HostCleanupError, example.RegistrationCleanupError
	if onHost == "" && onGitHub == "" {
		onGitHub = example.CleanupError
	}
	fix := fmt.Sprintf("look at %s on the Runners page. If the container is still on %s, remove it there; "+
		"Zoomies retries, and the row clears itself when it succeeds.",
		example.Name, hostName)
	switch {
	case strings.Contains(onHost, agent.StuckRemoval):
		// The agent's own verdict: it waited out the removal for as long as
		// a working daemon takes, and this one has stopped. Waiting longer
		// fixes nothing, so the action comes first.
		fix = fmt.Sprintf("Docker on %s has been removing %s's container for more than ten minutes and has stopped making progress; restart the daemon there. "+
			"Removing the container by hand gets the same refusal until then, and Zoomies confirms the removal on its next attempt afterwards.",
			hostName, example.Name)
	case strings.Contains(onHost, "already in progress"):
		// Docker's own words for a removal it has under way, reported by an
		// agent from before it waited these out: removing it by hand meets
		// the same refusal, and the daemon usually needs time, not help.
		fix = fmt.Sprintf("Docker on %s is still removing %s's container, and a removal by hand gets the same answer until it has finished. "+
			"Zoomies retries and the row clears itself once it has gone; a removal still under way after ten minutes is one the daemon has stopped making progress on, which restarting the daemon clears.",
			hostName, example.Name)
	case onHost != "":
		// The default fix, which names the container.
	case strings.Contains(onGitHub, "running a job"):
		fix = fmt.Sprintf("GitHub still reports %s busy. Zoomies rechecks every ten minutes and removes the registration once it is idle, or clears this warning if GitHub has already removed it. Allow a running workflow to finish; Zoomies will not cancel it to force cleanup. If this persists, verify the workflow's status on GitHub and investigate a stale busy registration; this is not evidence of a missing App permission.", example.Name)
	case strings.Contains(onGitHub, "registration"):
		fix = fmt.Sprintf("check the target's runner settings page for %s. Zoomies retries the deletion every "+
			"ten minutes and the row clears itself when it succeeds; a registration that stays is usually a "+
			"permission the App has lost.", example.Name)
	}
	if onHost != "" && onGitHub != "" {
		fix += " GitHub's side is rechecked on its own, and within a minute once the container has gone."
	}
	progress := "while waiting for cleanup"
	if example.CleanupAttempts > 0 {
		progress = "after " + plural(example.CleanupAttempts, "attempt")
	}
	*out = append(*out, Problem{
		Code:     "runners.cleanup_failed",
		Severity: config.SeverityWarning,
		Title:    fmt.Sprintf("%s could not be cleaned up", plural(len(stuck), "runner")),
		Detail: fmt.Sprintf("%s, %s: %s. Something is left behind, a container on its host, or a "+
			"registration on GitHub. Housekeeping will recheck and retry safe cleanup automatically.",
			example.Name, progress, example.CleanupError),
		Fix:        fix,
		TargetKind: "runner", TargetID: example.ID, Since: example.CleanupFailedAt,
	})
	return nil
}

// notProgressingSample bounds the page of starting runners the diagnosis is
// built from. They are read oldest first, and a runner is only a candidate
// once it is old enough, so the page always holds the worst cases; a fleet
// with more than this many stuck at once is told "at least".
const notProgressingSample = 100

// notProgressingAfter is how long a runner may sit in a starting state before
// the fleet says so: half the provision timeout, which is the point at which
// the scheduler will eventually fail it.
//
// It is deliberately not a setting of its own. An operator who raises
// provision_timeout for a slow image pull has already said how long starting up
// is allowed to take here, and a second number to keep in step with the first
// is a second number to get wrong.
func (c *Controller) notProgressingAfter() time.Duration {
	timeout := c.cfg().Scheduler.ProvisionTimeout
	if timeout <= 0 {
		return 0
	}
	return timeout / 2
}

// notProgressingProblems reports runners that are neither coming up nor being
// failed yet, and says which of the two shapes it is.
//
// The distinction is the whole point. Until the provision timeout expires the
// fleet says nothing at all, so an operator watching a pool that creates
// runners which never arrive has to read a runner timeline, the controller log
// and `docker ps` on the host to learn what Zoomies already knows: whether the
// agent ever reported the workload started. A runner still waiting for its
// container is a backend or image problem on the host; one whose container
// started and has not registered is the runner process failing to reach GitHub,
// and they are not fixed in the same place.
func (c *Controller) notProgressingProblems(ctx context.Context, out *[]Problem) error {
	after := c.notProgressingAfter()
	if after <= 0 {
		// provision_timeout is off, so nothing is stuck by anyone's definition;
		// saying otherwise would second-guess a deliberate setting.
		return nil
	}
	starting, _, err := c.st.ListRunners(ctx, store.RunnerFilter{
		States: []store.RunnerState{store.RunnerProvisioning, store.RunnerRegistering},
	}, store.Page{Limit: notProgressingSample, Sort: "created_at"})
	if err != nil {
		return fmt.Errorf("listing runners that are starting up: %w", err)
	}

	now := c.Now()
	var waitingForContainer, notRegistered int
	var oldest *store.Runner
	var since time.Time
	hosts := map[string]bool{}
	for _, r := range starting {
		// The clock that matters starts when the workload did. A runner whose
		// container came up ten seconds ago is registering normally even if the
		// image took four minutes to pull, and failing to make that distinction
		// would raise this on every cold start.
		from := r.CreatedAt
		if r.ContainerStartedAt != nil {
			from = *r.ContainerStartedAt
		}
		if now.Sub(from) < after {
			continue
		}
		if r.ContainerStartedAt == nil {
			waitingForContainer++
		} else {
			notRegistered++
		}
		// Longest stuck by its own clock, which is not the same as first
		// created: a runner that spent four minutes pulling an image and then
		// registered ten seconds ago is younger here than an older one whose
		// container came up first.
		// A batch created in one pass shares a millisecond, and the fix below
		// is written for whichever runner is named here, so a tie has to break
		// the same way every time: the one still waiting for a container
		// first, because that is the earlier failure, then the ID.
		if oldest == nil || from.Before(since) || (from.Equal(since) && stuckBefore(r, oldest)) {
			oldest, since = r, from
		}
		hosts[r.HostID] = true
	}
	stuck := waitingForContainer + notRegistered
	if stuck == 0 {
		return nil
	}

	count := plural(stuck, "runner")
	if stuck == notProgressingSample {
		count = "at least " + count
	}
	detail := fmt.Sprintf("the oldest is %s, in %s since %s.", oldest.Name,
		oldest.State, since.UTC().Format(time.RFC3339))
	// The fix follows the runner the detail names, not whichever bucket is
	// larger. A detail that names a runner still waiting for its container and
	// a fix that says to read that container's logs sends an operator looking
	// for something that is not there.
	fix := "the container is up but the runner has not registered: look at its logs on the Runners page. " +
		"The usual causes are the host being unable to reach github.com and a JIT configuration GitHub has already consumed."
	if oldest.ContainerStartedAt == nil {
		fix = "no agent has reported the workload started: check the agent log on the host, and that the pool's image exists and can be pulled. " +
			"A first pull of a large image can legitimately take minutes."
	}
	if waitingForContainer > 0 && notRegistered > 0 {
		detail += fmt.Sprintf(" %s waiting for a container to start, %d with a container that started and has not registered.",
			plural(waitingForContainer, "runner"), notRegistered)
	}
	if len(hosts) == 1 && oldest.HostID != "" {
		if h, err := c.st.GetHost(ctx, oldest.HostID); err == nil {
			detail += fmt.Sprintf(" All of them are on host %s.", naming.ForSentence(h.Name))
		}
	}

	*out = append(*out, Problem{
		Code:     "runners.not_progressing",
		Severity: config.SeverityWarning,
		Title: fmt.Sprintf("%s stuck starting up for over %s",
			count, after.Round(time.Second)),
		Detail:     detail,
		Fix:        fix,
		TargetKind: "runner", TargetID: oldest.ID, Since: &since,
	})
	return nil
}

// stuckBefore orders two runners that have been stuck for the same time: a
// runner with no container yet comes before one whose container started, and
// equal shapes fall back to the ID so the choice is stable across passes.
func stuckBefore(a, b *store.Runner) bool {
	if (a.ContainerStartedAt == nil) != (b.ContainerStartedAt == nil) {
		return a.ContainerStartedAt == nil
	}
	return a.ID < b.ID
}

// updateProblems is everything the update feature has to say: that a newer
// release exists, that the helper that would install it is missing, and that the
// last attempt to update this controller did not succeed.
//
// It takes no context and reads nothing from the database or the disk. The
// helper and the last attempt are what the update loop saw on its last look, and
// this runs after every scheduling pass.
//
// None of these carries a Remedy. The autopilot applies every remedy it finds,
// and replacing the binary the controller is running is not something to do
// because a list was read.
func (c *Controller) updateProblems() []Problem {
	out := c.newerReleaseProblems()
	return append(out, c.controllerUpdateProblems()...)
}

// newerReleaseProblems reports that a newer release of Zoomies exists.
//
// It says nothing at all in the two cases where it would otherwise mislead: a
// controller built from main, which is ahead of the newest release rather than
// behind it, and a check that has not yet answered.
//
// The wording claims no ordering. GitHub's "latest release" excludes drafts and
// prereleases, so it is the release an operator should be on, but comparing
// "0.2-beta" with "0.10-beta" properly means a version parser this does not
// have. Naming both and letting the operator read them is honest; guessing
// which is newer is not.
func (c *Controller) newerReleaseProblems() []Problem {
	if c.cfg().Updates.CheckInterval <= 0 {
		return nil
	}
	if running, ok := developmentCommit(version.Version); ok {
		latest := c.latestDevelopment()
		if latest == nil || strings.HasPrefix(latest.SHA, running) {
			return nil
		}
		newest := latest.SHA
		if len(newest) > 7 {
			newest = newest[:7]
		}
		fix := "let a main CI image-publishing run finish successfully, then run zoomies upgrade again"
		if latest.URL != "" {
			fix += "; the current main commit is " + latest.URL
		}
		at := latest.At
		return []Problem{{
			Code:     "controller.development_update_available",
			Severity: config.SeverityInfo,
			Title:    fmt.Sprintf("main is at %s; this development controller is running %s", newest, running),
			Detail: "the moving dev image may still point to this older build when its publishing workflow was cancelled or failed. " +
				"An upgrade can only pull the newest image that was actually published.",
			Fix:   fix,
			Since: &at,
		}}
	}
	latest := c.latestRelease()
	if latest == nil {
		return nil
	}
	running, ok := releaseVersion(version.Version)
	if !ok {
		return nil
	}
	newest, ok := releaseVersion(latest.Tag)
	if !ok || newest == running {
		return nil
	}
	// A list was read, so the tag is the newest release that can be taken, not
	// the newest there is: a controller on 1.4.0 whose release is still
	// uploading checksums.txt is ahead of v1.3.9, and the status says so.
	if latest.Releases != nil && version.CompareBuilds(version.Version, latest.Tag) != version.SkewBehind {
		return nil
	}
	fix := "upgrade with the same method you installed by; the release notes are at " + latest.URL
	if latest.URL == "" {
		fix = "upgrade with the same method you installed by."
	}
	// Only when the button would work: the mode allows it and the helper has been
	// seen. A page that says to press something that refuses is worse than the
	// general advice.
	if sight := c.updateSightNow(); c.updateMode() != updates.ModeOff && sight.looked && !sight.helperMissing {
		fix = "update from Settings → Updates; the release notes are at " + latest.URL
		if latest.URL == "" {
			fix = "update from Settings → Updates."
		}
	}
	at := latest.At
	return []Problem{{
		Code:     "controller.update_available",
		Severity: config.SeverityInfo,
		Title:    fmt.Sprintf("the current release of Zoomies is %s; this controller is running %s", latest.Tag, running),
		Detail: "nothing is wrong: runners, pools and jobs are unaffected by the controller's own version. " +
			"This is here so an upgrade is a decision rather than a surprise.",
		Fix:   fix,
		Since: &at,
	}}
}

// maxProblemSentence bounds how much of an attempt's reason a problem repeats.
// The attempt keeps up to two kilobytes for the page that shows it whole; a
// problem is a line in a drawer, and an operator needs the opening of the
// sentence to know whether to read on.
const maxProblemSentence = 300

// controllerUpdateProblems reports what the update loop last saw go wrong with
// updating this controller.
func (c *Controller) controllerUpdateProblems() []Problem {
	sight := c.updateSightNow()
	var out []Problem
	// Only a release build can be updated by the button, and RequestControllerUpdate
	// refuses any other before it looks for the helper, so for a build from main a
	// missing helper changes nothing and the warning would name a fix that does
	// nothing.
	_, fromRelease := version.Release(version.Version)
	if mode := c.updateMode(); mode != updates.ModeOff && fromRelease && sight.helperMissing {
		out = append(out, Problem{
			Code:     "controller.update_helper_missing",
			Severity: config.SeverityWarning,
			Title:    "this controller has no update helper, so it cannot update itself",
			Detail: fmt.Sprintf("updates.mode is %s, so this release build of Zoomies may be asked to update itself, but the update helper that would replace its binary "+
				"is not installed on its host. Nothing else is affected: runners, pools and jobs carry on.", mode),
			Fix: "have somebody with root on the controller's host run `" + helperInstallCommand + "`, " +
				"or set updates.mode to off if this controller is to be upgraded by hand with `zoomies upgrade`.",
		})
	}
	if p, ok := failedUpdateProblem(sight.last); ok {
		out = append(out, p)
	}
	return out
}

// failedUpdateProblem is the problem for the controller's latest attempt, when
// that attempt ended without the update happening.
//
// Only the latest counts, so a later success clears it and so does a new attempt
// opening. One that failed but whose release this build has since reached, by
// an upgrade done by hand, is history and not a problem.
func failedUpdateProblem(a *store.UpdateAttempt) (Problem, bool) {
	if a == nil || (a.State != store.UpdateFailed && a.State != store.UpdateTimedOut) {
		return Problem{}, false
	}
	if runsAtLeast(version.Version, a.ToVersion) {
		return Problem{}, false
	}
	how := "failed"
	if a.State == store.UpdateTimedOut {
		how = "timed out"
	}
	reason := problemSentence(a.Error)
	if reason == "" {
		reason = "no reason was recorded"
	}
	p := Problem{
		Code:     "controller.update_failed",
		Severity: config.SeverityError,
		Title:    fmt.Sprintf("the update of this controller to %s %s", naming.ForSentence(a.ToVersion), how),
		Detail: "The reason recorded for it: " + reason +
			" Only the opening of a long reason is shown here; the whole of it is on Settings → Updates, and zoomies updates helper status on the controller's host says what the helper did last.",
		Fix: "look at `journalctl -u zoomies-update` on the controller's host, where `zoomies updates helper status` says what the helper did last. " +
			"Once the cause is dealt with, update again from Settings → Updates (updates.mode must be manual or auto), or run `zoomies upgrade` on the host.",
	}
	if a.FinishedAt != nil {
		since := *a.FinishedAt
		p.Since = &since
	}
	return p, true
}

// problemSentence is the opening of an attempt's reason, made safe to sit in a
// problem. The sentence can be the update helper's own, read from a file the
// service can write, so it is treated as text from outside: only its first line
// is kept, a backtick becomes an apostrophe (the UI draws what sits between two
// as a command with a copy button), control and direction-changing characters
// become spaces, and it is cut at a length and a character boundary.
func problemSentence(s string) string {
	line := ""
	for _, l := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' }) {
		if l = strings.TrimSpace(l); l != "" {
			line = l
			break
		}
	}
	line = strings.Map(func(r rune) rune {
		if isBidiControl(r) {
			return ' '
		}
		return r
	}, naming.ForSentence(line))
	if len(line) <= maxProblemSentence {
		return line
	}
	return strings.TrimSpace(cutAt(line, maxProblemSentence)) + "..."
}
