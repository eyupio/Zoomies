/**
 * The live fleet cache.
 *
 * Every page reads from here rather than polling. SSE frames are applied to the
 * collections; a reconnect triggers one authoritative fetch so a replay gap
 * cannot leave the screen wrong.
 *
 * PERFORMANCE, and the reason this file looks the way it does:
 *
 * The rows live in plain `Map`s inside a single `$state.raw`. They are NOT deep
 * reactive state. A busy fleet has thousands of runners, and wrapping each of
 * them in a reactive proxy costs a proxy allocation per row plus a dependency
 * per field read -- which is exactly the shape of jank that makes a dashboard
 * feel slow on a second monitor. Instead the Maps are mutated in place and a
 * version counter is bumped inside a fresh container object; because the
 * container is `$state.raw`, reassigning it invalidates every reader once.
 * Coarse invalidation of a few hundred visible rows is far cheaper than fine
 * invalidation of several thousand invisible ones.
 *
 * Bumps are coalesced on an animation frame, so a burst of forty SSE frames
 * repaints once rather than forty times.
 *
 * Two counters come out of a bump. `version` moves on every change and is what
 * a component reading a row depends on. `shape` moves only when something
 * other than a live metric changed -- a runner appearing, changing state, pool,
 * host or job; a host flipping health -- and is what a server-side grid
 * refetches on. A runner's heartbeat updates its CPU and memory every thirty
 * seconds, and on a fleet of fifty runners that is a frame every second or so;
 * a grid that refetched on each of those would be a round trip per heartbeat,
 * for two columns it can read from this cache instead.
 */
import {
  ApiError,
  listHosts,
  listPools,
  listProblems,
  listRunners,
  listScalingEvents,
  getStats,
} from '../api/client';
import { events, type SseStatus } from '../api/sse';
import type { Host, Pool, Problem, Runner, ScalingEvent, Stats } from '../api/types';
import { toasts } from './toasts.svelte';

interface FleetData {
  version: number;
  shape: number;
  pools: Map<string, Pool>;
  runners: Map<string, Runner>;
  hosts: Map<string, Host>;
}

function emptyData(): FleetData {
  return { version: 0, shape: 0, pools: new Map(), runners: new Map(), hosts: new Map() };
}

/**
 * The fields a frame may change without the fleet's shape having changed: the
 * live metrics an agent reports on every heartbeat.
 */
const VOLATILE = new Set(['cpu_percent', 'memory_bytes', 'last_heartbeat']);

/** True when two rows differ in anything but a live metric. */
function shapeDiffers(a: Record<string, unknown> | undefined, b: Record<string, unknown>): boolean {
  if (!a) return true;
  for (const key of new Set([...Object.keys(a), ...Object.keys(b)])) {
    if (VOLATILE.has(key)) continue;
    if (JSON.stringify(a[key]) !== JSON.stringify(b[key])) return true;
  }
  return false;
}

/** How many scaling decisions the Overview keeps in memory. */
const SCALING_LIMIT = 50;
/** The API's maximum page size; enough to warm the cache and the palette. */
const RUNNER_PAGE = 500;

class Fleet {
  #data = $state.raw<FleetData>(emptyData());
  // These are small, so ordinary reactive state is the right trade here.
  #stats = $state<Stats | null>(null);
  #problems = $state<Problem[]>([]);
  #problemsOk = $state(true);
  #scaling = $state<ScalingEvent[]>([]);
  #connection = $state<SseStatus>('offline');
  #loaded = $state(false);
  #loading = $state(false);
  #error = $state<ApiError | null>(null);

  #frame: number | null = null;
  #unsubscribers: Array<() => void> = [];
  #started = false;
  #reconciling: Promise<void> | null = null;
  /** Set by a mutation that changed more than a live metric; read by #touch. */
  #shapeChanged = false;
  /**
   * Frames that arrived while a reconcile was in flight. They are newer than
   * the responses the reconcile is waiting on, so they are applied on top of
   * the new Maps once those land rather than to the old Maps the reconcile
   * is about to throw away.
   */
  #deferred: Array<() => void> = [];

  /* -- reads -------------------------------------------------------------- */

  /** The SSE connection state. The top bar renders this and never hides it. */
  get connection(): SseStatus {
    return this.#connection;
  }
  /** True once the first authoritative fetch has landed. */
  get loaded(): boolean {
    return this.#loaded;
  }
  get loading(): boolean {
    return this.#loading;
  }
  get error(): ApiError | null {
    return this.#error;
  }
  get stats(): Stats | null {
    return this.#stats;
  }
  get problems(): readonly Problem[] {
    return this.#problems;
  }
  /** True when there is genuinely nothing wrong, which is the common case. */
  get problemsOk(): boolean {
    return this.#problemsOk;
  }
  get scalingEvents(): readonly ScalingEvent[] {
    return this.#scaling;
  }
  /** Bumped once per applied change. Useful as a `{#key}` or effect dependency. */
  get version(): number {
    return this.#data.version;
  }
  /**
   * Bumped only when something other than a live metric changed. A grid that
   * fetches its rows from the server refetches on this, not on `version`.
   */
  get shape(): number {
    return this.#data.shape;
  }

