package backup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/store"
)

const transferPassphrase = "a long instance transfer passphrase"

func transferDestination(t *testing.T) (*config.Config, *store.Store, *cryptox.Key) {
	t.Helper()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(t.TempDir(), "zoomies.db")
	key, err := cryptox.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Security.EncryptionKey = key.Encode()
	cfg.Security.EncryptionKeyFile = ""
	st, err := store.Open(context.Background(), store.Options{Path: cfg.Database.Path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return cfg, st, key
}

func portableSnapshot(t *testing.T, cfg *config.Config, st *store.Store) []byte {
	t.Helper()
	if err := st.SetRecoveryFence(context.Background(), true, "moving this instance"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := WriteTransfer(context.Background(), st, cfg, transferPassphrase, &out); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestACompleteInstanceMovesAcrossKeysAndKeepsItsDestinationInCharge(t *testing.T) {
	ctx := context.Background()
	src, st, key := host(t)
	team := &store.User{Username: "team", Role: store.RoleAdmin, PasswordHash: "team password hash"}
	oldOperator := &store.User{Username: "old-operator", Role: store.Roles()[len(store.Roles())-1], PasswordHash: "old password hash"}
	for _, u := range []*store.User{team, oldOperator} {
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	sealed, err := key.Seal([]byte("team authenticator"))
	if err != nil {
		t.Fatal(err)
	}
	if err = st.BeginTwoStep(ctx, team.ID, sealed); err != nil {
		t.Fatal(err)
	}
	if err = st.BeginTwoStep(ctx, oldOperator.ID, sealed); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []*store.APIToken{
		{Name: "team", Role: store.RoleAdmin, OwnerRole: store.RoleAdmin, UserID: team.ID, TokenHash: "team-token"},
		{Name: "old operator", Role: oldOperator.Role, OwnerRole: oldOperator.Role, UserID: oldOperator.ID, TokenHash: "old-operator-token"},
	} {
		if err = st.CreateAPIToken(ctx, tok); err != nil {
			t.Fatal(err)
		}
	}
	if err = st.CreateSession(ctx, &store.Session{UserID: team.ID, TokenHash: "old-session", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	setting, _ := config.LookupSetting("agent.registry_auth")
	row, err := config.EncodeStored(setting, "effective registry credential", key)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.PutInstanceSettings(ctx, "test", []store.InstanceSetting{row}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.InstanceSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = src.Rebuild(rows, key); err != nil {
		t.Fatal(err)
	}
	oidcSetting, _ := config.LookupSetting("oidc.client_secret")
	oidcRow, err := config.EncodeStored(oidcSetting, "source operator issuer secret", key)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.PutInstanceSettings(ctx, "test", []store.InstanceSetting{oidcRow}); err != nil {
		t.Fatal(err)
	}
	history, err := st.UpsertJob(ctx, &store.Job{GitHubJobID: 123, GitHubRunID: 456, Repo: "team/repository", Workflow: "build", JobName: "test", State: store.JobCompleted, Conclusion: "success", QueuedAt: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.AppendAudit(ctx, &store.AuditEvent{ActorID: team.ID, ActorName: "team administrator", Action: "test.history", TargetKind: "job", TargetID: history.ID}); err != nil {
		t.Fatal(err)
	}
	if err = st.ConfirmTwoStep(ctx, team.ID, 17, []string{"single-use recovery hash"}); err != nil {
		t.Fatal(err)
	}
	archive := portableSnapshot(t, src, st)
	dest, dst, destKey := transferDestination(t)
	newOperator := &store.User{Username: "new-operator", Role: oldOperator.Role, PasswordHash: "new password hash"}
	if err = dst.CreateUser(ctx, newOperator); err != nil {
		t.Fatal(err)
	}
	destToken := &store.APIToken{Name: "new operator", Role: newOperator.Role, OwnerRole: newOperator.Role, UserID: newOperator.ID, TokenHash: "destination-token"}
	if err = dst.CreateAPIToken(ctx, destToken); err != nil {
		t.Fatal(err)
	}
	dest.Server.ExternalURL = "https://destination.example.test"
	entry, err := ReadTransfer(ctx, dest, t.TempDir(), bytes.NewReader(archive), transferPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Manifest.Transfer.Prepared || entry.Manifest.Key.Fingerprint != destKey.Fingerprint() || entry.KeyIncluded {
		t.Fatalf("snapshot not prepared: %+v", entry.Manifest)
	}
	if _, err = os.Stat(filepath.Join(entry.Dir, KeyName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("transport key retained")
	}
	exportedCopy, err := store.Open(ctx, store.Options{Path: filepath.Join(entry.Dir, DBName)})
	if err != nil {
		t.Fatal(err)
	}
	operatorCopy, err := exportedCopy.GetUser(ctx, oldOperator.ID)
	if err != nil || operatorCopy.PasswordHash != "" || !operatorCopy.Disabled {
		t.Fatal("source operator password travelled", err)
	}
	operatorStep, err := exportedCopy.GetTwoStep(ctx, oldOperator.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if operatorStep != nil && len(operatorStep.SecretEnc) != 0 {
		t.Fatal("source operator authenticator travelled")
	}
	exportedCopyRows, err := exportedCopy.InstanceSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range exportedCopyRows {
		if r.Key == oidcSetting.Key {
			t.Fatal("source issuer secret travelled")
		}
	}
	if err = exportedCopy.Close(); err != nil {
		t.Fatal(err)
	}
	unchangedOperator, err := st.GetUser(ctx, oldOperator.ID)
	if err != nil || unchangedOperator.PasswordHash != "old password hash" || unchangedOperator.Disabled {
		t.Fatal("export modified source access", err)
	}
	before, _ := dst.TransferInventory(ctx)
	if _, err = Restore(ctx, dest, entry.Dir, RestoreOptions{}); err == nil {
		t.Fatal("source stop was not required")
	}
	after, _ := dst.TransferInventory(ctx)
	if before["users"] != after["users"] {
		t.Fatal("failed preflight changed destination")
	}
	if err = dst.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := Restore(ctx, dest, entry.Dir, RestoreOptions{SourceStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.MovedAside == "" {
		t.Fatal("destination bootstrap was not kept for rollback")
	}
	live, err := store.Open(ctx, store.Options{Path: dest.Database.Path})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	installs, err := live.ListInstallations(ctx)
	if err != nil || len(installs) != 1 {
		t.Fatalf("installations lost: %v", err)
	}
	plain, err := destKey.Open(installs[0].PrivateKeyEnc)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := live.GetJob(ctx, history.ID)
	if err != nil || retained.Conclusion != "success" || retained.GitHubJobID != history.GitHubJobID {
		t.Fatal("job history changed", err)
	}
	if string(plain) != "-----BEGIN RSA PRIVATE KEY-----\nnot really\n" {
		t.Fatal("installation credential changed")
	}
	if _, err = key.Open(installs[0].PrivateKeyEnc); err == nil {
		t.Fatal("source key still opens imported secrets")
	}
	twoStep, err := live.GetTwoStep(ctx, team.ID)
	if err != nil {
		t.Fatal(err)
	}
	plain, err = destKey.Open(twoStep.SecretEnc)
	if err != nil || string(plain) != "team authenticator" || !twoStep.Enabled() || twoStep.LastStep != 17 {
		t.Fatalf("authenticator lost: %v", err)
	}
	if _, err = live.GetAPITokenByHash(ctx, "team-token"); err != nil {
		t.Fatal("team credential lost", err)
	}
	old, err := live.GetAPITokenByHash(ctx, "old-operator-token")
	if err == nil && !old.Revoked {
		t.Fatal("source operator token survived")
	}
	if _, err = live.GetAPITokenByHash(ctx, "destination-token"); err != nil {
		t.Fatal("destination credential lost", err)
	}
	oldUser, err := live.GetUser(ctx, oldOperator.ID)
	if err != nil || !oldUser.Disabled {
		t.Fatal("source operator still enabled", err)
	}
	if _, _, err = live.GetSessionByTokenHash(ctx, "old-session"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("session survived", err)
	}
	fence, err := live.RecoveryFenced(ctx)
	if err != nil || !fence.Fenced {
		t.Fatal("import not fenced", err)
	}
	cfg := config.Default()
	rows, err = live.InstanceSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cfg.Rebuild(rows, destKey); err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.RegistryAuth != "effective registry credential" || cfg.Server.ExternalURL != dest.Server.ExternalURL {
		t.Fatal("fleet or destination configuration lost")
	}
	// Moving back is exactly the same operation with a third operator and key.
	roundTrip := portableSnapshot(t, dest, live)
	third, empty, _ := transferDestination(t)
	_ = empty.Close()
	prepared, err := ReadTransfer(ctx, third, t.TempDir(), bytes.NewReader(roundTrip), transferPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Restore(ctx, third, prepared.Dir, RestoreOptions{SourceStopped: true, TransferOperator: &store.User{Username: "third-operator", Role: newOperator.Role, PasswordHash: "third password"}})
	if err != nil {
		t.Fatal("return transfer failed", err)
	}
}

func TestADamagedTransferNeverTouchesTheDestination(t *testing.T) {
	src, st, _ := host(t)
	archive := portableSnapshot(t, src, st)
	dest, dst, _ := transferDestination(t)
	for name, body := range map[string][]byte{"truncated": archive[:len(archive)-10], "appended": append(bytes.Clone(archive), 1), "altered": func() []byte { v := bytes.Clone(archive); v[len(v)/2] ^= 1; return v }()} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if _, err := ReadTransfer(context.Background(), dest, root, bytes.NewReader(body), transferPassphrase); err == nil {
				t.Fatal("damaged transfer accepted")
			}
			entries, _ := os.ReadDir(root)
			if len(entries) != 0 {
				t.Fatal("failed upload left a snapshot")
			}
		})
	}
	if _, err := ReadTransfer(context.Background(), dest, t.TempDir(), bytes.NewReader(archive), "wrong password"); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
	inventory, _ := dst.TransferInventory(context.Background())
	if inventory["installations"] != 0 {
		t.Fatal("destination changed")
	}
}

func TestATransferCannotReplaceAnExistingFleet(t *testing.T) {
	src, st, _ := host(t)
	archive := portableSnapshot(t, src, st)
	dest, dst, _ := host(t)
	entry, err := ReadTransfer(context.Background(), dest, t.TempDir(), bytes.NewReader(archive), transferPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Restore(context.Background(), dest, entry.Dir, RestoreOptions{SourceStopped: true, TransferOperator: &store.User{Username: "operator", Role: store.Roles()[len(store.Roles())-1], PasswordHash: "hash"}})
	if err == nil {
		t.Fatal("occupied destination replaced")
	}
	installs, _ := dst.ListInstallations(context.Background())
	if len(installs) != 1 {
		t.Fatal("existing fleet changed")
	}
}

func TestACompleteExportRequiresTheSourceFence(t *testing.T) {
	cfg, st, _ := host(t)
	var out bytes.Buffer
	if err := WriteTransfer(context.Background(), st, cfg, transferPassphrase, &out); err == nil {
		t.Fatal("unfenced source exported")
	}
	if out.Len() != 0 {
		t.Fatal("refusal wrote archive bytes")
	}
}

func TestExportDoesNotWriteThroughTheRunningConfiguration(t *testing.T) {
	cfg, st, key := host(t)
	ctx := context.Background()
	setting, _ := config.LookupSetting("agent.registry_auth")
	row, err := config.EncodeStored(setting, "a registry credential", key)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.PutInstanceSettings(ctx, "test", []store.InstanceSetting{row}); err != nil {
		t.Fatal(err)
	}
	before := cfg.Source(setting.Key)
	_ = portableSnapshot(t, cfg, st)
	if cfg.Source(setting.Key) != before || cfg.Agent.RegistryAuth != "" {
		t.Fatal("export changed the running configuration snapshot")
	}
}
