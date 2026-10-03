package store

import (
	"errors"
	"testing"
)

func TestInstallationOwnershipIsScopedLiveAndCascades(t *testing.T) {
	s := newTestStore(t)
	r, owner, _ := contextFixture(t, s)
	installation := r.Key.InstallationID
	other := &User{Username: "not-an-owner", Role: RoleViewer}
	if err := s.CreateUser(t.Context(), other); err != nil {
		t.Fatal(err)
	}

	if owns, err := s.UserOwnsAIContextInstallation(t.Context(), installation, owner.ID); err != nil || owns {
		t.Fatal("nobody owns an installation until an administrator says so", owns, err)
	}
	if err := s.ReplaceAIContextInstallationOwners(t.Context(), installation, []string{"nobody"}); !errors.Is(err, ErrNotFound) {
		t.Fatal("an unknown user must be refused, not skipped", err)
	}
	if err := s.ReplaceAIContextInstallationOwners(t.Context(), "missing", nil); !errors.Is(err, ErrNotFound) {
		t.Fatal("an unknown installation must be refused", err)
	}
	if err := s.ReplaceAIContextInstallationOwners(t.Context(), installation, []string{owner.ID}); err != nil {
		t.Fatal(err)
	}
	if owns, _ := s.UserOwnsAIContextInstallation(t.Context(), installation, owner.ID); !owns {
		t.Fatal("owner not recorded")
	}
	if owns, _ := s.UserOwnsAIContextInstallation(t.Context(), installation, other.ID); owns {
		t.Fatal("ownership leaked to another user")
	}

	// Ownership confers no source access.
	if allowed, _ := s.AIContextUserAccess(t.Context(), r.ID, owner.ID); allowed {
		t.Fatal("owning an installation must not make anyone a reader")
	}

	rows, total, err := s.ListAIContextRepositoriesOwnedBy(t.Context(), 50, 0, "", "", owner.ID)
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatal("owner listing", total, err)
	}
	if _, total, _ = s.ListAIContextRepositoriesOwnedBy(t.Context(), 50, 0, "", "", other.ID); total != 0 {
		t.Fatal("a non-owner listed repositories")
	}
	if _, total, _ = s.ListAIContextRepositoriesOwnedBy(t.Context(), 50, 0, installation, "", other.ID); total != 0 {
		t.Fatal("naming the installation must not widen a non-owner's listing")
	}

	refs, err := s.AIContextOwnedInstallations(t.Context(), owner.ID)
	if err != nil || len(refs) != 1 || refs[0].ID != installation {
		t.Fatal("owned installations", refs, err)
	}

	// A disabled user owns nothing, without anyone having to clean up.
	owner.Disabled = true
	if err := s.UpdateUser(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	if owns, _ := s.UserOwnsAIContextInstallation(t.Context(), installation, owner.ID); owns {
		t.Fatal("a disabled owner kept their authority")
	}
	owner.Disabled = false
	if err := s.UpdateUser(t.Context(), owner); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteUser(t.Context(), owner.ID); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.AIContextInstallationOwners(t.Context(), installation); len(ids) != 0 {
		t.Fatal("deleting a user left them as an owner", ids)
	}
}
