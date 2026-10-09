package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/store"
)

const TransferVersion = 1
const MaxTransferBytes int64 = 8 << 30

// TransferInfo distinguishes a portable snapshot from a key-bound backup.
// Prepared snapshots have already been sealed for this destination.
type TransferInfo struct {
	Version   int              `json:"version"`
	Prepared  bool             `json:"prepared"`
	Inventory map[string]int64 `json:"inventory"`
}

func transferTransform(from, to *cryptox.Key) func([]byte) ([]byte, error) {
	return func(sealed []byte) ([]byte, error) {
		plain, err := from.Open(sealed)
		if err != nil {
			return nil, err
		}
		defer clear(plain)
		return to.Seal(plain)
	}
}

func fleetTransferSetting(s config.Setting) bool {
	return s.Scope == config.ScopeInstance && !strings.HasPrefix(s.Key, "oidc.")
}

// WriteTransfer takes a consistent snapshot, gives its secrets a disposable key
// and streams the whole result under a passphrase. The source key never travels.
func WriteTransfer(ctx context.Context, st *store.Store, cfg *config.Config, passphrase string, w io.Writer) error {
	if len(passphrase) < config.MinBackupPassphrase {
		return fmt.Errorf("a transfer passphrase needs at least %d characters", config.MinBackupPassphrase)
	}
	if err := st.TransferReady(ctx); err != nil {
		return err
	}
	source, err := ConfiguredKey(cfg)
	if err != nil {
		return err
	}
	transfer, err := cryptox.GenerateKey()
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp(filepath.Dir(cfg.Database.Path), ".transfer-export-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	entry, err := Take(ctx, st, TakeOptions{Config: cfg, Dir: root, Source: SourceCLI})
	if err != nil {
		return err
	}
	if entry.Bytes > MaxTransferBytes {
		return errors.New("source database exceeds the 8 GiB transfer limit")
	}
	copy, err := store.Open(ctx, store.Options{Path: filepath.Join(entry.Dir, DBName)})
	if err != nil {
		return err
	}
	// Effective fleet configuration can live outside SQLite. Materialising it
	// into the private copy keeps environment-supplied runner secrets portable.
	rows, err := copy.InstanceSettings(ctx)
	if err == nil {
		for _, row := range rows {
			if _, known := config.LookupSetting(row.Key); row.Secret && !known {
				err = fmt.Errorf("transfer has no secret rule for setting %s", row.Key)
				break
			}
		}
	}
	if err == nil {
		cfg, err = rebuildTransferConfig(cfg, rows, source)
	}
	if err != nil {
		copy.Close()
		return err
	}
	var values []store.InstanceSetting
	for _, s := range config.Settings() {
		if !fleetTransferSetting(s) {
			continue
		}
		v, err := cfg.Value(s.Key)
		if err != nil {
			copy.Close()
			return err
		}
		row, err := config.EncodeStored(s, v, source)
		if err != nil {
			copy.Close()
			return err
		}
		values = append(values, row)
	}
	if err = copy.PutInstanceSettings(ctx, "instance transfer", values); err == nil {
		var processKeys []string
		for _, s := range config.Settings() {
			if !fleetTransferSetting(s) {
				processKeys = append(processKeys, s.Key)
			}
		}
		err = copy.StripTransferOperatorSecrets(ctx, processKeys)
	}
	if err == nil {
		err = copy.TransformSecrets(ctx, transferTransform(source, transfer))
	}
	var inventory map[string]int64
	if err == nil {
		inventory, err = copy.TransferInventory(ctx)
	}
	cerr := copy.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	tc := *cfg
	tc.Security.EncryptionKey = transfer.Encode()
	tc.Security.EncryptionKeyFile = ""
	m, err := buildManifest(ctx, &tc, filepath.Join(entry.Dir, DBName), entry.Dir, TakeOptions{Source: SourceCLI}, entry.TakenAt)
	if err != nil {
		return err
	}
	m.Transfer = &TransferInfo{Version: TransferVersion, Inventory: inventory}
	m.Key.Included = true
	m.Key.SourcePath = ""
	m.Key.From = "transfer"
	// The configuration projection is useful for ordinary disaster recovery;
	// migration uses the explicit fleet rows and destination configuration.
	m.Config = nil
	m.ConfigPath = ""
	if err = writeManifest(entry.Dir, m); err != nil {
		return err
	}
	if err = cryptox.WriteKeyFile(filepath.Join(entry.Dir, KeyName), transfer); err != nil {
		return err
	}
	entry.Manifest = m
	encrypted, err := NewEncryptor(w, passphrase)
	if err != nil {
		return err
	}
	if err = WriteArchive(encrypted, entry); err != nil {
		return err
	}
	return encrypted.Close()
}

// ReadTransfer authenticates the entire stream before re-sealing any secrets.
// The prepared snapshot can be staged without retaining a passphrase.
func ReadTransfer(ctx context.Context, cfg *config.Config, root string, r io.Reader, passphrase string) (entry *Entry, err error) {
	r = &transferLimitReader{r: r, left: MaxTransferBytes}
	encrypted, src := IsEncrypted(r)
	if !encrypted {
		return nil, errors.New("a complete instance transfer must be passphrase-encrypted")
	}
	src, err = NewDecryptor(src, passphrase)
	if err != nil {
		return nil, err
	}
	entry, err = Unpack(ctx, root, src, UnpackOptions{Source: SourceUploaded, MaxBytes: MaxTransferBytes})
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(entry.Dir)
		}
	}()
	// tar EOF can precede the authenticated final chunk. Reading through that
	// chunk rejects truncated ciphertext before accepting the copy.
	if _, err = io.Copy(io.Discard, src); err != nil {
		return nil, err
	}
	if entry.Manifest == nil || entry.Manifest.Transfer == nil || entry.Manifest.ManifestVersion != ManifestVersion || entry.Manifest.Transfer.Version != TransferVersion || entry.Manifest.Transfer.Prepared {
		return nil, errors.New("this is not a supported portable instance snapshot")
	}
	from, err := cryptox.LoadKeyFile(filepath.Join(entry.Dir, KeyName))
	if err != nil {
		return nil, err
	}
	if from.Fingerprint() != entry.Manifest.Key.Fingerprint {
		return nil, errors.New("the transfer key does not match the snapshot")
	}
	to, err := ConfiguredKey(cfg)
	if err != nil {
		return nil, err
	}
	copy, err := store.Open(ctx, store.Options{Path: filepath.Join(entry.Dir, DBName)})
	if err != nil {
		return nil, err
	}
	err = copy.TransformSecrets(ctx, transferTransform(from, to))
	cerr := copy.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	if err = os.Remove(filepath.Join(entry.Dir, KeyName)); err != nil {
		return nil, err
	}
	m, err := buildManifest(ctx, cfg, filepath.Join(entry.Dir, DBName), entry.Dir, TakeOptions{Source: SourceUploaded}, entry.TakenAt)
	if err != nil {
		return nil, err
	}
	m.Transfer = &TransferInfo{Version: TransferVersion, Prepared: true, Inventory: entry.Manifest.Transfer.Inventory}
	m.Key.Included = false
	m.Config = nil
	m.ConfigPath = ""
	if err = writeManifest(entry.Dir, m); err != nil {
		return nil, err
	}
	entry, err = Get(root, entry.ID)
	if err != nil {
		return nil, err
	}
	ok = true
	return entry, nil
}

