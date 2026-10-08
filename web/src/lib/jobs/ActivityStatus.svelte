<script lang="ts">
  import { prefs } from '$lib/state/prefs.svelte';
  import Tooltip from '$lib/components/Tooltip.svelte';
  import OffStatusIcon from '$lib/components/OffStatusIcon.svelte';
  import ZoomiesStatusIcon from '$lib/runners/ZoomiesStatusIcon.svelte';
  import type { ActivityStatus } from './activity-status';

  let {
    activity,
    seed,
    pack = false,
  }: { activity: ActivityStatus; seed: string; pack?: boolean } = $props();
  const id = $props.id();
  let open = $state(false);
</script>

<Tooltip
  text={activity.detail}
  descriptionId={id}
  bind:open
  placement="bottom"
  class="activity-status-tip"
>
  <button
    type="button"
    class="activity-status"
    style:--activity-colour={activity.status.colour}
    aria-label="{activity.label}, {activity.status.label}: show status details"
    aria-describedby={id}
    aria-expanded={open}
    onclick={(event) => {
      event.stopPropagation();
      event.currentTarget.focus();
      open = true;
    }}
    onkeydown={(event) => {
      if (event.key === 'Enter' || event.key === ' ') event.stopPropagation();
    }}
  >
    {#if prefs.quirkyStatus}
      <span class="avatars" class:pack aria-hidden="true">
        {#if pack}
          {@const companions = activity.packMotion ?? activity.motion}
          <span class="companion left"
            ><ZoomiesStatusIcon state={companions} seed={`${seed}-left`} /></span
          >
          <span class="companion right"
            ><ZoomiesStatusIcon state={companions} seed={`${seed}-right`} /></span
          >
        {/if}
        <span class="leader"><ZoomiesStatusIcon state={activity.motion} {seed} /></span>
      </span>
    {:else}
      <OffStatusIcon status={activity.status} />
    {/if}
    <span class="labels">
      <span>{activity.label}</span>
      {#if activity.status.label !== activity.label}<small>{activity.status.label}</small>{/if}
    </span>
  </button>
</Tooltip>

<style>
  :global(.activity-status-tip) {
    max-width: 100%;
    min-width: 0;
  }
  .activity-status {
    display: inline-flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-1);
    max-width: 100%;
    padding: var(--z-space-1);
    border: var(--z-border-width) solid transparent;
    border-radius: var(--z-radius-md);
    background: transparent;
    color: var(--activity-colour);
    font: inherit;
    font-size: var(--z-text-sm);
    text-align: left;
    cursor: help;
  }
  .activity-status:hover {
    background: var(--z-surface-sunken);
    border-color: var(--z-border);
  }
  .avatars {
    display: inline-flex;
    position: relative;
    flex: none;
    width: var(--z-avatar-size);
    height: var(--z-avatar-size);
  }
  .pack {
    width: calc(var(--z-avatar-size) * 1.5);
  }
  .leader {
    display: inline-flex;
    position: relative;
    z-index: 1;
  }
  .pack .leader {
    margin-left: calc(var(--z-avatar-size) * 0.25);
    margin-top: var(--z-space-1);
  }
  .companion {
    display: inline-flex;
    position: absolute;
    top: 0;
    --z-avatar-size: 2rem;
  }
  .left {
    left: 0;
  }
  .right {
    right: 0;
  }
  .labels {
    display: grid;
    gap: var(--z-space-1);
    flex: 1 1 min-content;
    min-width: 0;
    overflow-wrap: anywhere;
    white-space: normal;
    line-height: var(--z-leading-sm);
  }
  small {
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
  }
</style>
