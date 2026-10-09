/**
 * What a host's card says about updating it from the web UI.
 *
 * Every word is a function of the host as the controller sent it, and nothing
 * here reads a clock: the controller's `update` block holds nothing that moves
 * with a heartbeat, so the card is repainted only when an attempt changes. A
 * sentence that an update worked is written from an attempt the controller
 * closed as succeeded, or from the host itself reporting the release the
 * controller runs, and never from a request having been accepted.
 *
 * Pure, so each state is a table row in a test and not a timing in a browser.
 */
import type { Host } from '../api/types';

export type HostUpdateBlock = NonNullable<Host['update']>;
export type HostUpdateState = HostUpdateBlock['state'];

type HostFields = Pick<
  Host,
  'name' | 'id' | 'version' | 'version_skew' | 'upgrade_version' | 'embedded' | 'update'
>;

export interface SkewFact {
  label: string;
  /** Always neutral: a host on another release is not in a state, it is a fact worth knowing. */
  tone: 'neutral';
  hint: string;
}

/**
 * How a host's release stands to the controller's, or null when they match.
 *
 * Neutral on purpose. The status palette is a fixed mapping that operators have
 * learned, and a host that is behind is not idle, busy, pending, draining or
 * failing; reusing one of those colours would teach it a second meaning.
 */
export function skewFact(skew: Host['version_skew'] | '' | undefined): SkewFact | null {
  switch (skew) {
    case undefined:
    case '':
      return null;
    case 'behind':
      return {
        label: 'Behind',
        tone: 'neutral',
        hint: 'This agent is an earlier release than the controller. It is placing work as normal; upgrade it when convenient.',
      };
    case 'ahead':
      return {
        label: 'Ahead',
        tone: 'neutral',
        hint: 'This agent is a later release than the controller, which is the direction nothing is tested in. Upgrade the controller first.',
      };
    default:
      return {
        label: 'Different build',
        tone: 'neutral',
        hint: 'This agent is a build the controller cannot order against its own. Both are running; check which is which before reporting a bug.',
      };
  }
}

export interface HostUpdateWords {
  /** One word for the badge. */
  label: string;
  /** Never a status colour except for an update that did not work, which is what danger is for. */
  tone: 'neutral' | 'accent' | 'danger';
  /** The sentence shown beside the badge, as plain text. */
  sentence: string;
  /** Whether the Update button is drawn at all: not while an attempt is open, and not once it worked. */
  offered: boolean;
  /** Whether the drawn button can be pressed. A disabled one has the reason beside it as text. */
  canPress: boolean;
  /** The button's visible word. */
  action: 'Update' | 'Try again';
  /** An attempt is open: the card keeps watching and says nothing final. */
  inFlight: boolean;
  /** What the polite region says when the state changes. */
  live: string;
}

/** Release binaries report 1.3.5 where the tag is v1.3.5, so a version is named with its v. */
function tagOf(version: string | undefined): string {
  const bare = (version ?? '').trim().replace(/^v/, '');
  return bare === '' ? '' : `v${bare}`;
}

/**
 * The release the controller would take this host to, as a tag, or empty.
 *
 * `upgrade_version` is the controller's build, set for every host that is
 * behind it, which is every host `can_update` is true for.
 */
export function targetTag(host: Pick<Host, 'upgrade_version'>): string {
  return tagOf(host.upgrade_version);
}

/**
 * The host's update row, or null when there is nothing to say.
 *
 * Nothing to say is a host that matches the controller and has no attempt worth
 * showing: a row on every up-to-date card would bury the ones that differ. The
 * agent inside the controller is updated with the controller, so it has none.
 *
 * `reached` is the host reporting the controller's release, or a later one, while
 * its attempt is still open. The controller closes the attempt on the same
 * heartbeat, but a frame is a moment behind the heartbeat it came from, and the
 * host's own version is what the attempt was waiting for.
 */
