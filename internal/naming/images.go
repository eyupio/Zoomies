package naming

import (
	"fmt"
	"slices"
	"strings"
)

// RunnerImageRepo is where the images built from deploy/Dockerfile.runner are
// published. One repository with a tag per operating system, rather than a
// repository per operating system, so that `docker pull` autocompletion and
// the GHCR package page both show the whole catalogue at once.
const RunnerImageRepo = "ghcr.io/eyupio/zoomies-runner"

// RunnerFullImageRepo is where the runner-full target of the same Dockerfile is
// published, under the same tags as RunnerImageRepo, for the variants whose
// catalogue row is Full.
const RunnerFullImageRepo = "ghcr.io/eyupio/zoomies-runner-full"

// Image is one variant of the runner image: an operating system, the version
// of it, and the architectures that variant is published for.
type Image struct {
	// OS and Version are the platform the variant provides, in the same
	// spelling a Spec uses.
	OS      string
	Version string
	// Base is the upstream image the variant is built FROM. It is here rather
	// than only in the Dockerfile because the catalogue is what the docs, the
	// release matrix and the pool validator all read.
	Base string
	// Family is the package manager the variant installs with, which is the
	// only thing deploy/Dockerfile.runner has to branch on.
	Family string
	// Arches lists the architectures the variant is published for.
	Arches []string
	// Default marks the variant that :latest points at, and the one a pool
	// gets when it has not said which operating system it wants.
	Default bool
	// Full marks a variant that is also published as RunnerFullImageRepo:
	// the runner with the language toolchains the setup-* actions would
	// otherwise download already in its tool cache. Only Ubuntu carries it,
	// because Python's prebuilt interpreters -- the ones setup-python
	// installs -- are built for Ubuntu and nothing else.
	Full bool
}

// Tag is the image tag this variant is published under, e.g. "ubuntu-2404".
// It is the OS half of a Spec, which is what makes an image tag and a pool
// name agree by construction rather than by an operator remembering to keep
// them in step.
func (i Image) Tag() string {
	if v := CompactVersion(i.Version); v != "" {
		return i.OS + "-" + v
	}
	return i.OS
}

// Ref is the fully qualified image reference for this variant.
func (i Image) Ref() string { return RunnerImageRepo + ":" + i.Tag() }

// Describe renders the variant for prose: "Ubuntu 24.04 (amd64, arm64)".
func (i Image) Describe() string {
	return fmt.Sprintf("%s %s (%s)", PrettyOS(i.OS), i.Version, strings.Join(i.Arches, ", "))
}

// Supports reports whether this variant is published for an architecture. An
// empty arch is not a mismatch -- the caller simply has not said -- but an
// architecture Zoomies does not build for at all is.
func (i Image) Supports(arch string) bool {
	if strings.TrimSpace(arch) == "" {
		return true
	}
	return slices.Contains(i.Arches, NormalizeArch(arch))
}

// maxImageReferenceLength is Docker's own limit on a reference, name and tag
// together.
const maxImageReferenceLength = 255

