<!--
  One GitHub App connection.

  The card answers "can this controller still talk to GitHub for this target?".
  Health is the App's last probe result, and when it failed the error is shown
  in full rather than summarised, because it is usually the name of a permission
  somebody has to grant.
-->
<script lang="ts">
  import { BadgeCheck, Building2, Puzzle, Trash2 } from '@lucide/svelte';
  import type { Installation } from '$lib/api/types';
  import { formatNumber, pluralise } from '$lib/format';
  import { installationStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';

  export interface RateLimit {
    limit?: number;
    remaining?: number;
    reset_at?: string;
  }

  interface Props {
    installation: Installation;
    /** The last rate-limit reading, when one has been taken. */
    rate?: RateLimit;
    verifying?: boolean;
    canOperate?: boolean;
    canAdmin?: boolean;
    onverify: (installation: Installation) => void;
    ondelete: (installation: Installation) => void;
    class?: string;
  }

  let {
    installation,
    rate,
    verifying = false,
    canOperate = false,
    canAdmin = false,
    onverify,
    ondelete,
    class: className = '',
  }: Props = $props();

  const status = $derived(installationStatus(installation.healthy));
  const pools = $derived(installation.pool_count ?? 0);
  const low = $derived(
    rate?.remaining !== undefined && rate.limit ? rate.remaining / rate.limit < 0.1 : false,
  );
</script>

<article class="card {className}" aria-labelledby="installation-{installation.id}">
  <header>
    <div class="identity">
      <h3 id="installation-{installation.id}">{installation.target || 'Unnamed target'}</h3>
      <div class="badges">
        <Badge {status} size="sm" title={status.hint} />
        <Badge
          tone="neutral"
          label={installation.target_type === 'repo' ? 'Repository' : 'Organisation'}
          size="sm"
          dot={false}
        />
        {#if installation.enterprise}
          <Badge
            tone="accent"
            label="GitHub Enterprise"
            size="sm"
            dot={false}
            title={installation.api_base_url}
          />
        {:else}
          <Badge tone="neutral" label="github.com" size="sm" dot={false} />
        {/if}
      </div>
    </div>
    <div class="actions">
      <Button
        size="sm"
        variant="secondary"
        icon={BadgeCheck}
        loading={verifying}
        disabled={!canOperate}
        onclick={() => onverify(installation)}
      >
        Verify
      </Button>
      <Button
        size="sm"
        variant="ghost"
        icon={Trash2}
        disabled={!canAdmin}
        onclick={() => ondelete(installation)}
      >
        Delete
      </Button>
    </div>
  </header>

  <dl class="facts">
    <div>
      <dt>App</dt>
      <dd>
        {#if installation.app_slug}
          <span class="with-icon"
            ><Puzzle size={13} aria-hidden="true" />{installation.app_slug}</span
          >
        {:else}
          <span class="muted">App {formatNumber(installation.app_id ?? 0)}</span>
        {/if}
      </dd>
    </div>
    <div>
      <dt>Installation</dt>
      <dd class="mono">{formatNumber(installation.installation_id ?? 0)}</dd>
    </div>
    <div>
      <dt>API</dt>
      <dd class="mono api">
        {#if installation.enterprise}
          <span class="with-icon"
            ><Building2 size={13} aria-hidden="true" />{installation.api_base_url}</span
          >
        {:else}
          {installation.api_base_url || 'https://api.github.com'}
        {/if}
      </dd>
    </div>
    <div>
      <dt>Pools</dt>
      <dd>
        {#if pools > 0}
          <a href="/pools?installation={installation.id}">{pluralise(pools, 'pool')}</a>
        {:else}
          <span class="muted">None yet</span>
        {/if}
      </dd>
    </div>
    <div>
      <dt>Rate limit</dt>
      <dd class="tabular" class:low>
        {#if rate?.remaining === undefined}
          <span class="muted">Not checked</span>
        {:else}
          {formatNumber(rate.remaining)}{rate.limit ? ` of ${formatNumber(rate.limit)}` : ''} left
          {#if rate.reset_at}
            <span class="muted">· resets <RelativeTime value={rate.reset_at} plain /></span>
          {/if}
        {/if}
      </dd>
    </div>
    <div>
      <dt>Last checked</dt>
      <dd>
        {#if installation.last_checked_at}
          <RelativeTime value={installation.last_checked_at} />
        {:else}
          <span class="muted">Never</span>
        {/if}
      </dd>
    </div>
  </dl>

  {#if installation.healthy === false && installation.last_error}
    <p class="error">{installation.last_error}</p>
  {:else if installation.healthy === false}
    <p class="error">
      The last check failed and GitHub gave no reason. Verify it to see which credential or
      permission is missing.
    </p>
  {/if}
</article>

<style>
  .card {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    padding: var(--z-space-5);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
    min-width: 0;
  }
  header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--z-space-4);
    flex-wrap: wrap;
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
  .badges,
  .actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
  }
  .facts {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
    gap: var(--z-space-4);
    margin: 0;
  }
  dt {
    font-size: var(--z-text-2xs);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--z-text-muted);
  }
  dd {
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-sm);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .api {
    font-size: var(--z-text-xs);
  }
  .with-icon {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
  }
  .muted {
    color: var(--z-text-subtle);
  }
  .low {
    color: var(--z-pending);
  }
  a {
    color: var(--z-accent);
  }
  .error {
    margin: 0;
    padding: var(--z-space-3);
    border: 1px solid var(--z-danger-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-danger-subtle);
    color: var(--z-text);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    overflow-wrap: anywhere;
  }
</style>
