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
export type MachineState = Schemas['MachineState'];
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
export const JOB_STATES: readonly JobState[] = ['waiting', 'queued', 'in_progress', 'completed'];

/**
 * What an operator has done to a queued job's provisioning demand.
 *
 * Taken from the API's own enumeration so the Queue and the Jobs page cannot
 * drift apart on the spelling. `ready` and `expedited` are the two halves of
 * an untouched job -- normal demand, and demand an operator expedited -- and
 * the other two are the ways of standing it down.
 */
export type ProvisioningStatus = NonNullable<Query<'listJobs'>['provisioning']>[number];

export const PROVISIONING_STATUSES: readonly ProvisioningStatus[] = [
  'ready',
  'expedited',
  'paused',
  'deleted',
];

/**
 * The statuses that still mean "waiting for a runner here".
 *
 * A paused job is on hold and still waiting; a removed one is an operator
 * saying it should not run here at all, and it stops counting as queued work
 * everywhere the fleet reports queue depth. Any list that calls itself Queued
 * narrows to these, so the list and the figure above it say the same thing.
 */
export const WAITING_PROVISIONING: readonly ProvisioningStatus[] = ['ready', 'expedited', 'paused'];

/**
 * Every machine state, in the order a machine passes through them. Deleted,
 * failed and quarantined are exits from the flow rather than steps of it, and
 * come last for that reason.
 */
export const MACHINE_STATES: readonly MachineState[] = [
  'planned',
  'creating',
  'starting',
  'bootstrapping',
  'enrolling',
  'ready',
  'draining',
  'deleting',
  'deleted',
  'failed',
  'quarantined',
];

/** The roles, weakest first. `atLeast` below compares by this order. */
export const ROLES: readonly Role[] = ['viewer', 'operator', 'admin', 'platform'];

/* -- resources ---------------------------------------------------------- */

