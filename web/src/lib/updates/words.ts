/**
 * What the Updates page says, as functions of what the API answered.
 *
 * When auto would take a release is in the conditional, because whether it
 * does is the planner's to say (a missing helper or a halted rollout holds it),
 * and that sentence is shown beside the line. What each mode does is described
 * as it is. An update a person has asked for is in the present,
 * and only as far as the controller has said so: a sentence that an update
 * worked is written from an attempt the controller closed as succeeded, or from
 * a build it reports running the release, and never from a request having been
 * accepted. The controller's own sentence for each state travels with the status
 * and is shown as it was given; these are the lines around it, kept out of the
 * components so that they are tested once and read the same wherever they appear.
 */
import type { Host, Role, UpdatesStatus } from '../api/types';
import { describeWindow, parseGoDuration, pluralise, toMillis } from '../format';
import type { RestartCopy } from '../settings/restart-copy';

export type UpdateMode = UpdatesStatus['mode'];
/** The controller's latest update attempt, open or ended. */
export type ControllerAttempt = NonNullable<UpdatesStatus['controller']>;

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
    'Zoomies offers the newest release that can be installed on this system, with an Update button for this controller and for each host whose update helper is installed, and one on the Hosts page that updates every host behind this controller, one at a time. Nothing moves until someone presses one.',
  // The cost is part of the choice: a release replaced inside the soak is never
  // taken, so a project that publishes faster than the soak is never updated by
  // auto, and an operator is told so before they depend on it. So is what stops
  // it, because a halted rollout waits for a person and somebody has to know to
  // look.
  auto: 'Zoomies takes the newest release that can be installed on this system once it has been public for the soak: it updates this controller through its update helper, then every host behind the controller, one at a time. A newer release restarts the wait, so if releases are published faster than the soak, auto never takes one. A host whose update fails halts the rollout until an administrator resumes or cancels it.',
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
  if (mode === 'manual') return `Manual offers ${target.tag} and waits for a person to take it.`;
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

/* -- updating the controller ---------------------------------------------- */

/** Release binaries report 1.3.5 where the tag is v1.3.5, so the two are compared without the v. */
function bare(version: string): string {
  return version.trim().replace(/^v/, '');
}

/** Whether the build the controller reports is the release the attempt asked for. */
export function runsRelease(running: string, tag: string): boolean {
  return bare(running) !== '' && bare(running) === bare(tag);
}

/**
 * Where to look when an update did not end well. Both are on the controller's
 * host, and neither names a path, so every role may be told.
 */
export const HELPER_LOOK_AT =
  "Look at zoomies updates helper status, and at the journal of the unit zoomies-update (journalctl -u zoomies-update), on the controller's host.";

export interface AttemptWords {
  /** One word for the badge. */
  label: string;
  /** Never a status colour except for an update that did not work, which is what danger is for. */
  tone: 'neutral' | 'accent' | 'danger';
  title: string;
  detail: string;
  /** Still in flight: the page keeps watching and says nothing final. */
  open: boolean;
  /** Where to look when it did not end well, or empty. */
  lookAt: string;
}

/**
 * The controller's latest attempt, in words.
 *
 * `running` is the build the controller reports now. An attempt still open
 * while the controller already runs the release it asked for is shown as done:
 * the new process records the ending a moment after it starts, and for that
 * moment the status already says what happened. That is the controller's word,
 * not this page's guess. An open attempt with the old build running is never
 * shown as done, however long the page has waited.
 */
export function attemptWords(attempt: ControllerAttempt, running: string): AttemptWords {
  const { to, from } = attempt;
  const arrived = runsRelease(running, to);
  const state = attempt.state === 'requested' && arrived ? 'succeeded' : attempt.state;
  // Which build the controller runs, said without "still" once it is the target:
  // an attempt that ended without the update can be read after somebody upgraded
  // by hand, and "still" is a claim about the build it left.
  const runs = arrived
    ? `This controller runs ${running} now.`
    : `This controller still runs ${running}.`;
  switch (state) {
    case 'requested':
      return {
        label: 'In progress',
        tone: 'accent',
        title: `Updating to ${to}`,
        detail:
          `The update from ${from} was asked for. The update helper on the controller's host installs ${to} and the controller restarts. ` +
          'This page says how it ended when the controller does, and not before.',
        open: true,
        lookAt: '',
      };
    case 'succeeded':
      return {
        label: 'Updated',
        tone: 'accent',
        title: `Updated to ${to}`,
        detail: `This controller was on ${from} and runs ${running} now.`,
        open: false,
        lookAt: '',
      };
    case 'timed_out':
      return {
        label: 'Timed out',
        tone: 'danger',
        title: `The update to ${to} timed out`,
        detail: arrived
          ? `No answer came from the update helper within 90 minutes. ${runs}`
          : `No answer came from the update helper within 90 minutes, and this controller still runs ${running}.`,
        open: false,
        lookAt: HELPER_LOOK_AT,
      };
    case 'cancelled':
      return {
        label: 'Cancelled',
        tone: 'neutral',
        title: `The update to ${to} was cancelled`,
        detail: runs,
        open: false,
        lookAt: '',
      };
    default:
      return {
        label: 'Failed',
        tone: 'danger',
        title: `The update to ${to} did not succeed`,
        detail: runs,
        open: false,
        lookAt: HELPER_LOOK_AT,
      };
  }
}

