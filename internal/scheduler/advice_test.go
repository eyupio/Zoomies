package scheduler

import (
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

func kept(class store.SizeClass, runs int, labels ...string) *store.JobClassAsked {
	return &store.JobClassAsked{
		JobClass: store.JobClass{Repo: "Acme/Widgets", Workflow: "CI", JobName: "build", Class: class,
			Basis: store.SizeBasisHistory, Runs: runs,
			Reason: "its memory needs about 6.2 GB (the 90th percentile of 12 runs, with a fifth added), which a " + string(class) + " runner holds"},
		Labels: labels,
	}
}

func TestLabelAdviceSaysWhatAWorkflowShouldWriteInstead(t *testing.T) {
	cfg := DefaultSizeConfig()
	cases := []struct {
		name     string
		in       *store.JobClassAsked
		pins     []*store.SizePin
		wantKind string
		// wantInFix is what the fix has to tell the author to write.
		wantInFix string
	}{
		{"asks for less than its runs need", kept(store.SizeLarge, 12, "self-hosted", "zoomies-medium"), nil,
			AdviceTooSmall, "write zoomies-large in runs-on in place of zoomies-medium"},
		{"asks for more than its runs need", kept(store.SizeSmall, 12, "self-hosted", "zoomies-large"), nil,
			AdviceTooLarge, "write zoomies-small in runs-on in place of zoomies-large"},
		{"asks for nothing and needs more than the default", kept(store.SizeLarge, 12, "self-hosted", "linux", "x64", "zoomies"), nil,
			AdviceUnguaranteed, "add zoomies-large to runs-on"},
		{"asks for the class its runs call for", kept(store.SizeLarge, 12, "self-hosted", "zoomies-large"), nil, "", ""},
		{"asks for nothing and the default is right", kept(store.SizeMedium, 12, "self-hosted", "zoomies"), nil, "", ""},
		// Landing on a larger host than the default is the harmless direction: the
		// job is not killed and the labels need not be touched.
		{"asks for nothing and needs less than the default", kept(store.SizeSmall, 12, "self-hosted", "zoomies"), nil, "", ""},
		{"nothing was recorded about what it asks for", kept(store.SizeLarge, 12), nil, "", ""},
		// A job that asks for a pool of somebody's own is not asking for the
		// automatic ones, and adding a class label to it would send it away from
		// the pool that carries the label it needs.
		{"asks for a pool of its own", kept(store.SizeLarge, 12, "self-hosted", "zoomies", "gpu"), nil, "", ""},
		{"asks for a pool of its own by a class label too", kept(store.SizeLarge, 12, "self-hosted", "gpu", "zoomies-medium"), nil,
			AdviceTooSmall, "write zoomies-large in runs-on in place of zoomies-medium"},
		{"pinned in its repository", kept(store.SizeLarge, 12, "self-hosted", "zoomies-medium"),
			[]*store.SizePin{{Repo: "acme/widgets", Class: store.SizeMedium}}, "", ""},
		{"pinned itself", kept(store.SizeLarge, 12, "self-hosted", "zoomies-medium"),
			[]*store.SizePin{{Repo: "acme/widgets", Workflow: "CI", JobName: "build", Class: store.SizeMedium}}, "", ""},
		{"another job pinned", kept(store.SizeLarge, 12, "self-hosted", "zoomies-medium"),
			[]*store.SizePin{{Repo: "acme/widgets", Workflow: "CI", JobName: "test", Class: store.SizeMedium}},
			AdviceTooSmall, "zoomies-large"},
		{"another repository pinned", kept(store.SizeLarge, 12, "self-hosted", "zoomies-medium"),
			[]*store.SizePin{{Repo: "acme/other", Class: store.SizeMedium}}, AdviceTooSmall, "zoomies-large"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cfg.LabelAdvice(tc.in, tc.pins)
			if tc.wantKind == "" {
				if got != nil {
					t.Fatalf("advice was given where none was due: %+v", got)
				}
				return
			}
			if got == nil || got.Kind != tc.wantKind {
				t.Fatalf("advice = %+v, want kind %s", got, tc.wantKind)
			}
			if !strings.Contains(got.Fix, tc.wantInFix) {
				t.Fatalf("the fix is %q, want it to say %q", got.Fix, tc.wantInFix)
			}
			if got.Class != tc.in.Class || got.Runs != tc.in.Runs || got.Repo != tc.in.Repo || len(got.Labels) != len(tc.in.Labels) {
				t.Fatalf("the advice does not carry the job: %+v", got)
			}
			if got.State != AdviceStateOK || got.MinRuns != AdviceMinRuns || got.RecommendedClass != tc.in.Class || got.Reason != tc.in.Reason {
				t.Fatalf("the advice does not say what it rests on: %+v", got)
			}
			if !strings.HasSuffix(got.Message, ".") || !strings.HasSuffix(got.Fix, ".") {
				t.Fatalf("the sentences should end: %q / %q", got.Message, got.Fix)
			}
		})
	}
}

