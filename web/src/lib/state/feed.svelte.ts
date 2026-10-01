/**
 * The Overview's feed: what has happened to this fleet lately.
 *
 * It is the one place in the product that answers "what did I miss?" without
 * the operator having to guess which page to open. The panel that used to be
 * here showed the scheduler's decisions alone, which meant a host that went
 * quiet at 03:00, a machine that never became one, and a job whose runner died
 * under it were all found by going and looking.
 *
 * Three things feed it, and they are different on purpose:
 *
 *  * **Derived** from the live fleet cache -- the scheduler's decisions and the
 *    runners that failed. Both are already fetched and already kept live there,
 *    so deriving costs nothing and a reconnect's reconcile repairs them for
 *    free.
 *  * **Fetched once**, when a category that has a past is first shown: the
 *    recent failed jobs, and the audit log. Without this a tab opened at nine
 *    in the morning claims nothing has happened since the controller started.
 *  * **Captured** from the event stream for everything else, because hosts,
 *    machines, pools, installations and deliveries have no history endpoint
 *    shaped like a feed. Those categories say so on the settings page rather
 *    than looking broken.
 *
 * Entry ids are stable for the *fact* rather than the frame, so a fetch and
 * the replay it overlaps with produce one line rather than two.
 *
 * What is captured is bounded (`CAPACITY`) and lives only in this tab: the
 * feed is a window on the fleet, not a log, and the pages it links to are
 * where the whole of anything is.
 */
import { listAudit, listJobs } from '../api/client';
import { events } from '../api/sse';
import type { Host, Job, MachineState, Pool, Provider, Runner, RunnerState } from '../api/types';
import { toMillis } from '../format';
import {
  FEED_CATEGORIES,
  feedCategory,
  feedIsFiltered,
  type FeedCategory,
  type FeedCategoryID,
} from '../feed/categories';
import {
  cpuChange,
  hostChanges,
  hostSignal,
  installationChange,
  jobNews,
  machineChange,
  newProblems,
  providerChanges,
  providerSignal,
  runnerMilestone,
  type HostSignal,
  type ProviderSignal,
} from '../feed/changes';
import {
  auditEntry,
  cpuEntry,
  deliveryEntry,
  hostEntry,
  hostRemovedEntry,
  installationEntry,
  jobEntry,
  machineEntry,
  now,
  poolEntry,
  poolRemovedEntry,
  problemEntry,
  providerEntry,
  runnerFailureEntry,
  runnerMilestoneEntry,
  scalingEntry,
  type FeedEntry,
} from '../feed/entries';
import { fleet } from './fleet.svelte';
import { prefs } from './prefs.svelte';

/** How much of the recent past one tab holds. Well past what any panel shows. */
const CAPACITY = 200;

/** How far back the two fetched categories reach when they are first shown. */
const SEED_LIMIT = 30;

export interface FeedCategoryView {
  category: FeedCategory;
  on: boolean;
}

class Feed {
  #captured = $state.raw<FeedEntry[]>([]);
  #started = false;
  #unsubscribers: Array<() => void> = [];
  #stopWatching: (() => void) | null = null;

  /** What each resource last looked like, so a frame can be compared to it. */
  #hosts = new Map<string, HostSignal>();
  #runners = new Map<string, RunnerState>();
  #cpu = new Map<string, string>();
  #machines = new Map<string, MachineState>();
  #providers = new Map<string, ProviderSignal>();
  #installations = new Map<string, boolean>();
  #problems = new Set<string>();
  /** Names kept for the one frame that cannot carry them: a delete. */
  #names = new Map<string, string>();
  /** What has already been fetched, so a past is fetched once per mode. */
  #seeded = new Set<string>();
  /** The one request behind both job categories, per mode. */
  #jobFetches = new Map<string, Promise<Job[]>>();

  /* -- reads -------------------------------------------------------------- */

