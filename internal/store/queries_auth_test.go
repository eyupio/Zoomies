package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestUserPreferencesArePrivateToTheAccount(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	alice := &User{Username: "alice", Role: RoleViewer}
	bob := &User{Username: "bob", Role: RoleViewer}
	for _, user := range []*User{alice, bob} {
		if err := s.CreateUser(ctx, user); err != nil {
			t.Fatalf("CreateUser(%s): %v", user.Username, err)
		}
	}
	value := json.RawMessage(`{"table_layouts":{"runners":{"widths":{"name":240}}}}`)
	if err := s.SetUserPreferences(ctx, alice.ID, value); err != nil {
		t.Fatalf("SetUserPreferences: %v", err)
	}
	got, err := s.UserPreferences(ctx, alice.ID)
	if err != nil {
		t.Fatalf("UserPreferences(alice): %v", err)
	}
	if string(got) != string(value) {
		t.Fatalf("alice preferences = %s, want %s", got, value)
	}
	got, err = s.UserPreferences(ctx, bob.ID)
	if err != nil {
		t.Fatalf("UserPreferences(bob): %v", err)
	}
	if string(got) != "{}" {
		t.Fatalf("bob preferences = %s, want an empty document", got)
	}
}

// Usernames are the thing a person types at a login prompt, and people do not
// type case consistently. Storing them lowercased is what makes "Ada" and "ada"
// the same account instead of two accounts one of which cannot be created.
func TestCreateUserLowercasesTheUsername(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	u := &User{Username: "  Ada  ", Email: "ada@example.com", Role: RoleAdmin}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.Username != "ada" {
		t.Fatalf("stored username = %q, want %q", u.Username, "ada")
	}
	if u.ID == "" {
		t.Fatal("CreateUser left the ID empty")
	}

	got, err := s.GetUserByUsername(ctx, "ADA")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if got.ID != u.ID {
		t.Fatalf("GetUserByUsername returned %s, want %s", got.ID, u.ID)
	}
}

func TestGetUserReportsNotFoundForAnUnknownID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.GetUser(ctx, "usr_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetUser error = %v, want ErrNotFound", err)
	}
	if _, err := s.GetUserByUsername(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetUserByUsername error = %v, want ErrNotFound", err)
	}
	if _, err := s.GetUserByOIDCSubject(ctx, "sub-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetUserByOIDCSubject error = %v, want ErrNotFound", err)
	}
}

// An empty oidc_subject is what every password account carries, so a lookup for
// "" must not match one of them and hand a caller somebody else's account.
func TestGetUserByOIDCSubjectIgnoresAccountsWithoutOne(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	local := &User{Username: "local", Role: RoleViewer, PasswordHash: "hash"}
	if err := s.CreateUser(ctx, local); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	federated := &User{Username: "fed", Role: RoleOperator, OIDCSubject: "sub-1"}
	if err := s.CreateUser(ctx, federated); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if _, err := s.GetUserByOIDCSubject(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty subject matched an account: %v", err)
	}
	got, err := s.GetUserByOIDCSubject(ctx, "sub-1")
	if err != nil {
		t.Fatalf("GetUserByOIDCSubject: %v", err)
	}
	if got.ID != federated.ID {
		t.Fatalf("got %s, want %s", got.ID, federated.ID)
	}
}

func TestListUsersOrdersByUsername(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for _, name := range []string{"zoe", "ada", "mel"} {
		if err := s.CreateUser(ctx, &User{Username: name, Role: RoleViewer}); err != nil {
			t.Fatalf("CreateUser(%s): %v", name, err)
		}
	}
	users, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	var got []string
	for _, u := range users {
		got = append(got, u.Username)
	}
	want := []string{"ada", "mel", "zoe"}
	if len(got) != len(want) {
		t.Fatalf("ListUsers returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListUsers returned %v, want %v", got, want)
		}
	}
}

// CountAdmins is what stops the API deleting or demoting the last administrator,
// so a disabled admin must not count -- an account nobody can log into is not
// somebody who can still fix the mistake.
func TestCountAdminsIgnoresDisabledAccounts(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	n, err := s.CountUsers(ctx)
	if err != nil || n != 0 {
		t.Fatalf("CountUsers on a fresh store = %d, %v; want 0, nil", n, err)
	}

	live := &User{Username: "live", Role: RoleAdmin}
	off := &User{Username: "off", Role: RoleAdmin, Disabled: true}
	viewer := &User{Username: "viewer", Role: RoleViewer}
	for _, u := range []*User{live, off, viewer} {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
	}

	if n, err := s.CountUsers(ctx); err != nil || n != 3 {
		t.Fatalf("CountUsers = %d, %v; want 3, nil", n, err)
	}
	if n, err := s.CountAdmins(ctx); err != nil || n != 1 {
		t.Fatalf("CountAdmins = %d, %v; want 1, nil", n, err)
	}
}

