<!--
  What a host can actually run a job in.

  `detail` is the important field here: when a backend is unavailable it is the
  agent's own explanation of what is wrong -- a socket that is not readable, a
  daemon that is not running -- and it usually names the exact fix. It is shown
  in full rather than behind a tooltip, because it is the reason the host is not
  taking work.
-->
<script lang="ts">
  import { Check, X } from '@lucide/svelte';
  import type { BackendInfo } from '$lib/api/types';
  import RemedyText from '$lib/components/RemedyText.svelte';

  interface Props {
    backends?: readonly BackendInfo[];
    /** The plain `backends` array, used when the richer info is absent. */
    kinds?: readonly string[];
    class?: string;
  }

  let { backends = [], kinds = [], class: className = '' }: Props = $props();

  const rows = $derived<readonly BackendInfo[]>(
    backends.length > 0 ? backends : kinds.map((kind) => ({ kind: kind as BackendInfo['kind'] })),
  );
</script>

{#if rows.length === 0}
  <p class="none">
    This host has reported no backend it can run a job in, so the scheduler will not place a runner
    here. Check that Docker, Podman or the process backend is available to the agent.
  </p>
{:else}
  <ul class="backends {className}">
    {#each rows as backend (backend.kind)}
      {@const available = backend.available !== false}
      <li class:unavailable={!available}>
        <span class="mark" class:ok={available}>
          {#if available}
            <Check size={12} aria-hidden="true" />
          {:else}
            <X size={12} aria-hidden="true" />
          {/if}
        </span>
        <span class="body">
          <span class="line">
            <span class="kind mono">{backend.kind}</span>
            <span class="sr-only">{available ? 'available' : 'unavailable'}</span>
            {#if backend.version}<span class="meta">{backend.version}</span>{/if}
            {#if backend.rootless}<span class="meta">rootless</span>{/if}
            {#if backend.supports_dind}<span class="meta">supports Docker in Docker</span>{/if}
          </span>
          {#if backend.endpoint}
            <span class="endpoint mono">{backend.endpoint}</span>
          {/if}
          {#if !available && backend.detail}
            <span class="detail"><RemedyText text={backend.detail} /></span>
          {/if}
        </span>
      </li>
    {/each}
  </ul>
{/if}

<style>
  .backends {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  li {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
    min-width: 0;
  }
  .mark {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: var(--z-space-4);
    height: var(--z-space-4);
    border-radius: var(--z-radius-full);
    background: var(--z-danger-subtle);
    color: var(--z-danger);
    flex: none;
  }
  .mark.ok {
    background: var(--z-idle-subtle);
    color: var(--z-idle);
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    min-width: 0;
  }
  .line {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: var(--z-space-2);
  }
  .kind {
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
  .meta {
    font-size: var(--z-text-2xs);
    color: var(--z-text-muted);
  }
  .endpoint {
    font-size: var(--z-text-2xs);
    color: var(--z-text-subtle);
    overflow-wrap: anywhere;
  }
  .detail {
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-danger);
    overflow-wrap: anywhere;
  }
  .none {
    margin: 0;
    max-width: 62ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
</style>
