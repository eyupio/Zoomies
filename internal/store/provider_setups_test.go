package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProviderSetupCompletionIsSingleUseAndExpires(t *testing.T) {
	now := time.Now()
	s := newTestStoreAt(t, func() time.Time { return now })
	ctx := context.Background()
	p := &ProviderSetup{ID: NewID(PrefixProviderSetup), TokenHash: []byte("hash"), ExpiresAt: now.Add(time.Hour)}
	if err := s.CreateProviderSetup(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteProviderSetup(ctx, p.ID, []byte("wrong"), []byte("secret")); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong token: %v", err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			err := s.CompleteProviderSetup(ctx, p.ID, p.TokenHash, []byte("sealed"))
			if err == nil {
				wins.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("completion: %v", err)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d completions accepted", wins.Load())
	}
	got, err := s.GetProviderSetup(ctx, p.ID)
	if err != nil || string(got.PayloadEnc) != "sealed" {
		t.Fatalf("payload: %v %v", got, err)
	}
	now = p.ExpiresAt
	if _, err := s.GetProviderSetup(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired setup: %v", err)
	}
	if err := s.CompleteProviderSetup(ctx, p.ID, p.TokenHash, []byte("other")); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired completion: %v", err)
	}
}
