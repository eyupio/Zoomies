package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The passphrase that opens an offsite backup protects the whole fleet's
// database copy. As a flag value it is in the shell history and readable from
// /proc/<pid>/cmdline for as long as the download runs, which on a large bucket
// is minutes, on a host that during disaster recovery is often shared. Every
// other command that takes a passphrase reads it from a file.
func TestRestoreRefusesAPassphraseGivenBothWays(t *testing.T) {
	file := filepath.Join(t.TempDir(), "backup.pass")
	if err := os.WriteFile(file, []byte("correct horse\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e, _, errOut := newTestEnv(t)
	code := dispatch(context.Background(), e, []string{"restore", "--from-remote", "offsite", "latest",
		"--passphrase", "correct horse", "--passphrase-file", file})
	if code != exitError {
		t.Fatalf("exit code = %d, want %d\n%s", code, exitError, errOut)
	}
	if !strings.Contains(errOut.String(), "not both") {
		t.Errorf("the refusal does not say only one may be given:\n%s", errOut)
	}
}

func TestRestorePassphraseFileMustHoldAPassphrase(t *testing.T) {
	file := filepath.Join(t.TempDir(), "empty.pass")
	if err := os.WriteFile(file, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e, _, errOut := newTestEnv(t)
	code := dispatch(context.Background(), e, []string{"restore", "--from-remote", "offsite", "latest",
		"--passphrase-file", file})
	if code != exitError {
		t.Fatalf("exit code = %d, want %d\n%s", code, exitError, errOut)
	}
	if !strings.Contains(errOut.String(), "is empty") {
		t.Errorf("an empty passphrase file was not refused as such:\n%s", errOut)
	}
}

func TestRestorePassphraseComesFromItsFileWithoutTheTrailingNewline(t *testing.T) {
	file := filepath.Join(t.TempDir(), "backup.pass")
	if err := os.WriteFile(file, []byte("correct horse\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e, _, errOut := newTestEnv(t)
	got, err := restorePassphrase(e, "", file)
	if err != nil {
		t.Fatal(err)
	}
	if got != "correct horse" {
		t.Errorf("passphrase = %q, want the file's contents without its newline", got)
	}
	if errOut.Len() != 0 {
		t.Errorf("reading the file warned about something:\n%s", errOut)
	}
}

// Scripts written against the old flag keep working for a release, but they
// are told, on stderr, that the value they pass is being exposed.
func TestRestorePassphraseFlagStillWorksButIsDeprecated(t *testing.T) {
	e, _, errOut := newTestEnv(t)
	got, err := restorePassphrase(e, "correct horse", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "correct horse" {
		t.Errorf("passphrase = %q, want the flag's value", got)
	}
	if !strings.Contains(errOut.String(), "--passphrase-file") || !strings.Contains(errOut.String(), "deprecated") {
		t.Errorf("no deprecation notice pointing at --passphrase-file:\n%s", errOut)
	}
}
