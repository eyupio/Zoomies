<!--
  Which providers may rent machines for this pool.

  A pool never names a provider; renting is the answer to one situation (work is
  queued and no host can run it), and what this section controls is who is
  allowed to answer. It is the pool's half of an agreement: the provider has a
  half too (its pool selector), and a machine is rented only where both agree,
  the way a pool's host selector and a host's labels have to. So the list below
  is not "the providers this pool picked" but each provider's answer, with the
  side that refused named, because an operator whose queue is not moving needs
  to know whose setting to open.
-->
<script lang="ts">
  import { Cloud } from '@lucide/svelte';
  import { untrack } from 'svelte';
  import type { Provider, ProviderPairing } from '$lib/api/types';
  import LabelMapEditor from '$lib/hosts/LabelMapEditor.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import { poolMatchesSelector, providerMatchesSelector } from '$lib/providers/pairing';
  import type { PoolDraft } from './draft';

  interface Props {
    draft: PoolDraft;
    /** Null until the providers have been asked for, so a slow page does not claim there are none. */
    providers: readonly Provider[] | null;
    /** The server's reading of this pool as saved, for what a selector cannot tell: whether the machine fits. */
    pairings: readonly ProviderPairing[];
  }

  let { draft, providers, pairings }: Props = $props();

  // The editor owns its rows: a row with an empty key is half typed, and the
  // map the draft holds cannot represent one.
  let rows = $state(
    untrack(() => Object.entries(draft.provider_selector).map(([key, value]) => ({ key, value }))),
  );
  $effect(() => {
    const map: Record<string, string> = {};
    for (const row of rows) {
      if (row.key.trim() === '') continue;
      map[row.key.trim()] = row.value;
    }
    draft.provider_selector = map;
  });

  const thisPool = $derived({
    name: draft.name,
    backend: draft.backend,
    labels: draft.labels.map((label) => label.trim()).filter(Boolean),
  });

  interface Verdict {
    provider: Provider;
    serves: boolean;
    /** Whose setting says no, and what it says. Empty when it serves. */
    why: string;
  }

  const verdicts = $derived.by<Verdict[]>(() =>
    (providers ?? []).map((provider) => {
      if (!providerMatchesSelector(provider, draft.provider_selector)) {
        return {
          provider,
          serves: false,
          why: 'This pool’s provider selector leaves it out.',
        };
      }
      const theirs = provider.pool_selector ?? {};
      if (!poolMatchesSelector(thisPool, theirs)) {
        const rules = Object.entries(theirs)
          .map(([key, value]) => (value === '' ? key : `${key}=${value}`))
          .join(', ');
        return {
          provider,
          serves: false,
          why: `The provider only rents for pools matching ${rules}, and this pool does not. Change its pool selector, or add that label here.`,
        };
      }
      // What a selector cannot know: whether the machine suits. Only a saved
      // pool has an answer, from the same rule the controller buys by.
      const saved = pairings.find((entry) => entry.provider_id === provider.id);
      if (saved && saved.fits === false) {
        return {
          provider,
          serves: false,
          why: saved.fit_why || 'The machines it builds do not suit this pool.',
        };
      }
      return { provider, serves: true, why: '' };
    }),
  );
</script>

<p class="how">
  When work is queued for this pool and no host can run it, a provider may rent a machine. It does
  so only if <strong>this pool</strong> allows that provider (the rule below), the
  <strong>provider</strong> allows this pool (its own pool selector), and the machine it builds suits
  the pool. Leave the rule empty to allow any provider.
</p>

<LabelMapEditor
  bind:rows
  label="Provider selector"
  noun="rule"
  empty="Any provider. Nothing here narrows which providers may rent for this pool."
/>
<p class="hint">
  A key is a provider’s <code>name</code>, its <code>kind</code>, or one of its machine labels. A
  value left empty asks only that the label exists.
</p>

{#if providers === null}
  <p class="muted" aria-busy="true">Reading the providers…</p>
{:else if providers.length === 0}
  <div class="empty">
    <p class="title"><Cloud size={16} aria-hidden="true" /> No provider is set up</p>
    <p class="body">
      Without one, work no host can run waits until you add a host. <a href="/providers/new"
        >Add a provider</a
      > to have machines rented for it.
    </p>
  </div>
{:else}
  <ul class="verdicts" aria-label="What each provider would do for this pool">
    {#each verdicts as v (v.provider.id)}
      <li>
        <div class="line">
          <a class="name" href="/providers/{v.provider.id}">{v.provider.name}</a>
          <Badge tone="neutral" label={v.provider.kind ?? 'unknown'} size="sm" dot={false} />
          {#if v.serves}
            <Badge tone="accent" label="Would rent for this pool" size="sm" dot={false} />
          {:else}
            <Badge tone="neutral" label="Would not rent for this pool" size="sm" dot={false} />
          {/if}
        </div>
        {#if !v.serves}<p class="why">{v.why}</p>{/if}
        {#if v.serves && v.provider.held}
          <p class="why">Not buying right now: {v.provider.held}</p>
        {/if}
      </li>
    {/each}
  </ul>
{/if}

<style>
  .how,
  .hint,
  .muted {
    margin: 0;
    max-width: 70ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .how strong {
    color: var(--z-text);
    font-weight: var(--z-weight-semibold);
  }
  code {
    font-family: var(--z-font-mono);
  }
  .verdicts {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    margin: 0;
    padding: 0;
    list-style: none;
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
    max-width: 70ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .empty {
    padding: var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
  }
  .title {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .body {
    margin: var(--z-space-2) 0 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
</style>
