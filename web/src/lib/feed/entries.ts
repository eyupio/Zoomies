/**
 * One line of the Overview's feed, whatever it is about.
 *
 * Every entry is the same four things -- a mark in one of the six status
 * tones, what happened, what it happened to, and when -- so that a feed
 * carrying nine kinds of news still reads as one list rather than nine
 * stacked widgets. The detail line underneath is the controller's own
 * sentence where there is one: the scheduler's reason, the throttle's
 * explanation, the hypervisor's complaint. Paraphrasing those is how a
 * dashboard stops being trustworthy.
 *
 * The tones are the fixed status mapping every other page uses, and they are
 * used for the same meanings: danger for something that went wrong, draining
 * for something going away, pending for something under way or under
 * pressure, idle for something that came good, neutral for a fact.
 */
import {
  Boxes,
  Bot,
  CircleMinus,
  HeartPulse,
  Plug,
  Minus,
  Plus,
  RotateCw,
  ScrollText,
  ServerCog,
  TrendingDown,
  TrendingUp,
  TriangleAlert,
} from '@lucide/svelte';
import type { LucideIcon } from '@lucide/svelte';
import type {
  AuditEvent,
  Host,
  Installation,
  Job,
  Machine,
  MachineState,
  Pool,
  Problem,
  Provider,
  Runner,
  ScalingEvent,
  WebhookDelivery,
} from '../api/types';
import { faultLabel } from '../faults';
import { formatDuration, pluralise, shortId } from '../format';
import { findingsLabel } from '../hosts/health';
import {
  cpuResourceStatus,
  deliveryStatus,
  hostStatus,
  jobStatus,
  machineStatus,
  managedJob,
  runnerStatus,
  severityStatus,
  ELSEWHERE,
  RUNNER_LOST,
  type StatusTone,
} from '../status';
import type { FeedCategoryID } from './categories';
import { automaticTitle, causeOf, subjectOf } from './cause';
import {
  jobFailure,
  type HostChange,
  type InstallationChange,
  type JobNews,
  type ProviderChange,
  type RunnerMilestone,
} from './changes';

export interface FeedEntry {
  /**
   * Stable for the fact rather than for the frame, so the same thing arriving
   * twice -- a fetch and the replay of the frame it overlapped with -- is one
   * line. A repeated key in a keyed list is a framework error, not a cosmetic
   * one.
   */
  id: string;
  category: FeedCategoryID;
  /** When it happened, as the payload said, or when it arrived. */
  at: string;
  tone: StatusTone;
  icon: LucideIcon;
  /** What happened, in the fewest words that are still true. */
  title: string;
  /** The controller's own sentence about it, where there is one. */
  detail?: string;
  /** What it happened to, and the page that has the whole of it. */
  target?: { label: string; href?: string };
  /**
   * True for a job no runner of this fleet ran. The panel keeps these and
   * shows them only when the Overview is asked for every runner GitHub
   * reports on, which is the same switch its other job panels read.
   */
  elsewhere?: boolean;
}

/** Now, in the shape every timestamp in the API has. */
export function now(): string {
  return new Date().toISOString();
}

/* -- scaling --------------------------------------------------------------- */

/**
 * A scheduler decision.
 *
 * `index` is only there for a controller old enough to have written a
 * decision without an id; two decisions in the same second on the same pool
 * would otherwise share a key.
 */
export function scalingEntry(event: ScalingEvent, index: number): FeedEntry {
  const from = event.from;
  const to = event.to;
  const direction =
    from === undefined || to === undefined || to === from ? 'hold' : to > from ? 'up' : 'down';
  const counts = from !== undefined && to !== undefined ? ` ${from} → ${to}` : '';
  return {
    id: `scaling:${event.id ?? `${event.pool_id ?? ''}:${event.created_at ?? ''}:${index}`}`,
    category: 'scaling',
    at: event.created_at ?? '',
    tone: direction === 'up' ? 'pending' : direction === 'down' ? 'draining' : 'neutral',
    icon: direction === 'up' ? TrendingUp : direction === 'down' ? TrendingDown : Minus,
    title:
      direction === 'up'
        ? `Scaled up${counts}`
        : direction === 'down'
          ? `Scaled down${counts}`
          : 'Held',
    detail: event.reason || undefined,
    target: {
      label: event.pool_name ?? 'Unnamed pool',
      href: event.pool_id ? `/pools/${event.pool_id}` : undefined,
    },
  };
}

/* -- runners --------------------------------------------------------------- */

