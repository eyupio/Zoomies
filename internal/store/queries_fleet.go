package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Page describes server-side pagination and sorting for a list query.
type Page struct {
	Limit  int
	Offset int
	Sort   string // column name, validated against an allow-list per query
	Desc   bool
}

func (p Page) limit(def, max int) int {
	if p.Limit <= 0 {
		return def
	}
	if p.Limit > max {
		return max
	}
	return p.Limit
}

// orderBy validates p.Sort against allowed and renders an ORDER BY clause.
// An unknown sort column falls back to fallback rather than erroring, because
// a stale bookmarked URL should not break the page.
func (p Page) orderBy(allowed map[string]string, fallback string) string {
	col, ok := allowed[p.Sort]
	if !ok {
		return fallback
	}
	if p.Desc {
		return col + " DESC"
	}
	return col + " ASC"
}

// ---------------------------------------------------------------------------
// Installations
// ---------------------------------------------------------------------------

const installationCols = `id, app_id, installation_id, target, target_type, api_base_url,
	upload_base_url, private_key_enc, webhook_secret_enc, app_slug, last_checked_at,
	last_error, created_at, updated_at`

func scanInstallation(sc interface{ Scan(...any) error }) (*Installation, error) {
	var i Installation
	var created, updated int64
	var checked sql.NullInt64
	err := sc.Scan(&i.ID, &i.AppID, &i.InstallationID, &i.Target, &i.TargetType,
		&i.APIBaseURL, &i.UploadBaseURL, &i.PrivateKeyEnc, &i.WebhookSecretEnc,
		&i.AppSlug, &checked, &i.LastError, &created, &updated)
	if err != nil {
		return nil, err
	}
	i.CreatedAt, i.UpdatedAt, i.LastCheckedAt = at(created), at(updated), atp(checked)
	return &i, nil
}

// CreateInstallation inserts a new GitHub App installation.
func (s *Store) CreateInstallation(ctx context.Context, i *Installation) error {
	if i.ID == "" {
		i.ID = NewID(PrefixInstallation)
	}
	now := s.Now()
	i.CreatedAt, i.UpdatedAt = now, now
	i.Target = NormalizeTarget(i.Target)
	_, err := s.exec(ctx, `INSERT INTO installations (`+installationCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		i.ID, i.AppID, i.InstallationID, i.Target, string(i.TargetType), i.APIBaseURL,
		i.UploadBaseURL, i.PrivateKeyEnc, i.WebhookSecretEnc, i.AppSlug,
		msp(i.LastCheckedAt), i.LastError, ms(i.CreatedAt), ms(i.UpdatedAt))
	return wrapWrite(err)
}

// GetInstallation returns one installation by ID.
func (s *Store) GetInstallation(ctx context.Context, id string) (*Installation, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+installationCols+` FROM installations WHERE id = ?`, id)
	i, err := scanInstallation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("installation %s: %w", id, ErrNotFound)
	}
	return i, err
}

// NormalizeTarget folds an installation target to the form it is stored and
// compared in: trimmed and lower-cased. GitHub owner and repository names are
// case-insensitive, so "Acme/Widgets" and "acme/widgets" are one target.
func NormalizeTarget(target string) string {
	return strings.ToLower(strings.TrimSpace(target))
}

// FindInstallationByTarget resolves the installation that owns a repo or org.
// It prefers an exact repo match and falls back to the owning organisation, so
// a webhook for acme/widgets resolves against an org-wide installation.
func (s *Store) FindInstallationByTarget(ctx context.Context, repoFullName string) (*Installation, error) {
	owner := repoFullName
	if i := strings.IndexByte(repoFullName, '/'); i > 0 {
		owner = repoFullName[:i]
	}
	// GitHub logins are case-insensitive and a delivery carries the canonical
	// case, so the comparison folds both sides: a target an operator typed as
	// "Acme" has to match "acme/widgets". LOWER on the column rather than a
	// migration, because rows written before targets were folded on write are
	// still out there. Two installations covering the same target are ordered
	// by age so the answer is the same on every call rather than whichever
	// row SQLite reached first.
	row := s.read.QueryRowContext(ctx, `SELECT `+installationCols+` FROM installations
		WHERE (target_type = 'repo' AND LOWER(target) = ?) OR (target_type = 'org' AND LOWER(target) = ?)
		ORDER BY CASE target_type WHEN 'repo' THEN 0 ELSE 1 END, created_at, id LIMIT 1`,
		strings.ToLower(repoFullName), strings.ToLower(owner))
	i, err := scanInstallation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("no installation covers %s: %w", repoFullName, ErrNotFound)
	}
	return i, err
}

// ListInstallations returns every installation, oldest first.
func (s *Store) ListInstallations(ctx context.Context) ([]*Installation, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+installationCols+` FROM installations ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Installation
	for rows.Next() {
		i, err := scanInstallation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// UpdateInstallation persists changes to an existing installation.
func (s *Store) UpdateInstallation(ctx context.Context, i *Installation) error {
	i.UpdatedAt = s.Now()
	i.Target = NormalizeTarget(i.Target)
	res, err := s.exec(ctx, `UPDATE installations SET app_id=?, installation_id=?, target=?,
		target_type=?, api_base_url=?, upload_base_url=?, private_key_enc=?, webhook_secret_enc=?,
		app_slug=?, last_checked_at=?, last_error=?, updated_at=? WHERE id=?`,
		i.AppID, i.InstallationID, i.Target, string(i.TargetType), i.APIBaseURL,
		i.UploadBaseURL, i.PrivateKeyEnc, i.WebhookSecretEnc, i.AppSlug,
		msp(i.LastCheckedAt), i.LastError, ms(i.UpdatedAt), i.ID)
	if err != nil {
		return wrapWrite(err)
	}
	return affected(res, "installation", i.ID)
}

// SetInstallationHealth records the outcome of a credential probe.
func (s *Store) SetInstallationHealth(ctx context.Context, id string, errMsg string) error {
	_, err := s.exec(ctx, `UPDATE installations SET last_checked_at=?, last_error=?, updated_at=? WHERE id=?`,
		ms(s.Now()), errMsg, ms(s.Now()), id)
	return err
}

// SetInstallationAppSlug records the App's slug, which is learned from GitHub
// rather than supplied.
//
// An installation added by hand carries an App ID and nothing else -- the slug
// is not on the form, because an operator reading it off a settings page is one
// more thing to mistype. The first credential probe knows it, and it is what
// every link to the App on GitHub is built from, including the page where its
// avatar is uploaded. The write is skipped when nothing changed so a probe on a
// healthy installation does not bump updated_at every minute.
func (s *Store) SetInstallationAppSlug(ctx context.Context, id, slug string) error {
	_, err := s.exec(ctx, `UPDATE installations SET app_slug=?, updated_at=? WHERE id=? AND app_slug<>?`,
		slug, ms(s.Now()), id, slug)
	return err
}

// DeleteInstallation removes an installation and, by cascade, its pools.
// DeleteInstallation removes an installation, its pools and their runners, and
// returns the IDs of the runner rows that went, so the caller can announce each
// one: a row the schema cascades away silently is a row the Runners page keeps
// showing until it is reloaded.
func (s *Store) DeleteInstallation(ctx context.Context, id string) ([]string, error) {
	var runners []string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		err := recordSessionsWhere(ctx, tx, s.Now().UnixMilli(),
			`r.pool_id IN (SELECT id FROM pools WHERE installation_id = ?2)`, id)
		if err != nil {
			return err
		}
		runners, err = deletedIDs(ctx, tx,
			`DELETE FROM runners WHERE pool_id IN (SELECT id FROM pools WHERE installation_id = ?) RETURNING id`, id)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM installations WHERE id = ?`, id)
		if err != nil {
			return err
		}
		return affected(res, "installation", id)
	})
	return runners, err
}

