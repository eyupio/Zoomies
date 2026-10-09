package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// A backup leaves the machine as one file, because that is what a browser
// downloads and what an operator copies somewhere else: a gzipped tar of the
// backup's directory, holding the manifest, the database and -- only when the
// backup was taken with it -- the key.
//
// The archive is optionally wrapped in passphrase encryption, and the wrapping
// is deliberately a separate layer with its own magic rather than a flag in
// the manifest: an encrypted file has to be recognisable as one before
// anything inside it can be read, and a plain archive has to keep working with
// nothing but tar.

// The members an archive may carry, in the order they are written. Anything
// else is refused on the way in: an archive is written by this package and
// read by this package, and a member it did not write is a member somebody
// else put there.
var archiveMembers = []string{ManifestName, DBName, KeyName}

// memberName says which of archiveMembers the header names, or "" when it
// names none of them. The string returned is the package's own constant, never
// the header's: the only name that ever reaches the filesystem is one this
// package wrote, so a hand-made archive cannot choose a path however it spells
// its members. A directory prefix is allowed, because WriteArchive puts the
// members under one; a path that climbs, or an absolute one, is not.
func memberName(hdr *tar.Header) string {
	clean := path.Clean(strings.ReplaceAll(hdr.Name, "\\", "/"))
	if strings.HasPrefix(clean, "..") || path.IsAbs(clean) {
		return ""
	}
	base := path.Base(clean)
	for _, member := range archiveMembers {
		if member == base {
			return member
		}
	}
	return ""
}

// ArchiveName is the file name a backup downloads as.
func ArchiveName(id string, encrypted bool) string {
	name := id + ".tar.gz"
	if encrypted {
		name += EncryptedExt
	}
	return name
}

// WriteArchive writes the backup's directory to w as a gzipped tar. The
// members sit under a directory named for the backup, so extracting it by
// hand gives the same layout `zoomies restore` takes.
func WriteArchive(w io.Writer, entry *Entry) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	// The manifest first, so a reader that wants only the description does
	// not have to read the database to reach it.
	for _, name := range archiveMembers {
		p := filepath.Join(entry.Dir, name)
		info, err := os.Stat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("backup: reading %s: %w", p, err)
		}
		hdr := &tar.Header{
			Name:    entry.ID + "/" + name,
			Mode:    0o600,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			Format:  tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		// io.CopyN rather than io.Copy: the header promised a size, and a
		// database that grows under the copy would leave a tar the reader
		// cannot parse. The backup's database is never written to, so the
		// two agree; this is the belt to that brace.
		_, err = io.CopyN(tw, f, info.Size())
		f.Close()
		if err != nil {
			return fmt.Errorf("backup: archiving %s: %w", p, err)
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// UnpackOptions says who brought an archive and where to put it.
type UnpackOptions struct {
	// Source and TakenBy overwrite the manifest's: an uploaded backup is an
	// uploaded backup wherever it was taken.
	Source  string
	TakenBy string
	// MaxBytes bounds the unpacked database, so an archive that claims a
	// terabyte is refused before it fills the disk. Zero is unbounded.
	MaxBytes int64
	// Now is the clock; nil uses the wall clock.
	Now func() time.Time
}

// Unpack reads a gzipped tar written by WriteArchive into a new directory
// under root and returns it, verified.
//
// The archive's own directory name is ignored. The new backup is named for the
// instant its manifest says it was taken, so that an archive downloaded from
// one controller and uploaded to another lands under the name the first one
// gave it, and two uploads of the same file are two directories rather than
// one overwriting the other.
func Unpack(ctx context.Context, root string, r io.Reader, opts UnpackOptions) (*Entry, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("backup: creating %s: %w", root, err)
	}
	// Into a staging directory first, so a truncated upload never leaves a
	// half backup under a name List would show.
	staging, err := os.MkdirTemp(root, ".upload-*")
	if err != nil {
		return nil, fmt.Errorf("backup: creating a staging directory in %s: %w", root, err)
	}
	defer os.RemoveAll(staging)

	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("backup: this is not a gzipped archive: %w", err)
	}
	var inflated io.Reader = gz
	if opts.MaxBytes > 0 {
		inflated = &transferLimitReader{r: gz, left: opts.MaxBytes + (3 << 20)}
	}
	tr := tar.NewReader(inflated)
	seen := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("backup: reading the archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
			return nil, fmt.Errorf("backup: the archive holds %q, which is not a plain file", hdr.Name)
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		// The member's own name, whatever directory the archive put it in,
		// resolved to the constant this package knows it by.
		name := memberName(hdr)
		if name == "" {
			return nil, fmt.Errorf("backup: the archive holds %q, which is not part of a Zoomies backup", hdr.Name)
		}
		if seen[name] {
			return nil, fmt.Errorf("backup: the archive holds %s twice", name)
		}
		seen[name] = true
		if opts.MaxBytes > 0 && hdr.Size > opts.MaxBytes {
			return nil, fmt.Errorf("backup: %s is %d bytes, over the %d byte limit", name, hdr.Size, opts.MaxBytes)
		}
		if name != DBName && hdr.Size > 1<<20 {
			return nil, fmt.Errorf("backup: metadata member %s exceeds 1 MiB", name)
		}
		dest := filepath.Join(staging, name)
		f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		// Bounded by the header's own size, so a member that lies about
		// itself cannot write past what it declared.
		n, err := io.Copy(f, io.LimitReader(tr, hdr.Size))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, fmt.Errorf("backup: extracting %s: %w", name, err)
		}
		if n != hdr.Size {
			return nil, fmt.Errorf("backup: %s is truncated: the archive promised %d bytes and held %d", name, hdr.Size, n)
		}
	}
	if _, err := io.Copy(io.Discard, inflated); err != nil {
		return nil, fmt.Errorf("backup: finishing the archive: %w", err)
	}
	if !seen[DBName] {
		return nil, errors.New("backup: the archive holds no database; a Zoomies backup is " + DBName + " and its manifest")
	}

	taken := now().UTC()
	m, err := ReadManifest(staging)
	switch {
	case err == nil:
		if !m.TakenAt.IsZero() {
			taken = m.TakenAt.UTC()
		}
		m.Source = opts.Source
		m.TakenBy = opts.TakenBy
		if err := writeManifest(staging, m); err != nil {
			return nil, err
		}
	case errors.Is(err, os.ErrNotExist):
		// A database with no manifest is still a database. Nothing about it
		// can be checked beyond opening it, and the entry says so.
	default:
		return nil, err
	}

	// Checked before it is given a name, so a broken archive is refused
	// rather than listed.
	probe := &Entry{ID: "upload", Dir: staging, Manifest: m}
	v, err := verifyDir(ctx, probe)
	if err != nil {
		return nil, err
	}
	if !v.OK {
		return nil, fmt.Errorf("backup: the uploaded backup is not usable: %s", strings.Join(v.Problems, "; "))
	}

	dest, err := newDir(root, taken)
	if err != nil {
		return nil, err
	}
	// Rename the members rather than the directory: newDir has already made
	// the destination, and it is the name that has been reserved.
	for name := range seen {
		if err := os.Rename(filepath.Join(staging, name), filepath.Join(dest, name)); err != nil {
			_ = os.RemoveAll(dest)
			return nil, fmt.Errorf("backup: moving %s into place: %w", name, err)
		}
	}
	return Get(root, filepath.Base(dest))
}

