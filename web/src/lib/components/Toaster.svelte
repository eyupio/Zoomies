<!--
  The toast region. Two live regions, because an error interrupts and a success
  does not, and a screen reader should be told the difference.
-->
<script lang="ts">
  import { toasts } from '../state/toasts.svelte';
  import Toast from './Toast.svelte';
</script>

<div class="toaster" data-inert-exempt>
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
  /*
    Beside Eli, not under him: the dog button lives in the same corner, and a
    toast pinned to it put Dismiss under the one thing on the page that stays
    clickable above everything else.
  */
  .toaster {
    position: fixed;
    right: calc(var(--z-space-5) + var(--z-eli-launcher) + var(--z-space-3));
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
      /* Measured from the window rather than the document (see
         --z-window-width), or a page that overflows sideways puts the
         confirmation an operator just earned off the right of the screen. */
      right: auto;
      width: calc(var(--z-window-width) - var(--z-space-3) * 2);
      /* Above the dog button as well as the navigation: a phone has no room
         beside him, so the stack starts where he ends. */
      bottom: calc(
        var(--z-space-12) + var(--z-space-4) + var(--z-safe-bottom) + var(--z-eli-launcher) +
          var(--z-space-2)
      );
      max-width: none;
    }
  }
</style>
