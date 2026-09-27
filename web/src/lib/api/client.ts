/**
 * The Zoomies API client.
 *
 * Hand-written rather than generated, but every signature is derived from
 * `schema.d.ts`, which is generated from `api/openapi.yaml`. If the API changes
 * shape, this file stops compiling -- which is the point.
 *
 * Error handling follows the contract in docs/api-surface.md:
 *   401  the session is gone. Clear it and go to the login page.
 *   403  the message names the role required. Show it verbatim.
 *   422  `errors` names the offending fields so a form can attach them.
 */
import { supportHint } from '$lib/errors';
import type {
  Body,
  ErrorCode,
  FieldError,
  OptionalBody,
  Query,
  Result,
  SignInChallenge,
} from './types';

const BASE = '/api/v1';

/* -- errors -------------------------------------------------------------- */

export interface ApiErrorInit {
  status: number;
  code: ErrorCode;
  message: string;
  field?: string;
  detail?: string;
  errors?: FieldError[];
}

/**
 * Every failure the client throws. Never a bare `Error`, so callers can always
 * ask what happened without parsing strings.
 */
export class ApiError extends Error {
  readonly status: number;
  readonly code: ErrorCode;
  readonly field: string | undefined;
  readonly detail: string | undefined;
  readonly errors: FieldError[] | undefined;

  constructor(init: ApiErrorInit) {
    super(init.message);
    this.name = 'ApiError';
    this.status = init.status;
    this.code = init.code;
    this.field = init.field;
    this.detail = init.detail;
    this.errors = init.errors;
  }

  /** Signed in, but not allowed. The server's message says which role is needed. */
  get isForbidden(): boolean {
    return this.status === 403;
  }

  /** The request conflicts with the current state -- a runner already terminal, say. */
  get isConflict(): boolean {
    return this.status === 409;
  }

  get isNotFound(): boolean {
    return this.status === 404;
  }

  /** The network, not the server. Nothing the operator did is at fault. */
  get isOffline(): boolean {
    return this.status === 0;
  }

  /** Field errors keyed by field name, ready to hand to a form. */
  fieldErrors(): Record<string, string> {
    const out: Record<string, string> = {};
    for (const e of this.errors ?? []) out[e.field] = e.message;
    if (this.field && !out[this.field]) out[this.field] = this.message;
    return out;
  }
}

/* -- the 401 hook --------------------------------------------------------
 * The client must not import the session or the router: they both import the
 * client. The shell registers a handler at boot instead.
 * --------------------------------------------------------------------- */

type UnauthorizedHandler = () => void;
let unauthorized: UnauthorizedHandler | undefined;

/** Register what happens when the server says the session is gone. */
export function onUnauthorized(handler: UnauthorizedHandler): void {
  unauthorized = handler;
}

/* -- query strings -------------------------------------------------------- */

export type QueryValue = string | number | boolean | null | undefined | readonly string[];
export type QueryInput = Record<string, QueryValue | readonly number[]>;

/**
 * Serialise a query object the way the API expects: `form` style with
 * `explode: true`, so an array repeats its key (`?state=idle&state=busy`).
 * Empty values are dropped rather than sent blank.
 */
export function toQuery(query: QueryInput | undefined): string {
  if (!query) return '';
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === '') continue;
    if (Array.isArray(value)) {
      for (const v of value as readonly (string | number)[]) {
        if (v === undefined || v === null || v === '') continue;
        params.append(key, String(v));
      }
    } else {
      params.append(key, String(value));
    }
  }
  const s = params.toString();
  return s ? `?${s}` : '';
}

/* -- the request ---------------------------------------------------------- */

export interface RequestOptions {
  query?: QueryInput;
  body?: unknown;
  signal?: AbortSignal;
  /** Do not run the global 401 handler -- used by the boot probe and the login form. */
  allow401?: boolean;
}

type Method = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';

async function request<T>(method: Method, path: string, options: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  const init: RequestInit = {
    method,
    headers,
    // Cookie auth. Bearer tokens are for the CLI, not for this UI.
    credentials: 'same-origin',
  };
  if (options.signal) init.signal = options.signal;
  if (options.body !== undefined) {
    headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(options.body);
  }

  let response: Response;
  try {
    response = await fetch(`${BASE}${path}${toQuery(options.query)}`, init);
  } catch (cause) {
    if (cause instanceof DOMException && cause.name === 'AbortError') throw cause;
    throw new ApiError({
      status: 0,
      code: 'internal',
      message: 'Could not reach the Zoomies API. Check that the controller is still running.',
      detail: cause instanceof Error ? cause.message : undefined,
    });
  }

  if (response.status === 204 || response.status === 205) return undefined as T;

  const isJson = (response.headers.get('content-type') ?? '').includes('application/json');
  const payload: unknown = isJson ? await response.json().catch(() => undefined) : undefined;

  if (response.ok) return payload as T;

  if (response.status === 401 && !options.allow401) unauthorized?.();

  throw new ApiError(errorFrom(response.status, payload));
}

