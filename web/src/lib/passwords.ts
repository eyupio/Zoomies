/**
 * The one place the browser knows how long a password has to be.
 *
 * It is `auth.MinPasswordLength` in Go and `minLength: 12` in
 * api/openapi.yaml, and it was a literal in two Svelte components that had no
 * way of noticing when it changed. Length rather than a zoo of character
 * classes is the deliberate choice -- see internal/auth/auth.go, which explains
 * why.
 */
export const MIN_PASSWORD_LENGTH = 12;

/**
 * A hint that helps rather than scolds. It counts up to the minimum and then
 * says what actually makes a password stronger, which is more of it.
 */
export function passwordStrength(password: string): string {
  if (password.length === 0) {
    return `At least ${MIN_PASSWORD_LENGTH} characters. A phrase works well.`;
  }
  if (password.length < MIN_PASSWORD_LENGTH) {
    return `${password.length} of ${MIN_PASSWORD_LENGTH} characters.`;
  }
  if (password.length < 20) return 'Good. Longer is stronger than more punctuation.';
  return 'Strong.';
}