func TestUpdateUserPersistsProfileAndRole(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	u := &User{Username: "ada", Role: RoleViewer, MustChangePassword: true}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	login := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)
	u.Username = "  ADA.L  "
	u.Email = "ada@example.com"
	u.DisplayName = "Ada L"
	u.Role = RoleAdmin
	u.Disabled = true
	u.MustChangePassword = false
	u.LastLoginAt = &login
	if err := s.UpdateUser(ctx, u); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}

	got, err := s.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.Username != "ada.l" {
		t.Fatalf("username = %q, want %q", got.Username, "ada.l")
	}
	if got.Role != RoleAdmin || !got.Disabled || got.MustChangePassword {
		t.Fatalf("role/flags not persisted: %+v", got)
	}
	if got.LastLoginAt == nil || !got.LastLoginAt.Equal(login) {
		t.Fatalf("last_login_at = %v, want %v", got.LastLoginAt, login)
	}
	if got.DisplayName != "Ada L" || got.Email != "ada@example.com" {
		t.Fatalf("profile not persisted: %+v", got)
	}
}

func TestUpdateUserReportsNotFoundForAnUnknownID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	err := s.UpdateUser(ctx, &User{ID: "usr_missing", Username: "ghost", Role: RoleViewer})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateUser error = %v, want ErrNotFound", err)
	}
	if err := s.SetPassword(ctx, "usr_missing", "hash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetPassword error = %v, want ErrNotFound", err)
	}
	if err := s.DeleteUser(ctx, "usr_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteUser error = %v, want ErrNotFound", err)
	}
}

// The bootstrap admin is created with must_change_password set; choosing a
// password is the act that clears it, so the two have to move together.
func TestSetPasswordClearsTheChangeOnLoginFlag(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	u := &User{Username: "ada", Role: RoleAdmin, PasswordHash: "old", MustChangePassword: true}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := s.SetPassword(ctx, u.ID, "new"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	got, err := s.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.PasswordHash != "new" {
		t.Fatalf("password hash = %q, want %q", got.PasswordHash, "new")
	}
	if got.MustChangePassword {
		t.Fatal("SetPassword left must_change_password set")
	}
}

func TestTouchLoginRecordsTheMoment(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	u := &User{Username: "ada", Role: RoleAdmin}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if got, err := s.GetUser(ctx, u.ID); err != nil || got.LastLoginAt != nil {
		t.Fatalf("a new account already has a login time: %v, %v", got, err)
	}

	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := s.TouchLogin(ctx, u.ID, now); err != nil {
		t.Fatalf("TouchLogin: %v", err)
	}
	got, err := s.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.LastLoginAt == nil || !got.LastLoginAt.Equal(now) {
		t.Fatalf("last_login_at = %v, want %v", got.LastLoginAt, now)
	}
}

// Deleting an account has to take its sessions with it, or a departed user's
// browser keeps working against a row that is gone.
func TestDeleteUserCascadesToSessions(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	u := &User{Username: "ada", Role: RoleAdmin}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	sess := &Session{UserID: u.ID, TokenHash: "hash-1", ExpiresAt: s.Now().Add(time.Hour)}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, _, err := s.GetSessionByTokenHash(ctx, "hash-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session outlived its user: %v", err)
	}
}

// Resolving a cookie is on the path of every authenticated request, so it
// returns the session and its user together rather than costing two queries.
func TestGetSessionByTokenHashReturnsTheUserToo(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	u := &User{Username: "ada", Role: RoleOperator, DisplayName: "Ada"}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	expires := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	sess := &Session{UserID: u.ID, TokenHash: "hash-1", UserAgent: "curl", IP: "10.0.0.1", ExpiresAt: expires}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.ID == "" {
		t.Fatal("CreateSession left the ID empty")
	}

	gotSess, gotUser, err := s.GetSessionByTokenHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("GetSessionByTokenHash: %v", err)
	}
	if gotSess.ID != sess.ID || gotSess.UserAgent != "curl" || gotSess.IP != "10.0.0.1" {
		t.Fatalf("session round-tripped wrong: %+v", gotSess)
	}
	if !gotSess.ExpiresAt.Equal(expires) {
		t.Fatalf("expires_at = %v, want %v", gotSess.ExpiresAt, expires)
	}
	if gotUser.ID != u.ID || gotUser.Role != RoleOperator || gotUser.DisplayName != "Ada" {
		t.Fatalf("user round-tripped wrong: %+v", gotUser)
	}

	if _, _, err := s.GetSessionByTokenHash(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown hash error = %v, want ErrNotFound", err)
	}
}

