<script lang="ts">
  import type { StatusMeta } from '../status';

  let { status }: { status: Pick<StatusMeta, 'icon' | 'colour' | 'subtle' | 'border'> } = $props();
</script>

<span
  class="off-status-icon"
  aria-hidden="true"
  style:--icon-colour={status.colour}
  style:--icon-subtle={status.subtle}
  style:--icon-border={status.border}
>
  <status.icon class="standard-icon" size={18} strokeWidth={2.1} aria-hidden="true" />
</span>

<style>
  .off-status-icon {
    display: inline-flex;
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
  }
</style>
