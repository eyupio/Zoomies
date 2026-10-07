/**
 * What the host page says a person should do about the host in front of them,
 * and whether it is safe to reboot it.
 *
 * Pure, so the words and the order they are chosen in can be tested without a
 * browser. Route-only: the app shell must not import this (the shell has a byte
 * budget, and only the host page needs it).
 *
 * Two rules shape every sentence. Zoomies never runs a command on a host and
 * never reboots one, so the page offers the one thing the controller can do --
 * cordon, which is a scheduling decision and touches nothing on the machine --
 * and a command for a person to run themselves. And a runner count is not a job
 * count: a runner kept warm for a pool waits for work and never finishes by
 * itself, so "none" can be said but "they will finish" cannot.
 *
 * The word for holding work back is cordon, never drain. Drain cordons the host
 * and then stops a runner that is still busy after five minutes, which is the
 * wrong advice for waiting for jobs to finish; the controller's own fix text for
 * a pending reboot (host_health_problems.go) makes the same choice.
 */
import type { Host } from '$lib/api/types';
import { pluralise } from '../format';
import { attention, type DoctorReport } from './health';
import { reportOnly } from './report-only';

/**
 * The one command the panel offers. Never built from the host's name or
 * address: those are written by the agent, so a name with a newline in it could
 * otherwise put a second command on somebody's clipboard.
 */
export const DOCTOR_COMMAND = 'sudo zoomies doctor --interactive';

type Subject = Pick<Host, 'id' | 'name' | 'address' | 'embedded' | 'healthy' | 'active_runners'>;

/**
 * What can be said about rebooting. `cordoned` is the verdict for a host that
 * is cordoned with nothing waiting on it: it gives no reboot advice at all, only
 * the way back to taking work.
 */
export type Verdict = 'unknown' | 'busy' | 'idle' | 'safe' | 'cordoned';

export interface NextStepInput {
  host: Subject;
  report: DoctorReport | undefined;
  /**
   * The cordon state the controller has CONFIRMED. The page passes the value
   * from before the click while a request is in flight: the cache already
   * shows the optimistic one, and "safe to reboot" must not be said on the
   * strength of a request nobody has heard back from.
   */
  cordoned: boolean;
}

export interface NextStep {
  verdict: Verdict;
  /** The sentence a screen reader hears. It carries no count, so it changes only when the state does. */
  headline: string;
  detail: string;
  /** 'Not reported', 'None', '1 runner', '3 runners'. */
  runners: string;
  /** 'Taking new work' or 'Cordoned: no new runner is placed here'. */
  placement: string;
  cordoned: boolean;
  button: { label: 'Cordon this host' | 'Uncordon this host'; help: string };
  /** The caption above the command; null when there is nothing to run. */
  where: string | null;
  embeddedNote: string | null;
}

/**
 * A reboot the pill and the controller both call one. A container's report is
 * the container's view, so its flag is no verdict (health.ts, and the
 * controller's hostHealthReport returns nothing for it).
 */
export function rebootPending(report: DoctorReport | undefined): boolean {
  return report !== undefined && !report.container && report.reboot_pending === true;
}

/**
 * The panel for a host, or null when there is nothing to say.
 *
 * It is wanted for two reasons: the host has a counted finding or is waiting
 * for a reboot (a full report only), or it is cordoned. The second is what keeps
 * an Uncordon button on the page of a host that has been rebooted and is clean:
 * a cordon is easy to forget, and the host would sit there taking no work.
 */
