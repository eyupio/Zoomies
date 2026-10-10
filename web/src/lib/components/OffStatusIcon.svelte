<!--
  The plain status tile. A state still in progress moves a little, by glyph
  (see off-motion.ts); everything waiting or finished holds still, and anyone
  who asked for reduced motion gets none of it.
-->
<script lang="ts">
  import type { StatusMeta } from '../status';
  import { dogPhase } from '$lib/mascot/dog-motion';
  import { offMotion } from './off-motion';

  let {
    status,
    seed = '',
  }: { status: Pick<StatusMeta, 'icon' | 'colour' | 'subtle' | 'border'>; seed?: string } =
    $props();
  const motion = $derived(offMotion(status.icon));
</script>

<span
  class="off-status-icon"
  data-motion={motion}
  aria-hidden="true"
  style:--icon-colour={status.colour}
  style:--icon-subtle={status.subtle}
  style:--icon-border={status.border}
  style:--phase={dogPhase(seed)}
>
  <status.icon class="standard-icon" size={18} strokeWidth={2.1} aria-hidden="true" />
  {#if motion === 'trace'}
    <!-- A second copy of the trace carries the pulse, so the glyph itself
         stays whole and readable underneath it. -->
    <status.icon class="trace-pulse" size={18} strokeWidth={2.1} aria-hidden="true" />
  {/if}
</span>

<style>
  .off-status-icon {
    display: inline-flex;
    position: relative;
    align-items: center;
    justify-content: center;
    flex: none;
    width: var(--z-status-icon-size);
    height: var(--z-status-icon-size);
    border: var(--z-border-width) solid var(--icon-border);
    border-radius: var(--z-radius-md);
    background: var(--icon-subtle);
    color: var(--icon-colour);
  }
  .off-status-icon :global(svg) {
    flex: none;
    transform-box: fill-box;
    transform-origin: center;
  }
  /* Each row starts at its own point in the cycle, so a column of busy
     runners does not pulse in unison. */
  [data-motion='spin'] :global(.standard-icon) {
    animation: spin 1.6s linear infinite;
    animation-delay: calc(var(--phase) * -1.6s);
  }
  [data-motion='breathe'] :global(.standard-icon) {
    animation: breathe 2.8s ease-in-out infinite;
    animation-delay: calc(var(--phase) * -2.8s);
  }
  /* The hourglass sits for most of its cycle and turns over once; it is
     symmetrical, so the jump back to the start of the loop is invisible. */
  [data-motion='turn'] :global(.standard-icon) {
    animation: turn 6s var(--z-ease) infinite;
    animation-delay: calc(var(--phase) * -6s);
  }
  [data-motion='trace'] :global(.standard-icon) {
    opacity: 0.4;
  }
  .off-status-icon :global(.trace-pulse) {
    position: absolute;
    stroke-dasharray: 9 60;
    animation: pulse 2.4s linear infinite;
    animation-delay: calc(var(--phase) * -2.4s);
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  @keyframes breathe {
    50% {
      opacity: 0.55;
    }
  }
  @keyframes turn {
    0%,
    82% {
      transform: rotate(0);
    }
    94%,
    100% {
      transform: rotate(180deg);
    }
  }
  @keyframes pulse {
    from {
      stroke-dashoffset: 69;
    }
    to {
      stroke-dashoffset: 0;
    }
  }
  /* Important because a glyph's own rule outranks a plain reset here: a media
     query adds no specificity of its own. */
  @media (prefers-reduced-motion: reduce) {
    .off-status-icon :global(svg) {
      animation: none !important;
      opacity: 1;
    }
    .off-status-icon :global(.trace-pulse) {
      display: none;
    }
  }
</style>
