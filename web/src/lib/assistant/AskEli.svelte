<script lang="ts">
  import { Dog } from '@lucide/svelte';
  import Button from '$lib/components/Button.svelte';
  import { session } from '$lib/state/session.svelte';
  import { eli } from './eli.svelte';
  import type { EliContext } from './prompts';

  let { context }: { context: EliContext } = $props();
</script>

{#if session.can('viewer')}
  <Button
    variant="ghost"
    size="sm"
    icon={Dog}
    class="ask-eli"
    title="Share these displayed details with Eli and ask for guidance"
    onclick={() => eli.ask(context)}>Ask Eli</Button
  >
{/if}

<style>
  /* The same head-tilt as Eli's avatar, so the dog on the button and the dog
     in the corner are plainly the same dog. */
  :global(.ask-eli:hover:not(:disabled) .lead svg) {
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
  @media (prefers-reduced-motion: reduce) {
    :global(.ask-eli:hover:not(:disabled) .lead svg) {
      animation: none;
    }
  }
</style>
