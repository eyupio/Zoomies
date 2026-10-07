package controller

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// collectTimeout bounds the database work one scrape may do. Prometheus
// scrapes on a schedule and does not wait patiently; a slow query must fail
// the scrape rather than pile requests up.
const collectTimeout = 5 * time.Second

// metrics holds the collectors the API serves at /metrics.
//
// The counters and histograms are updated where the events happen. The gauges
// are not stored at all: they are read from the database at scrape time by
// fleetCollector, so they cannot drift from the fleet the way a cached counter
// would after a restart.
type metrics struct {
	reg *prometheus.Registry

	jobsTotal                                                                                    *prometheus.CounterVec
	jobsRunnerLost                                                                               *prometheus.CounterVec
	jobReruns                                                                                    *prometheus.CounterVec
	jobFailures                                                                                  *prometheus.CounterVec
	jobsSized, jobsRan, jobsRerouted                                                             *prometheus.CounterVec
	runnerStartFailures                                                                          *prometheus.CounterVec
	queueWait                                                                                    prometheus.Histogram
	jobDuration                                                                                  prometheus.Histogram
	startupWait, dindReady                                                                       *prometheus.HistogramVec
	queuedToCreate, createToContainer, containerToRegistered, registeredToReady, queuedToStarted *prometheus.HistogramVec
	scalingEvents                                                                                *prometheus.CounterVec
	registrationsDeferred                                                                        *prometheus.CounterVec
	webhookDeliveries                                                                            *prometheus.CounterVec
	githubRequests                                                                               *prometheus.CounterVec
	kennelRequests, kennelOpened, kennelClosed, kennelWaived                                     *prometheus.CounterVec
	kennelHolds                                                                                  prometheus.Counter
	reconcileDuration                                                                            prometheus.Histogram
	storeWriteWait                                                                               prometheus.Histogram
	storeWriteHeld                                                                               prometheus.Histogram
	reconcileErrors                                                                              prometheus.Counter
	cleanups                                                                                     *prometheus.CounterVec
	schedulingLatency, cleanupDuration                                                           *prometheus.HistogramVec
	pollsShed                                                                                    prometheus.Counter
	agentLimited                                                                                 *prometheus.CounterVec
	logRelayDropped                                                                              prometheus.Counter
	buildInfo                                                                                    *prometheus.GaugeVec
	providerOperations                                                                           *prometheus.CounterVec
	providerOperationSeconds                                                                     *prometheus.HistogramVec
	runtimeFailures                                                                              *prometheus.CounterVec
	imagePullFailures                                                                            *prometheus.CounterVec
	imagePrewarms                                                                                *prometheus.CounterVec
	imagePrewarmDuration                                                                         *prometheus.HistogramVec
	elasticCPUDecisions                                                                          *prometheus.CounterVec
	elasticCPUFactor                                                                             *prometheus.HistogramVec
	elasticMemoryDecisions, elasticMemoryNearLimit                                               *prometheus.CounterVec
}

// UnmatchedPool is the `pool` label for work no pool here claims.
//
// A literal rather than an empty string, because Prometheus has no notion of an
// absent label value and a blank one is indistinguishable from a bug. A real
// pool named `unmatched` would merge with it; that is a price worth naming in
// the docs rather than designing around.
const UnmatchedPool = "unmatched"

// poolLabel is the `pool` label value for a pool id, and it is the only place
// that decides what one looks like.
//
// Names, not ids. Every other pool-labelled metric uses the name, and
// zoomies_jobs_total used the id -- so a PromQL query joining a pool's job
// count against its runner count on `pool` silently matched nothing, which is
// the worst kind of wrong for a dashboard.
func (c *Controller) poolLabel(id string) string {
	if id == "" {
		return UnmatchedPool
	}
	if p, err := c.st.GetPool(context.Background(), id); err == nil && p.Name != "" {
		return p.Name
	}
	// A pool deleted between the job finishing and this call still has to be
	// counted somewhere, and its id is the only name left.
	return id
}

