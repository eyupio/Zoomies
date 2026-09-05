package backend

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/eyupio/zoomies/internal/store"
)

// cacheSource derives an isolated daemon volume or host directory without
// placing unchecked pool/repository values into it.
func cacheSource(spec Spec) (string, error) {
	c := spec.Cache
	if !c.Enabled {
		return "", nil
	}
	if !c.Scope.Valid() {
		return "", fmt.Errorf("backend: %q is not a cache scope", c.Scope)
	}
	key := spec.PoolID
	if c.Scope == store.CacheScopeRepository {
		// The repository comes from the pool's cache configuration when the
		// installation is organisation-wide and from the installation itself
		// when it targets one repository; either way it has to be a full
		// owner/name here, or two repositories would share a cache.
		repo := firstNonEmpty(strings.TrimSpace(c.Repository), spec.Repository)
		if !strings.Contains(repo, "/") {
			return "", fmt.Errorf("backend: a repository-scoped cache needs a repository as owner/name, and this pool has %q; set the pool's cache repository", repo)
		}
		key += "-" + repo
	}
	safe := sanitizeHostname(key)
	if safe == "" || safe == "." || safe == ".." {
		return "", fmt.Errorf("backend: unsafe cache identity")
	}
	prefix := strings.TrimSpace(c.Source)
	if strings.Contains(prefix, "..") {
		return "", fmt.Errorf("backend: unsafe cache source %q: path traversal is not allowed", prefix)
	}
	if prefix == "" {
		prefix = "zoomies-cache"
	}
	if filepath.IsAbs(prefix) {
		return filepath.Join(prefix, safe), nil
	}
	if strings.ContainsAny(prefix, `/\\:`) {
		return "", fmt.Errorf("backend: unsafe cache volume prefix %q", prefix)
	}
	return prefix + "-" + safe, nil
}

// cacheDirectory returns the host directory a cache lives in, and whether there
// is one at all. A named daemon volume has no path this agent can measure: its
// bytes are the daemon's business, on a filesystem the agent may not even
// share. That is the whole reason a size limit is only accepted alongside an
// absolute cache source.
func cacheDirectory(spec Spec) (string, bool) {
	source, err := cacheSource(spec)
	if err != nil || source == "" || !filepath.IsAbs(source) {
		return "", false
	}
	return source, true
}

// pruneCache brings a cache directory back under its configured limit by
// deleting whole top-level entries, least recently modified first.
//
// This runs before a runner starts rather than on a timer, because that is the
// moment the cache is guaranteed to be idle: a job holding a file open while we
// delete the directory under it is the failure this ordering designs out. It is
// a ceiling on how far over the limit a host can drift, not a quota -- a single
// job can still fill a disk between two runners -- so the limit belongs to a
// cache the operator is willing to lose, which is what this cache is.
func pruneCache(dir string, limit int64, log *slog.Logger) {
	if limit <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// A cache that does not exist yet is not over its limit; anything else
		// is worth saying once, because it means the limit is not being kept.
		if !os.IsNotExist(err) {
			log.Warn("could not read the cache directory to enforce its size limit",
				"dir", dir, "error", err)
		}
		return
	}
	type entry struct {
		name  string
		size  int64
		mtime int64
	}
	items := make([]entry, 0, len(entries))
	var total int64
	for _, e := range entries {
		size, mtime := treeSize(filepath.Join(dir, e.Name()))
		items = append(items, entry{name: e.Name(), size: size, mtime: mtime})
		total += size
	}
	if total <= limit {
		return
	}
	// Oldest first, so the entry a job is most likely to want next survives
	// longest. Ties break by name to keep the order stable across hosts.
	sort.Slice(items, func(i, j int) bool {
		if items[i].mtime == items[j].mtime {
			return items[i].name < items[j].name
		}
		return items[i].mtime < items[j].mtime
	})
	freed, removed := int64(0), 0
	for _, it := range items {
		if total-freed <= limit {
			break
		}
		if err := os.RemoveAll(filepath.Join(dir, it.name)); err != nil {
			log.Warn("could not evict a cache entry", "dir", dir, "entry", it.name, "error", err)
			continue
		}
		freed += it.size
		removed++
	}
	log.Info("evicted cache entries to stay under the pool's cache size limit",
		"dir", dir, "limit_bytes", limit, "was_bytes", total, "freed_bytes", freed, "entries", removed)
}

// treeSize sums the apparent size of a directory tree and reports the newest
// modification time in it, so that touching one file inside a cache entry keeps
// the whole entry warm.
func treeSize(path string) (size int64, newest int64) {
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			// An entry that vanished mid-walk simply contributes nothing.
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if mod := info.ModTime().UnixMilli(); mod > newest {
			newest = mod
		}
		if !d.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, newest
}