// ---------------------------------------------------------------------------
// Passphrase encryption
// ---------------------------------------------------------------------------

// The encrypted form. A backup leaving the machine over a browser download
// may end up on a laptop, in a chat window or in a shared drive, and the
// database inside it holds every account's password hash and every sealed
// credential. The instance key keeps the credentials sealed; this keeps the
// rest.
//
// The format is a header and then chunks:
//
//	magic       "zoomies-backup-enc-v1\n"
//	salt        16 bytes, for the passphrase derivation
//	nonce base  4 bytes, the random half of every chunk's nonce
//	chunks      4-byte big-endian length, then that many bytes of AES-256-GCM
//	            ciphertext (tag included)
//
// The key is argon2id over the passphrase and the salt. Each chunk's nonce is
// the 4-byte base followed by the chunk's 8-byte counter, and its additional
// data says whether it is the last one -- so a file cut short, or one with a
// chunk moved or repeated, fails to open rather than opening as a shorter
// backup. Chunked rather than one GCM call over the whole file because a
// database can be gigabytes, and GCM needs the whole message in memory.
const (
	EncryptedExt   = ".enc"
	encMagic       = "zoomies-backup-enc-v1\n"
	encSaltLen     = 16
	encNonceBase   = 4
	encChunk       = 1 << 20
	encArgonTime   = 3
	encArgonMemory = 64 * 1024 // KiB
	encArgonLanes  = 4
	encKeyLen      = 32
)

// ErrWrongPassphrase is a file that did not open with the passphrase given.
// A wrong passphrase and a tampered file are the same failure here, by design:
// the authentication tag cannot tell them apart and neither can this.
var ErrWrongPassphrase = errors.New("backup: the passphrase does not open this file, or the file has been altered")

// ErrNotEncrypted is a file offered with a passphrase that has no encryption
// header to apply it to.
var ErrNotEncrypted = errors.New("backup: this file is not an encrypted backup")

// IsEncrypted reports whether r begins with the encrypted header, and returns
// a reader that still yields the whole stream, header included.
func IsEncrypted(r io.Reader) (bool, io.Reader) {
	br := bufio.NewReaderSize(r, len(encMagic))
	head, _ := br.Peek(len(encMagic))
	return string(head) == encMagic, br
}

func deriveKey(passphrase string, salt []byte) []byte {
	return argon2.IDKey([]byte(passphrase), salt, encArgonTime, encArgonMemory, encArgonLanes, encKeyLen)
}

