package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/aicontext"
)

type AIContextRepository struct {
	Freshness  *AIContextFreshness `json:"freshness,omitempty"`
	SetupState string              `json:"setup_state,omitempty"`
	SetupPRURL string              `json:"setup_pr_url,omitempty"`
	// WorkflowOutdated is true while the installed workflow is exactly one an
	// earlier release wrote: still trusted, but due a repair to pick up fixes.
	WorkflowOutdated bool                    `json:"workflow_outdated"`
	ID               string                  `json:"id"`
	Key              aicontext.RepositoryKey `json:"repository"`
	FullName         string                  `json:"full_name"`
	Config           aicontext.Config        `json:"config"`
	Revision         int64                   `json:"revision"`
	// Available is a verified repository-access state, not whether a snapshot
	// exists. Access removal closes this gate before retained blobs are read.
	Available bool      `json:"available"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MarshalJSON derives shareable setup guidance from the saved destination, so
// it remains available after the preparation wizard has finished.
func (r AIContextRepository) MarshalJSON() ([]byte, error) {
	type repository AIContextRepository
	return json.Marshal(struct {
		repository
		Instructions  string `json:"instructions"`
		BadgeMarkdown string `json:"badge_markdown"`
	}{repository: repository(r), Instructions: aicontext.AssistantInstructions(r.Key, r.FullName, r.Config), BadgeMarkdown: aicontext.BadgeMarkdown(r.Key, r.FullName)})
}

const aiContextColumns = `id, installation_id, github_host, repository_id, full_name, config_json, revision, available, created_at, updated_at`

const aiContextReadColumns = aiContextColumns + `, COALESCE((SELECT state FROM ai_context_setups WHERE repository_id=ai_context_repositories.id),''), COALESCE((SELECT pr_url FROM ai_context_setups WHERE repository_id=ai_context_repositories.id),''), COALESCE((SELECT json_object('state',state,'desired_commit',desired_commit,'published_commit',published_commit,'snapshot_id',digest,'checked_at',checked_at,'failure',failure) FROM ai_context_freshness WHERE repository_id=ai_context_repositories.id),'null'), workflow_outdated`

func scanAIContext(sc interface{ Scan(...any) error }) (*AIContextRepository, error) {
	var r AIContextRepository
	var config, freshness string
	var available, outdated int
	var created, updated int64
	err := sc.Scan(&r.ID, &r.Key.InstallationID, &r.Key.GitHubHost, &r.Key.RepositoryID, &r.FullName, &config, &r.Revision, &available, &created, &updated, &r.SetupState, &r.SetupPRURL, &freshness, &outdated)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(config), &r.Config); err != nil {
		return nil, err
	}
	if freshness != "null" {
		var stored struct {
			State           string `json:"state"`
			DesiredCommit   string `json:"desired_commit"`
			PublishedCommit string `json:"published_commit"`
			Digest          string `json:"snapshot_id"`
			CheckedAt       int64  `json:"checked_at"`
			Failure         string `json:"failure"`
		}
		if err := json.Unmarshal([]byte(freshness), &stored); err != nil {
			return nil, err
		}
		r.Freshness = &AIContextFreshness{State: stored.State, DesiredCommit: stored.DesiredCommit, PublishedCommit: stored.PublishedCommit, Digest: stored.Digest, CheckedAt: at(stored.CheckedAt), Failure: stored.Failure}
	}
	r.Available, r.WorkflowOutdated, r.CreatedAt, r.UpdatedAt = available == 1, outdated == 1, at(created), at(updated)
	return &r, nil
}

func (s *Store) CreateAIContextRepository(ctx context.Context, r *AIContextRepository) error {
	if err := r.Key.Validate(); err != nil {
		return err
	}
	if err := r.Config.Validate(); err != nil {
		return err
	}
	if r.FullName == "" || len(r.FullName) > 255 {
		return fmt.Errorf("context needs the repository's full name")
	}
	if r.ID == "" {
		r.ID = NewID(PrefixAIContext)
	}
	body, err := json.Marshal(r.Config)
	if err != nil {
		return err
	}
	now := s.Now()
	_, err = s.exec(ctx, `INSERT INTO ai_context_repositories (`+aiContextColumns+`) VALUES (?,?,?,?,?,?,1,0,?,?)`,
		r.ID, r.Key.InstallationID, r.Key.GitHubHost, r.Key.RepositoryID, r.FullName, string(body), ms(now), ms(now))
	if err != nil {
		return wrapWrite(err)
	}
	r.Revision, r.Available, r.CreatedAt, r.UpdatedAt = 1, false, now, now
	return nil
}

// SetAIContextWorkflowOutdated records what the last verification found. It
// leaves revision and updated_at alone: this is an observation about GitHub, not
// an edit, and bumping the revision would reject a wizard write in progress.
func (s *Store) SetAIContextWorkflowOutdated(ctx context.Context, id string, outdated bool) error {
	v := 0
	if outdated {
		v = 1
	}
	_, err := s.exec(ctx, `UPDATE ai_context_repositories SET workflow_outdated=? WHERE id=? AND workflow_outdated<>?`, v, id, v)
	return wrapWrite(err)
}

func (s *Store) GetAIContextRepository(ctx context.Context, id string) (*AIContextRepository, error) {
	r, err := scanAIContext(s.read.QueryRowContext(ctx, `SELECT `+aiContextReadColumns+` FROM ai_context_repositories WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("context repository %s: %w", id, ErrNotFound)
	}
	return r, err
}

