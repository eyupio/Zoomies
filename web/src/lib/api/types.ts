/**
 * Friendly names for the generated OpenAPI types, plus the small amount of type
 * machinery the hand-written client needs.
 *
 * `schema.d.ts` is generated from `api/openapi.yaml` and must never be edited.
 * Everything in the UI that touches the API goes through the aliases here, so a
 * change to the OpenAPI document breaks the build rather than the runtime.
 */
import type { components, operations } from './schema';

export type Schemas = components['schemas'];
export type Ops = operations;

/* -- enumerations ------------------------------------------------------- */

export type Role = Schemas['Role'];
export type TargetType = Schemas['TargetType'];
export type BackendKind = Schemas['BackendKind'];
export type DockerMode = Schemas['DockerMode'];
export type RunnerState = Schemas['RunnerState'];
export type JobState = Schemas['JobState'];
export type Severity = Schemas['Severity'];
export type GoDuration = Schemas['Duration'];
export type ErrorCode = Schemas['ErrorEnvelope']['error']['code'];

/** Every runner state, in the order an operator thinks about them. */
export const RUNNER_STATES: readonly RunnerState[] = [
  'provisioning',
  'registering',
  'idle',
  'busy',
  'draining',
  'failed',
  'removed',
];

/** Every job state. */
export const JOB_STATES: readonly JobState[] = ['queued', 'in_progress', 'completed'];

/** The roles, weakest first. `atLeast` below compares by this order. */
export const ROLES: readonly Role[] = ['viewer', 'operator', 'admin'];

/* -- resources ---------------------------------------------------------- */

export type Page = Schemas['Page'];
export type FieldError = Schemas['FieldError'];
export type Problem = Schemas['Problem'];
export type Meta = Schemas['Meta'];
export type Stats = Schemas['Stats'];
export type PoolStats = NonNullable<Stats['pools']>[number];
export type FleetSample = Schemas['FleetSample'];
export type ScalingEvent = Schemas['ScalingEvent'];
export type Installation = Schemas['Installation'];
export type InstallationCreate = Schemas['InstallationCreate'];
export type InstallationUpdate = Schemas['InstallationUpdate'];
export type InstallationHealth = Schemas['InstallationHealth'];
export type WebhookDelivery = Schemas['WebhookDelivery'];
export type WebhookCheck = Schemas['WebhookCheck'];
export type Resources = Schemas['Resources'];
export type Pool = Schemas['Pool'];
export type PoolCreate = Schemas['PoolCreate'];
export type PoolUpdate = Schemas['PoolUpdate'];
export type Runner = Schemas['Runner'];
export type RunnerDetail = Schemas['RunnerDetail'];
export type TimelineEntry = Schemas['TimelineEntry'];
export type Job = Schemas['Job'];
export type BackendInfo = Schemas['BackendInfo'];
export type Host = Schemas['Host'];
export type JoinToken = Schemas['JoinToken'];
export type MigrationPlan = Schemas['MigrationPlan'];
export type MigrationRepo = Schemas['MigrationRepo'];
export type MigrationWorkflow = Schemas['MigrationWorkflow'];
export type MigrationRewrite = Schemas['MigrationRewrite'];
export type MigrationSkip = Schemas['MigrationSkip'];
export type MigrationPoolOption = Schemas['MigrationPoolOption'];
export type MigrationOutcome = Schemas['MigrationOutcome'];
export type MigrationResult = Schemas['MigrationResult'];
export type AuditEvent = Schemas['AuditEvent'];
export type User = Schemas['User'];
export type Identity = Schemas['Identity'];
export type APIToken = Schemas['APIToken'];
export type Settings = Schemas['Settings'];
export type RunnerGroup = NonNullable<Result<'listRunnerGroups'>['items']>[number];

/** A resource that no longer exists. Carried by the `*.deleted` SSE kinds. */
export interface Deleted {
  id: string;
}

/* -- operation helpers --------------------------------------------------
 * These pull the request and response shapes straight out of the generated
 * `operations` map, so every helper in `client.ts` is typed by the OpenAPI
 * document rather than by hand.
 * --------------------------------------------------------------------- */

type JsonOf<T> = T extends { content: { 'application/json': infer B } } ? B : void;

type SuccessOf<R> = 200 extends keyof R
  ? R[200]
  : 201 extends keyof R
    ? R[201]
    : 202 extends keyof R
      ? R[202]
      : 204 extends keyof R
        ? R[204]
        : never;

/** The decoded body of an operation's success response, or `void` if it has none. */
export type Result<K extends keyof Ops> = JsonOf<SuccessOf<Ops[K]['responses']>>;

/** The JSON request body an operation takes. */
export type Body<K extends keyof Ops> = Ops[K] extends {
  requestBody: { content: { 'application/json': infer B } };
}
  ? B
  : never;

/** The query parameters an operation accepts. */
export type Query<K extends keyof Ops> = Ops[K] extends { parameters: { query?: infer Q } }
  ? NonNullable<Q>
  : never;

/** A paginated list response: `{ items, total, limit, offset }`. */
export type Paged<T> = Page & { items?: T[] };

/* -- roles -------------------------------------------------------------- */

/** True when `held` is at least as strong as `needed`. */
export function atLeast(held: Role | undefined, needed: Role): boolean {
  if (!held) return false;
  return ROLES.indexOf(held) >= ROLES.indexOf(needed);
}

/* -- live events --------------------------------------------------------
 * The kinds emitted on /api/v1/events. `heartbeat` arrives as a comment
 * frame; it is listed because the stream state machine names it.
 * --------------------------------------------------------------------- */

export interface EventPayloads {
  'runner.created': Runner;
  'runner.updated': Runner;
  'runner.deleted': Deleted;
  'pool.created': Pool;
  'pool.updated': Pool;
  'pool.deleted': Deleted;
  'job.updated': Job;
  'host.updated': Host;
  'host.deleted': Deleted;
  scaling: ScalingEvent;
  'installation.updated': Installation;
  'problems.updated': { ok: boolean; items: Problem[] };
  stats: Stats;
  audit: AuditEvent;
  'webhook.delivery': WebhookDelivery;
  heartbeat: unknown;
}

export type EventKind = keyof EventPayloads;

export const EVENT_KINDS: readonly EventKind[] = [
  'runner.created',
  'runner.updated',
  'runner.deleted',
  'pool.created',
  'pool.updated',
  'pool.deleted',
  'job.updated',
  'host.updated',
  'host.deleted',
  'scaling',
  'installation.updated',
  'problems.updated',
  'stats',
  'audit',
  'webhook.delivery',
  'heartbeat',
];