/** Whether an attempt is one the page is waiting on, read the way `attemptWords` reads it. */
export function attemptIsOpen(attempt: ControllerAttempt | null, running: string): boolean {
  return attempt !== null && attemptWords(attempt, running).open;
}

export type UpdateOffer =
  /** The button is offered, for this release. */
  | { kind: 'offer'; tag: string }
  /** An attempt is open, so the progress is shown in place of the button. */
  | { kind: 'in-flight' }
  /**
   * No button, and the sentence says why. At most one command comes with it: the
   * install where the helper is missing, or the upgrade by hand where it can
   * never be installed.
   */
  | { kind: 'none'; sentence: string; installCommand: string; upgradeCommand: string };

/**
 * Whether the controller can be updated from this page, by this reader, and if
 * not, why not in the words of the first thing in the way.
 *
 * The order is the order an operator would fix them in: who you are, whether
 * updating is on, whether this build is one that updates, whether the helper is
 * there (or can never be), and last whether there is anything to take.
 */
export function controllerOffer(status: UpdatesStatus, canPlatform: boolean): UpdateOffer {
  if (attemptIsOpen(status.controller, status.running.version)) return { kind: 'in-flight' };
  const none = (sentence: string, installCommand = '', upgradeCommand = ''): UpdateOffer => ({
    kind: 'none',
    sentence,
    installCommand,
    upgradeCommand,
  });
  if (!canPlatform) return none('Updating the controller needs the platform role.');
  if (status.mode === 'off')
    return none('Updating is off. Set updates.mode to manual or auto to update from here.');
  if (!status.running.release)
    return none('This build is not from a release, so there is nothing to update it to from here.');
  // A helper that can never be installed is never offered the command that
  // installs one, whatever arrived beside it: that command would only refuse.
  if (status.helper.state === 'unsupported')
    return none(status.helper.reason, '', status.helper.upgrade_command);
  if (status.helper.state !== 'ready')
    return none(status.helper.reason, status.helper.install_command);
  const target = status.target;
  if (!target?.newer)
    return none('No newer release is on offer, so there is nothing to update to.');
  return { kind: 'offer', tag: target.tag };
}

/**
 * What the confirmation says before the controller is updated.
 *
 * The last line is as true as the code that makes the copy: the store copies the
 * database before it applies a migration, and only when one is pending, so a
 * release that changes nothing there takes none.
 */
export function confirmControllerUpdate(
  tag: string,
  running: string,
): {
  title: string;
  description: string;
  consequences: readonly string[];
  confirmLabel: string;
} {
  return {
    title: 'Update the controller',
    description: `Update this controller from ${running} to ${tag}?`,
    consequences: [
      'The controller restarts. This page loses its connection for a short while and finds it again by itself.',
      'Jobs that are running keep running.',
      'If the release changes the database, a copy of it is kept first beside the database, and the last two copies are kept. A release that changes nothing there takes none.',
    ],
    confirmLabel: `Update to ${tag}`,
  };
}

/** What the restart state says while the controller is stopped and started again by an update. */
export const UPDATE_RESTART: RestartCopy = {
  titles: {
    stopping: 'Waiting for the controller to restart',
    starting: 'Waiting for the controller to answer',
    back: 'The controller is answering again',
    'stuck-up': 'The controller has not stopped',
    'stuck-down': 'The controller has not come back yet',
  },
  stopping: 'The update helper is replacing the controller. This page waits for the restart.',
  starting:
    'This page has lost its connection to the controller, which is what the restart of an update looks like. It finds the controller again by itself. Jobs that are running keep running.',
  back: 'Reloading, to read how the update ended.',
  stuckUp: () => 'The controller is still answering.',
  stuckDown: (seconds) =>
    `The controller has not answered in ${pluralise(Math.round(seconds / 60), 'minute')}. ` +
    'It serves nothing while it migrates its database, which on a large one can take minutes, so it may still be starting. ' +
    `The update stays open until the controller says how it ended, and a build that fails to start is not rolled back for you. ${HELPER_LOOK_AT}`,
  command: 'zoomies updates helper status',
};

/* -- updating the hosts ----------------------------------------------------- */

/** The open rollout, or the last one that ended. */
export type Rollout = NonNullable<UpdatesStatus['rollout']>;

/** What the hosts action reads of a host: its own update block, as its card does. */
export type RolloutHost = Pick<Host, 'embedded' | 'version_skew' | 'update'>;

export interface RolloutWords {
  /** One word for the badge. */
  label: string;
  /**
   * A status colour only where the mapping gives one: halted is held until an
   * operator acts, which is draining, and done is idle. A running rollout is
   * news, as an update in flight is, and a cancelled one is a fact.
   */
  tone: 'accent' | 'draining' | 'idle' | 'neutral';
  title: string;
  /** How many hosts it has updated, of how many. */
  progress: string;
  /** The host being updated now, or the controller's sentence for the halt, or empty. */
  detail: string;
  /** What Resume and Cancel each do, for a halted rollout; empty otherwise. */
  next: string;
  /** Running or halted: Cancel is offered. */
  open: boolean;
  /** Halted: Resume is offered. */
  canResume: boolean;
  /** What the polite region says. */
  live: string;
}

