<!--
  The only loading affordance for content. Spinners are for in-flight actions.

  A skeleton must be the shape of what is coming, or it is just a grey box that
  moves. Pass the width and height of the real thing.
-->
<script lang="ts">
  interface Props {
    width?: string;
    height?: string;
    radius?: 'sm' | 'md' | 'full';
    /** Renders several lines of text, the last one short, as prose does. */
    lines?: number;
    class?: string;
  }

  let {
    width = '100%',
    height = '1rem',
    radius = 'sm',
    lines = 1,
    class: className = '',
  }: Props = $props();
</script>

{#if lines > 1}
  <div class="stack {className}" aria-hidden="true">
    {#each Array.from({ length: lines }, (_, i) => i) as line (line)}
      <span
        class="bar"
        style="width: {line === lines - 1
          ? '60%'
          : width}; height: {height}; border-radius: var(--z-radius-{radius})"
      ></span>
    {/each}
  </div>
{:else}
  <span
    class="bar {className}"
    aria-hidden="true"
    style="width: {width}; height: {height}; border-radius: var(--z-radius-{radius})"
  ></span>
{/if}

<style>
  .stack {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
  }
  .bar {
    display: block;
    background: linear-gradient(
      90deg,
      var(--z-surface-sunken) 0%,
      var(--z-border) 50%,
      var(--z-surface-sunken) 100%
    );
    background-size: 200% 100%;
    animation: shimmer calc(var(--z-motion-slow) * 4) var(--z-ease) infinite;
  }
  @keyframes shimmer {
    to {
      background-position: -200% 0;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .bar {
      animation: none;
      background: var(--z-surface-sunken);
    }
  }
</style>
