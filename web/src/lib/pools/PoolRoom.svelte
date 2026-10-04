<!--
  What the hosts can hold, at the size this pool is asking for.

  The two settings this panel exists for are correct on their own and wrong
  together: a pool of eight-core runners on four-core boxes is valid and starts
  nothing, and a maximum of forty on a fleet with room for eleven is valid,
  healthy, and queues twenty-nine jobs behind runners nothing will ever create.
  Neither is visible anywhere either setting is edited unless something counts
  them, so this is counted beside both.

  The counting is the controller's, not the browser's: what a runner is charged
  is not always what the pool says -- a docker-in-docker slot is charged for
  its sidecar -- and a second sum worked out here would disagree with where the
  fleet actually places runners. This renders the answer and offers the two
  changes that fix it: bring the maximum down to the room, or bring a host's
  slots up to what its machine can back.
-->
<script lang="ts">
  import { CircleCheck, Server, Sparkles, TriangleAlert } from '@lucide/svelte';
  import { ApiError, updateHost } from '$lib/api/client';
  import type { PoolHostRoom, PoolRoom } from '$lib/api/types';
  import { formatMegabytes, pluralise } from '$lib/format';
  import { fleet } from '$lib/state/fleet.svelte';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import { hostIsInvolved, hostSizingWords } from './hostSizing';
  import { cpuLabel, limitPhrase, sizeLabel } from './sizing';

  interface Props {
    room: PoolRoom | null;
    /**
     * What one runner asks for, so the panel can say what the room is of. Zero
     * for a pool that leaves its size to the host, where each host's row says
     * what a runner is there instead.
     */
    cpus: number;
    memoryMb: number;
    /**
     * Whether the pool takes its size from each host's runner profile. Every
     * row then says what a runner is on that host and whose figure it is,
     * because the answer is different on each.
     */
    profile?: boolean;
    /** True while a fresh count is on its way, so a stale one can say so. */
    validating?: boolean;
    /** The pool's cap, when there is one to weigh the room against. */
    maxRunners?: number;
    /** Offered when the maximum is above the room: set it to what fits. */
    onusemax?: (value: number) => void;
    class?: string;
  }

  let {
    room,
    cpus,
    memoryMb,
    profile = false,
    validating = false,
    maxRunners,
    onusemax,
    class: className = '',
  }: Props = $props();

  const hosts = $derived(room?.hosts ?? []);
  const total = $derived(room?.runners ?? 0);
  const canOperate = $derived(session.can('operator'));
  /* A host whose slots outrun its machine is the failure worth the amber: its
     free slots are counted on every page, and every create for one of them is
     refused for want of memory. */
  /* A host the pool is kept off is listed so its row can say why, and takes
     no part in the arithmetic: it is never asked for a runner of this pool,
     so its slots are not promises about it. */
  const overcommitted = $derived(
    hosts.filter((h) => !h.excluded && (h.slots ?? 0) > (h.fits ?? 0)),
  );
  const placeable = $derived(hosts.filter((h) => !h.excluded));
  /* "of 2 cores and 4 GB" where the pool has one size on every host, and
     nothing where it does not: "of 0 cores and none" is what a pool that leaves
     its size to the host used to say. */
  const ofSize = $derived(cpus > 0 || memoryMb > 0 ? ` of ${sizeLabel(cpus, memoryMb)}` : '');
  const above = $derived(maxRunners !== undefined && total > 0 && maxRunners > total);

  /** Which host is being adjusted, so only its button spins. */
  let adjusting = $state<string | null>(null);

  function machine(host: PoolHostRoom): string {
    const parts: string[] = [];
    if (host.cpus_known) parts.push(cpuLabel(host.cpus ?? 0));
    if (host.memory_known) parts.push(formatMegabytes(host.memory_mb ?? 0));
    return parts.length > 0 ? `${parts.join(', ')} to place on` : 'an unmeasured machine';
  }

  /**
   * Set a host's capacity to what its machine can actually back at this size.
   *
   * This is the other half of the same decision, and it belongs here rather
   * than three pages away on the Hosts grid: an operator who has just chosen
   * how big a runner is, and is being told a host cannot back its own slots,
   * is one click from either answer.
   */
  async function adjust(host: PoolHostRoom): Promise<void> {
    if (!host.host_id || adjusting) return;
    adjusting = host.host_id;
    try {
      await updateHost(host.host_id, { capacity: host.fits ?? 0 });
      await fleet.reconcile();
      toasts.success(
        `${host.host} adjusted to ${pluralise(host.fits ?? 0, 'slot')}`,
        'The scheduler uses it on its next pass.',
      );
    } catch (cause) {
      // A refusal here is the controller saying the new capacity would strand
      // another pool, and it names which. It is a sentence worth reading, not
      // a "that did not work".
      if (cause instanceof ApiError && cause.isConflict) {
        toasts.error(`${host.host} was not adjusted`, cause.message);
      } else {
        toasts.fromError(cause, `${host.host} was not adjusted`);
      }
    } finally {
      adjusting = null;
    }
  }
