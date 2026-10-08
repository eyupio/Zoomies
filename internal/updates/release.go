// Package updates is the rule for which GitHub release an update mode would
// take, and the sentence that says why.
//
// It is pure: it reads no clock, opens no database and makes no request. Time
// arrives as an argument and releases arrive as a list, which is what lets the
// moments that matter -- a soak ending this second, a release dated tomorrow --
// be tried to the nanosecond instead of waited for, and what lets the status the
// UI shows and the update the controller starts come from one answer instead of
// two that drift apart.
//
// Anything that reads a file, runs a command or asks the clock belongs to the
// code that calls this.
package updates

import (
	"regexp"
	"slices"
	"time"

	"github.com/eyupio/zoomies/internal/version"
)

// checksumsAsset is the file a release publishes the SHA-256 of each binary in.
// SelfUpdate installs nothing it cannot find an entry for there.
const checksumsAsset = "checksums.txt"

// Release is one entry of GitHub's release list, cut down to what the rule reads.
type Release struct {
	// Tag is the release's tag, such as "v1.3.5". It comes off the network, so
	// nothing here relies on its shape until ValidTag has said it is one.
	Tag string
	// URL is the release's page, so an operator can read its notes before
	// choosing to update.
	URL string
	// PublishedAt is GitHub's published_at, where the soak starts. The zero time
	// means no date was given -- encoding/json yields it for a null, a missing or
	// a zero published_at without an error -- and a release that cannot be dated
	// cannot be soaked, so it is never offered.
	PublishedAt time.Time
	// Prerelease and Draft are GitHub's own flags, honoured whatever the tag
	// looks like: a maintainer can mark a release with a stable-looking tag as
	// either.
	Prerelease, Draft bool
	// Assets are the names of the files attached to the release.
	Assets []string
}

// tagPattern is the only shape of tag an update ever names. A tag is network
// data on its way into a download address and into the request a privileged
// helper reads, so it is matched whole against one strict pattern rather than
// parsed and trusted. Go's $ matches only at the very end of the text, so
// "v1.3.5\n" is refused here where an engine whose $ also matches before a final
// newline would let it through.
var tagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// ValidTag says whether tag is exactly vMAJOR.MINOR.PATCH: no capital V, no
// prerelease suffix, no whitespace, nothing before or after.
func ValidTag(tag string) bool { return tagPattern.MatchString(tag) }

// TargetTag is the tag a host follows: the release its controller is running, or
// false when that build is not one a tag can name.
//
// A host is never sent to "latest", so that an agent is never left ahead of its
// controller. The tag is built from version.Release and not from
// version.InstallTag, which answers "dev" for a build from main -- right for
// choosing an image, wrong here, where the answer has to be a tag the helper
// will accept or nothing. Release binaries report their version without the
// leading v, so the v is put back before the shape is checked.
func TargetTag(running string) (string, bool) {
	release, ok := version.Release(running)
	if !ok {
		return "", false
	}
	tag := "v" + release
	if !ValidTag(tag) {
		return "", false
	}
	return tag, true
}

// AssetName is the binary a release publishes for a system, named as
// installer.SelfUpdate downloads it, or empty for a system SelfUpdate does not
// serve. This package may not import the installer, so the name is written out
// here as well, and the two change together.
func AssetName(goos, goarch string) string {
	if goos != "linux" && goos != "darwin" {
		return ""
	}
	return "zoomies_" + goos + "_" + goarch
}

// Complete says whether the release carries what SelfUpdate fetches for this
// system: checksums.txt to verify against, and the binary itself.
//
// A release is public before its files have all been uploaded -- binaries
// first, checksums last -- so for a while there is a tag that exists and cannot
// be installed, and offering it would send a host to a download that fails.
func (r Release) Complete(goos, goarch string) bool {
	asset := AssetName(goos, goarch)
	return asset != "" && slices.Contains(r.Assets, checksumsAsset) && slices.Contains(r.Assets, asset)
}

// eligible is the whole of "a release an update could take": a tag of the strict
// shape, neither a draft nor a prerelease, a publication date, its files, and a
// tag that CompareBuilds can place.
//
// The date matters in every mode, not only auto. The soak is counted from it, so
// a release that cannot be dated cannot be soaked, and it is treated as not yet
// released rather than as old: read as the zero time it would count as public
// for centuries and pass any wait, in the one mode that exists to hold a release
// back. Nothing upstream is certain to refuse it first, because encoding/json
// hands over the zero time without an error.
//
// The last condition is for a number too large for an int, which matches the
// pattern and which CompareBuilds can say nothing about. Ranked anyway, it would
// sit at the head of the list whenever it came first and hide every release that
// can be ordered. Any tag it can place compares as ahead of or equal to v0.0.0,
// and one it cannot parse compares as differing, which is the difference tested
// for.
func (r Release) eligible(goos, goarch string) bool {
	return ValidTag(r.Tag) && !r.Prerelease && !r.Draft && !r.PublishedAt.IsZero() && r.Complete(goos, goarch) &&
		version.CompareBuilds(r.Tag, "v0.0.0") != version.SkewDiffers
}

// Newest is the newest release an update could take, and false when there is
// none.
//
// It orders with version.CompareBuilds, the only ordering this repository
// trusts, and not by publish date: a maintainer can publish a patch to an old
// line after the new one, and the text of "v1.3.10" sorts before "v1.3.9". Two
// entries of one version -- a release deleted and made again -- are told apart
// by publish date, the later being the one whose files are current.
func Newest(releases []Release, goos, goarch string) (Release, bool) {
	var best Release
	found := false
	for _, r := range releases {
		if !r.eligible(goos, goarch) {
			continue
		}
		if !found || r.supersedes(best) {
			best, found = r, true
		}
	}
	return best, found
}

// supersedes says whether r is a better answer to "newest" than other.
func (r Release) supersedes(other Release) bool {
	switch version.CompareBuilds(r.Tag, other.Tag) {
	case version.SkewAhead:
		return true
	case version.SkewNone:
		return r.PublishedAt.After(other.PublishedAt)
	}
	return false
}

// displaced is the release that newest pushed aside: the newest release an
// update could take that is older than newest and still ahead of what is
// running. Under a soak it is the one an operator asks about, having perhaps
// been public for the whole wait and still not been taken. Only it is named,
// because each of the earlier ones was pushed aside by the release after it. It
// asks Newest rather than restating what makes a release eligible, so the two
// cannot drift apart.
func displaced(releases []Release, goos, goarch string, newest Release, running string) (Release, bool) {
	between := make([]Release, 0, len(releases))
	for _, r := range releases {
		if version.CompareBuilds(r.Tag, newest.Tag) == version.SkewBehind &&
			version.CompareBuilds(running, r.Tag) == version.SkewBehind {
			between = append(between, r)
		}
	}
	return Newest(between, goos, goarch)
}
