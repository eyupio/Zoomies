package migrate

import (
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

func pool(name string, labels ...string) *store.Pool {
	return &store.Pool{
		ID:      "pool_" + name,
		Name:    name,
		Labels:  store.StringSlice(store.BrandLabels(labels)),
		Enabled: true,
	}
}

func TestSuggestMatchesTheMachineAHostedLabelPromises(t *testing.T) {
	pools := []*store.Pool{
		pool("zoomies-linux-x64", "zoomies-linux-x64"),
		pool("zoomies-linux-arm64", "zoomies-linux-arm64"),
	}
	got := Suggest(pools, []string{"ubuntu-latest", "ubuntu-24.04-arm", "windows-latest"})

	if got["ubuntu-latest"] != "zoomies-linux-x64" {
		t.Errorf("ubuntu-latest -> %q, want the x64 pool", got["ubuntu-latest"])
	}
	if got["ubuntu-24.04-arm"] != "zoomies-linux-arm64" {
		t.Errorf("ubuntu-24.04-arm -> %q, want the arm64 pool", got["ubuntu-24.04-arm"])
	}
	// Nothing in this fleet runs Windows, and proposing a Linux pool for a
	// Windows job would produce a workflow that queues forever.
	if to, ok := got["windows-latest"]; ok {
		t.Errorf("windows-latest -> %q, want no suggestion at all", to)
	}
}

func TestSuggestUsesAPoolThatPromisesNothing(t *testing.T) {
	// The shape a single-host fleet ends up with: one pool, no os or arch
	// label, willing to take anything.
	pools := []*store.Pool{pool("builders", "builders")}
	got := Suggest(pools, []string{"ubuntu-latest"})
	if got["ubuntu-latest"] != "builders" {
		t.Fatalf("ubuntu-latest -> %q, want the only pool there is", got["ubuntu-latest"])
	}
}

func TestSuggestIgnoresDisabledPools(t *testing.T) {
	disabled := pool("zoomies-linux-x64", "zoomies-linux-x64")
	disabled.Enabled = false
	if got := Suggest([]*store.Pool{disabled}, []string{"ubuntu-latest"}); len(got) != 0 {
		t.Fatalf("Suggest = %v, want nothing: a disabled pool takes no work", got)
	}
}

func TestSuggestIsStable(t *testing.T) {
	pools := []*store.Pool{
		pool("b-pool", "b-pool", "linux", "x64"),
		pool("a-pool", "a-pool", "linux", "x64"),
	}
	first := Suggest(pools, []string{"ubuntu-latest"})["ubuntu-latest"]
	for range 20 {
		if got := Suggest(pools, []string{"ubuntu-latest"})["ubuntu-latest"]; got != first {
			t.Fatalf("Suggest is not stable: %q then %q", first, got)
		}
	}
	if first != "a-pool" {
		t.Errorf("tie broke to %q, want the first pool by name", first)
	}
}

func TestDescribeHosted(t *testing.T) {
	cases := []struct{ label, os, arch string }{
		{"ubuntu-latest", "linux", "x64"},
		{"ubuntu-22.04", "linux", "x64"},
		{"ubuntu-24.04-arm", "linux", "arm64"},
		{"windows-2022", "windows", "x64"},
		{"windows-11-arm", "windows", "arm64"},
		{"macos-13", "macos", "x64"},
		{"macos-14", "macos", "arm64"},
		{"macos-latest", "macos", "arm64"},
		{"macos-15-large", "macos", "x64"},
		{"self-hosted", "", ""},
	}
	for _, c := range cases {
		os, arch := describeHosted(c.label)
		if os != c.os || arch != c.arch {
			t.Errorf("describeHosted(%q) = %q/%q, want %q/%q", c.label, os, arch, c.os, c.arch)
		}
	}
}

func TestPlanRepoSummarisesEveryWorkflow(t *testing.T) {
	workflows := []Workflow{
		{Path: ".github/workflows/ci.yml", SHA: "aaa", Content: "jobs:\n  build:\n    runs-on: ubuntu-latest\n"},
		{Path: ".github/workflows/win.yml", SHA: "bbb", Content: "jobs:\n  build:\n    runs-on: windows-latest\n"},
	}
	plan := PlanRepo("acme/widgets", "main", workflows, zoomies)

	if !plan.Changed() {
		t.Fatal("the plan changes ci.yml but reports no change")
	}
	if len(plan.Workflows) != 2 {
		t.Fatalf("workflows = %d, want both files reported", len(plan.Workflows))
	}
	if !plan.Workflows[0].Changed() || plan.Workflows[1].Changed() {
		t.Errorf("wrong file changed: %+v", plan.Workflows)
	}
	if plan.Workflows[0].Diff == "" {
		t.Error("a changed file has no diff to review")
	}
	if plan.Workflows[1].Diff != "" {
		t.Error("an unchanged file produced a diff")
	}
	if strings.Join(plan.HostedLabels, ",") != "ubuntu-latest,windows-latest" {
		t.Errorf("hosted_labels = %v, want both labels the repository asks for", plan.HostedLabels)
	}

	counts := Count([]RepoPlan{plan})
	want := Counts{Repos: 1, Workflows: 1, Jobs: 1, Skipped: 1}
	if counts != want {
		t.Errorf("Count = %+v, want %+v", counts, want)
	}
}

func TestIsWorkflowPath(t *testing.T) {
	yes := []string{".github/workflows/ci.yml", ".github/workflows/release.yaml", "/.github/workflows/a.YML"}
	for _, p := range yes {
		if !IsWorkflowPath(p) {
			t.Errorf("IsWorkflowPath(%q) = false, want true", p)
		}
	}
	no := []string{
		".github/workflows/archive/old.yml", // GitHub does not run these
		".github/actions/thing/action.yml",
		".github/workflows/README.md",
		"workflows/ci.yml",
		"",
	}
	for _, p := range no {
		if IsWorkflowPath(p) {
			t.Errorf("IsWorkflowPath(%q) = true, want false", p)
		}
	}
}

func TestDiffIsUnifiedAndMinimal(t *testing.T) {
	before := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n"
	after := "a\nb\nc\nd\ne\nf\nG\nh\ni\nj\n"
	got := Diff("f.yml", before, after)
	if !strings.HasPrefix(got, "--- a/f.yml\n+++ b/f.yml\n@@ ") {
		t.Fatalf("diff does not start with a unified header:\n%s", got)
	}
	if !strings.Contains(got, "-g\n") || !strings.Contains(got, "+G\n") {
		t.Errorf("diff does not show the change:\n%s", got)
	}
	// Three lines of context each side, and nothing more: the first two lines
	// of the file are too far from the change to appear.
	if strings.Contains(got, " a\n") || strings.Contains(got, " b\n") {
		t.Errorf("diff carries more context than it should:\n%s", got)
	}
	if Diff("f.yml", before, before) != "" {
		t.Error("two identical files produced a diff")
	}
}