interface ErrorBody {
  error?: { code?: ErrorCode; message?: string; field?: string; detail?: string };
  errors?: FieldError[];
}

function errorFrom(status: number, payload: unknown): ApiErrorInit {
  const body = (payload ?? {}) as ErrorBody;
  const inner = body.error ?? {};
  return {
    status,
    code: inner.code ?? defaultCode(status),
    message: inner.message ?? defaultMessage(status),
    field: inner.field,
    detail: inner.detail,
    errors: body.errors,
  };
}

function defaultCode(status: number): ErrorCode {
  if (status === 400) return 'bad_request';
  if (status === 401) return 'unauthorized';
  if (status === 403) return 'forbidden';
  if (status === 404) return 'not_found';
  if (status === 409) return 'conflict';
  if (status === 422) return 'unprocessable';
  if (status === 429) return 'rate_limited';
  return 'internal';
}

function defaultMessage(status: number): string {
  switch (status) {
    case 401:
      return 'Your session has expired. Sign in again to continue.';
    case 403:
      return 'You do not have permission to do that.';
    case 404:
      return 'That no longer exists. It may have been removed since the page loaded.';
    case 409:
      return 'That conflicts with the current state. Reload the page and try again.';
    case 429:
      return 'Too many attempts. Wait a moment and try again.';
    default:
      return `The server returned ${status}. ${supportHint()}`;
  }
}

/** The low-level verbs. Prefer the named helpers below; these are the escape hatch. */
export const api = {
  get: <T>(path: string, options?: RequestOptions) => request<T>('GET', path, options),
  post: <T>(path: string, options?: RequestOptions) => request<T>('POST', path, options),
  put: <T>(path: string, options?: RequestOptions) => request<T>('PUT', path, options),
  patch: <T>(path: string, options?: RequestOptions) => request<T>('PATCH', path, options),
  del: <T>(path: string, options?: RequestOptions) => request<T>('DELETE', path, options),
};

const enc = encodeURIComponent;

/* -- meta and authentication --------------------------------------------- */

export const getMeta = (signal?: AbortSignal) =>
  api.get<Result<'getMeta'>>('/meta', { signal, allow401: true });

export const getSession = (signal?: AbortSignal) =>
  api.get<Result<'getSession'>>('/auth/session', { signal, allow401: true });

export const getOwnPreferences = (signal?: AbortSignal) =>
  api.get<Result<'getOwnPreferences'>>('/auth/preferences', { signal });

export const replaceOwnPreferences = (body: Body<'replaceOwnPreferences'>) =>
  api.put<Result<'replaceOwnPreferences'>>('/auth/preferences', { body });

/**
 * Sign in. A 200 is the identity; a 202 is a sign-in still waiting for its
 * second step, whose state is in a cookie this page cannot read.
 */
export const login = (body: Body<'login'>) =>
  api.post<Result<'login'> | SignInChallenge>('/auth/login', { body, allow401: true });

export const verifyTwoStepSignIn = (body: Body<'verifyTwoStepSignIn'>) =>
  api.post<Result<'verifyTwoStepSignIn'>>('/auth/two-step/verify', { body, allow401: true });

export const startTwoStepEnrolment = () =>
  api.post<Result<'startTwoStepEnrolment'>>('/auth/two-step/enrol', { allow401: true });

export const confirmTwoStepEnrolment = (body: Body<'confirmTwoStepEnrolment'>) =>
  api.post<Result<'confirmTwoStepEnrolment'>>('/auth/two-step/enrol/confirm', {
    body,
    allow401: true,
  });

export const getTwoStep = (signal?: AbortSignal) =>
  api.get<Result<'getTwoStep'>>('/auth/two-step', { signal });

export const setupTwoStep = () => api.post<Result<'setupTwoStep'>>('/auth/two-step/setup', {});

export const confirmTwoStep = (body: Body<'confirmTwoStep'>) =>
  api.post<Result<'confirmTwoStep'>>('/auth/two-step/confirm', { body });

export const disableTwoStep = (body: Body<'disableTwoStep'>) =>
  api.post<Result<'disableTwoStep'>>('/auth/two-step/disable', { body });

