<!--
  What this pool's runners are on each host, and the hosts it is kept off.

  It is the count the wizard's size step shows, asked of the pool as it is
  saved. A pool that takes its size from each host is a different size on every
  machine, and the pool's own page said nothing about it: an operator reading
  "the size each host sets" had to open every host to find what that was. The
  controller does the sum -- what a runner is charged is not always what the
  pool says -- so this renders its answer, with whose figure each one is.

  Asked of operators only, because the dry run is an operator's call; a viewer
  sees the pool's configuration, which says where the size comes from.
-->
<script lang="ts">
  import { validatePool } from '$lib/api/client';
  import type { Pool, Result } from '$lib/api/types';
  import { fleet } from '$lib/state/fleet.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import PoolRoom from './PoolRoom.svelte';
  import { draftFromPool, toPoolBody } from './draft';

  interface Props {
    pool: Pool;
  }

  let { pool }: Props = $props();

  let verdict = $state<Result<'validatePool'> | null>(null);
  let error = $state<unknown>(null);
  let validating = $state(false);

  // The pool as the dry run takes it. A string, so that a live update to the
  // pool's counters -- which arrives every few seconds on a busy pool and
  // changes none of these figures -- does not ask the controller again.
  const request = $derived(JSON.stringify(toPoolBody(draftFromPool(pool))));

  // Asked again when the pool or the fleet's hosts change: a host that is
  // resized, or given a size, moves every figure here.
  $effect(() => {
    const body = request;
    void fleet.shape;
    const controller = new AbortController();
    validating = true;
    const timer = setTimeout(() => {
      validatePool(JSON.parse(body), pool.id, controller.signal)
        .then((result) => {
          verdict = result;
          error = null;
        })
        .catch((cause: unknown) => {
          if (cause instanceof DOMException && cause.name === 'AbortError') return;
          error = cause;
        })
        .finally(() => {
          validating = false;
        });
    }, 250);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  });
</script>

{#if error}
  <ErrorState
    {error}
    compact
    title="The hosts could not be checked"
    description="The controller did not answer, so the sizes on each host are not shown."
  />
{:else if verdict === null}
  <div class="checking" aria-busy="true">
    <Skeleton width="45%" height="0.9rem" />
    <Skeleton lines={2} />
  </div>
{:else}
  <PoolRoom
    room={verdict.room ?? null}
    cpus={pool.resources?.cpus ?? 0}
    memoryMb={pool.resources?.memory_mb ?? 0}
    profile={pool.size_from_profile === true}
    maxRunners={pool.max_runners ?? 0}
    {validating}
  />
{/if}

<style>
  .checking {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
  }
</style>