  get pools(): Pool[] {
    return [...this.#data.pools.values()];
  }
  get runners(): Runner[] {
    return [...this.#data.runners.values()];
  }
  get hosts(): Host[] {
    return [...this.#data.hosts.values()];
  }

  pool(id: string | undefined): Pool | undefined {
    return id ? this.#data.pools.get(id) : undefined;
  }
  runner(id: string | undefined): Runner | undefined {
    return id ? this.#data.runners.get(id) : undefined;
  }
  host(id: string | undefined): Host | undefined {
    return id ? this.#data.hosts.get(id) : undefined;
  }

  /** Live runners belonging to one pool. Used by the pool detail page. */
  runnersInPool(poolId: string): Runner[] {
    return this.runners.filter((r) => r.pool_id === poolId);
  }

  /* -- lifecycle ----------------------------------------------------------- */

  /** Connect the stream and load the first snapshot. Called once, after sign-in. */
  start(): void {
    if (this.#started) return;
    this.#started = true;

    this.#unsubscribers.push(
      events.onStatus((status) => {
        const wasBroken = this.#connection !== 'live';
        this.#connection = status;
        // Every reconnect ends with one reconciling fetch, because the replay
        // buffer is finite and we cannot know whether it covered the gap. A
        // first load that failed is retried the same way: the controller that
        // was restarting under it is back, and there is no refresh button.
        const stale = this.#loaded || this.#error !== null;
        if (status === 'live' && wasBroken && stale) void this.reconcile();
      }),
    );

    // Every handler goes through #live so that a frame landing mid-reconcile
    // waits for the new Maps instead of being applied to the old ones.
    this.#unsubscribers.push(
      events.subscribe(['runner.created', 'runner.updated'], (runner) =>
        this.#live(() => {
          if (!runner.id) return;
          // A removed runner is gone from the API's own listing, which is
          // what a reconcile replaces this cache with, so keeping it here
          // would make the two disagree until the next reconnect. On a busy
          // fleet every ephemeral runner ends this way, so the Map would
          // otherwise grow without bound between reconnects, and the
          // palette's first few hundred entries would all be runners that no
          // longer exist.
          if (runner.state === 'removed') this.#delete(this.#data.runners, runner.id);
          else this.#set(this.#data.runners, runner.id, runner);
        }),
      ),
      events.subscribe('runner.deleted', ({ id }) =>
        this.#live(() => this.#delete(this.#data.runners, id)),
      ),
      events.subscribe(['pool.created', 'pool.updated'], (pool) =>
        this.#live(() => {
          if (pool.id) this.#set(this.#data.pools, pool.id, pool);
        }),
      ),
      events.subscribe('pool.deleted', ({ id }) =>
        this.#live(() => this.#delete(this.#data.pools, id)),
      ),
      events.subscribe('host.updated', (host) =>
        this.#live(() => {
          if (host.id) this.#set(this.#data.hosts, host.id, host);
        }),
      ),
      events.subscribe('host.deleted', ({ id }) =>
        this.#live(() => this.#delete(this.#data.hosts, id)),
      ),
      events.subscribe('stats', (stats) =>
        this.#live(() => {
          this.#stats = stats;
        }),
      ),
      events.subscribe('problems.updated', (payload) =>
        this.#live(() => {
          this.#problems = payload.items ?? [];
          this.#problemsOk = payload.ok !== false;
        }),
      ),
      events.subscribe('scaling', (event) =>
        this.#live(() => {
          this.#scaling = [event, ...this.#scaling].slice(0, SCALING_LIMIT);
        }),
      ),
    );

    events.start();
    void this.reconcile();
  }

  /** Disconnect and forget everything. Called on sign-out. */
  stop(): void {
    for (const off of this.#unsubscribers) off();
    this.#unsubscribers = [];
    this.#started = false;
    events.stop();
    this.#data = emptyData();
    this.#stats = null;
    this.#problems = [];
    this.#scaling = [];
    this.#loaded = false;
    this.#error = null;
    this.#deferred = [];
  }

  /**
   * One authoritative fetch. Called at start-up and after every reconnect;
   * concurrent callers share the in-flight promise rather than stampeding.
   *
   * Frames that arrive while it runs are held and replayed once the new Maps
   * are in place, because they are newer than anything the fetch returns: a
   * reconcile runs on every reconnect, which is exactly when the server's
   * replay burst lands, and applying that burst to Maps about to be replaced
   * lost it, while a fetched `stats` overwrote a fresher frame the server
   * would not send again until its numbers changed.
   */
  reconcile(): Promise<void> {
    if (this.#reconciling) return this.#reconciling;
    this.#loading = true;
    this.#reconciling = this.#fetchAll().finally(() => {
      this.#loading = false;
      this.#reconciling = null;
      // Whatever landed after #fetchAll's own replay and before this point
      // is applied now, to whichever Maps are current.
      this.#flushDeferred();
    });
    return this.#reconciling;
  }

  async #fetchAll(): Promise<void> {
    try {
      const [pools, hosts, runners, stats, problems, scaling] = await Promise.all([
        listPools(),
        listHosts(),
        listRunners({ limit: RUNNER_PAGE }),
        getStats(),
        listProblems(),
        listScalingEvents({ limit: SCALING_LIMIT }),
      ]);
      const next = emptyData();
      next.version = this.#data.version + 1;
      next.shape = this.#data.shape + 1;
      for (const pool of pools.items ?? []) if (pool.id) next.pools.set(pool.id, pool);
      for (const host of hosts.items ?? []) if (host.id) next.hosts.set(host.id, host);
      for (const runner of runners.items ?? []) if (runner.id) next.runners.set(runner.id, runner);
      this.#data = next;
      this.#stats = stats;
      this.#problems = problems.items;
      this.#problemsOk = problems.ok;
      this.#scaling = scaling.items ?? [];
      this.#loaded = true;
      this.#error = null;
      // The frames that arrived meanwhile, on top of the fresh snapshot, so
      // the first paint after a reconnect already shows them.
      this.#flushDeferred();
    } catch (cause) {
      // A failed reconcile is a UI state, not an exception: the stream may well
      // recover on its own, and the page keeps showing what it last knew.
      this.#error =
        cause instanceof ApiError
          ? cause
          : new ApiError({
              status: 0,
              code: 'internal',
              message: cause instanceof Error ? cause.message : 'The fleet could not be loaded.',
            });
    }
  }

  /* -- writes from pages ---------------------------------------------------- */

  /**
   * Warm the cache with rows a grid has already fetched, so the command palette
   * can find a runner the operator is looking at without a second request.
   */
  ingestRunners(rows: readonly Runner[]): void {
    for (const runner of rows) if (runner.id) this.#set(this.#data.runners, runner.id, runner);
  }

  ingestPools(rows: readonly Pool[]): void {
    for (const pool of rows) if (pool.id) this.#set(this.#data.pools, pool.id, pool);
  }

  ingestHosts(rows: readonly Host[]): void {
    for (const host of rows) if (host.id) this.#set(this.#data.hosts, host.id, host);
  }

  /**
   * Apply a change locally, then send it.
   *
   * The badge flips immediately; if the server refuses -- a 409 because the
   * runner is already terminal, say -- the row goes back and a toast explains
   * in the server's own words. The eventual truth arrives over SSE and needs no
   * toast of its own.
   *
   * Returns the request's result, or `undefined` when it failed. Failure is
   * reported to the operator here, so callers do not need to catch.
   */
  async optimistic<T>(
    id: string,
    patch: Partial<Runner> | Partial<Pool> | Partial<Host>,
    run: () => Promise<T>,
    failureTitle = 'That change was not applied',
  ): Promise<T | undefined> {
    const map = this.#mapFor(id);
    const previous = map?.get(id);
    if (map && previous) {
      map.set(id, { ...previous, ...patch });
      this.#touch();
    }
    try {
      return await run();
    } catch (cause) {
      if (map && previous) {
        map.set(id, previous);
        this.#touch();
      }
      toasts.fromError(cause, failureTitle);
      return undefined;
    }
  }

  /* -- internals ------------------------------------------------------------ */

  /** Run a frame's mutation now, or hold it until the reconcile in flight lands. */
  #live(apply: () => void): void {
    if (this.#reconciling) this.#deferred.push(apply);
    else apply();
  }

  #flushDeferred(): void {
    // A frame arriving during the flush is appended and taken in turn.
    while (this.#deferred.length > 0) this.#deferred.shift()?.();
  }

  /** Write a row, noting whether the fleet's shape moved with it. */
  #set<T extends Record<string, unknown>>(map: Map<string, T>, id: string, next: T): void {
    if (shapeDiffers(map.get(id), next)) this.#shapeChanged = true;
    map.set(id, next);
    this.#touch();
  }

  #delete<T>(map: Map<string, T>, id: string): void {
    if (map.delete(id)) this.#shapeChanged = true;
    this.#touch();
  }

  #mapFor(id: string): Map<string, Record<string, unknown>> | undefined {
    const data = this.#data as unknown as {
      runners: Map<string, Record<string, unknown>>;
      pools: Map<string, Record<string, unknown>>;
      hosts: Map<string, Record<string, unknown>>;
    };
    if (data.runners.has(id)) return data.runners;
    if (data.pools.has(id)) return data.pools;
    if (data.hosts.has(id)) return data.hosts;
    return undefined;
  }

  /**
   * Publish the mutations made to the Maps. Coalesced on a frame so a burst of
   * SSE frames costs one render, and falls back to a microtask where there is
   * no animation frame (a hidden tab, a test runner).
   */
  #touch(): void {
    if (this.#frame !== null) return;
    const publish = () => {
      this.#frame = null;
      const shape = this.#shapeChanged ? this.#data.shape + 1 : this.#data.shape;
      this.#shapeChanged = false;
      this.#data = { ...this.#data, version: this.#data.version + 1, shape };
    };
    if (typeof requestAnimationFrame === 'function') {
      this.#frame = requestAnimationFrame(publish);
    } else {
      this.#frame = 1;
      queueMicrotask(publish);
    }
  }
}

export const fleet = new Fleet();