export const regenerateRecoveryCodes = (body: Body<'regenerateRecoveryCodes'>) =>
  api.post<Result<'regenerateRecoveryCodes'>>('/auth/two-step/recovery-codes', { body });

export const logout = () => api.post<Result<'logout'>>('/auth/logout', {});

export const bootstrap = (body: Body<'bootstrap'>) =>
  api.post<Result<'bootstrap'>>('/auth/bootstrap', { body, allow401: true });

export const changeOwnPassword = (body: Body<'changeOwnPassword'>) =>
  api.post<Result<'changeOwnPassword'>>('/auth/password', { body });

/** Where the browser goes to start the OIDC flow. A full navigation, not fetch. */
/**
 * Where the single sign-on button goes. A sign-in that began on the MCP
 * consent screen carries that address along, so the person lands back on the
 * decision they came to make rather than on the Overview.
 */
export const oidcStartUrl = (returnTo?: string) =>
  returnTo
    ? `${BASE}/auth/oidc/start?return_to=${encodeURIComponent(returnTo)}`
    : `${BASE}/auth/oidc/start`;

/* -- overview ------------------------------------------------------------- */

export const getStats = (query?: Query<'getStats'>, signal?: AbortSignal) =>
  api.get<Result<'getStats'>>('/stats', { query, signal });

export const listSamples = (query?: Query<'listSamples'>, signal?: AbortSignal) =>
  api.get<Result<'listSamples'>>('/samples', { query, signal });

export const listProblems = (signal?: AbortSignal) =>
  api.get<Result<'listProblems'>>('/problems', { signal });

export const listScalingEvents = (query?: Query<'listScalingEvents'>, signal?: AbortSignal) =>
  api.get<Result<'listScalingEvents'>>('/scaling-events', { query, signal });

/* -- pools ---------------------------------------------------------------- */

/**
 * Where the browser goes for a pools export. A navigation rather than a fetch,
 * as the settings export is, because the point is the browser's own download.
 */
export const poolsExportUrl = (format: 'json' | 'yaml') =>
  `${BASE}/pools/export${toQuery({ format })}`;

export const importPools = (body: Body<'importPools'>) =>
  api.post<Result<'importPools'>>('/pools/import', { body });

export const listPools = (signal?: AbortSignal) =>
  api.get<Result<'listPools'>>('/pools', { signal });

export const getPool = (id: string, signal?: AbortSignal) =>
  api.get<Result<'getPool'>>(`/pools/${enc(id)}`, { signal });

export const createPool = (body: Body<'createPool'>) =>
  api.post<Result<'createPool'>>('/pools', { body });

/**
 * Dry-run a pool definition. Pass the pool's id when this is an edit, so the
 * server does not report the pool's own name as a name that is taken.
 */
export const validatePool = (body: Body<'validatePool'>, id?: string, signal?: AbortSignal) =>
  api.post<Result<'validatePool'>>('/pools/validate', {
    body,
    query: id ? { id } : undefined,
    signal,
  });

/**
 * What a pool that says nothing is. The size a runner gets is the fleet's
 * setting, so the wizard's sliders open on what the server would apply rather
 * than on a figure of the browser's own.
 */
export const getPoolDefaults = (signal?: AbortSignal) =>
  api.get<Result<'getPoolDefaults'>>('/pools/defaults', { signal });

/**
 * The operating systems a pool may ask for. Served rather than hard-coded so
 * the wizard cannot offer one no runner image is published for.
 */
export const listPoolPlatforms = (signal?: AbortSignal) =>
  api.get<Result<'listPoolPlatforms'>>('/pools/platforms', { signal });

export const updatePool = (id: string, body: Body<'updatePool'>, query?: Query<'updatePool'>) =>
  api.patch<Result<'updatePool'>>(`/pools/${enc(id)}`, { body, query });

export const deletePool = (id: string, query?: Query<'deletePool'>) =>
  api.del<Result<'deletePool'>>(`/pools/${enc(id)}`, { query });

export const enablePool = (id: string) =>
  api.post<Result<'enablePool'>>(`/pools/${enc(id)}/enable`, {});

export const disablePool = (id: string) =>
  api.post<Result<'disablePool'>>(`/pools/${enc(id)}/disable`, {});

export const prewarmPool = (id: string) =>
  api.post<Result<'prewarmPool'>>(`/pools/${enc(id)}/prewarm`, {});

/* -- runners -------------------------------------------------------------- */

export const listRunners = (query?: Query<'listRunners'>, signal?: AbortSignal) =>
  api.get<Result<'listRunners'>>('/runners', { query, signal });

