package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/eyupio/zoomies/internal/backup"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/store"
)

func runTransfer(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "transfer", "Move a complete instance, including its history and fleet credentials.", []*subcommand{
		{"prepare", "", "Drain a running instance in one action without interrupting busy jobs", transferPrepare},
		{"status", "", "Follow runner, job and cleanup progress", transferStatus},
		{"cancel", "", "Resume saved pools before the instance is fenced", transferCancel},
		{"export", "--out <file> --passphrase-file <file>", "Export a drained, fenced instance while its controller is stopped", transferExport},
		{"import", "<file> --source-stopped --passphrase-file <file>", "Import into an empty destination, keeping its operator access and recovery fence", transferImport},
	}, args)
}

func transferConfig(ctx context.Context, path string) (*config.Config, func(), error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, err
	}
	if err = os.MkdirAll(filepath.Dir(cfg.Database.Path), 0o750); err != nil {
		return nil, nil, err
	}
	unlock, err := store.Lock(cfg.Database.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("stop the controller before transferring its database: %w", err)
	}
	cleanup := func() { _ = unlock() }
	key, err := backup.ConfiguredKey(cfg)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if _, err = os.Stat(cfg.Database.Path); err == nil {
		st, err := store.Open(ctx, store.Options{Path: cfg.Database.Path, ReadOnly: true})
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		rows, err := st.InstanceSettings(ctx)
		_ = st.Close()
		if err == nil {
			findings, e := cfg.Rebuild(rows, key)
			err = e
			for _, f := range findings {
				if f.Severity == config.SeverityError {
					err = fmt.Errorf("cannot load stored settings: %s", f.Title)
				}
			}
		}
		if err != nil {
			cleanup()
			return nil, nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		cleanup()
		return nil, nil, err
	}
	return cfg, cleanup, nil
}