// SaveAIContextConfig is compare-and-swap: resuming yesterday's wizard cannot
// silently replace the exclusions or destination somebody changed today.
func (s *Store) SaveAIContextConfig(ctx context.Context, id string, revision int64, config aicontext.Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if revision < 1 {
		return ErrConflict
	}
	body, err := json.Marshal(config)
	if err != nil {
		return err
	}
	res, err := s.exec(ctx, `UPDATE ai_context_repositories SET config_json=?, revision=revision+1, updated_at=? WHERE id=? AND revision=? AND NOT EXISTS (SELECT 1 FROM ai_context_setups WHERE repository_id=ai_context_repositories.id)`, string(body), ms(s.Now()), id, revision)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrConflict
	}
	return nil
}

func (s *Store) SetAIContextAvailable(ctx context.Context, id string, available bool) error {
	res, err := s.exec(ctx, `UPDATE ai_context_repositories SET available=?, updated_at=? WHERE id=?`, boolInt(available), ms(s.Now()), id)
	if err != nil {
		return err
	}
	return affected(res, "context repository", id)
}

func (s *Store) DeleteAIContextRepository(ctx context.Context, id string) error {
	res, err := s.exec(ctx, `DELETE FROM ai_context_repositories WHERE id=?`, id)
	if err != nil {
		return err
	}
	return affected(res, "context repository", id)
}

// ReplaceAIContextMembers also removes consent for users losing membership.
// Adding the user back later must not quietly re-enable an old app connection.
func (s *Store) ReplaceAIContextMembers(ctx context.Context, repositoryID string, users []string) error {
	if len(users) > 200 {
		return fmt.Errorf("select at most 200 context readers")
	}
	return wrapWrite(s.tx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM ai_context_repositories WHERE id=?`, repositoryID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		// The temporary selection is held in Go; all changes still commit as
		// one writer transaction, so a failed member insert retains old grants.
		old := map[string]bool{}
		rows, err := tx.QueryContext(ctx, `SELECT user_id FROM ai_context_members WHERE repository_id=?`, repositoryID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var user string
			if err := rows.Scan(&user); err != nil {
				rows.Close()
				return err
			}
			old[user] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if _, err := tx.ExecContext(ctx, `DELETE FROM ai_context_members WHERE repository_id=?`, repositoryID); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, user := range users {
			if user == "" || seen[user] {
				return fmt.Errorf("select each context reader once")
			}
			seen[user] = true
			if _, err := tx.ExecContext(ctx, `INSERT INTO ai_context_members(repository_id,user_id) VALUES(?,?)`, repositoryID, user); err != nil {
				return err
			}
		}
		for user := range old {
			if !seen[user] {
				if _, err := tx.ExecContext(ctx, `DELETE FROM ai_context_connection_repositories WHERE repository_id=? AND grant_id IN (SELECT id FROM oauth_grants WHERE user_id=?)`, repositoryID, user); err != nil {
					return err
				}
			}
		}
		return nil
	}))
}

func (s *Store) AIContextUserAccess(ctx context.Context, repositoryID, userID string) (bool, error) {
	var allowed int
	err := s.read.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_context_members m
		JOIN ai_context_repositories r ON r.id=m.repository_id JOIN users u ON u.id=m.user_id
		WHERE m.repository_id=? AND m.user_id=? AND r.available=1 AND u.disabled=0)`, repositoryID, userID).Scan(&allowed)
	return allowed == 1, err
}

