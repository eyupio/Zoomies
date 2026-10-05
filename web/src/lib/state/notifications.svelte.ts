/**
 * What the fleet wants a person to know, and what that person has already read.
 *
 * The controller reports problems; it has no opinion about whether an operator
 * has seen them. That opinion lives here, per account, because the two useful
 * answers are different for every operator: "I know about the privileged pool,
 * I chose it" is a settled decision, while an unhealthy host is news. Without
 * somewhere to put the first kind, a panel that is never clear stops being
 * read, which costs the operator the second kind too.
 *
 * Three rules keep a dismissal from becoming a way to hide a real fault:
 *
 *  * A plain dismissal is forgotten the moment the controller stops reporting
 *    the problem, so the same fault happening again is news again.
 *  * A dismissal only covers the severity it was made at. A warning that
 *    becomes an error comes back, because it is not the thing that was read.
 *  * A snooze is a dismissal with an expiry the operator picks -- 15 minutes,
 *    an hour, a day -- rather than "until resolved". It returns to the active
 *    list on its own once the clock passes that time, with no further click,
 *    and not before: a problem that clears for a pass and comes back inside
 *    the window is still snoozed. A snooze can cover the one problem or every
 *    problem of its kind, so a planned outage that makes every pool short of
 *    capacity is put away once -- including a pool that runs short after the
 *    snooze was made.
 *
 * Dismissals are the operator's own reading of the fleet, not fleet state, so
 * nothing here changes what `GET /api/v1/problems`, `zoomies status` or an
 * alerting rule sees. They are kept on the account, in the database, so a
 * snooze made on a laptop holds on a phone and survives cleared site data and
 * a second tab. The browser keeps a copy as well: it is what shows instantly
 * on load, and it is the whole store for an identity with no account behind it
 * (an auth-disabled instance), which is how every browser kept them before.
 */
import { getProblemDismissals, patchProblemDismissals } from '../api/client';
import type { Problem, ProblemDismissal, Severity } from '../api/types';
import { onClockTick } from '../format';
import {
  carriedOver,
  covers,
  fromServer,
  rank,
  severityOf as severity,
  SEVERITY_ORDER,
  spentKeys,
  toServer,
  type Dismissals,
} from '../problems/dismissals';
import { problemKey, problemTypeKey } from '../problems/identity';
import { fleet } from './fleet.svelte';
import { storage } from './prefs.svelte';

// One definition of "the same fault", shared with the Overview's feed, which
// reports a problem the first time it is raised.
export { problemKey, SEVERITY_ORDER };

const DISMISSED_KEY = 'zoomies.problems.dismissed';
// Which account the local copy was last reconciled with. A copy that belongs to
// somebody else is never uploaded into this account.
const ACCOUNT_KEY = 'zoomies.problems.dismissed.account';

/** What one of each is called in a sentence an operator reads. */
export const SEVERITY_NOUN: Record<Severity, string> = {
  error: 'error',
  warning: 'warning',
  info: 'note',
};

export interface SnoozeOption {
  id: string;
  /** What the menu item reads: "Snooze for 15 minutes". */
  label: string;
  ms: number;
}

/** The durations offered on every problem. Fixed and short, on purpose: a
 * custom picker is one more decision on the way to putting a fault down. */
export const SNOOZE_OPTIONS: readonly SnoozeOption[] = [
  { id: '15m', label: '15 minutes', ms: 15 * 60_000 },
  { id: '1h', label: '1 hour', ms: 60 * 60_000 },
  { id: '4h', label: '4 hours', ms: 4 * 60 * 60_000 },
  { id: '24h', label: '24 hours', ms: 24 * 60 * 60_000 },
];

function load(): Dismissals {
  const raw = storage.get(DISMISSED_KEY);
  if (!raw) return {};
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object') return {};
    return parsed as Dismissals;
  } catch {
    return {};
  }
}

export interface ProblemGroup {
  severity: Severity;
  items: readonly Problem[];
}

class Notifications {
  #dismissals = $state<Dismissals>(load());
  #open = $state(false);
  #showDismissed = $state(false);
  // Ticks off the same shared clock every other relative time on the page
  // reads, so a snooze expiring costs nothing beyond the timer already
  // running for "4m ago" elsewhere on screen.
  #now = $state(Date.now());
  // Whether decisions are also kept on the account. False until a sync has
  // found an account to keep them on, and for good on an identity with none.
  #remote = false;
  #account: string | null = null;
  // Writes to the server go one at a time, in the order they were made, so a
  // restore can never overtake the dismissal it undoes.
  #queue: Promise<void> = Promise.resolve();
  #syncedAt = 0;

