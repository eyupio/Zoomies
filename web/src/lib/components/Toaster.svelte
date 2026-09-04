<!--
  The toast region. Two live regions, because an error interrupts and a success
  does not, and a screen reader should be told the difference.
-->
<script lang="ts">
  import { toasts } from '../state/toasts.svelte';
  import Toast from './Toast.svelte';
</script>

<div class="toaster">
  <div aria-live="polite" aria-atomic="false" class="region">
    {#each toasts.polite as toast (toast.id)}
      <Toast {toast} />
    {/each}
  </div>
  <div aria-live="assertive" aria-atomic="false" class="region">
    {#each toasts.assertive as toast (toast.id)}
      <Toast {toast} />
    {/each}
  </div>
</div>

<style>
  .toaster {
    position: fixed;
    right: var(--z-space-4);
    bottom: var(--z-space-4);
    z-index: var(--z-layer-toast);
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    pointer-events: none;
  }
  .region {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
  }
  .region > :global(*) {
    pointer-events: auto;
  }
</style>