export function nextStep(input: NextStepInput): NextStep | null {
  const { host, report, cordoned } = input;
  const reboot = rebootPending(report);
  // On a report-only distribution the `environment` warning is the report
  // saying so, and nothing an operator can fix, so it alone is not a reason to
  // put a panel on the page. It stays in Needs attention: the controller counts it.
  const kind = reportOnly(report);
  const waiting =
    reboot ||
    (report !== undefined &&
      attention(report).some((r) => !(kind === 'distro' && r.id === 'environment')));
  if (!waiting && !cordoned) return null;

  const name = host.name || host.id || 'this host';
  const count = host.active_runners;
  const base = {
    runners:
      count === undefined ? 'Not reported' : count === 0 ? 'None' : pluralise(count, 'runner'),
    placement: cordoned ? 'Cordoned: no new runner is placed here' : 'Taking new work',
    cordoned,
    button: cordoned
      ? {
          label: 'Uncordon this host' as const,
          help: `Uncordoning lets the scheduler place runners on ${name} again.`,
        }
      : {
          label: 'Cordon this host' as const,
          help: `Cordoning stops new runners being placed on ${name}. It does not stop or reboot anything: runners already there carry on.`,
        },
    embeddedNote: host.embedded
      ? `${name} runs inside the controller, so rebooting it takes Zoomies offline until it is back.`
      : null,
  };

  // Nothing to run and nothing to reboot, so no advice about either: this is
  // the host that was cordoned for a reboot, came back clean, and is still
  // cordoned. It is judged before the others because it is true whatever the
  // report is -- none, a container's, or one from a host that has gone quiet.
  if (!waiting) {
    return {
      ...base,
      verdict: 'cordoned',
      headline: `${name} is cordoned and nothing is waiting on it`,
      detail: 'Uncordon it so that it takes work again.',
      where: null,
    };
  }

  // `zoomies doctor --interactive` offers fixes that do not exist for a
  // distribution or container that cannot be tuned, so no command is offered;
  // the cordon and the reboot advice are about scheduling and stay.
  const where =
    kind === 'distro' || kind === 'container'
      ? null
      : host.embedded
        ? `Run this on the controller's own machine (${name}), in a shell there:`
        : `Run this on ${name}${host.address ? ` (${host.address})` : ''}, in a shell on that machine:`;
  const rebootLine = 'Zoomies never reboots a host itself.';

  // A host nobody can hear has a runner count that is the last one recorded,
  // and that is the least safe number to reboot on.
  if (host.healthy !== true || count === undefined) {
    return {
      ...base,
      verdict: 'unknown',
      headline: 'Check before rebooting',
      detail:
        host.healthy !== true
          ? `${name} is not connected, so the runner count and cordon state below are the last the controller recorded. Look at its runners before rebooting it.`
          : `The controller has not said how many runners are on ${name}. Look at its runners before rebooting it.`,
      where,
    };
  }
  if (count > 0) {
    const place = cordoned
      ? 'It is cordoned, so no new runner is placed here.'
      : 'New runners can still be placed here; cordon it to stop them.';
    const warm =
      'A runner kept warm for a pool waits for work and never finishes by itself, so this count cannot say whether any of them is running a job.';
    const tail = reboot ? ` Reboot ${name} when none is, then uncordon it. ${rebootLine}` : '';
    return {
      ...base,
      verdict: 'busy',
      headline: `Runners are still on ${name}`,
      detail: `${place} ${warm}${tail}`,
      where,
    };
  }
  if (cordoned) {
    return {
      ...base,
      verdict: 'safe',
      headline: 'Idle and cordoned: safe to reboot now',
      detail: reboot
        ? `Reboot ${name} yourself, then uncordon it so it takes work again. ${rebootLine}`
        : `Nothing is running on ${name} and nothing new will be placed there. If you do reboot it, uncordon it afterwards so it takes work again. ${rebootLine}`,
      where,
    };
  }
  return {
    ...base,
    verdict: 'idle',
    headline: `Nothing is running on ${name} right now`,
    detail: reboot
      ? `It can be rebooted now. Cordon it first so that a job cannot land on it in the meantime, and uncordon it afterwards. ${rebootLine}`
      : 'Cordon it first if you want to make changes without a job landing on it.',
    where,
  };
}

/**
 * What a viewer, who cannot cordon, is told in the host report instead of the
 * panel. It names the role they lack, so that a refusal has somewhere to go.
 */
export function rebootAdvice(): string {
  return 'A reboot is pending. Cordon the host so that no job lands on it, reboot it yourself once none of its runners is running a job, then uncordon it. Cordoning needs the operator role. Zoomies never reboots a host itself.';
}