  constructor() {
    // A dismissal outlives nothing it was not meant to. Once the controller
    // stops reporting a problem a plain dismissal is spent, so the fault
    // recurring is reported again rather than being silently swallowed by a
    // click somebody made last week; a snooze is spent when its own clock runs
    // out. Sweeping here -- rather than at dismissal time -- is what makes both
    // true without any bookkeeping at the call sites.
    $effect.root(() => {
      $effect(() => onClockTick((now) => (this.#now = now)));
      $effect(() => {
        if (!fleet.loaded) return;
        this.#sweep(fleet.problems, this.#now);
      });
      // A decision made in another tab or on another device reaches this one
      // when it is looked at again, rather than waiting for a reload.
      $effect(() => {
        if (typeof document === 'undefined') return;
        const onVisible = () => {
          if (document.visibilityState === 'visible' && this.#account !== null) {
            void this.syncAccount(this.#account);
          }
        };
        document.addEventListener('visibilitychange', onVisible);
        return () => document.removeEventListener('visibilitychange', onVisible);
      });
    });
  }

  /**
   * Bring this account's dismissals into the browser after sign-in, and again
   * whenever the page becomes visible. The server's list is the truth: a
   * decision undone on another device is undone here.
   *
   * The first account to sign in on a browser that has dismissals from before
   * they were kept on the server takes them with it, so an upgrade does not
   * bring back everything an operator had put away. A browser last used by a
   * different account hands nothing over.
   */
  async syncAccount(userID: string): Promise<void> {
    const now = Date.now();
    // Not more often than a tab can plausibly change hands: switching between
    // two windows should not be a request each time.
    if (this.#account === userID && now - this.#syncedAt < 5_000) return;
    try {
      await this.#queue;
      let view = await getProblemDismissals();
      if (!view.stored) {
        this.#remote = false;
        return;
      }
      let remote = fromServer(view.items, now);
      const previous = storage.get(ACCOUNT_KEY);
      const carry = previous === null ? carriedOver(this.#dismissals, remote, now) : [];
      if (carry.length > 0) {
        view = await patchProblemDismissals({ set: carry });
        remote = fromServer(view.items, now);
      }
      this.#dismissals = remote;
      this.#persist();
      storage.set(ACCOUNT_KEY, userID);
      this.#account = userID;
      this.#remote = true;
      this.#syncedAt = Date.now();
      this.#closeIfClear();
    } catch {
      // The browser's copy stands, exactly as it did before dismissals were
      // kept on the server: an unreachable controller must not make the drawer
      // forget what it was told.
    }
  }

  disconnectAccount(): void {
    this.#remote = false;
    this.#account = null;
  }

  /* -- reads -------------------------------------------------------------- */

  /** Everything the controller reports, worst first, dismissed or not. */
  get all(): readonly Problem[] {
    return fleet.problems;
  }

  /** What still wants a person: the badge, the banner and the panel show these. */
  get active(): readonly Problem[] {
    return fleet.problems.filter((p) => !this.isDismissed(p));
  }

  /** What has been read and put away. Reachable, never in the way. */
  get dismissed(): readonly Problem[] {
    return fleet.problems.filter((p) => this.isDismissed(p));
  }

  /** True when there is genuinely nothing wrong -- the common case. */
  get clear(): boolean {
    return fleet.problems.length === 0 && fleet.problemsOk;
  }

  get errorCount(): number {
    return this.active.filter((p) => severity(p) === 'error').length;
  }

  /** The worst severity still active, or null when nothing is. */
  get worst(): Severity | null {
    let worst: Severity | null = null;
    for (const p of this.active) {
      const s = severity(p);
      if (worst === null || rank(s) < rank(worst)) worst = s;
    }
    return worst;
  }

  /** The active problems grouped by severity, worst group first. */
  get groups(): readonly ProblemGroup[] {
    return this.group(this.active);
  }

  /** Group any list the same way, so the drawer's two lists read alike. */
  group(items: readonly Problem[]): readonly ProblemGroup[] {
    const out: ProblemGroup[] = [];
    for (const s of SEVERITY_ORDER) {
      const group = items.filter((p) => severity(p) === s);
      if (group.length > 0) out.push({ severity: s, items: group });
    }
    return out;
  }

  isDismissed(problem: Problem): boolean {
    return this.#covering(problem) !== undefined;
  }

  dismissedAt(problem: Problem): string | undefined {
    return this.#covering(problem)?.at;
  }

  /** When a snoozed problem comes back on its own, or undefined when it was
   * dismissed outright rather than snoozed. */
  snoozedUntil(problem: Problem): string | undefined {
    return this.#covering(problem)?.until;
  }

  /** Whether what is holding this problem back is a snooze of its whole kind,
   * so the drawer can say that restoring it brings the rest back too. */
  snoozedByType(problem: Problem): boolean {
    const own = this.#live(this.#dismissals[problemKey(problem)], problem);
    return !own && this.#live(this.#dismissals[problemTypeKey(problem)], problem);
  }

  /* -- the drawer ---------------------------------------------------------- */

  get open(): boolean {
    return this.#open;
  }

  set open(value: boolean) {
    this.#open = value;
    if (!value) this.#showDismissed = false;
  }

  /** Whether the drawer is currently listing what has been put away. */
  get showDismissed(): boolean {
    return this.#showDismissed;
  }

  set showDismissed(value: boolean) {
    this.#showDismissed = value;
  }

  /* -- writes -------------------------------------------------------------- */

  dismiss(problem: Problem): void {
    const key = problemKey(problem);
    this.#apply({ [key]: { severity: severity(problem), at: new Date().toISOString() } }, []);
  }

  /** Put this one away for a fixed while rather than until it resolves. */
  snooze(problem: Problem, ms: number): void {
    this.#apply({ [problemKey(problem)]: this.#snoozed(problem, ms) }, []);
  }

  /** Put every problem of this one's kind away for a fixed while, including
   * any raised after the snooze was made. */
  snoozeType(problem: Problem, ms: number): void {
    this.#apply({ [problemTypeKey(problem)]: this.#snoozed(problem, ms) }, []);
  }

  /** Put away everything currently listed. The drawer's one bulk action. */
  dismissAll(): void {
    const at = new Date().toISOString();
    const set: Dismissals = {};
    for (const problem of this.active) {
      set[problemKey(problem)] = { severity: severity(problem), at };
    }
    this.#apply(set, []);
  }

  /** Bring a problem back, lifting whichever decision was holding it: its
   * own, and a snooze of its kind, which brings the rest of that kind back
   * too -- a restore that left the problem hidden would be no restore. */
  restore(problem: Problem): void {
    const keys = [problemKey(problem), problemTypeKey(problem)].filter(
      (key) => key in this.#dismissals,
    );
    if (keys.length === 0) return;
    this.#apply({}, keys);
  }

  restoreAll(): void {
    this.#apply({}, Object.keys(this.#dismissals));
  }

  /* -- internals ------------------------------------------------------------ */

  /**
   * Dismissing the last problem still asking for attention closes the panel
   * that was showing it: there is nothing left for it to hold open for.
   */
  #closeIfClear(): void {
    if (this.#open && this.active.length === 0) this.open = false;
  }

  /** The live dismissal covering a problem -- its own, or a snooze of its
   * kind -- or undefined when neither does. */
  #covering(problem: Problem): Dismissals[string] | undefined {
    for (const key of [problemKey(problem), problemTypeKey(problem)]) {
      const record = this.#dismissals[key];
      if (this.#live(record, problem)) return record;
    }
    return undefined;
  }

  #snoozed(problem: Problem, ms: number): Dismissals[string] {
    const now = new Date();
    return {
      severity: severity(problem),
      at: now.toISOString(),
      until: new Date(now.getTime() + ms).toISOString(),
    };
  }

  #live(record: Dismissals[string] | undefined, problem: Problem): record is Dismissals[string] {
    return covers(record, problem, this.#now);
  }

  /** Drops what has been spent: a plain dismissal whose problem is no longer
   * reported, and a snooze whose clock has run out. See `spentKeys` for why a
   * snooze is not dropped with its problem. */
  #sweep(problems: readonly Problem[], now: number): void {
    const reported = new Set([...problems.map(problemKey), ...problems.map(problemTypeKey)]);
    const spent = spentKeys(this.#dismissals, reported, now);
    if (spent.length > 0) this.#apply({}, spent);
  }

  /** The one place a decision changes: in memory and in the browser at once,
   * so the drawer answers instantly, and on the account behind them. */
  #apply(set: Dismissals, remove: string[]): void {
    const next = { ...this.#dismissals };
    for (const key of remove) delete next[key];
    Object.assign(next, set);
    this.#dismissals = next;
    this.#persist();
    this.#push(
      Object.entries(set).map(([key, record]) => toServer(key, record)),
      remove,
    );
    this.#closeIfClear();
  }

  #push(set: ProblemDismissal[], remove: string[]): void {
    if (!this.#remote || (set.length === 0 && remove.length === 0)) return;
    this.#queue = this.#queue
      .then(() => patchProblemDismissals({ set, remove }))
      .then(
        () => undefined,
        () => {
          // The browser's copy stands until the next sync; a dismissal should
          // never turn into an error toast.
        },
      );
  }

  #persist(): void {
    storage.set(DISMISSED_KEY, JSON.stringify(this.#dismissals));
  }
}

export const notifications = new Notifications();