export const getRunner = (id: string, signal?: AbortSignal) =>
  api.get<Result<'getRunner'>>(`/runners/${enc(id)}`, { signal });

export const getRunnerTimeline = (id: string, signal?: AbortSignal) =>
  api.get<Result<'getRunnerTimeline'>>(`/runners/${enc(id)}/timeline`, { signal });

export const drainRunner = (id: string, query?: Query<'drainRunner'>) =>
  api.post<Result<'drainRunner'>>(`/runners/${enc(id)}/drain`, { query });

export const deleteRunner = (id: string, query?: Query<'deleteRunner'>) =>
  api.del<Result<'deleteRunner'>>(`/runners/${enc(id)}`, { query });

export const bulkRunners = (body: Body<'bulkRunnerAction'>) =>
  api.post<Result<'bulkRunnerAction'>>('/runners/bulk', { body });

/** The SSE endpoint for a runner's live log tail. Opened by the log viewer. */
export const runnerLogsUrl = (id: string, query?: Query<'streamRunnerLogs'>) =>
  `${BASE}/runners/${enc(id)}/logs${toQuery(query as QueryInput | undefined)}`;

/** A plain-text snapshot, for the download button. */
export const runnerLogsDownloadUrl = (id: string) => `${BASE}/runners/${enc(id)}/logs/download`;

/* -- jobs ----------------------------------------------------------------- */

export const listJobs = (query?: Query<'listJobs'>, signal?: AbortSignal) =>
  api.get<Result<'listJobs'>>('/jobs', { query, signal });

export const getJob = (id: string, signal?: AbortSignal) =>
  api.get<Result<'getJob'>>(`/jobs/${enc(id)}`, { signal });

export const getJobFacets = (signal?: AbortSignal) =>
  api.get<Result<'getJobFacets'>>('/jobs/facets', { signal });

export const listWorkflowRuns = (query?: Query<'listWorkflowRuns'>, signal?: AbortSignal) =>
  api.get<Result<'listWorkflowRuns'>>('/workflow-runs', { query, signal });

export const getJobEvents = (id: string, signal?: AbortSignal) =>
  api.get<Result<'getJobEvents'>>(`/jobs/${enc(id)}/events`, { signal });

export const getJobExplanation = (id: string, signal?: AbortSignal) =>
  api.get<Result<'getJobExplanation'>>(`/jobs/${enc(id)}/explanation`, { signal });

export const cancelJobWorkflow = (id: string, body: Body<'cancelJobWorkflow'>) =>
  api.post<Result<'cancelJobWorkflow'>>(`/jobs/${enc(id)}/cancel`, { body });

export const cancelWorkflowRun = (body: Body<'cancelWorkflowRun'>) =>
  api.post<Result<'cancelWorkflowRun'>>('/workflow-runs/cancel', { body });

export const controlWorkflowRunProvisioning = (body: Body<'controlWorkflowRunProvisioning'>) =>
  api.post<Result<'controlWorkflowRunProvisioning'>>('/workflow-runs/provisioning', { body });

export const rerunJobWorkflow = (id: string) =>
  api.post<Result<'rerunJobWorkflow'>>(`/jobs/${enc(id)}/rerun`, {});

/* -- hosts ---------------------------------------------------------------- */

export const listHosts = (signal?: AbortSignal) =>
  api.get<Result<'listHosts'>>('/hosts', { signal });

export const listHostSamples = (query?: Query<'listHostSamples'>, signal?: AbortSignal) =>
  api.get<Result<'listHostSamples'>>('/hosts/samples', { query, signal });

export const getHost = (id: string, signal?: AbortSignal) =>
  api.get<Result<'getHost'>>(`/hosts/${enc(id)}`, { signal });

export const updateHost = (id: string, body: Body<'updateHost'>, query?: Query<'updateHost'>) =>
  api.patch<Result<'updateHost'>>(`/hosts/${enc(id)}`, { body, query });

export const cordonHost = (id: string, body: Body<'cordonHost'>) =>
  api.post<Result<'cordonHost'>>(`/hosts/${enc(id)}/cordon`, { body });
export const clearHostThrottle = (id: string) =>
  api.post<Result<'clearHostThrottle'>>(`/hosts/${enc(id)}/throttle/clear`);

export const deleteHost = (id: string, query?: Query<'deleteHost'>) =>
  api.del<Result<'deleteHost'>>(`/hosts/${enc(id)}`, { query });

export const listJoinTokens = (signal?: AbortSignal) =>
  api.get<Result<'listJoinTokens'>>('/join-tokens', { signal });

export const createJoinToken = (body: Body<'createJoinToken'>) =>
  api.post<Result<'createJoinToken'>>('/join-tokens', { body });

