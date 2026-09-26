package controller

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/version"
)

// FleetState is the one word the status projection leads with.
type FleetState string

const (
	// FleetHealthy is a fleet with nothing on its own list above info.
	FleetHealthy FleetState = "healthy"
	// FleetDegraded is a fleet whose worst problem is a warning: jobs still
	// run, slower or with less room than they should have.
	FleetDegraded FleetState = "degraded"
	// FleetBlocked is a fleet whose worst problem is an error: something the
	// fleet is asked to do is not happening, and waiting will not fix it.
	FleetBlocked FleetState = "blocked"
)

// Band is a count said roughly. The status projection is read by people with
// no account, and "eleven jobs queued" tells them nothing "many" does not
// while telling a stranger exactly how big the fleet is.
type Band string

const (
	BandNone     Band = "none"
	BandFew      Band = "few"
	BandMany     Band = "many"
	BandBackedUp Band = "backed_up"
)

// fewUpTo is the largest count still called "few": a handful, the size of
// one push's matrix on a small fleet.
const fewUpTo = 5

// FleetStatus is what GET /api/v1/status returns: a projection, not a view.
//
// It is the one payload in the API that is deliberately not a resource's GET
// shape, because every resource view carries names -- a pool's, a host's, a
// repository's -- and this is for the developer with no account whose job
// has queued. So it is built the other way round: it carries no string that
// came from a row. Its reasons are codes, its counts are bands, its waits are
// whole minutes, and the only prose in it is the fixed public sentence for
// each code. Proving that is a test that searches a body for every name a
// fixture fleet has, which is a check rather than a promise renewed every
// time somebody edits a sentence.
type FleetStatus struct {
	State FleetState `json:"state"`
	// Since is when this process first saw the fleet in this state.
	Since   time.Time `json:"since"`
	Version string    `json:"version"`
	// Queued and Running are the fleet's own jobs, banded.
	Queued  Band `json:"queued"`
	Running Band `json:"running"`
	// The queue wait over the last hour, rounded to the minute.
	MedianWaitMinutes int `json:"median_wait_minutes"`
	P95WaitMinutes    int `json:"p95_wait_minutes"`
	// Reasons are the fleet's current problems, one per code, worst first.
	Reasons []StatusReason `json:"reasons"`
	// Explanations is the public sentence for each code in Reasons. It is
	// beside the reasons rather than in them so that a reason stays three
	// fields and nothing else.
	Explanations map[string]string `json:"explanations"`
}

// StatusReason is one code on the fleet's list: which, how bad, and since
// when. Nothing else -- the operator's title and detail name the pool or host
// they are about, and that is exactly what this must not carry.
type StatusReason struct {
	Code     string          `json:"code"`
	Severity config.Severity `json:"severity"`
	Since    *time.Time      `json:"since,omitempty"`
}

// ProjectStatus builds the name-free status from the operator's problems list
// and the Overview's numbers. It is pure so that the disclosure boundary can
// be tested without a fleet.
//
// Only the fleet's half of the problems split is read, the half a signed-in
// fleet operator without the platform role is shown, so a platform finding --
// the backup remote, the lease, the controller's own release -- cannot reach
// it even by a mistake in the sentences below.
func ProjectStatus(problems []Problem, stats *Stats) FleetStatus {
	out := FleetStatus{
		State:        FleetHealthy,
		Queued:       BandNone,
		Running:      BandNone,
		Reasons:      []StatusReason{},
		Explanations: map[string]string{},
	}
	byCode := map[string]int{}
	for _, p := range problems {
		audience := p.Audience
		if audience == "" {
			audience = audienceFor(p.Code)
		}
		if !audience.For(false) {
			continue
		}
		if p.Severity != config.SeverityError && p.Severity != config.SeverityWarning {
			continue
		}
		// One reason per code: three pools with no capacity is one reason a
		// job waits, and a count of them would be a count of pools.
		if i, ok := byCode[p.Code]; ok {
			r := &out.Reasons[i]
			if severityRank(p.Severity) < severityRank(r.Severity) {
				r.Severity = p.Severity
			}
			if p.Since != nil && (r.Since == nil || p.Since.Before(*r.Since)) {
				since := *p.Since
				r.Since = &since
			}
			continue
		}
		r := StatusReason{Code: p.Code, Severity: p.Severity}
		if p.Since != nil {
			since := *p.Since
			r.Since = &since
		}
		byCode[p.Code] = len(out.Reasons)
		out.Reasons = append(out.Reasons, r)
	}
	slices.SortStableFunc(out.Reasons, func(a, b StatusReason) int {
		if r := severityRank(a.Severity) - severityRank(b.Severity); r != 0 {
			return r
		}
		if a.Code < b.Code {
			return -1
		}
		if a.Code > b.Code {
			return 1
		}
		return 0
	})
	for _, r := range out.Reasons {
		switch {
		case r.Severity == config.SeverityError:
			out.State = FleetBlocked
		case out.State == FleetHealthy:
			out.State = FleetDegraded
		}
		out.Explanations[r.Code] = PublicSentence(r.Code)
	}

	if stats != nil {
		out.Queued = queueBand(stats.Fleet.QueuedJobs, stats.Hosts.Capacity)
		out.Running = countBand(stats.Fleet.RunningJobs)
		out.MedianWaitMinutes = wholeMinutes(stats.Fleet.MedianWaitMS)
		out.P95WaitMinutes = wholeMinutes(stats.Fleet.P95WaitMS)
	}
	return out
}