// deletedIDs runs a DELETE ... RETURNING id and collects what went.
func deletedIDs(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ---------------------------------------------------------------------------
// Pools
// ---------------------------------------------------------------------------

const poolCols = `id, name, installation_id, labels, runner_group, backend, os, os_version,
	arch, image, pull_policy, runner_version, min_runners, max_runners, priority,
	idle_timeout_ms, ephemeral, docker_mode, resources, cache, host_selector, env,
	run_as_root, enabled, created_at, updated_at, repository_scale_up_limit,
	cost_per_runner_hour, runner_settings, cpu_burst, no_default_labels`

func scanPool(sc interface{ Scan(...any) error }) (*Pool, error) {
	var p Pool
	var idle, created, updated int64
	var ephemeral, runAsRoot, enabled, noDefaultLabels int
	var resources, cache, runnerSettings, cpuBurst string
	err := sc.Scan(&p.ID, &p.Name, &p.InstallationID, &p.Labels, &p.RunnerGroup, &p.Backend,
		&p.Platform.OS, &p.Platform.OSVersion, &p.Platform.Arch,
		&p.Image, &p.PullPolicy, &p.RunnerVersion, &p.MinRunners, &p.MaxRunners, &p.Priority,
		&idle, &ephemeral, &p.DockerMode, &resources, &cache, &p.HostSelector, &p.Env,
		&runAsRoot, &enabled, &created, &updated, &p.RepositoryScaleUpLimit, &p.CostPerRunnerHour,
		&runnerSettings, &cpuBurst, &noDefaultLabels)
	if err != nil {
		return nil, err
	}
	p.NoDefaultLabels = noDefaultLabels == 1
	p.IdleTimeout = Duration(time.Duration(idle) * time.Millisecond)
	p.Ephemeral, p.RunAsRoot, p.Enabled = ephemeral == 1, runAsRoot == 1, enabled == 1
	p.CreatedAt, p.UpdatedAt = at(created), at(updated)
	if err := unmarshalJSON(resources, &p.Resources); err != nil {
		return nil, fmt.Errorf("pool %s: decoding resources: %w", p.ID, err)
	}
	if err := unmarshalJSON(cache, &p.Cache); err != nil {
		return nil, fmt.Errorf("pool %s: decoding cache: %w", p.ID, err)
	}
	if err := unmarshalJSON(runnerSettings, &p.RunnerSettings); err != nil {
		return nil, fmt.Errorf("pool %s: decoding runner settings: %w", p.ID, err)
	}
	if err := unmarshalJSON(cpuBurst, &p.CPUBurst); err != nil {
		return nil, fmt.Errorf("pool %s: decoding CPU burst policy: %w", p.ID, err)
	}
	return &p, nil
}

// CreatePool inserts a pool. The name is branded and the labels normalised on
// the way in, here rather than only in the handler, so that no caller -- the
// API, the installer, the seeder -- can put a pool in the database that GitHub
// would show under a name saying nothing about which fleet it belongs to. The
// scheduler then never has to think about case or whitespace either.
func (s *Store) CreatePool(ctx context.Context, p *Pool) error {
	query, args, err := s.poolInsert(p)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, query, args...)
	return wrapWrite(err)
}

// poolInsert normalises a new pool and returns the statement that stores it,
// so that a pool created on its own and one created as part of an import are
// the same row.
func (s *Store) poolInsert(p *Pool) (string, []any, error) {
	if p.ID == "" {
		p.ID = NewID(PrefixPool)
	}
	now := s.Now()
	p.CreatedAt, p.UpdatedAt = now, now
	p.Name = BrandedName(p.Name)
	p.Labels = NormalizeLabels(p.Labels)
	p.Platform = p.Platform.Normalized()
	if p.PullPolicy == "" {
		p.PullPolicy = PullIfNotPresent
	}
	res, cache, settings, burst, err := poolJSON(p)
	if err != nil {
		return "", nil, err
	}
	return `INSERT INTO pools (` + poolCols + `) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, []any{
		p.ID, p.Name, p.InstallationID, p.Labels, p.RunnerGroup, string(p.Backend),
		p.Platform.OS, p.Platform.OSVersion, p.Platform.Arch, p.Image,
		string(p.PullPolicy),
		p.RunnerVersion, p.MinRunners, p.MaxRunners, p.Priority, p.IdleTimeout.Duration().Milliseconds(),
		boolInt(p.Ephemeral), string(p.DockerMode), res, cache, p.HostSelector, p.Env,
		boolInt(p.RunAsRoot), boolInt(p.Enabled), ms(p.CreatedAt), ms(p.UpdatedAt),
		p.RepositoryScaleUpLimit, p.CostPerRunnerHour, settings, burst, boolInt(p.NoDefaultLabels)}, nil
}

// poolJSON encodes the four columns a pool keeps as JSON documents.
func poolJSON(p *Pool) (res, cache, settings, burst string, err error) {
	if res, err = marshalJSON(p.Resources); err != nil {
		return
	}
	if cache, err = marshalJSON(p.Cache); err != nil {
		return
	}
	if settings, err = marshalJSON(p.RunnerSettings); err != nil {
		return
	}
	burst, err = marshalJSON(p.CPUBurst)
	return
}

// GetPool returns a pool by ID.
func (s *Store) GetPool(ctx context.Context, id string) (*Pool, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+poolCols+` FROM pools WHERE id = ?`, id)
	p, err := scanPool(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("pool %s: %w", id, ErrNotFound)
	}
	return p, err
}

// GetPoolByName returns a pool by its unique name.
func (s *Store) GetPoolByName(ctx context.Context, name string) (*Pool, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+poolCols+` FROM pools WHERE name = ?`, name)
	p, err := scanPool(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("pool %q: %w", name, ErrNotFound)
	}
	return p, err
}

// CountPools counts pools, for limits.pools.
func (s *Store) CountPools(ctx context.Context) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM pools`).Scan(&n)
	return n, err
}

