/**
 * Rendering one configuration setting.
 *
 * The formatting rules are here rather than in the row component because the
 * panel's summary counts and the row's value have to agree about what "set"
 * means: a setting whose value is an empty list is not configured, and one
 * whose value is `false` very much is.
 */
import type { Setting, SettingSource } from '$lib/api/types';
import type { StatusTone } from '$lib/status';
import { formatQuantity, parseQuantity, type Quantity } from '$lib/units';

/** A section heading, and one line about what the section is for. */
export const SECTION_BLURB: Record<string, string> = {
  server: 'How Zoomies listens, and the URL GitHub and the agents use to reach it.',
  database: 'Where the SQLite file lives. It holds everything below.',
  security: 'Sessions, encryption and whether authentication is on at all.',
  github: 'How Zoomies talks to GitHub, and what it falls back to when webhooks do not arrive.',
  agent: 'The agent built into Zoomies. A standalone agent is configured on its own host.',
  runners:
    "What every runner this fleet creates is started with. A pool's own env is layered over it.",
  scheduler:
    'How eagerly runners are created, when they are given up on, and what happens to a job one of them broke.',
  log: 'How much the controller says, and in what format.',
  oidc: 'Single sign-on.',
  metrics: 'The Prometheus endpoint.',
  status:
    'The fleet status page for people with no account: a state, rough counts and problem codes, never a name.',
  retention: 'How long history is kept before it is pruned. Audit rows are never pruned.',
  limits:
    'The most hosts, pools, runners, join tokens and live-update streams this instance will hold. 0 is unlimited.',
  backup:
    'Copies of this database the controller takes of its own accord. The Backups page is where they are configured, listed, restored and downloaded.',
  images: 'Keeping the images your pools run up to date.',
  updates: 'Whether Zoomies asks github.com which release is current.',
  capacity_demand: 'Publishing a signed request for more hosts to an external provisioner.',
  provider: 'Renting machines from a hypervisor, and when to give them back.',
  ui: 'What the web UI opens with. An operator can pick differently on the page itself, and that browser remembers the pick; these are what somebody who has never chosen sees.',
};

/**
 * What a value looks like to a person.
 *
 * "not set" and "none" are different facts and are said differently: an empty
 * string is a setting nobody has given a value, and an empty list is a list
 * with nothing in it, which for `server.trusted_proxies` is the safe answer
 * rather than an omission.
 */
export function displayValue(setting: Setting): string {
  if (setting.secret) return setting.configured ? 'set, not shown' : 'not set';
  const raw = setting.value;
  if (raw === null || raw === undefined) return 'not set';
  const quantity = settingQuantity(setting);
  if (quantity && typeof raw === 'number') return formatQuantity(raw, quantity);
  if (quantity === 'ms' && typeof raw === 'string') {
    // The controller writes Go's spelling -- 720h -- and people read days.
    const parsed = parseQuantity(raw, 'ms');
    if (parsed.ok && parsed.value !== null) return formatQuantity(parsed.value, 'ms');
  }
  if (typeof raw === 'boolean') return raw ? 'on' : 'off';
  if (Array.isArray(raw)) return raw.length > 0 ? raw.join(', ') : 'none';
  if (typeof raw === 'object') {
    const entries = Object.entries(raw as Record<string, unknown>);
    return entries.length > 0 ? entries.map(([k, v]) => `${k}=${String(v)}`).join(' ') : 'none';
  }
  return String(raw) === '' ? 'not set' : String(raw);
}

/**
 * What a numeric or duration setting measures, so it can be shown and edited
 * the way people think of it. A duration is a length of time, shown as "7d"
 * rather than 168h. A number is a size where its key names the unit -- `_mb`
 * and `_gb` are sizes, a key ending in `cpus` is cores -- and a count
 * otherwise. Every one of them is edited with a slider and a field that reads
 * what people write: 4g, 1.5, 2 weeks.
 */
export function settingQuantity(setting: Pick<Setting, 'key' | 'kind'>): Quantity | null {
  if (setting.kind === 'duration') return 'ms';
  if (setting.kind !== 'int' && setting.kind !== 'float') return null;
  if (setting.key.endsWith('_mb')) return 'mb';
  if (setting.key.endsWith('_gb')) return 'gb';
  if (setting.key.endsWith('cpus')) return 'cpus';
  return setting.kind === 'int' ? 'count' : null;
}

/** Whether to render the value in the muted, italic "nothing here" style. */
export function isUnset(setting: Setting): boolean {
  const shown = displayValue(setting);
  return shown === 'not set' || shown === 'none';
}

/**
 * The badge that says where a value came from.
 *
 * The default layer gets no badge: it is the absence of anybody having said
 * anything, and a page of eighty-eight "Default" chips says nothing at all.
 * The other three each change what an operator should do next, which is why
 * they are worth the pixels.
 */
export function describeSource(
  setting: Setting,
): { label: string; tone: StatusTone | 'accent'; detail: string } | null {
  switch (setting.source as SettingSource) {
    case 'environment':
      return {
        label: 'From the environment',
        tone: 'pending',
        detail: `${setting.env} is setting this. The environment is the last word, so it overrides both the database and the configuration file.`,
      };
    case 'database':
      return {
        label: 'Saved here',
        tone: 'accent',
        detail: setting.updated_by
          ? `Stored in this fleet's database, last changed by ${setting.updated_by}.`
          : "Stored in this fleet's database.",
      };
    case 'file':
      return {
        label: 'From the file',
        tone: 'neutral',
        detail:
          'Set in the configuration file. Changing it here stores a value that takes precedence over the file.',
      };
    default:
      return null;
  }
}

/** Does this setting match what somebody typed into the search box? */
export function matches(setting: Setting, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return (
    setting.key.toLowerCase().includes(q) ||
    (setting.summary ?? '').toLowerCase().includes(q) ||
    (setting.env ?? '').toLowerCase().includes(q)
  );
}

/**
 * The sections this page hands to another one.
 *
 * A setting is easiest to change where its effects are visible, and the
 * backup schedule's effects are a list of backups. So the three `backup.*`
 * keys are edited on the Backups page, beside the copies they produce, and
 * this page links there rather than offering a second set of editors that
 * would disagree with the first the moment one of them was left open.
 */
export const SECTIONS_ELSEWHERE: Record<string, { href: string; page: string }> = {
  backup: { href: '/settings/backups', page: 'Backups' },
};