// IsImageReference reports whether ref is shaped like an image reference and
// nothing else, which is what it takes to be offered as the argument of a command
// somebody will copy and run.
//
// A pool's image is free text, and the reference in a failed start is whatever the
// task carried, so a sentence that builds `docker pull <image>` out of one hands
// the text's author a command with a copy button: a backtick ends the span the UI
// draws, and a semicolon, a pipe, a substitution or a space starts a second
// command or a second argument inside it. ForSentence cannot make that safe,
// because the span is deliberate and the characters that matter are not only the
// backtick. The caller leaves the command out when this says no, and the sentence
// carries on without it.
//
// The rule is Docker's own alphabet -- letters, digits and . _ - : / @ -- with a
// letter or digit first, so that a reference cannot be read as an option. It is
// narrower than the reference grammar on purpose: nothing it refuses is an image
// anyone runs, and nothing it accepts means anything to a shell.
func IsImageReference(ref string) bool {
	if ref == "" || len(ref) > maxImageReferenceLength {
		return false
	}
	for i := 0; i < len(ref); i++ {
		switch c := ref[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case i > 0 && strings.IndexByte("._-:/@", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// The package-manager families deploy/Dockerfile.runner knows how to drive.
const (
	FamilyAPT = "apt"
	FamilyDNF = "dnf"
)

// The architecture sets a variant can be published for. They are package-level
// slices so that callers cannot accidentally alias one variant's list into
// another's; runnerImages hands out copies.
//
// A variant's Arches is not a wish: it is what CI actually builds, because the
// generated workflow matrices are written from it. A row claiming an
// architecture nothing publishes gives a pool a tag that does not exist, which
// is a runner that will not start rather than a validation error.
var (
	bothArches = []string{ArchAMD64, ArchARM64}
)

// runnerImages is the catalogue, and the one place an operating system is added
// or swapped. A row here plus `make generate` is the whole of it: the generator
// rewrites both workflows' matrices, the Makefile's build variants and the
// table on the site from these rows, and the Dockerfile already handles both
// package families. The pool validator reads the same list to tell an operator
// what they may ask for.
//
// Alpine is deliberately absent. actions/runner ships glibc binaries and .NET
// dependencies that musl does not satisfy, so an Alpine variant would build
// and then fail at the first job, which is worse than not offering it.
var runnerImages = []Image{
	{OS: OSUbuntu, Version: "24.04", Base: "ubuntu:24.04@sha256:224a1869083a311ef3f13648a154ba79832fbef6364d31493642ca03082da254", Family: FamilyAPT, Arches: bothArches, Default: true, Full: true},
	{OS: OSUbuntu, Version: "26.04", Base: "ubuntu:26.04@sha256:da6fc2be547864451aa253836dd926da33623312df4a9a243e35dc877c378a78", Family: FamilyAPT, Arches: bothArches, Full: true},
	{OS: OSUbuntu, Version: "22.04", Base: "ubuntu:22.04@sha256:829f6df217bcbae2b371026e81711d1a787c61b2967ad09d015063663ebafbf7", Family: FamilyAPT, Arches: bothArches, Full: true},
	{OS: OSDebian, Version: "13", Base: "debian:13-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a", Family: FamilyAPT, Arches: bothArches},
	{OS: OSDebian, Version: "12", Base: "debian:12-slim@sha256:88200866dfff7ea7f5cbcb6ec7c8a701889efe6fe859fe64d6990e4b07ea4171", Family: FamilyAPT, Arches: bothArches},
	{OS: OSFedora, Version: "42", Base: "fedora:42@sha256:99e203b80b1c3d8f7e161ec10a68fd02b081ef83a3963553e513c82846b97814", Family: FamilyDNF, Arches: bothArches},
	{OS: OSRocky, Version: "9", Base: "rockylinux/rockylinux:9@sha256:8101994123cf3d0a8fee517bee7f39e555c7d92bd2d9eb3303cc988a0eeed00f", Family: FamilyDNF, Arches: bothArches},
}

// Images returns the catalogue in the order the docs and the UI list it, with
// the default variant first.
func Images() []Image {
	out := make([]Image, len(runnerImages))
	copy(out, runnerImages)
	return out
}

// DefaultImage returns the variant :latest points at.
func DefaultImage() Image {
	for _, i := range runnerImages {
		if i.Default {
			return i
		}
	}
	return runnerImages[0]
}

// DefaultRunnerImage is the image reference a pool gets when it names neither
// an image nor an operating system.
func DefaultRunnerImage() string { return RunnerImageRepo + ":" + RunnerImageTag }

// RunnerImageTag is stamped by the container build independently of the binary
// version. Native builds retain the released channel. Container builds use dev
// or the release tag, including its leading v, as published by the workflows.
var RunnerImageTag = "latest"

// SplitImage separates an image reference into its repository, its tag with the
// colon still on it, and its digest with the @ still on it.
//
// Splitting on the last colon is wrong twice over, and both ways produce a
// reference that looks plausible and pulls nothing: a registry may carry a port,
// so registry.example.com:5000/zoomies would lose its host, and a digest carries
// a colon of its own inside sha256:....
func SplitImage(ref string) (repo, tag, digest string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref, digest = ref[:i], ref[i:]
	}
	// A colon after the last slash is a tag; one before it belongs to the
	// registry's port.
	if i := strings.LastIndex(ref, ":"); i >= 0 && !strings.Contains(ref[i+1:], "/") {
		ref, tag = ref[:i], ref[i:]
	}
	return ref, tag, digest
}

// PublishedRunnerTag reports whether a runner image tag is one this build's own
// catalogue accounts for: the moving channels, the channel this binary was
// stamped with, and every variant in the catalogue under any of them.
//
// It answers the only question a caller translating between the stock runner
// image and its Docker variant can answer without asking a registry. One step
// of one workflow publishes both images, so a tag this build hands out exists
// for both or for neither -- while a tag somebody pinned from another build
// promises nothing: a run whose second build did not finish leaves that commit
// tag on one image and not the other, and a release old enough predates the
// variant altogether. A pool moved onto a tag the registry lacks stops running
// every job, including the ones that never touch Docker.
func PublishedRunnerTag(tag string) bool {
	tag = strings.TrimPrefix(strings.TrimSpace(tag), ":")
	// No tag is the registry's "latest", which both images carry.
	if tag == "" {
		return true
	}
	for _, i := range runnerImages {
		// The released channel publishes the bare alias rather than
		// <variant>-latest, which is why the alias stands on its own.
		if tag == i.Tag() {
			return true
		}
	}
	for _, channel := range channels() {
		if tag == channel {
			return true
		}
		for _, i := range runnerImages {
			if tag == i.Tag()+"-"+channel {
				return true
			}
		}
	}
	return false
}

// channels lists the tags a runner image is published under that are not one
// variant's alias: the two that follow a branch, the one that follows the
// newest release, and whichever this binary was built to ask for.
func channels() []string {
	out := []string{"latest", "main", "dev"}
	if tag := strings.TrimSpace(RunnerImageTag); tag != "" && !slices.Contains(out, tag) {
		out = append(out, tag)
	}
	return out
}

// FindImage returns the catalogue entry for an operating system and version.
//
// An empty version means "whichever one we publish", which is how a pool that
// says only "ubuntu" still gets a working image. A version that is not
// published is a miss rather than a silent downgrade: quietly running Ubuntu
// 24.04 for a pool that asked for 20.04 is exactly the kind of surprise this
// package exists to remove.
func FindImage(os, version string) (Image, bool) {
	o := NormalizeOS(os)
	if o == "" {
		return Image{}, false
	}
	want := CompactVersion(version)
	var fallback Image
	found := false
	for _, i := range runnerImages {
		if i.OS != o {
			continue
		}
		if want != "" && CompactVersion(i.Version) == want {
			return i, true
		}
		if !found || i.Default {
			fallback, found = i, true
		}
	}
	if want != "" || !found {
		return Image{}, false
	}
	return fallback, true
}

// RunnerImageFor returns the image reference for a platform, and whether the
// catalogue publishes one. Callers that get false should keep whatever image
// they already had rather than substitute a different operating system.
func RunnerImageFor(os, version string) (string, bool) {
	i, ok := FindImage(os, version)
	if !ok {
		return "", false
	}
	ref := i.Ref()
	// The released channel uses bare platform aliases, not <platform>-latest.
	if RunnerImageTag != "latest" {
		ref += "-" + RunnerImageTag
	}
	return ref, true
}

// ResolveRunnerImage picks the image a pool's runners boot.
//
// A pool that names an image gets it unchanged: an operator who has built
// their own is not second-guessed. Otherwise the pool's platform picks the
// variant, so a pool called zoomies-4vcpu-debian-12 boots the Debian 12 image
// without anyone keeping the two in step by hand. A pool that names neither
// gets the instance default.
func ResolveRunnerImage(poolImage, os, version, instanceDefault string) string {
	if img := strings.TrimSpace(poolImage); img != "" {
		return img
	}
	if ref, ok := RunnerImageFor(os, version); ok {
		return ref
	}
	return instanceDefault
}

// SupportedPlatforms renders the catalogue as the sentence an error message
// uses to tell an operator what they may ask for.
func SupportedPlatforms() string {
	out := make([]string, 0, len(runnerImages))
	for _, i := range runnerImages {
		out = append(out, PrettyOS(i.OS)+" "+i.Version)
	}
	return strings.Join(out, ", ")
}
