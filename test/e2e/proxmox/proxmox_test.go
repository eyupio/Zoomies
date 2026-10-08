//go:build e2e

package proxmox

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The live qualification: twenty create, enrol, run, drain and delete cycles
// against a real Proxmox VE cluster, the five fault cases the roadmap names, and
// the closing inventory reconciliation that decides all of it.
//
// roadmap/validation/proxmox-qualification.md is the procedure, and this file
// produces exactly the evidence its tables ask for. The two must not disagree:
// if a case here stops matching a row there, the row is the specification and
// this is what has drifted.
//
// Two things to know before reading it. Nothing is created until every
// prerequisite holds, including the read-only ones against the cluster itself.
// And the blast radius -- the cluster, the node, the storage, the VMID range and
// everything already in it -- is written to a ledger outside the temp directory
// before the first call that could build anything, so a run killed at any point
// leaves a record of where to go and look.

// fixture is everything the cases share: one controller, one provider, one
// installation and two pools.
type fixture struct {
	t       *testing.T
	e       env
	rec     *Record
	led     *Ledger
	cluster *Cluster
	ctl     *controllerProcess
	api     *client

	runID          string
	installationID string
	providerID     string
	settings       map[string]string
	pools          []poolRef
}

// poolRef is one pool and the label that is unique to it, which is what keeps
// two pools -- and two concurrent runs against one organisation -- out of each
// other's jobs.
type poolRef struct {
	ID    string
	Name  string
	Label string
}

func TestProxmoxQualification(t *testing.T) {
	if !requested() {
		t.Skip(skipReason())
	}

	// Before anything: is there time to finish? A run killed part-way leaves
	// virtual machines behind and never reaches the reconciliation that would
	// have found them, so this is the first assertion rather than a comment.
	if left, ok := remaining(t); ok && left < qualificationBudget {
		t.Fatalf("this procedure's waits add up to %s and `go test` has given it %s.\n"+
			"Run it with a timeout that fits: `make test-e2e-proxmox`. Starting now would build machines "+
			"and be killed before the closing reconciliation could account for them",
			qualificationBudget, left.Round(time.Second))
	}

	e, missing := preflight()
	if len(missing) > 0 {
		blocked(t, missing)
	}

	cluster, err := NewCluster(ClusterOptions{
		Endpoint: e.endpoint, Token: e.token, Secret: e.secret,
		CAPEMFile: e.caPEMFile, Insecure: e.insecure,
	})
	if err != nil {
		blocked(t, []string{"the cluster credential is unusable: " + err.Error()})
	}
	ctx, cancel := context.WithTimeout(context.Background(), waitProviderChecked)
	facts, missing := preflightCluster(ctx, cluster, e)
	cancel()
	if len(missing) > 0 {
		blocked(t, missing)
	}

	runID := newRunID()
	rec := newRecord(runID)
	t.Cleanup(func() {
		path := recordPath(runID)
		if err := rec.Write(path); err != nil {
			t.Errorf("writing the qualification evidence: %v", err)
			return
		}
		t.Logf("evidence: %s", path)
	})

	// The ledger claims the blast radius before anything is created, and the
	// sweep reports what earlier runs left, before this run adds to it.
	led, err := openLedger(runID, "zoomies-pve-"+runID, e, facts.PreexistingVMIDs)
	if err != nil {
		blocked(t, []string{"the ledger could not be opened: " + err.Error()})
	}
	sweep(t, runID, e)

	f := &fixture{t: t, e: e, rec: rec, led: led, cluster: cluster, runID: runID}
	f.describeSetup(facts)

	// The controller starts before the cleanup below is registered, so that
	// cleanup order puts them the right way round: t.Cleanup runs
	// last-registered first, and tidying up has to happen while the API it
	// deletes through is still listening.
	f.ctl = startController(t, e)
	f.api = newClient(t, f.ctl)
	t.Cleanup(func() { f.tidyUp() })

	f.connectGitHub()
	f.createProvider()
	f.createPools()

	// The cases, in the record's order. Each is a subtest so that one failing
	// does not take the reconciliation with it -- the reconciliation is the step
	// that decides qualification, and a failed run is exactly when it matters.
	t.Run("scale from zero", f.scaleFromZero)
	t.Run("twenty cycles", f.twentyCycles)
	t.Run("multi-pool burst", f.multiPoolBurst)
	t.Run("controller restart mid-create", f.restartMidCreate)
	t.Run("bootstrap failure", f.bootstrapFailure)
	t.Run("delete failure", f.deleteFailure)
	t.Run("closing inventory reconciliation", f.reconcile)
}

// blocked is a prerequisite that is not there. It is a failure when somebody
// asked for a qualification and a skip on a laptop, and it is never silent:
// before this distinction existed, a harness that had never once talked to a
// hypervisor reported the same green as one that had run the whole procedure.
func blocked(t *testing.T, missing []string) {
	t.Helper()
	msg := "blocked, so nothing was created:\n  " + strings.Join(missing, "\n  ")
	if required() {
		t.Fatal(msg)
	}
	t.Skip(msg)
}

