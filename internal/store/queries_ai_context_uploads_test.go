package store

import (
	"errors"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/aicontext"
)

// An Actions token is valid for minutes, and an upload replayed inside that
// window would re-admit a snapshot the controller may since have replaced.
func TestAnUploadTokenIsAdmittedOnceAndForgottenAfterItExpires(t *testing.T) {
	s := newTestStore(t)
	r, _, _ := contextFixture(t, s)
	expires := s.Now().Add(5 * time.Minute)
	if err := s.ClaimAIContextUploadToken(t.Context(), "jti-1", r.ID, expires); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimAIContextUploadToken(t.Context(), "jti-1", r.ID, expires); !errors.Is(err, ErrConflict) {
		t.Fatalf("replayed token = %v; want ErrConflict", err)
	}
	if err := s.ClaimAIContextUploadToken(t.Context(), "jti-old", r.ID, s.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimAIContextUploadToken(t.Context(), "jti-2", r.ID, expires); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := s.read.QueryRow(`SELECT COUNT(*) FROM ai_context_upload_tokens WHERE token_id='jti-old'`).Scan(&left); err != nil || left != 0 {
		t.Fatal("an expired token ID was kept", left, err)
	}
}

func TestOnlyAnEnabledZoomiesOnlyRepositoryReceivesUploads(t *testing.T) {
	s := newTestStore(t)
	r, _, _ := contextFixture(t, s)
	if _, err := s.FindAIContextUploadTarget(t.Context(), "github.com", r.Key.RepositoryID); !errors.Is(err, ErrNotFound) {
		t.Fatal("a Both-mode repository accepted an upload", err)
	}
	config := r.Config
	config.Destination, config.UploadURL = aicontext.Zoomies, aicontext.UploadURLFor("https://zoomies.example.com")
	if err := s.SaveAIContextConfig(t.Context(), r.ID, r.Revision, config); err != nil {
		t.Fatal(err)
	}
	// Only a host with a Zoomies-only repository is one whose Actions tokens
	// an upload may be checked against.
	if hosts, err := s.AIContextUploadHosts(t.Context()); err != nil || len(hosts) != 1 || hosts[0] != r.Key.GitHubHost {
		t.Fatal("upload hosts", hosts, err)
	}
	found, err := s.FindAIContextUploadTarget(t.Context(), "github.com", r.Key.RepositoryID)
	if err != nil || found.ID != r.ID {
		t.Fatal("the Zoomies-only repository was not found", err)
	}
	if _, err := s.FindAIContextUploadTarget(t.Context(), "github.example.org", r.Key.RepositoryID); !errors.Is(err, ErrNotFound) {
		t.Fatal("another host's repository ID matched", err)
	}
}
