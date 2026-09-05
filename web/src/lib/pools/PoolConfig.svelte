<!--
  A pool's configuration, read-only.

  Deliberately a definition list and not a disabled form: an operator reading a
  pool at 2am is answering "what is this thing set to", and a grid of greyed-out
  inputs answers that far worse than plain text does. Changing it is the wizard's
  job, one deliberate click away.
-->
<script lang="ts">
  import type { Pool } from '$lib/api/types';
  import { formatGoDuration, formatMegabytes, formatNumber } from '$lib/format';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import PoolLabels from './PoolLabels.svelte';
  import { backendLabel, dockerModeLabel } from './PoolVocabulary.svelte';

  interface Props {
    pool: Pool;
    /** Off while the pool is still only a draft and has no ID yet. */
    showId?: boolean;
    class?: string;
  }

  let { pool, showId = true, class: className = '' }: Props = $props();

  const resources = $derived(pool.resources ?? {});
  const hasResources = $derived(
    resources.cpus !== undefined ||
      resources.memory_mb !== undefined ||
      resources.disk_gb !== undefined ||
      resources.pids_limit !== undefined,
  );
  const selector = $derived(Object.entries(pool.host_selector ?? {}));
  const env = $derived(Object.entries(pool.env ?? {}));
</script>

<dl class="config {className}">
  <div class="pair">
    <dt>Labels</dt>
    <dd><PoolLabels labels={pool.labels ?? []} max={12} /></dd>
  </div>

  <div class="pair">
    <dt>GitHub target</dt>
    <dd>{pool.installation_target ?? 'Not set'}</dd>
  </div>

  <div class="pair">
    <dt>Runner group</dt>
    <dd>{pool.runner_group ? pool.runner_group : 'Default'}</dd>
  </div>

  <div class="pair">
    <dt>Backend</dt>
    <dd>{backendLabel(pool.backend)}</dd>
  </div>

  <div class="pair">
    <dt>Docker in jobs</dt>
    <dd class:flagged={(pool.docker_mode ?? 'none') !== 'none'}>
      {dockerModeLabel(pool.docker_mode)}
    </dd>
  </div>

  <div class="pair">
    <dt>Runner lifetime</dt>
    <dd class:flagged={pool.ephemeral === false}>
      {pool.ephemeral === false
        ? 'Reused between jobs'
        : 'One job per runner, then it is destroyed'}
    </dd>
  </div>

  <div class="pair">
    <dt>Runs as root</dt>
    <dd class:flagged={pool.run_as_root === true}>{pool.run_as_root === true ? 'Yes' : 'No'}</dd>
  </div>

  <div class="pair">
    <dt>Scale</dt>
    <dd class="tabular">
      {formatNumber(pool.min_runners ?? 0)} minimum, {formatNumber(pool.max_runners ?? 0)} maximum
    </dd>

    <dt>Priority</dt>
    <dd>{formatNumber(pool.priority ?? 0)}</dd>
  </div>

  <div class="pair">
    <dt>Idle timeout</dt>
    <dd>{formatGoDuration(pool.idle_timeout) || 'Not set'}</dd>
  </div>

  {#if pool.image}
    <div class="pair">
      <dt>Image</dt>
      <dd><code>{pool.image}</code></dd>
    </div>
  {/if}

  {#if pool.runner_version}
    <div class="pair">
      <dt>Runner version</dt>
      <dd><code>{pool.runner_version}</code></dd>
    </div>
  {/if}

  {#if hasResources}
    <div class="pair">
      <dt>Resources per runner</dt>
      <dd class="tabular">
        {#if resources.cpus !== undefined}<span>{formatNumber(resources.cpus)} CPU</span>{/if}
        {#if resources.memory_mb !== undefined}<span>{formatMegabytes(resources.memory_mb)}</span
          >{/if}
        {#if resources.disk_gb !== undefined}<span>{formatNumber(resources.disk_gb)} GB disk</span
          >{/if}
        {#if resources.pids_limit !== undefined}<span
            >{formatNumber(resources.pids_limit)} processes</span
          >{/if}
      </dd>
    </div>
  {/if}

  {#if selector.length > 0}
    <div class="pair">
      <dt>Host selector</dt>
      <dd>
        {#each selector as [key, value] (key)}
          <code>{key}={value}</code>
        {/each}
      </dd>
    </div>
  {/if}

  {#if env.length > 0}
    <div class="pair">
      <dt>Environment</dt>
      <dd>
        {#each env as [key] (key)}
          <code>{key}</code>
        {/each}
        <span class="note">Values are not shown here.</span>
      </dd>
    </div>
  {/if}

  {#if showId}
    <div class="pair">
      <dt>Pool ID</dt>
      <dd><CopyButton value={pool.id ?? ''} label="Copy the pool ID" showValue /></dd>
    </div>
  {/if}
</dl>

<style>
  .config {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    margin: 0;
  }
  .pair {
    display: grid;
    grid-template-columns: 11rem minmax(0, 1fr);
    gap: var(--z-space-4);
    align-items: baseline;
  }
  dt {
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    color: var(--z-text-muted);
  }
  dd {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    min-width: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  dd.flagged {
    color: var(--z-pending);
    font-weight: var(--z-weight-medium);
  }
  code {
    padding: 0 var(--z-space-2);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    font-size: var(--z-text-xs);
  }
  .note {
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  @media (max-width: 768px) {
    .pair {
      grid-template-columns: minmax(0, 1fr);
      gap: var(--z-space-1);
    }
  }
</style>
