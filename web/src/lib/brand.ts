/**
 * The brand, as it appears on GitHub.
 *
 * This is the browser's half of internal/store/brand.go. The two have to agree
 * exactly: the server brands the labels it stores, and the wizard shows the
 * operator what it is about to store. A disagreement would show one label in
 * the preview and save another.
 */

/** The product name, lowercased, as it appears in a label. */
export const BRAND = 'zoomies';

/** The one label every Zoomies pool answers to. */
export const BRAND_LABEL = BRAND;

/** What every runner name starts with. */
export const RUNNER_NAME_PREFIX = `${BRAND}-`;

/** The labels GitHub attaches to every self-hosted runner by itself. */
export const IMPLICIT_LABELS: readonly string[] = [
  'self-hosted',
  'linux',
  'windows',
  'macos',
  'x64',
  'arm64',
  'arm',
];

/** How long the part of a label derived from a pool name may be. */
const MAX_LABEL_SEGMENT = 40;

export function isImplicit(label: string): boolean {
  return IMPLICIT_LABELS.includes(label.trim().toLowerCase());
}

/** Lowercase and trim, which is how GitHub compares two labels. */
export function normalizeLabel(label: string): string {
  return label.trim().toLowerCase();
}

/** Lowercase, de-duplicate and sort, which is what the server stores. */
export function normalizeLabels(labels: readonly string[]): string[] {
  const seen = new Set<string>();
  for (const label of labels) {
    const l = normalizeLabel(label);
    if (l !== '') seen.add(l);
  }
  return [...seen].sort();
}

/**
 * Reduce an arbitrary string to the characters GitHub accepts in a label:
 * lowercase letters, digits and single hyphens, trimmed.
 */
export function sanitizeLabel(value: string): string {
  const collapsed = normalizeLabel(value)
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
  return collapsed.slice(0, MAX_LABEL_SEGMENT).replace(/-+$/, '');
}

/**
 * The label a pool of this name should answer to: the brand, then the name.
 * A name that already starts with the brand keeps it rather than gaining a
 * second one.
 */
export function brandedLabel(name: string): string {
  const s = sanitizeLabel(name);
  if (s === '' || s === BRAND) return BRAND_LABEL;
  return s.startsWith(RUNNER_NAME_PREFIX) ? s : RUNNER_NAME_PREFIX + s;
}

/**
 * The name a pool of this name will be saved under: the brand, then the name.
 *
 * The server brands every pool name on the way in, so a field left saying
 * "gpu" would be showing the operator a name that is not the one they get.
 * A name that already carries the brand keeps it rather than gaining a second.
 * An empty name stays empty, because "give the pool a name" is what should be
 * said about it, not "zoomies-".
 */
export function brandedName(name: string): string {
  const s = name.trim();
  const lower = normalizeLabel(s);
  if (s === '' || lower === BRAND || lower.startsWith(RUNNER_NAME_PREFIX)) return s;
  return RUNNER_NAME_PREFIX + s.replace(/^-+/, '');
}

/** These labels, with the brand guaranteed present. */
export function brandLabels(labels: readonly string[]): string[] {
  return normalizeLabels([BRAND_LABEL, ...labels]);
}

/**
 * The shortest `runs-on` value that reaches a pool with these labels.
 *
 * One label of its own is enough, and it is what a workflow should write:
 * "runs-on: zoomies-linux-x64" says where the job runs. Where a pool needs
 * several labels to be identified, the list form is rendered instead, because
 * dropping one would send the job somewhere else.
 */
export function runsOn(labels: readonly string[]): string {
  const all = normalizeLabels(labels);
  const specific = all.filter((l) => !isImplicit(l) && l !== BRAND_LABEL);
  if (specific.length === 1) return specific[0]!;
  if (specific.length === 0) return all.includes(BRAND_LABEL) ? BRAND_LABEL : 'self-hosted';
  return `[${specific.join(', ')}]`;
}

/** The workflow snippet a `runs-on` value belongs in. */
export function runsOnSnippet(labels: readonly string[]): string {
  return `jobs:\n  build:\n    runs-on: ${runsOn(labels)}`;
}
