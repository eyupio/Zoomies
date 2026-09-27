package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func seedTwoStepUser(t *testing.T, s *Store, name string) *User {
	t.Helper()
	u := &User{Username: name, Role: RoleOperator, PasswordHash: "x"}
	if err := s.CreateUser(context.Background(), u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return u
}

// An enrolment that was started and never confirmed must ask nothing of its
// owner at sign-in, and a confirmed one must not be quietly replaced by a
// second "begin": that would swap a working authenticator out from under
// somebody without their password or a code.
func TestTwoStepIsOnlyOnOnceConfirmedAndCannotBeRestartedOverAWorkingOne(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	u := seedTwoStepUser(t, s, "ada")

	if _, err := s.GetTwoStep(ctx, u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an account that never enrolled has a row: %v", err)
	}
	if err := s.BeginTwoStep(ctx, u.ID, []byte("sealed-1")); err != nil {
		t.Fatalf("BeginTwoStep: %v", err)
	}
	// Starting again before confirming replaces the abandoned secret.
	if err := s.BeginTwoStep(ctx, u.ID, []byte("sealed-2")); err != nil {
		t.Fatalf("restarting an unconfirmed enrolment: %v", err)
	}
	ts, err := s.GetTwoStep(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ts.Enabled() || string(ts.SecretEnc) != "sealed-2" {
		t.Fatalf("pending enrolment = enabled %v secret %q, want off with the second secret", ts.Enabled(), ts.SecretEnc)
	}
	if ok, _ := s.AcceptTwoStepStep(ctx, u.ID, 10); ok {
		t.Fatal("a code was accepted for an enrolment nobody confirmed")
	}

	if err := s.ConfirmTwoStep(ctx, u.ID, 100, []string{"h1", "h2"}); err != nil {
		t.Fatalf("ConfirmTwoStep: %v", err)
	}
	if err := s.ConfirmTwoStep(ctx, u.ID, 101, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("confirming twice = %v, want ErrConflict", err)
	}
	if err := s.BeginTwoStep(ctx, u.ID, []byte("sealed-3")); !errors.Is(err, ErrConflict) {
		t.Fatalf("beginning over a working enrolment = %v, want ErrConflict", err)
	}
	enabled, err := s.TwoStepEnabledUsers(ctx)
	if err != nil || !enabled[u.ID] {
		t.Fatalf("TwoStepEnabledUsers = %v, %v", enabled, err)
	}
	if n, _ := s.RemainingRecoveryCodes(ctx, u.ID); n != 2 {
		t.Fatalf("remaining recovery codes = %d, want 2", n)
	}
}

// The replay guard is the database's, not the caller's: a code for a step at
// or before the last accepted one is refused, whoever asks.
func TestATimeStepIsAcceptedOnce(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	u := seedTwoStepUser(t, s, "ada")
	if err := s.BeginTwoStep(ctx, u.ID, []byte("sealed")); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmTwoStep(ctx, u.ID, 100, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		step int64
		want bool
	}{
		{100, false}, // the step the enrolment was confirmed with
		{99, false},  // an older one
		{101, true},
		{101, false}, // the same code again
		{103, true},
		{102, false}, // one that was skipped is behind now
	} {
		got, err := s.AcceptTwoStepStep(ctx, u.ID, tc.step)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("AcceptTwoStepStep(%d) = %v, want %v", tc.step, got, tc.want)
		}
	}
}

func TestARecoveryCodeIsSpentOnce(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	u := seedTwoStepUser(t, s, "ada")
	other := seedTwoStepUser(t, s, "grace")
	if err := s.ReplaceRecoveryCodes(ctx, u.ID, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		user, hash string
		want       bool
	}{
		{u.ID, "a", true},
		{u.ID, "a", false},
		{other.ID, "b", false}, // somebody else's code is not yours
		{u.ID, "nope", false},
		{u.ID, "b", true},
	} {
		got, err := s.UseRecoveryCode(ctx, tc.user, tc.hash)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("UseRecoveryCode(%s, %s) = %v, want %v", tc.user, tc.hash, got, tc.want)
		}
	}
	if err := s.ReplaceRecoveryCodes(ctx, u.ID, []string{"c"}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.RemainingRecoveryCodes(ctx, u.ID); n != 1 {
		t.Fatalf("after replacing, %d codes remain, want 1", n)
	}
	if ok, _ := s.UseRecoveryCode(ctx, u.ID, "b"); ok {
		t.Fatal("a code from the replaced set still works")
	}
}

func TestDeletingTwoStepTakesTheCodesAndPendingSignInsWithIt(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	u := seedTwoStepUser(t, s, "ada")
	if err := s.BeginTwoStep(ctx, u.ID, []byte("sealed")); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmTwoStep(ctx, u.ID, 1, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSignInChallenge(ctx, &SignInChallenge{TokenHash: "c", UserID: u.ID,
		Purpose: ChallengeVerify, ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	removed, err := s.DeleteTwoStep(ctx, u.ID)
	if err != nil || !removed {
		t.Fatalf("DeleteTwoStep = %v, %v", removed, err)
	}
	if removed, _ := s.DeleteTwoStep(ctx, u.ID); removed {
		t.Fatal("a second delete said it removed something")
	}
	if n, _ := s.RemainingRecoveryCodes(ctx, u.ID); n != 0 {
		t.Fatalf("%d recovery codes survived", n)
	}
	if _, err := s.GetSignInChallenge(ctx, "c"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a pending sign-in survived turning two-step off: %v", err)
	}
}

func TestASignInChallengeCountsItsFailuresAndIsPrunedOnceExpired(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })
	u := seedTwoStepUser(t, s, "ada")
	for _, c := range []*SignInChallenge{
		{TokenHash: "old", UserID: u.ID, Purpose: ChallengeVerify, ExpiresAt: now.Add(-time.Second)},
		{TokenHash: "live", UserID: u.ID, Purpose: ChallengeEnrol, IP: "192.0.2.1", ExpiresAt: now.Add(5 * time.Minute)},
	} {
		if err := s.CreateSignInChallenge(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	for want := 1; want <= 2; want++ {
		n, err := s.FailSignInChallenge(ctx, "live")
		if err != nil || n != want {
			t.Fatalf("FailSignInChallenge = %d, %v; want %d", n, err, want)
		}
	}
	if _, err := s.FailSignInChallenge(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failing a missing challenge = %v, want ErrNotFound", err)
	}
	if _, err := s.PruneSessions(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSignInChallenge(ctx, "old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an expired challenge survived pruning: %v", err)
	}
	c, err := s.GetSignInChallenge(ctx, "live")
	if err != nil {
		t.Fatalf("a live challenge was pruned: %v", err)
	}
	if c.Purpose != ChallengeEnrol || c.IP != "192.0.2.1" || c.Failures != 2 {
		t.Fatalf("challenge read back as %+v", c)
	}
}

// The upgrade every operator with accounts does: the migration adds tables
// and touches no row, so every existing account signs in exactly as before
// and can then turn two-step on.
func TestTheTwoStepMigrationAppliesToADatabaseWithAccountsInIt(t *testing.T) {
	ctx := context.Background()
	path := atSchemaBefore(t, "0057_two_step.sql")

	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users (id, username, role, password_hash, disabled, created_at)
			VALUES ('usr_ada','ada','admin','hash',0,1000)`,
		`INSERT INTO users (id, username, role, oidc_subject, disabled, created_at)
			VALUES ('usr_sso','sso','viewer','sub-1',0,2000)`,
		`INSERT INTO sessions (id, user_id, token_hash, created_at, expires_at)
			VALUES ('ses_a','usr_ada','cookie-hash',1000,9999999999999)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	s, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("upgrading: %v", err)
	}
	defer s.Close()

	if _, u, err := s.GetSessionByTokenHash(ctx, "cookie-hash"); err != nil || u.ID != "usr_ada" {
		t.Fatalf("an existing session did not survive the upgrade: %v", err)
	}
	for _, id := range []string{"usr_ada", "usr_sso"} {
		if _, err := s.GetTwoStep(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s came out of the upgrade with two-step state: %v", id, err)
		}
	}
	if err := s.BeginTwoStep(ctx, "usr_ada", []byte("sealed")); err != nil {
		t.Fatalf("an upgraded account cannot start enrolment: %v", err)
	}
	if err := s.ConfirmTwoStep(ctx, "usr_ada", 1, []string{"h"}); err != nil {
		t.Fatalf("an upgraded account cannot finish enrolment: %v", err)
	}
	if err := s.DeleteUser(ctx, "usr_ada"); err != nil {
		t.Fatalf("deleting an enrolled account: %v", err)
	}
	if err := s.IntegrityCheck(ctx); err != nil {
		t.Errorf("integrity check after the upgrade: %v", err)
	}
}