// prepareTransferRestore adapts a private copy immediately before the swap, so
// operator accounts revoked since upload cannot be resurrected by the import.
func prepareTransferRestore(ctx context.Context, cfg *config.Config, entryDir string, opts RestoreOptions) (string, func(), error) {
	m, err := ReadManifest(entryDir)
	if err != nil {
		return "", nil, err
	}
	if m.Transfer == nil || m.Transfer.Version != TransferVersion || !m.Transfer.Prepared {
		return "", nil, errors.New("upload and prepare the portable snapshot for this destination first")
	}
	key, err := ConfiguredKey(cfg)
	if err != nil {
		return "", nil, err
	}
	if op := opts.TransferOperator; op != nil && (op.Role != store.Roles()[len(store.Roles())-1] || op.Disabled || op.PasswordHash == "" || strings.TrimSpace(op.Username) == "") {
		return "", nil, errors.New("the destination operator must be an enabled local operator with a username and password")
	}
	if !opts.SourceStopped {
		return "", nil, errors.New("confirm that the source controller is stopped before importing this instance")
	}
	var access []store.ArchiveTable
	hasAccess := opts.TransferOperator != nil
	if _, err := os.Stat(cfg.Database.Path); err == nil {
		dest, err := store.Open(ctx, store.Options{Path: cfg.Database.Path, ReadOnly: true})
		if err != nil {
			return "", nil, err
		}
		err = dest.TransferDestinationEmpty(ctx)
		if err == nil {
			hasAccess, err = dest.TransferHasOperator(ctx)
			hasAccess = hasAccess || opts.TransferOperator != nil
		}
		if err == nil {
			access, err = dest.TransferOperatorAccess(ctx)
		}
		if err == nil {
			rows, e := dest.InstanceSettings(ctx)
			if e != nil {
				err = e
			} else {
				cfg, err = rebuildTransferConfig(cfg, rows, key)
			}
		}
		cerr := dest.Close()
		if err == nil {
			err = cerr
		}
		if err != nil {
			return "", nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", nil, err
	}
	if !hasAccess {
		return "", nil, errors.New("the destination needs its own operator account or API token before import")
	}
	root, err := os.MkdirTemp(filepath.Dir(cfg.Database.Path), ".transfer-restore-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(root) }
	fail := func(err error) (string, func(), error) { cleanup(); return "", nil, err }
	if err = copyFile(filepath.Join(entryDir, DBName), filepath.Join(root, DBName)); err != nil {
		return fail(err)
	}
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(root, DBName)})
	if err != nil {
		return fail(err)
	}
	var clears []string
	for _, setting := range config.Settings() {
		if !fleetTransferSetting(setting) {
			clears = append(clears, setting.Key)
		}
	}
	err = st.AdaptTransferredInstance(ctx, access, clears)
	if err == nil && opts.TransferOperator != nil {
		err = st.CreateUser(ctx, opts.TransferOperator)
	}
	// Destination process settings may also be stored in SQLite. Keep them with
	// their original key, which already is the key sealing the imported copy.
	if err == nil {
		var settings []store.InstanceSetting
		for _, setting := range config.Settings() {
			if !setting.Stored() || fleetTransferSetting(setting) {
				continue
			}
			v, e := cfg.Value(setting.Key)
			if e != nil {
				err = e
				break
			}
			row, e := config.EncodeStored(setting, v, key)
			if e != nil {
				err = e
				break
			}
			settings = append(settings, row)
		}
		if err == nil {
			err = st.PutInstanceSettings(ctx, "instance transfer", settings)
		}
	}
	if err == nil {
		err = st.SetRecoveryFence(ctx, true, "complete instance transferred; verify agents, webhooks and ownership before resuming")
	}
	if err == nil {
		_, err = st.MarkMachinesUnverified(ctx)
	}
	cerr := st.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		return fail(err)
	}
	fresh, err := buildManifest(ctx, cfg, filepath.Join(root, DBName), root, TakeOptions{Source: SourceUploaded}, m.TakenAt)
	if err != nil {
		return fail(err)
	}
	// This copy is now an ordinary destination-key backup with authority adapted.
	if err = writeManifest(root, fresh); err != nil {
		return fail(err)
	}
	return root, cleanup, nil
}

// The same bound applies to encrypted bytes and inflated tar bytes, so a small
// compressed upload cannot spend unbounded work on padding or directory entries.
type transferLimitReader struct {
	r    io.Reader
	left int64
}

func (r *transferLimitReader) Read(p []byte) (int, error) {
	if r.left == 0 {
		var probe [1]byte
		n, err := r.r.Read(probe[:])
		if n > 0 {
			return 0, errors.New("instance archive exceeds its size limit")
		}
		return 0, err
	}
	if int64(len(p)) > r.left {
		p = p[:r.left]
	}
	n, err := r.r.Read(p)
	r.left -= int64(n)
	return n, err
}

// Live supplies the same independent source map as a normal settings update;
// applying database rows must never mutate the running controller's snapshot.
func rebuildTransferConfig(cfg *config.Config, rows []store.InstanceSetting, key *cryptox.Key) (*config.Config, error) {
	var findings config.Findings
	var err error
	_, next := config.NewLive(cfg).Update(func(copy *config.Config) { findings, err = copy.Rebuild(rows, key) })
	if err != nil {
		return nil, err
	}
	if len(findings.Errors()) > 0 {
		return nil, errors.New("cannot open stored settings with this instance's encryption key")
	}
	return next, nil
}
