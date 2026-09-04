<!--
  The facts about a runner that do not change while you watch: where it came
  from, what it is made of, and when each part of its life happened.

  Identifiers are monospaced and copyable, because the next thing an operator
  does with a container ID is paste it into a shell.
-->
<script lang="ts">
  import type { RunnerDetail } from '$lib/api/types';
  import { formatNumber, shortId } from '$lib/format';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';

  interface Props {
    runner: RunnerDetail;
    class?: string;
  }

  let { runner, class: className = '' }: Props = $props();

  const labels = $derived(runner.labels ?? []);
</script>

<dl class="facts {className}">
  <div class="row">
    <dt>Pool</dt>
    <dd>
      {#if runner.pool_id}
        <a href="/pools/{runner.pool_id}">{runner.pool_name ?? runner.pool_id}</a>
      {:else}
        <span class="none">No pool</span>
      {/if}
    </dd>
  </div>

  <div class="row">
    <dt>Host</dt>
    <dd>
      {#if runner.host_name || runner.host_id}
        <a href="/hosts">{runner.host_name ?? runner.host_id}</a>
      {:else}
        <span class="none">Not placed yet</span>
      {/if}
    </dd>
  </div>

  <div class="row">
    <dt>Image</dt>
    <dd class="mono break">{runner.image || '--'}</dd>
  </div>

  <div class="row">
    <dt>Runner version</dt>
    <dd class="mono">{runner.runner_version || 'Unknown'}</dd>
  </div>

  <div class="row">
    <dt>Lifetime</dt>
    <dd>
      {runner.ephemeral === false ? 'Reused between jobs' : 'One job, then it exits'}
    </dd>
  </div>

  <div class="row">
    <dt>Labels</dt>
    <dd>
      {#if labels.length > 0}
        <ul class="labels">
          {#each labels as label (label)}
            <li class="mono">{label}</li>
          {/each}
        </ul>
      {:else}
        <span class="none">None</span>
      {/if}
    </dd>
  </div>

  {#if runner.container_id}
    <div class="row">
      <dt>Container</dt>
      <dd class="with-copy">
        <span class="mono" title={runner.container_id}>{shortId(runner.container_id, 6)}</span>
        <CopyButton value={runner.container_id} label="Copy the container ID" size="sm" />
      </dd>
    </div>
  {/if}

  {#if runner.github_runner_id}
    <div class="row">
      <dt>GitHub runner</dt>
      <dd class="tabular">{formatNumber(runner.github_runner_id)}</dd>
    </div>
  {/if}

  {#if runner.id}
    <div class="row">
      <dt>Runner ID</dt>
      <dd class="with-copy">
        <span class="mono" title={runner.id}>{shortId(runner.id)}</span>
        <CopyButton value={runner.id} label="Copy the runner ID" size="sm" />
      </dd>
    </div>
  {/if}

  <div class="row">
    <dt>Created</dt>
    <dd><RelativeTime value={runner.created_at} /></dd>
  </div>

  {#if runner.started_at}
    <div class="row">
      <dt>Registered</dt>
      <dd><RelativeTime value={runner.started_at} /></dd>
    </div>
  {/if}

  {#if runner.last_idle_at}
    <div class="row">
      <dt>Last idle</dt>
      <dd><RelativeTime value={runner.last_idle_at} /></dd>
    </div>
  {/if}

  {#if runner.finished_at}
    <div class="row">
      <dt>Finished</dt>
      <dd><RelativeTime value={runner.finished_at} /></dd>
    </div>
  {/if}
</dl>

<style>
  .facts {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    margin: 0;
  }
  .row {
    display: grid;
    grid-template-columns: 8rem 1fr;
    align-items: baseline;
    gap: var(--z-space-3);
  }
  dt {
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--z-text-subtle);
  }
  dd {
    margin: 0;
    min-width: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
  .break {
    overflow-wrap: anywhere;
  }
  .none {
    color: var(--z-text-subtle);
  }
  .with-copy {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
  }
  a {
    color: var(--z-accent);
    text-decoration: none;
  }
  a:hover {
    text-decoration: underline;
  }
  .labels {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-1);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .labels li {
    padding: 0 var(--z-space-2);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
    color: var(--z-text-muted);
  }
  @media (max-width: 768px) {
    .row {
      grid-template-columns: 1fr;
      gap: var(--z-space-1);
    }
  }
</style>
