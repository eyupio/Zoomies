/**
 * "Check now" on the host page: what the button and the line beside it say.
 *
 * Route-only, like no-report.ts: the shell must not import this. Pure on purpose
 * -- the clock is an argument -- so every row of the state table is a line in a
 * test rather than a timing in a browser.
 *
 * Every sentence says the agent runs the checks. Nothing here may suggest that
 * the controller changes anything on a host or dials one: the request goes out on
 * the agent's own poll, and the checks only read. The gate is `host.features`,
 * which the agent sets from what it can really do, and never the report's
 * `container` flag: the host-health service of a Compose install reports
 * `container: false` and still cannot answer.
 */
import type { Host } from '$lib/api/types';

/** How long an ask goes unanswered before the line admits it is taking a while. */
export const WAITING_AFTER_MS = 5_000;
/**
 * The page's own patience for an ask it made, not a promised latency. The
 * controller gives up at 60 s; this is longer, for the one case it cannot speak
 * for: a controller restarted since the ask, which forgot it.
 */
export const CEILING_MS = 75_000;
/** How long the controller keeps an answer on the view; a ceiling failure is kept as long. */
export const KEEP_MS = 30_000;

/** The flag an agent advertises when it can run its checks on request. */
export const FEATURE = 'host-check';

export const ROLE_SENTENCE = 'Asking a host to check itself needs the operator role.';

export type CheckNowKind =
  'idle' | 'role' | 'unavailable' | 'asking' | 'waiting' | 'done' | 'cooling' | 'failed';

type HostFields = Pick<
  Host,
  'name' | 'features' | 'healthy' | 'incompatible' | 'incompatible_reason' | 'health_check'
>;

export interface CheckNowInput {
  host: HostFields;
  canOperate: boolean;
  now: number;
  /**
   * When this page pressed the button, kept after the request returns until the
   * controller shows an answer. It is what lets the page time out an ask that a
   * restarted controller forgot, which leaves nothing on the view to say so.
   */
  localAskedAt?: number | null;
  /** The request is in flight. */
  posting?: boolean;
  /** How many changes the report this check brought produced, from the page's own diff. */
  changes?: number;
  /** A press landed inside the cooldown: say how long is left instead of asking. */
  restate?: boolean;
}

export interface CheckNow {
  show: boolean;
  disabled: boolean;
  /** Why the button is off, as a sentence the page prints beside it. */
  reason?: string;
  loading: boolean;
  /** A press now would be refused for being too soon, so the page restates instead. */
  cooling: boolean;
  /** The status line. Plain text, set outside any live region. */
  status?: string;
  statusKind: CheckNowKind;
  /** What a screen reader is told, once, when the state arrives. Never the countdown. */
  announce?: string;
  /** The button's tooltip. */
  title: string;
  /** Identifies one failure, so the page toasts it once. */
  failureKey?: string;
}

export function idleTitle(name: string): string {
  return `Ask the agent on ${name} to run its read-only OS checks once and send the report. Nothing on the host is changed.`;
}

/** Why this host cannot be asked, or null when it can. Order follows the controller's refusals. */
export function unavailableReason(host: HostFields): string | null {
  const name = host.name || 'This host';
  if (host.healthy === false)
    return `${name} is not connected, so it cannot be asked. Check that its agent is running.`;
  if (host.incompatible === true)
    return (
      host.incompatible_reason ||
      `The agent on ${name} speaks a different protocol from this controller, so it cannot be asked. Update the agent.`
    );
  if (!host.features?.includes(FEATURE))
    return `The agent on ${name} cannot check on request. Agents running in a container, and agents older than this release, send their report on their own schedule instead. The last report is still shown.`;
  return null;
}

function seconds(until: number, now: number): number {
  return Math.max(1, Math.ceil((until - now) / 1000));
}

function failedMessage(name: string): string {
  return `${name} did not answer in time. Check that its agent is running; the last report is still shown.`;
}

function doneLine(name: string, outcome: string | undefined, changes: number): string {
  switch (outcome) {
    case 'first':
      return `Checked just now. This is the first report from ${name}.`;
    case 'changed':
      return changes > 0
        ? `Checked just now. ${changes} ${changes === 1 ? 'change' : 'changes'} since the last report, listed below.`
        : 'Checked just now. The report changed, but nothing that counts towards this host’s health.';
    case 'unchanged':
      return 'Checked just now. Nothing has changed since the last report.';
    case 'not_newer':
      return `${name} answered, but its clock is behind the last report, so the time did not move. Check its time settings.`;
    default:
      return 'Checked just now.';
  }
}

