package store

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEverySealedColumnAndEncodedSettingChangesTogether(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for _, query := range []string{
		`INSERT INTO installations (id,app_id,installation_id,target,target_type,created_at,updated_at,private_key_enc,webhook_secret_enc) VALUES ('i',1,1,'team','org',1,1,X'61',X'61')`,
		`INSERT INTO providers (id,kind,name,created_at,updated_at,credentials_enc,tailcat_address_enc) VALUES ('p','fake','provider',1,1,X'61',X'61')`,
		`INSERT INTO backup_remotes (id,name,endpoint,bucket,created_at,updated_at,secret_key_enc,passphrase_enc) VALUES ('b','remote','https://example.test','b',1,1,X'61',X'61')`,
		`INSERT INTO users (id,username,role,password_hash,created_at) VALUES ('u','alice','admin','hash',1)`,
		`INSERT INTO user_two_step (user_id,secret_enc,created_at) VALUES ('u',X'61',1)`,
		`INSERT INTO provider_setups (id,token_hash,expires_at,payload_enc) VALUES ('s',X'61',1,X'61')`,
		`INSERT INTO assistant_providers (id,name,kind,model,key_enc,created_at,updated_at) VALUES ('a','model','openai','model',X'61',1,1)`,
	} {
		if _, err := s.exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutInstanceSettings(ctx, "test", []InstanceSetting{{Key: "github.webhook_secret", Value: base64.StdEncoding.EncodeToString([]byte("a")), Secret: true}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "tailcat.identity.v1", base64.StdEncoding.EncodeToString([]byte("a")), true); err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err := s.TransformSecrets(ctx, func(b []byte) ([]byte, error) {
		calls++
		if string(b) != "a" {
			t.Fatalf("opened %q", b)
		}
		return []byte("b"), nil
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 11 {
		t.Fatalf("converted %d secrets, want 11", calls)
	}
	for table, cols := range secretColumns {
		for _, col := range cols {
			var got []byte
			if err := s.read.QueryRowContext(ctx, "SELECT "+quoteTransferIdentifier(col)+" FROM "+quoteTransferIdentifier(table)).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if string(got) != "b" {
				t.Fatalf("%s.%s remains %q", table, col, got)
			}
		}
	}
	rows, err := s.InstanceSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Value != base64.StdEncoding.EncodeToString([]byte("b")) {
		t.Fatal("encoded secret did not change")
	}
}

func TestAnUnknownSealedColumnRefusesTransferBeforeChangingAnything(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if _, err := s.exec(ctx, `CREATE TABLE future_secrets (id TEXT, new_enc BLOB)`); err != nil {
		t.Fatal(err)
	}
	calls := 0
	err := s.TransformSecrets(ctx, func(b []byte) ([]byte, error) { calls++; return b, nil })
	if err == nil || calls != 0 {
		t.Fatalf("unknown secret accepted: calls=%d err=%v", calls, err)
	}
}

func TestASecretConversionFailureRollsBackTheWholeSnapshot(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for _, key := range []string{"a", "b"} {
		if err := s.PutInstanceSettings(ctx, "test", []InstanceSetting{{Key: key, Value: "YQ==", Secret: true}}); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	err := s.TransformSecrets(ctx, func(b []byte) ([]byte, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("cannot open")
		}
		return []byte("changed"), nil
	})
	if err == nil {
		t.Fatal("conversion succeeded")
	}
	rows, err := s.InstanceSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Value != "YQ==" {
			t.Fatalf("half-converted row: %+v", row)
		}
	}
}

func TestAnExportOmitsOperatorSecretsButRetainsTeamCredentialsAndHistory(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	operator := &User{Username: "operator", Role: Roles()[len(Roles())-1], PasswordHash: "operator password"}
	team := &User{Username: "team", Role: RoleAdmin, PasswordHash: "team password"}
	for _, u := range []*User{operator, team} {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		if err := s.BeginTwoStep(ctx, u.ID, []byte("sealed authenticator")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.exec(ctx, `INSERT INTO backup_remotes (id,name,endpoint,bucket,created_at,updated_at,secret_key_enc,passphrase_enc) VALUES ('b','remote','https://example.test','b',1,1,X'61',X'61')`); err != nil {
		t.Fatal(err)
	}
	if err := s.PutInstanceSettings(ctx, "test", []InstanceSetting{{Key: "process.secret", Value: "secret", Secret: true}, {Key: "fleet.secret", Value: "fleet", Secret: true}}); err != nil {
		t.Fatal(err)
	}
	if err := s.StripTransferOperatorSecrets(ctx, []string{"process.secret"}); err != nil {
		t.Fatal(err)
	}
	op, err := s.GetUser(ctx, operator.ID)
	if err != nil || op.PasswordHash != "" || !op.Disabled || op.Role != operator.Role {
		t.Fatal("operator secret or historical authority marker changed", err)
	}
	u, err := s.GetUser(ctx, team.ID)
	if err != nil || u.PasswordHash != team.PasswordHash || u.Disabled {
		t.Fatal("team credentials changed", err)
	}
	step, err := s.GetTwoStep(ctx, team.ID)
	if err != nil || string(step.SecretEnc) != "sealed authenticator" {
		t.Fatal("team authenticator changed", err)
	}
	var retained, secrets int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(secret_key_enc)+COUNT(passphrase_enc) FROM backup_remotes`).Scan(&retained, &secrets); err != nil {
		t.Fatal(err)
	}
	if retained != 1 || secrets != 0 {
		t.Fatal("source backup secrets travelled or history lost")
	}
	rows, err := s.InstanceSettings(ctx)
	if err != nil || len(rows) != 1 || rows[0].Key != "fleet.secret" {
		t.Fatal("wrong settings omitted", err)
	}
}

// GitHub reports every job in an installed repository, and on an organisation
// that also uses hosted runners most of them run somewhere this controller
// cannot drain. Counting them kept a transfer "preparing" for as long as
// anybody else was busy, with running jobs it could neither hurry nor stop.
func TestTransferPreparationWaitsOnlyForJobsOnThisFleetsRunners(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, host := seedPool(t, s)
	if err := s.SetSetting(ctx, SettingTransferDraining, "true", false); err != nil {
		t.Fatal(err)
	}
	for _, j := range []*Job{
		{GitHubJobID: 1, Repo: "acme/widgets", JobName: "hosted", State: JobInProgress, Labels: StringSlice{"ubuntu-latest"}},
		{GitHubJobID: 2, Repo: "acme/widgets", JobName: "elsewhere", State: JobInProgress, Labels: StringSlice{"self-hosted", "macos-studio"}},
	} {
		if _, err := s.UpsertJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.TransferProgress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.ActiveJobs != 0 || !p.Quiescent() {
		t.Fatalf("jobs that run elsewhere held the transfer: %+v", p)
	}
	if err := s.TransferReady(ctx); err == nil {
		// The fence is down, so not ready; but the refusal must be about the
		// fence and not about work in flight.
		t.Fatal("an unfenced instance was called ready")
	} else if !strings.Contains(err.Error(), "fence") {
		t.Fatalf("refused for the wrong reason: %v", err)
	}

	// A job on one of this fleet's live runners is the one to wait for.
	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: NewRunnerName(pool), State: RunnerBusy}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertJob(ctx, &Job{GitHubJobID: 3, Repo: "acme/widgets", JobName: "ours", State: JobInProgress, Labels: StringSlice{"self-hosted", "linux-x64"}, RunnerID: r.ID, RunnerName: r.Name}); err != nil {
		t.Fatal(err)
	}
	p, err = s.TransferProgress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.ActiveJobs != 1 || len(p.Waiting) == 0 {
		t.Fatalf("a job on this fleet's runner was not waited for: %+v", p)
	}
	var job *TransferWait
	for i := range p.Waiting {
		if p.Waiting[i].Kind == TransferWaitJob {
			job = &p.Waiting[i]
		}
	}
	if job == nil || job.Name != "ours" || job.Repo != "acme/widgets" || job.HostName != "vm-1" {
		t.Fatalf("the waiting job is not named with its host: %+v", p.Waiting)
	}
}

// "8 awaiting cleanup" says nothing an operator can act on. Each runner that
// holds the transfer is named, with the side still to confirm it gone and
// whether the host that has to do so is even sending heartbeats.
func TestTransferPreparationNamesEachRunnerAwaitingCleanupAndWhichSideIsOutstanding(t *testing.T) {
	now := time.Date(2026, 10, 10, 18, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })
	ctx := context.Background()
	_, pool, host := seedPool(t, s)
	silent := &Host{Name: "vm-silent", Capacity: 4, Backends: StringSlice{"docker"}}
	if err := s.CreateHost(ctx, silent); err != nil {
		t.Fatal(err)
	}
	// Five minutes on, one host has checked in and the other has not.
	now = now.Add(5 * time.Minute)
	if err := s.Heartbeat(ctx, host.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, SettingTransferDraining, "true", false); err != nil {
		t.Fatal(err)
	}
	mk := func(name string, h *Host) *Runner {
		r := &Runner{PoolID: pool.ID, HostID: h.ID, Name: name, State: RunnerIdle}
		if err := s.CreateRunner(ctx, r); err != nil {
			t.Fatal(err)
		}
		if _, err := s.TransitionRunner(ctx, r.ID, RunnerRemoved, "withdrawn"); err != nil {
			t.Fatal(err)
		}
		return r
	}
	byHost := mk("pool-a-aaaaaaaa", silent)
	byGitHub := mk("pool-a-bbbbbbbb", host)
	if _, err := s.ConfirmRunnerCleanup(ctx, byGitHub.ID, true); err != nil {
		t.Fatal(err)
	}
	failed := mk("pool-a-cccccccc", host)
	if err := s.RecordCleanupFailure(ctx, failed.ID, "container is in use"); err != nil {
		t.Fatal(err)
	}

	p, err := s.TransferProgress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The failed one is unconfirmed on both sides too: a host that could not
	// remove a container has not confirmed it gone, and GitHub was never asked.
	if p.PendingCleanup != 3 || p.CleanupAwaitingHost != 2 || p.CleanupAwaitingGitHub != 3 || p.CleanupFailed != 1 {
		t.Fatalf("counts = %+v", p)
	}
	waits := map[string]TransferWait{}
	for _, w := range p.Waiting {
		if w.Kind == TransferWaitCleanup {
			waits[w.ID] = w
		}
	}
	if len(waits) != 3 {
		t.Fatalf("waits = %+v", p.Waiting)
	}
	if w := waits[byHost.ID]; w.HostConfirmed || w.RegistrationConfirmed || w.HostName != "vm-silent" || !w.HostLastHeartbeat.Before(now) {
		t.Errorf("a runner on a silent host: %+v", w)
	}
	if w := waits[byGitHub.ID]; !w.HostConfirmed || w.RegistrationConfirmed || !w.HostLastHeartbeat.Equal(now) {
		t.Errorf("a runner GitHub has not confirmed: %+v", w)
	}
	if w := waits[failed.ID]; w.Error != "container is in use" || w.Since == nil {
		t.Errorf("a runner whose cleanup failed: %+v", w)
	}
}