export const getJoinToken = (id: string, signal?: AbortSignal) =>
  api.get<Result<'getJoinToken'>>(`/join-tokens/${enc(id)}`, { signal });

export const deleteJoinToken = (id: string) =>
  api.del<Result<'deleteJoinToken'>>(`/join-tokens/${enc(id)}`);

/* -- installations -------------------------------------------------------- */

export const listInstallations = (signal?: AbortSignal) =>
  api.get<Result<'listInstallations'>>('/installations', { signal });

export const getInstallation = (id: string, signal?: AbortSignal) =>
  api.get<Result<'getInstallation'>>(`/installations/${enc(id)}`, { signal });

export const createInstallation = (body: Body<'createInstallation'>) =>
  api.post<Result<'createInstallation'>>('/installations', { body });

export const updateInstallation = (id: string, body: Body<'updateInstallation'>) =>
  api.patch<Result<'updateInstallation'>>(`/installations/${enc(id)}`, { body });

export const verifyInstallation = (id: string) =>
  api.post<Result<'verifyInstallation'>>(`/installations/${enc(id)}/verify`, {});

export const deleteInstallation = (id: string) =>
  api.del<Result<'deleteInstallation'>>(`/installations/${enc(id)}`);

export const listRunnerGroups = (id: string, signal?: AbortSignal) =>
  api.get<Result<'listRunnerGroups'>>(`/installations/${enc(id)}/runner-groups`, { signal });

export const getRateLimit = (id: string, signal?: AbortSignal) =>
  api.get<Result<'getRateLimit'>>(`/installations/${enc(id)}/rate-limit`, { signal });

export const createAppManifest = (body: Body<'createAppManifest'>) =>
  api.post<Result<'createAppManifest'>>('/installations/manifest', { body });

export const exchangeAppManifest = (body: Body<'exchangeAppManifest'>) =>
  api.post<Result<'exchangeAppManifest'>>('/installations/manifest/exchange', { body });

/* -- migrations ---------------------------------------------------------- */

/** What moving these repositories onto Zoomies would change. Writes nothing. */
export const planMigration = (body: Body<'planMigration'>, signal?: AbortSignal) =>
  api.post<Result<'planMigration'>>('/migrations/plan', { body, signal });

/** Opens one pull request per repository. The only call that writes to a repo. */
export const openMigrationPullRequests = (body: Body<'openMigrationPullRequests'>) =>
  api.post<Result<'openMigrationPullRequests'>>('/migrations/pull-requests', { body });

export const listWebhookDeliveries = (
  query?: Query<'listWebhookDeliveries'>,
  signal?: AbortSignal,
) => api.get<Result<'listWebhookDeliveries'>>('/webhook-deliveries', { query, signal });

export const testWebhookReachability = () =>
  api.post<Result<'testWebhookReachability'>>('/webhook-test', {});

/* -- audit ---------------------------------------------------------------- */

export const listAudit = (query?: Query<'listAudit'>, signal?: AbortSignal) =>
  api.get<Result<'listAudit'>>('/audit', { query, signal });

export const listAuditActions = (signal?: AbortSignal) =>
  api.get<Result<'listAuditActions'>>('/audit/actions', { signal });

/* -- users, tokens, settings ---------------------------------------------- */

export const listUsers = (signal?: AbortSignal) =>
  api.get<Result<'listUsers'>>('/users', { signal });

export const createUser = (body: Body<'createUser'>) =>
  api.post<Result<'createUser'>>('/users', { body });

export const updateUser = (id: string, body: Body<'updateUser'>) =>
  api.patch<Result<'updateUser'>>(`/users/${enc(id)}`, { body });

export const deleteUser = (id: string) => api.del<Result<'deleteUser'>>(`/users/${enc(id)}`);

export const resetUserPassword = (id: string, body: Body<'resetUserPassword'>) =>
  api.post<Result<'resetUserPassword'>>(`/users/${enc(id)}/password`, { body });

export const resetUserTwoStep = (id: string) =>
  api.del<Result<'resetUserTwoStep'>>(`/users/${enc(id)}/two-step`);

export const listTokens = (signal?: AbortSignal) =>
  api.get<Result<'listTokens'>>('/tokens', { signal });

export const createToken = (body: Body<'createToken'>) =>
  api.post<Result<'createToken'>>('/tokens', { body });

export const revokeToken = (id: string) => api.del<Result<'revokeToken'>>(`/tokens/${enc(id)}`);

/** Remove a token that is already revoked or expired. A live one is a 409. */
export const deleteToken = (id: string) =>
  api.del<Result<'revokeToken'>>(`/tokens/${enc(id)}`, { query: { purge: true } });

