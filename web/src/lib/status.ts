/**
 * The state map.
 *
 * Every status rendered anywhere in Zoomies comes from here, so an operator
 * learns one vocabulary. Each entry carries three things, and the shape is not
 * optional: colour is never the sole carrier of meaning (docs/ui-guidelines.md
 * section 1.1 and the accessibility checklist in section 6).
 *
 *   tone   which status token pair to use (`--z-busy`, `--z-busy-subtle`, ...)
 *   shape  the glyph StatusDot draws -- filled, hollow, dashed, slash, triangle, square
 *   icon   the Lucide icon Badge and the detail pages use
 */
import {
  Ban,
  Circle,
  CircleCheck,
  CircleDashed,
  CircleMinus,
  CircleSlash,
  CircleX,
  Clock,
  Info,
  Minus,
  Play,
  TriangleAlert,
} from '@lucide/svelte';
import type { LucideIcon } from '@lucide/svelte';
import type {
  APIToken,
  Host,
  Job,
  JobEventKind,
  JobState,
  JoinToken,
  Pool,
  Runner,
  RunnerState,
  Severity,
} from './api/types';

/** The six status hues from the token file. Nothing else may use them. */
export type StatusTone = 'idle' | 'busy' | 'pending' | 'draining' | 'danger' | 'neutral';

/**
 * The shape half of the encoding. `square` extends the five in the guidelines
 * for terminal states, which need to be distinguishable from `draining` at a
 * glance without inventing a colour.
 */
export type StatusShape = 'filled' | 'hollow' | 'dashed' | 'slash' | 'triangle' | 'square';

export interface StatusMeta {
  /** Stable key, useful for `data-` attributes and tests. */
  key: string;
  /** Sentence-case label, shown next to the shape. */
  label: string;
  tone: StatusTone;
  shape: StatusShape;
  icon: LucideIcon;
  /** The token holding this tone's foreground colour. */
  colour: string;
  /** The token for a badge or chart fill in this tone. */
  subtle: string;
  /** The token for a hairline in this tone. */
  border: string;
  /** One line an operator can act on. Used in tooltips and empty states. */
  hint?: string;
}

/** The CSS custom properties for a tone. */
export function toneTokens(tone: StatusTone): { colour: string; subtle: string; border: string } {
  return {
    colour: `var(--z-${tone})`,
    subtle: `var(--z-${tone}-subtle)`,
    border: `var(--z-${tone}-border)`,
  };
}

function meta(
  key: string,
  label: string,
  tone: StatusTone,
  shape: StatusShape,
  icon: LucideIcon,
  hint?: string,
): StatusMeta {
  return { key, label, tone, shape, icon, ...toneTokens(tone), hint };
}

/* -- runners -------------------------------------------------------------- */

const RUNNER: Record<RunnerState, StatusMeta> = {
  provisioning: meta(
    'provisioning',
    'Provisioning',
    'pending',
    'dashed',
    CircleDashed,
    'The host is creating the container.',
  ),
  registering: meta(
    'registering',
    'Registering',
    'pending',
    'dashed',
    CircleDashed,
    'Waiting for GitHub to accept the runner.',
  ),
  idle: meta('idle', 'Idle', 'idle', 'hollow', Circle, 'Registered and waiting for a job.'),
  busy: meta('busy', 'Busy', 'busy', 'filled', Play, 'Running a job right now.'),
  draining: meta(
    'draining',
    'Draining',
    'draining',
    'slash',
    CircleSlash,
    'Finishing its current job, then exiting. No new work is sent to it.',
  ),
  failed: meta(
    'failed',
    'Failed',
    'danger',
    'triangle',
    TriangleAlert,
    'The runner stopped unexpectedly. Its message says why.',
  ),
  removed: meta(
    'removed',
    'Removed',
    'neutral',
    'square',
    CircleMinus,
    'Gone, and deregistered from GitHub.',
  ),
};

const UNKNOWN = meta('unknown', 'Unknown', 'neutral', 'square', CircleMinus);

export function runnerStatus(state: RunnerState | undefined): StatusMeta {
  return state ? (RUNNER[state] ?? UNKNOWN) : UNKNOWN;
}

/** Convenience for a row that has the whole runner to hand. */
export function runnerRowStatus(runner: Pick<Runner, 'state'>): StatusMeta {
  return runnerStatus(runner.state);
}

/** Every runner state with its label, for filter menus. */
export function runnerStatuses(): StatusMeta[] {
  return Object.values(RUNNER);
}

/* -- jobs ----------------------------------------------------------------- */

const JOB_QUEUED = meta(
  'queued',
  'Queued',
  'pending',
  'dashed',
  Clock,
  'Waiting for a runner that answers its labels.',
);
const JOB_RUNNING = meta('in_progress', 'Running', 'busy', 'filled', Play);
const JOB_WAITING = meta(
  'waiting',
  'Waiting',
  'neutral',
  'hollow',
  Clock,
  'Held by GitHub for a deployment review. Nothing runs it until someone approves it.',
);

