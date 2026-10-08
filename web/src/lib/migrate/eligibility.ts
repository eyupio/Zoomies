/**
 * Whether one repository in a plan can be migrated, and why not when it cannot.
 *
 * This lives on its own because two places need the same answer: the
 * Repositories step, which decides what an operator may tick, and the wizard,
 * which decides what to tick by default and which repositories to name in the
 * call that opens the pull requests. Those two disagreeing is how a repository
 * that is greyed out on screen ends up in the request anyway.
 */

import type { MigrationRepo } from '$lib/api/types';

/** The API's MaxApplyRepos limit: a migration opens at most this many pull requests. */
export const MAX_SELECTED_REPOSITORIES = 25;

/** How many runs-on lines the migration would rewrite in this repository. */
export function jobsIn(repo: MigrationRepo): number {
  return (repo.workflows ?? []).reduce((n, w) => n + (w.rewrites ?? []).length, 0);
}

/** How many files it would change. */
export function filesIn(repo: MigrationRepo): number {
  return (repo.workflows ?? []).filter((w) => (w.rewrites ?? []).length > 0).length;
}

/** How many runs-on lines it would deliberately leave alone. */
export function skipsIn(repo: MigrationRepo): number {
  return (repo.workflows ?? []).reduce((n, w) => n + (w.skips ?? []).length, 0);
}

/**
 * Whether this repository is one the operator can choose.
 *
 * The test is "it asks for a runner somebody else operates", not "it would
 * change under the mapping guessed so far" -- the labels are mapped on the
 * next step. Being archived is the exception worth spelling out: an archived
 * repository can ask for as many rented runners as it likes, and GitHub still
 * refuses every write to it, so counting its labels alone would tick it, walk
 * the operator through mapping and review, and only then report that nothing
 * could be opened.
 */
export function isMigratable(repo: MigrationRepo): boolean {
  return !repo.error && !repo.archived && (repo.hosted_labels ?? []).length > 0;
}

/**
 * One line saying what this repository would get out of the migration, or why
 * it is not on offer.
 *
 * The order is the order an operator needs the answers in: a repository that
 * could not be read explains itself first, one that cannot be written to next,
 * then what would change, then what was found but not yet mapped -- and only
 * then the several different ways of having nothing to do. "Already on
 * Zoomies" and "nothing to move" look identical in a plan and mean opposite
 * things -- finished work, and work nobody has started -- so they are never
 * collapsed into one sentence.
 */
export function noteFor(repo: MigrationRepo): string {
  if (repo.error) return repo.error;
  if (repo.archived) return 'Archived, accepts no pull requests';

  const jobs = jobsIn(repo);
  const files = filesIn(repo);
  const skipped = skipsIn(repo);
  const labels = repo.hosted_labels ?? [];

  if (jobs > 0) {
    const moved = `${jobs} ${jobs === 1 ? 'job' : 'jobs'} in ${files} ${files === 1 ? 'file' : 'files'}`;
    return repo.on_zoomies ? `${moved}; the rest already on Zoomies` : moved;
  }
  if (labels.length > 0) {
    return `Runs on ${labels.join(', ')}; choose what those become on the next step`;
  }
  if ((repo.workflows ?? []).length === 0) return 'No workflows';
  if (repo.on_zoomies) {
    return skipped > 0 ? `Already on Zoomies; ${skipped} left alone` : 'Already on Zoomies';
  }
  return skipped > 0 ? `Nothing to move; ${skipped} left alone` : 'Nothing to move';
}