func TestDeleteSessionLogsOutOneBrowserAndDeleteUserSessionsLogsOutAll(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	u := &User{Username: "ada", Role: RoleAdmin}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	for _, h := range []string{"a", "b", "c"} {
		if err := s.CreateSession(ctx, &Session{UserID: u.ID, TokenHash: h,
			ExpiresAt: s.Now().Add(time.Hour)}); err != nil {
			t.Fatalf("CreateSession(%s): %v", h, err)
		}
	}

	if err := s.DeleteSession(ctx, "a"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, _, err := s.GetSessionByTokenHash(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session a survived: %v", err)
	}
	if _, _, err := s.GetSessionByTokenHash(ctx, "b"); err != nil {
		t.Fatalf("session b should have survived: %v", err)
	}

	// Deleting a session that is not there is how a stale cookie logs out; it
	// is not an error the caller has to handle.
	if err := s.DeleteSession(ctx, "gone"); err != nil {
		t.Fatalf("DeleteSession on a missing row: %v", err)
	}

	if err := s.DeleteUserSessions(ctx, u.ID); err != nil {
		t.Fatalf("DeleteUserSessions: %v", err)
	}
	for _, h := range []string{"b", "c"} {
		if _, _, err := s.GetSessionByTokenHash(ctx, h); !errors.Is(err, ErrNotFound) {
			t.Fatalf("session %s survived a log-out-everywhere: %v", h, err)
		}
	}
}

func TestPruneSessionsRemovesOnlyExpiredOnes(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	u := &User{Username: "ada", Role: RoleAdmin}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.CreateSession(ctx, &Session{UserID: u.ID, TokenHash: "old",
		ExpiresAt: now.Add(-time.Hour)}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.CreateSession(ctx, &Session{UserID: u.ID, TokenHash: "live",
		ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	n, err := s.PruneSessions(ctx, now)
	if err != nil {
		t.Fatalf("PruneSessions: %v", err)
	}
	if n != 1 {
		t.Fatalf("PruneSessions removed %d, want 1", n)
	}
	if _, _, err := s.GetSessionByTokenHash(ctx, "live"); err != nil {
		t.Fatalf("the live session was pruned: %v", err)
	}
}

func TestAPITokensRoundTripAndAuthenticateByHash(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	expires := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	tok := &APIToken{Name: "ci", Role: RoleOperator, Scopes: StringSlice{"pools:read"},
		TokenHash: "hash-1", Prefix: "zmt_abc", ExpiresAt: &expires}
	if err := s.CreateAPIToken(ctx, tok); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if tok.ID == "" {
		t.Fatal("CreateAPIToken left the ID empty")
	}

	got, err := s.GetAPITokenByHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("GetAPITokenByHash: %v", err)
	}
	if got.ID != tok.ID || got.Name != "ci" || got.Role != RoleOperator || got.Prefix != "zmt_abc" {
		t.Fatalf("token round-tripped wrong: %+v", got)
	}
	if len(got.Scopes) != 1 || got.Scopes[0] != "pools:read" {
		t.Fatalf("scopes = %v, want [pools:read]", got.Scopes)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) {
		t.Fatalf("expires_at = %v, want %v", got.ExpiresAt, expires)
	}
	if got.LastUsedAt != nil {
		t.Fatalf("a fresh token already has a last-used time: %v", got.LastUsedAt)
	}

	if _, err := s.GetAPITokenByHash(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown hash error = %v, want ErrNotFound", err)
	}
}

func TestTouchAPITokenRecordsTheMoment(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	tok := &APIToken{Name: "ci", Role: RoleViewer, TokenHash: "hash-1"}
	if err := s.CreateAPIToken(ctx, tok); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	now := time.Date(2025, 5, 5, 5, 5, 5, 0, time.UTC)
	if err := s.TouchAPIToken(ctx, tok.ID, now); err != nil {
		t.Fatalf("TouchAPIToken: %v", err)
	}
	got, err := s.GetAPITokenByHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("GetAPITokenByHash: %v", err)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(now) {
		t.Fatalf("last_used_at = %v, want %v", got.LastUsedAt, now)
	}
}

// Revoking keeps the row, because the audit trail is the point: a token that
// did something needs to stay nameable after it is turned off.
func TestRevokeAPITokenKeepsTheRow(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	tok := &APIToken{Name: "ci", Role: RoleViewer, TokenHash: "hash-1"}
	if err := s.CreateAPIToken(ctx, tok); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if err := s.RevokeAPIToken(ctx, tok.ID); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}
	got, err := s.GetAPITokenByHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("GetAPITokenByHash: %v", err)
	}
	if !got.Revoked {
		t.Fatal("RevokeAPIToken did not set revoked")
	}

	if err := s.RevokeAPIToken(ctx, "tok_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RevokeAPIToken on a missing row = %v, want ErrNotFound", err)
	}
	if err := s.DeleteAPIToken(ctx, "tok_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteAPIToken on a missing row = %v, want ErrNotFound", err)
	}

	if err := s.DeleteAPIToken(ctx, tok.ID); err != nil {
		t.Fatalf("DeleteAPIToken: %v", err)
	}
	if _, err := s.GetAPITokenByHash(ctx, "hash-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted token still resolves: %v", err)
	}
}

