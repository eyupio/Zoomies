<!--
  One provider: where machines come from, what it is renting now, and whether
  it is allowed to rent any more.

  The card leads with what is holding it back rather than with what it is,
  because that is the question somebody has this page open for. `held` is the
  controller's own sentence and answers for three switches at once -- the
  fence, the configuration and the row -- so it is shown verbatim; working one
  out here would have the card disagree with the reconciler.
-->
<script lang="ts">
  import { Pause, Play, Stethoscope } from '@lucide/svelte';
  import type { Provider } from '$lib/api/types';
  import { formatNumber, pluralise } from '$lib/format';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import RemedyText from '$lib/components/RemedyText.svelte';
  import Tooltip from '$lib/components/Tooltip.svelte';

  interface Props {
    provider: Provider;
    canOperate?: boolean;
    /** Busy while its own preflight is in flight, so the button cannot be double-pressed. */
    checking?: boolean;
    /** Busy while its own pause/resume is in flight, so the button cannot be double-pressed. */
    pausing?: boolean;
    oncheck: (provider: Provider) => void;
    onpause: (provider: Provider, paused: boolean) => void;
    class?: string;
  }

  let {
    provider,
    canOperate = false,
    checking = false,
    pausing = false,
    oncheck,
    onpause,
    class: className = '',
  }: Props = $props();

  /** Why Check and Pause are greyed out for a viewer -- the badges above already say why the card itself is held; this is why *acting* on it is refused. */
  const restricted = 'Needs the operator role. An administrator can grant it under Settings.';
  const owned = $derived(provider.owned ?? 0);
  const ceiling = $derived(provider.max_machines ?? 0);
  const counts = $derived(Object.entries(provider.machines ?? {}).filter(([, n]) => n > 0));
  const shape = $derived.by(() => {
    const parts: string[] = [];
    if (provider.machine_cpus) parts.push(`${formatNumber(provider.machine_cpus)} vCPU`);
    if (provider.machine_memory_mb)
      parts.push(`${formatNumber(Math.round(provider.machine_memory_mb / 1024))} GB`);
    parts.push(pluralise(provider.machine_capacity ?? 0, 'runner slot'));
    return parts.join(' · ');
  });
</script>

<article class="card {className}" aria-labelledby="provider-{provider.id}-name">
  <header>
    <div class="identity">
      <h3 id="provider-{provider.id}-name" tabindex="-1">
        <a href="/providers/{provider.id}">{provider.name || provider.id}</a>
      </h3>
      <div class="badges">
        <!-- Neutral and accent, never the status palette: which hypervisor this
             is and whether a credential is stored are facts about a provider,
             not states of one, and the six status hues already mean something
             an operator has learned. -->
        <Badge tone="neutral" label={provider.kind ?? 'unknown'} size="sm" dot={false} />
        {#if provider.credentials_configured !== true}
          <Badge
            tone="pending"
            label="No credential"
            size="sm"
            dot={false}
            title="Nothing can be created until a credential is stored."
          />
        {/if}
        {#if provider.paused}
          <Badge
            tone="draining"
            label="Paused"
            size="sm"
            dot={false}
            title={provider.paused_reason || 'New machines are held. Drains and deletes continue.'}
          />
        {/if}
        {#if provider.enabled === false}
          <Badge tone="draining" label="Disabled" size="sm" dot={false} />
        {/if}
        {#if provider.connection === 'tailcat'}
          <Badge
            tone="accent"
            label="Tailcat"
            size="sm"
            dot={false}
            title="Reached through a zoomies gateway over a private encrypted connection. The gateway's address is sealed and never shown."
          />
        {/if}
        {#if provider.insecure_skip_verify}
          <Badge
            tone="danger"
            label="Certificate unchecked"
            size="sm"
            dot={false}
            title="Zoomies does not verify the provider's certificate, so the credential travels to whatever answers at that address."
          />
        {/if}
      </div>
    </div>
  </header>

  <p class="meta">
    {#if provider.endpoint}<span class="mono">{provider.endpoint}</span>{/if}
    {#if shape}<span>{shape}</span>{/if}
    {#if provider.machine_backend}<span>{provider.machine_backend}</span>{/if}
  </p>

  {#if provider.held}
    <p class="held"><RemedyText text={provider.held} /></p>
  {/if}

  {#if provider.last_check_error}
    <p class="held bad"><RemedyText text={provider.last_check_error} /></p>
  {/if}

  <p class="figures tabular">
    <strong>{formatNumber(owned)}</strong>
    of {pluralise(ceiling, 'machine')}
    {#if ceiling === 0}
      <span class="muted">· a ceiling of zero rents nothing</span>
    {/if}
  </p>

  {#if counts.length > 0}
    <ul class="counts">
      {#each counts as [state, count] (state)}
        <li>{state} {formatNumber(count)}</li>
      {/each}
    </ul>
  {/if}

  <p class="checked">
    {#if provider.last_check_at}
      Last checked <RelativeTime value={provider.last_check_at} plain />
    {:else}
      Never checked. Run it before anything is built on this.
    {/if}
  </p>

  <div class="actions">
    {#snippet checkButton()}
      <Button
        size="sm"
        icon={Stethoscope}
        disabled={!canOperate || checking}
        onclick={() => oncheck(provider)}
      >
        {checking ? 'Checking…' : 'Check'}
      </Button>
    {/snippet}
    {#if canOperate}
      {@render checkButton()}
    {:else}
      <Tooltip text={restricted}>{@render checkButton()}</Tooltip>
    {/if}
    {#snippet pauseButton()}
      <Button
        size="sm"
        icon={provider.paused ? Play : Pause}
        disabled={!canOperate || pausing}
        onclick={() => onpause(provider, !provider.paused)}
      >
        {#if pausing}
          {provider.paused ? 'Resuming…' : 'Pausing…'}
        {:else}
          {provider.paused ? 'Resume' : 'Pause'}
        {/if}
      </Button>
    {/snippet}
    {#if canOperate}
      {@render pauseButton()}
    {:else}
      <Tooltip text={restricted}>{@render pauseButton()}</Tooltip>
    {/if}
    <Button size="sm" variant="secondary" href="/providers/{provider.id}">Open</Button>
  </div>
</article>

<style>
  .card {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    padding: var(--z-space-5);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
    min-width: 0;
  }
  header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--z-space-3);
  }
  .identity {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    min-width: 0;
  }
  h3 {
    margin: 0;
    font-size: var(--z-text-lg);
    line-height: var(--z-leading-lg);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  h3 a {
    color: inherit;
    text-decoration: none;
  }
  h3 a:hover {
    text-decoration: underline;
  }
  .badges {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2);
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-3);
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
    overflow-wrap: anywhere;
  }
  .held {
    margin: 0;
    padding: var(--z-space-2) var(--z-space-3);
    border: var(--z-border-width) solid var(--z-draining-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-draining-subtle);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .held.bad {
    border-color: var(--z-danger-border);
    background: var(--z-danger-subtle);
  }
  .figures {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .figures strong {
    color: var(--z-text);
    font-size: var(--z-text-lg);
    font-weight: var(--z-weight-semibold);
  }
  .muted {
    color: var(--z-text-subtle);
  }
  .counts {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-1);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .counts li {
    padding: 0 var(--z-space-1);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
  }
  .checked {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .actions {
    /* Against the foot of the card, so a row of stretched cards puts every set
       of buttons on one line rather than wherever its own prose ran out. */
    margin-top: auto;
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    padding-top: var(--z-space-3);
    border-top: var(--z-border-width) solid var(--z-border);
  }
</style>
