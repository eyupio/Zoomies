package controller

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

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

	jobsTotal         *prometheus.CounterVec
	queueWait         prometheus.Histogram
	jobDuration       prometheus.Histogram
	scalingEvents     *prometheus.CounterVec
	webhookDeliveries *prometheus.CounterVec
	githubRequests    *prometheus.CounterVec
	reconcileDuration prometheus.Histogram
	buildInfo         *prometheus.GaugeVec
}

func newMetrics(c *Controller) *metrics {
	m := &metrics{
		reg: prometheus.NewRegistry(),
		jobsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "zoomies_jobs_total",
			Help: "Workflow jobs Zoomies has seen complete, by pool and conclusion.",
		}, []string{"pool", "conclusion"}),
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
		reconcileDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "zoomies_reconcile_duration_seconds",
			Help:    "How long one reconcile pass took, including the GitHub calls it made.",
			Buckets: []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		}),
		buildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "zoomies_build_info",
			Help: "Always 1; the version and commit are in the labels.",
		}, []string{"version", "commit"}),
	}
	m.buildInfo.WithLabelValues(version.Version, version.Commit).Set(1)

	m.reg.MustRegister(
		m.jobsTotal, m.queueWait, m.jobDuration, m.scalingEvents,
		m.webhookDeliveries, m.githubRequests, m.reconcileDuration, m.buildInfo,
		&fleetCollector{c: c},
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
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
		"Total runner slots across healthy, uncordoned hosts.", nil, nil)
	descHostCapacityUsed = prometheus.NewDesc("zoomies_host_capacity_used",
		"Runner slots currently occupied.", nil, nil)
)

// fleetCollector reads the fleet's shape from the database on each scrape.
type fleetCollector struct{ c *Controller }

func (f *fleetCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- descRunners
	ch <- descJobsQueued
	ch <- descHosts
	ch <- descHostCapacity
	ch <- descHostCapacityUsed
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

	queuedByPool := map[string]int{}
	for _, j := range queued {
		queuedByPool[j.PoolID]++
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
	}
	// Jobs no pool claimed still have to be visible somewhere.
	gauge(descJobsQueued, float64(queuedByPool[""]), "unmatched")

	var healthy, unhealthy, cordoned, capacity, used int
	now := f.c.Now()
	for _, h := range hosts {
		switch {
		case !h.Healthy(now):
			unhealthy++
		case h.Cordoned:
			cordoned++
		default:
			healthy++
		}
		if h.Healthy(now) && !h.Cordoned {
			capacity += h.Capacity
		}
		used += h.ActiveRunners
	}
	gauge(descHosts, float64(healthy), "healthy")
	gauge(descHosts, float64(unhealthy), "unhealthy")
	gauge(descHosts, float64(cordoned), "cordoned")
	gauge(descHostCapacity, float64(capacity))
	gauge(descHostCapacityUsed, float64(used))
}