// A token is a bearer credential with its own role: nothing about the owning
// account is consulted when one is presented, so disabling an account has to
// revoke its tokens explicitly or a departed administrator keeps their access.
// Tokens with no owner belong to nobody and must be left alone.
func TestRevokeAPITokensForUserSparesTokensWithNoOwner(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	u := &User{Username: "ada", Role: RoleAdmin}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	mine := &APIToken{Name: "mine", Role: RoleAdmin, UserID: u.ID, TokenHash: "h-mine"}
	also := &APIToken{Name: "also", Role: RoleViewer, UserID: u.ID, TokenHash: "h-also"}
	orphan := &APIToken{Name: "orphan", Role: RoleViewer, TokenHash: "h-orphan"}
	for _, tok := range []*APIToken{mine, also, orphan} {
		if err := s.CreateAPIToken(ctx, tok); err != nil {
			t.Fatalf("CreateAPIToken(%s): %v", tok.Name, err)
		}
	}

	n, err := s.RevokeAPITokensForUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("RevokeAPITokensForUser: %v", err)
	}
	if n != 2 {
		t.Fatalf("revoked %d tokens, want 2", n)
	}
	got, err := s.GetAPITokenByHash(ctx, "h-orphan")
	if err != nil {
		t.Fatalf("GetAPITokenByHash: %v", err)
	}
	if got.Revoked {
		t.Fatal("a token with no owner was revoked along with the account's")
	}

	// Already-revoked tokens are not revoked twice, so the count stays honest.
	if n, err := s.RevokeAPITokensForUser(ctx, u.ID); err != nil || n != 0 {
		t.Fatalf("second pass revoked %d, %v; want 0, nil", n, err)
	}
	// An empty user ID is "no owner", not "every token".
	if n, err := s.RevokeAPITokensForUser(ctx, ""); err != nil || n != 0 {
		t.Fatalf("empty owner revoked %d, %v; want 0, nil", n, err)
	}
	if got, err := s.GetAPITokenByHash(ctx, "h-orphan"); err != nil || got.Revoked {
		t.Fatalf("the orphan token was revoked by an empty owner: %v, %v", got, err)
	}
}

func TestListAPITokensReturnsNewestFirst(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })

	for _, name := range []string{"first", "second", "third"} {
		if err := s.CreateAPIToken(ctx, &APIToken{Name: name, Role: RoleViewer,
			TokenHash: "h-" + name}); err != nil {
			t.Fatalf("CreateAPIToken(%s): %v", name, err)
		}
		now = now.Add(time.Minute)
	}

	tokens, err := s.ListAPITokens(ctx)
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	want := []string{"third", "second", "first"}
	if len(tokens) != len(want) {
		t.Fatalf("ListAPITokens returned %d tokens, want %d", len(tokens), len(want))
	}
	for i, name := range want {
		if tokens[i].Name != name {
			t.Fatalf("token %d = %q, want %q", i, tokens[i].Name, name)
		}
	}
}