/** Whose spent tokens to delete: the caller's own when both are left out. */
export interface PurgeTokensBody {
  user_id?: string;
  all?: boolean;
}

export const purgeTokens = (body: PurgeTokensBody = {}) =>
  api.post<Result<'purgeTokens'>>('/tokens/purge', { body });
/* -- MCP connections and the clients that make them ---------------------------- */

export const getMCPRequest = (id: string, signal?: AbortSignal) =>
  api.get<Result<'getMCPRequest'>>(`/auth/mcp-requests/${enc(id)}`, { signal });

export const approveMCPRequest = (id: string, body: Body<'approveMCPRequest'>) =>
  api.post<Result<'approveMCPRequest'>>(`/auth/mcp-requests/${enc(id)}/approve`, { body });

export const denyMCPRequest = (id: string) =>
  api.post<Result<'denyMCPRequest'>>(`/auth/mcp-requests/${enc(id)}/deny`, {});

export const listOwnMCPConnections = (signal?: AbortSignal) =>
  api.get<Result<'listOwnMCPConnections'>>('/auth/mcp-connections', { signal });

export const revokeOwnMCPConnection = (id: string) =>
  api.del<Result<'revokeOwnMCPConnection'>>(`/auth/mcp-connections/${enc(id)}`);

export const listMCPConnections = (signal?: AbortSignal) =>
  api.get<Result<'listMCPConnections'>>('/mcp-connections', { signal });

export const revokeMCPConnection = (id: string) =>
  api.del<Result<'revokeMCPConnection'>>(`/mcp-connections/${enc(id)}`);

export const listMCPClients = (signal?: AbortSignal) =>
  api.get<Result<'listMCPClients'>>('/mcp-clients', { signal });

export const createMCPClient = (body: Body<'createMCPClient'>) =>
  api.post<Result<'createMCPClient'>>('/mcp-clients', { body });

export const rotateMCPClientSecret = (id: string) =>
  api.post<Result<'rotateMCPClientSecret'>>(`/mcp-clients/${enc(id)}/secret`, {});

export const revokeMCPClient = (id: string) =>
  api.del<Result<'revokeMCPClient'>>(`/mcp-clients/${enc(id)}`);

/* -- providers and machines ------------------------------------------------
 * "Provider" is the infrastructure a machine is rented from, and "machine" is
 * the thing rented. Neither is `listProvisioning` below, which is the queued
 * job demand queue -- the two words look alike and mean nothing like each
 * other, so nothing here is spelled "provisioning".
 * ------------------------------------------------------------------------ */

export const listProviders = (signal?: AbortSignal) =>
  api.get<Result<'listProviders'>>('/providers', { signal });

/** What each driver can do, and the settings schema its form renders from. */
export const listProviderKinds = (signal?: AbortSignal) =>
  api.get<Result<'listProviderKinds'>>('/providers/kinds', { signal });

export const getProvider = (id: string, signal?: AbortSignal) =>
  api.get<Result<'getProvider'>>(`/providers/${enc(id)}`, { signal });

export const createProvider = (body: Body<'createProvider'>) =>
  api.post<Result<'createProvider'>>('/providers', { body });

export const updateProvider = (id: string, body: Body<'updateProvider'>) =>
  api.patch<Result<'updateProvider'>>(`/providers/${enc(id)}`, { body });

export const deleteProvider = (id: string) =>
  api.del<Result<'deleteProvider'>>(`/providers/${enc(id)}`);

/**
 * A dry run over a draft. Always answers 200: whether the draft is usable is
 * in the body, so the form can show a verdict beside the setting that changes
 * it rather than waiting for a failed save.
 */
export const validateProvider = (
  body: Body<'validateProvider'>,
  id?: string,
  signal?: AbortSignal,
) =>
  api.post<Result<'validateProvider'>>('/providers/validate', {
    body,
    query: id ? { id } : undefined,
    signal,
  });

/** The live preflight. Read-only: it changes nothing at the provider. */
export const checkProvider = (id: string) =>
  api.post<Result<'checkProvider'>>(`/providers/${enc(id)}/check`);

export const getProviderDiscovery = (id: string, signal?: AbortSignal) =>
  api.get<Result<'getProviderDiscovery'>>(`/providers/${enc(id)}/discovery`, { signal });

/**
 * What a draft's credential can see, before the draft is saved. A hypervisor
 * that does not answer is a 200 with the reason in `unavailable`, because
 * halfway through the wizard that is the expected state.
 */