func newMetrics(c *Controller) *metrics {
	m := &metrics{
		reg: prometheus.NewRegistry(),
		jobsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_jobs_total",
			Help: "Workflow jobs Zoomies has seen complete, by pool and conclusion.",
		}, []string{"pool", "conclusion"}),
		// A runner the scheduler had already chosen a host for, and did not
		// create, because the installation was at its credential-minting
		// limit. It counts work deferred rather than work failed: the demand
		// stands and a later pass takes it. Without it the limit is invisible
		// -- the pool reports jobs waiting, an operator adds hosts, and the
		// hosts do not help, because hosts were never what ran out.
		registrationsDeferred: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_registrations_deferred_total",
			Help: "Runner creations held back by scheduler.registration_concurrency, by installation. Demand is retained and retried on a later pass.",
		}, []string{"installation"}),
		jobsRunnerLost: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_jobs_runner_lost_total",
			Help: "Jobs whose runner stopped before GitHub reported the job over, by pool. These are the fleet's failures rather than the workflows'.",
		}, []string{"pool"}),
		// Who asked, not whether it worked: a re-run this fleet decided on is
		// spending somebody's GitHub minutes, and an operator turning the
		// setting on wants to see how often it fires before they trust it.
		jobReruns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_job_reruns_total",
			Help: "Re-runs Zoomies has asked GitHub for, by pool and who asked. trigger is operator for the button on a job and fleet_fault for scheduler.auto_rerun.",
		}, []string{"pool", "trigger"}),
		// The same failures as jobs_runner_lost, split by category, and kept
		// beside it rather than replacing it: an operator whose alert fires on
		// the old series should not have it disappear under them on an
		// upgrade. Sum this one by pool and the two agree.
		jobFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_job_failures_total",
			Help: "Job failures by whose they are and why. domain is fleet or workflow; fault is the category, and is empty for a workflow's own failure. Alert on the fleet domain -- a rise there is this deployment, not somebody's tests.",
		}, []string{"pool", "domain", "fault"}),
		// How size routing is doing, in three counters. The first says what the
		// controller classed jobs as and how it decided; the second says where
		// they ran, which is what an operator reads the rate of "classed
		// medium, ran large" off to decide whether to turn routing on; the
		// third says how often the wait for room ran out. None of them is
		// emitted while size routing is off.
		jobsSized: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_jobs_sized_total",
			Help: "Jobs put in a size class when they were queued, by how the class was decided (explicit, pin, history or default), the class, and the size routing mode.",
		}, []string{"basis", "class", "mode"}),
		jobsRan: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_jobs_ran_by_class_total",
			Help: "Classed jobs by the class they were put in and the class of the host that took them. Where the two differ the job ran somewhere other than where it was classed, which for a job that only asked for the base label is GitHub choosing a runner and not a fault.",
		}, []string{"class", "ran_class", "mode"}),
		jobsRerouted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_jobs_rerouted_total",
			Help: "Queued jobs sent to another size class because their own had no pool, or no room for as long as scheduler.size_fallback_wait.",
		}, []string{"from", "to"}),
		// Runner starts that failed, which reach no job at all: the job they
		// were meant for stays queued. A pool climbing here with a queue that
		// never moves is the fleet failing without a single failed job to show
		// for it, which is the blind spot this exists to cover.
		runnerStartFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_runner_start_failures_total",
			Help: "Runners that failed before they could take a job, by pool and category.",
		}, []string{"pool", "fault"}),
		queueWait: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "zoomies_job_queue_wait_seconds",
			Help: "How long jobs waited between being queued and a runner picking them up.",
			// From "a runner was already idle" to "somebody should look at
			// this": one second to an hour.
			Buckets: []float64{1, 5, 10, 30, 60, 120, 300, 600, 1800, 3600},
		}),
		jobDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "zoomies_job_duration_seconds",
			Help:    "How long jobs took to run once they started.",
			Buckets: []float64{10, 30, 60, 300, 600, 1800, 3600, 7200, 21600},
		}),
		queuedToCreate:        startupHistogram("zoomies_runner_queued_to_create_seconds", "Time from a queued job to runner creation."),
		schedulingLatency:     startupHistogram("zoomies_runner_eligible_to_create_task_seconds", "Time from observed job eligibility to its runner's first create task delivery. Excludes prewarmed runners and unobserved timestamps."),
		cleanupDuration:       startupHistogram("zoomies_runner_cleanup_duration_seconds", "Time from runner finish to confirmed host and GitHub removal, including retention. Excludes missing confirmations."),
		startupWait:           startupHistogram("zoomies_runner_startup_queue_seconds", "Time waiting for the host startup admission slot."),
		dindReady:             startupHistogram("zoomies_runner_dind_ready_seconds", "Time creating and awaiting a healthy Docker sidecar, excluding its image pull."),
		createToContainer:     startupHistogram("zoomies_runner_create_to_container_started_seconds", "Time from runner creation to its container starting."),
		containerToRegistered: startupHistogram("zoomies_runner_container_started_to_registered_seconds", "Time from container start to GitHub registration."),
		registeredToReady:     startupHistogram("zoomies_runner_registered_to_ready_seconds", "Time from registration to the runner becoming idle or busy."),
		queuedToStarted:       startupHistogram("zoomies_runner_queued_to_job_started_seconds", "Total time from queueing to job start."),
		scalingEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_scaling_events_total",
			Help: "Scheduler decisions that changed a pool's size, by direction.",
		}, []string{"pool", "direction"}),
		webhookDeliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_webhook_deliveries_total",
			Help: "Inbound webhook deliveries by outcome; a rising rejected count means a secret mismatch or a prober.",
		}, []string{"status"}),
		githubRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_github_api_requests_total",
			Help: "GitHub API calls by installation and outcome.",
		}, []string{"installation", "result"}),
		// Kennel Club's share of the line above, kept apart so that what it spends
		// can be read against the installation's limit without subtracting what the
		// poller and the scheduler spend. It is the number to watch before anybody
		// raises kennel.api_budget_percent.
		kennelRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_kennel_github_requests_total",
			Help: "GitHub API calls made for Kennel Club, by outcome. Read it against the installation's hourly limit before raising kennel.api_budget_percent.",
		}, []string{"result"}),
		// Opened and closed are what makes "is this check useful" answerable from an
		// operator's own Prometheus: a check whose findings are mostly closed by a
		// change is doing its job, and one whose findings are mostly waived is wrong
		// for this fleet.
		kennelOpened: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_kennel_findings_opened_total",
			Help: "Findings Kennel Club has raised, by check code. A finding that comes back after being waived is not counted again.",
		}, []string{"code"}),
		kennelClosed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_kennel_findings_closed_total",
			Help: "Findings that stopped being reported, by check code: fixed, or gone with the repository's jobs. Waived findings are counted apart.",
		}, []string{"code"}),
		kennelWaived: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_kennel_findings_waived_total",
			Help: "Open findings that a waiver now covers, by check code. A high share of a check's findings waived means the check is wrong for this fleet.",
		}, []string{"code"}),
		kennelHolds: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "zoomies_kennel_rate_limit_holds_total",
			Help: "Rate-limit holds that began because a Kennel Club read was refused. The target is zero: any is a defect in the budget.",
		}),
		// The single database writer is the design, and the queue behind it is
		// where a busy instance slows down first: every heartbeat, webhook,
		// scheduling pass and API write waits its turn there. The two halves
		// are kept apart because they point at different fixes -- a long wait
		// says too much is writing, a long hold says one write is too slow --
		// and the buckets reach the ten seconds SQLite's busy timeout allows,
		// which is also how long GitHub waits for a webhook to be answered.
		storeWriteWait: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "zoomies_store_write_wait_seconds",
			Help:    "How long a database write waited for the single writer before it could start.",
			Buckets: []float64{.0005, .001, .005, .01, .05, .1, .5, 1, 2.5, 5, 10},
		}),
		storeWriteHeld: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "zoomies_store_write_held_seconds",
			Help:    "How long a database write then held the single writer, every other write waiting behind it.",
			Buckets: []float64{.0005, .001, .005, .01, .05, .1, .5, 1, 2.5, 5, 10},
		}),
		reconcileDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "zoomies_reconcile_duration_seconds",
			Help:    "How long one reconcile pass took, including the GitHub calls it made.",
			Buckets: []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		}),
		// A pass that failed observes no duration, so the histogram's count is
		// the number of passes that *worked* and this is the rest. Without it a
		// controller whose every pass fails looks like one that is simply not
		// busy: the duration series goes quiet either way.
		reconcileErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "zoomies_reconcile_errors_total",
			Help: "Reconcile passes that failed. The loop carries on, so a rising count is a fleet deciding nothing while looking idle.",
		}),
		// Taking a runner away is the half of the lifecycle that leaves
		// something behind when it goes wrong, and it goes wrong on the host
		// rather than in the fleet -- so it is invisible in every other series
		// here, which count what the fleet decided rather than what the host
		// managed.
		cleanups: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_runner_cleanups_total",
			Help: "Attempts to take a runner off its host, by outcome: succeeded, or failed and recorded on the row.",
		}, []string{"outcome"}),
		// A shed poll is the only sign the controller is holding more agent
		// connections than it wants to: the fleet keeps working, each host
		// just hears about its tasks a little later, so nothing else here
		// moves.
		pollsShed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "zoomies_agent_polls_shed_total",
			Help: "Task polls answered with a backoff because the controller was holding too many at once.",
		}),
		// One series per limit rather than per host: a host id is unbounded
		// cardinality, and the log line that goes with each refusal names it.
		agentLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_agent_requests_limited_total",
			Help: "Agent requests refused or cut short by a per-host limit, by which limit.",
		}, []string{"limit"}),
		logRelayDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "zoomies_log_relay_dropped_bytes_total",
			Help: "Relayed runner output dropped because a stream went over its byte budget.",
		}),
		buildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "zoomies_build_info",
			Help: "Always 1; the version and commit are in the labels.",
		}, []string{"version", "commit"}),
		// An ambiguous outcome is its own label value rather than a failure,
		// because it is the one that means something different: a create that
		// failed costs nothing and a create whose answer was lost may already
		// be a machine somebody is paying for.
		providerOperations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_provider_operations_total",
			Help: "Provider operations by kind and outcome: ok, ambiguous, quota, unreachable or refused.",
		}, []string{"kind", "outcome"}),
		providerOperationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "zoomies_provider_operation_seconds",
			Help: "How long one provider request took. It measures the request, not the clone it starts.",
			// From "a cached answer" to "this hypervisor has stopped
			// answering": a tenth of a second to five minutes.
			Buckets: []float64{.1, .5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		}, []string{"kind"}),
		runtimeFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_host_runtime_failures_total",
			Help: "Container-runtime failures agents reported, each one opening or extending a cooldown on new starts, by kind: unavailable or timeout.",
		}, []string{"kind"}),
		imagePullFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_image_pull_failures_total",
			Help: "Runner starts and prewarms that failed because the pool's image could not be made ready on the host, by pool and kind: start or prewarm.",
		}, []string{"pool", "kind"}),
		imagePrewarms: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_image_prewarms_total",
			Help: "Background image preparations by pool, backend and outcome: prepared, cache_hit, failed or unknown for a successful older agent.",
		}, []string{"pool", "backend", "outcome"}),
		imagePrewarmDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "zoomies_image_prewarm_duration_seconds",
			Help:    "Time an agent spent satisfying background image preparation, including local metadata cache hits.",
			Buckets: []float64{.001, .01, .1, .5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 900},
		}, []string{"pool", "backend", "outcome"}),
		elasticCPUDecisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_elastic_cpu_decisions_total",
			Help: "Elastic CPU decisions by pool, policy mode and outcome. Observe mode records the same plan without changing quotas.",
		}, []string{"pool", "mode", "outcome"}),
		elasticCPUFactor: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "zoomies_elastic_cpu_target_factor",
			Help:    "Planned CPU target divided by the runner's guaranteed CPU, by pool and policy mode.",
			Buckets: []float64{1, 1.25, 1.5, 2, 3, 4, 6, 8},
		}, []string{"pool", "mode"}),
		elasticMemoryDecisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_elastic_memory_decisions_total",
			Help: "What the memory valve reported of each runner it watches, once per heartbeat, by pool, policy mode and outcome: healthy, raised, spilled, at_ceiling, pool_empty, host_floor, unmeasured, unsupported, failed, or unreported for a runner not heard from. Observe mode counts what the valve would have done.",
		}, []string{"pool", "mode", "outcome"}),
		elasticMemoryNearLimit: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_elastic_memory_near_limit_total",
			Help: "Runners that came within a tenth of a memory limit, by pool and policy mode, counted once per runner: the evidence of whether a pool's jobs would use the valve at all.",
		}, []string{"pool", "mode"}),
	}
	m.buildInfo.WithLabelValues(version.Version, version.Commit).Set(1)

	m.reg.MustRegister(
		m.jobsTotal, m.jobsRunnerLost, m.jobReruns, m.jobFailures, m.jobsSized, m.jobsRan, m.jobsRerouted, m.runnerStartFailures, m.queueWait, m.jobDuration, m.scalingEvents,
		m.registrationsDeferred,
		m.webhookDeliveries, m.githubRequests, m.kennelRequests, m.kennelOpened, m.kennelClosed, m.kennelWaived, m.kennelHolds, m.reconcileDuration, m.storeWriteWait, m.storeWriteHeld, m.reconcileErrors, m.cleanups, m.pollsShed, m.agentLimited, m.logRelayDropped, m.buildInfo,
		m.providerOperations, m.providerOperationSeconds,
		m.imagePrewarms, m.imagePrewarmDuration, m.runtimeFailures, m.imagePullFailures,
		m.elasticCPUDecisions, m.elasticCPUFactor,
		m.elasticMemoryDecisions, m.elasticMemoryNearLimit,
		m.startupWait, m.dindReady, m.queuedToCreate, m.createToContainer, m.containerToRegistered, m.registeredToReady, m.queuedToStarted,
		m.schedulingLatency, m.cleanupDuration,
		&fleetCollector{c: c},
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

func startupHistogram(name, help string) *prometheus.HistogramVec {
	return prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: name, Help: help,
		Buckets: []float64{.1, .5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800}}, []string{"pool", "backend"})
}

