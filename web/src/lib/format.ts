/**
 * Formatting helpers. Every number an operator reads goes through one of these,
 * so "4m ago" means the same thing on every page.
 */

/* -- the shared clock ----------------------------------------------------
 * A thousand runner rows must not mean a thousand timers. One interval ticks,
 * and every RelativeTime on screen recomputes from it.
 * --------------------------------------------------------------------- */

type Tick = (now: number) => void;

const tickers = new Set<Tick>();
let clock: ReturnType<typeof setInterval> | null = null;

/** How often relative times refresh. Ten seconds keeps "just now" honest. */
export const CLOCK_INTERVAL_MS = 10_000;

function fire(): void {
  const now = Date.now();
  for (const fn of tickers) fn(now);
}

/**
 * Subscribe to the shared clock. Returns the unsubscribe function; the last
 * unsubscriber stops the interval, so an idle page costs nothing.
 */
export function onClockTick(fn: Tick): () => void {
  tickers.add(fn);
  if (clock === null && typeof window !== 'undefined') {
    clock = setInterval(fire, CLOCK_INTERVAL_MS);
    document.addEventListener('visibilitychange', onVisible);
  }
  return () => {
    tickers.delete(fn);
    if (tickers.size === 0 && clock !== null) {
      clearInterval(clock);
      clock = null;
      document.removeEventListener('visibilitychange', onVisible);
    }
  };
}

function onVisible(): void {
  // A backgrounded tab throttles timers, so catch up the moment it returns.
  if (document.visibilityState === 'visible') fire();
}

/* -- time ---------------------------------------------------------------- */

export type TimeInput = string | number | Date | null | undefined;

/** Milliseconds since the epoch, or null when there is no usable value. */
export function toMillis(value: TimeInput): number | null {
  if (value === null || value === undefined || value === '') return null;
  const ms = value instanceof Date ? value.getTime() : new Date(value).getTime();
  return Number.isNaN(ms) ? null : ms;
}

/** The ISO 8601 form, which is what a timestamp's tooltip shows. */
export function toIso(value: TimeInput): string {
  const ms = toMillis(value);
  return ms === null ? '' : new Date(ms).toISOString();
}

const ABSOLUTE = new Intl.DateTimeFormat(undefined, {
  dateStyle: 'medium',
  timeStyle: 'medium',
});

/** The local wall-clock rendering, for detail panels and tooltips. */
export function formatAbsolute(value: TimeInput): string {
  const ms = toMillis(value);
  return ms === null ? '--' : ABSOLUTE.format(ms);
}

/** Both forms, for a `title` attribute: local time first, then the exact ISO value. */
export function formatTimestampTitle(value: TimeInput): string {
  const ms = toMillis(value);
  if (ms === null) return 'No timestamp recorded';
  return `${ABSOLUTE.format(ms)} (${new Date(ms).toISOString()})`;
}

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/**
 * "4m ago", "in 2h", "just now". Past and future both read naturally, which
 * matters because token expiry and job queue times sit in the same table.
 */
export function relativeTime(value: TimeInput, now: number = Date.now()): string {
  const ms = toMillis(value);
  if (ms === null) return '--';
  const delta = now - ms;
  const abs = Math.abs(delta);
  const past = delta >= 0;

  if (abs < 10_000) return 'just now';
  const unit =
    abs < MINUTE
      ? `${Math.round(abs / 1000)}s`
      : abs < HOUR
        ? `${Math.round(abs / MINUTE)}m`
        : abs < DAY
          ? `${Math.round(abs / HOUR)}h`
          : abs < 7 * DAY
            ? `${Math.round(abs / DAY)}d`
            : null;
  if (unit === null) return formatAbsolute(ms);
  return past ? `${unit} ago` : `in ${unit}`;
}

/* -- durations ------------------------------------------------------------ */

function pad(n: number): string {
  return n < 10 ? `0${n}` : String(n);
}

/**
 * A humanised duration: "820ms", "4.2s", "1m 04s", "3h 12m", "2d 04h".
 * Two units at most -- more than that is noise in a table cell.
 */