// The advice for a job that only asks for the brand label has to say why it is
// advice at all: the routing it gets is not a promise.
func TestTheUnguaranteedAdviceSaysWhyBestEffortIsNotEnough(t *testing.T) {
	got := DefaultSizeConfig().LabelAdvice(kept(store.SizeLarge, 12, "self-hosted", "zoomies"), nil)
	if got == nil || got.Asked != "" {
		t.Fatalf("advice = %+v", got)
	}
	for _, want := range []string{"asks only for zoomies", "best effort", "GitHub decides which waiting job a runner takes", "Its memory needs about 6.2 GB"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("the message %q does not say %q", got.Message, want)
		}
	}
}

func TestAdviceOnlyFollowsMeasuredHistory(t *testing.T) {
	cfg := DefaultSizeConfig()
	in := kept(store.SizeLarge, 12, "self-hosted", "zoomies-medium")
	in.Basis = store.SizeBasisDefault
	if got := cfg.LabelAdvice(in, nil); got != nil {
		t.Fatalf("advice from a class that was not worked out from runs: %+v", got)
	}
	if got := cfg.LabelAdvice(nil, nil); got != nil {
		t.Fatalf("advice from nothing: %+v", got)
	}
	in.Basis, in.Class = store.SizeBasisHistory, "huge"
	if got := cfg.LabelAdvice(in, nil); got != nil {
		t.Fatalf("advice from a class that is not one: %+v", got)
	}
}

func TestAdviceIsOrderedByWhatItCostsToLeaveIt(t *testing.T) {
	in := []*Advice{
		{Repo: "b/b", Workflow: "CI", JobName: "x", Kind: AdviceTooLarge},
		{Repo: "b/b", Workflow: "CI", JobName: "a", Kind: AdviceTooSmall},
		{Repo: "a/a", Workflow: "CI", JobName: "z", Kind: AdviceUnguaranteed},
		{Repo: "a/a", Workflow: "CI", JobName: "b", Kind: AdviceTooSmall},
		{Repo: "a/a", Workflow: "CI", JobName: "a", Kind: AdviceTooSmall},
	}
	SortAdvice(in)
	var got []string
	for _, a := range in {
		got = append(got, a.Kind+":"+a.Repo+":"+a.JobName)
	}
	want := []string{
		"too_small:a/a:a", "too_small:a/a:b", "too_small:b/b:a", "unguaranteed:a/a:z", "too_large:b/b:x",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// Too few runs is a row, not an absence: an agent asking why a job has no
// advice is told how far it is from having some, and the boundary is the
// named constant on both sides.
func TestTooFewRunsIsARowThatSaysSo(t *testing.T) {
	cfg := DefaultSizeConfig()
	got := cfg.LabelAdvice(kept(store.SizeLarge, AdviceMinRuns-1, "self-hosted", "zoomies-medium"), nil)
	if got == nil || got.State != AdviceStateNotEnoughData || got.Kind != "" {
		t.Fatalf("advice = %+v, want a not_enough_data row with no kind", got)
	}
	if got.Runs != AdviceMinRuns-1 || got.MinRuns != AdviceMinRuns || got.RecommendedClass != store.SizeLarge || got.Reason == "" {
		t.Fatalf("the row does not carry the count and the class: %+v", got)
	}
	if !strings.Contains(got.Message, "4 of 5 measured runs") || got.Fix != "" {
		t.Fatalf("message = %q, fix = %q", got.Message, got.Fix)
	}
	if at := cfg.LabelAdvice(kept(store.SizeLarge, AdviceMinRuns, "self-hosted", "zoomies-medium"), nil); at == nil || at.State != AdviceStateOK {
		t.Fatalf("at the minimum the advice is given: %+v", at)
	}
	// A job that asks for exactly what it is heading for has nothing to be
	// told, even once; it is the sparse row's kind that is empty, not its advice.
	if right := cfg.LabelAdvice(kept(store.SizeLarge, AdviceMinRuns-1, "self-hosted", "zoomies-large"), nil); right == nil || right.State != AdviceStateNotEnoughData {
		t.Fatalf("a sparse job is a sparse row whatever it asks for: %+v", right)
	}
}

func TestSparseRowsSortLast(t *testing.T) {
	in := []*Advice{
		{Repo: "a/a", JobName: "sparse", State: AdviceStateNotEnoughData},
		{Repo: "b/b", JobName: "x", Kind: AdviceTooLarge, State: AdviceStateOK},
	}
	SortAdvice(in)
	if in[0].JobName != "x" || in[1].JobName != "sparse" {
		t.Fatalf("order = %s, %s", in[0].JobName, in[1].JobName)
	}
}