func observeDuration(h *prometheus.HistogramVec, pool, backend string, from, to time.Time) bool {
	if from.IsZero() || to.IsZero() || to.Before(from) {
		return false
	}
	h.WithLabelValues(pool, backend).Observe(to.Sub(from).Seconds())
	return true
}

// Registry returns the Prometheus registry the API serves at the configured
// metrics path.
func (c *Controller) Registry() *prometheus.Registry { return c.metrics.reg }

// Metric descriptors for the fleet gauges. They are package-level because a
// collector must return the same descriptors on every Describe call.
var (
	descRunners = prometheus.NewDesc("zoomies_runners",
		"Runners by pool and state.", []string{"pool", "state"}, nil)
	descJobsQueued = prometheus.NewDesc("zoomies_jobs_queued",
		"Jobs waiting for a runner, by the pool that claimed them.", []string{"pool"}, nil)
	descHosts = prometheus.NewDesc("zoomies_hosts",
		"Agent hosts by state.", []string{"state"}, nil)
	descHostCapacity = prometheus.NewDesc("zoomies_host_capacity",
		"Total configured runner slots across healthy, uncordoned hosts.", nil, nil)
	// Configured and effective are kept apart rather than one replacing the
	// other: the gap between them is the throttle, and an operator alerting
	// on "the fleet shrank" needs to tell a host somebody resized from a host
	// the controller stepped down.
	descHostEffectiveCapacity = prometheus.NewDesc("zoomies_host_effective_capacity",
		"Runner slots across healthy, uncordoned hosts as their throttles leave them; equals zoomies_host_capacity while no host is throttled.", nil, nil)
	descHostCapacityUsed = prometheus.NewDesc("zoomies_host_capacity_used",
		"Runner slots currently occupied on healthy, uncordoned hosts: the same hosts zoomies_host_capacity counts, so the ratio of the two is utilisation.", nil, nil)
	// Slots say how many runners a fleet will take; these say whether the
	// machines can carry them. A fleet with slots free and no memory left is
	// the case the slot gauges cannot describe, and it is the one an operator
	// is looking for when the queue is not draining.
	descAllocatableCPUs = prometheus.NewDesc("zoomies_host_allocatable_cpus",
		"CPUs across healthy, uncordoned hosts, less each host's reserve.", nil, nil)
	descAllocatableMemory = prometheus.NewDesc("zoomies_host_allocatable_memory_bytes",
		"Memory across healthy, uncordoned hosts, less each host's reserve.", nil, nil)
	descReservedCPUs = prometheus.NewDesc("zoomies_host_reserved_cpus",
		"CPUs the live runners on those hosts have promised away, as of the last scheduling pass.", nil, nil)
	descReservedMemory = prometheus.NewDesc("zoomies_host_reserved_memory_bytes",
		"Memory the live runners on those hosts have promised away, as of the last scheduling pass.", nil, nil)
	// The backlog's depth and its age answer different questions: ten jobs
	// queued for four seconds is a fleet working, and one job queued for forty
	// minutes is a fleet that has stopped, and `zoomies_jobs_queued` cannot
	// tell them apart. This is the one an alert should use.
	descQueueAge = prometheus.NewDesc("zoomies_job_queue_age_seconds",
		"How long the oldest job still waiting has been waiting, by pool. Zero when nothing is waiting.",
		[]string{"pool"}, nil)
	// Rate-limit backoff is per installation and always has been, so this
	// carries the installation rather than being the fleet-wide flag the plan
	// described. A fleet with two installations, one of them held, is a fleet
	// half working -- and the fleet-wide version could not say which half.
	descGitHubPaused = prometheus.NewDesc("zoomies_github_paused",
		"1 while an installation is inside its GitHub rate-limit backoff and every background sweep is standing down from it, 0 otherwise.",
		[]string{"installation"}, nil)
	descHostCPUUsage = prometheus.NewDesc("zoomies_host_cpu_usage_percent",
		"Recent whole-host CPU occupied, including I/O wait. Absent when stale or unmeasured.", []string{"host"}, nil)
	descHostMemoryPool = prometheus.NewDesc("zoomies_host_memory_pool_bytes",
		"Memory the host can still lend to runners of pools that have the memory valve on, which is what no guarantee needs and the host measured as free. Absent for a host with no such runner.", []string{"host"}, nil)
	descHostMemoryLent = prometheus.NewDesc("zoomies_host_memory_lent_bytes",
		"Memory the host's runners hold beyond what they were created with, lent by the memory valve. It is not taken back until the runner that holds it is gone. Absent for a host with no such runner.", []string{"host"}, nil)
	descHostMemoryAvailable = prometheus.NewDesc("zoomies_host_memory_available_bytes",
		"Recent whole-host memory available, including reclaimable cache. Absent when stale or unmeasured.", []string{"host"}, nil)
	descHostAdmissionHeld = prometheus.NewDesc("zoomies_host_admission_held",
		"1 while measured host pressure holds new starts, 0 otherwise. Does not describe operator cordons.", []string{"host"}, nil)
	descHostUsageFresh = prometheus.NewDesc("zoomies_host_usage_fresh",
		"1 when a host usage measurement is recent enough for placement, 0 otherwise.", []string{"host"}, nil)
	// The machines a fleet is renting, by provider and state. Read at scrape
	// time like every other fleet gauge, so it cannot drift from the rows the
	// way a counter kept in memory would across a restart -- and a restart is
	// exactly when somebody is looking at it.
	descProviderMachines = prometheus.NewDesc("zoomies_provider_machines",
		"Machines by provider and state.", []string{"provider", "state"}, nil)
	// Quarantined machines are separated from the state series because they
	// are the one state nothing will move on its own: the number is a queue of
	// work for a person, and an alert on it is an alert on somebody being
	// needed rather than on the fleet's shape.
	descProviderQuarantined = prometheus.NewDesc("zoomies_provider_machines_quarantined",
		"Machines whose ownership could not be proved, which nothing will act on until a person does.", nil, nil)
	descHostLoadAverage = prometheus.NewDesc("zoomies_host_load_average_1m",
		"Recent whole-host one-minute load average. Absent when stale or unmeasured.", []string{"host"}, nil)
	descHostRuntimeRecovering = prometheus.NewDesc("zoomies_host_runtime_recovering",
		"1 while a host's agent reports its container runtime is in a recovery cooldown, 0 otherwise.", []string{"host"}, nil)
	descHostThrottleLevel = prometheus.NewDesc("zoomies_host_throttle_level",
		"The throttle rung a host is on after sustained pressure, 0 to 3; 0 while it is not throttled.", []string{"host"}, nil)
	// What a host's OS report says, as counts and never as names. The failure
	// these are shaped to avoid is an alert that disagrees with the drawer: they
	// read the same count the problems do and are gated by the same function, so
	// a host the drawer says nothing about cannot page somebody from here. They
	// carry no check label, because a metrics endpoint can be public and a check
	// id such as docker.logs would tell anyone who can reach it which setting a
	// host has not changed.
	descHostOSChecks = prometheus.NewDesc("zoomies_host_os_checks",
		"The checks on a host's latest OS report that need attention, by state: warning and error are the ones that count towards its health, skipped is a counted check that could not be assessed, and suggestion is a warning that is only a choice, and accepted is a counted warning an operator has accepted as deliberate, which is in none of the others. Every state is reported for a host with a report; absent for a host that is offline, has sent no report, or sent only the container's partial one.",
		[]string{"host", "state"}, nil)
	descHostRebootPending = prometheus.NewDesc("zoomies_host_reboot_pending",
		"1 while a host's latest OS report says an update is waiting for a reboot, 0 otherwise. Absent when the host is offline, has sent no report, or sent only the container's partial one.",
		[]string{"host"}, nil)
	descHostHealthReportAge = prometheus.NewDesc("zoomies_host_health_report_age_seconds",
		"How old a host's latest OS report is, by the host's own clock; never negative. This is the freshness signal: on a native agent a host whose collector has stopped is still reported and its age climbs, but on a Docker or Compose installation the container's partial report takes over and the series is absent. Absent when the host is offline, has sent no report, or sent only the container's partial one.",
		[]string{"host"}, nil)
)

