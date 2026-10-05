package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// The sizing tools: the handful of pool and host settings that decide how many
// runners a fleet holds and how large each one is. They are the changes an
// operator is told to make by a problem's fix or by `zoomies doctor`, and they
// are the ones an agent that can read the fleet can usefully make.
//
// They are deliberately not a general "edit a pool". Each takes named fields and
// nothing else, and every other setting of the pool or host is carried forward
// untouched, because the API replaces `resources`, `cpu_burst` and a host's
// `runner_profile` whole and a tool that sent only what it was asked to change
// would clear the rest. They merge into the raw JSON the API returned rather
// than into a copy of its types, so a field added to the API tomorrow is kept
// by a tool that has never heard of it.
//
// The controller decides every call, as it does for the CLI: it refuses a
// change that would leave a pool with no host that could run it, and these tools
// never pass confirm=true to override that, so an agent cannot talk its way past
// the one guard that tells an operator they are about to switch a pool off.

func configTools() []*tool {
	return []*tool{
		{
			Name:  "update_pool",
			Title: "Change a pool's sizing",
			Description: "Change a pool's smallest runner, its Docker sidecar's share of a slot, or its CPU burst ceiling, leaving every " +
				"other setting as it is. It applies to runners created after the change; running ones finish as they are. The controller " +
				"refuses a change that would leave the pool with no host that could run it. Read the pool first (list_pools) and make one " +
				"change at a time: the smallest runner is per container, so a Docker-in-Docker pool needs it twice over, scaled by the " +
				"sidecar's share, and lowering a share raises what each slot is charged. Zero for a minimum means 'follow the fleet's'.",
			InputSchema: object([]string{"pool_id"}, map[string]any{
				"pool_id":                     str("the pool's ID, starting pool_"),
				"min_cpus":                    map[string]any{"type": "number", "minimum": 0, "description": "the least CPU a runner of this pool is given, per container; 0 follows the fleet's, otherwise at least 0.25"},
				"min_memory_mb":               map[string]any{"type": "integer", "minimum": 0, "description": "the least memory a runner is given, per container, in MiB; 0 follows the fleet's, otherwise at least 512"},
				"daemon_cpu_share_percent":    integer("the Docker sidecar's share of a slot's CPU, 10 to 90", 10, 90),
				"daemon_memory_share_percent": integer("the Docker sidecar's share of a slot's memory, 10 to 90", 10, 90),
				"cpu_burst_max_cpus":          map[string]any{"type": "number", "minimum": 0, "description": "the most CPU one runner may be lent up to, in cores; 0 is the host's whole allocatable CPU"},
			}),
			Annotations: annotations{Idempotent: true},
			action:      true,
			call:        updatePool,
		},
		{
			Name:  "update_host",
			Title: "Change a host's capacity and runner sizes",
			Description: "Change a host's capacity, what it keeps back for itself, or the runner sizes it gives pools that take their size " +
				"from it, leaving every other setting of the host as it is. It applies to runners created after the change. Capacity is " +
				"never set to zero here: stopping a host taking runners is a decision for a person at the UI or CLI (cordon it). Read the " +
				"host first (list_hosts); a host's slots are limited by whichever of its CPU, memory or capacity runs out first, and " +
				"lowering a standard size adds slots only up to the capacity. Zero for a size means 'follow the fleet's'.",
			InputSchema: object([]string{"host_id"}, map[string]any{
				"host_id":            str("the host's ID, starting host_"),
				"capacity":           integer("the most runners the host takes; with a standard runner size, the ceiling on the slots its machine gives", 1, 1024),
				"reserve_cpus":       integer("whole CPUs held back from placement for the machine's own sake", 0, 1024),
				"reserve_memory_mb":  integer("memory held back from placement, in MiB", 0, 16<<20),
				"standard_cpus":      map[string]any{"type": "number", "minimum": 0, "description": "the CPU one runner is given here for a pool that takes its size from the host; 0 follows the fleet's"},
				"standard_memory_mb": map[string]any{"type": "integer", "minimum": 0, "description": "the memory one runner is given here, in MiB; 0 follows the fleet's"},
				"min_cpus":           map[string]any{"type": "number", "minimum": 0, "description": "the least CPU a runner on this host is given; 0 follows the fleet's"},
				"min_memory_mb":      map[string]any{"type": "integer", "minimum": 0, "description": "the least memory a runner on this host is given, in MiB; 0 follows the fleet's"},
				"burst_max_cpus":     map[string]any{"type": "number", "minimum": 0, "description": "the most CPU one runner here may use, lent CPU included; 0 sets no host ceiling"},
			}),
			Annotations: annotations{Idempotent: true},
			action:      true,
			call:        updateHost,
		},
	}
}

