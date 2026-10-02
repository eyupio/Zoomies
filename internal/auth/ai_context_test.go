package auth

import (
	"testing"

	"github.com/eyupio/zoomies/internal/aicontext"
	"github.com/eyupio/zoomies/internal/store"
)

func TestSourceAccessRequiresMembershipBeyondFleetRolesAndTokenScopes(t *testing.T) {
	s, st, _ := newService(t)
	u := addUser(t, st, "reader", store.RoleAdmin, nil)
	i := &store.Installation{AppID: 1, InstallationID: 2, Target: "acme", TargetType: store.TargetOrg}
	if err := st.CreateInstallation(t.Context(), i); err != nil {
		t.Fatal(err)
	}
	r := &store.AIContextRepository{Key: aicontext.RepositoryKey{GitHubHost: "github.com", InstallationID: i.ID, RepositoryID: 1}, FullName: "acme/private", Config: aicontext.DefaultConfig("main")}
	if err := st.CreateAIContextRepository(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	st.SetAIContextAvailable(t.Context(), r.ID, true)
	id := &Identity{Kind: KindUser, ID: u.ID, UserID: u.ID, Role: u.Role}
	if allowed, err := s.ContextAccess(t.Context(), id, r.ID); err != nil || allowed {
		t.Fatal("admin implicitly gained source access")
	}
	if err := st.ReplaceAIContextMembers(t.Context(), r.ID, []string{u.ID}); err != nil {
		t.Fatal(err)
	}
	if allowed, err := s.ContextAccess(t.Context(), id, r.ID); err != nil || !allowed {
		t.Fatal("member refused")
	}
	for _, id := range []*Identity{nil, {Kind: KindToken, Role: store.RoleAdmin}, {Kind: KindToken, UserID: u.ID, Role: store.RoleAdmin, Scopes: []string{"jobs:read"}}, {Kind: KindConnection, ID: "old-connection", UserID: u.ID, Role: store.RoleViewer}, {Kind: KindAgent, UserID: u.ID, Role: store.RoleAdmin}} {
		if allowed, err := s.ContextAccess(t.Context(), id, r.ID); err != nil || allowed {
			t.Fatalf("source granted to %#v: %v", id, err)
		}
	}
	id = &Identity{Kind: KindToken, UserID: u.ID, Role: store.RoleViewer, Scopes: []string{"context:read"}}
	if allowed, err := s.ContextAccess(t.Context(), id, r.ID); err != nil || !allowed {
		t.Fatal("owned scoped token refused")
	}
	u.Disabled = true
	if err := st.UpdateUser(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	if allowed, err := s.ContextAccess(t.Context(), id, r.ID); err != nil || allowed {
		t.Fatal("cached identity outlived disabled user")
	}
}

func TestAnAgentCannotGrantItselfSourceConsent(t *testing.T) {
	s, _, _ := newService(t)
	for _, actor := range []*Identity{nil, {Kind: KindToken, UserID: "owner", Role: store.RoleAdmin}, {Kind: KindConnection, UserID: "owner", Role: store.RoleOperator}} {
		if err := s.SetContextConnectionRepositories(t.Context(), actor, "grant", []string{"repository"}); err == nil {
			t.Fatal("agent widened its own consent")
		}
	}
}
