package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/aicontext"
)

func contextFixture(t *testing.T, s *Store) (*AIContextRepository, *User, *OAuthGrant) {
	t.Helper()
	inst, _, _ := seedPool(t, s)
	r := &AIContextRepository{Key: aicontext.RepositoryKey{GitHubHost: "github.com", InstallationID: inst.ID, RepositoryID: 42}, FullName: "acme/widgets", Config: aicontext.DefaultConfig("main")}
	if err := s.CreateAIContextRepository(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	u := &User{Username: "reader", Role: RoleViewer}
	if err := s.CreateUser(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	c := &OAuthClient{Kind: OAuthClientAdmin, Name: "Claude", RedirectURIs: StringSlice{"https://claude.ai/callback"}}
	if err := s.CreateOAuthClient(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	req := &OAuthRequest{ClientID: c.ID, RedirectURI: "https://claude.ai/callback", Resource: "https://zoomies.test/mcp", ExpiresAt: s.Now().Add(time.Minute)}
	if err := s.CreateOAuthRequest(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	g := &OAuthGrant{ClientID: c.ID, UserID: u.ID, Role: RoleViewer, Scope: "mcp:read", Resource: req.Resource}
	code := &OAuthToken{TokenHash: "test-code", ExpiresAt: s.Now().Add(time.Minute)}
	if err := s.ApproveOAuthRequest(t.Context(), req.ID, g, code); err != nil {
		t.Fatal(err)
	}
	return r, u, g
}

func TestContextInstructionsAndBadgeAreAvailableAfterReloadingSavedSetup(t *testing.T) {
	s := newTestStore(t)
	r, _, _ := contextFixture(t, s)
	saved, err := s.GetAIContextRepository(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Instructions string `json:"instructions"`
		Badge        string `json:"badge_markdown"`
	}
	if err := json.Unmarshal(b, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Instructions != aicontext.AssistantInstructions(saved.Key, saved.FullName, saved.Config) || metadata.Badge != aicontext.BadgeMarkdown(saved.Key, saved.FullName) {
		t.Fatal("saved setup lost its shareable instructions or badge")
	}
}

func TestContextStartsUnavailableAndExistingConnectionsHaveNoSourceAccess(t *testing.T) {
	s := newTestStore(t)
	r, u, g := contextFixture(t, s)
	if r.Available {
		t.Fatal("draft claims verified access")
	}
	if err := s.SetAIContextAvailable(t.Context(), r.ID, true); err != nil {
		t.Fatal(err)
	}
	if allowed, err := s.AIContextUserAccess(t.Context(), r.ID, u.ID); err != nil || allowed {
		t.Fatalf("implicit user grant: %v %v", allowed, err)
	}
	if err := s.ReplaceAIContextMembers(t.Context(), r.ID, []string{u.ID}); err != nil {
		t.Fatal(err)
	}
	if allowed, err := s.AIContextConnectionAccess(t.Context(), r.ID, g.ID, u.ID); err != nil || allowed {
		t.Fatalf("existing MCP connection gained access: %v %v", allowed, err)
	}
	if err := s.ReplaceAIContextConnectionAccess(t.Context(), g.ID, u.ID, []string{r.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if allowed, err := s.AIContextConnectionAccess(t.Context(), r.ID, g.ID, u.ID); err != nil || !allowed {
		t.Fatalf("explicit access refused: %v %v", allowed, err)
	}
}

func TestMembershipRemovalRevokesConsentEvenIfThePersonIsAddedBack(t *testing.T) {
	s := newTestStore(t)
	r, u, g := contextFixture(t, s)
	s.SetAIContextAvailable(t.Context(), r.ID, true)
	s.ReplaceAIContextMembers(t.Context(), r.ID, []string{u.ID})
	s.ReplaceAIContextConnectionAccess(t.Context(), g.ID, u.ID, []string{r.ID}, nil)
	if err := s.ReplaceAIContextMembers(t.Context(), r.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceAIContextMembers(t.Context(), r.ID, []string{u.ID}); err != nil {
		t.Fatal(err)
	}
	if allowed, err := s.AIContextConnectionAccess(t.Context(), r.ID, g.ID, u.ID); err != nil || allowed {
		t.Fatalf("restored membership resurrected app consent: %v %v", allowed, err)
	}
}

func TestLiveRevocationAndRepositoryIsolationApplyToCachedIdentities(t *testing.T) {
	for _, what := range []string{"repository", "user", "grant", "client"} {
		t.Run(what, func(t *testing.T) {
			s := newTestStore(t)
			r, u, g := contextFixture(t, s)
			s.SetAIContextAvailable(t.Context(), r.ID, true)
			s.ReplaceAIContextMembers(t.Context(), r.ID, []string{u.ID})
			s.ReplaceAIContextConnectionAccess(t.Context(), g.ID, u.ID, []string{r.ID}, nil)
			if allowed, err := s.AIContextConnectionAccess(t.Context(), "other", g.ID, u.ID); err != nil || allowed {
				t.Fatal("cross-repository access")
			}
			if err := s.ReplaceAIContextConnectionAccess(t.Context(), g.ID, "another-user", nil, nil); !errors.Is(err, ErrNotFound) {
				t.Fatalf("another person modified grant: %v", err)
			}
			switch what {
			case "repository":
				s.SetAIContextAvailable(t.Context(), r.ID, false)
			case "user":
				u.Disabled = true
				s.UpdateUser(t.Context(), u)
			case "grant":
				s.RevokeOAuthGrant(t.Context(), g.ID, "test")
			case "client":
				s.RevokeOAuthClient(t.Context(), g.ClientID)
			}
			if allowed, err := s.AIContextConnectionAccess(t.Context(), r.ID, g.ID, u.ID); err != nil || allowed {
				t.Fatalf("revocation failed: %v %v", allowed, err)
			}
		})
	}
}

func TestFailedMembershipAndConsentUpdatesPreservePreviousSelections(t *testing.T) {
	s := newTestStore(t)
	r, u, g := contextFixture(t, s)
	s.SetAIContextAvailable(t.Context(), r.ID, true)
	s.ReplaceAIContextMembers(t.Context(), r.ID, []string{u.ID})
	s.ReplaceAIContextConnectionAccess(t.Context(), g.ID, u.ID, []string{r.ID}, nil)
	if err := s.ReplaceAIContextMembers(t.Context(), r.ID, []string{"missing"}); err == nil {
		t.Fatal("missing member accepted")
	}
	if err := s.ReplaceAIContextConnectionAccess(t.Context(), g.ID, u.ID, []string{r.ID, "missing"}, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing repository accepted: %v", err)
	}
	if allowed, err := s.AIContextConnectionAccess(t.Context(), r.ID, g.ID, u.ID); err != nil || !allowed {
		t.Fatal("failed update destroyed previous consent")
	}
}

func TestContextConfigurationHasStableIdentityAndRejectsStaleWizardWrites(t *testing.T) {
	s := newTestStore(t)
	r, _, _ := contextFixture(t, s)
	c := r.Config
	c.Destination = aicontext.Repository
	if err := s.SaveAIContextConfig(t.Context(), r.ID, r.Revision, c); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAIContextConfig(t.Context(), r.ID, r.Revision, r.Config); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale wizard update: %v", err)
	}
	got, err := s.GetAIContextRepository(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 2 || got.Config.Destination != aicontext.Repository || got.Key != r.Key {
		t.Fatal("configuration identity changed")
	}
	r.ID = ""
	if err := s.CreateAIContextRepository(t.Context(), r); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate identity: %v", err)
	}
}

func TestDraftConfigurationSurvivesRestartAndInstallationDeletionCascades(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zoomies.db")
	s, err := Open(t.Context(), Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	r, _, _ := contextFixture(t, s)
	s.Close()
	s, err = Open(t.Context(), Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := s.GetAIContextRepository(t.Context(), r.ID); err != nil || got.Config.SourceBranch != "main" {
		t.Fatalf("draft lost on restart: %v", err)
	}
	// Delete the fixture pool before its installation: its FK protects live fleet configuration.
	pools, err := s.ListPools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pools {
		if _, _, err := s.DeletePool(t.Context(), p.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DeleteInstallation(t.Context(), r.Key.InstallationID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAIContextRepository(t.Context(), r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("orphaned context config: %v", err)
	}
}

func TestContextRepositoryListsHaveBoundedPages(t *testing.T) {
	s := newTestStore(t)
	r, _, _ := contextFixture(t, s)
	rows, total, err := s.ListAIContextRepositories(t.Context(), 1, 0)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].ID != r.ID {
		t.Fatalf("first page: %+v %d %v", rows, total, err)
	}
	rows, total, err = s.ListAIContextRepositories(t.Context(), 1, 1)
	if err != nil || total != 1 || len(rows) != 0 || rows == nil {
		t.Fatalf("last page: %+v %d %v", rows, total, err)
	}
	for _, page := range [][2]int{{0, 0}, {101, 0}, {1, -1}} {
		if _, _, err := s.ListAIContextRepositories(t.Context(), page[0], page[1]); err == nil {
			t.Fatalf("accepted unbounded page: %v", page)
		}
	}
}

func TestConnectionSourceChoicesHideUnsharedAndUnavailableRepositories(t *testing.T) {
	s := newTestStore(t)
	r, u, g := contextFixture(t, s)
	s.SetAIContextAvailable(t.Context(), r.ID, true)
	s.ReplaceAIContextMembers(t.Context(), r.ID, []string{u.ID})
	makeRepo := func(id int64, name string, available, member bool) *AIContextRepository {
		t.Helper()
		other := &AIContextRepository{Key: r.Key, FullName: name, Config: r.Config}
		other.Key.RepositoryID = id
		if err := s.CreateAIContextRepository(t.Context(), other); err != nil {
			t.Fatal(err)
		}
		if available {
			s.SetAIContextAvailable(t.Context(), other.ID, true)
		}
		if member {
			s.ReplaceAIContextMembers(t.Context(), other.ID, []string{u.ID})
		}
		return other
	}
	second := makeRepo(43, "acme/z-last", true, true)
	makeRepo(44, "acme/private-unshared", true, false)
	makeRepo(45, "acme/pending", false, true)
	if err := s.ReplaceAIContextConnectionAccess(t.Context(), g.ID, u.ID, []string{r.ID, second.ID}, nil); err != nil {
		t.Fatal(err)
	}
	choices, err := s.AIContextConnectionChoices(t.Context(), g.ID, u.ID, 1, 0)
	if err != nil || choices.Total != 2 || len(choices.Items) != 1 || len(choices.SelectedRepositoryIDs) != 2 {
		t.Fatalf("choices = %+v, %v", choices, err)
	}
	page, err := s.AIContextConnectionChoices(t.Context(), g.ID, u.ID, 1, 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != second.ID || len(page.SelectedRepositoryIDs) != 2 {
		t.Fatalf("second page lost consent = %+v, %v", page, err)
	}
	if _, err := s.AIContextConnectionChoices(t.Context(), g.ID, "another-owner", 1, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another owner saw choices: %v", err)
	}
	if err := s.ReplaceAIContextMembers(t.Context(), r.ID, nil); err != nil {
		t.Fatal(err)
	}
	choices, err = s.AIContextConnectionChoices(t.Context(), g.ID, u.ID, 1, 0)
	if err != nil || choices.Total != 1 || len(choices.SelectedRepositoryIDs) != 1 || choices.SelectedRepositoryIDs[0] != second.ID {
		t.Fatalf("revoked membership still visible = %+v, %v", choices, err)
	}
}

func TestContextMetadataSearchAndDraftLookupKeepIdentityAndAccessBoundaries(t *testing.T) {
	s := newTestStore(t)
	r, u, _ := contextFixture(t, s)
	got, err := s.FindAIContextRepository(t.Context(), r.Key)
	if err != nil || got.ID != r.ID {
		t.Fatalf("lookup=%+v %v", got, err)
	}
	otherKey := r.Key
	otherKey.GitHubHost = "github.example"
	if _, err := s.FindAIContextRepository(t.Context(), otherKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-host lookup=%v", err)
	}
	rows, total, err := s.ListAIContextRepositoriesFiltered(t.Context(), 1, 0, r.Key.InstallationID, "widgets")
	if err != nil || len(rows) != 1 || total != 1 {
		t.Fatalf("filtered list=%+v %d %v", rows, total, err)
	}
	for _, search := range []string{"%", "_", "' OR 1=1 --", `\`} {
		rows, total, err = s.ListAIContextRepositoriesFiltered(t.Context(), 1, 0, r.Key.InstallationID, search)
		if err != nil || len(rows) != 0 || total != 0 {
			t.Fatalf("search %q broadened list: %+v %d %v", search, rows, total, err)
		}
	}
	s.SetAIContextAvailable(t.Context(), r.ID, true)
	visible, count, err := s.ListAIContextReaderRepositories(t.Context(), u.ID, 1, 0, "")
	if err != nil || len(visible) != 0 || count != 0 {
		t.Fatalf("implicit membership=%+v %d %v", visible, count, err)
	}
	s.ReplaceAIContextMembers(t.Context(), r.ID, []string{u.ID})
	visible, count, err = s.ListAIContextReaderRepositories(t.Context(), u.ID, 1, 0, "widgets")
	if err != nil || len(visible) != 1 || count != 1 {
		t.Fatalf("member list=%+v %d %v", visible, count, err)
	}
	u.Disabled = true
	s.UpdateUser(t.Context(), u)
	visible, count, err = s.ListAIContextReaderRepositories(t.Context(), u.ID, 1, 0, "")
	if err != nil || len(visible) != 0 || count != 0 {
		t.Fatalf("disabled reader=%+v %d %v", visible, count, err)
	}
}

func TestContextDiscoveryShowsOnlyTheConnectionsLiveConsent(t *testing.T) {
	s := newTestStore(t)
	r, u, g := contextFixture(t, s)
	if err := s.SetAIContextAvailable(t.Context(), r.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceAIContextMembers(t.Context(), r.ID, []string{u.ID}); err != nil {
		t.Fatal(err)
	}
	check := func(want int) {
		t.Helper()
		items, total, err := s.ListAIContextGrantedRepositories(t.Context(), g.ID, u.ID, 12, 0, "")
		if err != nil || total != want || len(items) != want {
			t.Fatalf("discovery %v %d %v", items, total, err)
		}
	}
	check(0)
	if err := s.ReplaceAIContextConnectionAccess(t.Context(), g.ID, u.ID, []string{r.ID}, nil); err != nil {
		t.Fatal(err)
	}
	check(1)
	items, total, err := s.ListAIContextGrantedRepositories(t.Context(), g.ID, "other-user", 12, 0, "")
	if err != nil || total != 0 || len(items) != 0 {
		t.Fatal("other user source names leaked")
	}
	if err := s.ReplaceAIContextMembers(t.Context(), r.ID, nil); err != nil {
		t.Fatal(err)
	}
	check(0)
	if err := s.ReplaceAIContextMembers(t.Context(), r.ID, []string{u.ID}); err != nil {
		t.Fatal(err)
	}
	check(0)
}

// The flag is an observation about GitHub, not an edit: moving it must not
// reject a wizard write that read the revision a moment earlier.
func TestWorkflowOutdatedIsRecordedWithoutMovingTheRevision(t *testing.T) {
	s := newTestStore(t)
	r, _, _ := contextFixture(t, s)
	ctx := t.Context()
	got, err := s.GetAIContextRepository(ctx, r.ID)
	if err != nil || got.WorkflowOutdated {
		t.Fatalf("a new repository starts current: %+v %v", got, err)
	}
	for _, want := range []bool{true, true, false} {
		if err := s.SetAIContextWorkflowOutdated(ctx, r.ID, want); err != nil {
			t.Fatal(err)
		}
		got, err = s.GetAIContextRepository(ctx, r.ID)
		if err != nil || got.WorkflowOutdated != want || got.Revision != r.Revision {
			t.Fatalf("after setting %v: outdated=%v revision=%d (was %d) %v", want, got.WorkflowOutdated, got.Revision, r.Revision, err)
		}
	}
}