// sweep reports what earlier runs left behind, before this run creates
// anything, so that its findings belong to them and cannot be confused with
// this one's.
//
// It reports and does not delete. A stale ledger names virtual machines on
// somebody's cluster, and a harness that quietly destroyed them would be
// destroying the evidence of its own last failure.
func sweep(t *testing.T, runID string, e env) {
	t.Helper()
	stale, err := staleLedgers(runID, e)
	if err != nil {
		t.Logf("sweep: could not read the ledger directory: %v", err)
		return
	}
	for _, rec := range stale {
		t.Logf("sweep: run %s (%s) never closed its ledger and may have left: %s",
			rec.RunID, rec.StartedAt.Format(time.RFC3339), strings.Join(rec.Outstanding(), ", "))
	}
	if len(stale) > 0 {
		t.Logf("sweep: %d earlier run(s) did not finish tidying up; "+
			"`go run ./test/e2e/proxmox/verify` says what is still on the cluster", len(stale))
	}
}

// ---------------------------------------------------------------------------
// Setup
// ---------------------------------------------------------------------------

// describeSetup fills in the record's setup table. A number without the setup
// that produced it is not evidence, and every row here comes from the run rather
// than from the plan: the version is what the cluster said, the template is what
// was found on it.
func (f *fixture) describeSetup(facts clusterFacts) {
	e := f.e
	f.rec.setup("Zoomies commit", f.rec.Commit)
	f.rec.setup("Proxmox VE version", facts.Version)
	f.rec.setup("Node(s)", e.node)
	f.rec.setup("Template VMID, OS and image", fmt.Sprintf("%d (%s)", e.templateID, orDash(facts.TemplateName)))
	f.rec.setup("Storage", e.storage)
	f.rec.setup("Network bridge", e.bridge)
	f.rec.setup("VMID range", e.vmidRange().String())
	f.rec.setup("Machine shape", fmt.Sprintf("%d cpus / %d MB memory / %s disk",
		e.cpus, e.memoryMB, diskText(e.diskMB)))
	f.rec.setup("Runner backend in the guest", e.backend)
	f.rec.setup("Cluster certificate", map[bool]string{
		true:  "verified",
		false: "NOT verified (ZOOMIES_PROXMOX_INSECURE)",
	}[f.cluster.Verified()])
	f.rec.setup("Controller URL the machines were given", e.controllerURL)
	f.rec.setup("Observed-timing resolution", "polled every "+watchInterval.String()+
		"; phases taken from the machine timeline are the controller's own timestamps and are exact")
}

func diskText(mb int) string {
	if mb == 0 {
		return "the template's"
	}
	return strconv.Itoa(mb) + " MB"
}

// connectGitHub creates the installation the pools register runners through, and
// proves it verifies before anything depends on it.
func (f *fixture) connectGitHub() {
	t := f.t
	t.Helper()
	var inst struct {
		ID string `json:"id"`
	}
	f.api.post("/installations", map[string]any{
		"app_id":          mustInt(t, f.e.appID),
		"installation_id": mustInt(t, f.e.installationID),
		"target":          f.e.target,
		"target_type":     f.e.targetType,
		"private_key":     f.e.privateKey,
	}, &inst)
	if inst.ID == "" {
		t.Fatal("the installation was created but came back without an ID")
	}
	f.installationID = inst.ID
	if err := f.led.created(KindInstallation, inst.ID, f.e.target); err != nil {
		t.Fatalf("recording the installation in the ledger: %v", err)
	}

	var health struct {
		OK                 bool     `json:"ok"`
		Message            string   `json:"message"`
		MissingPermissions []string `json:"missing_permissions"`
	}
	f.api.post("/installations/"+inst.ID+"/verify", nil, &health)
	if !health.OK {
		t.Fatalf("the installation did not verify: %s (missing: %v)", health.Message, health.MissingPermissions)
	}
}

// createProvider makes the provider row and runs its live preflight, which is
// where the cluster's version comes from for a second time -- through the
// product this time rather than around it.
func (f *fixture) createProvider() {
	t := f.t
	t.Helper()
	e := f.e

	f.settings = map[string]string{
		"nodes":          e.node,
		"storage":        e.storage,
		"bridge":         e.bridge,
		"template_id":    strconv.Itoa(e.templateID),
		"vmid_min":       strconv.Itoa(e.vmidMin),
		"vmid_max":       strconv.Itoa(e.vmidMax),
		"controller_url": e.controllerURL,
	}
	if e.pool != "" {
		f.settings["pool"] = e.pool
	}
	credential := e.token
	if e.secret != "" {
		// A token given as an identifier and a secret separately: the
		// identifier is not a secret and belongs in the settings, where every
		// message that asks for a privilege can quote it.
		f.settings["api_token_id"] = e.token
		credential = e.secret
	}

	body := map[string]any{
		"kind":                 "proxmox",
		"name":                 "qualification-" + f.runID,
		"endpoint":             e.endpoint,
		"credential":           credential,
		"insecure_skip_verify": e.insecure,
		"settings":             f.settings,
		"machine_capacity":     2,
		"machine_backend":      e.backend,
		"machine_cpus":         e.cpus,
		"machine_memory_mb":    e.memoryMB,
		"machine_disk_mb":      e.diskMB,
		// Four machines and two creates in flight: enough for the multi-pool
		// burst to want two at once, and low enough that a runaway would be
		// four VMs rather than a cluster.
		"max_machines":          4,
		"max_creates_in_flight": 2,
		"idle_timeout":          "45m",
		"enabled":               true,
	}
	if e.caPEMFile != "" {
		body["ca_pem"] = readFile(t, e.caPEMFile)
	}

	var prov struct {
		ID string `json:"id"`
	}
	f.api.post("/providers", body, &prov)
	if prov.ID == "" {
		t.Fatal("the provider was created but came back without an ID")
	}
	f.providerID = prov.ID
	if err := f.led.created(KindProvider, prov.ID, "qualification-"+f.runID); err != nil {
		t.Fatalf("recording the provider in the ledger: %v", err)
	}

	var check struct {
		OK        bool   `json:"ok"`
		Reachable bool   `json:"reachable"`
		Version   string `json:"version"`
		Findings  []struct {
			Code, Severity, Title, Fix string
		} `json:"findings"`
	}
	f.api.post("/providers/"+prov.ID+"/check", nil, &check)
	f.rec.setup("Preflight, through the product", fmt.Sprintf("reachable=%t, ok=%t, version %s",
		check.Reachable, check.OK, orDash(check.Version)))
	f.rec.setup("Fleet and provider limits",
		"provider max_machines=4, max_creates_in_flight=2, machine_capacity=2; fleet defaults otherwise")
	if !check.OK {
		for _, fnd := range check.Findings {
			t.Logf("preflight: [%s] %s -- %s: %s", fnd.Severity, fnd.Code, fnd.Title, fnd.Fix)
		}
		t.Fatalf("the provider's own preflight refuses this configuration; nothing has been built. "+
			"reachable=%t, %d finding(s) above", check.Reachable, len(check.Findings))
	}
}

