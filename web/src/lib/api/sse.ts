/**
 * One shared connection to /api/v1/events for the whole application.
 *
 * There is no refresh button in Zoomies, so this is the only thing standing
 * between the operator and a stale screen. It therefore never throws: a dead
 * connection is a state the top bar renders, not an exception a page has to
 * catch.
 *
 * Reconnection has two paths, deliberately:
 *
 *  * The browser's own EventSource retry, which resends `Last-Event-ID` and so
 *    lets the server replay what it buffered. We leave that alone and just
 *    report `reconnecting` while it happens.
 *  * Our own backoff, for when the browser gives up (a non-2xx response closes
 *    an EventSource for good). Because a fresh EventSource has no last id to
 *    send, we carry it in the query string instead.
 *
 * Either way, regaining focus or coming back online retries immediately rather
 * than waiting out the backoff -- an operator who has just opened the laptop
 * lid should not stare at "reconnecting" for fifteen seconds.
 */
import { apiBase } from './client';
import type { EventKind, EventPayloads } from './types';

export type SseStatus = 'connecting' | 'live' | 'reconnecting' | 'offline';

type AnyListener = (data: unknown, id: string | undefined) => void;
type StatusListener = (status: SseStatus) => void;

const BACKOFF_BASE = 500;
const BACKOFF_CAP = 15_000;
/** Server heartbeat is every 20s; three missed ones means something is wrong. */
const WATCHDOG_MS = 70_000;

class EventStream {
  #source: EventSource | null = null;
  #status: SseStatus = 'offline';
  #lastEventId: string | null = null;
  #attempt = 0;
  #retryTimer: ReturnType<typeof setTimeout> | null = null;
  #watchdog: ReturnType<typeof setTimeout> | null = null;
  #heartbeats = false;
  #started = false;
  #bound = false;

  readonly #listeners = new Map<EventKind, Set<AnyListener>>();
  readonly #statusListeners = new Set<StatusListener>();

  get status(): SseStatus {
    return this.#status;
  }

  get lastEventId(): string | null {
    return this.#lastEventId;
  }

