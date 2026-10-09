package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

func TestTheTransferCommandsMoveAnOfflineInstanceWithoutCopyingItsKey(t *testing.T) {
	ctx := context.Background()
	dir, _ := backupHost(t)
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(dir, "zoomies.db")})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SetRecoveryFence(ctx, true, "instance transfer"); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	passFile := filepath.Join(dir, "move.pass")
	if err = os.WriteFile(passFile, []byte("a long transfer passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "instance.zbk")
	var stdout, stderr bytes.Buffer
	e := &env{out: &stdout, err: &stderr, in: strings.NewReader("")}
	if err = runTransfer(ctx, e, []string{"export", "--out", out, "--passphrase-file", passFile}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if err = runTransfer(ctx, e, []string{"export", "--out", out, "--passphrase-file", passFile}); err == nil {
		t.Fatal("existing archive overwritten")
	}
	after, _ := os.ReadFile(out)
	if !bytes.Equal(before, after) {
		t.Fatal("failed export changed existing archive")
	}
	// A fresh encryption key and database belong to the destination. bootstrap
	// access is established explicitly rather than trusting the incoming users.
	dest := t.TempDir()
	keyFile := filepath.Join(dest, "key")
	// Reuse the fixture helper's key generation without reusing its database.
	_, destKey := backupHost(t)
	if err = os.WriteFile(keyFile, []byte(destKey.Encode()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZOOMIES_DB_PATH", filepath.Join(dest, "zoomies.db"))
	t.Setenv("ZOOMIES_ENCRYPTION_KEY_FILE", keyFile)
	password := filepath.Join(dest, "operator.pass")
	_ = os.WriteFile(password, []byte("a strong operator password\n"), 0o600)
	args := []string{"import", out, "--passphrase-file", passFile, "--operator-name", "destination-operator", "--operator-password-file", password}
	if err = runTransfer(ctx, e, args); err == nil {
		t.Fatal("source stop acknowledgement omitted")
	}
	args = append(args, "--source-stopped")
	if err = runTransfer(ctx, e, args); err != nil {
		t.Fatal(err)
	}
	live, err := store.Open(ctx, store.Options{Path: filepath.Join(dest, "zoomies.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	installations, err := live.ListInstallations(ctx)
	if err != nil || len(installations) != 1 {
		t.Fatal("instance missing", err)
	}
	if _, err = destKey.Open(installations[0].PrivateKeyEnc); err != nil {
		t.Fatal("destination key cannot open transferred credential", err)
	}
	fence, err := live.RecoveryFenced(ctx)
	if err != nil || !fence.Fenced {
		t.Fatal("import started unfenced", err)
	}
}

func TestAnOfflineExportRefusesTheRunningControllersLock(t *testing.T) {
	dir, _ := backupHost(t)
	unlock, err := store.Lock(filepath.Join(dir, "zoomies.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	file := filepath.Join(dir, "pass")
	_ = os.WriteFile(file, []byte("a long transfer passphrase"), 0o600)
	var out bytes.Buffer
	e := &env{out: &out, err: &out, in: strings.NewReader("")}
	err = runTransfer(context.Background(), e, []string{"export", "--out", filepath.Join(dir, "archive.zbk"), "--passphrase-file", file})
	if err == nil || !strings.Contains(err.Error(), "stop the controller") {
		t.Fatal("running controller not refused", err)
	}
}