const CONCLUSIONS: Record<string, StatusMeta> = {
  success: meta('success', 'Success', 'idle', 'filled', CircleCheck),
  failure: meta('failure', 'Failure', 'danger', 'triangle', CircleX),
  cancelled: meta('cancelled', 'Cancelled', 'neutral', 'square', Ban),
  skipped: meta('skipped', 'Skipped', 'neutral', 'hollow', Minus),
  timed_out: meta('timed_out', 'Timed out', 'danger', 'triangle', Clock),
  startup_failure: meta('startup_failure', 'Startup failure', 'danger', 'triangle', CircleX),
  action_required: meta('action_required', 'Action required', 'pending', 'triangle', TriangleAlert),
  neutral: meta('neutral', 'Neutral', 'neutral', 'hollow', Minus),
  stale: meta('stale', 'Stale', 'neutral', 'square', CircleMinus),
};

/** A job's status is its state, except once it is complete, when it is its conclusion. */
export function jobStatus(state: JobState | undefined, conclusion?: string | null): StatusMeta {
  if (state === 'waiting') return JOB_WAITING;
  if (state === 'queued') return JOB_QUEUED;
  if (state === 'in_progress') return JOB_RUNNING;
  if (state === 'completed') {
    const key = (conclusion ?? '').toLowerCase();
    return (
      CONCLUSIONS[key] ?? meta(key || 'completed', 'Completed', 'neutral', 'square', CircleCheck)
    );
  }
  return UNKNOWN;
}

/** A queued job no enabled pool claims. Nothing here will start it. */
export const UNMATCHED: StatusMeta = meta(
  'unmatched',
  'Unmatched',
  'danger',
  'triangle',
  TriangleAlert,
  'No enabled pool here answers these labels, so nothing in this fleet will start it.',
);

/**
 * Whether a job is actually waiting on a pool that does not exist.
 *
 * `matched` alone is not that question. It records only that no enabled pool
 * claims the job's labels, which is equally true of every repository still on
 * GitHub-hosted or vendor runners -- jobs that run perfectly well, just not
 * here. Saying "will never run" about a job that already succeeded turns the
 * Jobs page into a wall of red during exactly the migration this fleet exists
 * to make, so the warning is kept for the one case it is true of: a job still
 * queued, with nothing to hand it to.
 */
export function stuckUnmatched(job: { matched?: boolean; state?: JobState }): boolean {
  return job.matched === false && job.state === 'queued';
}

/**
 * A job whose runner stopped under it. GitHub records the job as an ordinary
 * failure; this badge is how the fleet owns up to having caused it.
 */
export const RUNNER_LOST: StatusMeta = meta(
  'runner_lost',
  'Runner lost',
  'danger',
  'triangle',
  TriangleAlert,
  "The runner executing this job stopped before GitHub reported the job over. The failure is the fleet's, not the workflow's.",
);

/** The conclusions GitHub counts as a job going wrong, as the failed filter does. */
const FAILING_CONCLUSIONS = new Set(['failure', 'timed_out', 'startup_failure']);

/**
 * Whether a job went wrong on either side: a failing conclusion, or a runner
 * that stopped under it -- including one GitHub still believes is running.
 */
export function jobFailed(job: Pick<Job, 'conclusion' | 'runner_fault'>): boolean {
  return Boolean(job.runner_fault) || FAILING_CONCLUSIONS.has((job.conclusion ?? '').toLowerCase());
}

/**
 * One step of a job, coloured like the job it belongs to: a step still running
 * is busy, one that has not started is pending, and a finished one takes its
 * conclusion.
 */
export function stepStatus(step: { status?: string; conclusion?: string }): StatusMeta {
  if (step.status === 'completed' || step.conclusion) {
    return jobStatus('completed', step.conclusion);
  }
  if (step.status === 'in_progress') return JOB_RUNNING;
  return meta('queued', 'Not started', 'pending', 'hollow', Clock);
}

/* -- the job timeline ----------------------------------------------------- */

const JOB_EVENTS: Record<JobEventKind, StatusMeta> = {
  queued: JOB_QUEUED,
  claimed: meta('claimed', 'Claimed', 'idle', 'hollow', Circle),
  unmatched: UNMATCHED,
  started: JOB_RUNNING,
  completed: meta('completed', 'Completed', 'neutral', 'square', CircleCheck),
  runner_lost: RUNNER_LOST,
};

/**
 * The status a timeline entry is drawn with. A `completed` entry takes the
 * job's own conclusion when it is known, so the last mark on a failed job's
 * timeline is red rather than a neutral "it ended".
 */
export function jobEventStatus(
  kind: JobEventKind | undefined,
  conclusion?: string | null,
): StatusMeta {
  if (kind === 'completed' && conclusion) return jobStatus('completed', conclusion);
  return kind ? (JOB_EVENTS[kind] ?? UNKNOWN) : UNKNOWN;
}

/* -- hosts ---------------------------------------------------------------- */