// change is one field's before and after, as the tool reports it. Before is
// absent for a setting that followed the fleet's.
type change struct {
	Before any `json:"before,omitempty"`
	After  any `json:"after,omitempty"`
}

// sizingReply is what an update tool answers: what changed, not the whole
// object, which the agent can read again if it wants it.
type sizingReply struct {
	ID       string            `json:"id"`
	Name     string            `json:"name,omitempty"`
	Changes  map[string]change `json:"changes"`
	Warnings json.RawMessage   `json:"warnings,omitempty"`
	Note     string            `json:"note"`
}

func updatePool(ctx context.Context, c API, raw json.RawMessage) ([]Content, error) {
	var a struct {
		PoolID            string   `json:"pool_id"`
		MinCPUs           *float64 `json:"min_cpus"`
		MinMemoryMB       *int64   `json:"min_memory_mb"`
		DaemonCPUShare    *int     `json:"daemon_cpu_share_percent"`
		DaemonMemoryShare *int     `json:"daemon_memory_share_percent"`
		CPUBurstMax       *float64 `json:"cpu_burst_max_cpus"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if err := requireID("pool_id", a.PoolID); err != nil {
		return nil, err
	}
	if a.MinCPUs == nil && a.MinMemoryMB == nil && a.DaemonCPUShare == nil && a.DaemonMemoryShare == nil && a.CPUBurstMax == nil {
		return nil, fmt.Errorf("name at least one setting to change")
	}
	if err := atLeastOrZero("min_cpus", a.MinCPUs, 0.25); err != nil {
		return nil, err
	}
	if a.MinMemoryMB != nil {
		if err := atLeastOrZero("min_memory_mb", ptr(float64(*a.MinMemoryMB)), 512); err != nil {
			return nil, err
		}
	}
	if a.CPUBurstMax != nil && *a.CPUBurstMax < 0 {
		return nil, fmt.Errorf("cpu_burst_max_cpus cannot be negative")
	}
	bc, ok := c.(BodyCaller)
	if !ok {
		return nil, fmt.Errorf("this transport cannot change a pool")
	}

	path := "/pools/" + url.PathEscape(a.PoolID)
	current, err := readObject(ctx, c, path, "pool", a.PoolID, "list_pools")
	if err != nil {
		return nil, err
	}
	resources := copyMap(current["resources"])
	changes := map[string]change{}
	body := map[string]any{}

	set := func(key string, v float64, field string) {
		before := resources[key]
		if v == 0 {
			delete(resources, key)
		} else {
			resources[key] = v
		}
		changes[field] = change{Before: before, After: orFleet(v)}
	}
	if a.MinCPUs != nil {
		set("min_cpus", *a.MinCPUs, "min_cpus")
	}
	if a.MinMemoryMB != nil {
		set("min_memory_mb", float64(*a.MinMemoryMB), "min_memory_mb")
	}
	if a.DaemonCPUShare != nil {
		set("daemon_cpu_share_percent", float64(*a.DaemonCPUShare), "daemon_cpu_share_percent")
	}
	if a.DaemonMemoryShare != nil {
		set("daemon_memory_share_percent", float64(*a.DaemonMemoryShare), "daemon_memory_share_percent")
	}
	if a.MinCPUs != nil || a.MinMemoryMB != nil || a.DaemonCPUShare != nil || a.DaemonMemoryShare != nil {
		body["resources"] = resources
	}
	if a.CPUBurstMax != nil {
		burst := copyMap(current["cpu_burst"])
		changes["cpu_burst_max_cpus"] = change{Before: burst["max_cpus"], After: orFleet(*a.CPUBurstMax)}
		if *a.CPUBurstMax == 0 {
			delete(burst, "max_cpus")
		} else {
			burst["max_cpus"] = *a.CPUBurstMax
		}
		body["cpu_burst"] = burst
	}
	return send(ctx, bc, path, body, sizingReply{
		ID:      a.PoolID,
		Name:    stringOf(current["name"]),
		Changes: changes,
		Note:    "Applies to runners created from now on; those already running finish as they are. Read the pool again to see what it is charged per slot.",
	})
}

func updateHost(ctx context.Context, c API, raw json.RawMessage) ([]Content, error) {
	var a struct {
		HostID           string   `json:"host_id"`
		Capacity         *int     `json:"capacity"`
		ReserveCPUs      *int     `json:"reserve_cpus"`
		ReserveMemoryMB  *int64   `json:"reserve_memory_mb"`
		StandardCPUs     *float64 `json:"standard_cpus"`
		StandardMemoryMB *int64   `json:"standard_memory_mb"`
		MinCPUs          *float64 `json:"min_cpus"`
		MinMemoryMB      *int64   `json:"min_memory_mb"`
		BurstMaxCPUs     *float64 `json:"burst_max_cpus"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if err := requireID("host_id", a.HostID); err != nil {
		return nil, err
	}
	if a.Capacity == nil && a.ReserveCPUs == nil && a.ReserveMemoryMB == nil && a.StandardCPUs == nil &&
		a.StandardMemoryMB == nil && a.MinCPUs == nil && a.MinMemoryMB == nil && a.BurstMaxCPUs == nil {
		return nil, fmt.Errorf("name at least one setting to change")
	}
	if a.Capacity != nil && *a.Capacity < 1 {
		return nil, fmt.Errorf("capacity must be at least 1: stopping a host taking runners is a decision for a person, who cordons it at the UI or CLI")
	}
	if err := atLeastOrZero("min_cpus", a.MinCPUs, 0.25); err != nil {
		return nil, err
	}
	if a.MinMemoryMB != nil {
		if err := atLeastOrZero("min_memory_mb", ptr(float64(*a.MinMemoryMB)), 512); err != nil {
			return nil, err
		}
	}
	bc, ok := c.(BodyCaller)
	if !ok {
		return nil, fmt.Errorf("this transport cannot change a host")
	}

	path := "/hosts/" + url.PathEscape(a.HostID)
	current, err := readObject(ctx, c, path, "host", a.HostID, "list_hosts")
	if err != nil {
		return nil, err
	}
	changes := map[string]change{}
	body := map[string]any{}

	if a.Capacity != nil {
		changes["capacity"] = change{Before: current["capacity"], After: *a.Capacity}
		body["capacity"] = *a.Capacity
	}
	if a.ReserveCPUs != nil {
		changes["reserve_cpus"] = change{Before: current["reserve_cpus"], After: *a.ReserveCPUs}
		body["reserve_cpus"] = *a.ReserveCPUs
	}
	if a.ReserveMemoryMB != nil {
		changes["reserve_memory_mb"] = change{Before: current["reserve_memory_mb"], After: *a.ReserveMemoryMB}
		body["reserve_memory_mb"] = *a.ReserveMemoryMB
	}

	// The profile is replaced whole, so it is read as it stands and only the
	// fields named are touched. Its tmpfs part is carried forward as it came.
	profile := copyMap(current["runner_profile"])
	minimum, standard := copyMap(profile["minimum"]), copyMap(profile["standard"])
	touched := false
	put := func(into map[string]any, key string, v float64, field string) {
		touched = true
		changes[field] = change{Before: into[key], After: orFleet(v)}
		if v == 0 {
			delete(into, key)
		} else {
			into[key] = v
		}
	}
	if a.MinCPUs != nil {
		put(minimum, "cpus", *a.MinCPUs, "min_cpus")
	}
	if a.MinMemoryMB != nil {
		put(minimum, "memory_mb", float64(*a.MinMemoryMB), "min_memory_mb")
	}
	if a.StandardCPUs != nil {
		put(standard, "cpus", *a.StandardCPUs, "standard_cpus")
	}
	if a.StandardMemoryMB != nil {
		put(standard, "memory_mb", float64(*a.StandardMemoryMB), "standard_memory_mb")
	}
	if a.BurstMaxCPUs != nil {
		put(standard, "burst_max_cpus", *a.BurstMaxCPUs, "burst_max_cpus")
	}
	if touched {
		profile["minimum"], profile["standard"] = minimum, standard
		body["runner_profile"] = profile
	}
	return send(ctx, bc, path, body, sizingReply{
		ID:      a.HostID,
		Name:    stringOf(current["name"]),
		Changes: changes,
		Note:    "Applies to runners created from now on. Read the host again (list_hosts) to see the slots it now gives and what limits them.",
	})
}

// send makes the update and answers with what changed, plus any warnings the
// controller attached, which are the part of its answer an agent must not miss.
func send(ctx context.Context, bc BodyCaller, path string, body map[string]any, reply sizingReply) ([]Content, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	// No confirm=true, ever: the controller's refusal of a change that leaves a
	// pool with nowhere to run is the guard, and it is not the tool's to lift.
	answer, err := bc.CallBody(ctx, http.MethodPatch, path, nil, payload)
	if err != nil {
		return nil, err
	}
	var sent struct {
		Warnings json.RawMessage `json:"warnings"`
	}
	if json.Unmarshal(answer, &sent) == nil && len(sent.Warnings) > 0 && string(sent.Warnings) != "null" && string(sent.Warnings) != "[]" {
		reply.Warnings = sent.Warnings
	}
	out, err := json.Marshal(reply)
	if err != nil {
		return nil, err
	}
	return jsonContent(out), nil
}

// readObject reads the object as it stands, which an update has to keep.
func readObject(ctx context.Context, c API, path, kind, id, lister string) (map[string]any, error) {
	body, err := c.Call(ctx, http.MethodGet, path, nil)
	if err != nil {
		if notFound(err) {
			return nil, fmt.Errorf("there is no %s %q; %s lists them", kind, id, lister)
		}
		return nil, fmt.Errorf("reading the %s as it stands, which an update has to keep: %w", kind, err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("the %s came back unreadable: %w", kind, err)
	}
	return out, nil
}

func copyMap(v any) map[string]any {
	in, _ := v.(map[string]any)
	out := make(map[string]any, len(in))
	for k, x := range in {
		out[k] = x
	}
	return out
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

func ptr[T any](v T) *T { return &v }

// orFleet is a setting's value for a report: zero is "follows the fleet's", and
// saying a bare 0 would read as a limit of nothing.
func orFleet(v float64) any {
	if v == 0 {
		return "follows the fleet's"
	}
	return v
}

// atLeastOrZero refuses a minimum between zero, which follows the fleet's, and
// the least the controller will store, so the refusal says what to type.
func atLeastOrZero(name string, v *float64, least float64) error {
	if v == nil || *v == 0 || *v >= least {
		return nil
	}
	return fmt.Errorf("%s is %g: use 0 to follow the fleet's, or at least %g", name, *v, least)
}
