/**
 * What the controller's dry run comes to, in a line.
 *
 * The sticky bar beside the Create button is the one place the whole pool's
 * state is on screen at once, so what it says has to be decided in one place
 * and held by a test: whether something is wrong, whether nothing has changed,
 * whether the controller has not been heard from, and -- when all of that is
 * clear -- the sentence that matters most to a pool nobody has run yet, how
 * many machines can actually run it.
 *
 * Pure, so the wording can be pinned. The order of the checks is the point: a
 * rule the browser can see is said before anything the controller thinks, and
 * a controller that has not answered is said before it is trusted.
 */
import type { Problem, Result } from '$lib/api/types';
import { pluralise } from '$lib/format';
import { isStartupFinding } from './startupStability';

type Verdict = Result<'validatePool'>;

/**
 * The warnings that have somewhere better to be. The sections that change them
 * render the three room warnings themselves, with the fix one click away; the
 * fleet's own account of which host is out is the controller panel's; and the
 * startup recommendations have a panel with the button that applies them.
 */
const ROOM_CODES = ['pool.max_above_room', 'pool.host_overcommitted', 'pool.cache_above_disk'];

export function visibleWarnings(verdict: Verdict | null): Problem[] {
  return (verdict?.warnings ?? []).filter(
    (warning) =>
      warning.code !== 'pool.no_matching_hosts' &&
      !ROOM_CODES.includes(warning.code ?? '') &&
      !isStartupFinding(warning),
  );
}

export interface Status {
  tone: 'neutral' | 'ok' | 'warning' | 'danger';
  text: string;
  /** Present when the line has something to open: the first thing to fix. */
  action?: string;
}

export interface StatusInput {
  /** The rules the browser can check on its own, however many are being broken. */
  blockers: number;
  /** How many of the controller's refusals the browser has not already said. */
  refusals: number;
  editing: boolean;
  /** Whether an edit has changed anything. Always true for a pool being created. */
  dirty: boolean;
  /** The dry run failed to answer. */
  unreachable: boolean;
  verdict: Verdict | null;
  /** True once the fleet has been asked, so a count of none is a fact and not a blank. */
  fleetKnown: boolean;
}

export function statusLine(input: StatusInput): Status {
  const { blockers, refusals, editing, dirty, unreachable, verdict, fleetKnown } = input;
  const saved = editing ? 'saved' : 'created';

  if (blockers > 0) {
    return {
      tone: 'danger',
      text:
        blockers === 1
          ? `1 thing to fix before this can be ${saved}.`
          : `${blockers} things to fix before this can be ${saved}.`,
      action: 'Show',
    };
  }
  if (editing && !dirty) return { tone: 'neutral', text: 'No changes yet.' };
  if (unreachable) {
    return {
      tone: 'warning',
      text: 'The controller could not check this pool. It is checked again when you save.',
    };
  }
  if (verdict === null) return { tone: 'neutral', text: 'Checking with the controller…' };
  if (refusals > 0) {
    return {
      tone: 'danger',
      text: 'The controller will not accept this pool yet.',
      action: 'Show',
    };
  }

  const matching = verdict.matching_hosts;
  if (fleetKnown && matching === 0) {
    return {
      tone: 'warning',
      text: `No connected host can run this pool yet. It is ${saved} anyway, and waits for one.`,
    };
  }
  const warnings = visibleWarnings(verdict).length;
  if (warnings > 0) {
    return {
      tone: 'warning',
      text: `Ready, with ${warnings === 1 ? 'a warning' : `${warnings} warnings`} to read below.`,
    };
  }
  const runners = verdict.room?.runners ?? 0;
  if (matching !== undefined && matching > 0) {
    return {
      tone: 'ok',
      text:
        runners > 0
          ? `Room for ${pluralise(runners, 'runner')} on ${pluralise(matching, 'host')}.`
          : `${pluralise(matching, 'connected host')} can run this pool.`,
    };
  }
  return { tone: 'neutral', text: 'Nothing to flag.' };
}