// ReplaceAIContextConnectionAccess is called only after explicit consent.
// Ownership, live user membership and connection revocation are checked here
// as well as by the caller; a cached identity cannot restore revoked access.
//
// publish is the subset of repositories this connection may also write notes
// to. nil keeps the publish consent already given for repositories that stay
// selected, so a client that only knows about read consent cannot widen or
// clear the write half by accident; an empty slice clears it.
func (s *Store) ReplaceAIContextConnectionAccess(ctx context.Context, grantID, userID string, repositories, publish []string) error {
	if len(repositories) > 100 {
		return fmt.Errorf("select at most 100 repositories for one connection")
	}
	selected := map[string]bool{}
	for _, repository := range repositories {
		selected[repository] = true
	}
	for _, repository := range publish {
		if !selected[repository] {
			return fmt.Errorf("a connection can only publish notes to repositories it may also read")
		}
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		var active int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM oauth_grants g JOIN users u ON u.id=g.user_id JOIN oauth_clients c ON c.id=g.client_id
			WHERE g.id=? AND g.user_id=? AND g.revoked_at IS NULL AND c.revoked_at IS NULL AND u.disabled=0`, grantID, userID).Scan(&active)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		writes := map[string]bool{}
		if publish == nil {
			rows, err := tx.QueryContext(ctx, `SELECT repository_id FROM ai_context_connection_repositories WHERE grant_id=? AND publish=1`, grantID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				writes[id] = true
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		} else {
			for _, id := range publish {
				writes[id] = true
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM ai_context_connection_repositories WHERE grant_id=?`, grantID); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, repository := range repositories {
			if seen[repository] {
				return fmt.Errorf("select each context repository once")
			}
			seen[repository] = true
			var allowed int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM ai_context_members m JOIN ai_context_repositories r ON r.id=m.repository_id WHERE m.repository_id=? AND m.user_id=? AND r.available=1`, repository, userID).Scan(&allowed)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO ai_context_connection_repositories(grant_id,repository_id,publish) VALUES(?,?,?)`, grantID, repository, boolInt(writes[repository])); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) AIContextConnectionAccess(ctx context.Context, repositoryID, grantID, userID string) (bool, error) {
	return s.aiContextConnectionAccess(ctx, repositoryID, grantID, userID, false)
}

// AIContextConnectionPublishAccess is read access plus the owner's separate
// consent for this connection to write notes to the repository.
func (s *Store) AIContextConnectionPublishAccess(ctx context.Context, repositoryID, grantID, userID string) (bool, error) {
	return s.aiContextConnectionAccess(ctx, repositoryID, grantID, userID, true)
}

