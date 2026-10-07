/**
 * Whether AI Context needs somebody, and how many repositories it is for.
 *
 * The controller raises one problem for each repository whose last workflow run
 * failed, and says which repository in the problem's target. Those problems are
 * already on every page, so the answer needs no request of its own and follows the
 * stream: the badge appears when a run fails and goes when the next one passes.
 *
 * It counts repositories whose run failed, and nothing else. A repository waiting
 * for its setup pull request to be merged is not failing, and one that has never
 * been enabled is not anybody's problem, so a fleet with AI Context working, or
 * with none, has no badge at all.
 */
import type { Problem } from '../api/types';

export interface AiContextAttention {
  /** Repositories whose last AI Context workflow run failed. */
  count: number;
  /** The worst severity among them, which is how the badge is drawn. */
  worst: 'error' | 'warning';
  /** The same, in a sentence, for a tooltip and for somebody who cannot see the badge. */
  text: string;
}

export function aiContextAttention(
  problems: readonly Pick<Problem, 'target_kind' | 'severity'>[],
): AiContextAttention | null {
  const failing = problems.filter((problem) => problem.target_kind === 'ai_context');
  if (failing.length === 0) return null;
  const count = failing.length;
  return {
    count,
    worst: failing.some((problem) => problem.severity === 'error') ? 'error' : 'warning',
    text: count === 1 ? '1 repository needs attention' : `${count} repositories need attention`,
  };
}
