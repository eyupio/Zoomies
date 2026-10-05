package main

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// profileFlags are the flags that edit a host's runner profile, in the order
// the help lists them.
var profileFlags = []string{"min-cpus", "min-memory-mb", "standard-cpus", "standard-memory-mb", "burst-max-cpus", "burst-max-memory-mb", "tmpfs-off", "tmpfs-max-mb", "tmpfs-work-mb", "tmpfs-tmp-mb", "tmpfs-docker-mb"}

// hostsEdit is `zoomies hosts edit`: the host's capacity, its reserve and how
// big a runner is on it, which is the host's half of what the API's PATCH takes.
//
// The API replaces a runner profile whole, so an edit that names one of its
// figures has to carry the others forward: `--standard-cpus 3` must not clear
// the memory the host already had. The host is therefore read first, but only
// when a profile flag is named, so changing a capacity costs no extra call.
// That is the same shape as `pools edit` and for the same reason -- a partial
// edit that silently resets what it did not mention is a fleet-wide change
// nobody asked for.
func hostsEdit(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies hosts edit <host-id> [flags]",
		"Change a host's capacity, reserve or runner profile. Anything you do not name is left alone.")
	cf := registerClientFlags(fs, true)
	capacity := fs.Int("capacity", 0, "the most runners the host takes; with a standard runner size, the ceiling on the slots its machine gives (0 stops it taking new runners)")
	reserveCPUs := fs.Int("reserve-cpus", 0, "whole CPUs held back from placement for the machine's own sake")
	reserveMemory := fs.Int64("reserve-memory-mb", 0, "memory held back from placement, in MB")
	reserveDisk := fs.Int64("reserve-disk-mb", 0, "disk held back from placement, in MB")
	minCPUs := fs.Float64("min-cpus", 0, "the least CPU a runner on this host is given; 0 follows the fleet's runners.minimum_cpus")
	minMemory := fs.Int64("min-memory-mb", 0, "the least memory a runner on this host is given, in MB; 0 follows runners.minimum_memory_mb")
	standardCPUs := fs.Float64("standard-cpus", 0, "the CPU one runner is given here for a pool that takes its size from the host; 0 follows runners.default_cpus")
	standardMemory := fs.Int64("standard-memory-mb", 0, "the memory one runner is given here, in MB; 0 follows runners.default_memory_mb")
	burstMax := fs.Float64("burst-max-cpus", 0, "the most CPU one runner here may use, lent CPU included; 0 sets no host ceiling")
	burstMaxMemory := fs.Int64("burst-max-memory-mb", 0, "the most memory one runner here may hold, in MB, lent memory included; 0 sets no host ceiling, which leaves a pool's own or half as much again as a runner starts with")
	tmpfsOff := fs.Bool("tmpfs-off", false, "fall back to disk on this host: keep pools' in-memory folders off it, whatever a pool asks for. A tactical fix for a machine that cannot spare the memory today, and the pool is told for as long as it is on; --tmpfs-off=false lets them back")
	tmpfsWork := fs.Int64("tmpfs-work-mb", 0, "the size the _work folder is asked for on this host when a pool leaves it to size itself, in MB (at least 64); 0 is the default of 4096")
	tmpfsTmp := fs.Int64("tmpfs-tmp-mb", 0, "the same for /tmp, in MB (at least 64); 0 is the default of 1024")
	tmpfsDocker := fs.Int64("tmpfs-docker-mb", 0, "the same for the Docker-in-Docker image store, in MB (at least 64); 0 is the default of 8192")
	tmpfsMax := fs.Int64("tmpfs-max-mb", 0, "the most any one in-memory folder may be on this host, in MB (at least 64); 0 sets no host ceiling")
	clearProfile := fs.Bool("clear-profile", false, "remove the host's runner profile, so it follows the fleet's settings again")
	tags, untags := tagValue{}, &listValue{}
	fs.Var(tags, "tag", "put a tag on the host: key=value, or a bare key for key=true (repeatable); a pool's host selector asks for tags")
	fs.Var(untags, "untag", "take a tag off the host (repeatable)")
	confirm := fs.Bool("confirm", false, "save even if it leaves a pool with nowhere to run")
	fs.example(
		"zoomies hosts edit hst_k3f9qz2m --standard-cpus 3 --standard-memory-mb 8192",
		"zoomies hosts edit hst_k3f9qz2m --min-cpus 1 --standard-cpus 2 --burst-max-cpus 4",
		"zoomies hosts edit hst_k3f9qz2m --burst-max-memory-mb 16384",
		"zoomies hosts edit hst_k3f9qz2m --capacity 6 --reserve-memory-mb 4096",
		"zoomies hosts edit hst_k3f9qz2m --tmpfs-max-mb 2048",
		"zoomies hosts edit hst_k3f9qz2m --tmpfs-off",
		"zoomies hosts edit hst_k3f9qz2m --clear-profile",
		"zoomies hosts edit hst_k3f9qz2m --tag rack=b4 --tag gpu",
		"zoomies hosts edit hst_k3f9qz2m --tag size=large --untag rack",
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	id, err := fs.oneArg("a host ID, as shown by `zoomies hosts list`")
	if err != nil {
		return err
	}
	for _, key := range *untags {
		if strings.Contains(key, "=") {
			return usagef("hosts edit", "--untag takes a tag's name, written without a value: --untag %s", strings.TrimSpace(strings.SplitN(key, "=", 2)[0]))
		}
		if _, both := tags[key]; both {
			return usagef("hosts edit", "--tag and --untag both name %s; put it on or take it off, not both", key)
		}
	}
	touchesProfile := slices.ContainsFunc(profileFlags, fs.changed)
	if *clearProfile && touchesProfile {
		return usagef("hosts edit", "--clear-profile removes the whole runner profile, so it cannot be combined with --%s",
			profileFlags[slices.IndexFunc(profileFlags, fs.changed)])
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}

	body := map[string]any{}
	if fs.changed("capacity") {
		body["capacity"] = *capacity
	}
	if fs.changed("reserve-cpus") {
		body["reserve_cpus"] = *reserveCPUs
	}
	if fs.changed("reserve-memory-mb") {
		body["reserve_memory_mb"] = *reserveMemory
	}
	if fs.changed("reserve-disk-mb") {
		body["reserve_disk_mb"] = *reserveDisk
	}
	switch {
	case *clearProfile:
		body["runner_profile"] = map[string]any{}
	case touchesProfile:
		var current hostItem
		if _, err := client.get(ctx, "/hosts/"+url.PathEscape(id), nil, &current); err != nil {
			return fmt.Errorf("reading the host as it stands, which an edit to part of its runner profile has to keep: %w", err)
		}
		profile := runnerProfile{}
		if current.RunnerProfile != nil {
			profile = *current.RunnerProfile
		}
		if fs.changed("min-cpus") {
			profile.Minimum.CPUs = *minCPUs
		}
		if fs.changed("min-memory-mb") {
			profile.Minimum.MemoryMB = *minMemory
		}
		if fs.changed("standard-cpus") {
			profile.Standard.CPUs = *standardCPUs
		}
		if fs.changed("standard-memory-mb") {
			profile.Standard.MemoryMB = *standardMemory
		}
		if fs.changed("burst-max-cpus") {
			profile.Standard.BurstMaxCPUs = *burstMax
		}
		if fs.changed("burst-max-memory-mb") {
			profile.Standard.BurstMaxMemoryMB = *burstMaxMemory
		}
		if fs.changed("tmpfs-off") {
			profile.Tmpfs.Disabled = *tmpfsOff
		}
		if fs.changed("tmpfs-max-mb") {
			profile.Tmpfs.MaxMB = *tmpfsMax
		}
		if fs.changed("tmpfs-work-mb") {
			profile.Tmpfs.WorkMB = *tmpfsWork
		}
		if fs.changed("tmpfs-tmp-mb") {
			profile.Tmpfs.TmpMB = *tmpfsTmp
		}
		if fs.changed("tmpfs-docker-mb") {
			profile.Tmpfs.DaemonMB = *tmpfsDocker
		}
		body["runner_profile"] = profile
	}
	if len(tags) > 0 || len(*untags) > 0 {
		// The API replaces a host's labels whole, so a tag put on or taken off
		// has to carry the others forward, which means reading the host as it
		// stands. Only the stored labels are carried: the automatic tags are the
		// machine's and are never written back.
		var current hostItem
		if _, err := client.get(ctx, "/hosts/"+url.PathEscape(id), nil, &current); err != nil {
			return fmt.Errorf("reading the host as it stands, which an edit to its tags has to keep: %w", err)
		}
		labels := map[string]string{}
		for k, v := range current.Labels {
			labels[k] = v
		}
		for _, key := range *untags {
			if _, stored := labels[key]; !stored {
				// Said, because the request would otherwise succeed and change
				// nothing, which reads as a tag taken off that is still there.
				why := "it has no tag of that name"
				for _, t := range current.Tags {
					if t.Key == key && t.Source != "operator" {
						why = "that one is worked out from the machine and is not stored, so there is nothing to take off"
					}
				}
				return usagef("hosts edit", "cannot take off %s: %s", key, why)
			}
			delete(labels, key)
		}
		for k, v := range tags {
			labels[k] = v
		}
		body["labels"] = labels
	}
	if len(body) == 0 {
		return usagef("hosts edit", "nothing to change; name at least one setting, for example --standard-cpus 3")
	}

	q := url.Values{}
	if *confirm {
		q.Set("confirm", "true")
	}
	var host hostItem
	raw, err := client.patch(ctx, "/hosts/"+url.PathEscape(id), q, body, &host)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	p.note("Updated host %s (%s).", host.Name, host.ID)
	describeRunnerSizes(p, host)
	if tags := describeTags(host); tags != "" {
		p.note("  %s: tags %s", host.Name, tags)
	}
	return nil
}

