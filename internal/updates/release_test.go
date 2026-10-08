package updates

import (
	"slices"
	"strconv"
	"testing"
	"time"
)

// finished is the file list of a release once CI has uploaded all of it. The
// rule reads two of these, but a release in the wild lists every one, and a test
// that gave it only the two it reads could not notice a rule that matched too
// loosely.
var finished = []string{
	"checksums.txt",
	"zoomies_darwin_amd64", "zoomies_darwin_arm64",
	"zoomies_linux_amd64", "zoomies_linux_arm64",
	"zoomies_windows_amd64.exe",
	"zoomies-provenance.intoto.jsonl", "zoomies-provenance.sigstore.json",
}

// published is a finished release that went public at the given time.
func published(tag string, at time.Time) Release {
	return Release{
		Tag:         tag,
		URL:         "https://github.com/eyupio/zoomies/releases/tag/" + tag,
		PublishedAt: at,
		Assets:      slices.Clone(finished),
	}
}

// without is the release as it looks while its files are still uploading:
// public, and short of one asset.
func without(r Release, asset string) Release {
	r.Assets = slices.DeleteFunc(slices.Clone(r.Assets), func(a string) bool { return a == asset })
	return r
}

// inEveryRotation runs check on the list started from each entry in turn.
// GitHub promises no order, and a rule that finds the right answer only when it
// comes first is a rule that works until the day it does not.
func inEveryRotation(releases []Release, check func(rotated []Release)) {
	for i := range releases {
		check(append(slices.Clone(releases[i:]), releases[:i]...))
	}
}

// The tag is the one part of a release that is put into a download address and
// into a request a privileged helper reads, so the check is a yes or a no on the
// whole string. Each refusal here is a way a tag has been, or could be, almost
// right -- and "almost" is how a newline or a semicolon gets through.
func TestValidTagIsStrict(t *testing.T) {
	for _, tag := range []string{"v1.3.5", "v10.0.12", "v0.0.0"} {
		if !ValidTag(tag) {
			t.Errorf("ValidTag(%q) = false, want true", tag)
		}
	}
	for _, tc := range []struct{ name, tag string }{
		{"a capital V", "V1.3.5"},
		{"no v", "1.3.5"},
		{"two numbers", "v1.3"},
		{"four numbers", "v1.3.5.1"},
		{"a prerelease suffix", "v1.3.5-rc1"},
		{"a trailing newline", "v1.3.5\n"},
		{"a trailing carriage return", "v1.3.5\r"},
		{"a leading space", " v1.3.5"},
		{"a trailing space", "v1.3.5 "},
		{"text before the v", "xv1.3.5"},
		{"a shell separator", "v1.3.5;x"},
		{"fullwidth digits", "v\uff11.\uff13.\uff15"},
		{"an empty number", "v1..5"},
		{"a sign", "v1.-3.5"},
		{"nothing", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if ValidTag(tc.tag) {
				t.Errorf("ValidTag(%q) = true, want false", tc.tag)
			}
		})
	}
}

