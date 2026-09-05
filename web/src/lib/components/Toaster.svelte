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

  /*
    On a phone the navigation is fixed to the bottom edge, so a toast pinned
    there landed on top of it -- covering the navigation the operator needs
    next with the confirmation they just earned. index.html asks for
    viewport-fit=cover, so the home-indicator gap is honoured here too rather
    than declared and ignored.
  */
  @media (max-width: 768px) {
    .toaster {
      left: var(--z-space-3);
      right: var(--z-space-3);
      bottom: calc(var(--z-space-16) + env(safe-area-inset-bottom, 0px));
      max-width: none;
    }
  }
</style>