// CountHosts counts enrolled hosts, for limits.hosts.
func (s *Store) CountHosts(ctx context.Context) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM hosts`).Scan(&n)
	return n, err
}

// ListPools returns all pools ordered by name.
func (s *Store) ListPools(ctx context.Context) ([]*Pool, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+poolCols+` FROM pools ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Pool
	for rows.Next() {
		p, err := scanPool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdatePool persists changes to a pool. The name is branded on the way in for
// the reason CreatePool brands it, which also means a pool carried over from a
// build that did not brand names gains the prefix the next time it is edited.
func (s *Store) UpdatePool(ctx context.Context, p *Pool) error {
	query, args, err := s.poolUpdate(p)
	if err != nil {
		return err
	}
	r, err := s.exec(ctx, query, args...)
	if err != nil {
		return wrapWrite(err)
	}
	return affected(r, "pool", p.ID)
}

// poolUpdate is poolInsert's counterpart for a pool that already exists.
func (s *Store) poolUpdate(p *Pool) (string, []any, error) {
	p.UpdatedAt = s.Now()
	p.Name = BrandedName(p.Name)
	p.Labels = NormalizeLabels(p.Labels)
	p.Platform = p.Platform.Normalized()
	if p.PullPolicy == "" {
		p.PullPolicy = PullIfNotPresent
	}
	res, cache, settings, burst, err := poolJSON(p)
	if err != nil {
		return "", nil, err
	}
	// The arguments are built apart from the query rather than as a literal
	// opened on the raw string's closing line: two builds of gofmt 1.27.1
	// indent that literal differently, so whichever one formatted it, the
	// other -- CI's or a contributor's -- reported the file as unformatted.
	args := []any{
		p.Name, p.InstallationID, p.Labels, p.RunnerGroup, string(p.Backend),
		p.Platform.OS, p.Platform.OSVersion, p.Platform.Arch, p.Image,
		string(p.PullPolicy),
		p.RunnerVersion, p.MinRunners, p.MaxRunners, p.Priority, p.IdleTimeout.Duration().Milliseconds(),
		boolInt(p.Ephemeral), string(p.DockerMode), res, cache, p.HostSelector, p.Env,
		boolInt(p.RunAsRoot), boolInt(p.Enabled), ms(p.UpdatedAt), p.RepositoryScaleUpLimit,
		p.CostPerRunnerHour, settings, burst, boolInt(p.NoDefaultLabels), p.ID,
	}
	query := `UPDATE pools SET name=?, installation_id=?, labels=?, runner_group=?,
		backend=?, os=?, os_version=?, arch=?, image=?, pull_policy=?, runner_version=?,
		min_runners=?, max_runners=?, priority=?, idle_timeout_ms=?, ephemeral=?,
		docker_mode=?, resources=?, cache=?, host_selector=?, env=?, run_as_root=?,
		enabled=?, updated_at=?, repository_scale_up_limit=?, cost_per_runner_hour=?,
		runner_settings=?, cpu_burst=?, no_default_labels=? WHERE id=?`
	return query, args, nil
}

// ApplyPools creates and updates pools as one transaction: every row is
// written or none is. It is what an import of pools applies through, because
// a document that half-landed -- three pools created and the fourth refused
// by the database -- leaves a fleet that matches neither the file nor what
// was there before, and nobody can say which of the two to go back to.
func (s *Store) ApplyPools(ctx context.Context, create, update []*Pool) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		for _, p := range create {
			query, args, err := s.poolInsert(p)
			if err != nil {
				return err
			}
			if _, err := t.ExecContext(ctx, query, args...); err != nil {
				return wrapWrite(err)
			}
		}
		for _, p := range update {
			query, args, err := s.poolUpdate(p)
			if err != nil {
				return err
			}
			r, err := t.ExecContext(ctx, query, args...)
			if err != nil {
				return wrapWrite(err)
			}
			if err := affected(r, "pool", p.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeletePool removes a pool and, by cascade, its runner rows and any queued
// demand still matched to it.
//
// Demand goes the same way an operator removes it from the Queue page --
// provisioning=deleted, reversible from the Removed view -- rather than the
// row disappearing outright, because nothing else here ever deletes a job
// wholesale. It is also unmatched (pool_id and matched cleared): a pool_id
// pointing at a pool row that no longer exists is a dangling reference
// nothing else corrects, and a resume that tried to match it back to a pool
// that is gone would have nothing to restore. A job already being cancelled
// is left to that flow rather than raced with it.
//
// It returns the IDs of the runners and the jobs it took with it, so each can
// be announced.
func (s *Store) DeletePool(ctx context.Context, id string) (runners, jobs []string, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		terr := recordSessionsWhere(ctx, tx, s.Now().UnixMilli(), `r.pool_id = ?2`, id)
		if terr != nil {
			return terr
		}
		runners, terr = deletedIDs(ctx, tx, `DELETE FROM runners WHERE pool_id = ? RETURNING id`, id)
		if terr != nil {
			return terr
		}
		jobs, terr = deletedIDs(ctx, tx,
			`UPDATE jobs SET provisioning=?, pool_id='', matched=0
			 WHERE pool_id = ? AND `+queuedJobSQL("jobs")+` RETURNING id`,
			ProvisioningDeleted, id)
		if terr != nil {
			return terr
		}
		res, terr := tx.ExecContext(ctx, `DELETE FROM pools WHERE id = ?`, id)
		if terr != nil {
			return terr
		}
		return affected(res, "pool", id)
	})
	return runners, jobs, err
}

func (s *Store) SetPoolPrewarm(ctx context.Context, poolID, hostID, image, state, digest, failure string) error {
	_, err := s.exec(ctx, `INSERT INTO pool_prewarms(pool_id,host_id,image,state,digest,error,updated_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(pool_id,host_id) DO UPDATE SET image=excluded.image,state=excluded.state,digest=excluded.digest,error=excluded.error,updated_at=excluded.updated_at`,
		poolID, hostID, image, state, digest, failure, ms(s.Now()))
	return err
}

func (s *Store) ListPoolPrewarms(ctx context.Context, poolID string) ([]PoolPrewarm, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT p.pool_id,p.host_id,h.name,p.image,p.state,p.digest,p.error,p.updated_at FROM pool_prewarms p JOIN hosts h ON h.id=p.host_id WHERE p.pool_id=? ORDER BY h.name`, poolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PoolPrewarm
	for rows.Next() {
		var x PoolPrewarm
		var updated int64
		if err := rows.Scan(&x.PoolID, &x.HostID, &x.HostName, &x.Image, &x.State, &x.Digest, &x.Error, &updated); err != nil {
			return nil, err
		}
		x.UpdatedAt = at(updated)
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) SetRunnerImageDigest(ctx context.Context, id, digest string) error {
	_, err := s.exec(ctx, `UPDATE runners SET image_digest=? WHERE id=?`, digest, id)
	return err
}

// PoolCounts is the per-state runner tally used by the scheduler and the UI's
// utilisation bars.
type PoolCounts struct {
	PoolID       string `json:"pool_id"`
	Provisioning int    `json:"provisioning"`
	Registering  int    `json:"registering"`
	Idle         int    `json:"idle"`
	Busy         int    `json:"busy"`
	Draining     int    `json:"draining"`
	Failed       int    `json:"failed"`
}

// Live returns the number of runners that exist or are being created.
func (c PoolCounts) Live() int {
	return c.Provisioning + c.Registering + c.Idle + c.Busy + c.Draining
}

// Total returns every non-removed runner including failures awaiting cleanup.
func (c PoolCounts) Total() int { return c.Live() + c.Failed }

// Utilisation returns busy/live in the range [0,1].
func (c PoolCounts) Utilisation() float64 {
	live := c.Live()
	if live == 0 {
		return 0
	}
	return float64(c.Busy) / float64(live)
}

// CountRunnersByPool returns per-state runner counts for every pool.
func (s *Store) CountRunnersByPool(ctx context.Context) (map[string]PoolCounts, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT pool_id, state, COUNT(*) FROM runners
		WHERE state != 'removed' GROUP BY pool_id, state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]PoolCounts{}
	for rows.Next() {
		var pool, state string
		var n int
		if err := rows.Scan(&pool, &state, &n); err != nil {
			return nil, err
		}
		c := out[pool]
		c.PoolID = pool
		switch RunnerState(state) {
		case RunnerProvisioning:
			c.Provisioning = n
		case RunnerRegistering:
			c.Registering = n
		case RunnerIdle:
			c.Idle = n
		case RunnerBusy:
			c.Busy = n
		case RunnerDraining:
			c.Draining = n
		case RunnerFailed:
			c.Failed = n
		}
		out[pool] = c
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Hosts
// ---------------------------------------------------------------------------

const hostCols = `id, name, address, embedded, capacity, backends, backend_info, labels,
	os, distro, os_version, arch, cpus, memory_mb, version, cordoned, token_hash,
	last_heartbeat, created_at, agent_session_id, agent_session_prev,
	agent_session_alternations, agent_session_alt_at,
	disk_total_mb, disk_free_mb, reserve_cpus, reserve_memory_mb, reserve_disk_mb,
	protocol_version, incompatible, connection, usage, throttle, features, incidents`

func scanHost(sc interface{ Scan(...any) error }) (*Host, error) {
	var h Host
	var embedded, cordoned, incompatible int
	var heartbeat, created int64
	var altAt sql.NullInt64
	err := sc.Scan(&h.ID, &h.Name, &h.Address, &embedded, &h.Capacity, &h.Backends,
		&h.BackendInfo, &h.Labels, &h.OS, &h.Distro, &h.OSVersion, &h.Arch, &h.CPUs,
		&h.MemoryMB, &h.Version, &cordoned, &h.TokenHash, &heartbeat, &created,
		&h.AgentSessionID, &h.AgentSessionPrev, &h.AgentSessionAlternations, &altAt,
		&h.DiskTotalMB, &h.DiskFreeMB, &h.ReserveCPUs, &h.ReserveMemoryMB, &h.ReserveDiskMB,
		&h.ProtocolVersion, &incompatible, &h.Connection, &h.Usage, &h.Throttle, &h.Features, &h.Incidents)
	if err != nil {
		return nil, err
	}
	h.Embedded, h.Cordoned, h.Incompatible = embedded == 1, cordoned == 1, incompatible == 1
	h.LastHeartbeat, h.CreatedAt = at(heartbeat), at(created)
	h.AgentSessionAltAt = atp(altAt)
	return &h, nil
}

// CreateHost registers a new agent host.
func (s *Store) CreateHost(ctx context.Context, h *Host) error {
	if h.ID == "" {
		h.ID = NewID(PrefixHost)
	}
	if h.Connection == "" {
		h.Connection = "direct"
	}
	h.CreatedAt = s.Now()
	if h.LastHeartbeat.IsZero() {
		h.LastHeartbeat = h.CreatedAt
	}
	_, err := s.exec(ctx, `INSERT INTO hosts (`+hostCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		h.ID, h.Name, h.Address, boolInt(h.Embedded), h.Capacity, h.Backends, h.BackendInfo,
		h.Labels, h.OS, h.Distro, h.OSVersion, h.Arch, h.CPUs, h.MemoryMB, h.Version,
		boolInt(h.Cordoned), h.TokenHash, ms(h.LastHeartbeat), ms(h.CreatedAt),
		h.AgentSessionID, h.AgentSessionPrev, h.AgentSessionAlternations, msp(h.AgentSessionAltAt),
		h.DiskTotalMB, h.DiskFreeMB, h.ReserveCPUs, h.ReserveMemoryMB, h.ReserveDiskMB,
		h.ProtocolVersion, boolInt(h.Incompatible), h.Connection, h.Usage, h.Throttle, h.Features, h.Incidents)
	return wrapWrite(err)
}

// GetHost returns one host with its live runner count filled in.
func (s *Store) GetHost(ctx context.Context, id string) (*Host, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+hostCols+` FROM hosts WHERE id = ?`, id)
	h, err := scanHost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("host %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return h, s.fillHostCounts(ctx, h)
}

// GetHostByName returns a host by its unique name.
func (s *Store) GetHostByName(ctx context.Context, name string) (*Host, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+hostCols+` FROM hosts WHERE name = ?`, name)
	h, err := scanHost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("host %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return h, s.fillHostCounts(ctx, h)
}

func (s *Store) fillHostCounts(ctx context.Context, hosts ...*Host) error {
	if len(hosts) == 0 {
		return nil
	}
	counts, err := s.countRunnersByHost(ctx)
	if err != nil {
		return err
	}
	for _, h := range hosts {
		h.ActiveRunners = counts[h.ID].live
		h.UnlimitedRunners = counts[h.ID].unlimited
	}
	return nil
}

// hostRunnerCounts is what one host's live rows add up to: how many there
// are, and how many of them were created with no CPU quota.
type hostRunnerCounts struct {
	live, unlimited int
}

func (s *Store) countRunnersByHost(ctx context.Context) (map[string]hostRunnerCounts, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT host_id, COUNT(*), SUM(allocated_cpus <= 0) FROM runners
		WHERE state NOT IN ('removed','failed') GROUP BY host_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]hostRunnerCounts{}
	for rows.Next() {
		var id string
		var n, unlimited int
		if err := rows.Scan(&id, &n, &unlimited); err != nil {
			return nil, err
		}
		out[id] = hostRunnerCounts{live: n, unlimited: unlimited}
	}
	return out, rows.Err()
}

// ListHosts returns every host, with live runner counts.
func (s *Store) ListHosts(ctx context.Context) ([]*Host, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+hostCols+` FROM hosts ORDER BY embedded DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Host
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.fillHostCounts(ctx, out...)
}

// FindHostByTokenHash authenticates an agent request.
func (s *Store) FindHostByTokenHash(ctx context.Context, hash string) (*Host, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+hostCols+` FROM hosts WHERE token_hash = ? AND token_hash != ''`, hash)
	h, err := scanHost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return h, err
}

// UpdateHost persists agent-reported host facts.
func (s *Store) UpdateHost(ctx context.Context, h *Host) error {
	// The reserves are deliberately absent: they are the operator's, set through
	// the API, and this is the path an agent's heartbeat takes. A host must not
	// be able to talk its way out of the room its operator held back for it.
	res, err := s.exec(ctx, `UPDATE hosts SET name=?, address=?, capacity=?, backends=?,
		backend_info=?, labels=?, os=?, distro=?, os_version=?, arch=?, cpus=?, memory_mb=?,
		version=?, cordoned=?, last_heartbeat=?, disk_total_mb=?, disk_free_mb=?,
		protocol_version=?, incompatible=? WHERE id=?`,
		h.Name, h.Address, h.Capacity, h.Backends, h.BackendInfo, h.Labels, h.OS, h.Distro,
		h.OSVersion, h.Arch, h.CPUs, h.MemoryMB, h.Version, boolInt(h.Cordoned),
		ms(h.LastHeartbeat), h.DiskTotalMB, h.DiskFreeMB,
		h.ProtocolVersion, boolInt(h.Incompatible), h.ID)
	if err != nil {
		return wrapWrite(err)
	}
	return affected(res, "host", h.ID)
}

// HostChanges contains only the operator's editable fields. Nil leaves the
// current value alone, including changes made since the caller read the host.
type HostChanges struct {
	Capacity        *int
	Labels          *StringMap
	ReserveCPUs     *int
	ReserveMemoryMB *int64
	ReserveDiskMB   *int64
}

// PatchHost writes one edit without overwriting a concurrent cordon or
// heartbeat. Capacity, labels and reserves succeed or fail together.
func (s *Store) PatchHost(ctx context.Context, id string, changes HostChanges) error {
	var labels any
	if changes.Labels != nil {
		labels = *changes.Labels
	}
	res, err := s.exec(ctx, `UPDATE hosts SET capacity=COALESCE(?,capacity),
		labels=COALESCE(?,labels), reserve_cpus=COALESCE(?,reserve_cpus),
		reserve_memory_mb=COALESCE(?,reserve_memory_mb), reserve_disk_mb=COALESCE(?,reserve_disk_mb)
		WHERE id=?`, changes.Capacity, labels, changes.ReserveCPUs,
		changes.ReserveMemoryMB, changes.ReserveDiskMB, id)
	if err != nil {
		return wrapWrite(err)
	}
	return affected(res, "host", id)
}

// SetHostReported writes the facts an agent measures about its own machine,
// and only those.
//
// Its own statement for the reason the reserves have one, carried the rest of
// the way. UpdateHost writes the whole row, and the heartbeat reaches it with a
// copy of the host read at the top of the request -- so an operator who
// cordoned the machine, changed its capacity, renamed it or edited its labels
// in the interval had the pre-edit values written back over their change, with
// no error and nothing in the log. A cordon is the one that bites: the row goes
// back to uncordoned, the UI still shows it cordoned because the cordon handler
// already published its own view, and the scheduler puts new runners on the
// machine somebody is about to power off.
//
// What an agent may say about itself is what it can measure: its backends, its
// version, its features, its processors, its memory, its disk. Everything else
// on a host is the operator's.
func (s *Store) SetHostReported(ctx context.Context, h *Host) error {
	res, err := s.exec(ctx, `UPDATE hosts SET backends=?, backend_info=?, version=?, features=?,
		cpus=?, memory_mb=?, disk_total_mb=?, disk_free_mb=?, last_heartbeat=? WHERE id=?`,
		h.Backends, h.BackendInfo, h.Version, h.Features, h.CPUs, h.MemoryMB,
		h.DiskTotalMB, h.DiskFreeMB, ms(h.LastHeartbeat), h.ID)
	if err != nil {
		return wrapWrite(err)
	}
	return affected(res, "host", h.ID)
}

// SetHostProtocol records what agent protocol a host speaks and whether this
// controller can work with it.
//
// Its own statement rather than a field of UpdateHost, for the same reason the
// reserves have one: UpdateHost writes the whole row, and folding this into it
// would make a protocol change carry every other figure the heartbeat brought
// with it -- including a free-disk drift the tolerance exists to ignore. A
// heartbeat that only learnt the protocol should write only the protocol.
func (s *Store) SetHostProtocol(ctx context.Context, id string, version int, incompatible bool) error {
	res, err := s.exec(ctx, `UPDATE hosts SET protocol_version=?, incompatible=? WHERE id=?`,
		version, boolInt(incompatible), id)
	if err != nil {
		return wrapWrite(err)
	}
	return affected(res, "host", id)
}

// SetHostReserve records what an operator wants held back from placement on a
// host, for the machine's own sake rather than for any pool's.
//
// Its own statement, because UpdateHost deliberately cannot touch these: that
// is the path an agent's heartbeat takes, and a host must not be able to talk
// its way out of the room its operator kept for it. Capacity has the same shape
// for the same reason.
func (s *Store) SetHostReserve(ctx context.Context, id string, cpus int, memoryMB, diskMB int64) error {
	res, err := s.exec(ctx, `UPDATE hosts SET reserve_cpus=?, reserve_memory_mb=?, reserve_disk_mb=?
		WHERE id=?`, cpus, memoryMB, diskMB, id)
	if err != nil {
		return wrapWrite(err)
	}
	return affected(res, "host", id)
}

// Heartbeat records that an agent is alive and refreshes its live capacity.
func (s *Store) Heartbeat(ctx context.Context, id string, now time.Time) error {
	res, err := s.exec(ctx, `UPDATE hosts SET last_heartbeat=? WHERE id=?`, ms(now), id)
	if err != nil {
		return err
	}
	return affected(res, "host", id)
}

// SetHostCordoned cordons or uncordons a host.
func (s *Store) SetHostCordoned(ctx context.Context, id string, cordoned bool) error {
	res, err := s.exec(ctx, `UPDATE hosts SET cordoned=? WHERE id=?`, boolInt(cordoned), id)
	if err != nil {
		return err
	}
	return affected(res, "host", id)
}

// DeleteHost removes a host and its runner rows, and returns the IDs of those
// rows so each can be announced as deleted.
func (s *Store) DeleteHost(ctx context.Context, id string) ([]string, error) {
	return s.DeleteHostForgettingMachine(ctx, id, "")
}

// DeleteHostForgettingMachine is DeleteHost that also drops the machine row
// behind the host, in the same transaction. A forced delete of a rented
// machine's host has to forget the machine too, or the machine loop reads a
// Ready machine with no host as one to release and destroys the VM. As two
// writes, a failure or a cancelled request between them would leave the row
// gone and the host standing -- a billed VM that nothing records -- so they
// commit together or not at all.
//
// A machine that has since started deleting is left alone: dropping its row
// would abandon a half-finished delete with the VM still billed.
func (s *Store) DeleteHostForgettingMachine(ctx context.Context, id, machineID string) ([]string, error) {
	var runners []string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if machineID != "" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM machines WHERE id = ? AND state <> ?`, machineID, MachineDeleting); err != nil {
				return err
			}
		}
		err := recordSessionsWhere(ctx, tx, s.Now().UnixMilli(), `r.host_id = ?2`, id)
		if err != nil {
			return err
		}
		runners, err = deletedIDs(ctx, tx, `DELETE FROM runners WHERE host_id = ? RETURNING id`, id)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM hosts WHERE id = ?`, id)
		if err != nil {
			return err
		}
		return affected(res, "host", id)
	})
	return runners, err
}