func TestJoinTokensRoundTripAndListNewestFirst(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })

	expires := now.Add(time.Hour)
	first := &JoinToken{TokenHash: "h-first", Prefix: "zmj_a", CreatedBy: "usr_1",
		Labels: StringMap{"zone": "eu"}, Capacity: 4, ExpiresAt: expires}
	if err := s.CreateJoinToken(ctx, first); err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}
	now = now.Add(time.Minute)
	second := &JoinToken{TokenHash: "h-second", Prefix: "zmj_b", CreatedBy: "usr_1",
		Capacity: 2, ExpiresAt: expires}
	if err := s.CreateJoinToken(ctx, second); err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}

	got, err := s.GetJoinToken(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetJoinToken: %v", err)
	}
	if got.Capacity != 4 || got.Prefix != "zmj_a" || got.CreatedBy != "usr_1" {
		t.Fatalf("join token round-tripped wrong: %+v", got)
	}
	if got.Labels["zone"] != "eu" {
		t.Fatalf("labels = %v, want zone=eu", got.Labels)
	}
	if got.UsedAt != nil || got.UsedByID != "" {
		t.Fatalf("a fresh join token is already spent: %+v", got)
	}

	if _, err := s.GetJoinToken(ctx, "join_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetJoinToken error = %v, want ErrNotFound", err)
	}

	list, err := s.ListJoinTokens(ctx)
	if err != nil {
		t.Fatalf("ListJoinTokens: %v", err)
	}
	if len(list) != 2 || list[0].ID != second.ID {
		t.Fatalf("ListJoinTokens returned %d tokens, newest %v", len(list), list[0].ID)
	}
}

// The link between the credential an operator handed out and the machine that
// used it is used_by_id, and redeeming is what writes it. A second attempt has
// to fail, which is what "single-use" means.
func TestRedeemJoinTokenIsSingleUse(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })

	tok := &JoinToken{TokenHash: "h-1", Prefix: "zmj_a", ExpiresAt: now.Add(time.Hour)}
	if err := s.CreateJoinToken(ctx, tok); err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}

	redeemed, err := s.RedeemJoinToken(ctx, "h-1", JoinClaim{HostID: "hst_1"}, now)
	if err != nil {
		t.Fatalf("RedeemJoinToken: %v", err)
	}
	if redeemed.UsedByID != "hst_1" || redeemed.UsedAt == nil || !redeemed.UsedAt.Equal(now) {
		t.Fatalf("redeemed token = %+v", redeemed)
	}

	if _, err := s.RedeemJoinToken(ctx, "h-1", JoinClaim{HostID: "hst_2"}, now); !errors.Is(err, ErrJoinTokenUsed) {
		t.Fatalf("second redemption error = %v, want ErrJoinTokenUsed", err)
	}
	stored, err := s.GetJoinToken(ctx, tok.ID)
	if err != nil {
		t.Fatalf("GetJoinToken: %v", err)
	}
	if stored.UsedByID != "hst_1" {
		t.Fatalf("the second attempt overwrote used_by_id: %q", stored.UsedByID)
	}
}

func TestRedeemJoinTokenRefusesAnExpiredOrUnknownToken(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })

	tok := &JoinToken{TokenHash: "h-1", ExpiresAt: now.Add(time.Minute)}
	if err := s.CreateJoinToken(ctx, tok); err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}

	if _, err := s.RedeemJoinToken(ctx, "h-1", JoinClaim{HostID: "hst_1"}, now.Add(time.Hour)); !errors.Is(err, ErrJoinTokenExpired) {
		t.Fatalf("expired redemption error = %v, want ErrJoinTokenExpired", err)
	}
	if _, err := s.RedeemJoinToken(ctx, "nope", JoinClaim{HostID: "hst_1"}, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown redemption error = %v, want ErrNotFound", err)
	}
}

func TestPruneJoinTokensSparesSpentAndLiveOnes(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })

	stale := &JoinToken{TokenHash: "h-stale", ExpiresAt: now.Add(-time.Hour)}
	live := &JoinToken{TokenHash: "h-live", ExpiresAt: now.Add(time.Hour)}
	spent := &JoinToken{TokenHash: "h-spent", ExpiresAt: now.Add(time.Minute)}
	for _, tok := range []*JoinToken{stale, live, spent} {
		if err := s.CreateJoinToken(ctx, tok); err != nil {
			t.Fatalf("CreateJoinToken: %v", err)
		}
	}
	if _, err := s.RedeemJoinToken(ctx, "h-spent", JoinClaim{HostID: "hst_1"}, now); err != nil {
		t.Fatalf("RedeemJoinToken: %v", err)
	}

	// A spent token is expired by the time this runs, but it is the record of
	// which host joined with which credential, so pruning must leave it.
	n, err := s.PruneJoinTokens(ctx, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("PruneJoinTokens: %v", err)
	}
	if n != 2 {
		t.Fatalf("PruneJoinTokens removed %d, want 2", n)
	}
	if _, err := s.GetJoinToken(ctx, spent.ID); err != nil {
		t.Fatalf("a spent token was pruned: %v", err)
	}
	if _, err := s.GetJoinToken(ctx, stale.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the stale token survived: %v", err)
	}
}

