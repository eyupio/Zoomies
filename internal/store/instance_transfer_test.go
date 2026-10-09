package store

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
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
