package store

import (
	"slices"
	"time"
)

// ---------------------------------------------------------------------------
// Providers
// ---------------------------------------------------------------------------

// ProviderKind selects the infrastructure a provider rents machines from. It is
// the key the provider registry resolves a factory with, and it is CHECKed in
// the schema, so a kind this build does not know cannot be written and then
// read back as something to act on.
type ProviderKind string

const (
	ProviderProxmox ProviderKind = "proxmox"
	ProviderFake    ProviderKind = "fake"
)

func (k ProviderKind) Valid() bool {
	switch k {
	case ProviderProxmox, ProviderFake:
		return true
	}
	return false
}

// Provider is one place machines can be rented from: the credential, the one
// machine shape it offers, and the ceilings an operator put on it.
//
// The ceilings are the safe default rather than a convenience: MaxMachines is
// zero on a row nobody has configured, which refuses everything. A provider
// that bought machines the moment its credential was accepted would spend money
// on the strength of a connection test.
type Provider struct {
	ID   string       `json:"id"`
	Kind ProviderKind `json:"kind"`
	Name string       `json:"name"`
	// Endpoint, CAPEM and InsecureSkipVerify are how this controller reaches
	// the provider. InsecureSkipVerify is recorded rather than refused because
	// a homelab hypervisor's certificate is usually its own, but it weakens
	// the connection and the validator says so.
	Endpoint           string `json:"endpoint,omitempty"`
	CAPEM              string `json:"ca_pem,omitempty"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify,omitempty"`
	// Settings holds the non-secret answers to the provider's own SettingSpecs
	// -- which node, which storage, which template. It is a map rather than a
	// column per key so that a second provider needs no migration.
	Settings StringMap `json:"settings"`
	// CredentialsEnc is sealed with the instance key and never leaves this
	// process in plaintext. HasSealedSecrets counts it, so a controller that
	// has lost its key refuses to generate a new one rather than starting and
	// failing inside its first call to the hypervisor.
	CredentialsEnc []byte `json:"-"`
	// TailcatAddressEnc is the sealed address of a `zoomies gateway` in front
	// of the provider's API, for a hypervisor on a home network the controller
	// cannot otherwise reach. Empty means the endpoint is dialled directly.
	// Sealed for the same reason the credential is: the address is a lasting
	// capability to open connections to that API, not a name.
	TailcatAddressEnc []byte `json:"-"`

	// The one machine shape this provider offers. Every machine it buys is
	// this shape, and a second shape is a second provider row.
	MachineLabels   StringMap   `json:"machine_labels"`
	MachineCapacity int         `json:"machine_capacity"`
	MachineBackend  BackendKind `json:"machine_backend"`
	MachinePlatform Platform    `json:"machine_platform"`
	MachineCPUs     float64     `json:"machine_cpus,omitempty"`
	MachineMemoryMB int64       `json:"machine_memory_mb,omitempty"`
	MachineDiskMB   int64       `json:"machine_disk_mb,omitempty"`

	// PoolSelector narrows which pools this provider may buy for, the way a
	// pool's HostSelector narrows where its runners may be placed. Empty means
	// every pool, which is what one provider and one fleet want.
	PoolSelector StringMap `json:"pool_selector"`
	// MaxMachines is the ceiling on machines this provider may own at once;
	// MaxCreatesInFlight is the ceiling on how many it may be creating. The
	// second exists because a hypervisor cloning four templates at once is
	// slower at all four than it would have been at one.
	MaxMachines        int      `json:"max_machines"`
	MaxCreatesInFlight int      `json:"max_creates_in_flight"`
	IdleTimeout        Duration `json:"idle_timeout"`
	CostPerMachineHour float64  `json:"cost_per_machine_hour,omitempty"`
	Enabled            bool     `json:"enabled"`

	// Paused is the operator's kill switch and PausedUntil the breaker's. They
	// are separate columns because a breaker that expired must not un-press a
	// switch a person pressed.
	Paused              bool       `json:"paused"`
	PausedReason        string     `json:"paused_reason,omitempty"`
	PausedUntil         *time.Time `json:"paused_until,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures,omitempty"`
	LastCheckAt         *time.Time `json:"last_check_at,omitempty"`
	LastCheckError      string     `json:"last_check_error,omitempty"`
	// LastCheckReport is the whole of the last preflight, serialised by the
	// controller. LastCheckError is the sentence a card has room for; this is
	// what an operator reads to act on it.
	LastCheckReport string     `json:"-"`
	LastSweepAt     *time.Time `json:"last_sweep_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// Machines
// ---------------------------------------------------------------------------

// MachineState is the lifecycle of one rented machine, from a row that names
// nothing to a host that runs jobs.
type MachineState string

const (
	MachinePlanned       MachineState = "planned"       // the reservation exists; nothing has been created
	MachineCreating      MachineState = "creating"      // identity persisted; a create may or may not have happened
	MachineStarting      MachineState = "starting"      // the resource exists and is being powered on
	MachineBootstrapping MachineState = "bootstrapping" // the guest is up; the agent is being installed
	MachineEnrolling     MachineState = "enrolling"     // the agent has its credential and has not joined
	MachineReady         MachineState = "ready"         // a host row exists and is linked
	MachineDraining      MachineState = "draining"      // cordoned; waiting for its runners to finish
	MachineDeleting      MachineState = "deleting"      // a delete was issued and is not confirmed
	MachineDeleted       MachineState = "deleted"       // the provider confirmed the resource is gone
	MachineFailed        MachineState = "failed"        // we gave up on the lifecycle; the resource may still exist
	MachineQuarantined   MachineState = "quarantined"   // ownership could not be proved; only an operator moves it
)

func (s MachineState) Valid() bool {
	switch s {
	case MachinePlanned, MachineCreating, MachineStarting, MachineBootstrapping,
		MachineEnrolling, MachineReady, MachineDraining, MachineDeleting,
		MachineDeleted, MachineFailed, MachineQuarantined:
		return true
	}
	return false
}

// Terminal is deleted alone. Failed and quarantined still need attention, and a
// failed machine may still have a resource behind it.
func (s MachineState) Terminal() bool { return s == MachineDeleted }

// Pending is a machine the fleet is waiting on: it has no host yet, so its
// capacity is a reservation rather than a fact.
func (s MachineState) Pending() bool {
	switch s {
	case MachinePlanned, MachineCreating, MachineStarting, MachineBootstrapping, MachineEnrolling:
		return true
	}
	return false
}

// Offers is a machine whose capacity counts towards satisfying demand.
func (s MachineState) Offers() bool { return s.Pending() || s == MachineReady }

// MachineOpKind is the one kind of operation that may be in flight against a
// machine. The empty string is "none", and the schema CHECKs the set.
type MachineOpKind string

const (
	MachineOpNone      MachineOpKind = ""
	MachineOpCreate    MachineOpKind = "create"
	MachineOpStart     MachineOpKind = "start"
	MachineOpStop      MachineOpKind = "stop"
	MachineOpBootstrap MachineOpKind = "bootstrap"
	MachineOpDelete    MachineOpKind = "delete"
)

func (k MachineOpKind) Valid() bool {
	switch k {
	case MachineOpNone, MachineOpCreate, MachineOpStart, MachineOpStop, MachineOpBootstrap, MachineOpDelete:
		return true
	}
	return false
}

// MachineOperation is one pass's claim on a machine's next step.
//
// Timeout is required, and ClaimMachineOperation refuses a claim without one:
// a claim with no deadline is a claim a controller that dies mid-operation
// holds for ever, and the machine behind it would never be looked at again.
type MachineOperation struct {
	// ID is minted per attempt when it is empty, and is what a log line and an
	// operator quote. Holder is the controller taking the step, so a machine
	// stuck mid-create names the controller to go and look at.
	ID      string
	Kind    MachineOpKind
	Holder  string
	Timeout time.Duration
}

// MachineOpOutcome is how an operation ended, which decides what happens to the
// retry accounting rather than to the state -- the state is TransitionMachine's.
type MachineOpOutcome string

const (
	// MachineOpSucceeded clears the provider's complaint and the backoff.
	MachineOpSucceeded MachineOpOutcome = "succeeded"
	// MachineOpFailed counts an attempt and sets the next one.
	MachineOpFailed MachineOpOutcome = "failed"
	// MachineOpReleased gives the claim back without a verdict: a guard that
	// refused after the claim was taken, or a controller shutting down. It
	// must not count an attempt, because nothing about the machine went wrong
	// and counting it would walk the backoff up towards giving up.
	MachineOpReleased MachineOpOutcome = "released"
)

func (o MachineOpOutcome) Valid() bool {
	switch o {
	case MachineOpSucceeded, MachineOpFailed, MachineOpReleased:
		return true
	}
	return false
}

// MachineErrorSource says which of the two independent systems complained. They
// have a column each for the reason 0022 gives about runner cleanup: a success
// on one must not erase the other's complaint, and an operator reading one
// error has to know which half to go and look at.
type MachineErrorSource string

const (
	// MachineErrorProvider is the hypervisor or cloud API.
	MachineErrorProvider MachineErrorSource = "provider"
	// MachineErrorBootstrap is everything inside the guest: the agent install,
	// the enrolment, the handshake back.
	MachineErrorBootstrap MachineErrorSource = "bootstrap"
)

func (s MachineErrorSource) Valid() bool {
	return s == MachineErrorProvider || s == MachineErrorBootstrap
}

// Machine is one rented machine: the row that exists before the resource does,
// and outlives it by exactly as long as it takes to prove the resource is gone.
type Machine struct {
	ID         string       `json:"id"`
	ProviderID string       `json:"provider_id"`
	Name       string       `json:"name"`
	State      MachineState `json:"state"`
	Message    string       `json:"message,omitempty"`
	// PoolID records which pool's unmet demand asked for this machine. It is
	// not ownership -- a machine serves every pool its labels match -- and it
	// exists so the machine page can answer "why does this exist".
	PoolID string `json:"pool_id,omitempty"`

	// OwnerControllerID and OwnerFingerprint are written before the first call
	// that can create anything. The fingerprint is tamper-evidence rather than
	// a credential: anybody with audit rights on the hypervisor can read it,
	// and its job is to tell "the VM I created at 143" apart from "a VM
	// somebody else created at 143 after mine was destroyed".
	OwnerControllerID string `json:"owner_controller_id,omitempty"`
	OwnerFingerprint  string `json:"owner_fingerprint,omitempty"`
	// ResourceZone and ResourceID are the provider's own identity for this
	// machine -- a node and a VM id. Written once, never moved.
	ResourceZone   string    `json:"resource_zone,omitempty"`
	ResourceID     string    `json:"resource_id,omitempty"`
	ResourceDetail StringMap `json:"resource_detail,omitempty"`
	Address        string    `json:"address,omitempty"`
	// OwnershipVerifiedAt is the last time a fresh observation agreed that this
	// resource is ours. OwnershipError is what disagreed. A delete needs the
	// first and the absence of the second.
	OwnershipVerifiedAt *time.Time `json:"ownership_verified_at,omitempty"`
	OwnershipError      string     `json:"ownership_error,omitempty"`

	// HostID is the enrolment link, and the only thing that grants this
	// controller deletion authority over a host. It is written by
	// LinkMachineHost after a token this row minted was redeemed, and by
	// nothing else -- there is no route that attaches a machine to a host an
	// operator already had.
	HostID      string    `json:"host_id,omitempty"`
	JoinTokenID string    `json:"join_token_id,omitempty"`
	Capacity    int       `json:"capacity,omitempty"`
	Labels      StringMap `json:"labels,omitempty"`

	// The operation in flight, if any. OpHandle is the provider's own handle
	// for the call -- a task id -- and is what recovery asks about after a
	// restart. OpOutcomeUnknown marks a call that went out and was never
	// answered, which is the one thing a retry must not treat as "it failed".
	OpID             string        `json:"op_id,omitempty"`
	OpKind           MachineOpKind `json:"op_kind,omitempty"`
	OpHandle         string        `json:"op_handle,omitempty"`
	OpHolder         string        `json:"op_holder,omitempty"`
	OpStartedAt      *time.Time    `json:"op_started_at,omitempty"`
	OpDeadlineAt     *time.Time    `json:"op_deadline_at,omitempty"`
	OpOutcomeUnknown bool          `json:"op_outcome_unknown,omitempty"`

	Attempts       int        `json:"attempts,omitempty"`
	NextAttemptAt  *time.Time `json:"next_attempt_at,omitempty"`
	ProviderError  string     `json:"provider_error,omitempty"`
	BootstrapError string     `json:"bootstrap_error,omitempty"`

	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	ReservationExpiresAt *time.Time `json:"reservation_expires_at,omitempty"`
	CreateStartedAt      *time.Time `json:"create_started_at,omitempty"`
	CreatedOKAt          *time.Time `json:"created_ok_at,omitempty"`
	StartedAt            *time.Time `json:"started_at,omitempty"`
	BootstrappedAt       *time.Time `json:"bootstrapped_at,omitempty"`
	EnrolledAt           *time.Time `json:"enrolled_at,omitempty"`
	ReadyAt              *time.Time `json:"ready_at,omitempty"`
	IdleSince            *time.Time `json:"idle_since,omitempty"`
	DrainingAt           *time.Time `json:"draining_at,omitempty"`
	DeleteStartedAt      *time.Time `json:"delete_started_at,omitempty"`
	// DeletedAt is when the provider confirmed the resource was gone, by an
	// inspect that could not find it. A 200 from the delete call is not that,
	// which is why only ConfirmMachineDeleted writes this.
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

// Owns reports whether this row believes a resource exists in the provider. It
// is deliberately NOT a state test: a failed machine whose VM was created still
// costs money and still counts against max_machines, and a planned one costs
// nothing. The SQL half of this predicate lives in CountOwnedMachines, and a
// test holds the two definitions equal -- live_rows_test.go's reason.
func (m *Machine) Owns() bool { return m.ResourceID != "" && m.DeletedAt == nil }

// validMachineTransitions is the allow-list Store.TransitionMachine enforces. It
// is here rather than in the reconciler for the same reason the runner one is: a
// pass that has just lost a race, or a provider that answered oddly, must not be
// able to talk a machine into a state that makes the fleet's accounting wrong.
//
// The absences are the design:
//   - nothing returns to planned: you never go back to "nothing exists";
//   - deleting leads only to deleted or quarantined, because a delete that was
//     issued cannot be un-issued; if we cannot tell, an operator is asked;
//   - quarantined never returns to ready: a machine whose ownership we could not
//     prove does not silently rejoin the fleet;
//   - failed may still reach deleting, because a later sweep can find a resource
//     we believed never existed;
//   - draining returns to ready, so demand coming back cancels a drain rather
//     than paying for a new machine.
var validMachineTransitions = map[MachineState][]MachineState{
	MachinePlanned:       {MachineCreating, MachineFailed, MachineDeleted, MachineQuarantined},
	MachineCreating:      {MachineStarting, MachineBootstrapping, MachineDeleting, MachineFailed, MachineQuarantined},
	MachineStarting:      {MachineBootstrapping, MachineDeleting, MachineFailed, MachineQuarantined},
	MachineBootstrapping: {MachineEnrolling, MachineDeleting, MachineFailed, MachineQuarantined},
	MachineEnrolling:     {MachineReady, MachineDeleting, MachineFailed, MachineQuarantined},
	MachineReady:         {MachineDraining, MachineDeleting, MachineFailed, MachineQuarantined},
	MachineDraining:      {MachineReady, MachineDeleting, MachineFailed, MachineQuarantined},
	MachineDeleting:      {MachineDeleted, MachineQuarantined},
	MachineDeleted:       {},
	MachineFailed:        {MachineDeleting, MachineDeleted, MachineQuarantined},
	MachineQuarantined:   {MachineDeleting, MachineDeleted},
}

// CanTransitionMachine reports whether from -> to is a legal machine state
// change. A self-transition is always legal, because a pass re-reporting the
// state a machine is already in is not an error and must not become one.
func CanTransitionMachine(from, to MachineState) bool {
	if from == to {
		return true
	}
	return slices.Contains(validMachineTransitions[from], to)
}