func (s *Store) aiContextConnectionAccess(ctx context.Context, repositoryID, grantID, userID string, publish bool) (bool, error) {
	var allowed int
	err := s.read.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_context_connection_repositories a
		JOIN oauth_grants g ON g.id=a.grant_id JOIN oauth_clients c ON c.id=g.client_id
		JOIN ai_context_members m ON m.repository_id=a.repository_id AND m.user_id=g.user_id
		JOIN ai_context_repositories r ON r.id=a.repository_id JOIN users u ON u.id=g.user_id
		WHERE a.repository_id=? AND g.id=? AND g.user_id=? AND r.available=1 AND u.disabled=0
		AND g.revoked_at IS NULL AND c.revoked_at IS NULL AND (?=0 OR a.publish=1))`, repositoryID, grantID, userID, boolInt(publish)).Scan(&allowed)
	return allowed == 1, err
}

// ListAIContextRepositories bounds configuration metadata separately from source
// retrieval. Ordering includes the ID so equally named drafts cannot skip rows.
func (s *Store) ListAIContextRepositories(ctx context.Context, limit, offset int) ([]AIContextRepository, int, error) {
	return s.ListAIContextRepositoriesFiltered(ctx, limit, offset, "", "")
}

func (s *Store) ListAIContextRepositoriesFiltered(ctx context.Context, limit, offset int, installationID, query string) ([]AIContextRepository, int, error) {
	return s.listAIContextRepositories(ctx, limit, offset, installationID, query, "")
}

// ListAIContextRepositoriesOwnedBy is the same page restricted to installations
// the user owns, so an owner's listing cannot reach another installation's rows
// however the filters are combined.
func (s *Store) ListAIContextRepositoriesOwnedBy(ctx context.Context, limit, offset int, installationID, query, userID string) ([]AIContextRepository, int, error) {
	if userID == "" {
		return nil, 0, fmt.Errorf("an owner is required")
	}
	return s.listAIContextRepositories(ctx, limit, offset, installationID, query, userID)
}

func (s *Store) listAIContextRepositories(ctx context.Context, limit, offset int, installationID, query, ownerID string) ([]AIContextRepository, int, error) {
	if limit < 1 || limit > 100 || offset < 0 || len(query) > 200 {
		return nil, 0, fmt.Errorf("choose a page size between 1 and 100 and a non-negative offset")
	}
	pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query) + "%"
	const where = ` WHERE (?='' OR installation_id=?) AND full_name LIKE ? ESCAPE '\'
		AND (?='' OR installation_id IN (SELECT o.installation_id FROM ai_context_installation_owners o JOIN users u ON u.id=o.user_id WHERE o.user_id=? AND u.disabled=0))`
	var total int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_context_repositories`+where, installationID, installationID, pattern, ownerID, ownerID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.read.QueryContext(ctx, `SELECT `+aiContextReadColumns+` FROM ai_context_repositories`+where+` ORDER BY full_name,id LIMIT ? OFFSET ?`, installationID, installationID, pattern, ownerID, ownerID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]AIContextRepository, 0)
	for rows.Next() {
		row, err := scanAIContext(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *row)
	}
	return out, total, rows.Err()
}

