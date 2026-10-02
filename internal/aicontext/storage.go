package aicontext

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

type Storage interface {
	Put(context.Context, *Snapshot) (string, error)
	Get(context.Context, string) (*Snapshot, error)
	Delete(context.Context, string) error
}

// DiskStorage stores blobs by their content hash, never by a source path.
// OpenRoot confines filesystem operations even if a path is replaced by a
// symlink. The owning controller, not a runner, retains these files.
type DiskStorage struct{ root *os.Root }

func OpenDiskStorage(directory string) (*DiskStorage, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("context storage must be a private directory (mode 0700)")
	}
	r, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	return &DiskStorage{r}, nil
}

func (d *DiskStorage) Close() error { return d.root.Close() }

func (d *DiskStorage) Put(ctx context.Context, s *Snapshot) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.Validate(); err != nil {
		return "", err
	}
	body, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	if len(body) > MaxSnapshotBytes {
		return "", fmt.Errorf("encoded context exceeds the snapshot limit")
	}
	id := Hash(body)
	// Temporary names contain only entropy generated here. An incomplete
	// write can never masquerade as the immutable published digest.
	temp := "pending-" + rand.Text()
	file, err := d.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	defer d.root.Remove(temp)
	_, writeErr := file.Write(body)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := d.root.Rename(temp, id+".json"); err != nil {
		return "", err
	}
	return id, nil
}

func (d *DiskStorage) Get(ctx context.Context, id string) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !digestPattern.MatchString(id) {
		return nil, fmt.Errorf("context blob ID must be a SHA-256 digest")
	}
	f, err := d.root.Open(id + ".json")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxSnapshotBytes {
		return nil, fmt.Errorf("context blob is not a bounded regular file")
	}
	body, err := io.ReadAll(io.LimitReader(f, MaxSnapshotBytes+1))
	if err != nil {
		return nil, err
	}
	if Hash(body) != id {
		return nil, fmt.Errorf("stored context fails integrity validation")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return Decode(bytes.NewReader(body))
}

func (d *DiskStorage) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !digestPattern.MatchString(id) {
		return fmt.Errorf("context blob ID must be a SHA-256 digest")
	}
	err := d.root.Remove(id + ".json")
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
