package hosttune

// driftingCurrent names the checks whose Current text moves on every report
// ("61% free (210.0 GiB)"). Acceptable bars the same two for the same reason,
// and a test holds the two lists together.
var driftingCurrent = map[string]bool{"disk.space": true, "disk.inodes": true}

// SameFindings reports whether o says the same thing as r, ignoring when it was
// taken. disk.space and disk.inodes may differ in Current when their Status is
// equal: those figures move every report, so treating them as news would
// rewrite the stored body on every report of a busy host.
//
// The operator's marks (Accepted, Ended, Acceptable) are not compared. They are
// scrubbed on the way in and stamped at read time, so a decision never makes
// two reports differ.
func (r Report) SameFindings(o Report) bool {
	if r.OS != o.OS || r.Distro != o.Distro || r.WorkDir != o.WorkDir ||
		r.Container != o.Container || r.RebootPending != o.RebootPending ||
		len(r.Results) != len(o.Results) {
		return false
	}
	for i, a := range r.Results {
		b := o.Results[i]
		if a.ID != b.ID || a.Title != b.Title || a.Tier != b.Tier || a.Status != b.Status ||
			a.Recommended != b.Recommended || a.Rationale != b.Rationale || a.Reason != b.Reason ||
			a.Actionable != b.Actionable || a.Optional != b.Optional {
			return false
		}
		if a.Current != b.Current && !driftingCurrent[a.ID] {
			return false
		}
	}
	return true
}