// ---------------------------------------------------------------------------
// Runners
// ---------------------------------------------------------------------------

const runnerCols = `id, pool_id, host_id, name, state, github_runner_id, container_id,
	ephemeral, labels, image, image_digest, runner_version, current_job_id, created_at, started_at,
	last_idle_at, finished_at, message, jobs_handled, cpu_percent, memory_bytes,
	image_pull_ms, container_started_at, registered_at, task_issued_at,
	cleanup_error, cleanup_failed_at, cleanup_attempts, registration_deleted_at, cleaned_up_at,
	draining_since, create_task_issued_at, host_removed_at, cleanup_estimated_at,
	allocated_cpus, allocated_memory_mb, allocation_source, fault_kind, resource_sample,
	host_cleanup_error, registration_cleanup_error, sized_for_cpus`

func scanRunner(sc interface{ Scan(...any) error }) (*Runner, error) {
	var r Runner
	var resourceSample string
	var ephemeral int
	var created int64
	var started, idle, finished, pullMS, containerStarted, registered, taskIssued sql.NullInt64
	var cleanupFailed, registrationDeleted, cleanedUp, drainingSince sql.NullInt64
	var createIssued, hostRemoved, cleanupEstimated sql.NullInt64
	err := sc.Scan(&r.ID, &r.PoolID, &r.HostID, &r.Name, &r.State, &r.GitHubRunnerID,
		&r.ContainerID, &ephemeral, &r.Labels, &r.Image, &r.ImageDigest, &r.RunnerVersion, &r.CurrentJobID,
		&created, &started, &idle, &finished, &r.Message, &r.JobsHandled,
		&r.CPUPercent, &r.MemoryBytes, &pullMS, &containerStarted, &registered, &taskIssued,
		&r.CleanupError, &cleanupFailed, &r.CleanupAttempts, &registrationDeleted, &cleanedUp,
		&drainingSince, &createIssued, &hostRemoved, &cleanupEstimated,
		&r.AllocatedCPUs, &r.AllocatedMemoryMB, &r.AllocationSource, &r.FaultKind, &resourceSample,
		&r.HostCleanupError, &r.RegistrationCleanupError, &r.SizedForCPUs)
	if err != nil {
		return nil, err
	}
	r.ResourceSample = []byte(resourceSample)
	r.Ephemeral = ephemeral == 1
	r.CreatedAt = at(created)
	r.StartedAt, r.LastIdleAt, r.FinishedAt = atp(started), atp(idle), atp(finished)
	if pullMS.Valid {
		d := time.Duration(pullMS.Int64) * time.Millisecond
		r.ImagePullDuration = &d
	}
	r.ContainerStartedAt, r.RegisteredAt = atp(containerStarted), atp(registered)
	r.TaskIssuedAt = atp(taskIssued)
	r.CleanupFailedAt, r.RegistrationDeletedAt = atp(cleanupFailed), atp(registrationDeleted)
	r.CleanedUpAt = atp(cleanedUp)
	r.DrainingSince = atp(drainingSince)
	r.CreateTaskIssuedAt, r.HostRemovedAt, r.CleanupEstimatedAt = atp(createIssued), atp(hostRemoved), atp(cleanupEstimated)
	return &r, nil
}

