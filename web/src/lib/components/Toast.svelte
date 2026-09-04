<script lang="ts">
  import { CircleCheck, Info, TriangleAlert, X } from '@lucide/svelte';
  import type { Toast } from '../state/toasts.svelte';
  import { toasts } from '../state/toasts.svelte';
  import Button from './Button.svelte';
  import IconButton from './IconButton.svelte';

  interface Props {
    toast: Toast;
  }

  let { toast }: Props = $props();
</script>

<div class="toast" data-tone={toast.tone}>
  <span class="icon" aria-hidden="true">
    {#if toast.tone === 'success'}<CircleCheck size={16} />
    {:else if toast.tone === 'error' || toast.tone === 'warning'}<TriangleAlert size={16} />
    {:else}<Info size={16} />{/if}
  </span>
  <div class="text">
    <p class="title">{toast.title}</p>
    {#if toast.message}<p class="message">{toast.message}</p>{/if}
    {#if toast.action}
      <div class="action">
        <Button size="sm" variant="secondary" onclick={toast.action.run}
          >{toast.action.label}</Button
        >
      </div>
    {/if}
  </div>
  <IconButton icon={X} label="Dismiss" size="sm" onclick={() => toasts.dismiss(toast.id)} />
</div>

<style>
  .toast {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
    width: 360px;
    max-width: calc(100vw - var(--z-space-8));
    padding: var(--z-space-3);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-raised);
    box-shadow: var(--z-shadow-md);
    animation: rise var(--z-motion-base) var(--z-ease);
  }
  .toast[data-tone='success'] {
    border-color: var(--z-idle-border);
  }
  .toast[data-tone='error'] {
    border-color: var(--z-danger-border);
  }
  .toast[data-tone='warning'] {
    border-color: var(--z-pending-border);
  }
  .icon {
    flex: none;
    display: inline-flex;
    margin-top: 1px;
    color: var(--z-text-muted);
  }
  .toast[data-tone='success'] .icon {
    color: var(--z-idle);
  }
  .toast[data-tone='error'] .icon {
    color: var(--z-danger);
  }
  .toast[data-tone='warning'] .icon {
    color: var(--z-pending);
  }
  .text {
    flex: 1;
    min-width: 0;
  }
  .title {
    margin: 0;
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .message {
    margin: 2px 0 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
    overflow-wrap: anywhere;
  }
  .action {
    margin-top: var(--z-space-2);
  }
  @keyframes rise {
    from {
      opacity: 0;
      transform: translateY(6px);
    }
  }
</style>
