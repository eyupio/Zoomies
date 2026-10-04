package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/version"
)

// SelfUpdateEnv, when set, stops `zoomies upgrade` fetching a binary. install.sh
// sets it because it has just installed one, and the re-executed process sets
// it so that a binary which still looks out of date to itself cannot loop. It
// is an environment variable rather than a flag so a release that predates it
// ignores it instead of refusing an unknown flag.
const SelfUpdateEnv = "ZOOMIES_NO_SELF_UPDATE"

// SelfUpdateOptions says which binary to bring up to date and from where.
type SelfUpdateOptions struct {
	// BinaryPath is the binary the service runs, which is what gets replaced.
	BinaryPath string
	// Version pins a tag, or "dev" for the rolling build. Empty follows the
	// channel this binary was built from: the newest release, or dev.
	Version string
	// Current is the version this binary reports, for the downgrade guard.
	Current string
	// BaseURL is the releases URL; install.sh's ZOOMIES_BASE_URL and
	// ZOOMIES_REPO decide it when empty.
	BaseURL string
	Out     io.Writer
	Client  *http.Client
	// Check only says what would change.
	Check bool

	goos, goarch string
	run          commandRunner
}

// SelfUpdateResult says what was found and what was done about it.
type SelfUpdateResult struct {
	Tag     string
	Updated bool
	// Newer is set by a check when a different build is available.
	Newer bool
}

func (o *SelfUpdateOptions) defaults() {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 5 * time.Minute}
	}
	if o.goos == "" {
		o.goos = runtime.GOOS
	}
	if o.goarch == "" {
		o.goarch = runtime.GOARCH
	}
	if o.run == nil {
		o.run = runCommand
	}
	if o.BaseURL == "" {
		o.BaseURL = os.Getenv("ZOOMIES_BASE_URL")
	}
	if o.BaseURL == "" {
		repo := os.Getenv("ZOOMIES_REPO")
		if repo == "" {
			repo = "eyupio/zoomies"
		}
		o.BaseURL = "https://github.com/" + repo + "/releases"
	}
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
}

// SelfUpdate replaces the installed binary with the newest published one when
// it differs, and refuses anything it could not verify. It is the download half
// of install.sh, so `zoomies upgrade` on its own leaves a host on the current
// release rather than restarting the build it already had.
//
// Whether a build is out of date is decided by hashing the installed file
// against the release's checksums.txt, not by comparing version strings: a
// rolling dev build carries no version worth comparing, and a hash cannot be
// fooled by two builds of one tag.
func SelfUpdate(ctx context.Context, opts SelfUpdateOptions) (SelfUpdateResult, error) {
	opts.defaults()
	var res SelfUpdateResult
	if opts.goos != "linux" && opts.goos != "darwin" {
		return res, nil
	}
	tag, err := opts.target(ctx)
	if err != nil {
		return res, err
	}
	res.Tag = tag
	if tag == "" {
		return res, nil
	}
	if tag != "dev" && opts.Current != "" && version.CompareBuilds(opts.Current, tag) == version.SkewAhead {
		// Never turn `upgrade` into a downgrade: an older build may not read
		// a database a newer one has migrated.
		return res, nil
	}
	asset := "zoomies_" + opts.goos + "_" + opts.goarch
	sums, err := opts.fetchText(ctx, opts.BaseURL+"/download/"+tag+"/checksums.txt")
	if err != nil {
		return res, fmt.Errorf("could not fetch the checksums for %s, so no binary was downloaded: %w", tag, err)
	}
	want := checksumFor(sums, asset)
	if want == "" {
		return res, fmt.Errorf("the checksums for %s have no entry for %s, so no binary was downloaded", tag, asset)
	}
	have, err := fileSHA256(opts.BinaryPath)
	if err != nil {
		return res, fmt.Errorf("read the installed binary: %w", err)
	}
	if have == want {
		return res, nil
	}
	res.Newer = true
	if opts.Check {
		return res, nil
	}
	dir := filepath.Dir(opts.BinaryPath)
	tmp, err := os.CreateTemp(dir, ".zoomies-update-*")
	if err != nil {
		return res, fmt.Errorf("cannot write to %s: %w; run the upgrade as root (sudo zoomies upgrade), or use install.sh --upgrade --prefix for another directory", dir, err)
	}
	defer os.Remove(tmp.Name())
	got, err := opts.download(ctx, opts.BaseURL+"/download/"+tag+"/"+asset, tmp)
	_ = tmp.Close()
	if err != nil {
		return res, fmt.Errorf("download %s %s: %w", asset, tag, err)
	}
	if got != want {
		return res, fmt.Errorf("checksum mismatch for %s %s (expected %s, got %s); the installed binary was left in place. Try again, and report it if it happens twice", asset, tag, want[:12], got[:12])
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return res, err
	}
	// A file that hashes right but will not run -- the wrong architecture
	// behind a matching sums file on a mirror -- must not replace one that does.
	if _, err := opts.run(ctx, tmp.Name(), "version", "--short"); err != nil {
		return res, fmt.Errorf("the downloaded binary will not run on this host; the installed one was left in place: %w", err)
	}
	if err := os.Rename(tmp.Name(), opts.BinaryPath); err != nil {
		return res, fmt.Errorf("replace %s: %w", opts.BinaryPath, err)
	}
	res.Updated = true
	return res, nil
}

