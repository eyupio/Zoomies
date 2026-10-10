/**
 * What the transfer panel shows of a preparation: the four things the fence
 * waits on as a checklist, and each thing still in the way with the tone it
 * deserves.
 *
 * The counts come from the controller and the sentences are its own; this
 * decides only how they are laid out, so that "8 awaiting cleanup" is a step
 * with eight named rows under it rather than a number in a paragraph.
 */
import type { Schemas } from '$lib/api/types';
import type { StatusTone } from '$lib/status';

export type TransferProgress = Schemas['TransferProgress'];
export type TransferWait = Schemas['TransferWait'];

export interface TransferStep {
  /** Matches `TransferWait.kind`, so the rows under a step are found by it. */
  kind: TransferWait['kind'];
  label: string;
  count: number;
  /** A step is done when nothing of its kind is left. */
  done: boolean;
  tone: StatusTone;
  /** Said under the label while the step is not done. */
  note: string;
}

/**
 * The checklist. A step is `pending` while its count is above zero, `danger`
 * when something under it needs a person (a host that has stopped talking,
 * or a cleanup that failed), and `idle` once it is done: the tone operators
 * already read as "nothing to do here".
 */
export function transferSteps(p: TransferProgress): TransferStep[] {
  const waits = p.waiting ?? [];
  const silent = (kind: TransferWait['kind']) =>
    waits.some((w) => w.kind === kind && w.host && !w.host_healthy);
  const step = (
    kind: TransferWait['kind'],
    label: string,
    count: number,
    note: string,
    needsSomebody: boolean,
  ): TransferStep => ({
    kind,
    label,
    count,
    done: count === 0,
    tone: count === 0 ? 'idle' : needsSomebody ? 'danger' : 'pending',
    note: count === 0 ? '' : note,
  });
  const idle = Math.max(0, p.live_runners - p.busy_runners);
  return [
    step(
      'job',
      'Jobs on this fleet finish',
      p.active_jobs,
      'Running jobs are left to finish; nothing new is started.',
      silent('job'),
    ),
    step(
      'runner',
      'Runners are withdrawn',
      p.live_runners,
      idle > 0 && p.busy_runners > 0
        ? `${p.busy_runners} busy, ${idle} idle being withdrawn.`
        : p.busy_runners > 0
          ? 'Busy runners stop once their job is done.'
          : 'Idle runners are withdrawn from GitHub and stopped.',
      silent('runner'),
    ),
    step(
      'cleanup',
      'Removed runners are confirmed gone',
      p.pending_cleanup,
      cleanupNote(p),
      silent('cleanup') || p.cleanup_failed > 0,
    ),
    step(
      'machine',
      'Machine operations finish',
      p.machine_operations,
      'A clone or a delete under way at a provider is left to finish.',
      false,
    ),
  ];
}

function cleanupNote(p: TransferProgress): string {
  const sides: string[] = [];
  if (p.cleanup_awaiting_host > 0) sides.push(`${p.cleanup_awaiting_host} by the host`);
  if (p.cleanup_awaiting_github > 0) sides.push(`${p.cleanup_awaiting_github} by GitHub`);
  const failed = p.cleanup_failed > 0 ? `; ${p.cleanup_failed} failed` : '';
  return sides.length > 0
    ? `Waiting to be confirmed gone: ${sides.join(', ')}${failed}.`
    : 'Waiting to be confirmed gone.';
}

/** The tone of one row: danger where a person is needed, pending otherwise. */
export function waitTone(w: TransferWait): StatusTone {
  if (w.error) return 'danger';
  if (w.host && !w.host_healthy) return 'danger';
  if (w.kind === 'runner' && w.state === 'busy') return 'busy';
  return 'pending';
}

/** The short word on a row's pill: its state, or what it is waiting on. */
export function waitLabel(w: TransferWait): string {
  if (w.error) return 'Failed';
  if (w.host && !w.host_healthy) return 'Host silent';
  switch (w.kind) {
    case 'cleanup':
      return !w.host_confirmed ? 'Host' : 'GitHub';
    case 'job':
      return 'Running';
    default:
      return w.state ? w.state.charAt(0).toUpperCase() + w.state.slice(1) : 'Waiting';
  }
}