// CreateRunner inserts a runner row in its initial state.
func (s *Store) CreateRunner(ctx context.Context, r *Runner) error {
	if r.ID == "" {
		r.ID = NewID(PrefixRunner)
	}
	if r.State == "" {
		r.State = RunnerProvisioning
	}
	r.CreatedAt = s.Now()
	_, err := s.exec(ctx, `INSERT INTO runners (`+runnerCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.PoolID, r.HostID, r.Name, string(r.State), r.GitHubRunnerID, r.ContainerID,
		boolInt(r.Ephemeral), r.Labels, r.Image, r.ImageDigest, r.RunnerVersion, r.CurrentJobID,
		ms(r.CreatedAt), msp(r.StartedAt), msp(r.LastIdleAt), msp(r.FinishedAt),
		r.Message, r.JobsHandled, r.CPUPercent, r.MemoryBytes, durationMS(r.ImagePullDuration),
		msp(r.ContainerStartedAt), msp(r.RegisteredAt), msp(r.TaskIssuedAt),
		r.CleanupError, msp(r.CleanupFailedAt), r.CleanupAttempts,
		msp(r.RegistrationDeletedAt), msp(r.CleanedUpAt), msp(r.DrainingSince),
		msp(r.CreateTaskIssuedAt), msp(r.HostRemovedAt), msp(r.CleanupEstimatedAt),
		r.AllocatedCPUs, r.AllocatedMemoryMB, r.AllocationSource, r.FaultKind, runnerSampleJSON(r.ResourceSample),
		r.HostCleanupError, r.RegistrationCleanupError, r.SizedForCPUs)
	return wrapWrite(err)
}

func durationMS(d *time.Duration) any {
	if d == nil {
		return nil
	}
	return d.Milliseconds()
}

// SetRunnerStartup records backend timings without inventing an image pull for
// backends that cannot report one.
func (s *Store) SetRunnerStartup(ctx context.Context, id string, pull *time.Duration, started *time.Time) error {
	_, err := s.exec(ctx, `UPDATE runners SET image_pull_ms=?, container_started_at=? WHERE id=?`, durationMS(pull), msp(started), id)
	return err
}

// SetRunnerCreatedAt moves a runner's creation time.
//
// Creation time is otherwise immutable -- UpdateRunner deliberately does not
// write the column, because a row whose age can be edited is a row whose age
// cannot be reasoned about. The one caller is the demo seeder, whose fixtures
// have no agent behind them and would otherwise age into a fleet reporting
// problems it does not have. Whether an ID is a fixture is the seeder's
// judgement, not this package's.
func (s *Store) SetRunnerCreatedAt(ctx context.Context, id string, at time.Time) error {
	_, err := s.exec(ctx, `UPDATE runners SET created_at=? WHERE id=?`, ms(at), id)
	return err
}

// GetRunner returns one runner by ID.
func (s *Store) GetRunner(ctx context.Context, id string) (*Runner, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+runnerCols+` FROM runners WHERE id = ?`, id)
	r, err := scanRunner(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("runner %s: %w", id, ErrNotFound)
	}
	return r, err
}

// GetRunnerByName returns one runner by the name GitHub knows it as.
func (s *Store) GetRunnerByName(ctx context.Context, name string) (*Runner, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+runnerCols+` FROM runners WHERE name = ?`, name)
	r, err := scanRunner(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("runner %q: %w", name, ErrNotFound)
	}
	return r, err
}

// RunnerFilter narrows a runner listing.
type RunnerFilter struct {
	PoolIDs []string
	HostIDs []string
	States  []RunnerState
	Search  string
	// IncludeRemoved keeps terminal runners in the result; the UI's default
	// view hides them because a busy fleet accumulates thousands.
	IncludeRemoved bool
}

var runnerSortCols = map[string]string{
	"name":       "name",
	"state":      "state",
	"created_at": "created_at",
	"pool":       "pool_id",
	"host":       "host_id",
	"jobs":       "jobs_handled",
}

// ListRunners returns a filtered, paginated page of runners plus the total
// number of rows matching the filter (for the grid's pagination footer).
func (s *Store) ListRunners(ctx context.Context, f RunnerFilter, p Page) ([]*Runner, int, error) {
	where, args := runnerWhere(f)
	var total int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM runners `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + runnerCols + ` FROM runners ` + where +
		` ORDER BY ` + p.orderBy(runnerSortCols, "created_at DESC") + ` LIMIT ? OFFSET ?`
	args = append(args, p.limit(50, 500), max(p.Offset, 0))
	rows, err := s.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*Runner
	for rows.Next() {
		r, err := scanRunner(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

func runnerWhere(f RunnerFilter) (string, []any) {
	var cond []string
	var args []any
	if !f.IncludeRemoved && len(f.States) == 0 {
		cond = append(cond, `state != 'removed'`)
	}
	if len(f.States) > 0 {
		ph := make([]string, len(f.States))
		for i, st := range f.States {
			ph[i] = "?"
			args = append(args, string(st))
		}
		cond = append(cond, `state IN (`+strings.Join(ph, ",")+`)`)
	}
	if len(f.PoolIDs) > 0 {
		ph := make([]string, len(f.PoolIDs))
		for i, id := range f.PoolIDs {
			ph[i] = "?"
			args = append(args, id)
		}
		cond = append(cond, `pool_id IN (`+strings.Join(ph, ",")+`)`)
	}
	if len(f.HostIDs) > 0 {
		ph := make([]string, len(f.HostIDs))
		for i, id := range f.HostIDs {
			ph[i] = "?"
			args = append(args, id)
		}
		cond = append(cond, `host_id IN (`+strings.Join(ph, ",")+`)`)
	}
	if q := strings.TrimSpace(f.Search); q != "" {
		cond = append(cond, `(name LIKE ? ESCAPE '\' OR id LIKE ? ESCAPE '\' OR container_id LIKE ? ESCAPE '\')`)
		like := likePattern(q)
		args = append(args, like, like, like)
	}
	if len(cond) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(cond, " AND "), args
}

