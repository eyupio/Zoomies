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
import type { Body, ErrorCode, FieldError, Query, Result } from './types';

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

type Method = 'GET' | 'POST' | 'PATCH' | 'DELETE';

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
      return `The server returned ${status}. Check the controller logs for the cause.`;
  }
}

/** The low-level verbs. Prefer the named helpers below; these are the escape hatch. */
export const api = {
  get: <T>(path: string, options?: RequestOptions) => request<T>('GET', path, options),
  post: <T>(path: string, options?: RequestOptions) => request<T>('POST', path, options),
  patch: <T>(path: string, options?: RequestOptions) => request<T>('PATCH', path, options),
  del: <T>(path: string, options?: RequestOptions) => request<T>('DELETE', path, options),
};

const enc = encodeURIComponent;

/* -- meta and authentication --------------------------------------------- */

export const getMeta = (signal?: AbortSignal) =>
  api.get<Result<'getMeta'>>('/meta', { signal, allow401: true });

export const getSession = (signal?: AbortSignal) =>
  api.get<Result<'getSession'>>('/auth/session', { signal, allow401: true });

export const login = (body: Body<'login'>) =>
  api.post<Result<'login'>>('/auth/login', { body, allow401: true });

export const logout = () => api.post<Result<'logout'>>('/auth/logout', {});

export const bootstrap = (body: Body<'bootstrap'>) =>
  api.post<Result<'bootstrap'>>('/auth/bootstrap', { body, allow401: true });

export const changeOwnPassword = (body: Body<'changeOwnPassword'>) =>
  api.post<Result<'changeOwnPassword'>>('/auth/password', { body });

/** Where the browser goes to start the OIDC flow. A full navigation, not fetch. */
export const oidcStartUrl = () => `${BASE}/auth/oidc/start`;

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

export const listPools = (signal?: AbortSignal) =>
  api.get<Result<'listPools'>>('/pools', { signal });

export const getPool = (id: string, signal?: AbortSignal) =>
  api.get<Result<'getPool'>>(`/pools/${enc(id)}`, { signal });

export const createPool = (body: Body<'createPool'>) =>
  api.post<Result<'createPool'>>('/pools', { body });

export const validatePool = (body: Body<'validatePool'>, signal?: AbortSignal) =>
  api.post<Result<'validatePool'>>('/pools/validate', { body, signal });

export const updatePool = (id: string, body: Body<'updatePool'>) =>
  api.patch<Result<'updatePool'>>(`/pools/${enc(id)}`, { body });

export const deletePool = (id: string, query?: Query<'deletePool'>) =>
  api.del<Result<'deletePool'>>(`/pools/${enc(id)}`, { query });

export const enablePool = (id: string) =>
  api.post<Result<'enablePool'>>(`/pools/${enc(id)}/enable`, {});

export const disablePool = (id: string) =>
  api.post<Result<'disablePool'>>(`/pools/${enc(id)}/disable`, {});

/* -- runners -------------------------------------------------------------- */

export const listRunners = (query?: Query<'listRunners'>, signal?: AbortSignal) =>
  api.get<Result<'listRunners'>>('/runners', { query, signal });

export const getRunner = (id: string, signal?: AbortSignal) =>
  api.get<Result<'getRunner'>>(`/runners/${enc(id)}`, { signal });

export const getRunnerTimeline = (id: string, signal?: AbortSignal) =>
  api.get<Result<'getRunnerTimeline'>>(`/runners/${enc(id)}/timeline`, { signal });

export const drainRunner = (id: string) =>
  api.post<Result<'drainRunner'>>(`/runners/${enc(id)}/drain`, {});

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

/* -- hosts ---------------------------------------------------------------- */

export const listHosts = (signal?: AbortSignal) =>
  api.get<Result<'listHosts'>>('/hosts', { signal });

export const getHost = (id: string, signal?: AbortSignal) =>
  api.get<Result<'getHost'>>(`/hosts/${enc(id)}`, { signal });

export const updateHost = (id: string, body: Body<'updateHost'>) =>
  api.patch<Result<'updateHost'>>(`/hosts/${enc(id)}`, { body });

export const cordonHost = (id: string, body: Body<'cordonHost'>) =>
  api.post<Result<'cordonHost'>>(`/hosts/${enc(id)}/cordon`, { body });

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

export const listTokens = (signal?: AbortSignal) =>
  api.get<Result<'listTokens'>>('/tokens', { signal });

export const createToken = (body: Body<'createToken'>) =>
  api.post<Result<'createToken'>>('/tokens', { body });

export const revokeToken = (id: string) => api.del<Result<'revokeToken'>>(`/tokens/${enc(id)}`);

export const getSettings = (signal?: AbortSignal) =>
  api.get<Result<'getSettings'>>('/settings', { signal });

export const updateSettings = (body: Body<'updateSettings'>) =>
  api.patch<Result<'updateSettings'>>('/settings', { body });

/** The base path, for the two endpoints the browser navigates to directly. */
export const apiBase = BASE;
