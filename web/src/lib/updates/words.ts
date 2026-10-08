/**
 * What the Updates page says, as functions of what the API answered.
 *
 * Everything here is in the conditional. This part of the product reads what an
 * update mode would do and installs nothing, so a sentence saying an update is
 * happening, or will, would promise what nothing keeps. The controller's own
 * sentence for each state travels with the status and is shown as it was given;
 * these are the lines around it, kept out of the components so that they are
 * tested once and read the same wherever they appear.
 */
import type { Role, UpdatesStatus } from '../api/types';
import { describeWindow, parseGoDuration, pluralise, toMillis } from '../format';

export type UpdateMode = UpdatesStatus['mode'];

const HOUR = 3_600_000;
const MINUTE = 60_000;

const MODE_LABELS: Record<UpdateMode, string> = { off: 'Off', manual: 'Manual', auto: 'Auto' };

/** The setting's own word for the mode, capitalised for a badge. */
export function modeLabel(mode: UpdateMode): string {
  return MODE_LABELS[mode];
}

const MODE_SENTENCES: Record<UpdateMode, string> = {
  off: 'Zoomies tells you when a newer release exists and does nothing about it. Updating stays a command you run yourself.',
  manual:
    'Zoomies would offer the newest release that can be installed on this system and wait for a person to take it. Nothing would move until someone asks.',
  // The cost is part of the choice: a release replaced inside the soak is never
  // taken, so a project that publishes faster than the soak is never updated by
  // auto, and an operator is told so before they depend on it.
  auto: 'Zoomies would take the newest release that can be installed on this system once it has been public for the soak. A newer release restarts the wait, so if releases are published faster than the soak, auto never takes one.',
};

/** What the mode would do, and what it costs, in the words the choice is made by. */
export function describeMode(mode: UpdateMode): string {
  return MODE_SENTENCES[mode];
}

/**
 * A length of time as the controller's own sentences say it: whole hours from
 * an hour up, whole minutes below that. It rounds down, as they do, because the
 * line this feeds sits beside one of theirs and two different times for one
 * release is worse than either being a little short.
 */
function spanText(ms: number): string {
  if (ms >= HOUR) return pluralise(Math.floor(ms / HOUR), 'hour');
  if (ms >= MINUTE) return pluralise(Math.floor(ms / MINUTE), 'minute');
  return 'less than a minute';
}

/**
 * What the mode would do about the release, in one line, or nothing when there
 * is nothing to take. Only a release ahead of the build is something to take;
 * why there is not one is the controller's sentence, shown beside this.
 *
 * `now` is a parameter so the line can be tried at the second it changes. The
 * page passes none: the line is worked out when a status arrives, which is when
 * the controller's sentence beside it was, so the two count from the same moment.
 */
export function targetLine(
  status: Pick<UpdatesStatus, 'mode' | 'target'>,
  now: number = Date.now(),
): string {
  const { mode, target } = status;
  if (mode === 'off' || !target?.newer) return '';
  if (mode === 'manual')
    return `Manual would offer ${target.tag} and wait for a person to take it.`;
  // A due time that cannot be read leaves the line without one rather than
  // with a date that is not a date.
  const due = toMillis(target.due_at);
  if (due === null) return `Auto would take ${target.tag}.`;
  if (due <= now) return `Auto would take ${target.tag} now.`;
  return `Auto would take ${target.tag} in ${spanText(due - now)}.`;
}

export interface UpdateState {
  label: string;
  /** Never a status colour: nothing is wrong when a release is waiting, and those are the fleet's. */
  tone: 'neutral' | 'accent';
}

/**
 * The state the status is in, as one word. They are the words the controller
 * opens its own sentences with, so the badge and the sentence under it agree.
 */
export function updateState(status: UpdatesStatus, now: number = Date.now()): UpdateState {
  if (status.mode === 'off') return { label: 'Off', tone: 'neutral' };
  // A build that is not from a release is left alone whether or not a list was
  // read: one from main never reads it, and the sentence for either is the same.
  if (!status.running.release) return { label: 'Left alone', tone: 'neutral' };
  if (status.checked_at === null) return { label: 'Not read yet', tone: 'neutral' };
  const { target } = status;
  if (target === null) return { label: 'Nothing to take', tone: 'neutral' };
  if (!target.newer) return { label: 'Nothing newer', tone: 'neutral' };
  if (status.mode === 'manual') return { label: 'Available', tone: 'accent' };
  const due = toMillis(target.due_at);
  if (due === null) return { label: 'Available', tone: 'accent' };
  return { label: due > now ? 'Waiting' : 'Ready', tone: 'accent' };
}

/** The soak as a period, and zero as what it means: auto does not wait. */
export function soakText(soak: string): string {
  if (parseGoDuration(soak) === 0) return 'No wait';
  // Whatever this page cannot read is shown as it came rather than dropped.
  return describeWindow(soak) || soak;
}

/** Who the soak applies to, since it is shown in every mode. */
export function soakNote(mode: UpdateMode): string {
  return mode === 'auto'
    ? 'Counted from the day GitHub published the release.'
    : 'Only auto waits; manual and off ignore it.';
}

/** What the build says about where it came from. */
export function buildText(release: boolean): string {
  return release ? 'From a release' : 'Not from a release, so updates leave it alone';
}

/**
 * The address to link a release to, or null to show its tag as plain text.
 *
 * The address is GitHub's `html_url`, which is text that came off the network
 * and is going into an `href`. Only a web page over TLS is plainly one; a
 * `javascript:` or `data:` address would run in this origin, and a plain `http:`
 * one is a link that can be rewritten on the way.
 */
export function releaseHref(url: string | null | undefined): string | null {
  return typeof url === 'string' && url.startsWith('https://') ? url : null;
}

/**
 * Where the mode is changed, for a reader who can see it there, or null.
 *
 * `updates.mode` and `updates.soak` are platform-scoped, and the settings list
 * leaves out a platform row for anyone below that role, so a link offered to an
 * administrator lands on a Configuration page that does not show the setting.
 */
export function modeSettingHref(can: (needed: Role) => boolean): string | null {
  return can('platform') ? '/settings/configuration?setting=updates.mode' : null;
}
