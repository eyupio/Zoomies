package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// The bounds on assistant-written notes. A note is prose about a repository,
// not a copy of it, so it is small; versions are kept so an edit never loses
// what an earlier run concluded, but not without end.
const (
	MaxArtifactBodyBytes      = 128 << 10
	MaxArtifactTitle          = 200
	MaxArtifactVersions       = 20
	MaxArtifactsPerRepository = 100
)

// ErrInvalidArtifact is a note that breaks one of the rules every note must
// keep; the wrapped text says which, for the person or agent to fix.
var ErrInvalidArtifact = errors.New("invalid note")

var artifactSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ArtifactKinds are the kinds of note an assistant may publish.
var ArtifactKinds = map[string]bool{"report": true, "plan": true, "note": true}

// AIContextArtifact is one version of an assistant-written note.
type AIContextArtifact struct {
	ID           string    `json:"id"`
	RepositoryID string    `json:"repository_id"`
	Slug         string    `json:"slug"`
	Version      int       `json:"version"`
	Kind         string    `json:"kind"`
	Title        string    `json:"title"`
	Body         string    `json:"body,omitempty"`
	SourceCommit string    `json:"source_commit"`
	AuthorName   string    `json:"author_name"`
	ViaKind      string    `json:"via_kind"`
	ViaName      string    `json:"via_name,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	// Versions is how many versions of this slug are kept; set on listings.
	Versions int `json:"versions,omitempty"`
}

// PublishAIContextArtifact stores a new version of a note, numbering it after
// the slug's latest. Who may publish is decided before this is called; this
// enforces what any note must be, whoever wrote it.
func (s *Store) PublishAIContextArtifact(ctx context.Context, a *AIContextArtifact, authorUserID string) error {
	switch {
	case !artifactSlug.MatchString(a.Slug):
		return fmt.Errorf("%w: name the note with 1-64 lower-case letters, digits and hyphens, starting with a letter or digit", ErrInvalidArtifact)
	case !ArtifactKinds[a.Kind]:
		return fmt.Errorf("%w: choose report, plan or note as the kind", ErrInvalidArtifact)
	case strings.TrimSpace(a.Title) == "" || utf8.RuneCountInString(a.Title) > MaxArtifactTitle || strings.ContainsAny(a.Title, "\r\n\x00"):
		return fmt.Errorf("%w: give the note a one-line title of at most %d characters", ErrInvalidArtifact, MaxArtifactTitle)
	case a.Body == "" || len(a.Body) > MaxArtifactBodyBytes || !utf8.ValidString(a.Body) || strings.ContainsRune(a.Body, 0):
		return fmt.Errorf("%w: a note's body is UTF-8 Markdown of 1 byte to %d KiB", ErrInvalidArtifact, MaxArtifactBodyBytes>>10)
	case a.ViaKind != "user" && a.ViaKind != "token" && a.ViaKind != "connection":
		return fmt.Errorf("%w: unknown publishing route %q", ErrInvalidArtifact, a.ViaKind)
	}
	return wrapWrite(s.tx(ctx, func(tx *sql.Tx) error {
		var available int
		if err := tx.QueryRowContext(ctx, `SELECT available FROM ai_context_repositories WHERE id=?`, a.RepositoryID).Scan(&available); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if available != 1 {
			return fmt.Errorf("%w: notes can only be published to a repository whose context is verified", ErrConflict)
		}
		var latest int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM ai_context_artifacts WHERE repository_id=? AND slug=?`, a.RepositoryID, a.Slug).Scan(&latest); err != nil {
			return err
		}
		if latest == 0 {
			var slugs int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT slug) FROM ai_context_artifacts WHERE repository_id=?`, a.RepositoryID).Scan(&slugs); err != nil {
				return err
			}
			if slugs >= MaxArtifactsPerRepository {
				return fmt.Errorf("%w: this repository already has %d notes; publish a new version of an existing one instead", ErrConflict, MaxArtifactsPerRepository)
			}
		}
		// The commit the repository's verified context was at: what the author
		// could have read when it wrote this.
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT published_commit FROM ai_context_freshness WHERE repository_id=?),'')`, a.RepositoryID).Scan(&a.SourceCommit); err != nil {
			return err
		}
		a.ID, a.Version, a.CreatedAt = NewID(PrefixAIArtifact), latest+1, s.Now().UTC().Truncate(time.Millisecond)
		var author any
		if authorUserID != "" {
			author = authorUserID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ai_context_artifacts(id,repository_id,slug,version,kind,title,body,source_commit,author_user_id,author_name,via_kind,via_name,created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.ID, a.RepositoryID, a.Slug, a.Version, a.Kind, a.Title, a.Body, a.SourceCommit, author, a.AuthorName, a.ViaKind, a.ViaName, ms(a.CreatedAt)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM ai_context_artifacts WHERE repository_id=? AND slug=? AND version<=?`, a.RepositoryID, a.Slug, a.Version-MaxArtifactVersions)
		return err
	}))
}

// ListAIContextArtifacts is the latest version of each note, newest first,
// without bodies.
func (s *Store) ListAIContextArtifacts(ctx context.Context, repositoryID string, limit, offset int) ([]AIContextArtifact, int, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, 0, fmt.Errorf("choose a page size between 1 and 100 and a non-negative offset")
	}
	var total int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(DISTINCT slug) FROM ai_context_artifacts WHERE repository_id=?`, repositoryID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.read.QueryContext(ctx, `SELECT a.id,a.repository_id,a.slug,a.version,a.kind,a.title,'',a.source_commit,a.author_name,a.via_kind,a.via_name,a.created_at,
		(SELECT COUNT(*) FROM ai_context_artifacts v WHERE v.repository_id=a.repository_id AND v.slug=a.slug)
		FROM ai_context_artifacts a WHERE a.repository_id=? AND a.version=(SELECT MAX(version) FROM ai_context_artifacts l WHERE l.repository_id=a.repository_id AND l.slug=a.slug)
		ORDER BY a.created_at DESC, a.slug LIMIT ? OFFSET ?`, repositoryID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []AIContextArtifact{}
	for rows.Next() {
		a, err := scanArtifact(rows, true)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *a)
	}
	return out, total, rows.Err()
}

// GetAIContextArtifact returns one version of a note, or its latest when
// version is zero.
func (s *Store) GetAIContextArtifact(ctx context.Context, repositoryID, slug string, version int) (*AIContextArtifact, error) {
	if version < 0 {
		return nil, fmt.Errorf("%w: choose a positive version", ErrInvalidArtifact)
	}
	row := s.read.QueryRowContext(ctx, `SELECT id,repository_id,slug,version,kind,title,body,source_commit,author_name,via_kind,via_name,created_at FROM ai_context_artifacts
		WHERE repository_id=? AND slug=? AND (?=0 OR version=?) ORDER BY version DESC LIMIT 1`, repositoryID, slug, version, version)
	a, err := scanArtifact(row, false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("note %s: %w", slug, ErrNotFound)
	}
	return a, err
}

func scanArtifact(sc interface{ Scan(...any) error }, withVersions bool) (*AIContextArtifact, error) {
	var a AIContextArtifact
	var created int64
	dest := []any{&a.ID, &a.RepositoryID, &a.Slug, &a.Version, &a.Kind, &a.Title, &a.Body, &a.SourceCommit, &a.AuthorName, &a.ViaKind, &a.ViaName, &created}
	if withVersions {
		dest = append(dest, &a.Versions)
	}
	if err := sc.Scan(dest...); err != nil {
		return nil, err
	}
	a.CreatedAt = at(created)
	return &a, nil
}