  /** Open the stream. Safe to call more than once. */
  start(): void {
    if (this.#started) return;
    this.#started = true;
    this.#bindWindow();
    this.#open();
  }

  /** Close the stream and stop retrying. Used on sign-out. */
  stop(): void {
    this.#started = false;
    this.#clearTimers();
    this.#close();
    this.#setStatus('offline');
  }

  /** Drop the current connection and open a new one now, resetting the backoff. */
  reconnectNow(): void {
    if (!this.#started) return;
    this.#clearTimers();
    this.#close();
    this.#attempt = 0;
    this.#open();
  }

  /**
   * Listen for one or more event kinds. Returns the unsubscribe function; call
   * it from an `$effect` teardown.
   */
  subscribe<K extends EventKind>(
    kinds: K | readonly K[],
    handler: (data: EventPayloads[K], id: string | undefined) => void,
  ): () => void {
    const list = (Array.isArray(kinds) ? kinds : [kinds]) as readonly K[];
    const wrapped: AnyListener = (data, id) => handler(data as EventPayloads[K], id);
    for (const kind of list) {
      let set = this.#listeners.get(kind);
      if (!set) {
        set = new Set();
        this.#listeners.set(kind, set);
      }
      set.add(wrapped);
      if (this.#source) this.#attachKind(this.#source, kind);
    }
    return () => {
      for (const kind of list) this.#listeners.get(kind)?.delete(wrapped);
    };
  }

  /** Listen for connection-state changes. Fires immediately with the current one. */
  onStatus(handler: StatusListener): () => void {
    this.#statusListeners.add(handler);
    handler(this.#status);
    return () => this.#statusListeners.delete(handler);
  }

  /* -- internals --------------------------------------------------------- */

  #bindWindow(): void {
    if (this.#bound || typeof window === 'undefined') return;
    this.#bound = true;
    const wake = () => {
      if (!this.#started) return;
      if (this.#status === 'live') return;
      this.reconnectNow();
    };
    window.addEventListener('online', wake);
    window.addEventListener('focus', wake);
    window.addEventListener('offline', () => {
      if (this.#started) this.#setStatus('offline');
    });
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'visible') wake();
    });
  }

  #open(): void {
    if (typeof EventSource === 'undefined') return;
    if (typeof navigator !== 'undefined' && navigator.onLine === false) {
      this.#setStatus('offline');
      return;
    }
    this.#setStatus(this.#attempt === 0 ? 'connecting' : 'reconnecting');

    // A fresh EventSource cannot send the Last-Event-ID header, so the id
    // travels in the query string on a manual reconnect. Servers that only
    // read the header ignore it, which costs nothing.
    const query = this.#lastEventId
      ? `?last_event_id=${encodeURIComponent(this.#lastEventId)}`
      : '';
    let source: EventSource;
    try {
      source = new EventSource(`${apiBase}/events${query}`);
    } catch {
      this.#scheduleRetry();
      return;
    }
    this.#source = source;

    source.onopen = () => {
      this.#attempt = 0;
      this.#setStatus('live');
      this.#armWatchdog();
    };
    source.onmessage = (e) => this.#dispatch('heartbeat', e);
    source.onerror = () => {
      // readyState CONNECTING means the browser is retrying by itself and will
      // resend Last-Event-ID; leave it to do that and just report the state.
      if (source.readyState === EventSource.CONNECTING) {
        this.#setStatus('reconnecting');
        return;
      }
      this.#close();
      this.#scheduleRetry();
    };
    for (const kind of this.#listeners.keys()) this.#attachKind(source, kind);
    // Heartbeats are always listened for: they drive the stall watchdog.
    this.#attachKind(source, 'heartbeat');
  }

  #attachKind(source: EventSource, kind: EventKind): void {
    const flag = `zoomies:${kind}`;
    const marked = source as EventSource & { [key: string]: unknown };
    if (marked[flag]) return;
    marked[flag] = true;
    source.addEventListener(kind, (e) => this.#dispatch(kind, e as MessageEvent<string>));
  }

  #dispatch(kind: EventKind, e: MessageEvent<string>): void {
    if (e.lastEventId) this.#lastEventId = e.lastEventId;
    if (kind === 'heartbeat') {
      this.#heartbeats = true;
      this.#armWatchdog();
    } else {
      this.#armWatchdog();
    }
    const listeners = this.#listeners.get(kind);
    if (!listeners || listeners.size === 0) return;
    let data: unknown;
    try {
      data = e.data ? JSON.parse(e.data) : undefined;
    } catch {
      // A malformed frame is the server's problem, not something a page should
      // ever see as an exception. Drop it and keep the stream alive.
      return;
    }
    for (const fn of listeners) {
      try {
        fn(data, e.lastEventId || undefined);
      } catch {
        // A broken subscriber must not take the connection down with it.
      }
    }
  }

  /**
   * Guards against the connection that is open but silent -- a proxy holding a
   * dead socket. Only armed once we have actually seen a heartbeat event, so a
   * server that heartbeats with comment frames does not get reconnected every
   * seventy seconds for no reason.
   */
  #armWatchdog(): void {
    if (this.#watchdog) clearTimeout(this.#watchdog);
    if (!this.#heartbeats) return;
    this.#watchdog = setTimeout(() => {
      if (this.#started) this.reconnectNow();
    }, WATCHDOG_MS);
  }

  #scheduleRetry(): void {
    if (!this.#started) return;
    this.#setStatus(navigator?.onLine === false ? 'offline' : 'reconnecting');
    const delay = Math.min(BACKOFF_CAP, BACKOFF_BASE * 2 ** this.#attempt);
    // A little jitter so a controller restart does not bring every open dashboard
    // back at exactly the same instant.
    const jittered = delay * (0.75 + Math.random() * 0.5);
    this.#attempt += 1;
    if (this.#retryTimer) clearTimeout(this.#retryTimer);
    this.#retryTimer = setTimeout(() => this.#open(), jittered);
  }

  #close(): void {
    if (this.#source) {
      this.#source.onopen = null;
      this.#source.onerror = null;
      this.#source.onmessage = null;
      this.#source.close();
      this.#source = null;
    }
  }

  #clearTimers(): void {
    if (this.#retryTimer) clearTimeout(this.#retryTimer);
    if (this.#watchdog) clearTimeout(this.#watchdog);
    this.#retryTimer = null;
    this.#watchdog = null;
  }

  #setStatus(next: SseStatus): void {
    if (this.#status === next) return;
    this.#status = next;
    for (const fn of this.#statusListeners) {
      try {
        fn(next);
      } catch {
        // Same reasoning as above.
      }
    }
  }
}

/** The one stream. Started by the shell once the visitor is authenticated. */
export const events = new EventStream();