export type Page = Schemas['Page'];
export type FieldError = Schemas['FieldError'];
export type Problem = Schemas['Problem'];
export type Meta = Schemas['Meta'];
export type SignInChallenge = Schemas['SignInChallenge'];
export type TwoStepSignIn = Schemas['TwoStepSignIn'];
export type TwoStepStatus = Schemas['TwoStepStatus'];
export type TwoStepSetup = Schemas['TwoStepSetup'];
export type UserPreferences = Schemas['UserPreferences'];
export type TableLayoutPreference = Schemas['TableLayoutPreference'];
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
/** What one pool overrides of the fleet's runner timings; every field optional. */
export type RunnerSettings = Schemas['RunnerSettings'];
export type Platform = Schemas['Platform'];
export type PoolPlatform = Schemas['PoolPlatform'];
export type PoolRoom = Schemas['PoolRoom'];
export type PoolHostRoom = Schemas['PoolHostRoom'];
export type Pool = Schemas['Pool'];
export type PoolCreate = Schemas['PoolCreate'];
export type PoolUpdate = Schemas['PoolUpdate'];
export type Runner = Schemas['Runner'];
export type RunnerDetail = Schemas['RunnerDetail'];
export type TimelineEntry = Schemas['TimelineEntry'];
export type JobExplanation = Schemas['JobExplanation'];
export type Job = Schemas['Job'];
export type JobStats = Schemas['JobStats'];
export type JobStatsGroup = Schemas['JobStatsGroup'];
export type WorkflowRun = Schemas['WorkflowRun'];
export type JobStep = Schemas['JobStep'];
export type JobEvent = Schemas['JobEvent'];
export type JobEventKind = Schemas['JobEventKind'];
export type FaultKind = Schemas['FaultKind'];
export type BackendInfo = Schemas['BackendInfo'];
export type Host = Schemas['Host'];
export type HostSample = Schemas['HostSample'];
export type HostThrottle = Schemas['HostThrottle'];
export type HostExclusion = Schemas['HostExclusion'];
export type JoinToken = Schemas['JoinToken'];
export type Provider = Schemas['Provider'];
export type ProviderInput = Schemas['ProviderInput'];
/** The enum: which infrastructure a provider rents from. */
export type ProviderKindName = Schemas['ProviderKindName'];
/** What a driver can do, and the schema its form renders from. */
export type ProviderKind = Schemas['ProviderKind'];
export type ProviderSetting = Schemas['ProviderSetting'];
export type ProviderGuideStep = Schemas['ProviderGuideStep'];
export type ProviderValidation = Schemas['ProviderValidation'];
export type ProviderCheck = Schemas['ProviderCheck'];
export type ProviderChoice = Schemas['ProviderChoice'];
export type ProviderDiscovery = Schemas['ProviderDiscovery'];
export type ProviderOrphans = Schemas['ProviderOrphans'];
export type Orphan = Schemas['Orphan'];
export type Machine = Schemas['Machine'];
export type MachineTimelineEntry = Schemas['MachineTimelineEntry'];
export type MigrationPlan = Schemas['MigrationPlan'];
export type MigrationRepo = Schemas['MigrationRepo'];
export type MigrationWorkflow = Schemas['MigrationWorkflow'];
export type MigrationRewrite = Schemas['MigrationRewrite'];
export type MigrationSkip = Schemas['MigrationSkip'];
export type MigrationOverride = Schemas['MigrationOverride'];
export type MigrationPoolOption = Schemas['MigrationPoolOption'];
export type MigrationOutcome = Schemas['MigrationOutcome'];
export type MigrationResult = Schemas['MigrationResult'];
export type Usage = Schemas['UsageResponse'];
export type UsageRow = Usage['items'][number];
export type UsageGrouping = Usage['group_by'];
export type InstallationReport = Schemas['InstallationReport'];
export type ReportPercentiles = Schemas['ReportPercentiles'];
export type AuditEvent = Schemas['AuditEvent'];
export type User = Schemas['User'];
export type Identity = Schemas['Identity'];
export type APIToken = Schemas['APIToken'];
export type MCPClient = Schemas['MCPClient'];
export type MCPConnection = Schemas['MCPConnection'];
export type MCPConsent = Schemas['MCPConsent'];
export type Settings = Schemas['Settings'];
export type Setting = Schemas['Setting'];
export type SettingKind = NonNullable<Setting['kind']>;
export type SettingSource = NonNullable<Setting['source']>;
export type SettingScope = NonNullable<Setting['scope']>;
export type RunnerGroup = NonNullable<Result<'listRunnerGroups'>['items']>[number];
export type Backup = Schemas['Backup'];
export type Backups = Schemas['Backups'];
export type BackupSchedule = Schemas['BackupSchedule'];
export type BackupVerification = Schemas['BackupVerification'];
export type BackupRemote = Schemas['BackupRemote'];
export type RemoteBackupCopy = Schemas['RemoteBackupCopy'];
export type RemoteBackupCheck = Schemas['RemoteBackupCheck'];
export type BackupPruning = Schemas['BackupPruning'];
export type RemotePruning = Schemas['RemotePruning'];
export type BackupRemoteInput = Schemas['BackupRemoteInput'];
export type StagedRestore = Schemas['StagedRestore'];
export type RestoreOutcome = Schemas['RestoreOutcome'];
export type SettingsImport = Schemas['SettingsImport'];
export type SettingsImportChange = Schemas['SettingsImportChange'];
export type SettingsImportAction = SettingsImportChange['action'];
export type PoolsImport = Schemas['PoolsImport'];
export type PoolsImportChange = Schemas['PoolsImportChange'];

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

/**
 * The JSON body of an operation whose request body is optional -- a pause that
 * takes a reason, say. `Body` cannot answer for one, because an optional
 * `requestBody` does not match its required shape.
 */
export type OptionalBody<K extends keyof Ops> = Ops[K] extends {
  requestBody?: { content: { 'application/json': infer B } };
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
  'provider.updated': Provider;
  'provider.deleted': Deleted;
  'machine.updated': Machine;
  'machine.deleted': Deleted;
  scaling: ScalingEvent;
  'installation.updated': Installation;
  'installation.deleted': Deleted;
  'problems.updated': { ok: boolean; items: Problem[] };
  stats: Stats;
  audit: AuditEvent;
  'webhook.delivery': WebhookDelivery;
  heartbeat: unknown;
  /**
   * The first frame on a reconnection whose gap the server could not replay:
   * its buffer had moved on, or the controller restarted. The cache is stale
   * and should be fetched again.
   */
  resync: { reason?: string };
}

export type EventKind = keyof EventPayloads;

export type AIContextRepository = Schemas['AIContextRepository'];
export type AIContextConfig = AIContextRepository['config'];
export type AIContextDiscovery = Result<'discoverAIContext'>;
