import type { Pool, Provider } from '$lib/api/types';

/*
  The two halves of a pool's agreement with a provider, mirrored from
  internal/scheduler/providers.go for the one place the page cannot wait for the
  server: the editor of a selector, which says which providers (or pools) the
  selector being typed would let through. Every saved pairing is read from
  GET /providers/pairings instead, so the Go rule stays the only one that acts.
*/

/** What a pool answers for a key in a provider's `pool_selector`, or undefined if it has no answer. */
export function poolSelectorValue(
  pool: Pick<Pool, 'name' | 'backend' | 'labels'>,
  key: string,
): string | undefined {
  const k = key.toLowerCase();
  if (k === 'name') return pool.name ?? '';
  if (k === 'backend') return pool.backend ? String(pool.backend) : undefined;
  for (const label of pool.labels ?? []) {
    if (label.toLowerCase() === k) return '';
    const at = label.indexOf('=');
    if (at > 0 && label.slice(0, at).toLowerCase() === k) return label.slice(at + 1);
  }
  return undefined;
}

/** What a provider answers for a key in a pool's `provider_selector`, or undefined if it has no answer. */
export function providerSelectorValue(
  provider: Pick<Provider, 'name' | 'kind' | 'machine_labels'>,
  key: string,
): string | undefined {
  const k = key.toLowerCase();
  if (k === 'name') return provider.name ?? '';
  if (k === 'kind') return provider.kind ?? '';
  const labels = provider.machine_labels ?? {};
  return key in labels ? labels[key] : undefined;
}

function satisfies(
  selector: Record<string, string>,
  value: (key: string) => string | undefined,
): boolean {
  return Object.entries(selector).every(([key, want]) => {
    const got = value(key);
    if (got === undefined) return false;
    // An entry with no value asks only that the subject carries the key.
    return want === '' || got.toLowerCase() === want.toLowerCase();
  });
}

/** Whether a pool satisfies a provider's `pool_selector`. Empty means every pool. */
export function poolMatchesSelector(
  pool: Pick<Pool, 'name' | 'backend' | 'labels'>,
  selector: Record<string, string>,
): boolean {
  return satisfies(selector, (key) => poolSelectorValue(pool, key));
}

/** Whether a provider satisfies a pool's `provider_selector`. Empty means any provider. */
export function providerMatchesSelector(
  provider: Pick<Provider, 'name' | 'kind' | 'machine_labels'>,
  selector: Record<string, string>,
): boolean {
  return satisfies(selector, (key) => providerSelectorValue(provider, key));
}