// NewEncryptor wraps w so that everything written to the returned writer is
// encrypted under passphrase. Close writes the final chunk and must be called,
// or the file is truncated and will not open.
func NewEncryptor(w io.Writer, passphrase string) (io.WriteCloser, error) {
	if strings.TrimSpace(passphrase) == "" {
		return nil, errors.New("backup: a passphrase is needed to encrypt")
	}
	salt := make([]byte, encSaltLen)
	base := make([]byte, encNonceBase)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(base); err != nil {
		return nil, err
	}
	aead, err := newAEAD(passphrase, salt)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write([]byte(encMagic)); err != nil {
		return nil, err
	}
	if _, err := w.Write(salt); err != nil {
		return nil, err
	}
	if _, err := w.Write(base); err != nil {
		return nil, err
	}
	return &encWriter{w: w, aead: aead, base: base, buf: make([]byte, 0, encChunk)}, nil
}

func newAEAD(passphrase string, salt []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(deriveKey(passphrase, salt))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

type encWriter struct {
	w       io.Writer
	aead    cipher.AEAD
	base    []byte
	buf     []byte
	counter uint64
	closed  bool
}

func (e *encWriter) Write(p []byte) (int, error) {
	if e.closed {
		return 0, errors.New("backup: write after close")
	}
	written := 0
	for len(p) > 0 {
		room := encChunk - len(e.buf)
		n := min(room, len(p))
		e.buf = append(e.buf, p[:n]...)
		p = p[n:]
		written += n
		if len(e.buf) == encChunk {
			if err := e.flush(false); err != nil {
				return written, err
			}
		}
	}
	return written, nil
}

// Close seals whatever is buffered as the final chunk. The final chunk may be
// empty, and is written even so: it is what tells the reader the file ended
// where the writer meant it to.
func (e *encWriter) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	return e.flush(true)
}

func (e *encWriter) flush(final bool) error {
	nonce, aad := chunkNonce(e.base, e.counter, final)
	sealed := e.aead.Seal(nil, nonce, e.buf, aad)
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(sealed)))
	if _, err := e.w.Write(length[:]); err != nil {
		return err
	}
	if _, err := e.w.Write(sealed); err != nil {
		return err
	}
	e.counter++
	e.buf = e.buf[:0]
	return nil
}

func chunkNonce(base []byte, counter uint64, final bool) (nonce, aad []byte) {
	nonce = make([]byte, encNonceBase+8)
	copy(nonce, base)
	binary.BigEndian.PutUint64(nonce[encNonceBase:], counter)
	aad = make([]byte, 9)
	binary.BigEndian.PutUint64(aad, counter)
	if final {
		aad[8] = 1
	}
	return nonce, aad
}

// NewDecryptor wraps r, which must begin with the encrypted header, so that
// reading from the returned reader yields the plaintext. A wrong passphrase is
// reported on the first read, not on open: nothing in the header proves a
// passphrase, only a chunk does.
func NewDecryptor(r io.Reader, passphrase string) (io.Reader, error) {
	head := make([]byte, len(encMagic)+encSaltLen+encNonceBase)
	if _, err := io.ReadFull(r, head); err != nil {
		return nil, ErrNotEncrypted
	}
	if string(head[:len(encMagic)]) != encMagic {
		return nil, ErrNotEncrypted
	}
	salt := head[len(encMagic) : len(encMagic)+encSaltLen]
	base := head[len(encMagic)+encSaltLen:]
	aead, err := newAEAD(passphrase, salt)
	if err != nil {
		return nil, err
	}
	return &decReader{r: bufio.NewReader(r), aead: aead, base: base}, nil
}

type decReader struct {
	r       *bufio.Reader
	aead    cipher.AEAD
	base    []byte
	counter uint64
	plain   bytes.Reader
	done    bool
}

func (d *decReader) Read(p []byte) (int, error) {
	for d.plain.Len() == 0 {
		if d.done {
			return 0, io.EOF
		}
		if err := d.next(); err != nil {
			return 0, err
		}
	}
	return d.plain.Read(p)
}

// next opens one chunk. Either nonce variant is tried -- the final flag is in
// the additional data, so an ordinary chunk and a final one differ only there
// -- and a chunk that opens as neither is the wrong passphrase or a file that
// has been altered.
func (d *decReader) next() error {
	var length [4]byte
	if _, err := io.ReadFull(d.r, length[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("backup: the encrypted file ends before its final chunk; it was cut short")
		}
		return err
	}
	n := binary.BigEndian.Uint32(length[:])
	if n > encChunk+uint32(d.aead.Overhead()) {
		return ErrWrongPassphrase
	}
	sealed := make([]byte, n)
	if _, err := io.ReadFull(d.r, sealed); err != nil {
		return errors.New("backup: the encrypted file ends inside a chunk; it was cut short")
	}
	for _, final := range []bool{false, true} {
		nonce, aad := chunkNonce(d.base, d.counter, final)
		plain, err := d.aead.Open(nil, nonce, sealed, aad)
		if err != nil {
			continue
		}
		d.counter++
		d.plain.Reset(plain)
		d.done = final
		if final {
			if _, err := d.r.Peek(1); !errors.Is(err, io.EOF) {
				if err != nil {
					return err
				}
				return errors.New("backup: bytes follow the encrypted final chunk")
			}
		}
		return nil
	}
	return ErrWrongPassphrase
}