// ListRunnersForPool returns every non-removed runner in a pool. The scheduler
// uses this on each reconcile, so it is deliberately unpaginated.
func (s *Store) ListRunnersForPool(ctx context.Context, poolID string) ([]*Runner, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+runnerCols+` FROM runners
		WHERE pool_id = ? AND state != 'removed' ORDER BY created_at`, poolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Runner
	for rows.Next() {
		r, err := scanRunner(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRunnersForHost returns the runners an agent is responsible for.
func (s *Store) ListRunnersForHost(ctx context.Context, hostID string, states ...RunnerState) ([]*Runner, error) {
	q := `SELECT ` + runnerCols + ` FROM runners WHERE host_id = ?`
	args := []any{hostID}
	if len(states) > 0 {
		ph := make([]string, len(states))
		for i, st := range states {
			ph[i] = "?"
			args = append(args, string(st))
		}
		q += ` AND state IN (` + strings.Join(ph, ",") + `)`
	} else {
		q += ` AND state != 'removed'`
	}
	q += ` ORDER BY created_at`
	rows, err := s.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Runner
	for rows.Next() {
		r, err := scanRunner(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateRunner persists a full runner row.
func (s *Store) UpdateRunner(ctx context.Context, r *Runner) error {
	// Every column the row has. The driver binds positionally and ignores a
	// surplus argument, so an argument without a placeholder shifts every
	// later column onto its neighbour's value and binds WHERE id to a number
	// -- which updates nothing and reports the runner as not found. The
	// round-trip test is what keeps the two lists the same length.
	res, err := s.exec(ctx, `UPDATE runners SET pool_id=?, host_id=?, name=?, state=?,
		github_runner_id=?, container_id=?, ephemeral=?, labels=?, image=?, image_digest=?,
		runner_version=?, current_job_id=?, started_at=?, last_idle_at=?, finished_at=?,
		message=?, jobs_handled=?, cpu_percent=?, memory_bytes=?, image_pull_ms=?,
		container_started_at=?, registered_at=?, allocated_cpus=?, allocated_memory_mb=?,
		allocation_source=?, fault_kind=? WHERE id=?`,
		r.PoolID, r.HostID, r.Name, string(r.State), r.GitHubRunnerID, r.ContainerID,
		boolInt(r.Ephemeral), r.Labels, r.Image, r.ImageDigest, r.RunnerVersion, r.CurrentJobID,
		msp(r.StartedAt), msp(r.LastIdleAt), msp(r.FinishedAt), r.Message,
		r.JobsHandled, r.CPUPercent, r.MemoryBytes, durationMS(r.ImagePullDuration),
		msp(r.ContainerStartedAt), msp(r.RegisteredAt), r.AllocatedCPUs, r.AllocatedMemoryMB,
		r.AllocationSource, r.FaultKind, r.ID)
	if err != nil {
		return wrapWrite(err)
	}
	return affected(res, "runner", r.ID)
}

// TransitionRunner moves a runner to a new state, rejecting illegal moves and
// stamping the timestamps that belong to each transition. It returns the runner
// as it now stands, so callers can publish the change without a second read.
//
// A transition to failed made through here records no category, which reads as
// the unclassified one. Callers that know why the runner failed -- which is
// most of them -- go through FailRunner instead.
func (s *Store) TransitionRunner(ctx context.Context, id string, to RunnerState, message string) (*Runner, error) {
	return s.transitionRunner(ctx, id, to, message, "", false)
}

// TransitionRunnerUnlessBusy is TransitionRunner with one more condition, checked
// in the same transaction as the write: the runner must not be running a job.
// It allows nothing the state machine does not; it only refuses, with
// ErrInvalidTransition, a move that has stopped being what the caller meant.
//
// The scheduler decides against a snapshot and applies against live rows, and
// the webhook that starts a job is handled without the reconcile lock. A drain
// planned for an idle runner that took a job in between would otherwise go
// through -- busy to draining is legal, because an operator may ask for it --
// and stop the job that had just started. A caller's own read of the row cannot
// close that window, since the row can change between the read and the write.
func (s *Store) TransitionRunnerUnlessBusy(ctx context.Context, id string, to RunnerState, message string) (*Runner, error) {
	return s.transitionRunner(ctx, id, to, message, "", true)
}

// FailRunner moves a runner to failed and records why in one write, so a row
// can never carry a message without the category that says what to do about
// it. The kind is normalised: one this build does not know is recorded as
// FaultRunnerExited rather than stored as an identifier nothing can render.
func (s *Store) FailRunner(ctx context.Context, id, message string, kind FaultKind) (*Runner, error) {
	if kind = kind.Normalise(); kind == "" {
		kind = FaultRunnerExited
	}
	return s.transitionRunner(ctx, id, RunnerFailed, message, kind, false)
}

// FailRunnerUnlessBusy is FailRunner with TransitionRunnerUnlessBusy's condition,
// for a failure decided against a snapshot that the runner may have outgrown by
// taking a job.
func (s *Store) FailRunnerUnlessBusy(ctx context.Context, id, message string, kind FaultKind) (*Runner, error) {
	if kind = kind.Normalise(); kind == "" {
		kind = FaultRunnerExited
	}
	return s.transitionRunner(ctx, id, RunnerFailed, message, kind, true)
}

func (s *Store) transitionRunner(ctx context.Context, id string, to RunnerState, message string, kind FaultKind, unlessBusy bool) (*Runner, error) {
	if !to.Valid() {
		return nil, fmt.Errorf("%w: %q is not a runner state", ErrInvalidTransition, to)
	}
	var out *Runner
	err := s.tx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT `+runnerCols+` FROM runners WHERE id = ?`, id)
		r, err := scanRunner(row)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("runner %s: %w", id, ErrNotFound)
		}
		if err != nil {
			return err
		}
		if !CanTransition(r.State, to) {
			return fmt.Errorf("%w: runner %s cannot go %s -> %s", ErrInvalidTransition, id, r.State, to)
		}
		if unlessBusy && (r.State == RunnerBusy || r.CurrentJobID != "") {
			return fmt.Errorf("%w: runner %s is running a job, so it cannot go %s -> %s now", ErrInvalidTransition, id, r.State, to)
		}
		now := s.Now()
		prev := r.State
		r.State = to
		if message != "" {
			r.Message = message
		}
		// A transition to the state the runner is already in is legal and
		// changes no timestamp. last_idle_at in particular is when the runner
		// *became* idle, which is what the idle timeout counts from; an agent
		// or a second caller reporting "still idle" used to move it forward and
		// make the runner immortal.
		switch to {
		case RunnerIdle:
			if r.RegisteredAt == nil {
				t := now
				r.RegisteredAt = &t
			}
			if r.StartedAt == nil {
				t := now
				r.StartedAt = &t
			}
			if prev != RunnerIdle {
				t := now
				r.LastIdleAt = &t
			}
			// Leaving busy means a job just finished on this runner.
			if prev == RunnerBusy {
				r.JobsHandled++
			}
			r.CurrentJobID = ""
		case RunnerBusy:
			if r.RegisteredAt == nil {
				t := now
				r.RegisteredAt = &t
			}
			if r.StartedAt == nil {
				t := now
				r.StartedAt = &t
			}
			r.LastIdleAt = nil
		case RunnerDraining:
			// Only on the way in. Draining is one-way -- it leads to removed
			// or failed and nowhere else -- so this is written once, and a
			// second caller reporting "still draining" must not push it
			// forward and make the drain timeout unreachable.
			if prev != RunnerDraining {
				t := now
				r.DrainingSince = &t
			}
		case RunnerRemoved, RunnerFailed:
			if prev != to {
				t := now
				r.FinishedAt = &t
			}
			r.CurrentJobID = ""
		}
		if kind != "" && to == RunnerFailed {
			r.FaultKind = kind
		}
		_, err = tx.ExecContext(ctx, `UPDATE runners SET state=?, message=?, started_at=?,
			last_idle_at=?, finished_at=?, jobs_handled=?, current_job_id=?, registered_at=?,
			draining_since=?, fault_kind=? WHERE id=?`,
			string(r.State), r.Message, msp(r.StartedAt), msp(r.LastIdleAt), msp(r.FinishedAt),
			r.JobsHandled, r.CurrentJobID, msp(r.RegisteredAt), msp(r.DrainingSince),
			r.FaultKind, r.ID)
		if err != nil {
			return err
		}
		out = r
		return nil
	})
	return out, err
}

// SetRunnerGitHubID records the runner ID GitHub assigned at registration.
func (s *Store) SetRunnerGitHubID(ctx context.Context, id string, ghID int64) error {
	_, err := s.exec(ctx, `UPDATE runners SET github_runner_id=? WHERE id=?`, ghID, id)
	return err
}

// SetRunnerContainer records the backend's handle for this runner's workload.
func (s *Store) SetRunnerContainer(ctx context.Context, id, containerID string) error {
	_, err := s.exec(ctx, `UPDATE runners SET container_id=? WHERE id=?`, containerID, id)
	return err
}

// SetRunnerResourceUsage stores a best-effort resource sample from the agent.
func (s *Store) SetRunnerResourceUsage(ctx context.Context, id string, cpu float64, mem int64) error {
	_, err := s.exec(ctx, `UPDATE runners SET cpu_percent=?, memory_bytes=? WHERE id=?`, cpu, mem, id)
	return err
}

// RecordCleanupFailure notes that taking this runner away on its host did not work, and
// counts the attempt.
//
// It is deliberately not a state transition. A runner whose remove failed is
// still removed as far as the fleet's accounting goes -- its slot is free and
// the scheduler has moved on -- and forcing it back through the state machine
// would make capacity wrong to record a tidying problem. What is wrong is the
// host, or GitHub, and that is what these columns say.
//
// A runner its host has already confirmed removed is refused with ErrConflict
// and left as it is. The confirmation came from a fresh listing of the host,
// so a failure arriving after it is about an attempt it overtook -- and a
// report and a task result travel on separate requests, so the two can cross.
// The check is the write's own condition rather than the caller's, because a
// caller's copy of the row can be read before the confirmation it races.
func (s *Store) RecordCleanupFailure(ctx context.Context, id, reason string) error {
	if reason == "" {
		reason = "cleanup failed without saying why"
	}
	res, err := s.exec(ctx, `UPDATE runners
		SET host_cleanup_error=?,
		    cleanup_error=? || CASE WHEN registration_cleanup_error='' THEN '' ELSE '; ' || registration_cleanup_error END,
		    cleanup_failed_at=?, cleanup_attempts=cleanup_attempts+1, cleaned_up_at=NULL
		WHERE id=? AND host_removed_at IS NULL`, reason, reason, s.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return err
	}
	var confirmed bool
	err = s.read.QueryRowContext(ctx, `SELECT host_removed_at IS NOT NULL FROM runners WHERE id=?`, id).Scan(&confirmed)
	if err == nil && confirmed {
		return fmt.Errorf("runner %s: its host has already confirmed it removed: %w", id, ErrConflict)
	}
	return nil
}

// RecordRegistrationCleanupFailure preserves the host's complaint while
// recording a failed GitHub deletion. Neither side can settle the other.
func (s *Store) RecordRegistrationCleanupFailure(ctx context.Context, id, reason string) error {
	if reason == "" {
		reason = "the GitHub runner registration could not be deleted"
	}
	_, err := s.exec(ctx, `UPDATE runners
		SET registration_cleanup_error=?,
		    cleanup_error=CASE WHEN host_cleanup_error='' THEN '' ELSE host_cleanup_error || '; ' END || ?,
		    cleanup_failed_at=?, cleanup_attempts=cleanup_attempts+1, cleaned_up_at=NULL, registration_deleted_at=NULL
		WHERE id=?`, reason, reason, s.Now().UnixMilli(), id)
	return err
}

// ClearCleanupFailure settles the host's complaint alone. A GitHub deletion
// still outstanding remains visible until its own retry succeeds.
//
// The attempt count is kept. How many tries it took is the difference between
// a blip and a host that needs looking at, and clearing it would erase the
// only evidence that anything was ever wrong.
func (s *Store) ClearCleanupFailure(ctx context.Context, id string) error {
	_, err := s.exec(ctx, `UPDATE runners
		SET host_cleanup_error='', cleanup_error=registration_cleanup_error,
		    cleanup_failed_at=CASE WHEN registration_cleanup_error='' THEN NULL ELSE cleanup_failed_at END,
		    cleaned_up_at=CASE WHEN registration_cleanup_error='' THEN cleaned_up_at ELSE NULL END
		WHERE id=?`, id)
	return err
}

// RecordRegistrationDeleted stamps the moment GitHub confirmed a runner's
// registration was gone, and closes the cleanup interval when nothing else is
// outstanding.
func (s *Store) RecordRegistrationDeleted(ctx context.Context, id string) error {
	_, err := s.ConfirmRunnerCleanup(ctx, id, false)
	return err
}

// ConfirmRunnerCleanup records a positive removal confirmation. Its result is
// true only for the write which completes both sides, so concurrent reports
// and retries cannot count the same cleanup twice. Times use the controller's
// clock rather than a remote host's clock.
func (s *Store) ConfirmRunnerCleanup(ctx context.Context, id string, host bool) (bool, error) {
	stamp, otherStamp := "registration_deleted_at", "host_removed_at"
	errColumn, otherError := "registration_cleanup_error", "host_cleanup_error"
	if host {
		stamp, otherStamp = otherStamp, stamp
		errColumn, otherError = otherError, errColumn
	}
	completed := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		now := s.Now().UnixMilli()
		var cleaned sql.NullInt64
		// All identifiers above are constants chosen here, never caller input.
		err := tx.QueryRowContext(ctx, `UPDATE runners SET `+stamp+`=?, `+errColumn+`='',
			cleanup_error=`+otherError+`,
			cleanup_failed_at=CASE WHEN `+otherError+`='' THEN NULL ELSE cleanup_failed_at END,
			cleaned_up_at=CASE WHEN `+otherStamp+` IS NOT NULL AND `+otherError+`='' THEN ? ELSE NULL END
			WHERE id=? AND `+stamp+` IS NULL RETURNING cleaned_up_at`, now, now, id).Scan(&cleaned)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		completed = cleaned.Valid
		if !completed {
			return nil
		}
		// The session is written by the same transaction that closes the
		// cleanup interval, so a controller that stops between the two cannot
		// leave a confirmed runner without one -- and the UPDATE above only
		// matches while the stamp is unset, so a replay after a restart never
		// reaches here twice.
		return recordRunnerSession(ctx, tx, id, now)
	})
	return completed, err
}

// SetRunnerCreateTaskIssued preserves the first actual delivery, not retries.
func (s *Store) SetRunnerCreateTaskIssued(ctx context.Context, id string, issued time.Time) error {
	_, err := s.exec(ctx, `UPDATE runners SET create_task_issued_at=COALESCE(create_task_issued_at, ?) WHERE id=?`, issued.UnixMilli(), id)
	return err
}

// RunnersWithFailedCleanup returns the rows still complaining, newest failure
// first, so the problems pass can name them without reading the whole table.
func (s *Store) RunnersWithFailedCleanup(ctx context.Context) ([]*Runner, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+runnerCols+` FROM runners
		WHERE cleanup_error != '' ORDER BY cleanup_failed_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Runner
	for rows.Next() {
		r, err := scanRunner(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetRunnerTaskIssued records when this runner's lifecycle task was handed to
// its host, at enqueue and again on every redelivery.
//
// It is what survives a controller restart of an in-memory task queue: the row
// keeps the time even though the task itself is gone, so a provisioning runner
// can still be told apart from one whose create was issued moments ago.
func (s *Store) SetRunnerTaskIssued(ctx context.Context, id string, at time.Time) error {
	_, err := s.exec(ctx, `UPDATE runners SET task_issued_at=? WHERE id=?`, at.UnixMilli(), id)
	return err
}

// RecordAgentSession folds one agent's session id into a host's record and
// reports whether it is a session this host has already moved on from.
//
// That is the duplicate-agent signal, and it is chosen because a single agent
// cannot produce it. A session id is minted at start-up, so restarting moves
// the id forward and never back; only two agents sharing one host's
// credentials hand the same pair back and forth. Counting plain changes would
// flag every restart instead.
//
// The whole read-modify-write is one transaction. Two agents alternating are
// by definition writing this row at the same time, so doing it in the caller
// would drop exactly the alternations it exists to count.
func (s *Store) RecordAgentSession(ctx context.Context, hostID, sessionID string) (bool, error) {
	if sessionID == "" {
		return false, nil
	}
	var alternated bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var current, prev string
		row := tx.QueryRowContext(ctx, `SELECT agent_session_id, agent_session_prev FROM hosts WHERE id = ?`, hostID)
		if err := row.Scan(&current, &prev); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("host %s: %w", hostID, ErrNotFound)
		} else if err != nil {
			return err
		}
		if sessionID == current {
			return nil
		}
		if sessionID == prev {
			// Back to a session this host had already left behind.
			alternated = true
			_, err := tx.ExecContext(ctx, `UPDATE hosts SET agent_session_id=?, agent_session_prev=?,
				agent_session_alternations=agent_session_alternations+1, agent_session_alt_at=? WHERE id=?`,
				sessionID, current, s.Now().UnixMilli(), hostID)
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE hosts SET agent_session_id=?, agent_session_prev=? WHERE id=?`,
			sessionID, current, hostID)
		return err
	})
	return alternated, err
}

// AssignRunnerJob links a runner to the job it is executing.
func (s *Store) AssignRunnerJob(ctx context.Context, runnerID, jobID string) error {
	_, err := s.exec(ctx, `UPDATE runners SET current_job_id=? WHERE id=?`, jobID, runnerID)
	return err
}

// StartRunnerJob atomically links a runner to the job GitHub says is running on
// it and takes it busy. It is the other half of CompleteRunnerJob, and it is
// atomic for the same reason: the link and the state are one fact, and two
// writes can disagree.
//
// GitHub's in_progress is authoritative that the runner registered and picked up
// work, so a row that is merely behind -- still provisioning or registering,
// because the agent's create result has not landed yet -- is brought forward
// rather than argued with. Nothing else can produce busy: the agent stops
// asserting a state once the workload is up, because whether GitHub has given it
// a job is not the agent's call.
//
// So losing this leaves the row in one of two wrong places. A row still on
// provisioning stays there holding the job, until the provision timeout fails a
// runner in the middle of somebody's build. A row that has already reached
// registering lands somewhere subtler: the next report finds no state asserted
// and a workload running, asks GitHub, and is told "online" -- which a busy
// runner also is -- so it goes idle, and idle clears the job link. The fleet is
// then holding a runner it believes is free with a build running on it, free to
// hand it another job or drain it as idle. reconcileKnownJobs does eventually
// re-apply the delivery and put that right, but not until the job is two minutes
// old and its turn comes round in a rotating batch of ten, which is a long time
// to be wrong about who is working.
//
// A runner already working keeps the job it has -- never take one off another
// job -- and only a missing link is filled in, which is what a drain needs: the
// drain timeout only fires on a drain with nothing left to wait for.
func (s *Store) StartRunnerJob(ctx context.Context, runnerID, jobID, message string) (*Runner, bool, error) {
	var out *Runner
	changed := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT `+runnerCols+` FROM runners WHERE id = ?`, runnerID)
		r, err := scanRunner(row)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("runner %s: %w", runnerID, ErrNotFound)
		}
		if err != nil {
			return err
		}
		out = r
		switch {
		case r.State.Terminal():
			// Removed or failed. Linking a job to a runner that is gone would
			// point the Jobs page at something nobody can look at.
			return nil
		case r.State == RunnerBusy || r.State == RunnerDraining:
			// Already working, or finishing the work it has. A redelivery of
			// the same start must not restamp it, and a different job must
			// never displace the one it is on.
			if r.CurrentJobID != "" {
				return nil
			}
		}

		now := s.Now()
		r.CurrentJobID = jobID
		if r.State == RunnerProvisioning || r.State == RunnerRegistering || r.State == RunnerIdle {
			r.State = RunnerBusy
			if message != "" {
				r.Message = message
			}
			// Stamped the same way TransitionRunner stamps a move to busy, so
			// a runner that reached busy by this route is not missing the
			// timings every other one has.
			if r.RegisteredAt == nil {
				t := now
				r.RegisteredAt = &t
			}
			if r.StartedAt == nil {
				t := now
				r.StartedAt = &t
			}
			r.LastIdleAt = nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runners SET state=?, message=?, started_at=?,
			last_idle_at=?, current_job_id=?, registered_at=? WHERE id=?`,
			string(r.State), r.Message, msp(r.StartedAt), msp(r.LastIdleAt),
			r.CurrentJobID, msp(r.RegisteredAt), r.ID); err != nil {
			return err
		}
		out, changed = r, true
		return nil
	})
	return out, changed, err
}

// CompleteRunnerJob atomically releases a runner from the job GitHub says has
// finished. Persistent runners return to idle; ephemeral runners are finished
// immediately so a missed workload-exit report cannot leave one busy forever.
// The job ID guard makes duplicate and out-of-order deliveries harmless.
func (s *Store) CompleteRunnerJob(ctx context.Context, runnerID, jobID, message string) (*Runner, bool, error) {
	var out *Runner
	changed := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT `+runnerCols+` FROM runners WHERE id = ?`, runnerID)
		r, err := scanRunner(row)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("runner %s: %w", runnerID, ErrNotFound)
		}
		if err != nil {
			return err
		}
		assigned := r.CurrentJobID == jobID && (r.State == RunnerBusy || r.State == RunnerDraining)
		// GitHub can deliver completion before in_progress (including a fast
		// cancellation). Only an unused ephemeral runner may be completed from
		// that observation; never release a persistent runner or another job.
		missedStart := r.Ephemeral && r.CurrentJobID == "" && r.JobsHandled == 0 &&
			(r.State == RunnerProvisioning || r.State == RunnerRegistering || r.State == RunnerIdle)
		if missedStart {
			var linked bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE id=? AND runner_id=? AND state='completed')`, jobID, runnerID).Scan(&linked); err != nil {
				return err
			}
			missedStart = linked
		}
		if !assigned && !missedStart {
			out = r
			return nil
		}

		now := s.Now()
		r.CurrentJobID = ""
		r.JobsHandled++
		r.Message = message
		if r.Ephemeral {
			r.State = RunnerRemoved
			r.FinishedAt = &now
		} else if r.State != RunnerDraining {
			r.State = RunnerIdle
			r.LastIdleAt = &now
		}
		_, err = tx.ExecContext(ctx, `UPDATE runners SET state=?, message=?, last_idle_at=?,
			finished_at=?, jobs_handled=?, current_job_id=? WHERE id=?`,
			string(r.State), r.Message, msp(r.LastIdleAt), msp(r.FinishedAt),
			r.JobsHandled, r.CurrentJobID, r.ID)
		if err != nil {
			return err
		}
		out, changed = r, true
		return nil
	})
	return out, changed, err
}

// DeleteRunner hard-deletes a runner row. Normal teardown transitions to
// "removed" instead; this exists for the UI's explicit delete action and for
// pruning ancient history.
func (s *Store) DeleteRunner(ctx context.Context, id string) error {
	res, err := s.exec(ctx, `DELETE FROM runners WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return affected(res, "runner", id)
}

// StartupSamples returns, for every runner created since the cutoff, how long
// its container took to start and how long it then took to register, in
// milliseconds, each sorted so that a percentile is an index into it. A runner
// still starting contributes nothing; one that started and never registered
// contributes to the first slice only.
//
// The Overview used to work this out from a page of runner rows, and the list
// query's limit quietly cut that page to the 500 newest runners, so the
// percentiles described the last few hundred starts rather than the window.
func (s *Store) StartupSamples(ctx context.Context, since time.Time) (startup, registration []int64, err error) {
	rows, err := s.read.QueryContext(ctx, `SELECT container_started_at - created_at, registered_at - container_started_at
		FROM runners WHERE created_at >= ? AND container_started_at IS NOT NULL AND container_started_at >= created_at`, ms(since))
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var start int64
		var register sql.NullInt64
		if err := rows.Scan(&start, &register); err != nil {
			return nil, nil, err
		}
		startup = append(startup, start)
		if register.Valid && register.Int64 >= 0 {
			registration = append(registration, register.Int64)
		}
	}
	slices.Sort(startup)
	slices.Sort(registration)
	return startup, registration, rows.Err()
}

// PruneRunners deletes removed/failed runners older than the cutoff and
// returns the IDs of the rows that went, so each can be announced as deleted.
func (s *Store) PruneRunners(ctx context.Context, before time.Time) ([]string, error) {
	var ids []string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		// A runner whose cleanup was never confirmed has no session yet, and
		// this is the last moment one can be written: without it the usage
		// ledger would be short by exactly the runners whose cleanup went
		// wrong. Those already recorded are left alone by the conflict clause.
		if _, err := tx.ExecContext(ctx, insertRunnerSessionSQL+`r.state IN ('removed','failed')
			AND COALESCE(r.finished_at, r.created_at) < ?2 ON CONFLICT(runner_id) DO NOTHING`, s.Now().UnixMilli(), ms(before)); err != nil {
			return err
		}
		var err error
		ids, err = deletedIDs(ctx, tx, `DELETE FROM runners WHERE state IN ('removed','failed')
			AND COALESCE(finished_at, created_at) < ? RETURNING id`, ms(before))
		return err
	})
	return ids, err
}

// affected turns "UPDATE matched nothing" into ErrNotFound.
func affected(res sql.Result, kind, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s %s: %w", kind, id, ErrNotFound)
	}
	return nil
}

// SetHostConnection records the transport the controller actually observed.
func (s *Store) SetHostConnection(ctx context.Context, id, connection string) error {
	if connection != "direct" && connection != "tailcat" {
		return ErrConflict
	}
	res, err := s.exec(ctx, `UPDATE hosts SET connection=? WHERE id=?`, connection, id)
	if err != nil {
		return wrapWrite(err)
	}
	return affected(res, "host", id)
}

func runnerSampleJSON(sample []byte) string {
	if len(sample) == 0 {
		return "{}"
	}
	return string(sample)
}

// SetRunnerResourceSample stores the original sample timestamp, including when
// an agent reports its retained last sample after a collection failure.
func (s *Store) SetRunnerResourceSample(ctx context.Context, id string, cpu float64, mem int64, sample []byte) error {
	_, err := s.exec(ctx, `UPDATE runners SET cpu_percent=?, memory_bytes=?, resource_sample=? WHERE id=?`, cpu, mem, runnerSampleJSON(sample), id)
	return err
}

// DeferRegistrationCleanup refreshes a busy registration's explanation without
// counting an observation as a failed deletion or resetting its original age.
// Host cleanup evidence remains independent.
func (s *Store) DeferRegistrationCleanup(ctx context.Context, id, reason string) error {
	_, err := s.exec(ctx, `UPDATE runners SET registration_cleanup_error=?,
  cleanup_error=CASE WHEN host_cleanup_error='' THEN '' ELSE host_cleanup_error || '; ' END || ?,
  cleanup_failed_at=COALESCE(cleanup_failed_at, ?), cleaned_up_at=NULL, registration_deleted_at=NULL
  WHERE id=?`, reason, reason, s.Now().UnixMilli(), id)
	return err
}

// RunnersPendingRegistrationCleanup includes removed rows, which ordinary pool
// listings omit. A complete GitHub snapshot can confirm their registrations gone.
func (s *Store) RunnersPendingRegistrationCleanup(ctx context.Context, installationID string) ([]*Runner, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+runnerCols+` FROM runners
  WHERE state IN ('removed', 'failed') AND registration_deleted_at IS NULL
  AND pool_id IN (SELECT id FROM pools WHERE installation_id=?)`, installationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Runner
	for rows.Next() {
		r, err := scanRunner(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