/**
 * The rollout, in words. Only the controller's figures and sentences are used:
 * which host is next, and why, is the planner's, and the status shows its
 * sentence beside this rather than this page guessing at it.
 */
export function rolloutWords(rollout: Rollout): RolloutWords {
  const { target, done, total } = rollout;
  const progress = `${done} of ${pluralise(total, 'host')} updated.`;
  const words = (
    over: Pick<RolloutWords, 'label' | 'tone' | 'title'> & Partial<RolloutWords>,
  ): RolloutWords => {
    const all: RolloutWords = {
      progress,
      detail: '',
      next: '',
      open: false,
      canResume: false,
      live: '',
      ...over,
    };
    all.live = `${all.title}. ${progress}${all.detail ? ` ${all.detail}` : ''}`;
    return all;
  };
  switch (rollout.state) {
    case 'running':
      return words({
        label: 'Rolling out',
        tone: 'accent',
        title: `Updating hosts to ${target}`,
        detail: rollout.current ? `${rollout.current} is being updated now.` : '',
        open: true,
      });
    case 'halted':
      // What each button does is the API's own account of it: the failure stays
      // on that host's attempt, and an update already handed over finishes.
      return words({
        label: 'Halted',
        tone: 'draining',
        title: `The rollout to ${target} is halted`,
        detail:
          rollout.halted_reason ||
          "A host's update did not succeed, so nothing more is updated until an administrator acts.",
        next:
          'Resume goes on to the next host, and the one that failed waits out its retry. ' +
          'Cancel stops the rollout; an update a host has already been handed finishes by itself.',
        open: true,
        canResume: true,
      });
    case 'done':
      return words({ label: 'Done', tone: 'idle', title: `The rollout to ${target} is done` });
    default:
      return words({
        label: 'Cancelled',
        tone: 'neutral',
        title: `The rollout to ${target} was cancelled`,
      });
  }
}

/** Whether a rollout is still moving or waiting for a person. */
export function rolloutIsOpen(rollout: Rollout | null | undefined): boolean {
  return rollout?.state === 'running' || rollout?.state === 'halted';
}

export type RolloutOffer =
  /** "Update N hosts", for the release the controller runs. */
  | { kind: 'offer'; count: number; tag: string }
  /** Drawn and not pressable, with the reason beside it, as a host card is with the mode off. */
  | { kind: 'off'; sentence: string }
  | { kind: 'none' };

/**
 * Whether an administrator is offered to update every host that is behind.
 *
 * The count is the hosts whose own update block says they can be asked now,
 * the answer each card's button is drawn from, so the page never works out
 * eligibility for itself. With the mode off no block says so, and the action is
 * drawn disabled while any host is behind, which is the card's fact too.
 */
export function rolloutOffer(
  status: UpdatesStatus | null,
  hosts: readonly RolloutHost[],
  canAdmin: boolean,
): RolloutOffer {
  if (!canAdmin || !status || rolloutIsOpen(status.rollout)) return { kind: 'none' };
  if (status.mode === 'off') {
    return hosts.some((h) => h.version_skew === 'behind' && !h.embedded)
      ? {
          kind: 'off',
          sentence:
            'Updating is off. Set updates.mode to manual or auto to update hosts from here.',
        }
      : { kind: 'none' };
  }
  const count = hosts.filter((h) => h.update?.can_update === true).length;
  const tag = bare(status.running.version);
  if (count === 0 || tag === '') return { kind: 'none' };
  return { kind: 'offer', count, tag: `v${tag}` };
}

/**
 * What the confirmation says before every host behind is updated.
 *
 * Each line is as true as the code behind it. The planner asks one host at a
 * time and waits for it to report the release; the agent's restart does not
 * wait for its jobs, which keep running and are taken over, so it is not said
 * to; the first failure halts the rollout; and a host that has failed twice on
 * this release is counted in the button but passed over by the planner.
 */
export function confirmRollout(
  tag: string,
  count: number,
): {
  title: string;
  description: string;
  consequences: readonly string[];
  confirmLabel: string;
} {
  const hosts = pluralise(count, 'host');
  return {
    title: `Update ${hosts}`,
    description: `Update ${hosts} to ${tag}, one at a time?`,
    consequences: [
      `The host running the fewest jobs goes first, and each waits until the one before it reports ${tag}.`,
      'Each host’s agent restarts when its turn comes. Jobs that are running keep running: their runners stay in place and the new agent takes them over. The restart does not wait for them to finish.',
      'If one host’s update fails or times out, the rollout halts, and nothing more is updated until an administrator resumes or cancels it.',
      `A host whose update to ${tag} has already failed twice is skipped, and left for a person to update.`,
    ],
    confirmLabel: `Update ${hosts}`,
  };
}
