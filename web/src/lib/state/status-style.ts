/** One choice controls the vocabulary and the illustration across all status surfaces. */
export const STATUS_STYLES = ['off', 'cute', 'standard'] as const;
export type StatusStyle = (typeof STATUS_STYLES)[number];

/**
 * What a browser that has never chosen starts on: a new install, and one whose
 * saved preferences were cleared or could not be read. Plain words are the
 * ones an operator new to the fleet can match against GitHub's own runner
 * page and against the docs, and the dogs are one press away in Appearance
 * for anyone who wants them.
 */
export const DEFAULT_STATUS_STYLE: StatusStyle = 'off';

export const STATUS_STYLE_OPTIONS = [
  { value: 'off', label: 'Off', name: 'Plain status names and icons' },
  { value: 'cute', label: 'Cute', name: 'Zoomies words and the original animated dog' },
  {
    value: 'standard',
    label: 'Standard',
    name: 'Zoomies words and the dog icon with its paw prints',
  },
] as const;

export function isStatusStyle(value: unknown): value is StatusStyle {
  return STATUS_STYLES.some((style) => style === value);
}

/**
 * The saved style, or the default where there is none.
 *
 * A browser from before the three-way choice stored only the old boolean, and
 * with it on it was showing the kennel words, so it keeps them: moving an
 * existing operator's screen under them on upgrade is not what changing the
 * default means. Anything else -- nothing stored, or a value this build does
 * not recognise -- is a browser that has not chosen, and gets the default.
 */
export function resolveStatusStyle(stored: {
  statusStyle?: unknown;
  quirkyStatus?: unknown;
}): StatusStyle {
  if (isStatusStyle(stored.statusStyle)) return stored.statusStyle;
  if (stored.quirkyStatus === true) return 'cute';
  if (stored.quirkyStatus === false) return 'off';
  return DEFAULT_STATUS_STYLE;
}