// fleetCollector reads the fleet's shape from the database on each scrape.
type fleetCollector struct{ c *Controller }

func (f *fleetCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- descRunners
	ch <- descJobsQueued
	ch <- descHosts
	ch <- descHostCapacity
	ch <- descHostCapacityUsed
	ch <- descQueueAge
	ch <- descGitHubPaused
	ch <- descAllocatableCPUs
	ch <- descAllocatableMemory
	ch <- descReservedCPUs
	ch <- descReservedMemory
	ch <- descHostCPUUsage
	ch <- descHostMemoryAvailable
	ch <- descHostMemoryPool
	ch <- descHostMemoryLent
	ch <- descHostAdmissionHeld
	ch <- descHostUsageFresh
	ch <- descProviderMachines
	ch <- descProviderQuarantined
	ch <- descHostLoadAverage
	ch <- descHostThrottleLevel
	ch <- descHostRuntimeRecovering
	ch <- descHostEffectiveCapacity
	ch <- descHostOSChecks
	ch <- descHostRebootPending
	ch <- descHostHealthReportAge
}

func (f *fleetCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
	defer cancel()

	pools, err := f.c.st.ListPools(ctx)
	if err != nil {
		f.c.log.Warn("could not read pools for the metrics endpoint", "error", err)
		return
	}
	counts, err := f.c.st.CountRunnersByPool(ctx)
	if err != nil {
		f.c.log.Warn("could not count runners for the metrics endpoint", "error", err)
		return
	}
	queued, err := f.c.st.ListQueuedJobs(ctx)
	if err != nil {
		f.c.log.Warn("could not read queued jobs for the metrics endpoint", "error", err)
		return
	}
	hosts, err := f.c.st.ListHosts(ctx)
	if err != nil {
		f.c.log.Warn("could not read hosts for the metrics endpoint", "error", err)
		return
	}

	installations, err := f.c.st.ListInstallations(ctx)
	if err != nil {
		f.c.log.Warn("could not read installations for the metrics endpoint", "error", err)
		return
	}

	now := f.c.Now()
	queuedByPool := map[string]int{}
	// The oldest wait per pool, from the moment GitHub queued the job: that is
	// the wait somebody is actually sitting through, and it is the same clock
	// `zoomies_job_queue_wait_seconds` measures once the job finally starts.
	oldestByPool := map[string]float64{}
	for _, j := range queued {
		queuedByPool[j.PoolID]++
		if j.QueuedAt.IsZero() {
			continue
		}
		if age := now.Sub(j.QueuedAt).Seconds(); age > oldestByPool[j.PoolID] {
			oldestByPool[j.PoolID] = age
		}
	}

	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
	}

	for _, p := range pools {
		pc := counts[p.ID]
		// Every state is emitted, including the zeroes: a series that vanishes
		// when a pool empties makes rate() and alerting rules unreliable.
		for state, n := range map[store.RunnerState]int{
			store.RunnerProvisioning: pc.Provisioning,
			store.RunnerRegistering:  pc.Registering,
			store.RunnerIdle:         pc.Idle,
			store.RunnerBusy:         pc.Busy,
			store.RunnerDraining:     pc.Draining,
			store.RunnerFailed:       pc.Failed,
		} {
			gauge(descRunners, float64(n), p.Name, string(state))
		}
		gauge(descJobsQueued, float64(queuedByPool[p.ID]), p.Name)
		gauge(descQueueAge, oldestByPool[p.ID], p.Name)
	}
	// Jobs no pool claimed still have to be visible somewhere.
	gauge(descJobsQueued, float64(queuedByPool[""]), UnmatchedPool)
	gauge(descQueueAge, oldestByPool[""], UnmatchedPool)

	// Every installation reports, held or not: a series that only exists while
	// something is wrong cannot be alerted on with a threshold, and an operator
	// reading the endpoint by hand learns nothing from an absent line.
	held := f.c.heldInstallations(now)
	for _, inst := range installations {
		v := 0.0
		if _, ok := held[inst.ID]; ok {
			v = 1
		}
		gauge(descGitHubPaused, v, inst.ID)
	}

	// By the hosts there are, and the ones that are heard from: the figures are
	// a heartbeat old at best, and a host that was deleted or has fallen silent
	// would otherwise stand at the last pool it had for ever.
	for _, h := range hosts {
		s := f.c.memoryState(h.ID)
		if s.At.IsZero() || !h.Healthy(now) {
			continue
		}
		gauge(descHostMemoryPool, float64(s.Pool.PoolMB)*(1<<20), h.ID)
		gauge(descHostMemoryLent, float64(s.Pool.LentMB)*(1<<20), h.ID)
	}

	var healthy, unhealthy, cordoned, capacity, effective, used int
	for _, h := range hosts {
		fresh, held := 0.0, 0.0
		if h.Usage.Fresh(now) {
			fresh = 1
			if v := h.Usage.CPUPercent; v != nil {
				gauge(descHostCPUUsage, *v, h.ID)
			}
			if v := h.Usage.MemoryAvailableMB; v != nil {
				gauge(descHostMemoryAvailable, float64(*v)*(1<<20), h.ID)
			}
			if v := h.Usage.LoadAverage1; v != nil {
				gauge(descHostLoadAverage, *v, h.ID)
			}
		}
		if scheduler.HostAdmissionReason(h, now) != "" {
			held = 1
		}
		gauge(descHostUsageFresh, fresh, h.ID)
		gauge(descHostAdmissionHeld, held, h.ID)
		// Every host reports a level, throttled or not, for the same reason
		// the paused gauge does: a series that only exists while something
		// is wrong cannot be alerted on with a threshold.
		gauge(descHostThrottleLevel, float64(h.Throttle.Level), h.ID)
		recovering := 0.0
		if h.Incidents.Runtime != nil {
			recovering = 1
		}
		gauge(descHostRuntimeRecovering, recovering, h.ID)
		switch {
		case !h.Healthy(now):
			unhealthy++
		case h.Cordoned:
			cordoned++
		default:
			healthy++
		}
		if h.Healthy(now) && !h.Cordoned {
			capacity += h.Slots()
			effective += h.EffectiveCapacity()
			// The same hosts as the capacity it is divided by; see Stats.
			used += h.ActiveRunners
		}
	}
	// Only the hosts that can actually take work, which is the same set
	// descHostCapacity counts: a cordoned host's memory is not the fleet's to
	// place into, and counting it would say the fleet has room it will not use.
	var allocCPUs, reservedCPUs float64
	var allocMemory, reservedMemory int64
	for _, h := range hosts {
		if !h.Healthy(now) || h.Cordoned {
			continue
		}
		a := h.Allocatable()
		allocCPUs += a.CPUs
		allocMemory += a.MemoryMB
		if r, ok := f.c.reservedOn(h.ID); ok {
			reservedCPUs += r.CPUs
			reservedMemory += r.MemoryMB
		}
	}
	const mb = 1 << 20
	gauge(descAllocatableCPUs, allocCPUs)
	gauge(descAllocatableMemory, float64(allocMemory)*mb)
	gauge(descReservedCPUs, reservedCPUs)
	gauge(descReservedMemory, float64(reservedMemory)*mb)

	f.collectMachines(ctx, gauge)
	f.collectHostHealth(hosts, now, gauge)

	gauge(descHosts, float64(healthy), "healthy")
	gauge(descHosts, float64(unhealthy), "unhealthy")
	gauge(descHosts, float64(cordoned), "cordoned")
	gauge(descHostCapacity, float64(capacity))
	gauge(descHostEffectiveCapacity, float64(effective))
	gauge(descHostCapacityUsed, float64(used))
}

