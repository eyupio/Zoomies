import { formatNumber } from '../format';

/**
 * A check's current or recommended value as a person reads it: a plain whole
 * number of four to fifteen digits gets thousands separators, anything else is
 * left exactly as the agent wrote it.
 *
 * Those two columns exist to be compared ("65536" against "524288" is slow,
 * "65,536" against "524,288" is not), and docs/host-health.md already writes the
 * numbers that way. Fifteen digits is the most a double holds exactly, so
 * 9223372036854775807 -- the kernel's own "no limit" -- is not turned into a
 * rounded number that was never reported. A leading zero marks something that is
 * not a quantity (a mode, an identifier), and a sign or a unit is not a plain
 * number, so none of those is touched.
 */
export function niceValue(value: string): string {
  return /^[1-9]\d{3,14}$/.test(value) ? formatNumber(Number(value)) : value;
}
