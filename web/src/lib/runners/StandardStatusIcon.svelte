<script lang="ts">
  import DogMotion from '$lib/mascot/DogMotion.svelte';
  import { standardMotion } from './standard-motion';
  let { state, seed = 'zoomies' }: { state: string; seed?: string } = $props();
  const animation = $derived(standardMotion(state, seed));
</script>

<span class="standard-dog">
  <DogMotion
    motion={animation.motion}
    cue={animation.cue}
    duration={animation.duration}
    status={animation.state}
    style="standard"
    {seed}
  />
</span>

<style>
  .standard-dog {
    display: inline-block;
    flex: none;
    width: var(--z-avatar-size);
    height: var(--z-avatar-size);
    color: var(--status-colour, currentColor);
  }
</style>