// collectMachines reports what each provider is renting.
//
// Every state of every configured provider is emitted, zeroes included, for the
// reason the runner gauges are: a series that vanishes when a provider empties
// makes an alerting rule unreliable, and "no machines" is exactly what somebody
// paging on a stuck fleet needs to be able to see.
func (f *fleetCollector) collectMachines(ctx context.Context, gauge func(*prometheus.Desc, float64, ...string)) {
	providers, err := f.c.st.ListProviders(ctx)
	if err != nil {
		f.c.log.Warn("could not read providers for the metrics endpoint", "error", err)
		return
	}
	if len(providers) == 0 {
		return
	}
	quarantined := 0
	for _, p := range providers {
		machines, err := f.c.st.ListMachinesForProvider(ctx, p.ID)
		if err != nil {
			f.c.log.Warn("could not read a provider's machines for the metrics endpoint",
				"provider", p.ID, "error", err)
			continue
		}
		counts := map[store.MachineState]int{}
		for _, m := range machines {
			counts[m.State]++
			if m.State == store.MachineQuarantined {
				quarantined++
			}
		}
		for _, state := range []store.MachineState{
			store.MachinePlanned, store.MachineCreating, store.MachineStarting,
			store.MachineBootstrapping, store.MachineEnrolling, store.MachineReady,
			store.MachineDraining, store.MachineDeleting, store.MachineFailed,
			store.MachineQuarantined,
		} {
			gauge(descProviderMachines, float64(counts[state]), p.Name, string(state))
		}
	}
	gauge(descProviderQuarantined, float64(quarantined))
}