func transferExport(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies transfer export --out <file> --passphrase-file <file>", "Export a complete instance after draining, raising its recovery fence and stopping its controller.")
	cfgFile := fs.String("config", "", "path to zoomies.yaml")
	out := fs.String("out", "", "new encrypted archive file; existing files are refused")
	passFile := fs.String("passphrase-file", "", "file holding the archive passphrase")
	fs.example("zoomies transfer export --out instance.zbk --passphrase-file ./move.pass")
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	if *out == "" || *passFile == "" {
		return usagef("transfer export", "needs --out and --passphrase-file")
	}
	pass, err := readPassphraseFile(*passFile)
	if err != nil {
		return err
	}
	cfg, cleanup, err := transferConfig(ctx, *cfgFile)
	if err != nil {
		return err
	}
	defer cleanup()
	st, err := store.Open(ctx, store.Options{Path: cfg.Database.Path, ReadOnly: true})
	if err != nil {
		return err
	}
	defer st.Close()
	f, err := os.CreateTemp(filepath.Dir(*out), ".instance-transfer-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = backup.WriteTransfer(ctx, st, cfg, pass, f); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// A hard link publishes the complete file without overwriting an existing
	// archive. The temporary file is on the same volume as the destination.
	if err = os.Link(f.Name(), *out); err != nil {
		return fmt.Errorf("publishing the archive without replacing a file: %w", err)
	}
	fmt.Fprintf(e.out, "Exported the complete instance to %s. Keep the source stopped during cutover.\n", *out)
	return nil
}

func transferImport(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies transfer import <file> --source-stopped --passphrase-file <file>", "Import a complete instance into an empty, stopped destination. Its own encryption key and operator access remain in charge.")
	cfgFile := fs.String("config", "", "path to zoomies.yaml")
	passFile := fs.String("passphrase-file", "", "file holding the archive passphrase")
	stopped := fs.Bool("source-stopped", false, "confirm the source controller is stopped and will stay stopped during cutover")
	name := fs.String("operator-name", "", "create a destination operator when this host has no existing operator access")
	passwordFile := fs.String("operator-password-file", "", "file holding the new destination operator's password")
	fs.example("zoomies transfer import instance.zbk --source-stopped --passphrase-file ./move.pass",
		"zoomies transfer import instance.zbk --source-stopped --passphrase-file ./move.pass --operator-name new-operator --operator-password-file ./operator.pass")
	if err := fs.parse(args); err != nil {
		return err
	}
	src, err := fs.oneArg("the encrypted instance archive")
	if err != nil {
		return err
	}
	if !*stopped || *passFile == "" {
		return usagef("transfer import", "needs --source-stopped and --passphrase-file")
	}
	if (*name == "") != (*passwordFile == "") {
		return usagef("transfer import", "give both --operator-name and --operator-password-file")
	}
	var operator *store.User
	if *name != "" {
		pass, err := readPassphraseFile(*passwordFile)
		if err != nil {
			return err
		}
		if len(pass) < 12 {
			return errors.New("use at least 12 characters for the destination operator password")
		}
		hash, err := cryptox.HashPassword(pass)
		if err != nil {
			return err
		}
		operator = &store.User{Username: strings.TrimSpace(*name), Role: store.Roles()[len(store.Roles())-1], PasswordHash: hash}
		if operator.Username == "" {
			return errors.New("give the destination operator a username")
		}
	}
	pass, err := readPassphraseFile(*passFile)
	if err != nil {
		return err
	}
	cfg, cleanup, err := transferConfig(ctx, *cfgFile)
	if err != nil {
		return err
	}
	defer cleanup()
	root, err := os.MkdirTemp(filepath.Dir(cfg.Database.Path), ".transfer-import-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	entry, err := backup.ReadTransfer(ctx, cfg, root, f, pass)
	if err != nil {
		return err
	}
	report, err := backup.Restore(ctx, cfg, entry.Dir, backup.RestoreOptions{Replace: true, SourceStopped: *stopped, TransferOperator: operator})
	if err != nil {
		return err
	}
	printRestoreReport(e, report, cfg.Database.Path)
	return nil
}

func transferPrepare(ctx context.Context, e *env, args []string) error {
	return transferPreparationRequest(ctx, e, args, "prepare")
}
func transferStatus(ctx context.Context, e *env, args []string) error {
	return transferPreparationRequest(ctx, e, args, "status")
}
func transferCancel(ctx context.Context, e *env, args []string) error {
	return transferPreparationRequest(ctx, e, args, "cancel")
}
func transferPreparationRequest(ctx context.Context, e *env, args []string, action string) error {
	fs := newFlagSet(e, "zoomies transfer "+action, "Prepare a running instance for transfer, follow its drain, or cancel before it is fenced.")
	cf := registerClientFlags(fs, true)
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	var progress store.TransferProgress
	p, err := cf.printer(e)
	if err != nil {
		return err
	}
	var raw []byte
	switch action {
	case "prepare":
		raw, err = client.post(ctx, "/transfers/preparation", nil, nil, &progress)
	case "status":
		raw, err = client.get(ctx, "/transfers/preparation", nil, &progress)
	case "cancel":
		_, err = client.del(ctx, "/transfers/preparation", nil, nil)
		if err == nil {
			fmt.Fprintln(e.out, "Preparation cancelled; saved pools resume.")
		}
		return err
	}
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	if progress.Ready {
		fmt.Fprintln(e.out, "Ready to export. This instance is fenced; keep its controller stopped during cutover.")
		return nil
	}
	if !progress.Draining {
		fmt.Fprintln(e.out, "Preparation has not started.")
		return nil
	}
	fmt.Fprintf(e.out, "Draining: %d busy runners, %d runners remaining, %d awaiting cleanup, %d running jobs and %d machine operations.\n", progress.BusyRunners, progress.LiveRunners, progress.PendingCleanup, progress.ActiveJobs, progress.MachineOperations)
	return nil
}
