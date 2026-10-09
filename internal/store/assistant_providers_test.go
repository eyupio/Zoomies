package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func seedAssistantProvider(t *testing.T, s *Store, name string) *AssistantProvider {
	t.Helper()
	p := &AssistantProvider{Name: name, Kind: "openai_compatible", BaseURL: "http://localhost:11434/v1", Model: "llama3", Enabled: true}
	if err := s.CreateAssistantProvider(context.Background(), p); err != nil {
		t.Fatalf("creating %s: %v", name, err)
	}
	return p
}

// The key is written by its own setter, as a Proxmox credential is, so that
// an operator re-submitting a form never carries a key they did not type,
// and the row's other fields never ride along with a rotated key.
func TestAssistantProvidersRoundTripWithTheKeySealedApart(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := seedAssistantProvider(t, s, "Ollama")
	if p.ID == "" || p.ID[:4] != "asp_" {
		t.Errorf("id %q", p.ID)
	}
	if err := s.SetAssistantProviderKey(ctx, p.ID, []byte("sealed")); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAssistantProvider(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Ollama" || got.Kind != "openai_compatible" || got.BaseURL != p.BaseURL || got.Model != "llama3" || !got.Enabled || got.IsDefault {
		t.Errorf("row %+v", got)
	}
	if string(got.KeyEnc) != "sealed" {
		t.Errorf("key %q", got.KeyEnc)
	}
	if got.LastCheck != nil || got.LastCheckedAt != nil {
		t.Errorf("never checked, got %s at %v", got.LastCheck, got.LastCheckedAt)
	}
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	if err := s.SetAssistantProviderCheck(ctx, p.ID, []byte(`{"ok":true}`), at); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAssistantProvider(ctx, p.ID)
	if string(got.LastCheck) != `{"ok":true}` || got.LastCheckedAt == nil || !got.LastCheckedAt.Equal(at) {
		t.Errorf("check %s at %v", got.LastCheck, got.LastCheckedAt)
	}
	list, err := s.ListAssistantProviders(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list %v %v", list, err)
	}
	if _, err := s.GetAssistantProvider(ctx, "asp_nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing row: %v", err)
	}
}

func TestANameInUseIsAConflict(t *testing.T) {
	s := newTestStore(t)
	seedAssistantProvider(t, s, "Ollama")
	err := s.CreateAssistantProvider(context.Background(), &AssistantProvider{Name: "Ollama", Kind: "openai"})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("got %v", err)
	}
}

func TestSettingADefaultClearsTheOldOne(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := seedAssistantProvider(t, s, "A")
	b := seedAssistantProvider(t, s, "B")
	for _, id := range []string{a.ID, b.ID, a.ID} {
		if err := s.SetDefaultAssistantProvider(ctx, id); err != nil {
			t.Fatal(err)
		}
		list, _ := s.ListAssistantProviders(ctx)
		defaults := 0
		for _, p := range list {
			if p.IsDefault {
				defaults++
				if p.ID != id {
					t.Errorf("default is %s, want %s", p.ID, id)
				}
			}
		}
		if defaults != 1 {
			t.Errorf("after setting %s: %d defaults", id, defaults)
		}
	}
	if err := s.SetDefaultAssistantProvider(ctx, "asp_nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing row: %v", err)
	}
}

func TestDeletingTheDefaultLeavesNoDefault(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := seedAssistantProvider(t, s, "A")
	seedAssistantProvider(t, s, "B")
	if err := s.SetDefaultAssistantProvider(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAssistantProvider(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListAssistantProviders(ctx)
	if len(list) != 1 || list[0].IsDefault {
		t.Errorf("after delete: %+v", list)
	}
	if err := s.DeleteAssistantProvider(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestUpdateLeavesTheKeyAndTheDefaultAlone(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	p := seedAssistantProvider(t, s, "A")
	s.SetAssistantProviderKey(ctx, p.ID, []byte("sealed"))
	s.SetDefaultAssistantProvider(ctx, p.ID)
	p.Name, p.Model, p.Enabled, p.KeyEnc, p.IsDefault = "A2", "llama4", false, nil, false
	if err := s.UpdateAssistantProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetAssistantProvider(ctx, p.ID)
	if got.Name != "A2" || got.Model != "llama4" || got.Enabled {
		t.Errorf("update not applied: %+v", got)
	}
	if string(got.KeyEnc) != "sealed" || !got.IsDefault {
		t.Errorf("update touched the key or the default: key %q default %v", got.KeyEnc, got.IsDefault)
	}
}
