<script lang="ts">
  import type { Runner } from '$lib/api/types';
  import { formatNumber } from '$lib/format';
  import { prefs } from '$lib/state/prefs.svelte';
  import Tooltip from '$lib/components/Tooltip.svelte';
  import OffStatusIcon from '$lib/components/OffStatusIcon.svelte';
  import ZoomiesStatusIcon from './ZoomiesStatusIcon.svelte';
  import { runnerDisplayStatus } from './runner-status';

  let { runner }: { runner: Pick<Runner, 'id' | 'state' | 'cpu_resource'> } = $props();
  const componentId = $props.id();
  const descriptionId = `${componentId}-status-description`;
  let detailsOpen = $state(false);
  const status = $derived(runnerDisplayStatus(runner, prefs.quirkyStatus));
  const resource = $derived(runner.cpu_resource);
  const cpu = (value: number | undefined) =>
    value === undefined ? 'Not reported' : formatNumber(value);
  const description = $derived(
    [
      status.active ? `${status.lifecycle.label}. ${status.title}.` : '',
      status.detail,
      status.cpuDetail,
      resource
        ? `${status.canBoost ? 'CPU' : 'Last reported CPU'}: ${cpu(resource.current_cpus)} current, ${cpu(resource.guaranteed_cpus)} guaranteed, ${cpu(resource.ceiling_cpus)} ceiling.`
        : '',
    ]
      .filter(Boolean)
      .join(' '),
  );
</script>

<Tooltip
  text={description}
  {descriptionId}
  bind:open={detailsOpen}
  placement="bottom"
  class="runner-status-tip"
>
  {#snippet content()}
    <span class="details">
      <span class="eyebrow">{status.lifecycle.label}{status.active ? ' · Elastic CPU' : ''}</span>
      <strong>{status.title}</strong>
      <span>{status.detail}</span>
      {#if status.cpuDetail}<span>{status.cpuDetail}</span>{/if}
      {#if resource}
        <span class="cpu-title"
          >{status.canBoost ? 'CPU allocation' : 'Last reported CPU allocation'}</span
        >
        <span class="figures">
          <span><b>{cpu(resource.current_cpus)}</b><small>Current</small></span>
          <span><b>{cpu(resource.guaranteed_cpus)}</b><small>Guaranteed</small></span>
          <span><b>{cpu(resource.ceiling_cpus)}</b><small>Ceiling</small></span>
        </span>
      {/if}
    </span>
  {/snippet}
  <button
    type="button"
    class="runner-status"
    class:active={status.active}
    data-runner-status={status.key}
    aria-label="{status.label}: show status details"
    aria-describedby={descriptionId}
    aria-expanded={detailsOpen}
    style:--status-colour={status.colour}
    style:--status-subtle={status.subtle}
    style:--status-border={status.border}
    onclick={(event) => {
      event.stopPropagation();
      event.currentTarget.focus();
      detailsOpen = true;
    }}
    onkeydown={(event) => {
      if (event.key === 'Enter' || event.key === ' ') event.stopPropagation();
    }}
  >
    {#if prefs.quirkyStatus}
      <ZoomiesStatusIcon state={status.key} seed={runner.id} />
    {:else}
      <OffStatusIcon {status} seed={runner.id} />
    {/if}
    <span class="label">{status.label}</span>
  </button>
</Tooltip>

<style>
  :global(.runner-status-tip) {
    max-width: 100%;
    min-width: 0;
  }
  .runner-status {
    display: inline-flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-1);
    max-width: 100%;
    min-width: 0;
    padding: var(--z-space-1);
    border: var(--z-border-width) solid transparent;
    border-radius: var(--z-radius-md);
    background: transparent;
    color: var(--status-colour);
    font: inherit;
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
    text-align: left;
    cursor: help;
    white-space: normal;
    transition:
      background var(--z-motion-fast),
      border-color var(--z-motion-fast);
  }
  .runner-status.active {
    background: var(--status-subtle);
    border-color: var(--status-border);
  }
  .runner-status:hover {
    background: var(--status-subtle);
    border-color: var(--status-border);
  }
  .label {
    min-width: 0;
    flex: 1 1 min-content;
    overflow-wrap: anywhere;
    line-height: var(--z-leading-sm);
  }
  .details {
    display: grid;
    gap: var(--z-space-2);
    padding: var(--z-space-1);
    text-align: left;
  }
  .eyebrow,
  .cpu-title {
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
  }
  strong {
    font-size: var(--z-text-sm);
  }
  .figures {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: var(--z-space-3);
  }
  .figures > span {
    display: grid;
    gap: var(--z-space-1);
  }
  b {
    font-variant-numeric: tabular-nums;
    font-size: var(--z-text-sm);
  }
  small {
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
  }
</style>
