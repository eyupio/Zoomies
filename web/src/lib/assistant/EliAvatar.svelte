<!--
  Eli's face wherever Eli speaks. A paw on the accent's own tint, from the same
  tokens as everything else, so it follows the theme.
-->
<script lang="ts">
  import { PawPrint } from '@lucide/svelte';

  let { size = 28 }: { size?: number } = $props();
</script>

<span class="avatar" style:--size="{size}px" aria-hidden="true">
  <PawPrint size={Math.round(size * 0.55)} />
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
</style>