// target is the tag to follow: the one asked for, the rolling dev build for a
// binary that came from main, or the newest release.
func (o *SelfUpdateOptions) target(ctx context.Context) (string, error) {
	if v := strings.TrimSpace(o.Version); v != "" {
		if v == "dev" || strings.HasPrefix(v, "v") {
			return v, nil
		}
		return "v" + v, nil
	}
	channel, ok := version.InstallTag(o.Current)
	switch {
	case !ok:
		// A local build has no channel to follow, and replacing it with a
		// published one would throw away whatever the developer is testing.
		return "", nil
	case channel == "dev":
		return "dev", nil
	}
	return o.latestTag(ctx)
}

// latestTag reads the tag from the /releases/latest redirect, which spends no
// API quota, and falls back to the release list for the period in which every
// release is a pre-release and the redirect names no tag.
func (o *SelfUpdateOptions) latestTag(ctx context.Context) (string, error) {
	client := *o.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.BaseURL+"/latest", nil)
	if err == nil {
		req.Header.Set("User-Agent", version.UserAgent())
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
			if loc := resp.Header.Get("Location"); strings.Contains(loc, "/tag/") {
				return loc[strings.LastIndex(loc, "/tag/")+len("/tag/"):], nil
			}
		}
	}
	// Only github.com has this API shape; a mirror gets the error instead of a
	// guess at what its API might be.
	const prefix = "https://github.com/"
	if !strings.HasPrefix(o.BaseURL, prefix) || !strings.HasSuffix(o.BaseURL, "/releases") {
		return "", fmt.Errorf("could not work out the latest release from %s; pass --version v1.2.3", o.BaseURL)
	}
	repo := strings.TrimSuffix(strings.TrimPrefix(o.BaseURL, prefix), "/releases")
	body, err := o.fetchText(ctx, "https://api.github.com/repos/"+repo+"/releases?per_page=20")
	if err != nil {
		return "", fmt.Errorf("could not work out the latest release: %w; pass --version v1.2.3", err)
	}
	var list []struct {
		Tag string `json:"tag_name"`
	}
	if err := json.Unmarshal([]byte(body), &list); err == nil {
		for _, r := range list {
			if strings.HasPrefix(r.Tag, "v") {
				return r.Tag, nil
			}
		}
	}
	return "", fmt.Errorf("could not work out the latest release; pass --version v1.2.3")
}

func (o *SelfUpdateOptions) fetchText(ctx context.Context, u string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	resp, err := o.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered %s", redact(u), resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return string(b), err
}

// download streams the body to w and returns its SHA-256, so the hash is of
// what was written rather than of a second read.
func (o *SelfUpdateOptions) download(ctx context.Context, u string, w io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	resp, err := o.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered %s", redact(u), resp.Status)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, h), resp.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func redact(u string) string {
	if p, err := url.Parse(u); err == nil {
		p.User, p.RawQuery = nil, ""
		return p.String()
	}
	return u
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checksumFor finds an asset's hash in a `sha256sum` style file.
func checksumFor(sums, asset string) string {
	for _, line := range strings.Split(sums, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && (f[1] == asset || f[1] == "*"+asset) {
			return strings.ToLower(f[0])
		}
	}
	return ""
}