/**
 * A runner that failed. The fix is the detail rather than the message where
 * there is one: a runner that died before ever taking a job is the case this
 * exists for, and nothing else in the fleet says why a pool's containers will
 * not start.
 */
export function runnerFailureEntry(runner: Runner): FeedEntry | null {
  if (runner.state !== 'failed' || !runner.id) return null;
  const status = runnerStatus('failed');
  return {
    id: `runner:${runner.id}:failed`,
    category: 'runners',
    at: runner.finished_at ?? runner.created_at ?? '',
    tone: status.tone,
    icon: status.icon,
    title: 'A runner failed',
    detail: runner.fault_fix || runner.message || undefined,
    target: { label: runner.name ?? shortId(runner.id), href: `/runners/${runner.id}` },
  };
}

/**
 * A runner reaching the two moments of an ordinary life: ready for work, and
 * gone when the work was done.
 *
 * This is the half of the fleet that goes right, and it is here because a
 * panel that only ever reports failures teaches an operator that silence is
 * the good state -- which is the same thing as teaching them not to read it.
 */
export function runnerMilestoneEntry(
  runner: Runner,
  milestone: RunnerMilestone,
  at: string,
): FeedEntry | null {
  if (!runner.id) return null;
  const ready = milestone === 'ready';
  const status = runnerStatus(ready ? 'idle' : 'removed');
  const handled = runner.jobs_handled ?? 0;
  return {
    id: `runner:${runner.id}:${milestone}`,
    category: 'runner-lifecycle',
    at: (ready ? runner.registered_at : runner.finished_at) ?? at,
    tone: ready ? 'idle' : 'neutral',
    icon: status.icon,
    title: ready ? 'A runner is ready for work' : 'A runner finished and was removed',
    detail: ready
      ? [runner.pool_name, runner.host_name].filter(Boolean).join(' · ') || undefined
      : // What it was for, which is the only thing that distinguishes an
        // ephemeral runner's end from a scale-down taking an idle one away.
        [
          handled > 0 ? pluralise(handled, 'job') : 'no jobs',
          runner.pool_name ?? '',
          runner.host_name ?? '',
        ]
          .filter(Boolean)
          .join(' · '),
    target: { label: runner.name ?? shortId(runner.id), href: `/runners/${runner.id}` },
  };
}

/* -- elastic CPU ------------------------------------------------------------ */

/**
 * A runner lent spare CPU, or having it taken back.
 *
 * The title is the controller's own label -- "Squirrel spotted — maximum
 * zoomies" -- because that vocabulary is the product's, it is what the
 * runner's own page says, and a feed that translated it into "CPU allocation
 * factor 1.8" would be describing a different system from the one beside it
 * -- unless the operator has turned that vocabulary off, when it says the
 * same thing plainly instead.
 */
export function cpuEntry(
  runner: Runner,
  state: string,
  at: string,
  quirky = true,
): FeedEntry | null {
  if (!runner.id) return null;
  const cpu = runner.cpu_resource;
  const status = cpuResourceStatus(state, cpu?.label, quirky);
  const current = cpu?.current_cpus;
  const guaranteed = cpu?.guaranteed_cpus;
  return {
    id: `cpu:${runner.id}:${state}:${at}`,
    category: 'cpu',
    at,
    tone: status.tone,
    icon: status.icon,
    title: status.label,
    detail:
      current !== undefined && guaranteed !== undefined
        ? `${formatCPUs(current)} CPUs now against a guarantee of ${formatCPUs(guaranteed)}` +
          (runner.host_name ? ` · ${runner.host_name}` : '')
        : status.hint,
    target: { label: runner.name ?? shortId(runner.id), href: `/runners/${runner.id}` },
  };
}

/** CPUs as the runner's own page prints them: whole where they are whole. */
function formatCPUs(value: number): string {
  return Number.isInteger(value) ? String(value) : value.toFixed(1);
}

/* -- jobs ------------------------------------------------------------------ */

/**
 * One finished job, in the shape the recent-outcomes panel used to give it:
 * what it was, how it ended, the step it stopped at or the fault behind it,
 * the repository and branch, and how long it took. That panel is gone, and
 * this is where its rows went -- a job ending is news like anything else, and
 * two reverse-chronological lists on one page, each with its own idea of what
 * belongs on it, is one list too many.
 *
 * The failures land in their own category, so an operator on a busy fleet can
 * switch off the hundred jobs that succeeded without switching off the two
 * that did not.
 */
