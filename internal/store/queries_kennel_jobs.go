package store

import (
	"context"
	"strings"
	"time"
)

// kennelJobNamesPerQuery bounds how many names one statement carries, so a
// repository that requires a great many checks costs a few statements and not
// one that SQLite refuses.
const kennelJobNamesPerQuery = 50

// KennelJobsSeen says how many jobs the fleet's record holds for a repository
// since a moment, and which of the given job names at least one of them had.
//
// It counts every job the controller was told about, hosted or not, matched or
// not. A required check is posted by whichever runner ran the job, and a job a
// GitHub-hosted runner ran produced it as surely as one of ours did; counting
// only the fleet's own would call every such check missing.
//
// A name is matched exactly, as GitHub matches a required check against the name
// of the check a job posts. The names are a repository's own text, so they are
// only ever bound as parameters and the answer is a set of the names that were
// asked about, never anything read back from the table.
func (s *Store) KennelJobsSeen(ctx context.Context, repo string, since time.Time, names []string) (seen int, reported map[string]bool, err error) {
	if err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM jobs WHERE repo = ? AND queued_at >= ?`,
		repo, ms(since)).Scan(&seen); err != nil {
		return 0, nil, err
	}
	reported = make(map[string]bool, len(names))
	for start := 0; start < len(names); start += kennelJobNamesPerQuery {
		batch := names[start:min(start+kennelJobNamesPerQuery, len(names))]
		args := make([]any, 0, len(batch)+2)
		args = append(args, repo, ms(since))
		for _, n := range batch {
			args = append(args, n)
		}
		rows, err := s.read.QueryContext(ctx,
			`SELECT DISTINCT job_name FROM jobs WHERE repo = ? AND queued_at >= ? AND job_name IN (?`+
				strings.Repeat(",?", len(batch)-1)+`)`, args...)
		if err != nil {
			return 0, nil, err
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return 0, nil, err
			}
			reported[name] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return 0, nil, err
		}
		rows.Close()
	}
	return seen, reported, nil
}