// describeRunnerSizes says what a host's runners are now: the standard size and
// whose it is, and the slots that gives -- with what sets them where the
// operator's capacity is not what does. It is printed after an edit and under
// the host list, because "capacity 8" next to a standard that holds three is
// the sentence an operator would otherwise have to work out.
func describeRunnerSizes(p *printer, h hostItem) {
	if h.RunnerProfile == nil {
		return
	}
	std := h.EffectiveProfile.Standard
	if std.CPUs > 0 || std.MemoryMB > 0 {
		p.note("  %s: runners of %s CPU and %s (%s), %s", h.Name, strconv.FormatFloat(std.CPUs, 'f', -1, 64),
			formatMemoryMB(std.MemoryMB), sourcePhrase(std.CPUsSource, std.MemoryMBSource), plural(h.Slots, "slot"))
	}
	switch h.SlotsLimitedBy {
	case "cpu":
		p.note("  %s: its CPUs set the slots; a lower standard CPU gives more", h.Name)
	case "memory":
		p.note("  %s: its memory sets the slots; a lower standard memory gives more", h.Name)
	case "capacity":
		p.note("  %s: its capacity of %d is what limits the slots; its machine holds more of the standard size, so raise the capacity to use them", h.Name, h.Capacity)
	}
}

// sourcePhrase is where the two figures of a size came from, in a few words.
func sourcePhrase(cpus, memory string) string {
	switch {
	case cpus == memory && cpus == "host":
		return "from the host's profile"
	case cpus == memory && cpus == "global":
		return "the fleet's default"
	case cpus != "" || memory != "":
		return "CPU from " + sourceWord(cpus) + ", memory from " + sourceWord(memory)
	}
	return "from the host's profile"
}

func sourceWord(s string) string {
	switch s {
	case "host":
		return "the host's profile"
	case "global":
		return "the fleet's default"
	}
	return "nowhere"
}

// formatMemoryMB writes memory the way an operator says it.
func formatMemoryMB(mb int64) string {
	if mb >= 1024 && mb%1024 == 0 {
		return strconv.FormatInt(mb/1024, 10) + " GB"
	}
	return strconv.FormatInt(mb, 10) + " MB"
}

// plural is "1 slot" and "3 slots".
func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}