export function formatDuration(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || Number.isNaN(ms)) return '--';
  const value = Math.max(0, Math.round(ms));
  if (value < 1000) return `${value}ms`;
  const seconds = value / 1000;
  if (seconds < 10) return `${seconds.toFixed(1)}s`;
  if (seconds < 60) return `${Math.round(seconds)}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${pad(Math.floor(seconds % 60))}s`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ${pad(minutes % 60)}m`;
  const days = Math.floor(hours / 24);
  return `${days}d ${pad(hours % 24)}h`;
}

/** How long ago something started, as a duration rather than a relative time. */
export function elapsedSince(value: TimeInput, now: number = Date.now()): string {
  const ms = toMillis(value);
  return ms === null ? '--' : formatDuration(now - ms);
}

const GO_UNITS: Record<string, number> = {
  ns: 1e-6,
  us: 1e-3,
  µs: 1e-3,
  ms: 1,
  s: 1000,
  m: 60_000,
  h: 3_600_000,
};

/**
 * Parse a Go duration string ("5m", "1h30s") into milliseconds. Returns null
 * for anything it does not understand, so a form can report it as invalid.
 */
export function parseGoDuration(input: string | null | undefined): number | null {
  if (!input) return null;
  const text = input.trim();
  if (text === '0') return 0;
  const pattern = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/g;
  let total = 0;
  let matched = 0;
  let consumed = 0;
  for (const m of text.matchAll(pattern)) {
    const amount = Number(m[1]);
    const unit = GO_UNITS[m[2] ?? ''];
    if (unit === undefined || Number.isNaN(amount)) return null;
    total += amount * unit;
    matched += 1;
    consumed += m[0].length;
  }
  if (matched === 0 || consumed !== text.length) return null;
  return total;
}

/** Render milliseconds as the Go duration string the API expects. */
export function toGoDuration(ms: number): string {
  if (ms <= 0) return '0s';
  const parts: string[] = [];
  let rest = Math.round(ms);
  const hours = Math.floor(rest / 3_600_000);
  rest -= hours * 3_600_000;
  const minutes = Math.floor(rest / 60_000);
  rest -= minutes * 60_000;
  const seconds = rest / 1000;
  if (hours) parts.push(`${hours}h`);
  if (minutes) parts.push(`${minutes}m`);
  if (seconds) parts.push(`${Number(seconds.toFixed(3))}s`);
  return parts.join('') || '0s';
}

/** A Go duration string, spelled out for a person: "5m" becomes "5m". */
export function formatGoDuration(input: string | null | undefined): string {
  const ms = parseGoDuration(input);
  return ms === null ? (input ?? '--') : formatDuration(ms);
}

/* -- numbers -------------------------------------------------------------- */

const NUMBER = new Intl.NumberFormat();

/** Grouped digits. Pair with the `.tabular` class so columns line up. */
export function formatNumber(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(value)) return '--';
  return NUMBER.format(value);
}

/** Compact digits for tiles: 12.4k, 3.1M. */
export function formatCompact(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(value)) return '--';
  if (Math.abs(value) < 10_000) return NUMBER.format(value);
  if (Math.abs(value) < 1_000_000) return `${(value / 1000).toFixed(1)}k`;
  return `${(value / 1_000_000).toFixed(1)}M`;
}

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'] as const;

/** Binary-scaled bytes: 1536 becomes "1.5 KB". */
export function formatBytes(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(value)) return '--';
  let n = value;
  let unit = 0;
  while (Math.abs(n) >= 1024 && unit < BYTE_UNITS.length - 1) {
    n /= 1024;
    unit += 1;
  }
  const digits = unit === 0 ? 0 : Math.abs(n) < 10 ? 1 : 0;
  return `${n.toFixed(digits)} ${BYTE_UNITS[unit]}`;
}

/** Megabytes as the API reports them (`memory_mb`). */
export function formatMegabytes(value: number | null | undefined): string {
  if (value === null || value === undefined) return '--';
  return formatBytes(value * 1024 * 1024);
}

/**
 * A ratio in the range 0..1 as a percentage. Utilisation arrives as a double,
 * so this is what every bar and tile uses.
 */
export function formatPercent(value: number | null | undefined, digits = 0): string {
  if (value === null || value === undefined || Number.isNaN(value)) return '--';
  return `${(value * 100).toFixed(digits)}%`;
}

/** Clamp to 0..1, for anything that drives a width. */
export function ratio(value: number | null | undefined): number {
  if (value === null || value === undefined || Number.isNaN(value)) return 0;
  return Math.min(1, Math.max(0, value));
}

/* -- words ---------------------------------------------------------------- */

/** "1 runner", "3 runners". Pass the plural when it is irregular. */
export function pluralise(count: number, one: string, many = `${one}s`): string {
  return `${formatNumber(count)} ${count === 1 ? one : many}`;
}

/** A list read aloud: "a, b and c". British serial comma rules, so none. */
export function joinWords(items: readonly string[]): string {
  if (items.length === 0) return '';
  if (items.length === 1) return items[0] ?? '';
  return `${items.slice(0, -1).join(', ')} and ${items[items.length - 1]}`;
}

/** Shorten an identifier for a dense cell, keeping the prefix that means something. */
export function shortId(id: string | null | undefined, keep = 8): string {
  if (!id) return '--';
  const underscore = id.indexOf('_');
  if (underscore > 0 && id.length > underscore + keep + 1) {
    return `${id.slice(0, underscore + 1)}${id.slice(underscore + 1, underscore + 1 + keep)}…`;
  }
  return id.length > keep * 2 ? `${id.slice(0, keep * 2)}…` : id;
}

/** `owner/repo` split for two-line rendering in a cell. */
export function splitRepo(repo: string | null | undefined): { owner: string; name: string } {
  const [owner = '', name = ''] = (repo ?? '').split('/');
  return { owner, name };
}
