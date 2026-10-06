package kennel

import "time"

// Visibility is a repository's visibility as GitHub reports it. An internal
// repository, GitHub Enterprise's, is not public: only members of the
// enterprise can open a pull request against it.
type Visibility string

const (
	VisibilityPublic   Visibility = "public"
	VisibilityPrivate  Visibility = "private"
	VisibilityInternal Visibility = "internal"
)

// Snapshot is everything Evaluate is told about one repository. It is plain
// data, assembled by the controller from the store and from GitHub, and it
// carries no raw repository content: a field is a number, a flag, an
// enumerated word, or an identifier the controller made.
type Snapshot struct {
	// At is when the facts were taken. It is data, because this package reads no
	// clock; waivers are judged against it.
	At       time.Time
	Repo     Repo
	Fleet    Fleet
	Runs     *RunFacts
	Coverage Coverage
}

// Repo is what GitHub says about the repository itself.
type Repo struct {
	// Visibility is empty until the metadata has been read.
	Visibility Visibility
}

// Fleet is what this controller observed about the repository's jobs.
type Fleet struct {
	// Window is how far back these facts reach: the shorter of thirty days and
	// the fleet's job retention. A sentence that says "in the last N days"
	// says this one, never a number somebody remembered.
	Window time.Duration
	Jobs   JobFacts
	// Pools are the pools that ran this repository's jobs inside the window.
	Pools []PoolFact
	// RunnerGroupAllowsPublic is what the organisation runner group the fleet
	// registers into says about public repositories.
	RunnerGroupAllowsPublic Tri
}

// Tri is a yes, a no, or "GitHub did not say". Treating a missing answer as no
// would raise a warning nobody could act on.
type Tri string

const (
	TriUnknown Tri = ""
	TriYes     Tri = "yes"
	TriNo      Tri = "no"
)

// JobFacts counts and measures what the fleet did for the repository.
type JobFacts struct {
	// Ran is the jobs a runner of this fleet executed inside the window.
	Ran int
	// Queued is the jobs waiting for this fleet right now.
	Queued int
	// Unserved lists jobs that waited for a label no pool serves, inside the
	// last seven days, longest first and capped at 200 by the collector.
	Unserved []Unserved
	// Long lists finished jobs that held a runner for LongJobFloor or more,
	// inside the window, capped at 500 by the collector. Handing over only the
	// long ones keeps the snapshot small; deciding which of them hit GitHub's
	// limit stays here, where it can be tested.
	Long []FinishedJob
}

// Unserved is one job that waited for a label no pool serves.
type Unserved struct {
	Waited time.Duration
}

// FinishedJob is one job that held a runner for a long time.
type FinishedJob struct {
	Duration   time.Duration
	Conclusion string
}

// PoolDanger is one way a pool weakens the isolation between a job and its host.
// The controller reduces a pool to these words; the evaluator never sees a pool.
type PoolDanger string

const (
	DangerPersistent  PoolDanger = "persistent"
	DangerHostSocket  PoolDanger = "host_socket"
	DangerPrivileged  PoolDanger = "privileged_docker"
	DangerRoot        PoolDanger = "root"
	DangerNoContainer PoolDanger = "no_container"
)

// PoolFact is one pool that ran jobs for the repository.
type PoolFact struct {
	// ID and Name are the pool's. The name is the operator's own text, so it
	// reaches a finding only as evidence, through the gate in refs.go.
	ID      string
	Name    string
	JobsRun int
	Dangers []PoolDanger
}

// RunFacts are five fields of each run the fleet ran in a public repository:
// nothing else about a run is read, so nothing else can leak into a finding.
type RunFacts struct {
	// Window is how far back the runs reach.
	Window time.Duration
	Runs   []Run
}

// Run is one workflow run.
type Run struct {
	ID int64
	// Event is GitHub's trigger name. The controller passes it through an
	// allow-list, so an event this package does not know arrives as "other".
	Event string
	// FromFork is whether the run's head repository is not the repository it
	// ran in.
	FromFork bool
}