export function jobEntry(job: Job, news: JobNews, at = now()): FeedEntry | null {
  if (!job.id) return null;
  const failure = jobFailure(news);
  const status = news === 'runner_lost' ? RUNNER_LOST : jobStatus('completed', job.conclusion);
  return {
    id: `job:${job.id}`,
    category: failure ? 'jobs' : 'outcomes',
    at: job.completed_at ?? job.started_at ?? at,
    tone: failure ? 'danger' : status.tone,
    icon: status.icon,
    title: jobTitle(job, news),
    detail: jobDetail(job, news),
    // Kept rather than dropped, and hidden unless the page is asked for
    // everything: GitHub reports every job in the repositories an installation
    // covers, and on an organisation that also uses hosted runners most of
    // them are somebody else's.
    elsewhere: !managedJob(job),
    target: {
      label: `${job.workflow ?? 'Unknown workflow'} / ${job.job_name ?? 'unnamed job'}`,
      href: `/jobs?q=${encodeURIComponent(job.job_name ?? '')}&repo=${encodeURIComponent(job.repo ?? '')}`,
    },
  };
}

function jobTitle(job: Job, news: JobNews): string {
  if (news === 'runner_lost') return 'A job’s runner stopped under it';
  if (news === 'failed') return 'A job failed';
  switch ((job.conclusion ?? '').toLowerCase()) {
    case 'success':
      return 'A job succeeded';
    case 'cancelled':
      return 'A job was cancelled';
    case 'skipped':
      return 'A job was skipped';
    default:
      return 'A job finished';
  }
}

/**
 * The one line under it, in the order the outcomes panel read: why it ended
 * that way, where it came from, and how long it took.
 *
 * The fault's category rather than "the runner stopped under it" for the
 * fleet's own failures, because the sentence above already says that and the
 * word that differs between two lines is the one worth the space.
 */
function jobDetail(job: Job, news: JobNews): string | undefined {
  const why =
    news === 'runner_lost'
      ? faultLabel(job.fault_kind).toLowerCase() || 'the runner stopped under it'
      : news === 'failed' && job.failed_step
        ? `at ${job.failed_step.name ?? `step ${job.failed_step.number ?? '?'}`}`
        : '';
  const parts = [
    !managedJob(job) ? ELSEWHERE.label.toLowerCase() : '',
    why,
    job.repo ?? '',
    job.head_branch ?? '',
    formatDuration(job.duration_ms),
  ].filter((part) => part && part !== '--');
  return parts.length > 0 ? parts.join(' · ') : undefined;
}

/* -- hosts ----------------------------------------------------------------- */

const HOST_NEWS: Record<HostChange, { title: string; tone: StatusTone }> = {
  joined: { title: 'A host joined the fleet', tone: 'idle' },
  unreachable: { title: 'A host stopped answering', tone: 'danger' },
  recovered: { title: 'A host is answering again', tone: 'idle' },
  cordoned: { title: 'A host was cordoned', tone: 'draining' },
  uncordoned: { title: 'A host was uncordoned', tone: 'idle' },
  throttled: { title: 'A host was throttled', tone: 'pending' },
  calm: { title: 'A host’s throttle lifted', tone: 'idle' },
  holding: { title: 'A host is holding new runners', tone: 'pending' },
  admitting: { title: 'A host is taking runners again', tone: 'idle' },
  incompatible: { title: 'A host’s agent is too old for Zoomies', tone: 'danger' },
  attention: { title: 'A host needs attention', tone: 'pending' },
  failing: { title: 'A host has a health error', tone: 'danger' },
  clear: { title: 'A host no longer needs attention', tone: 'idle' },
  reboot: { title: 'A host is waiting for a reboot', tone: 'pending' },
};

/**
 * The four lines about a host's OS get a mark of their own. The host's status
 * icon is the host's -- for a healthy one, a plain circle -- and a line about
 * its settings drawn with it would look like a line about its heartbeat. Clear
 * has the same mark as attention on purpose: it is the same subject coming
 * good, and the tone says which way.
 */
const HOST_HEALTH_ICON: Partial<Record<HostChange, LucideIcon>> = {
  attention: HeartPulse,
  failing: TriangleAlert,
  clear: HeartPulse,
  reboot: RotateCw,
};

/**
 * The one line under a host's entry, where there is one.
 *
 * The OS lines say the controller's count, in the pill's own words, and for a
 * reboot whether it can be done now -- the question an operator reads it to
 * answer. The active runner count is always sent, so 0 means nothing is
 * running rather than nothing is known.
 */