export function hostStatus(host: Pick<Host, 'healthy' | 'cordoned'>): StatusMeta {
  if (host.healthy === false) {
    return meta(
      'unreachable',
      'Unreachable',
      'danger',
      'triangle',
      TriangleAlert,
      'No heartbeat recently. Check the agent on this host.',
    );
  }
  if (host.cordoned) {
    return meta(
      'cordoned',
      'Cordoned',
      'draining',
      'slash',
      CircleSlash,
      'Existing runners keep going; no new ones are placed here.',
    );
  }
  return meta('healthy', 'Healthy', 'idle', 'hollow', Circle);
}

/* -- pools ---------------------------------------------------------------- */

export function poolStatus(pool: Pick<Pool, 'enabled'>): StatusMeta {
  return pool.enabled === false
    ? meta(
        'disabled',
        'Disabled',
        'draining',
        'slash',
        CircleSlash,
        'Existing runners drain; no new ones are made.',
      )
    : meta('enabled', 'Enabled', 'idle', 'hollow', Circle);
}

/* -- problems and deliveries ---------------------------------------------- */

export function severityStatus(severity: Severity | undefined): StatusMeta {
  if (severity === 'error') return meta('error', 'Error', 'danger', 'triangle', TriangleAlert);
  if (severity === 'warning')
    return meta('warning', 'Warning', 'pending', 'triangle', TriangleAlert);
  return meta('info', 'Info', 'busy', 'hollow', Info);
}

export type DeliveryStatus = 'accepted' | 'rejected' | 'error';

export function deliveryStatus(status: DeliveryStatus | undefined): StatusMeta {
  if (status === 'accepted') return meta('accepted', 'Accepted', 'idle', 'filled', CircleCheck);
  if (status === 'rejected') {
    return meta(
      'rejected',
      'Rejected',
      'danger',
      'triangle',
      CircleX,
      'The signature did not verify. Check the webhook secret.',
    );
  }
  if (status === 'error') return meta('error', 'Error', 'danger', 'triangle', TriangleAlert);
  return UNKNOWN;
}

/** GitHub App connection health. */
export function installationStatus(healthy: boolean | undefined): StatusMeta {
  if (healthy === true) return meta('healthy', 'Connected', 'idle', 'hollow', Circle);
  if (healthy === false) {
    return meta(
      'unhealthy',
      'Not connected',
      'danger',
      'triangle',
      TriangleAlert,
      'Verify the installation to see which credential or permission is missing.',
    );
  }
  return meta('unchecked', 'Not checked', 'neutral', 'dashed', CircleDashed);
}

/** A user or token account state. */
export function accountStatus(disabled: boolean | undefined): StatusMeta {
  return disabled
    ? meta('disabled', 'Disabled', 'draining', 'slash', CircleSlash)
    : meta('active', 'Active', 'idle', 'hollow', Circle);
}

/* -- credentials ---------------------------------------------------------- */

/** How soon "expires soon" is: a week, so the warning lands on a working day. */
const EXPIRY_WARNING_MS = 7 * 24 * 60 * 60 * 1000;

/** Where an API token is in its life: revoked, expired, expiring, or good. */
export function apiTokenStatus(
  token: Pick<APIToken, 'revoked' | 'expires_at'>,
  now: number = Date.now(),
): StatusMeta {
  if (token.revoked) {
    return meta(
      'revoked',
      'Revoked',
      'draining',
      'slash',
      CircleSlash,
      'Switched off by an administrator.',
    );
  }
  if (!token.expires_at) {
    return meta(
      'never_expires',
      'Never expires',
      'pending',
      'triangle',
      TriangleAlert,
      'A token with no expiry stays valid until it is revoked.',
    );
  }
  const remaining = new Date(token.expires_at).getTime() - now;
  if (remaining <= 0) return meta('expired', 'Expired', 'neutral', 'square', CircleMinus);
  if (remaining < EXPIRY_WARNING_MS) {
    return meta(
      'expiring',
      'Expires soon',
      'pending',
      'dashed',
      Clock,
      'Create a replacement before it does.',
    );
  }
  return meta('active', 'Active', 'idle', 'hollow', Circle);
}

/** Whether a join token can still enrol a host. */
export function joinTokenStatus(token: Pick<JoinToken, 'used_at' | 'usable'>): StatusMeta {
  if (token.used_at) {
    return meta(
      'used',
      'Used',
      'neutral',
      'square',
      CircleMinus,
      'A join token enrols one host, once.',
    );
  }
  if (token.usable === false) {
    return meta(
      'expired',
      'Expired',
      'draining',
      'slash',
      CircleSlash,
      'Create a new one to add a host.',
    );
  }
  return meta('usable', 'Usable', 'idle', 'hollow', Circle);
}

/** Whether the webhook address answered the controller's own probe. */
export function reachabilityStatus(reachable: boolean | undefined): StatusMeta {
  if (reachable === true) return meta('reachable', 'Reachable', 'idle', 'filled', CircleCheck);
  if (reachable === false) {
    return meta(
      'unreachable',
      'Not reachable',
      'danger',
      'triangle',
      TriangleAlert,
      'Nothing answered at the webhook address, so GitHub cannot deliver to it either.',
    );
  }
  return meta('unchecked', 'Not checked', 'neutral', 'dashed', CircleDashed);
}
