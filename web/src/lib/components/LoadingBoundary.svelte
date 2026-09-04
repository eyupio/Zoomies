<!--
  The four states every piece of remote data has, in one place: loading, failed,
  empty, and the happy path. Written in that order, because the first three are
  most of what an operator actually sees.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import ErrorState from './ErrorState.svelte';
  import Skeleton from './Skeleton.svelte';

  interface Props {
    loading?: boolean;
    error?: unknown;
    empty?: boolean;
    onretry?: () => void;
    /** The shape of what is coming. Falls back to plain bars. */
    skeleton?: Snippet;
    /** What to show when the request succeeded and returned nothing. */
    emptyState?: Snippet;
    class?: string;
    children: Snippet;
  }

  let {
    loading = false,
    error,
    empty = false,
    onretry,
    skeleton,
    emptyState,
    class: className = '',
    children,
  }: Props = $props();
</script>

<div class={className} aria-busy={loading ? 'true' : undefined}>
  {#if error}
    <ErrorState {error} {onretry} />
  {:else if loading}
    {#if skeleton}
      {@render skeleton()}
    {:else}
      <div class="bars">
        <Skeleton height="1.25rem" width="40%" />
        <Skeleton lines={3} />
      </div>
    {/if}
  {:else if empty && emptyState}
    {@render emptyState()}
  {:else}
    {@render children()}
  {/if}
</div>

<style>
  .bars {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    padding: var(--z-space-4) 0;
  }
</style>