/**
 * Whether the host a POST answered with is older than the one the page already
 * holds. An agent that answers in a few milliseconds sends its frame before the
 * 202 arrives, and writing the 202 over it would put the old report and a
 * check that is "still asked" back on the page, for good: no later frame is
 * coming. Only the check can tell, so it is compared by when it was asked.
 */
export function superseded(current: Host['health_check'], answer: Host['health_check']): boolean {
  if (!current || !answer) return false;
  const have = Date.parse(current.asked_at);
  const got = Date.parse(answer.asked_at);
  if (!Number.isFinite(have) || !Number.isFinite(got)) return false;
  return have > got || (have === got && current.state !== 'asked' && answer.state === 'asked');
}

export function checkNow(i: CheckNowInput): CheckNow {
  const { host, now } = i;
  const name = host.name || 'This host';
  const title = idleTitle(name);
  const base: CheckNow = {
    show: true,
    disabled: false,
    loading: false,
    cooling: false,
    statusKind: 'idle',
    title,
  };
  if (!i.canOperate) return { ...base, show: false, status: ROLE_SENTENCE, statusKind: 'role' };

  const reason = unavailableReason(host);
  const off = reason === null ? {} : { disabled: true, reason };
  const hc = host.health_check;
  const local = i.localAskedAt ?? null;

  if (i.posting) {
    const line = `Asked ${name} to check itself…`;
    return { ...base, ...off, loading: true, status: line, statusKind: 'asking', announce: line };
  }

  if (hc?.state === 'asked') {
    const since = local ?? Date.parse(hc.asked_at);
    const elapsed = Number.isFinite(since) ? now - since : 0;
    if (elapsed >= CEILING_MS) {
      const message = failedMessage(name);
      return {
        ...base,
        ...off,
        status: message,
        statusKind: 'failed',
        failureKey: `asked:${hc.asked_at}`,
      };
    }
    if (elapsed >= WAITING_AFTER_MS)
      return {
        ...base,
        ...off,
        loading: true,
        status: `Still waiting for ${name} to answer. A busy host can take a little longer.`,
        statusKind: 'waiting',
      };
    const line = `Asked ${name} to check itself…`;
    return { ...base, ...off, loading: true, status: line, statusKind: 'asking', announce: line };
  }

  if (hc?.state === 'done' || hc?.state === 'failed') {
    const next = hc.next_at ? Date.parse(hc.next_at) : NaN;
    const cooling = Number.isFinite(next) && now < next;
    const failed = hc.state === 'failed';
    if (cooling && i.restate) {
      const left = seconds(next, now);
      const status = failed
        ? `${name} was asked a moment ago. You can ask again in ${left} s.`
        : `Checked just now. You can check again in ${left} s.`;
      return { ...base, ...off, cooling, status, statusKind: 'cooling' };
    }
    if (failed)
      return {
        ...base,
        ...off,
        cooling,
        status: hc.message || failedMessage(name),
        statusKind: 'failed',
        failureKey: `failed:${hc.asked_at}`,
      };
    const line = doneLine(name, hc.outcome, i.changes ?? 0);
    return {
      ...base,
      ...off,
      cooling,
      status: line,
      statusKind: 'done',
      // The count can land a beat after the frame, so the announcement leaves it out.
      announce: hc.outcome === 'changed' ? 'Checked just now. The report has changed.' : line,
    };
  }

  // Nothing on the view. A page that asked, and whose controller has since
  // forgotten the ask, would otherwise wait for ever.
  if (local !== null) {
    const elapsed = now - local;
    if (elapsed >= CEILING_MS) {
      if (elapsed >= CEILING_MS + KEEP_MS) return { ...base, ...off };
      return {
        ...base,
        ...off,
        status: failedMessage(name),
        statusKind: 'failed',
        failureKey: `local:${local}`,
      };
    }
    if (elapsed >= WAITING_AFTER_MS)
      return {
        ...base,
        ...off,
        loading: true,
        status: `Still waiting for ${name} to answer. A busy host can take a little longer.`,
        statusKind: 'waiting',
      };
    const line = `Asked ${name} to check itself…`;
    return { ...base, ...off, loading: true, status: line, statusKind: 'asking', announce: line };
  }

  return reason === null ? base : { ...base, ...off, statusKind: 'unavailable' };
}
