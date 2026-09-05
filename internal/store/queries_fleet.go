package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// FindInstallationByTarget resolves the installation that owns a repo or org.
// It prefers an exact repo match and falls back to the owning organisation, so
// a webhook for acme/widgets resolves against an org-wide installation.
func (s *Store) FindInstallationByTarget(ctx context.Context, repoFullName string) (*Installation, error) {
	owner := repoFullName
	if i := strings.IndexByte(repoFullName, '/'); i > 0 {
		owner = repoFullName[:i]
	}
	row := s.read.QueryRowContext(ctx, `SELECT `+installationCols+` FROM installations
		WHERE (target_type = 'repo' AND target = ?) OR (target_type = 'org' AND target = ?)
		ORDER BY CASE target_type WHEN 'repo' THEN 0 ELSE 1 END LIMIT 1`,
		repoFullName, owner)
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
func (s *Store) DeleteInstallation(ctx context.Context, id string) error {
	res, err := s.exec(ctx, `DELETE FROM installations WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return affected(res, "installation", id)
}

// ---------------------------------------------------------------------------
// Pools
// ---------------------------------------------------------------------------

const poolCols = `id, name, installation_id, labels, runner_group, backend, image, pull_policy,
	runner_version, min_runners, max_runners, priority, idle_timeout_ms, ephemeral, docker_mode,
	resources, cache, host_selector, env, run_as_root, enabled, created_at, updated_at,
	repository_concurrency_limit, cost_per_runner_hour`

func scanPool(sc interface{ Scan(...any) error }) (*Pool, error) {
	var p Pool
	var idle, created, updated int64
	var ephemeral, runAsRoot, enabled int
	var resources, cache string
	err := sc.Scan(&p.ID, &p.Name, &p.InstallationID, &p.Labels, &p.RunnerGroup, &p.Backend,
		&p.Image, &p.PullPolicy, &p.RunnerVersion, &p.MinRunners, &p.MaxRunners, &p.Priority, &idle, &ephemeral,
		&p.DockerMode, &resources, &cache, &p.HostSelector, &p.Env, &runAsRoot, &enabled,
		&created, &updated, &p.RepositoryConcurrencyLimit, &p.CostPerRunnerHour)
	if err != nil {
		return nil, err
	}
	p.IdleTimeout = Duration(time.Duration(idle) * time.Millisecond)
	p.Ephemeral, p.RunAsRoot, p.Enabled = ephemeral == 1, runAsRoot == 1, enabled == 1
	p.CreatedAt, p.UpdatedAt = at(created), at(updated)
	if err := unmarshalJSON(resources, &p.Resources); err != nil {
		return nil, fmt.Errorf("pool %s: decoding resources: %w", p.ID, err)
	}
	if err := unmarshalJSON(cache, &p.Cache); err != nil {
		return nil, fmt.Errorf("pool %s: decoding cache: %w", p.ID, err)
	}
	return &p, nil
}

// CreatePool inserts a pool. The name is branded and the labels normalised on
// the way in, here rather than only in the handler, so that no caller -- the
// API, the installer, the seeder -- can put a pool in the database that GitHub
// would show under a name saying nothing about which fleet it belongs to. The
// scheduler then never has to think about case or whitespace either.
func (s *Store) CreatePool(ctx context.Context, p *Pool) error {
	if p.ID == "" {
		p.ID = NewID(PrefixPool)
	}
	now := s.Now()
	p.CreatedAt, p.UpdatedAt = now, now
	p.Name = BrandedName(p.Name)
	p.Labels = NormalizeLabels(p.Labels)
	if p.PullPolicy == "" {
		p.PullPolicy = PullIfNotPresent
	}
	res, err := marshalJSON(p.Resources)
	if err != nil {
		return err
	}
	cache, err := marshalJSON(p.Cache)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `INSERT INTO pools (`+poolCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Name, p.InstallationID, p.Labels, p.RunnerGroup, string(p.Backend), p.Image,
		string(p.PullPolicy),
		p.RunnerVersion, p.MinRunners, p.MaxRunners, p.Priority, p.IdleTimeout.Duration().Milliseconds(),
		boolInt(p.Ephemeral), string(p.DockerMode), res, cache, p.HostSelector, p.Env,
		boolInt(p.RunAsRoot), boolInt(p.Enabled), ms(p.CreatedAt), ms(p.UpdatedAt),
		p.RepositoryConcurrencyLimit, p.CostPerRunnerHour)
	return wrapWrite(err)
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
	p.UpdatedAt = s.Now()
	p.Name = BrandedName(p.Name)
	p.Labels = NormalizeLabels(p.Labels)
	if p.PullPolicy == "" {
		p.PullPolicy = PullIfNotPresent
	}
	res, err := marshalJSON(p.Resources)
	if err != nil {
		return err
	}
	cache, err := marshalJSON(p.Cache)
	if err != nil {
		return err
	}
	r, err := s.exec(ctx, `UPDATE pools SET name=?, installation_id=?, labels=?, runner_group=?,
		backend=?, image=?, pull_policy=?, runner_version=?, min_runners=?, max_runners=?, priority=?, idle_timeout_ms=?,
		ephemeral=?, docker_mode=?, resources=?, cache=?, host_selector=?, env=?, run_as_root=?,
		enabled=?, updated_at=?, repository_concurrency_limit=?, cost_per_runner_hour=? WHERE id=?`,
		p.Name, p.InstallationID, p.Labels, p.RunnerGroup, string(p.Backend), p.Image,
		string(p.PullPolicy),
		p.RunnerVersion, p.MinRunners, p.MaxRunners, p.Priority, p.IdleTimeout.Duration().Milliseconds(),
		boolInt(p.Ephemeral), string(p.DockerMode), res, cache, p.HostSelector, p.Env,
		boolInt(p.RunAsRoot), boolInt(p.Enabled), ms(p.UpdatedAt), p.RepositoryConcurrencyLimit,
		p.CostPerRunnerHour, p.ID)
	if err != nil {
		return wrapWrite(err)
	}
	return affected(r, "pool", p.ID)
}

// DeletePool removes a pool and, by cascade, its runner rows.
func (s *Store) DeletePool(ctx context.Context, id string) error {
	res, err := s.exec(ctx, `DELETE FROM pools WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return affected(res, "pool", id)
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

const hostCols = `id, name, address, embedded, capacity, backends, backend_info, labels, os, arch,
	version, cordoned, token_hash, last_heartbeat, created_at`

func scanHost(sc interface{ Scan(...any) error }) (*Host, error) {
	var h Host
	var embedded, cordoned int
	var heartbeat, created int64
	err := sc.Scan(&h.ID, &h.Name, &h.Address, &embedded, &h.Capacity, &h.Backends,
		&h.BackendInfo, &h.Labels, &h.OS, &h.Arch, &h.Version, &cordoned, &h.TokenHash,
		&heartbeat, &created)
	if err != nil {
		return nil, err
	}
	h.Embedded, h.Cordoned = embedded == 1, cordoned == 1
	h.LastHeartbeat, h.CreatedAt = at(heartbeat), at(created)
	return &h, nil
}

// CreateHost registers a new agent host.
func (s *Store) CreateHost(ctx context.Context, h *Host) error {
	if h.ID == "" {
		h.ID = NewID(PrefixHost)
	}
	h.CreatedAt = s.Now()
	if h.LastHeartbeat.IsZero() {
		h.LastHeartbeat = h.CreatedAt
	}
	_, err := s.exec(ctx, `INSERT INTO hosts (`+hostCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		h.ID, h.Name, h.Address, boolInt(h.Embedded), h.Capacity, h.Backends, h.BackendInfo,
		h.Labels, h.OS, h.Arch, h.Version, boolInt(h.Cordoned), h.TokenHash,
		ms(h.LastHeartbeat), ms(h.CreatedAt))
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
		h.ActiveRunners = counts[h.ID]
	}
	return nil
}

func (s *Store) countRunnersByHost(ctx context.Context) (map[string]int, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT host_id, COUNT(*) FROM runners
		WHERE state NOT IN ('removed','failed') GROUP BY host_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
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
	res, err := s.exec(ctx, `UPDATE hosts SET name=?, address=?, capacity=?, backends=?,
		backend_info=?, labels=?, os=?, arch=?, version=?, cordoned=?, last_heartbeat=? WHERE id=?`,
		h.Name, h.Address, h.Capacity, h.Backends, h.BackendInfo, h.Labels, h.OS, h.Arch,
		h.Version, boolInt(h.Cordoned), ms(h.LastHeartbeat), h.ID)
	if err != nil {
		return wrapWrite(err)
	}
	return affected(res, "host", h.ID)
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

// DeleteHost removes a host and cascades to its runner rows.
func (s *Store) DeleteHost(ctx context.Context, id string) error {
	res, err := s.exec(ctx, `DELETE FROM hosts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return affected(res, "host", id)
}

// ---------------------------------------------------------------------------
// Runners
// ---------------------------------------------------------------------------

const runnerCols = `id, pool_id, host_id, name, state, github_runner_id, container_id,
	ephemeral, labels, image, image_digest, runner_version, current_job_id, created_at, started_at,
	last_idle_at, finished_at, message, jobs_handled, cpu_percent, memory_bytes`

func scanRunner(sc interface{ Scan(...any) error }) (*Runner, error) {
	var r Runner
	var ephemeral int
	var created int64
	var started, idle, finished sql.NullInt64
	err := sc.Scan(&r.ID, &r.PoolID, &r.HostID, &r.Name, &r.State, &r.GitHubRunnerID,
		&r.ContainerID, &ephemeral, &r.Labels, &r.Image, &r.ImageDigest, &r.RunnerVersion, &r.CurrentJobID,
		&created, &started, &idle, &finished, &r.Message, &r.JobsHandled,
		&r.CPUPercent, &r.MemoryBytes)
	if err != nil {
		return nil, err
	}
	r.Ephemeral = ephemeral == 1
	r.CreatedAt = at(created)
	r.StartedAt, r.LastIdleAt, r.FinishedAt = atp(started), atp(idle), atp(finished)
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
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.PoolID, r.HostID, r.Name, string(r.State), r.GitHubRunnerID, r.ContainerID,
		boolInt(r.Ephemeral), r.Labels, r.Image, r.ImageDigest, r.RunnerVersion, r.CurrentJobID,
		ms(r.CreatedAt), msp(r.StartedAt), msp(r.LastIdleAt), msp(r.FinishedAt),
		r.Message, r.JobsHandled, r.CPUPercent, r.MemoryBytes)
	return wrapWrite(err)
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
		cond = append(cond, `(name LIKE ? OR id LIKE ? OR container_id LIKE ?)`)
		like := "%" + q + "%"
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
	res, err := s.exec(ctx, `UPDATE runners SET pool_id=?, host_id=?, name=?, state=?,
		github_runner_id=?, container_id=?, ephemeral=?, labels=?, image=?, runner_version=?,
		current_job_id=?, started_at=?, last_idle_at=?, finished_at=?, message=?,
		jobs_handled=?, cpu_percent=?, memory_bytes=? WHERE id=?`,
		r.PoolID, r.HostID, r.Name, string(r.State), r.GitHubRunnerID, r.ContainerID,
		boolInt(r.Ephemeral), r.Labels, r.Image, r.ImageDigest, r.RunnerVersion, r.CurrentJobID,
		msp(r.StartedAt), msp(r.LastIdleAt), msp(r.FinishedAt), r.Message,
		r.JobsHandled, r.CPUPercent, r.MemoryBytes, r.ID)
	if err != nil {
		return wrapWrite(err)
	}
	return affected(res, "runner", r.ID)
}

// TransitionRunner moves a runner to a new state, rejecting illegal moves and
// stamping the timestamps that belong to each transition. It returns the runner
// as it now stands, so callers can publish the change without a second read.
func (s *Store) TransitionRunner(ctx context.Context, id string, to RunnerState, message string) (*Runner, error) {
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
		now := s.Now()
		prev := r.State
		r.State = to
		if message != "" {
			r.Message = message
		}
		switch to {
		case RunnerIdle:
			if r.StartedAt == nil {
				t := now
				r.StartedAt = &t
			}
			t := now
			r.LastIdleAt = &t
			// Leaving busy means a job just finished on this runner.
			if prev == RunnerBusy {
				r.JobsHandled++
			}
			r.CurrentJobID = ""
		case RunnerBusy:
			if r.StartedAt == nil {
				t := now
				r.StartedAt = &t
			}
			r.LastIdleAt = nil
		case RunnerRemoved, RunnerFailed:
			t := now
			r.FinishedAt = &t
			r.CurrentJobID = ""
		}
		_, err = tx.ExecContext(ctx, `UPDATE runners SET state=?, message=?, started_at=?,
			last_idle_at=?, finished_at=?, jobs_handled=?, current_job_id=? WHERE id=?`,
			string(r.State), r.Message, msp(r.StartedAt), msp(r.LastIdleAt), msp(r.FinishedAt),
			r.JobsHandled, r.CurrentJobID, r.ID)
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

// AssignRunnerJob links a runner to the job it is executing.
func (s *Store) AssignRunnerJob(ctx context.Context, runnerID, jobID string) error {
	_, err := s.exec(ctx, `UPDATE runners SET current_job_id=? WHERE id=?`, jobID, runnerID)
	return err
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

// PruneRunners deletes removed/failed runners older than the cutoff and
// returns how many rows went.
func (s *Store) PruneRunners(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.exec(ctx, `DELETE FROM runners WHERE state IN ('removed','failed')
		AND COALESCE(finished_at, created_at) < ?`, ms(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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
