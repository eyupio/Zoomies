<!--
  One small pill, and the card it opens.

  It is a button because a tooltip that only hover reaches is a tooltip a keyboard
  and a finger cannot read: focus opens the same card hover does, and a tap on a
  touch screen opens it and leaves it until the next tap elsewhere. The card is
  the whole of what the pill means -- the pill is only a few words and a glyph --
  so the sentence for assistive technology is the card's, not the pill's.
-->
<script lang="ts">
  import Tooltip from '$lib/components/Tooltip.svelte';
  import BadgeIcon from './BadgeIcon.svelte';
  import MemoryBadgeCard from './MemoryBadgeCard.svelte';
  import type { MemoryBadge } from './memory-badges';

  let { badge, placement = 'bottom' }: { badge: MemoryBadge; placement?: 'top' | 'bottom' } =
    $props();

  const componentId = $props.id();
  const descriptionId = `${componentId}-description`;
  let open = $state(false);
</script>

<Tooltip text={badge.text} {descriptionId} bind:open {placement} wide class="memory-badge-tip">
  {#snippet content()}
    <MemoryBadgeCard {badge} />
  {/snippet}
  <button
    type="button"
    class="pill"
    class:dashed={badge.dashed}
    data-tone={badge.tone}
    data-badge={badge.key}
    data-wanting={badge.wanting || undefined}
    aria-label="{badge.title}{badge.wanting ? ', and it wants more' : ''}: show details"
    aria-describedby={descriptionId}
    aria-expanded={open}
    onclick={(event) => {
      // Row clicks open the runner; a pill is for reading, not for navigating.
      event.stopPropagation();
      event.currentTarget.focus();
      open = true;
    }}
    onkeydown={(event) => {
      if (event.key === 'Enter' || event.key === ' ') event.stopPropagation();
    }}
  >
    <span class="icons">
      {#each badge.icons as icon (icon)}<BadgeIcon {icon} />{/each}
    </span>
    <span class="label">{badge.label}</span>
  </button>
</Tooltip>

<style>
  :global(.memory-badge-tip) {
    min-width: 0;
    max-width: 100%;
  }
  .pill {
    --pill-colour: var(--z-neutral);
    --pill-subtle: var(--z-neutral-subtle);
    --pill-border: var(--z-neutral-border);
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    height: var(--z-space-5);
    /* A pill is as wide as what is in it: a column that is short of room wraps
       its pills onto another line, and never squeezes one until its words are gone
       and only a glyph is left, which is what an ellipsis here would do. */
    flex: none;
    padding: 0 var(--z-space-2) 0 var(--z-space-1);
    border: var(--z-border-width) solid var(--pill-border);
    border-radius: var(--z-radius-full);
    background: var(--pill-subtle);
    color: var(--pill-colour);
    font: inherit;
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    line-height: 1;
    white-space: nowrap;
    cursor: help;
    transition:
      border-color var(--z-motion-fast),
      background var(--z-motion-fast);
  }
  .pill[data-tone='accent'] {
    --pill-colour: var(--z-accent);
    --pill-subtle: var(--z-accent-subtle);
    --pill-border: var(--z-accent-border);
  }
  .pill[data-tone='pending'] {
    --pill-colour: var(--z-pending);
    --pill-subtle: var(--z-pending-subtle);
    --pill-border: var(--z-pending-border);
  }
  .pill[data-tone='danger'] {
    --pill-colour: var(--z-danger);
    --pill-subtle: var(--z-danger-subtle);
    --pill-border: var(--z-danger-border);
  }
  /* Something that has not happened: what an observing valve would do, or a folder
     the pool wanted in memory and this runner was not given. */
  .pill.dashed {
    border-style: dashed;
    background: transparent;
  }
  .pill:hover {
    border-color: var(--pill-colour);
  }
  .icons {
    display: inline-flex;
    align-items: center;
    gap: var(--z-nudge-1);
    flex: none;
  }
  .label {
    font-variant-numeric: tabular-nums;
  }
  /* The runner wants more and cannot have it: a dot, because a colour alone is
     not a signal, and the card says what it is. */
  .pill[data-wanting]::after {
    content: '';
    flex: none;
    width: var(--z-space-1);
    height: var(--z-space-1);
    margin-left: var(--z-nudge-1);
    border-radius: var(--z-radius-full);
    background: var(--z-pending);
  }
</style>
