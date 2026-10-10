<!--
  Eli's face wherever Eli speaks. A dog on the accent's own tint, from the same
  tokens as everything else, so it follows the theme.
-->
<script lang="ts">
  import { Dog } from '@lucide/svelte';
  import { dogPhase } from '$lib/mascot/dog-motion';
  const id = $props.id();
  const greeting = ['perk', 'boop', 'waggle'][Math.floor(dogPhase(id) * 3)];

  let { size = 28 }: { size?: number } = $props();
</script>

<span class="avatar" data-greeting={greeting} style:--size="{size}px" aria-hidden="true">
  <Dog size={Math.round(size * 0.55)} />
</span>

<style>
  .avatar {
    display: inline-flex;
    flex: none;
    align-items: center;
    justify-content: center;
    width: var(--size);
    height: var(--size);
    color: var(--z-accent);
    background: var(--z-accent-subtle);
    border: var(--z-border-width) solid var(--z-accent-border);
    border-radius: var(--z-radius-full);
  }
  .avatar:hover :global(svg) {
    animation: perk 600ms var(--z-ease);
  }
  @keyframes perk {
    30% {
      transform: rotate(-14deg) translateY(-2px);
    }
    65% {
      transform: rotate(12deg);
    }
  }
  .avatar[data-greeting='boop']:hover :global(svg) {
    animation-name: boop;
  }
  .avatar[data-greeting='waggle']:hover :global(svg) {
    animation-name: waggle;
  }
  @keyframes boop {
    35% {
      transform: translateY(-4px) scale(1.12);
    }
    65% {
      transform: translateY(1px) scale(0.95);
    }
  }
  @keyframes waggle {
    20%,
    60% {
      transform: rotate(-12deg);
    }
    40%,
    80% {
      transform: rotate(12deg);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .avatar[data-greeting]:hover :global(svg) {
      animation: none;
    }
  }
</style>