// createPools makes the two pools the cases use. Two, because the burst needs
// two things wanting machines at the same moment, and each carries a label of
// its own so a job can never be answered by the wrong one.
func (f *fixture) createPools() {
	t := f.t
	t.Helper()
	for _, suffix := range []string{"a", "b"} {
		label := fmt.Sprintf("zoomies-pve-%s-%s", f.runID, suffix)
		var pool struct {
			ID string `json:"id"`
		}
		f.api.post("/pools", map[string]any{
			"installation_id": f.installationID,
			"name":            fmt.Sprintf("pve-%s-%s", f.runID, suffix),
			"labels":          []string{label},
			"backend":         f.e.backend,
			"min_runners":     0,
			"max_runners":     1,
			"idle_timeout":    "1m",
			"ephemeral":       true,
			"docker_mode":     "none",
		}, &pool)
		if pool.ID == "" {
			t.Fatal("a pool was created but came back without an ID")
		}
		if err := f.led.created(KindPool, pool.ID, label); err != nil {
			t.Fatalf("recording the pool in the ledger: %v", err)
		}
		f.pools = append(f.pools, poolRef{ID: pool.ID, Name: "pve-" + f.runID + "-" + suffix, Label: label})
	}
}

// ---------------------------------------------------------------------------
// One cycle
// ---------------------------------------------------------------------------

// cycleResult is what one cycle observed, and the phases it timed.
type cycleResult struct {
	machineID string
	name      string
	vmid      int
	node      string
	jobOK     bool
	notes     []string
}

// runCycle is the procedure's unit: queued work with nowhere to go, a machine
// bought for it, a guest that enrols, a real job, a drain and a delete that is
// not complete until an inspect cannot find the resource.
func (f *fixture) runCycle(t *testing.T, pool poolRef, timed bool) cycleResult {
	t.Helper()
	var out cycleResult

	before := map[string]bool{}
	for _, m := range f.api.machines() {
		before[m.ID] = true
	}

	dispatchedAt := time.Now()
	_ = dispatchWorkflow(t, f.e, pool.Label)

	// 1. A machine row must appear. Nothing has been built yet: this is the
	//    fleet deciding, and a long wait here means the demand never reached the
	//    provider rather than that the hypervisor is slow.
	var id string
	waitFor(t, waitFirstMachine, "a machine to be planned for "+pool.Name, func() bool {
		for _, m := range f.api.machines() {
			if !before[m.ID] {
				id = m.ID
				return true
			}
		}
		return false
	})
	out.machineID = id
	name := f.api.machine(id).Name
	out.name = name
	if err := f.led.created(KindMachine, id, name); err != nil {
		t.Fatalf("recording the machine in the ledger: %v", err)
	}

	// 2. As soon as the provider has chosen an identifier, write it down. The
	//    window between the row and the identifier is the one in which a run can
	//    die owning a guest it cannot name.
	w := newWatch()
	if !w.until(f.api, id, waitMachineCreated, func(m machineView) bool { return m.ResourceID != "" }) {
		t.Fatalf("machine %s has no resource after %s: %s", id, waitMachineCreated, w.last.complaint())
	}
	out.vmid, out.node = w.last.vmid(), w.last.ResourceZone
	if err := f.led.placed(id, out.node, out.vmid); err != nil {
		t.Fatalf("recording where the machine was built: %v", err)
	}
	if !f.e.vmidRange().contains(out.vmid) {
		t.Fatalf("machine %s was built at VMID %d, outside the reserved range %s; "+
			"the range is the one thing that keeps this procedure off somebody else's guests",
			id, out.vmid, f.e.vmidRange())
	}

	// 3. It must become a host.
	if !w.until(f.api, id, waitMachineReady, func(m machineView) bool {
		return m.State == "ready" || m.State == "failed" || m.State == "quarantined"
	}) {
		t.Fatalf("machine %s stopped at %q after %s: %s", id, w.last.State, waitMachineReady, w.last.complaint())
	}
	if w.last.State != "ready" {
		t.Fatalf("machine %s ended %q rather than ready: %s", id, w.last.State, w.last.complaint())
	}
	if w.last.HostID == "" {
		t.Errorf("machine %s is ready and linked to no host, so nothing could ever be scheduled on it", id)
	}

	// 4. A real job, on that machine.
	job := f.waitForJob(t, pool)
	out.jobOK = job.Conclusion == "success"
	if !out.jobOK {
		t.Errorf("the job on %s concluded %q, want success", pool.Name, job.Conclusion)
	}

	// 5. Drain, and wait for the last runner to finish.
	drainAt := time.Now()
	f.api.post("/machines/"+id+"/drain", map[string]any{}, nil)
	if !w.until(f.api, id, waitMachineDrained, func(m machineView) bool {
		return f.runnersOn(m.HostID) == 0
	}) {
		t.Fatalf("machine %s still has %d runner(s) %s after being drained",
			id, f.runnersOn(w.last.HostID), waitMachineDrained)
	}
	drainedAt := time.Now()

	// 6. Delete, and do not believe it until an inspect cannot find it.
	f.api.del("/machines/" + id)
	if !w.until(f.api, id, waitMachineDeleted, func(m machineView) bool { return m.State == "deleted" }) {
		t.Fatalf("machine %s is %q %s after the delete was issued: %s",
			id, w.last.State, waitMachineDeleted, w.last.complaint())
	}
	if w.last.DeletedAt == nil {
		t.Errorf("machine %s says deleted and carries no confirmation timestamp; "+
			"only an inspect that could not find the resource writes that one", id)
	}
	f.led.removed(KindMachine, id)

	if timed {
		f.timeCycle(w.last, job, dispatchedAt, drainAt, drainedAt)
	}
	return out
}

