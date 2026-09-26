/**
 * How the status page says what GET /api/v1/status returns.
 *
 * Kept apart from the component, and free of imports, so the unit tests can
 * hold it and so the page's own entry stays a few kilobytes: it is the one
 * page in the build that people with no account load, often on a phone.
 */
import type { components } from '../lib/api/schema';

export type FleetStatus = components['schemas']['FleetStatus'];
export type FleetState = FleetStatus['state'];
export type CountBand = components['schemas']['CountBand'];

/**
 * The three states on the fixed status mapping in docs/ui-guidelines.md:
 * blocked is danger, degraded is pending, healthy is idle. They are the tones
 * an operator already reads on every other page, and each carries the shape
 * that goes with it, so colour is never the only thing saying which.
 */
export const STATE_PRESENTATION: Record<
  FleetState,
  { tone: 'danger' | 'pending' | 'idle'; shape: 'triangle' | 'dashed' | 'hollow'; headline: string }
> = {
  healthy: { tone: 'idle', shape: 'hollow', headline: 'The fleet is running normally' },
  degraded: {
    tone: 'pending',
    shape: 'dashed',
    headline: 'The fleet is running, with problems that may slow jobs down',
  },
  blocked: {
    tone: 'danger',
    shape: 'triangle',
    headline: 'Some jobs cannot run until the fleet’s operators act',
  },
};

const BAND_WORDS: Record<CountBand, string> = {
  none: 'none',
  few: 'a few',
  many: 'many',
  backed_up: 'backed up',
};

/** A band as a reader would say it. */
export function bandWords(band: CountBand): string {
  return BAND_WORDS[band] ?? band;
}

/** A wait in whole minutes, said the way a person would. */
export function waitWords(minutes: number): string {
  if (minutes <= 0) return 'under a minute';
  if (minutes === 1) return 'about a minute';
  if (minutes < 60) return `about ${minutes} minutes`;
  const hours = Math.round(minutes / 60);
  return hours === 1 ? 'about an hour' : `about ${hours} hours`;
}

/** How long ago an instant was, coarsely: the page refreshes every thirty seconds. */
export function sinceWords(iso: string | undefined, now: Date): string {
  if (!iso) return '';
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return '';
  const minutes = Math.max(0, Math.floor((now.getTime() - then) / 60_000));
  if (minutes < 1) return 'just now';
  if (minutes < 60) return minutes === 1 ? '1 minute ago' : `${minutes} minutes ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return hours === 1 ? '1 hour ago' : `${hours} hours ago`;
  return `${Math.floor(hours / 24)} days ago`;
}

/** How often the page asks again. Thirty seconds, as docs/ui.md promises. */
export const POLL_MS = 30_000;