  /**
   * Everything this tab has to show, newest first, in the categories this
   * browser has left on.
   */
  get entries(): readonly FeedEntry[] {
    // A job no runner of this fleet ran is kept and hidden rather than
    // dropped, because the switch that asks for it is on the page beside this
    // panel and flipping it should not need a round trip.
    const others = prefs.otherRunners;
    const wanted = this.#captured.filter(
      (entry) => this.shows(entry.category) && (others || !entry.elsewhere),
    );
    if (this.shows('scaling')) {
      wanted.push(...fleet.scalingEvents.map((event, index) => scalingEntry(event, index)));
    }
    if (this.shows('runners')) {
      for (const runner of fleet.runners) {
        const entry = runnerFailureEntry(runner);
        if (entry) wanted.push(entry);
      }
    }
    return wanted.sort((a, b) => (toMillis(b.at) ?? 0) - (toMillis(a.at) ?? 0)).slice(0, CAPACITY);
  }

  /** The categories with the answer for this browser, in their listed order. */
  get categories(): readonly FeedCategoryView[] {
    return FEED_CATEGORIES.map((category) => ({ category, on: this.shows(category.id) }));
  }

  /** Whether this browser shows a category: its choice, or the default. */
  shows(id: FeedCategoryID): boolean {
    return prefs.feedChoice(id) ?? feedCategory(id)?.on ?? false;
  }

  /** How many categories are off, for the sentence the panel shows. */
  get hidden(): number {
    return FEED_CATEGORIES.filter((category) => !this.shows(category.id)).length;
  }

  /**
   * Whether this browser has narrowed the feed itself, rather than showing what
   * it opens with. `hidden` cannot say: two kinds are off by default, so it is
   * never zero on a browser nobody has touched.
   */
  get filtered(): boolean {
    return feedIsFiltered((id) => prefs.feedChoice(id));
  }

  /* -- writes -------------------------------------------------------------- */

  /**
   * Show or hide a category. Turning one on fetches its past if it has one and
   * this tab has not already: an operator who has just asked for job failures
   * should not have to wait for the next one to see the switch worked.
   */
  setShown(id: FeedCategoryID, on: boolean): void {
    prefs.setFeedChoice(id, on);
    if (on && this.#started) void this.#seed(id);
  }

  /* -- lifecycle ----------------------------------------------------------- */

  /** Subscribe and seed. Called once, after sign-in, beside the fleet cache. */
  start(): void {
    if (this.#started) return;
    this.#started = true;

    this.#unsubscribers.push(
      events.subscribe(['runner.created', 'runner.updated'], (runner) => this.#runner(runner)),
      events.subscribe('runner.deleted', ({ id }) => this.#forget(id)),
      events.subscribe('host.updated', (host) => this.#host(host)),
      events.subscribe('host.deleted', ({ id }) => {
        this.#hosts.delete(id);
        this.#push(hostRemovedEntry(id, this.#names.get(id), now()));
      }),
      events.subscribe('machine.updated', (machine) => {
        if (!machine.id) return;
        const change = machineChange(machine, this.#machines.get(machine.id));
        if (machine.state) this.#machines.set(machine.id, machine.state);
        this.#names.set(machine.id, machine.name);
        if (change) this.#push(machineEntry(machine, change, now()));
      }),
      events.subscribe('provider.updated', (provider) => this.#provider(provider)),
      events.subscribe('pool.created', (pool) => this.#pool(pool, 'created')),
      events.subscribe('pool.updated', (pool) => this.#pool(pool, 'updated')),
      events.subscribe('pool.deleted', ({ id }) =>
        this.#push(poolRemovedEntry(id, this.#names.get(id), now())),
      ),
      events.subscribe('installation.updated', (installation) => {
        if (!installation.id) return;
        const change = installationChange(installation, this.#installations.get(installation.id));
        this.#installations.set(installation.id, installation.healthy !== false);
        if (change) {
          const entry = installationEntry(installation, change, now());
          if (entry) this.#push(entry);
        }
      }),
      events.subscribe('webhook.delivery', (delivery) => {
        const entry = deliveryEntry(delivery);
        if (entry) this.#push(entry);
      }),
      events.subscribe('job.updated', (job) => {
        const news = jobNews(job);
        if (!news) return;
        const entry = jobEntry(job, news);
        if (entry) this.#push(entry);
      }),
      events.subscribe('problems.updated', (payload) => {
        const raised = newProblems(payload.items ?? [], this.#problems);
        // Until the snapshot has landed, everything the controller reports is
        // simply everything it was already reporting before this tab opened.
        if (!fleet.loaded) return;
        const at = now();
        for (const problem of raised) this.#push(problemEntry(problem, at));
      }),
      events.subscribe('audit', (event) => {
        const entry = auditEntry(event);
        if (entry) this.#push(entry);
      }),
    );

    // The fleet's snapshot is what "seen before" means: without it every host
    // in the first frame after a reconnect would read as a host that had just
    // joined, and a reconnect is exactly when a burst of them arrives.
    this.#stopWatching = $effect.root(() => {
      $effect(() => {
        if (!fleet.loaded) return;
        void fleet.shape;
        this.#prime();
      });
      // The Overview's own switch widens what counts as this fleet's work, and
      // the past already fetched answered the narrower question. Asking again
      // is one request, once, and it is what makes the switch mean the same
      // thing in this panel as in the ones beside it.
      $effect(() => {
        if (!prefs.otherRunners) return;
        for (const id of ['jobs', 'outcomes'] as const) if (this.shows(id)) void this.#seed(id);
      });
    });

    for (const category of FEED_CATEGORIES) {
      if (this.shows(category.id)) void this.#seed(category.id);
    }
  }

  /** Disconnect and forget everything. Called on sign-out. */
  stop(): void {
    for (const off of this.#unsubscribers) off();
    this.#unsubscribers = [];
    this.#stopWatching?.();
    this.#stopWatching = null;
    this.#started = false;
    this.#captured = [];
    this.#hosts.clear();
    this.#runners.clear();
    this.#cpu.clear();
    this.#machines.clear();
    this.#providers.clear();
    this.#installations.clear();
    this.#problems.clear();
    this.#names.clear();
    this.#seeded.clear();
  }

  /* -- internals ------------------------------------------------------------ */

  /**
   * A runner frame: the two milestones of an ordinary life, and the elastic
   * CPU moving under it. The failures are not here -- they are derived from
   * the fleet cache, which holds every failed runner already.
   */
  #runner(runner: Runner): void {
    if (!runner.id) return;
    const state = runner.state;
    const previous = this.#runners.get(runner.id);
    if (state) this.#runners.set(runner.id, state);
    const cpu = runner.cpu_resource?.state;
    const seenCPU = this.#cpu.get(runner.id);
    if (cpu) this.#cpu.set(runner.id, cpu);
    // Before the snapshot has landed there is nothing to compare against that
    // is not simply this tab's ignorance, and after a reconnect the replay
    // would otherwise announce a fleet that has been running all morning.
    if (!fleet.loaded) return;
    const at = now();
    const milestone = runnerMilestone(state, previous);
    if (milestone) {
      const entry = runnerMilestoneEntry(runner, milestone, at);
      if (entry) this.#push(entry);
    }
    const lent = cpuChange(cpu, seenCPU);
    if (lent) {
      const entry = cpuEntry(runner, lent, at, prefs.quirkyStatus);
      if (entry) this.#push(entry);
    }
    // A removed runner is gone from the fleet cache too, and on a fleet of
    // ephemeral runners these maps would otherwise grow by one entry per job
    // for as long as the tab is open.
    if (state === 'removed') this.#forget(runner.id);
  }

  #forget(id: string): void {
    this.#runners.delete(id);
    this.#cpu.delete(id);
  }

  #host(host: Host): void {
    if (!host.id) return;
    if (host.name) this.#names.set(host.id, host.name);
    const next = hostSignal(host);
    const previous = this.#hosts.get(host.id);
    this.#hosts.set(host.id, next);
    // Until the first snapshot has landed there is nothing to compare against
    // that is not simply this tab's ignorance.
    if (!previous && !fleet.loaded) return;
    const at = now();
    for (const change of hostChanges(next, previous)) {
      const entry = hostEntry(host, change, at);
      if (entry) this.#push(entry);
    }
  }

  #pool(pool: Pool, change: 'created' | 'updated'): void {
    if (!pool.id) return;
    if (pool.name) this.#names.set(pool.id, pool.name);
    const entry = poolEntry(pool, change, now());
    if (entry) this.#push(entry);
  }

  #provider(provider: Provider): void {
    if (!provider.id) return;
    if (provider.name) this.#names.set(provider.id, provider.name);
    const next = providerSignal(provider);
    const previous = this.#providers.get(provider.id);
    this.#providers.set(provider.id, next);
    const at = now();
    for (const change of providerChanges(next, previous)) {
      const entry = providerEntry(provider, change, at);
      if (entry) this.#push(entry);
    }
  }

  /** Learn what the fleet already holds, without reporting any of it. */
  #prime(): void {
    for (const host of fleet.hosts) {
      if (!host.id) continue;
      if (host.name) this.#names.set(host.id, host.name);
      if (!this.#hosts.has(host.id)) this.#hosts.set(host.id, hostSignal(host));
    }
    for (const runner of fleet.runners) {
      if (!runner.id) continue;
      if (runner.state && !this.#runners.has(runner.id)) this.#runners.set(runner.id, runner.state);
      const cpu = runner.cpu_resource?.state;
      if (cpu && !this.#cpu.has(runner.id)) this.#cpu.set(runner.id, cpu);
    }
    for (const pool of fleet.pools) {
      if (pool.id && pool.name) this.#names.set(pool.id, pool.name);
    }
    // Primed rather than reported: the problems the controller was already
    // raising when this tab opened are not news, and `newProblems` forgets a
    // problem that has cleared, so the same fault happening again is.
    newProblems(fleet.problems, this.#problems);
  }

  /**
   * Fetch a category's past, once per tab and once per mode.
   *
   * A failure is silent on purpose: the feed is a panel on a dashboard, and a
   * category that could not be backfilled still fills from the stream.
   */
  async #seed(id: FeedCategoryID): Promise<void> {
    if (id !== 'jobs' && id !== 'outcomes' && id !== 'audit') return;
    // The jobs the fetch asks for depend on the Overview's own switch, so the
    // mode is part of what "already seeded" means: asking for every runner's
    // jobs after opening on this fleet's own is a second, different past.
    const key = id === 'audit' ? id : `${id}:${prefs.otherRunners ? 'all' : 'ours'}`;
    if (this.#seeded.has(key)) return;
    this.#seeded.add(key);
    try {
      if (id === 'audit') {
        const page = await listAudit({ limit: SEED_LIMIT });
        for (const event of page.items ?? []) {
          const entry = auditEntry(event);
          if (entry) this.#push(entry);
        }
        return;
      }
      // Both job categories come out of one request: they are the same
      // fetch filtered two ways, and a tab that shows both should not ask
      // for it twice.
      for (const job of await this.#completedJobs(prefs.otherRunners)) {
        const news = jobNews(job);
        if (!news) continue;
        const entry = jobEntry(job, news);
        if (entry) this.#push(entry);
      }
    } catch {
      // Left unseeded rather than surfaced: see above. Another tab, or this
      // one after a reload, will try again.
      this.#seeded.delete(key);
    }
  }

  /** The recently finished jobs, fetched once per mode and shared. */
  #completedJobs(all: boolean): Promise<Job[]> {
    const key = all ? 'all' : 'ours';
    let pending = this.#jobFetches.get(key);
    if (!pending) {
      pending = listJobs({
        state: ['completed'],
        managed: all ? undefined : true,
        limit: SEED_LIMIT,
        sort: 'completed_at',
        order: 'desc',
      })
        .then((page) => page.items ?? [])
        .catch((cause: unknown) => {
          this.#jobFetches.delete(key);
          throw cause;
        });
      this.#jobFetches.set(key, pending);
    }
    return pending;
  }

  /**
   * Add one entry, replacing an earlier report of the same fact.
   *
   * A category that is off is not merely hidden, it is not kept: the bound is
   * shared, and a fleet with automation against its API would otherwise spend
   * the whole of it on an audit trail nobody asked to see.
   */
  #push(entry: FeedEntry): void {
    if (!this.shows(entry.category)) return;
    const rest = this.#captured.filter((seen) => seen.id !== entry.id);
    this.#captured = [entry, ...rest].slice(0, CAPACITY);
  }
}

export const feed = new Feed();