export const discoverProviderDraft = (body: Body<'discoverProviderDraft'>, signal?: AbortSignal) =>
  api.post<Result<'discoverProviderDraft'>>('/providers/discover', { body, signal });

export const getProviderOrphans = (id: string, signal?: AbortSignal) =>
  api.get<Result<'getProviderOrphans'>>(`/providers/${enc(id)}/orphans`, { signal });

export const pauseProvider = (id: string, body?: OptionalBody<'pauseProvider'>) =>
  api.post<Result<'pauseProvider'>>(`/providers/${enc(id)}/pause`, { body });

export const resumeProvider = (id: string) =>
  api.post<Result<'resumeProvider'>>(`/providers/${enc(id)}/resume`);

export const listMachines = (query?: Query<'listMachines'>, signal?: AbortSignal) =>
  api.get<Result<'listMachines'>>('/machines', { query, signal });

export const getMachine = (id: string, signal?: AbortSignal) =>
  api.get<Result<'getMachine'>>(`/machines/${enc(id)}`, { signal });

export const drainMachine = (id: string) =>
  api.post<Result<'drainMachine'>>(`/machines/${enc(id)}/drain`);

export const deleteMachine = (id: string, query?: Query<'deleteMachine'>) =>
  api.del<Result<'deleteMachine'>>(`/machines/${enc(id)}`, { query });

/** Forget a quarantined row without touching the resource behind it. */
export const releaseMachine = (id: string, body: Body<'releaseMachine'>) =>
  api.post<Result<'releaseMachine'>>(`/machines/${enc(id)}/release`, { body });

/* -- usage ---------------------------------------------------------------- */

export const getUsage = (query: Query<'getUsage'>, signal?: AbortSignal) =>
  api.get<Result<'getUsage'>>('/usage', { query, signal });

export const getInstallationReport = (
  id: string,
  query?: Query<'getInstallationReport'>,
  signal?: AbortSignal,
) =>
  api.get<Result<'getInstallationReport'>>(`/installations/${enc(id)}/report`, { query, signal });

/**
 * Where the browser goes for the CSV. A full navigation rather than a fetch,
 * because the point is the browser's own download, not a string in memory.
 */
export const usageCsvUrl = (query: Query<'getUsage'>) =>
  `${BASE}/usage.csv${toQuery(query as QueryInput)}`;

/* -- settings ------------------------------------------------------------- */

export const getSettings = (signal?: AbortSignal) =>
  api.get<Result<'getSettings'>>('/settings', { signal });

export const updateSettings = (body: Body<'updateSettings'>) =>
  api.patch<Result<'updateSettings'>>('/settings', { body });

/**
 * Where the browser goes for a settings export. A navigation rather than a
 * fetch, because the point is the browser's own download.
 */
export const settingsExportUrl = (format: 'json' | 'yaml') =>
  `${BASE}/settings/export${toQuery({ format })}`;

export const importSettings = (body: Body<'importSettings'>) =>
  api.post<Result<'importSettings'>>('/settings/import', { body });

/* -- backups -------------------------------------------------------------- */

export const listBackups = (signal?: AbortSignal) =>
  api.get<Result<'listBackups'>>('/backups', { signal });

export const takeBackup = () => api.post<Result<'takeBackup'>>('/backups', {});

export const getBackup = (id: string, signal?: AbortSignal) =>
  api.get<Result<'getBackup'>>(`/backups/${enc(id)}`, { signal });

export const deleteBackup = (id: string) => api.del<Result<'deleteBackup'>>(`/backups/${enc(id)}`);

export const verifyBackup = (id: string) =>
  api.post<Result<'verifyBackup'>>(`/backups/${enc(id)}/verify`, {});

/** The plain archive, for the download button: a navigation, not a fetch. */
export const backupDownloadUrl = (id: string) => `${BASE}/backups/${enc(id)}/download`;

export const stageRestore = (id: string, body: OptionalBody<'stageRestore'>) =>
  api.post<Result<'stageRestore'>>(`/backups/${enc(id)}/restore`, { body });

export const cancelRestore = () => api.del<Result<'cancelRestore'>>('/backups/restore');

export const applyRestore = () => api.post<Result<'applyRestore'>>('/backups/restore/apply', {});

export const dismissRestoreOutcome = () =>
  api.del<Result<'dismissRestoreOutcome'>>('/backups/restore/outcome');

/* -- the copies that leave the machine ------------------------------------- */

export const shipBackups = () => api.post<Result<'shipBackups'>>('/backups/offsite', {});

/** Retention now, rather than at the next backup. */
export const pruneBackups = () => api.post<Result<'pruneBackups'>>('/backups/prune', {});