func TestDeleteJoinTokenRevokesAnUnusedOne(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	tok := &JoinToken{TokenHash: "h-1", ExpiresAt: s.Now().Add(time.Hour)}
	if err := s.CreateJoinToken(ctx, tok); err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}
	if err := s.DeleteJoinToken(ctx, tok.ID); err != nil {
		t.Fatalf("DeleteJoinToken: %v", err)
	}
	if _, err := s.GetJoinToken(ctx, tok.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted join token still resolves: %v", err)
	}
	if err := s.DeleteJoinToken(ctx, tok.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteJoinToken on a missing row = %v, want ErrNotFound", err)
	}
}

// The settings list is handed straight to an API handler, so a secret's value
// must not be in it -- the key and the fact that it is set are what an operator
// needs to see.
func TestListSettingsBlanksSecretValues(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.SetSetting(ctx, "ui.theme", "dark", false); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := s.SetSetting(ctx, "github.webhook_secret", "hunter2", true); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	got, err := s.ListSettings(ctx)
	if err != nil {
		t.Fatalf("ListSettings: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListSettings returned %d rows, want 2", len(got))
	}
	// Ordered by key, so the secret comes first.
	if got[0].Key != "github.webhook_secret" || !got[0].Secret || got[0].Value != "" {
		t.Fatalf("secret row = %+v, want a blanked value", got[0])
	}
	if got[1].Key != "ui.theme" || got[1].Secret || got[1].Value != "dark" {
		t.Fatalf("plain row = %+v", got[1])
	}
	if got[1].UpdatedAt.IsZero() {
		t.Fatal("updated_at was not recorded")
	}

	// The value itself is still readable through the single-key accessor, which
	// is what the controller uses.
	if v, err := s.GetSetting(ctx, "github.webhook_secret"); err != nil || v != "hunter2" {
		t.Fatalf("GetSetting = %q, %v; want hunter2, nil", v, err)
	}
}

func TestSetSettingUpsertsAndDeleteRemoves(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if v, err := s.GetSetting(ctx, "absent"); err != nil || v != "" {
		t.Fatalf("GetSetting on an unset key = %q, %v; want \"\", nil", v, err)
	}

	if err := s.SetSetting(ctx, "k", "one", false); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := s.SetSetting(ctx, "k", "two", true); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if v, err := s.GetSetting(ctx, "k"); err != nil || v != "two" {
		t.Fatalf("GetSetting = %q, %v; want two, nil", v, err)
	}
	rows, err := s.ListSettings(ctx)
	if err != nil {
		t.Fatalf("ListSettings: %v", err)
	}
	if len(rows) != 1 || !rows[0].Secret {
		t.Fatalf("upsert produced %d rows (%+v), want one secret row", len(rows), rows)
	}

	if err := s.DeleteSetting(ctx, "k"); err != nil {
		t.Fatalf("DeleteSetting: %v", err)
	}
	if v, err := s.GetSetting(ctx, "k"); err != nil || v != "" {
		t.Fatalf("GetSetting after delete = %q, %v", v, err)
	}
}

// Deleting is tidying up after a revocation: a token that still works must
// never vanish from the list, so the store refuses it rather than trusting
// every caller to have checked.
func TestDeleteAPITokenRefusesATokenThatStillWorks(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	s := newTestStoreAt(t, func() time.Time { return now })
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)

	cases := []struct {
		name    string
		revoked bool
		expires *time.Time
		want    error
	}{
		{name: "active, never expires", want: ErrConflict},
		{name: "active, expires later", expires: &future, want: ErrConflict},
		{name: "revoked", revoked: true},
		{name: "expired", expires: &past},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := &APIToken{Name: tc.name, Role: RoleViewer, TokenHash: fmt.Sprintf("h-%d", i), Revoked: tc.revoked, ExpiresAt: tc.expires}
			if err := s.CreateAPIToken(ctx, tok); err != nil {
				t.Fatalf("CreateAPIToken: %v", err)
			}
			err := s.DeleteAPIToken(ctx, tok.ID)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("DeleteAPIToken = %v, want %v", err, tc.want)
			}
		})
	}
}