// A release binary reports 1.3.5 while its tag is v1.3.5, and a host is only
// ever sent to the release its controller runs. A build that is not exactly
// such a release has no tag to be sent to -- "dev" least of all, which is a real
// install tag and not one the helper accepts.
func TestTargetTagUsesTheRunningRelease(t *testing.T) {
	for _, tc := range []struct {
		running string
		want    string
		ok      bool
	}{
		{"1.3.5", "v1.3.5", true},
		{"v1.3.5", "v1.3.5", true},
		{"dev", "", false},
		{"main-sha-abc1234", "", false},
		{"v0.2-beta-5-gabc1234", "", false},
		{"v1.2", "", false},
		{"0.2-beta", "", false},
		{"", "", false},
	} {
		t.Run(strconv.Quote(tc.running), func(t *testing.T) {
			got, ok := TargetTag(tc.running)
			if got != tc.want || ok != tc.ok {
				t.Errorf("TargetTag(%q) = %q, %v; want %q, %v", tc.running, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// The names are the ones SelfUpdate downloads, and it serves only linux and
// darwin. An empty name for anything else is what keeps a release from looking
// complete for a system nothing can install it on.
func TestAssetNameNamesOnlyTheSystemsSelfUpdateServes(t *testing.T) {
	for _, tc := range []struct{ goos, goarch, want string }{
		{"linux", "amd64", "zoomies_linux_amd64"},
		{"linux", "arm64", "zoomies_linux_arm64"},
		{"darwin", "amd64", "zoomies_darwin_amd64"},
		{"darwin", "arm64", "zoomies_darwin_arm64"},
		{"windows", "amd64", ""},
		{"freebsd", "amd64", ""},
		{"", "", ""},
	} {
		if got := AssetName(tc.goos, tc.goarch); got != tc.want {
			t.Errorf("AssetName(%q, %q) = %q, want %q", tc.goos, tc.goarch, got, tc.want)
		}
	}
}

// A release is public before its files finish uploading -- binaries first, the
// checksums last -- so for a while there is a tag SelfUpdate would fail on.
// Offering it is the mistake this check exists to prevent: it sends a host to a
// download that cannot be verified.
func TestCompleteNeedsTheChecksumsAndTheHostsBinary(t *testing.T) {
	for _, tc := range []struct {
		name         string
		assets       []string
		goos, goarch string
		want         bool
	}{
		{"the checksums and the binary", []string{"checksums.txt", "zoomies_linux_amd64"}, "linux", "amd64", true},
		{"everything a finished release carries", finished, "linux", "amd64", true},
		{"a darwin host reads its own binary", finished, "darwin", "arm64", true},
		{"the binary without the checksums", []string{"zoomies_linux_amd64"}, "linux", "amd64", false},
		{"the checksums without the binary", []string{"checksums.txt"}, "linux", "amd64", false},
		{"only the other architecture's binary", []string{"checksums.txt", "zoomies_linux_arm64"}, "linux", "amd64", false},
		{"only the other system's binary", []string{"checksums.txt", "zoomies_darwin_amd64"}, "linux", "amd64", false},
		{"nothing at all", nil, "linux", "amd64", false},
		// SelfUpdate serves no windows host, so no release is complete for one,
		// whatever it is called and however many names it carries.
		{"windows, under every name it could have", append([]string{"zoomies_windows_amd64"}, finished...), "windows", "amd64", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Release{Tag: "v1.3.5", Assets: tc.assets}
			if got := r.Complete(tc.goos, tc.goarch); got != tc.want {
				t.Errorf("Complete(%q, %q) with %v = %v, want %v", tc.goos, tc.goarch, tc.assets, got, tc.want)
			}
		})
	}
}

// Every release below is one a careless rule would take, and all but the capital
// V are newer than the one that is right, so a filter that fails to drop one
// shows as the wrong answer rather than as a quiet pass. Three are tags
// CompareBuilds can order -- it reads a missing v, a missing patch number and a
// suffix without complaint -- so only the check on the shape of the tag keeps
// them out, and the last is perfect in every way but has no publication date.
// The list is tried in every rotation because a tag CompareBuilds cannot order
// only wins when it comes first.
func TestNewestIgnoresIncompletePrereleaseDraftAndOddTags(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	flagged := published("v1.6.0", t0)
	flagged.Prerelease = true
	candidate := published("v1.4.0-rc1", t0)
	candidate.Prerelease = true
	draft := published("v1.5.0", t0)
	draft.Draft = true
	undated := published("v2.0.0", time.Time{})
	releases := []Release{
		published("V1.1.0", t0),
		published("v1.3.4", t0),
		without(published("v1.3.5", t0), "checksums.txt"),
		candidate,
		draft,
		flagged,
		published("v1.7", t0),
		published("1.8.0", t0),
		published("v1.9.0-rc2", t0),
		undated,
	}

	inEveryRotation(releases, func(rotated []Release) {
		got, ok := Newest(rotated, "linux", "amd64")
		if !ok || got.Tag != "v1.3.4" {
			t.Errorf("Newest = %q, %v from %s first; want v1.3.4", got.Tag, ok, rotated[0].Tag)
		}
	})

	ineligible := slices.DeleteFunc(slices.Clone(releases), func(r Release) bool { return r.Tag == "v1.3.4" })
	if got, ok := Newest(ineligible, "linux", "amd64"); ok {
		t.Errorf("Newest of releases none of which qualifies = %q, true; want nothing", got.Tag)
	}
	if got, ok := Newest(nil, "linux", "amd64"); ok {
		t.Errorf("Newest of no releases = %q, true; want nothing", got.Tag)
	}
}

// 1.3.10 is later than 1.3.9, and a sort on the text or on the date says
// otherwise: the first because "1" comes before "9", the second because a
// maintainer can publish a patch to an old line after the new one.
func TestNewestOrdersByVersionNotByPublishTime(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	releases := []Release{
		published("v1.3.9", t0.Add(2*time.Hour)),
		published("v1.3.10", t0),
		published("v1.2.40", t0.Add(3*time.Hour)),
	}
	inEveryRotation(releases, func(rotated []Release) {
		if got, ok := Newest(rotated, "linux", "amd64"); !ok || got.Tag != "v1.3.10" {
			t.Errorf("Newest = %q, %v from %s first; want v1.3.10", got.Tag, ok, rotated[0].Tag)
		}
	})
}

// A release deleted and made again for the same tag is two entries of one
// version. The one made last is the one whose files are current.
func TestNewestBreaksAVersionTieByPublishTime(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	first := published("v1.3.5", t0)
	first.URL += "?first"
	second := published("v1.3.5", t0.Add(time.Hour))
	second.URL += "?second"

	inEveryRotation([]Release{first, second}, func(rotated []Release) {
		if got, ok := Newest(rotated, "linux", "amd64"); !ok || got.URL != second.URL {
			t.Errorf("Newest = %q, %v from %s first; want the one published last", got.URL, ok, rotated[0].URL)
		}
	})
}

// A number too large for an int still matches the tag pattern, and
// CompareBuilds can say nothing about such a tag. Left in, it would sit at the
// head of the list whenever it came first and hide every release that can be
// ordered.
func TestNewestSkipsATagTooBigToOrder(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	huge := published("v99999999999999999999.0.0", t0.Add(time.Hour))
	if !ValidTag(huge.Tag) {
		t.Fatalf("%q is not a valid tag, so this test no longer tries what it says", huge.Tag)
	}
	inEveryRotation([]Release{huge, published("v1.3.4", t0), published("v1.3.3", t0)}, func(rotated []Release) {
		if got, ok := Newest(rotated, "linux", "amd64"); !ok || got.Tag != "v1.3.4" {
			t.Errorf("Newest = %q, %v from %s first; want v1.3.4", got.Tag, ok, rotated[0].Tag)
		}
	})
}