function hostDetail(host: Host, change: HostChange): string | undefined {
  switch (change) {
    case 'holding':
      return host.admission_reason || undefined;
    case 'throttled':
      return host.throttle_reason || hostStatus(host).hint;
    case 'incompatible':
      return host.incompatible_reason || undefined;
    case 'unreachable':
      return hostStatus(host).hint;
    case 'attention':
    case 'failing':
      return (host.doctor?.summary && findingsLabel(host.doctor.summary)) || undefined;
    case 'reboot':
      return host.active_runners === 0
        ? 'Nothing is running on it, so it can be rebooted now.'
        : 'Runners are still running on it. Cordon it and reboot once they finish; Zoomies never reboots a host itself.';
    default:
      return undefined;
  }
}

export function hostEntry(host: Host, change: HostChange, at: string): FeedEntry | null {
  if (!host.id) return null;
  const news = HOST_NEWS[change];
  const osIcon = HOST_HEALTH_ICON[change];
  const aboutOS = osIcon !== undefined;
  return {
    id: `host:${host.id}:${change}:${at}`,
    category: 'hosts',
    at,
    tone: news.tone,
    icon: osIcon ?? (change === 'joined' ? Plus : hostStatus(host).icon),
    title: news.title,
    detail: hostDetail(host, change),
    // The host's own page for a line about its OS, because that is where the
    // report and its rows are. `at` stays the arrival time rather than the
    // report's `checked_at`, which is the agent's clock: a host whose clock is
    // wrong would otherwise date its own news.
    target: {
      label: host.name ?? shortId(host.id),
      href: aboutOS ? `/hosts/${host.id}` : '/hosts',
    },
  };
}

export function hostRemovedEntry(id: string, name: string | undefined, at: string): FeedEntry {
  return {
    id: `host:${id}:removed:${at}`,
    category: 'hosts',
    at,
    tone: 'neutral',
    icon: CircleMinus,
    title: 'A host was removed',
    target: { label: name ?? shortId(id), href: '/hosts' },
  };
}

/* -- machines and providers ------------------------------------------------- */

const MACHINE_NEWS: Record<string, string> = {
  creating: 'A machine is being rented',
  ready: 'A machine became a host',
  draining: 'A machine is draining',
  deleted: 'A machine was returned',
  failed: 'A machine failed',
  quarantined: 'A machine was quarantined',
};

export function machineEntry(machine: Machine, state: MachineState, at: string): FeedEntry {
  const status = machineStatus(state);
  return {
    id: `machine:${machine.id}:${state}`,
    category: 'machines',
    at: machine.updated_at ?? at,
    tone: status.tone,
    icon: status.icon,
    title: MACHINE_NEWS[state] ?? `A machine is ${status.label.toLowerCase()}`,
    detail:
      machine.provider_error ||
      machine.bootstrap_error ||
      machine.ownership_error ||
      machine.message ||
      undefined,
    target: { label: machine.name, href: `/machines/${machine.id}` },
  };
}

const PROVIDER_NEWS: Record<ProviderChange, { title: string; tone: StatusTone }> = {
  paused: { title: 'A provider was paused', tone: 'draining' },
  resumed: { title: 'A provider was resumed', tone: 'idle' },
  disabled: { title: 'A provider was disabled', tone: 'draining' },
  enabled: { title: 'A provider was enabled', tone: 'idle' },
  unreachable: { title: 'A provider could not be reached', tone: 'danger' },
  reachable: { title: 'A provider answered again', tone: 'idle' },
};

export function providerEntry(
  provider: Provider,
  change: ProviderChange,
  at: string,
): FeedEntry | null {
  if (!provider.id) return null;
  const news = PROVIDER_NEWS[change];
  return {
    id: `provider:${provider.id}:${change}:${at}`,
    category: 'machines',
    at,
    tone: news.tone,
    icon: ServerCog,
    title: news.title,
    detail:
      change === 'unreachable'
        ? provider.last_check_error || undefined
        : change === 'paused'
          ? provider.paused_reason || undefined
          : undefined,
    target: { label: provider.name ?? shortId(provider.id), href: `/providers/${provider.id}` },
  };
}

/* -- pools ------------------------------------------------------------------ */

