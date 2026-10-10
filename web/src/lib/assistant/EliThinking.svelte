<script lang="ts">
  import { onMount } from 'svelte';
  import DogMotion from '$lib/mascot/DogMotion.svelte';
  import { thinkingBeat, THINKING_QUOTES, THINKING_QUOTE_MS } from './personality';
  let step = $state(0);
  let offset = $state(0);
  const beat = $derived(thinkingBeat(step, offset));
  onMount(() => {
    // Start each answer on a different part of the repertoire, not always lap one.
    offset = Math.floor(Math.random() * (THINKING_QUOTES.length / 2)) * 2;
    const timer = window.setInterval(() => {
      step += 1;
    }, THINKING_QUOTE_MS);
    return () => window.clearInterval(timer);
  });
</script>

<div class="thinking">
  <span class="sr-only">Eli is thinking</span>
  <div class="track" aria-hidden="true">
    <DogMotion motion={beat.motion} />
  </div>
  <p aria-hidden="true">{beat.quote}</p>
</div>

<style>
  .thinking {
    color: var(--z-text-muted);
    font-size: var(--z-text-sm);
  }
  .track {
    width: 6rem;
    height: var(--z-space-16);
    color: var(--z-accent);
  }
  p {
    margin: 0;
    min-height: 2.5em;
  }
</style>
