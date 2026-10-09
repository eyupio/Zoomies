<!--
  Which pools this provider would rent machines for, and why not for the rest.

  A provider is not attached to a pool; it answers when a pool has queued work
  and no host can run it. Whether it may answer is an agreement between the two
  sides, so each row names the side that refused: a pool is left out by this
  provider's own pool selector or by the pool's provider selector, and the
  sentence says which one to open.
-->
<script lang="ts">
  import type { Provider, ProviderPairing } from '$lib/api/types';
  import { pluralise } from '$lib/format';
  import Badge from '$lib/components/Badge.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import RemedyText from '$lib/components/RemedyText.svelte';

  interface Props {
    provider: Provider;
    /** Every pool against this provider, or null until they have been asked for. */
    pairings: readonly ProviderPairing[] | null;
  }

  let { provider, pairings }: Props = $props();

  const serving = $derived((pairings ?? []).filter((entry) => entry.serves));
  const selector = $derived(Object.entries(provider.pool_selector ?? {}));
</script>

<div class="tab">
  <p class="how">
    A pool does not pick a provider. When a pool has queued work and no host can run it, the
    controller asks the providers that may rent for that pool to build a machine, and the machine
    becomes a host. A provider may rent for a pool only if <strong>both</strong> allow it: this
    provider's pool selector
    {#if selector.length === 0}
      (empty: every pool)
    {:else}
      (<span class="mono">{selector.map(([k, v]) => (v === '' ? k : `${k}=${v}`)).join(', ')}</span
      >)
    {/if}
    and the pool's own provider selector, and the machine has to suit the pool.
  </p>

  {#if pairings === null}
    <p class="muted" aria-busy="true">Reading the pools…</p>
  {:else if pairings.length === 0}
    <EmptyState
      compact
      title="No pools yet"
      description="Create a pool and it will be offered here. Nothing is rented until a pool has work no host can run."
    />
  {:else}
    <p class="count" data-testid="provider-serves">
      Rents for {pluralise(serving.length, 'pool')} of {pairings.length}.
      {#if serving.length === 0}
        No pool can use this provider yet, so nothing will be rented from it until one can.
      {/if}
    </p>
    <ul class="pools">
      {#each pairings as entry (entry.pool_id)}
        <li>
          <div class="line">
            <a class="name" href="/pools/{entry.pool_id}">{entry.pool_name}</a>
            {#if entry.serves}
              <Badge tone="accent" label="Would rent" size="sm" dot={false} />
            {:else}
              <Badge tone="neutral" label="Would not rent" size="sm" dot={false} />
            {/if}
          </div>
          {#if !entry.serves && entry.why}
            <p class="why"><RemedyText text={entry.why} /></p>
            {#if entry.fix}<p class="why fix"><RemedyText text={entry.fix} /></p>{/if}
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .tab {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
  }
  .how,
  .count,
  .muted {
    margin: 0;
    max-width: 72ch;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
  }
  .how strong {
    color: var(--z-text);
    font-weight: var(--z-weight-semibold);
  }
  .mono {
    font-family: var(--z-font-mono);
  }
  .count {
    color: var(--z-text);
  }
  .pools {
    display: flex;
    flex-direction: column;
    margin: 0;
    padding: 0;
    list-style: none;
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  li {
    padding: var(--z-space-3) var(--z-space-4);
  }
  li + li {
    border-top: var(--z-border-width) solid var(--z-border);
  }
  .line {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
  }
  .name {
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .why {
    margin: var(--z-nudge-1) 0 0;
    max-width: 72ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .fix {
    color: var(--z-text);
  }
</style>