export function poolEntry(pool: Pool, change: 'created' | 'updated', at: string): FeedEntry | null {
  if (!pool.id) return null;
  return {
    id: `pool:${pool.id}:${change}:${at}`,
    category: 'pools',
    at,
    tone: change === 'created' ? 'idle' : 'neutral',
    icon: change === 'created' ? Plus : Boxes,
    title: change === 'created' ? 'A pool was created' : 'A pool changed',
    target: { label: pool.name ?? shortId(pool.id), href: `/pools/${pool.id}` },
  };
}

export function poolRemovedEntry(id: string, name: string | undefined, at: string): FeedEntry {
  return {
    id: `pool:${id}:deleted:${at}`,
    category: 'pools',
    at,
    tone: 'neutral',
    icon: CircleMinus,
    title: 'A pool was deleted',
    target: { label: name ?? shortId(id), href: '/pools' },
  };
}

/* -- GitHub ------------------------------------------------------------------ */

const INSTALLATION_NEWS: Record<InstallationChange, { title: string; tone: StatusTone }> = {
  connected: { title: 'A GitHub App was connected', tone: 'idle' },
  failing: { title: 'The GitHub App connection is failing', tone: 'danger' },
  working: { title: 'The GitHub App connection is working again', tone: 'idle' },
};

export function installationEntry(
  installation: Installation,
  change: InstallationChange,
  at: string,
): FeedEntry | null {
  if (!installation.id) return null;
  const news = INSTALLATION_NEWS[change];
  return {
    id: `installation:${installation.id}:${change}:${at}`,
    category: 'github',
    at,
    tone: news.tone,
    icon: Plug,
    title: news.title,
    detail: change === 'failing' ? installation.last_error || undefined : undefined,
    target: {
      label: installation.target ?? shortId(installation.id),
      href: '/installations',
    },
  };
}

/**
 * A delivery Zoomies would not take. An accepted one is the system
 * working, and there are thousands of them.
 */
export function deliveryEntry(delivery: WebhookDelivery): FeedEntry | null {
  if (delivery.status !== 'rejected' && delivery.status !== 'error') return null;
  const status = deliveryStatus(delivery.status);
  const event = [delivery.event, delivery.action].filter(Boolean).join('.');
  return {
    id: `webhook:${delivery.id ?? delivery.delivery_id ?? now()}`,
    category: 'github',
    at: delivery.received_at ?? now(),
    tone: status.tone,
    icon: status.icon,
    title:
      delivery.status === 'rejected'
        ? 'A webhook delivery was rejected'
        : 'A webhook delivery could not be handled',
    detail: delivery.error || status.hint,
    target: {
      label: event || delivery.repo || 'delivery',
      href: '/installations',
    },
  };
}

/* -- problems and the record ------------------------------------------------- */

export function problemEntry(problem: Problem, at: string): FeedEntry {
  const status = severityStatus(problem.severity);
  return {
    id: `problem:${problem.code}:${problem.target_id ?? ''}:${at}`,
    category: 'problems',
    at: problem.since ?? at,
    tone: status.tone,
    icon: status.icon,
    title: problem.title ?? 'A problem was raised',
    detail: problem.fix || problem.detail || undefined,
    target: { label: problem.code ?? 'problem' },
  };
}

/**
 * One recorded action. The action name is printed as it was recorded rather
 * than translated into prose: it is what the Audit page filters on and what a
 * support conversation quotes.
 */
export function auditEntry(event: AuditEvent): FeedEntry | null {
  if (!event.id || !event.action) return null;
  const actor = event.actor_name || event.actor_kind || 'somebody';
  const target =
    event.target_kind && event.target_id
      ? `${event.target_kind} ${shortId(event.target_id)}`
      : undefined;
  // What the controller did on its own is not "who changed what": nobody did,
  // and it has a reason, which is what the entry is read for -- "pool.auto_resize"
  // alone is a number that moved. It has a category of its own, on by default,
  // because a fleet that has turned automatic pools on wants to watch them work.
  const automatic = automaticTitle(event.action);
  const cause = causeOf(event);
  const subject = automatic ? (subjectOf(event) ?? target) : target;
  return {
    id: `audit:${event.id}`,
    category: automatic ? 'automatic' : 'audit',
    at: event.created_at ?? now(),
    tone: 'neutral',
    icon: automatic ? Bot : ScrollText,
    title: automatic ?? `${actor} ran ${event.action}`,
    detail: cause ? (subject ? `${subject}: ${cause}` : cause) : subject,
    target: {
      label: 'Audit',
      href: `/audit?action=${encodeURIComponent(event.action)}`,
    },
  };
}