func (s *Store) AIContextMembers(ctx context.Context, repositoryID string) ([]string, error) {
	if _, err := s.GetAIContextRepository(ctx, repositoryID); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, `SELECT user_id FROM ai_context_members WHERE repository_id=? ORDER BY user_id`, repositoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// AIContextChoice exposes only repository names a person already has source
// membership for. Configuration, source and other users' grants stay private.
type AIContextChoice struct {
	ID       string `json:"id"`
	FullName string `json:"full_name"`
}

type AIContextConnectionSelection struct {
	Items                 []AIContextChoice `json:"items"`
	Total                 int               `json:"total"`
	Limit                 int               `json:"limit"`
	Offset                int               `json:"offset"`
	SelectedRepositoryIDs []string          `json:"selected_repository_ids"`
	// PublishRepositoryIDs is the subset this connection may also write
	// notes to, likewise complete rather than per page.
	PublishRepositoryIDs []string `json:"publish_repository_ids"`
}

// AIContextConnectionChoices checks the live owner/client/grant on every query,
// so revocation closes both metadata discovery and the subsequent consent write.
func (s *Store) AIContextConnectionChoices(ctx context.Context, grantID, userID string, limit, offset int) (*AIContextConnectionSelection, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, fmt.Errorf("choose a page size between 1 and 100 and a non-negative offset")
	}
	const active = `EXISTS(SELECT 1 FROM oauth_grants g JOIN users u ON u.id=g.user_id JOIN oauth_clients c ON c.id=g.client_id WHERE g.id=? AND g.user_id=? AND g.revoked_at IS NULL AND c.revoked_at IS NULL AND u.disabled=0)`
	var valid bool
	if err := s.read.QueryRowContext(ctx, `SELECT `+active, grantID, userID).Scan(&valid); err != nil {
		return nil, err
	}
	if !valid {
		return nil, ErrNotFound
	}
	const eligible = ` FROM ai_context_repositories r JOIN ai_context_members m ON m.repository_id=r.id WHERE m.user_id=? AND r.available=1 AND ` + active
	out := &AIContextConnectionSelection{Items: []AIContextChoice{}, SelectedRepositoryIDs: []string{}, PublishRepositoryIDs: []string{}, Limit: limit, Offset: offset}
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*)`+eligible, userID, grantID, userID).Scan(&out.Total); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, `SELECT r.id,r.full_name`+eligible+` ORDER BY r.full_name,r.id LIMIT ? OFFSET ?`, userID, grantID, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var choice AIContextChoice
		if err := rows.Scan(&choice.ID, &choice.FullName); err != nil {
			rows.Close()
			return nil, err
		}
		out.Items = append(out.Items, choice)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// Return the complete eligible selection, not merely the current page. A
	// save on page one must not quietly revoke choices made on page two.
	rows, err = s.read.QueryContext(ctx, `SELECT r.id,(SELECT a.publish FROM ai_context_connection_repositories a WHERE a.repository_id=r.id AND a.grant_id=?)`+eligible+` AND EXISTS(SELECT 1 FROM ai_context_connection_repositories a WHERE a.repository_id=r.id AND a.grant_id=?) ORDER BY r.id`, grantID, userID, grantID, userID, grantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var publish int
		if err := rows.Scan(&id, &publish); err != nil {
			return nil, err
		}
		out.SelectedRepositoryIDs = append(out.SelectedRepositoryIDs, id)
		if publish == 1 {
			out.PublishRepositoryIDs = append(out.PublishRepositoryIDs, id)
		}
	}
	return out, rows.Err()
}

func (s *Store) FindAIContextRepository(ctx context.Context, key aicontext.RepositoryKey) (*AIContextRepository, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	row, err := scanAIContext(s.read.QueryRowContext(ctx, `SELECT `+aiContextReadColumns+` FROM ai_context_repositories WHERE github_host=? AND installation_id=? AND repository_id=?`, key.GitHubHost, key.InstallationID, key.RepositoryID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return row, err
}

// Reader metadata is gated in the query, before paging: an ordinary fleet
// viewer cannot enumerate private drafts, names or configuration.
func (s *Store) ListAIContextReaderRepositories(ctx context.Context, userID string, limit, offset int, query string) ([]AIContextChoice, int, error) {
	if limit < 1 || limit > 100 || offset < 0 || len(query) > 200 {
		return nil, 0, fmt.Errorf("choose a bounded page and search query")
	}
	pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query) + "%"
	const from = ` FROM ai_context_repositories r JOIN ai_context_members m ON m.repository_id=r.id JOIN users u ON u.id=m.user_id WHERE m.user_id=? AND u.disabled=0 AND r.available=1 AND r.full_name LIKE ? ESCAPE '\'`
	var total int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*)`+from, userID, pattern).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.read.QueryContext(ctx, `SELECT r.id,r.full_name`+from+` ORDER BY r.full_name,r.id LIMIT ? OFFSET ?`, userID, pattern, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []AIContextChoice{}
	for rows.Next() {
		var c AIContextChoice
		if err := rows.Scan(&c.ID, &c.FullName); err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

// ListAIContextGrantedRepositories reveals only repositories selected for this
// connection, never the owner's wider membership or other connections' grants.
func (s *Store) ListAIContextGrantedRepositories(ctx context.Context, grantID, userID string, limit, offset int, query string) ([]AIContextChoice, int, error) {
	if limit < 1 || limit > 100 || offset < 0 || len(query) > 200 {
		return nil, 0, fmt.Errorf("choose a bounded page and search query")
	}
	pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query) + "%"
	const from = ` FROM ai_context_repositories r JOIN ai_context_connection_repositories a ON a.repository_id=r.id JOIN oauth_grants g ON g.id=a.grant_id JOIN oauth_clients c ON c.id=g.client_id JOIN ai_context_members m ON m.repository_id=r.id AND m.user_id=g.user_id JOIN users u ON u.id=g.user_id WHERE g.id=? AND g.user_id=? AND g.revoked_at IS NULL AND c.revoked_at IS NULL AND u.disabled=0 AND r.available=1 AND r.full_name LIKE ? ESCAPE '\'`
	var total int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*)`+from, grantID, userID, pattern).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.read.QueryContext(ctx, `SELECT r.id,r.full_name`+from+` ORDER BY r.full_name,r.id LIMIT ? OFFSET ?`, grantID, userID, pattern, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []AIContextChoice{}
	for rows.Next() {
		var choice AIContextChoice
		if err := rows.Scan(&choice.ID, &choice.FullName); err != nil {
			return nil, 0, err
		}
		out = append(out, choice)
	}
	return out, total, rows.Err()
}
