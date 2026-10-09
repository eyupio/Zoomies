package github

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

func TestWorkflowReadsUseImmutableBlobsAndDoNotFollowLaterBranchChanges(t *testing.T) {
	f := newFake(t)
	initial := "on: push\njobs: {build: {runs-on: self-hosted, steps: []}}\n"
	f.AddWorkflow("acme/api", ".github/workflows/ci.yml", initial)
	reader := f.Client("acme", store.TargetOrg).(KennelWorkflowReader)
	inventory, err := reader.KennelWorkflowInventory(context.Background(), "acme/api", "main")
	if err != nil || len(inventory.Files) != 1 {
		t.Fatalf("inventory=%+v, %v", inventory, err)
	}
	f.AddWorkflow("acme/api", ".github/workflows/ci.yml", "changed")
	got, err := reader.KennelWorkflowBlob(context.Background(), "acme/api", inventory.Files[0])
	if err != nil || string(got) != initial {
		t.Fatalf("pinned blob=%q, %v", got, err)
	}
	for _, req := range f.Requests() {
		if !strings.HasPrefix(req, "GET ") {
			t.Errorf("reader wrote: %s", req)
		}
	}
}

func TestWorkflowInventoryMakesOmittedAndOversizedFilesIncomplete(t *testing.T) {
	f := newFake(t)
	for i := 0; i < KennelWorkflowFiles+1; i++ {
		f.AddWorkflow("acme/api", fmt.Sprintf(".github/workflows/ci%03d.yml", i), "on: push")
	}
	f.AddWorkflow("acme/api", ".github/workflows/large.yml", strings.Repeat("x", KennelWorkflowBytes+1))
	got, err := f.Client("acme", store.TargetOrg).(KennelWorkflowReader).KennelWorkflowInventory(context.Background(), "acme/api", "main")
	if err != nil || !got.Partial || len(got.Files) != KennelWorkflowFiles {
		t.Fatalf("inventory=%+v, %v", got, err)
	}
}
