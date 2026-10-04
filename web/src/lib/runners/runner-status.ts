import type { Runner } from '../api/types';
import { cpuResourceStatus, runnerStatus, toneTokens, type StatusTone } from '../status';

type StatusRunner = Pick<Runner, 'state' | 'cpu_resource'>;

interface Activity {
  label: string;
  title: string;
  detail: string;
  tone: StatusTone;
}

const activityQuirky: Record<string, Activity> = {
  maximum_zoomies: {
    label: 'Squirrel spotted',
    title: 'Maximum zoomies',
    detail: 'A major CPU boost is active. This runner is borrowing spare host capacity.',
    tone: 'busy',
  },
  zoomies: {
    label: 'Rabbit spotted',
    title: 'Extra zoomies',
    detail: 'Extra CPU is available to this runner while its host has room to spare.',
    tone: 'busy',
  },
  throttled: {
    label: 'Leash tightened',
    title: 'Host pressure protection',
    detail: 'CPU has been reduced to protect the host. It can recover as pressure eases.',
    tone: 'draining',
  },
};

/**
 * The same three states, said plainly, for when the kennel vocabulary is off.
 *
 * The two boosts each get a word of their own. They both said "Boost active"
 * once, and an operator scanning the grid could not tell an extra boost from
 * a maximum one without learning which icon was which.
 */
const activityStandard: Record<string, Activity> = {
  maximum_zoomies: {
    label: 'Zoomies',
    title: 'Maximum boost',
    detail: 'A major CPU boost is active. This runner is borrowing spare host capacity.',
    tone: 'busy',
  },
  zoomies: {
    label: 'Extra',
    title: 'Extra boost',
    detail: 'Extra CPU is available to this runner while its host has room to spare.',
    tone: 'busy',
  },
  throttled: {
    label: 'Throttled',
    title: 'Host pressure protection',
    detail: 'CPU has been reduced to protect the host. It can recover as pressure eases.',
    tone: 'draining',
  },
};

const quietCPUQuirky: Record<string, string> = {
  observing: 'Nose to the wind: spare CPU is being observed, without changing this allocation.',
  guaranteed: 'Steady paws: this runner has its guaranteed CPU allocation.',
  sit_and_stay: 'Sit and stay: elastic CPU is off for this pool.',
};

const quietCPUStandard: Record<string, string> = {
  observing: 'Spare CPU is being observed, without changing this allocation.',
  guaranteed: 'This runner has its guaranteed CPU allocation.',
  sit_and_stay: 'Elastic CPU is off for this pool.',
};

/**
 * Lifecycle stays authoritative. Only an active allocation replaces Busy/Idle.
 *
 * `quirky` is passed in by the caller rather than read here -- see the note
 * on `runnerStatus` in `../status` for why this module stays free of it too.
 */
export function runnerDisplayStatus(runner: StatusRunner, quirky = true) {
  const lifecycle = runnerStatus(runner.state, quirky);
  const canBoost = runner.state === 'busy' || runner.state === 'idle';
  const resource = runner.cpu_resource;
  const activity = quirky ? activityQuirky : activityStandard;
  const active = canBoost ? activity[resource?.state ?? ''] : undefined;
  const key = active ? resource!.state! : lifecycle.key;
  const detail = active?.detail ?? lifecycle.hint ?? 'No runner state has been reported.';
  const quietCPU = quirky ? quietCPUQuirky : quietCPUStandard;
  const cpuDetail = !active && canBoost ? quietCPU[resource?.state ?? ''] : undefined;
  const icon = active ? cpuResourceStatus(resource!.state, undefined, quirky).icon : lifecycle.icon;
  return {
    key,
    label: active?.label ?? lifecycle.label,
    title: active?.title ?? lifecycle.label,
    detail,
    cpuDetail,
    lifecycle,
    active: Boolean(active),
    canBoost,
    icon,
    ...toneTokens(active?.tone ?? lifecycle.tone),
  };
}