export function hostUpdateWords(host: HostFields): HostUpdateWords | null {
  const update = host.update;
  if (!update || host.embedded) return null;
  const name = host.name || host.id || 'this host';
  const reached =
    update.state === 'requested' && (!host.version_skew || host.version_skew === 'ahead');
  // An attempt that worked for an earlier release says nothing about a host the
  // controller has since moved ahead of: it is behind again, and can be asked again.
  const outdated =
    update.state === 'succeeded' &&
    (host.version_skew === 'behind' || host.version_skew === 'differs');
  const state: HostUpdateState = reached ? 'succeeded' : outdated ? 'none' : update.state;
  const done = `${name} runs ${host.version || 'the new release'} now.`;
  switch (state) {
    case 'requested':
      return {
        label: 'Updating',
        tone: 'accent',
        sentence: update.reason,
        offered: false,
        canPress: false,
        action: 'Update',
        inFlight: true,
        live: `${name}: updating. ${update.reason}`,
      };
    case 'succeeded':
      return {
        label: 'Updated',
        tone: 'accent',
        sentence: done,
        offered: false,
        canPress: false,
        action: 'Update',
        inFlight: false,
        live: `${name}: updated. ${done}`,
      };
    case 'failed':
    case 'timed_out':
      return {
        label: state === 'failed' ? 'Failed' : 'Timed out',
        tone: 'danger',
        sentence: update.reason,
        offered: true,
        canPress: update.can_update,
        action: 'Try again',
        inFlight: false,
        live: `${name}: the update ${state === 'failed' ? 'failed' : 'timed out'}. ${update.reason}`,
      };
    case 'cancelled':
      return {
        label: 'Cancelled',
        tone: 'neutral',
        sentence: update.reason,
        offered: true,
        canPress: update.can_update,
        action: 'Try again',
        inFlight: false,
        live: `${name}: the update was cancelled. ${update.reason}`,
      };
    case 'unsupported':
      // The helper can never be installed there, so a button would be a promise
      // nothing can keep. Said only for a host that is not on the release.
      if (host.version_skew !== 'behind' && host.version_skew !== 'differs') return null;
      return {
        label: 'Update by command',
        tone: 'neutral',
        sentence: update.reason,
        offered: false,
        canPress: false,
        action: 'Update',
        inFlight: false,
        live: '',
      };
    default: {
      // No attempt. Said only for a host that is not on the controller's release.
      if (host.version_skew !== 'behind' && host.version_skew !== 'differs') return null;
      return update.can_update
        ? {
            label: 'Can be updated',
            tone: 'neutral',
            sentence: update.reason,
            offered: true,
            canPress: true,
            action: 'Update',
            inFlight: false,
            live: '',
          }
        : {
            label: 'Update by command',
            tone: 'neutral',
            sentence: update.reason,
            offered: true,
            canPress: false,
            action: 'Update',
            inFlight: false,
            live: '',
          };
    }
  }
}

/**
 * What the confirmation says before a host is asked to update.
 *
 * Each line is as true as the code behind it. `zoomies upgrade --mode agent`
 * restarts the agent's unit, whose KillMode is `process`, so a stop reaches the
 * agent and not the runners; the restarted agent adopts what it finds running.
 * The restart does not wait for jobs, and the page does not say it does.
 */
export function confirmHostUpdate(
  name: string,
  running: string,
  tag: string,
): {
  title: string;
  description: string;
  consequences: readonly string[];
  confirmLabel: string;
} {
  const target = tag || 'the controller’s release';
  return {
    title: `Update the agent on ${name}`,
    description: `Update the agent on ${name} from ${running || 'its current release'} to ${target}?`,
    consequences: [
      `The agent on ${name} restarts.`,
      'Jobs that are running keep running: their runners stay in place and the new agent takes them over. The restart does not wait for them to finish.',
      `The host reports back when it runs ${target}. This page says the update is done then, and not before.`,
    ],
    confirmLabel: tag ? `Update to ${tag}` : 'Update',
  };
}
