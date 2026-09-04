<!--
  Something failed. Say what happened and what to do about it, in the server's
  own words where there are any -- a 403 message names the role required, and
  paraphrasing it would throw that away.
-->
<script lang="ts">
  import { TriangleAlert, WifiOff } from '@lucide/svelte';
  import { ApiError } from '../api/client';
  import Button from './Button.svelte';

  interface Props {
    error?: unknown;
    title?: string;
    /** Overrides the message derived from the error. */
    description?: string;
    /** Offered only after a failure -- Zoomies has no refresh button. */
    onretry?: () => void;
    retryLabel?: string;
    compact?: boolean;
    class?: string;
  }

  let {
    error,
    title,
    description,
    onretry,
    retryLabel = 'Try again',
    compact = false,
    class: className = '',
  }: Props = $props();

  const apiError = $derived(error instanceof ApiError ? error : null);
  const offline = $derived(apiError?.isOffline ?? false);

  const heading = $derived(
    title ??
      (offline
        ? 'Cannot reach the controller'
        : apiError?.isForbidden
          ? 'Not allowed'
          : apiError?.isNotFound
            ? 'Not found'
            : 'Something went wrong'),
  );

  const body = $derived(
    description ??
      apiError?.message ??
      (error instanceof Error
        ? error.message
        : 'The cause was not reported. The controller log will have it.'),
  );
</script>

<div class="error {className}" class:compact role="alert">
  <span class="icon" aria-hidden="true">
    {#if offline}<WifiOff size={20} />{:else}<TriangleAlert size={20} />{/if}
  </span>
  <div class="text">
    <p class="title">{heading}</p>
    <p class="body">{body}</p>
    {#if apiError?.detail}<p class="detail">{apiError.detail}</p>{/if}
  </div>
  {#if onretry}
    <Button size="sm" onclick={onretry}>{retryLabel}</Button>
  {/if}
</div>

<style>
  .error {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
    padding: var(--z-space-5);
    border: 1px solid var(--z-danger-border);
    border-radius: var(--z-radius-md);
    background: var(--z-danger-subtle);
  }
  .error.compact {
    padding: var(--z-space-3);
  }
  .icon {
    flex: none;
    display: inline-flex;
    color: var(--z-danger);
  }
  .text {
    flex: 1;
    min-width: 0;
  }
  .title {
    margin: 0 0 2px;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .body,
  .detail {
    margin: 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
    overflow-wrap: anywhere;
  }
  .detail {
    margin-top: var(--z-space-1);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
</style>