// collectHostHealth reports what each host's OS report says, for the hosts it is
// fair to judge and for no others.
//
// It asks hostHealthReport, the function the problems ask, so a host the drawer
// says nothing about -- one that is offline, has sent no report or sent only the
// container's partial one -- has no series at all. Missing is deliberately not
// zero: a zero here would read as a clean bill of health for a machine nobody
// has looked at. A host whose report has gone stale is still reported, because
// the age is the signal that says so, and an alert on it agrees with
// host.health_stale.
//
// Every state is emitted for a host that reports, zeroes included, for the reason
// the runner gauges are: a threshold rule on the error count has to keep
// matching when there are none. Exactly one sample is written per host and state;
// a duplicate would fail the whole scrape.
func (f *fleetCollector) collectHostHealth(hosts []*store.Host, now time.Time, gauge func(*prometheus.Desc, float64, ...string)) {
	for _, h := range hosts {
		r := hostHealthReport(h, now)
		if r == nil {
			continue
		}
		s := r.Summary()
		for _, c := range []struct {
			state string
			n     int
		}{
			{"warning", s.Warnings},
			{"error", s.Errors},
			{"skipped", s.Skipped},
			{"suggestion", s.Suggestions},
			// A warning an operator accepted is in no other state, so its
			// silence stays countable; warning keeps meaning unaccepted ones, so
			// an existing alert rule means what it did.
			{"accepted", s.Accepted},
		} {
			gauge(descHostOSChecks, float64(c.n), h.ID, c.state)
		}
		pending := 0.0
		if r.RebootPending {
			pending = 1
		}
		gauge(descHostRebootPending, pending, h.ID)
		// A report is accepted up to a minute ahead of the controller's clock, so
		// the age can come out negative; a negative age would read as a report
		// from the future rather than as a fresh one.
		gauge(descHostHealthReportAge, max(now.Sub(r.CheckedAt).Seconds(), 0), h.ID)
	}
}