// jobView is the part of GET /jobs this procedure reads.
type jobView struct {
	ID          string    `json:"id"`
	State       string    `json:"state"`
	Conclusion  string    `json:"conclusion"`
	RunnerID    string    `json:"runner_id"`
	Labels      []string  `json:"labels"`
	QueuedAt    time.Time `json:"queued_at"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
}

// waitForJob waits for this pool's job to finish. The pool's label is unique to
// this run, so a job carrying it is ours by construction.
func (f *fixture) waitForJob(t *testing.T, pool poolRef) jobView {
	t.Helper()
	var found jobView
	waitFor(t, waitJobCompleted, "a job to complete on "+pool.Name, func() bool {
		var out struct {
			Items []jobView `json:"items"`
		}
		f.api.get("/jobs?pool_id="+pool.ID, &out)
		for _, j := range out.Items {
			if j.State != "completed" {
				continue
			}
			var full jobView
			f.api.get("/jobs/"+j.ID, &full)
			found = full
			return true
		}
		return false
	})
	if found.RunnerID == "" {
		t.Errorf("the completed job on %s is not linked to the runner that ran it", pool.Name)
	}
	return found
}

// runnersOn is how many live runners a host still has. It is asked of the
// controller because the question is about the fleet's own accounting: a drain
// is finished when nothing is running on the machine.
func (f *fixture) runnersOn(hostID string) int {
	if hostID == "" {
		return 0
	}
	var out struct {
		Items []struct {
			State string `json:"state"`
		} `json:"items"`
	}
	f.api.get("/runners?host_id="+hostID+"&include_removed=false", &out)
	return len(out.Items)
}

// timeCycle files one cycle's phases into the evidence.
//
// Everything that can come from the machine's timeline does, because those are
// the controller's own timestamps and are exact; only the two phases with no
// timestamp of their own -- a guest that has begun answering, a drain that has
// finished -- are this harness's observations, and the record says so.
func (f *fixture) timeCycle(m machineView, job jobView, dispatchedAt, drainAt, drainedAt time.Time) {
	at := func(phase string) (time.Time, bool) { return m.at(phase) }
	span := func(name string, from, to time.Time, ok bool) {
		if ok && !from.IsZero() && !to.IsZero() {
			f.rec.timings.add(name, to.Sub(from))
		}
	}

	queued := job.QueuedAt
	if queued.IsZero() {
		// GitHub's own queue time is the honest denominator; without it, when
		// this run asked for the work is the next best thing.
		queued = dispatchedAt
	}
	creating, okCreating := at("creating")
	span("queue to create issued", queued, creating, okCreating)

	running, okRunning := at("starting")
	if !okRunning {
		// A provider whose create leaves the guest running skips starting, and
		// the phase then ends where the create did.
		running, okRunning = at("created")
	}
	span("create to resource running", creating, running, okCreating && okRunning)

	bootstrapped, okBootstrapped := at("bootstrapped")
	span("running to guest agent answering", running, bootstrapped, okRunning && okBootstrapped)

	ready, okReady := at("ready")
	span("bootstrap to host joined", bootstrapped, ready, okBootstrapped && okReady)
	span("host joined to first job started", ready, job.StartedAt, okReady && !job.StartedAt.IsZero())

	span("drain requested to last runner finished", drainAt, drainedAt, true)

	deleting, okDeleting := at("deleting")
	deleted, okDeleted := at("deleted")
	span("delete issued to resource confirmed gone", deleting, deleted, okDeleting && okDeleted)
}

// ---------------------------------------------------------------------------
// The cases
// ---------------------------------------------------------------------------

// scaleFromZero is the first case for a reason: it is the only moment in the
// whole procedure when the fleet demonstrably has nothing at all. A pool with no
// eligible host is not the same as a full fleet, and this is where the
// difference is worth money.
func (f *fixture) scaleFromZero(t *testing.T) {
	assertTimeToFinish(t, scaleFromZeroBudget, "scale from zero")
	row := CaseRow{Number: 2, Case: "Scale from zero, no hosts at all, then queued work"}
	defer func() { f.finish(t, &row) }()

	var hosts struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	f.api.get("/hosts", &hosts)
	if len(hosts.Items) > 0 {
		t.Fatalf("the fleet already has %d host(s); this case can only prove anything from nothing", len(hosts.Items))
	}
	if ms := f.api.machines(); len(ms) > 0 {
		t.Fatalf("the fleet already has %d machine(s) before the first cycle", len(ms))
	}

	start := time.Now()
	res := f.runCycle(t, f.pools[0], true)
	row.Timings = "from an empty fleet to a deleted machine: " + round(time.Since(start))
	row.Observed = fmt.Sprintf("no hosts and no machines at the start; VM %d on %s served the job and was destroyed",
		res.vmid, res.node)
}

// twentyCycles is the body of the procedure. Twenty, sequentially, so that each
// timing belongs to one machine and the percentiles mean what they say.
func (f *fixture) twentyCycles(t *testing.T) {
	row := CaseRow{Number: 1, Case: fmt.Sprintf("%d full cycles", qualificationCycles)}
	defer func() { f.finish(t, &row) }()

	done := 0
	for i := 1; i <= qualificationCycles; i++ {
		// Before each one rather than once at the start: a cycle that ran long
		// has eaten into what is left, and the run must stop while there is
		// still time to delete what it has built and reconcile the cluster.
		assertTimeToFinish(t, cycleBudget, fmt.Sprintf("cycle %d of %d", i, qualificationCycles))
		res := f.runCycle(t, f.pools[0], true)
		done++
		t.Logf("cycle %d/%d: machine %s at VMID %d on %s, job %s",
			i, qualificationCycles, res.machineID, res.vmid, res.node,
			map[bool]string{true: "succeeded", false: "FAILED"}[res.jobOK])
	}
	row.Observed = fmt.Sprintf("%d of %d cycles completed; every machine was built inside %s",
		done, qualificationCycles, f.e.vmidRange())
	row.Timings = "see the timings table; every figure carries the count it came from"
}

// multiPoolBurst is two pools wanting a machine at the same moment. The failure
// it looks for is the fleet buying the same shortfall twice, or buying one
// machine and leaving the second pool waiting for ever.
func (f *fixture) multiPoolBurst(t *testing.T) {
	assertTimeToFinish(t, multiPoolBurstBudget, "the multi-pool burst")
	row := CaseRow{Number: 3, Case: "Multi-pool burst, two pools demanding at once"}
	defer func() { f.finish(t, &row) }()

	before := map[string]bool{}
	for _, m := range f.api.machines() {
		before[m.ID] = true
	}
	for _, p := range f.pools {
		_ = dispatchWorkflow(t, f.e, p.Label)
	}

	// Both pools must end up with a machine of their own: one machine cannot
	// carry two pools' labels, so a fleet that bought one has left a pool
	// waiting.
	var fresh []machineView
	waitFor(t, waitFirstMachine+waitMachineCreated, "a machine for each of the two pools", func() bool {
		fresh = nil
		for _, m := range f.api.machines() {
			if !before[m.ID] {
				fresh = append(fresh, m)
			}
		}
		return len(fresh) >= len(f.pools)
	})
	for _, m := range fresh {
		if err := f.led.created(KindMachine, m.ID, m.Name); err != nil {
			t.Fatalf("recording a burst machine in the ledger: %v", err)
		}
	}
	if len(fresh) > len(f.pools) {
		t.Errorf("two pools wanted one machine each and the fleet bought %d; "+
			"overlapping demand has been counted twice", len(fresh))
	}

	// Each must reach ready, run its pool's job, and then be taken away again.
	for i, p := range f.pools {
		id := fresh[min(i, len(fresh)-1)].ID
		w := newWatch()
		if !w.until(f.api, id, waitMachineReady+waitMachineCreated, func(m machineView) bool {
			return m.State == "ready" || m.State == "failed" || m.State == "quarantined"
		}) || w.last.State != "ready" {
			t.Fatalf("burst machine %s for %s ended %q: %s", id, p.Name, w.last.State, w.last.complaint())
		}
		if err := f.led.placed(id, w.last.ResourceZone, w.last.vmid()); err != nil {
			t.Fatalf("recording where a burst machine was built: %v", err)
		}
	}
	for _, p := range f.pools {
		job := f.waitForJob(t, p)
		if job.Conclusion != "success" {
			t.Errorf("the burst job on %s concluded %q, want success", p.Name, job.Conclusion)
		}
	}
	for _, m := range fresh {
		f.deleteMachine(t, m.ID)
	}
	row.Observed = fmt.Sprintf("%d pools demanded at once and the fleet bought %d machine(s), one each",
		len(f.pools), len(fresh))
}

// restartMidCreate kills the controller while a create is in flight.
//
// The failure this exists for is the natural reflex: the call did not come back,
// so try again -- and now there are two virtual machines and a row that knows
// about one. The cluster is asked afterwards, not the controller, because the
// controller's opinion of how many VMs it made is the thing under test.
func (f *fixture) restartMidCreate(t *testing.T) {
	assertTimeToFinish(t, restartMidCreateBudget, "the restart mid-create")
	row := CaseRow{Number: 4, Case: "Controller restart mid-create"}
	defer func() { f.finish(t, &row) }()

	before := map[string]bool{}
	for _, m := range f.api.machines() {
		before[m.ID] = true
	}
	_ = dispatchWorkflow(t, f.e, f.pools[0].Label)

	var id string
	waitFor(t, waitFirstMachine, "a machine to be planned", func() bool {
		for _, m := range f.api.machines() {
			if !before[m.ID] {
				id = m.ID
				return true
			}
		}
		return false
	})
	m := f.api.machine(id)
	if err := f.led.created(KindMachine, id, m.Name); err != nil {
		t.Fatalf("recording the machine in the ledger: %v", err)
	}

	// Restart at the instant the create is under way: the row exists, an
	// identifier has been persisted, and the outcome of the call is unknown.
	w := newWatch()
	if !w.until(f.api, id, waitMachineCreated, func(m machineView) bool {
		return m.State == "creating" || m.ResourceID != ""
	}) {
		t.Fatalf("machine %s never reached a create to be interrupted: %s", id, w.last.complaint())
	}
	name := w.last.Name
	f.ctl.restart()
	row.Observed = fmt.Sprintf("restarted while %s was %q", id, w.last.State)

	if !w.until(f.api, id, waitMachineCreated+waitMachineReady, func(m machineView) bool {
		return m.State == "ready" || m.State == "failed" || m.State == "quarantined"
	}) {
		t.Fatalf("machine %s is %q after the restart: %s", id, w.last.State, w.last.complaint())
	}
	if err := f.led.placed(id, w.last.ResourceZone, w.last.vmid()); err != nil {
		t.Fatalf("recording where the machine was built: %v", err)
	}
	if w.last.State != "ready" {
		t.Errorf("machine %s ended %q after a restart mid-create: %s", id, w.last.State, w.last.complaint())
	}

	// The assertion that matters, and it is the cluster's answer rather than
	// the fleet's: one machine, one guest.
	guests := f.guestsNamed(t, name)
	if len(guests) != 1 {
		t.Errorf("the cluster has %d guests named %q after a restart mid-create, want exactly 1: %v",
			len(guests), name, guests)
	}
	row.Observed += fmt.Sprintf("; the cluster holds %d guest named %q afterwards", len(guests), name)
	f.deleteMachine(t, id)
}

// bootstrapFailure gives a machine a guest it cannot bootstrap, and asks that
// the fleet say what went wrong rather than quietly build another.
//
// Two ways to induce it, and the evidence names which was used. A deliberately
// broken template is the procedure's preferred one, because it is the failure
// operators actually have; without one, the machines are given a controller
// address nothing answers on, which fails the same step for a reason the guest
// can also report.
func (f *fixture) bootstrapFailure(t *testing.T) {
	assertTimeToFinish(t, bootstrapFailureBudget, "the induced bootstrap failure")
	row := CaseRow{Number: 5, Case: "Bootstrap failure, a deliberately broken template"}
	defer func() { f.finish(t, &row) }()

	broken := map[string]string{}
	for k, v := range f.settings {
		broken[k] = v
	}
	method := ""
	if f.e.brokenTemplateID > 0 {
		broken["template_id"] = strconv.Itoa(f.e.brokenTemplateID)
		method = fmt.Sprintf("cloned the deliberately broken template %d", f.e.brokenTemplateID)
	} else {
		// TEST-NET-1, which is reserved for documentation and routes nowhere:
		// the guest comes up, the bootstrap runs, and the agent can never reach
		// a controller.
		broken["controller_url"] = "http://192.0.2.1:8080"
		method = "pointed the machines at a controller address nothing answers on (no broken template was supplied)"
	}
	f.api.patch("/providers/"+f.providerID, map[string]any{"settings": broken}, nil)
	defer f.api.patch("/providers/"+f.providerID, map[string]any{"settings": f.settings}, nil)
	row.Observed = method

	before := map[string]bool{}
	for _, m := range f.api.machines() {
		before[m.ID] = true
	}
	_ = dispatchWorkflow(t, f.e, f.pools[0].Label)

	var id string
	waitFor(t, waitFirstMachine, "a machine to be planned for the broken configuration", func() bool {
		for _, m := range f.api.machines() {
			if !before[m.ID] {
				id = m.ID
				return true
			}
		}
		return false
	})
	if err := f.led.created(KindMachine, id, f.api.machine(id).Name); err != nil {
		t.Fatalf("recording the machine in the ledger: %v", err)
	}

	w := newWatch()
	if !w.until(f.api, id, waitMachineCreated+waitMachineFailed, func(m machineView) bool {
		return m.State == "failed" || m.State == "ready"
	}) {
		t.Fatalf("machine %s neither failed nor became ready within %s; it is %q: %s",
			id, waitMachineCreated+waitMachineFailed, w.last.State, w.last.complaint())
	}
	if err := f.led.placed(id, w.last.ResourceZone, w.last.vmid()); err != nil {
		t.Fatalf("recording where the failed machine was built: %v", err)
	}
	if w.last.State == "ready" {
		t.Fatalf("machine %s enrolled anyway, so this case induced nothing: %s", id, method)
	}
	// The two error columns are two systems. A guest that could not be
	// bootstrapped is not a hypervisor complaint, and an operator reading one
	// has to know which half to go and look at.
	if w.last.BootstrapError == "" {
		t.Errorf("machine %s failed with no bootstrap_error (provider_error %q); "+
			"the guest's own complaint is what says what to fix", id, w.last.ProviderError)
	}
	row.Observed += "; failed with " + w.last.complaint()
	row.Human = "none, the machine failed with an explanation and its guest was destroyed"

	// It failed, and its guest exists. Nothing may leave it there.
	f.deleteMachine(t, id)
}

// deleteFailure makes one delete fail and asks that the fleet keep trying.
//
// Proxmox's own protection flag is the fault, because it is a refusal the
// hypervisor really does make, it is reversible, and it cannot be mistaken for
// the machine having been destroyed. A delete that failed must not be recorded
// as a delete that succeeded: that is how an orphan is made.
func (f *fixture) deleteFailure(t *testing.T) {
	assertTimeToFinish(t, deleteFailureBudget, "the induced delete failure")
	row := CaseRow{Number: 6, Case: "Deletion retry, a delete that fails once"}
	defer func() { f.finish(t, &row) }()

	before := map[string]bool{}
	for _, m := range f.api.machines() {
		before[m.ID] = true
	}
	_ = dispatchWorkflow(t, f.e, f.pools[0].Label)

	var id string
	waitFor(t, waitFirstMachine, "a machine to be planned", func() bool {
		for _, m := range f.api.machines() {
			if !before[m.ID] {
				id = m.ID
				return true
			}
		}
		return false
	})
	if err := f.led.created(KindMachine, id, f.api.machine(id).Name); err != nil {
		t.Fatalf("recording the machine in the ledger: %v", err)
	}
	w := newWatch()
	if !w.until(f.api, id, waitMachineCreated+waitMachineReady, func(m machineView) bool {
		return m.State == "ready" || m.State == "failed" || m.State == "quarantined"
	}) || w.last.State != "ready" {
		t.Fatalf("machine %s ended %q rather than ready: %s", id, w.last.State, w.last.complaint())
	}
	if err := f.led.placed(id, w.last.ResourceZone, w.last.vmid()); err != nil {
		t.Fatalf("recording where the machine was built: %v", err)
	}
	node, vmid := w.last.ResourceZone, w.last.vmid()

	ctx, cancel := context.WithTimeout(context.Background(), waitMachineDeleted)
	defer cancel()
	if err := f.cluster.setProtection(ctx, node, vmid, true); err != nil {
		t.Fatalf("could not protect VM %d to make its delete fail: %v", vmid, err)
	}
	// Clearing it is not optional: a protected guest nothing can destroy is the
	// one leftover this procedure could create and never clean up.
	defer func() {
		clear, cancel := context.WithTimeout(context.Background(), waitMachineDeleted)
		defer cancel()
		if err := f.cluster.setProtection(clear, node, vmid, false); err != nil {
			t.Errorf("could not clear the protection flag on VM %d; it must be cleared by hand "+
				"or nothing will ever destroy it: %v", vmid, err)
		}
	}()

	f.api.post("/machines/"+id+"/drain", map[string]any{}, nil)
	f.api.del("/machines/" + id)
	if !w.until(f.api, id, waitMachineDeleted, func(m machineView) bool {
		return m.Attempts > 0 && m.ProviderError != ""
	}) {
		t.Fatalf("the delete of protected VM %d was not recorded as having failed: state %q, attempts %d, %s",
			vmid, w.last.State, w.last.Attempts, w.last.complaint())
	}
	if w.last.State == "deleted" {
		t.Fatalf("machine %s was recorded as deleted while its guest is still on the cluster; "+
			"a 200 from a delete call is not evidence that a resource is gone", id)
	}
	row.Observed = fmt.Sprintf("VM %d refused to be destroyed while protected; the fleet recorded %d attempt(s) and %s",
		vmid, w.last.Attempts, w.last.complaint())

	// Clear the fault and let the retry through.
	clearCtx, clearCancel := context.WithTimeout(context.Background(), waitMachineDeleted)
	err := f.cluster.setProtection(clearCtx, node, vmid, false)
	clearCancel()
	if err != nil {
		t.Fatalf("could not clear the protection flag on VM %d: %v", vmid, err)
	}
	if !w.until(f.api, id, waitMachineDeleted, func(m machineView) bool { return m.State == "deleted" }) {
		t.Fatalf("machine %s did not recover once the fault was cleared; it is %q after %s: %s",
			id, w.last.State, waitMachineDeleted, w.last.complaint())
	}
	f.led.removed(KindMachine, id)
	row.Observed += "; it was destroyed on a later attempt once the flag was cleared"
	row.Human = "none, the retry succeeded without anybody intervening"
}

// reconcile is the case that decides qualification, and it is deliberately last.
//
// It asks the cluster, not the fleet: every VM in the range and every disk on
// the storage, accounted for against the ledger this run wrote before it created
// anything. No owned resource may be left unexplained.
func (f *fixture) reconcile(t *testing.T) {
	row := CaseRow{Number: 7, Case: "Closing inventory reconciliation"}
	defer func() { f.finish(t, &row) }()

	// Anything still alive is deleted first, or the reconciliation would be
	// measuring the harness's unfinished business rather than the product's.
	for _, m := range f.api.machines() {
		if m.State != "deleted" {
			f.deleteMachine(t, m.ID)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), waitReconciliation)
	defer cancel()
	inv, err := f.cluster.Take(ctx, f.e.node, f.e.storage)
	if err != nil {
		t.Fatalf("listing the cluster for the closing reconciliation: %v", err)
	}
	rec := Reconcile(inv, []LedgerRecord{f.led.record()}, f.e.vmidRange())
	f.rec.Reconciliation = &rec

	for _, fnd := range rec.Findings {
		if fnd.Verdict == Accounted {
			continue
		}
		t.Logf("reconciliation: %s VM %d (%s): %s", fnd.Verdict, fnd.VMID, fnd.Name, fnd.Detail)
	}
	if !rec.OK() {
		row.Human = fmt.Sprintf("%d owned resource(s) and %d disk(s) are still on the cluster; "+
			"delete them by hand and record why", rec.Unexplained, rec.DisksLeft)
		t.Fatalf("%d unexplained owned resource(s) and %d disk(s) left on %s. "+
			"An orphan found and deliberately left is a finding; an orphan nobody noticed is a failure of this procedure",
			rec.Unexplained, rec.DisksLeft, f.e.storage)
	}
	row.Observed = fmt.Sprintf("%d created, %d confirmed deleted, %d VM(s) left in %s, %d disk(s) left on %s",
		rec.Created, rec.Confirmed, rec.VMsAtEnd, rec.Range, rec.DisksLeft, f.e.storage)

	// And the other half of "gone": GitHub must not still be holding
	// registrations for machines that no longer exist.
	for _, p := range f.pools {
		left, err := githubRunnersWithLabel(f.e, p.Label)
		if err != nil {
			t.Errorf("checking GitHub for leftover registrations on %s: %v", p.Name, err)
			continue
		}
		if len(left) > 0 {
			t.Errorf("GitHub still holds %d registration(s) carrying %s after every machine was destroyed: %s",
				len(left), p.Label, strings.Join(left, ", "))
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers the cases share
// ---------------------------------------------------------------------------

// finish writes one case's row, whatever happened to it. A case that failed is
// the one worth reading, and the row is where a person is told what to do about
// it.
func (f *fixture) finish(t *testing.T, row *CaseRow) {
	row.Outcome = "passed"
	if t.Failed() {
		row.Outcome = "FAILED"
		if row.Human == "" {
			row.Human = "read this run's log, then run `go run ./test/e2e/proxmox/verify` " +
				"against the cluster: a failed case may have left a guest behind"
		}
	}
	if row.Human == "" {
		row.Human = "none"
	}
	f.rec.addCase(*row)
}

// deleteMachine takes one machine away and waits for the resource to be
// confirmed gone.
func (f *fixture) deleteMachine(t *testing.T, id string) {
	t.Helper()
	f.api.post("/machines/"+id+"/drain", map[string]any{}, nil)
	f.api.del("/machines/" + id)
	w := newWatch()
	if !w.until(f.api, id, waitMachineDrained+waitMachineDeleted, func(m machineView) bool {
		return m.State == "deleted"
	}) {
		t.Errorf("machine %s is %q rather than deleted after %s: %s",
			id, w.last.State, waitMachineDrained+waitMachineDeleted, w.last.complaint())
		return
	}
	f.led.removed(KindMachine, id)
}

// guestsNamed asks the cluster how many guests carry one name. It is how "one
// machine, one resource" is asserted: the fleet's own count of what it built is
// the thing being doubted.
func (f *fixture) guestsNamed(t *testing.T, name string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitReconciliation)
	defer cancel()
	inv, err := f.cluster.Take(ctx, f.e.node, "")
	if err != nil {
		t.Errorf("listing the cluster: %v", err)
		return nil
	}
	var found []string
	for _, g := range inv.Guests {
		if g.Name == name {
			found = append(found, g.String())
		}
	}
	return found
}

// tidyUp removes everything this run created, in the order that makes each step
// possible, and records whatever it could not.
//
// It runs however the test ends. Anything left in the ledger afterwards is on
// somebody's real cluster, so it goes into the evidence rather than being
// dropped.
func (f *fixture) tidyUp() {
	t := f.t
	deadline := time.Now().Add(waitCleanup)

	// Machines first, and through the product, so that a machine the fleet
	// still owns is destroyed by the code that owns it.
	for _, res := range f.led.record().Resources {
		if res.Kind != KindMachine || res.RemovedAt != nil || time.Now().After(deadline) {
			continue
		}
		if !f.api.try("POST", "/machines/"+res.ID+"/drain", map[string]any{}, nil) {
			continue
		}
		if !f.api.try("DELETE", "/machines/"+res.ID, nil, nil) {
			continue
		}
		w := newWatch()
		if w.until(f.api, res.ID, waitMachineDeleted, func(m machineView) bool { return m.State == "deleted" }) {
			f.led.removed(KindMachine, res.ID)
			continue
		}
		t.Errorf("cleanup: machine %s (VM %d on %s) is %q and was not destroyed; "+
			"it is still on the cluster", res.ID, res.VMID, res.Node, w.last.State)
	}

	for _, res := range f.led.record().Resources {
		if res.RemovedAt != nil {
			continue
		}
		switch res.Kind {
		case KindPool:
			if f.api.try("DELETE", "/pools/"+res.ID+"?force=true", nil, nil) {
				f.led.removed(KindPool, res.ID)
			}
		}
	}
	for _, res := range f.led.record().Resources {
		if res.RemovedAt != nil {
			continue
		}
		switch res.Kind {
		case KindProvider:
			if f.api.try("DELETE", "/providers/"+res.ID, nil, nil) {
				f.led.removed(KindProvider, res.ID)
			}
		case KindInstallation:
			if f.api.try("DELETE", "/installations/"+res.ID, nil, nil) {
				f.led.removed(KindInstallation, res.ID)
			}
		}
	}

	f.rec.Residual = f.led.outstanding()
	if len(f.rec.Residual) == 0 {
		if err := f.led.close(); err != nil {
			t.Errorf("closing the ledger: %v", err)
		}
		return
	}
	t.Errorf("this run left %d thing(s) behind, which are on a real cluster or organisation now: %s. "+
		"The ledger at %s names them, and `go run ./test/e2e/proxmox/verify` will find the guests",
		len(f.rec.Residual), strings.Join(f.rec.Residual, ", "), LedgerDir())
}

func mustInt(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		t.Fatalf("%q is not a number: %v", s, err)
	}
	return n
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(raw)
}