export const createBackupRemote = (body: Body<'createBackupRemote'>) =>
  api.post<Result<'createBackupRemote'>>('/backups/remotes', { body });

export const updateBackupRemote = (name: string, body: Body<'updateBackupRemote'>) =>
  api.patch<Result<'updateBackupRemote'>>(`/backups/remotes/${enc(name)}`, { body });

export const deleteBackupRemote = (name: string) =>
  api.del<Result<'deleteBackupRemote'>>(`/backups/remotes/${enc(name)}`);

/** Test a destination that has not been saved, which is the point of it. */
export const checkDraftBackupRemote = (body: Body<'checkDraftBackupRemote'>) =>
  api.post<Result<'checkDraftBackupRemote'>>('/backups/remotes/check', { body });

export const listRemoteBackups = (name: string, signal?: AbortSignal) =>
  api.get<Result<'listRemoteBackups'>>(`/backups/remotes/${enc(name)}/copies`, { signal });

export const checkBackupRemote = (name: string) =>
  api.post<Result<'checkBackupRemote'>>(`/backups/remotes/${enc(name)}/check`, {});

export const fetchRemoteBackup = (
  name: string,
  id: string,
  body: OptionalBody<'fetchRemoteBackup'>,
) =>
  api.post<Result<'fetchRemoteBackup'>>(`/backups/remotes/${enc(name)}/copies/${enc(id)}/fetch`, {
    body,
  });

export const deleteRemoteBackup = (name: string, id: string) =>
  api.del<Result<'deleteRemoteBackup'>>(`/backups/remotes/${enc(name)}/copies/${enc(id)}`);

/**
 * The encrypted archive. A fetch rather than a navigation because the
 * passphrase travels in a JSON body, where a proxy log does not see it, and a
 * navigation cannot carry one. The whole file lands in memory before the
 * browser is handed it, which is the price of that.
 */
export async function downloadBackupEncrypted(id: string, passphrase: string): Promise<Blob> {
  let response: Response;
  try {
    response = await fetch(`${BASE}/backups/${enc(id)}/download`, {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', Accept: 'application/octet-stream' },
      body: JSON.stringify({ passphrase }),
    });
  } catch (cause) {
    throw new ApiError({
      status: 0,
      code: 'internal',
      message: 'Could not reach the Zoomies API. Check that the controller is still running.',
      detail: cause instanceof Error ? cause.message : undefined,
    });
  }
  if (!response.ok) {
    if (response.status === 401) unauthorized?.();
    const payload: unknown = await response.json().catch(() => undefined);
    throw new ApiError(errorFrom(response.status, payload));
  }
  return response.blob();
}

/**
 * Upload an archive. XMLHttpRequest rather than fetch, because a backup is
 * the size of the database and the one thing an operator wants from a
 * multi-minute upload is to see it moving: fetch reports nothing until the
 * response, and XHR reports every chunk sent.
 */
export function uploadBackup(
  file: File,
  passphrase: string,
  onProgress?: (fraction: number) => void,
): Promise<Result<'uploadBackup'>> {
  const form = new FormData();
  // The passphrase first, so the server can read it before spooling the
  // file; it handles either order, but this is the cheaper one.
  if (passphrase) form.append('passphrase', passphrase);
  form.append('file', file, file.name);
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('POST', `${BASE}/backups/upload`);
    xhr.withCredentials = true;
    xhr.setRequestHeader('Accept', 'application/json');
    xhr.responseType = 'json';
    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable && onProgress) onProgress(event.loaded / event.total);
    };
    xhr.onerror = () =>
      reject(
        new ApiError({
          status: 0,
          code: 'internal',
          message: 'Could not reach the Zoomies API. Check that the controller is still running.',
        }),
      );
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(xhr.response as Result<'uploadBackup'>);
        return;
      }
      if (xhr.status === 401) unauthorized?.();
      reject(new ApiError(errorFrom(xhr.status, xhr.response)));
    };
    xhr.send(form);
  });
}

/** The base path, for the two endpoints the browser navigates to directly. */
export const apiBase = BASE;

export const listProvisioning = (query?: Query<'listProvisioning'>, signal?: AbortSignal) =>
  api.get<Result<'listProvisioning'>>('/provisioning', { query, signal });
export const selectProvisioning = (query?: Query<'selectProvisioning'>, signal?: AbortSignal) =>
  api.get<Result<'selectProvisioning'>>('/provisioning/selection', { query, signal });
export const controlProvisioning = (body: Body<'controlProvisioning'>) =>
  api.post<Result<'controlProvisioning'>>('/provisioning/bulk', { body });
