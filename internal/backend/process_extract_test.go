package backend

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type tarEntry struct {
	name, link, body string
	typeflag         byte
}

func writeTarGz(t *testing.T, entries []tarEntry) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o755, Typeflag: e.typeflag, Linkname: e.link, Size: int64(len(e.body))}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "runner.tar.gz")
	if err := os.WriteFile(src, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return src
}

// safeJoin cleaned "/etc" to "etc" under the entry's directory, so a symlink
// to an absolute path passed the check, and a later regular entry beneath the
// link then followed it out of the destination. The digest check on every
// archive was the only thing standing in the way, and an operator may now
// choose to download a release Zoomies has no digest for.
func TestExtractTarGzRefusesSymlinkEscapes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks in archives are a Unix concern here")
	}
	cases := []struct {
		name    string
		entries []tarEntry
		want    string
	}{
		{"an absolute link target", []tarEntry{
			{name: "lib", link: "/etc", typeflag: tar.TypeSymlink},
		}, "absolute path"},
		{"a link out of the archive", []tarEntry{
			{name: "lib", link: "../../outside", typeflag: tar.TypeSymlink},
		}, "links outside"},
		{"a file written through an earlier link", []tarEntry{
			{name: "real", typeflag: tar.TypeDir},
			{name: "lib", link: "real", typeflag: tar.TypeSymlink},
			{name: "lib/cron.d/x", body: "* * * * * root evil\n", typeflag: tar.TypeReg},
		}, "through the symlink"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "tools")
			err := extractTarGz(writeTarGz(t, tc.entries), dest)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("extract = %v, want a refusal mentioning %q", err, tc.want)
			}
		})
	}

	// A relative link inside the archive, the shape a runner release actually
	// carries, still extracts.
	dest := filepath.Join(t.TempDir(), "tools")
	src := writeTarGz(t, []tarEntry{
		{name: "bin/real.sh", body: "#!/bin/sh\n", typeflag: tar.TypeReg},
		{name: "bin/alias.sh", link: "real.sh", typeflag: tar.TypeSymlink},
	})
	if err := extractTarGz(src, dest); err != nil {
		t.Fatalf("a relative link inside the archive was refused: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(dest, "bin/alias.sh")); err != nil || target != "real.sh" {
		t.Fatalf("alias.sh -> %q, %v", target, err)
	}
}