</script>

{#if room === null && validating}
  <div class="room checking {className}" aria-busy="true">
    <Skeleton width="60%" height="0.9rem" />
  </div>
{:else if room !== null}
  <div
    class="room {className}"
    class:none={total === 0 && hosts.length > 0}
    class:empty={hosts.length === 0}
    class:warn={above || overcommitted.length > 0}
    class:stale={validating}
    aria-live="polite"
  >
    {#if hosts.length === 0}
      <p class="title">
        <Server size={15} aria-hidden="true" />
        No host is standing by for this pool
      </p>
      <p class="body">
        There is nothing to size against yet. Once a machine joins and matches this pool, this says
        how many runners{ofSize} it can hold.
      </p>
    {:else}
      <p class="title">
        {#if total === 0}
          <TriangleAlert size={15} aria-hidden="true" />
          {placeable.length === 0 && profile
            ? 'No host\u2019s runner profile lets this pool run'
            : 'No host has room for a runner this size'}
        {:else if above || overcommitted.length > 0}
          <TriangleAlert size={15} aria-hidden="true" />
          Room for {pluralise(total, 'runner')}{ofSize}
        {:else}
          <CircleCheck size={15} aria-hidden="true" />
          Room for {pluralise(total, 'runner')}{ofSize}
        {/if}
      </p>

      <ul class="hosts">
        {#each hosts as host (host.host_id)}
          {@const words = hostSizingWords(host.sizing)}
          <li class:short={!host.excluded && (host.slots ?? 0) > (host.fits ?? 0)}>
            <span class="name">{host.host}</span>
            {#if host.excluded}
              <!-- Listed, with the reason, rather than dropped: "this host is
                   left out, and this is the limit" is the row an operator came
                   to read. -->
              <span class="count">kept off</span>
              <span class="why" data-testid="room-host-excluded">{host.excluded}</span>
            {:else}
              <span class="count">
                {pluralise(host.room ?? 0, 'runner')}
                <span class="of">of {pluralise(host.slots ?? 0, 'slot')}</span>
              </span>
              <span class="why">
                {machine(host)}{host.limited_by ? `, ${limitPhrase(host.limited_by)}` : ''}
              </span>
              <!-- Never offered at zero: a host with no room for one runner of
                   this pool is a machine to give less to, not one to pause for
                   every other pool as well. -->
              {#if (host.slots ?? 0) > (host.fits ?? 0) && (host.fits ?? 0) > 0 && canOperate}
                <Button
                  size="sm"
                  variant="secondary"
                  icon={Sparkles}
                  loading={adjusting === host.host_id}
                  onclick={() => void adjust(host)}
                >
                  Set to {host.fits}
                </Button>
              {/if}
              <!-- What a runner is on this host, and whose figure that is, for
                   the hosts whose own settings are doing something to it. A
                   fleet that has never set a profile sees nothing here. -->
              {#if hostIsInvolved(host.sizing, profile) && words.standard}
                <span class="sizing" data-testid="room-host-sizing">
                  A runner is <strong>{words.standard}</strong>{words.floor
                    ? `, never less than ${words.floor}`
                    : ''}{words.ceiling ? `, and may use up to ${words.ceiling}` : ''}.
                </span>
              {/if}
            {/if}
          </li>
        {/each}
      </ul>

      {#if overcommitted.length > 0}
        <p class="body">
          {overcommitted.length === 1 ? 'That host promises' : 'Those hosts promise'} more slots than
          the machine can back at this size. The extra slots are counted as free capacity on every page
          that shows them, and every create for one of them is refused for want of cores or memory — so
          jobs wait on runners nothing will make.
        </p>
      {:else if total === 0 && placeable.length === 0}
        <p class="body">
          Every matching host's runner profile keeps this pool off it, and the reason is on each row
          above. Change the pool's size or minimum, or the host's runner sizes, so that the two
          agree.
        </p>
      {:else if total === 0}
        <p class="body">
          Every matching host is smaller than one runner of this pool, so it would never place one.
          Ask for less here, or give the pool a bigger machine.
        </p>
      {/if}

      {#if above && maxRunners !== undefined}
        <p class="body">
          This pool's maximum is {maxRunners}, which is {pluralise(maxRunners - total, 'runner')} more
          than the fleet can place. The maximum is a backstop rather than a target, so it is not wrong
          — but those runners are ones the scheduler will never create.
        </p>
        {#if onusemax}
          <div class="actions">
            <Button size="sm" variant="secondary" onclick={() => onusemax(total)}>
              Set the maximum to {total}
            </Button>
          </div>
        {/if}
      {/if}
    {/if}
  </div>
{/if}

<style>
  .room {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-idle-border);
    border-radius: var(--z-radius-md);
    background: var(--z-idle-subtle);
  }
  .room.warn {
    border-color: var(--z-pending-border);
    background: var(--z-pending-subtle);
  }
  .room.none {
    border: var(--z-border-width-thick) solid var(--z-danger-border);
    background: var(--z-danger-subtle);
  }
  /* No host yet is not "idle": the green says a runner is standing by, and the
     status colours are a fixed mapping. It is a thing not known yet, and reads
     as one. */
  .room.empty {
    border-color: var(--z-border);
    background: var(--z-surface-sunken);
  }
  /* A count being refreshed is still the last true answer, so it fades rather
     than being replaced by a skeleton under the slider being moved. */
  .room.stale {
    opacity: 0.6;
  }
  .checking {
    border-color: var(--z-border);
    background: var(--z-surface-sunken);
  }
  .title {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-idle);
  }
  .room.warn .title {
    color: var(--z-pending);
  }
  .room.none .title {
    color: var(--z-danger);
  }
  .room.empty .title {
    color: var(--z-text);
  }
  .body {
    margin: 0;
    max-width: 70ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .hosts {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .hosts li {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: var(--z-space-2);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }
  .name {
    font-family: var(--z-font-mono);
    font-size: var(--z-text-2xs);
    color: var(--z-text);
  }
  .count {
    font-weight: var(--z-weight-medium);
    font-variant-numeric: tabular-nums;
    color: var(--z-text);
  }
  .hosts li.short .count {
    color: var(--z-pending);
  }
  .of {
    font-weight: var(--z-weight-normal);
    color: var(--z-text-subtle);
  }
  .why {
    flex: 1 1 20ch;
    color: var(--z-text-muted);
  }
  /* A line of its own under the row: the sizes are a sentence, and fitting one
     between a name and a count would make every row a different height. */
  .sizing {
    flex: 1 1 100%;
    padding-left: var(--z-space-3);
    color: var(--z-text-muted);
  }
  .sizing strong {
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2);
  }
</style>