// countBand says a count roughly.
func countBand(n int) Band {
	switch {
	case n <= 0:
		return BandNone
	case n <= fewUpTo:
		return BandFew
	default:
		return BandMany
	}
}

// queueBand is countBand with one more word: a queue longer than every slot
// the fleet has is backed up, which is the thing a developer waiting on it
// most needs to know and the one a bare "many" hides.
func queueBand(queued, capacity int) Band {
	b := countBand(queued)
	if b == BandMany && queued > capacity {
		return BandBackedUp
	}
	return b
}

func wholeMinutes(ms int64) int {
	if ms <= 0 {
		return 0
	}
	return int(math.Round(float64(ms) / float64(time.Minute/time.Millisecond)))
}

// Status is the projection for the running fleet, with Since kept across
// calls so it means "since the state last changed" rather than "since you
// asked".
func (c *Controller) Status(ctx context.Context) (*FleetStatus, error) {
	problems, err := c.Problems(ctx)
	if err != nil {
		return nil, fmt.Errorf("gathering the fleet's problems: %w", err)
	}
	stats, err := c.Stats(ctx, defaultStatsWindow)
	if err != nil {
		return nil, fmt.Errorf("gathering the fleet's numbers: %w", err)
	}
	out := ProjectStatus(problems, stats)
	out.Version = version.Version

	c.statusMu.Lock()
	if c.statusSince.IsZero() || c.statusState != out.State {
		c.statusState = out.State
		c.statusSince = c.Now().UTC().Truncate(time.Second)
	}
	out.Since = c.statusSince
	c.statusMu.Unlock()
	return &out, nil
}

// PublicSentence is what the status page says about a code, for a reader who
// cannot act on the fleet and wants to know whether to wait.
func PublicSentence(code string) string {
	if s, ok := publicSentences[code]; ok {
		return s
	}
	// Unreachable for any code the fleet's list can carry --
	// TestEveryFleetCodeHasAPublicSentence sees to that -- but a build between
	// a new code and its sentence says something true rather than nothing.
	return "The fleet has a problem its operators have been told about."
}

