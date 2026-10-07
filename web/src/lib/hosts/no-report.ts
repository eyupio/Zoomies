/**
 * Why a host has no OS report, and what a person can do about it.
 *
 * Route-only, like report-only.ts: health.ts is shell code, so the sentences only
 * the host page needs live here. Pure on purpose -- the clock is an argument --
 * so every cause is a table row in a test rather than a timing in a browser.
 *
 * The page used to give one answer to five different situations, and half of it
 * was wrong for a container deployment. Each cause here is told apart from fields
 * the page already has; where the page cannot tell, it says so and offers a
 * read-only command rather than guessing.
 */
import type { Host } from '$lib/api/types';

/**
 * How long a new host is given before silence reads as a fault. The first check
 * waits for a heartbeat, and the page cannot see the agent's interval, so this
 * is generous on purpose and never shown to a person as a promise.
 */
export const FIRST_REPORT_GRACE_MS = 5 * 60_000;

/**
 * Read-only on purpose: next-step.ts's DOCTOR_COMMAND offers fixes, and a person
 * who is only trying to learn why there is no report should not be handed one.
 * Never interpolated with anything.
 */
export const READ_ONLY_DOCTOR_COMMAND = 'sudo zoomies doctor';

export type NoReportKind =
  'disconnected' | 'release' | 'joining' | 'embedded' | 'no-release' | 'unknown';

export interface NoReport {
  kind: NoReportKind;
  /** The panel's description: one sentence. */
  description: string;
  detail: string;
  /** The controller's own words about the upgrade, passed through. */
  note: string | null;
  /** host.upgrade_command byte for byte, or the read-only constant, or nothing. */
  command: string | null;
  commandCaption: string | null;
  copyLabel: 'Copy the upgrade command' | 'Copy command' | null;
  /** A sentence under the command block. */
  after: string | null;
  /** Which RelativeTime sentence the markup appends, if any. */
  tail: 'heartbeat' | 'joined' | 'joined-late' | 'since-joined' | null;
}

type HostFields = Pick<
  Host,
  | 'healthy'
  | 'version'
  | 'version_skew'
  | 'upgrade_command'
  | 'upgrade_note'
  | 'embedded'
  | 'created_at'
  | 'last_heartbeat'
>;

const NONE = { note: null, command: null, commandCaption: null, copyLabel: null, after: null };

function joinedRecently(created: string | undefined, now: number): boolean {
  const at = created ? Date.parse(created) : NaN;
  // An unparseable date is not recent: guessing "just joined" would hide a real fault.
  return Number.isFinite(at) && now - at < FIRST_REPORT_GRACE_MS;
}

/**
 * First match wins, and the order matters. Skew precedes the grace period
 * because an old agent never reports however new its row is; the embedded case
 * follows it because created_at is the row's first start only.
 */
export function noReport(i: { host: HostFields; now: number; canOperate: boolean }): NoReport {
  const { host, now, canOperate } = i;

  if (host.healthy === false) {
    return {
      ...NONE,
      kind: 'disconnected',
      description: 'This host is not connected, so no report can reach the controller.',
      detail:
        'Reports travel with the agent’s heartbeat. Check that the agent is running on that machine and can reach this controller; there is nothing to read until it is back.',
      tail: host.last_heartbeat ? 'heartbeat' : null,
    };
  }

  if (host.version_skew === 'behind' || host.version_skew === 'differs') {
    const description =
      host.version_skew === 'behind'
        ? 'This agent is an earlier release than the controller, and may be too old to send OS reports.'
        : 'This agent is a different build from the controller’s, and may be too old to send OS reports.';
    const tail: NoReport['tail'] = joinedRecently(host.created_at, now) ? 'joined-late' : null;
    const base = { kind: 'release' as const, description, tail };
    if (!canOperate) {
      return {
        ...NONE,
        ...base,
        detail:
          'Updating an agent needs the operator role. Ask an operator to update this host’s agent; Zoomies never updates a host itself.',
      };
    }
    if (host.upgrade_command && !host.embedded) {
      return {
        ...base,
        detail:
          'A release from before OS reports were added cannot send one, so updating it is the first thing to try.',
        note: host.upgrade_note || null,
        command: host.upgrade_command,
        commandCaption: 'Run this on the host:',
        copyLabel: 'Copy the upgrade command',
        after:
          'Running runner containers stay in place. This page fills in by itself once the agent restarts and reports. Zoomies never runs it for you.',
      };
    }
    return {
      ...NONE,
      ...base,
      detail: host.upgrade_note || 'Zoomies has no command to offer for this build.',
    };
  }

  if (joinedRecently(host.created_at, now)) {
    return {
      ...NONE,
      kind: 'joining',
      description: 'Waiting for this host’s first report.',
      detail:
        'An agent sends its first OS report shortly after it starts. This page fills in by itself when it arrives.',
      tail: 'joined',
    };
  }

  if (host.embedded) {
    return {
      ...NONE,
      kind: 'embedded',
      description: 'This is the controller’s own agent, and it has not sent a report.',
      detail:
        'It checks itself from the moment the controller starts, so a report should be here by now. It is the same program as the controller, so updating it cannot help. Zoomies cannot tell why from this page.',
      tail: null,
    };
  }

  if (!host.version) {
    return {
      ...NONE,
      kind: 'no-release',
      description:
        'This agent has not said which release it is, so Zoomies cannot tell whether it is new enough to send OS reports.',
      // No command, deliberately: an unversioned upgrade could move an agent past its controller.
      detail: `An agent from before OS reports cannot send one. Update it the way it was installed; Zoomies has no command to offer because it does not know which release to give it.${canOperate ? '' : ' Ask an operator to update it.'}`,
      tail: null,
    };
  }

  return {
    kind: 'unknown',
    description:
      'This agent reports the same release as the controller, or a later one, and has still not sent a report.',
    detail:
      'Zoomies cannot tell why from this page, and two builds can share a release name, so an older agent is still possible. On the host, run the command below to see whether its checks run at all.',
    note: null,
    command: READ_ONLY_DOCTOR_COMMAND,
    commandCaption: 'Run this on the host:',
    copyLabel: 'Copy command',
    after: 'It makes no change. Zoomies never runs it for you.',
    tail: 'since-joined',
  };
}

export function noReportSubtitle(): string {
  return 'No OS report has arrived from this host yet.';
}