// publicSentences is the public half of docs/problem-codes.md: one sentence
// per code the fleet's list can carry, written for somebody with no account.
//
// Each is fixed text. None may be built from a row, because the whole of the
// disclosure argument is that nothing on the status page came from one;
// internal/docs holds this map and the page's table to each other in both
// directions, and the controller's tests hold it to the audience table.
var publicSentences = map[string]string{
	"controller.problems_partial": "The controller could not check everything, so this status may be incomplete.",

	"host.cordoned_with_work":                       "A machine is being taken out of service once its current jobs finish.",
	"host.duplicate_agent":                          "Two machines are claiming to be the same one, so jobs may not be placed on it.",
	"host.image_pull_failed":                        "A machine cannot download the runner image, so jobs that need it may wait.",
	"host.limits_unenforceable":                     "A machine cannot hold runners to their size, so jobs may run slower than usual.",
	"host.limits_unverified":                        "A machine has not yet confirmed it can hold runners to their size.",
	"host.overprovisioned":                          "A machine is promised to more runners than it can hold at once.",
	"host.resources_unknown":                        "A machine has not reported how much room it has, so it is used cautiously.",
	"host.shared_folder_unmounted":                  "A machine keeps no tool cache, so jobs there download their tools again and start slower.",
	"host.runtime_recovering":                       "A machine's container runtime is recovering, so it is taking no new jobs for now.",
	"host.throttled":                                "A machine is busy enough that it is taking new jobs more slowly.",
	"host.unhealthy":                                "A machine has stopped checking in, so fewer jobs can run at once.",
	"host.version_behind":                           "A machine is running an older agent than the controller.",
	"installation.unhealthy":                        "The fleet has lost a permission it needs on GitHub, so jobs from some repositories cannot start.",
	"jobs.runner_lost":                              "A runner stopped while running a job, so that job may fail or need a re-run.",
	"jobs.unmatched":                                "Some jobs ask for runner labels this fleet does not offer, so they will wait until the workflow or the fleet changes.",
	"poller.paused":                                 "The controller has stopped asking GitHub for queued jobs, so a missed notification is not caught.",
	"poller.stale":                                  "The controller has not heard from GitHub recently, so new jobs may be noticed late.",
	"pool.cache_above_disk":                         "A runner cache is set larger than the disk it lives on.",
	"pool.cache_shared":                             "A runner cache is shared more widely than usual.",
	"pool.dangerous":                                "Some runners are set up with more access to their machine than the safe default.",
	"pool.docker_client_missing":                    "Jobs that use Docker may fail because their runner image lacks the Docker client.",
	"pool.elastic_cpu_unsupported":                  "Runners cannot borrow spare CPU on some machines, so busy jobs run at their normal size.",
	"pool.github_rate_limited":                      "GitHub is limiting how fast the fleet can register runners, so jobs may wait longer.",
	"pool.host_overcommitted":                       "Runners are sized to more than their machines have, so jobs may run slower.",
	"pool.max_above_room":                           "The fleet is allowed more runners than its machines have room for.",
	"pool.no_capacity":                              "No machine has room for some jobs right now, so they wait until one frees up or a machine is added.",
	"pool.provision_timeout_short":                  "Runners are given less time to start than they usually need, so some may be retried.",
	"pool.repository_scale_up_deferred":             "Starting runners for some repositories is held back until GitHub allows it.",
	"pool.resources_unenforced":                     "Runner sizes are not enforced on some machines.",
	"pool.runner_group_public_repositories_blocked": "Jobs from public repositories are not allowed on some runners, so they will wait.",
	"pool.runner_group_unresolved":                  "Some runners cannot be registered in the group they belong to, so jobs for them wait.",
	"pool.runners_failing":                          "Runners are failing to start, so jobs are waiting longer than usual.",
	"pool.size_strands_hosts":                       "Runners are sized larger than some machines can hold, so those machines stay idle.",
	"pool.size_unlimited":                           "Some runners have no size limit, so one job can slow the others on its machine.",
	"provider.bootstrap_failed":                     "A newly rented machine failed to join the fleet.",
	"provider.contract_unsupported":                 "The fleet cannot rent machines from one of its providers.",
	"provider.credentials_refused":                  "A machine provider refused the fleet's credentials, so no new machines can be added from it.",
	"provider.delete_pending":                       "A machine the fleet no longer needs is still being removed.",
	"provider.machine_failed":                       "A machine the fleet asked for did not arrive, so jobs may wait for room.",
	"provider.orphan_found":                         "A machine provider has a machine the fleet cannot account for.",
	"provider.ownership_unverified":                 "The fleet cannot confirm it owns a machine, so it is leaving it alone.",
	"provider.preflight_failed":                     "A machine provider failed its checks, so no new machines can be added from it.",
	"provider.provisioning_paused":                  "Adding machines is paused, so jobs wait for the machines the fleet already has.",
	"provider.quota_exhausted":                      "A machine provider has no more room for the fleet, so jobs wait for the machines it has.",
	"provider.template_unverified":                  "A machine template has not been checked yet.",
	"provider.unreachable":                          "A machine provider cannot be reached, so no new machines can be added from it.",
	"provider.unservable":                           "Some jobs need machines no provider can supply.",
	"provider.zone_missing":                         "A machine provider is missing a location the fleet is set to use.",
	"proxmox.bridge_missing":                        "A machine provider is missing a network the fleet is set to use.",
	"proxmox.credentials_refused":                   "A machine provider refused the fleet's credentials, so no new machines can be added from it.",
	"proxmox.insecure_tls":                          "The fleet talks to a machine provider without checking its certificate.",
	"proxmox.node_missing":                          "A machine provider is missing a server the fleet is set to use.",
	"proxmox.node_offline":                          "A server at a machine provider is offline, so fewer machines can be added.",
	"proxmox.privilege_missing":                     "The fleet lacks a permission at a machine provider, so some machines cannot be added.",
	"proxmox.storage_inactive":                      "Storage at a machine provider is inactive, so no new machines can be added there.",
	"proxmox.storage_missing":                       "A machine provider is missing storage the fleet is set to use.",
	"proxmox.storage_no_images":                     "Storage at a machine provider cannot hold machine images.",
	"proxmox.template_missing":                      "A machine provider is missing the template new machines are made from.",
	"proxmox.template_no_agent":                     "The template new machines are made from cannot report their address.",
	"proxmox.template_not_a_template":               "The template new machines are made from is set up the wrong way.",
	"proxmox.unreachable":                           "A machine provider cannot be reached, so no new machines can be added from it.",
	"proxmox.version_unqualified":                   "A machine provider runs a version this fleet has not been tested with.",
	"proxmox.vmid_range":                            "A machine provider's numbering range is too small for the machines the fleet may need.",
	"proxmox.vmid_range_reserved":                   "A machine provider's numbering range overlaps machines the fleet does not own.",
	"recovery.fenced":                               "The fleet is recovering from a restore and is not starting new runners yet.",
	"runners.cleanup_failed":                        "Some finished runners could not be cleaned up, which uses room new jobs need.",
	"runners.failed":                                "Some runners failed, so jobs may wait while they are replaced.",
	"runners.not_progressing":                       "Some runners are taking far longer to start than they should, so jobs are waiting.",
	"webhook.never_received":                        "The controller has never heard from GitHub directly, so new jobs are noticed late.",
	"webhook.rejected":                              "GitHub's notifications are being refused because their signature does not verify, so new jobs are noticed late.",
}
