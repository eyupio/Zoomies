<!--
  The memory a host may lend to a runner that is about to be killed for its own,
  and where the rest of the machine's memory has gone.

  A host's pool is what no runner's share needs and the host measures as free,
  above a floor it keeps for itself. A pool of nothing is the answer an operator
  has to be able to explain -- "why was my job not given more?" -- so the bar says
  whose memory it is, and the card behind it is the ledger the controller worked
  it out from, line by line, in the order it took them.

  The bar is drawn against the whole machine, so its segments are comparable
  between hosts of different sizes; the figures beside it are what a bar cannot
  carry.
-->
<script lang="ts">
  import type { MemoryPool } from '$lib/api/types';
  import { formatMegabytes } from '$lib/format';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Tooltip from '$lib/components/Tooltip.svelte';

  interface Props {
    pool: MemoryPool;
    /** The host's memory, which the bar is drawn against. */
    totalMb: number;
  }

  let { pool, totalMb }: Props = $props();

  const componentId = $props.id();
  const descriptionId = `${componentId}-pool-description`;
  let open = $state(false);

  const mb = formatMegabytes;
  const promised = $derived(pool.committed_mb + pool.idle_reserve_mb + pool.start_reserve_mb);
  // What the ledger leaves, before the memory the host actually has free is
  // asked: the line the capacity is bound by when the promises are the limit.
  const ledger = $derived(Math.max(totalMb - pool.floor_mb - promised, 0));
  // The whole machine, so a segment's width means the same on every host. Where
  // the host measured less free than the ledger allows, the capacity is smaller
  // and the difference is memory in use by something that is not a runner.
  const scale = $derived(Math.max(totalMb, promised + pool.floor_mb + pool.capacity_mb, 1));
  const pct = (value: number) => `${Math.min(Math.max((value / scale) * 100, 0), 100).toFixed(2)}%`;
  const elsewhere = $derived(Math.max(totalMb - promised - pool.floor_mb - pool.capacity_mb, 0));
  const limited = $derived(
    pool.binding === 'measured'
      ? 'the free memory the host measured'
      : 'the promises already made to runners',
  );

  const refused = $derived(
    pool.short_at
      ? pool.short_code === 'host_floor'
        ? 'was down to the memory it keeps free for itself'
        : 'had no spare memory left to lend'
      : '',
  );

  const sentence = $derived(
    [
      pool.supported
        ? `${mb(pool.pool_mb)} left to lend, of ${mb(pool.capacity_mb)} this host may lend in all; ${mb(pool.lent_mb)} is lent.`
        : "This host's agent cannot lend memory, so none is.",
      `That is limited by ${limited}.`,
      `${mb(promised)} is promised to runners and ${mb(pool.floor_mb)} is kept free for the host.`,
      pool.short_at ? `A runner here was last refused memory because the host ${refused}.` : '',
    ]
      .filter(Boolean)
      .join(' '),
  );
</script>

<Tooltip text={sentence} {descriptionId} bind:open placement="top" wide class="host-pool-tip">
  {#snippet content()}
    <span class="card">
      <span class="head">
        <span class="eyebrow">Memory valve</span>
        <strong class="title">
          {pool.supported ? `${mb(pool.pool_mb)} left to lend` : 'This agent cannot lend memory'}
        </strong>
      </span>
      <span class="detail">
        {#if pool.supported}
          What no runner's share needs and the host measures as free, less the floor it keeps for
          itself. Limited by {limited}.
        {:else}
          Upgrade the agent on this host: until then a runner placed here keeps the memory it was
          created with.
        {/if}
      </span>
      <span class="ledger">
        <span class="line"><span>Host memory</span><b>{mb(totalMb)}</b></span>
        <span class="line"
          ><span>Kept free for the host</span><b class="minus">{mb(pool.floor_mb)}</b></span
        >
        <span class="line"
          ><span>Promised to busy and starting runners</span><b class="minus"
            >{mb(pool.committed_mb)}</b
          ></span
        >
        <span class="line"
          ><span>Held for the next idle runner</span><b class="minus">{mb(pool.idle_reserve_mb)}</b
          ></span
        >
        <span class="line"
          ><span>Held for a queued start</span><b class="minus">{mb(pool.start_reserve_mb)}</b
          ></span
        >
        <span class="line total"><span>May be lent, by the ledger</span><b>{mb(ledger)}</b></span>
        {#if pool.binding === 'measured'}
          <span class="line"
            ><span>May be lent, by free memory</span><b>{mb(pool.capacity_mb)}</b></span
          >
        {/if}
        <span class="line"><span>Lent so far</span><b class="lent">{mb(pool.lent_mb)}</b></span>
      </span>
      {#if pool.short_at}
        <span class="note">A runner here was refused memory: the host {refused}.</span>
      {/if}
    </span>
  {/snippet}

  <span class="pool" data-testid="host-memory-pool">
    <span class="top">
      <span class="figure tabular">
        {#if pool.supported}
          <strong>{mb(pool.pool_mb)}</strong> to lend
          <span class="muted">· {mb(pool.lent_mb)} lent</span>
        {:else}
          <strong>None</strong> <span class="muted">· this agent cannot lend memory</span>
        {/if}
      </span>
    </span>
    <span class="track" aria-hidden="true">
      <span class="seg promised" style:width={pct(promised)}></span>
      <span class="seg floor" style:width={pct(pool.floor_mb)}></span>
      <span class="seg lent" style:width={pct(pool.lent_mb)}></span>
      <span class="seg spare" style:width={pct(pool.pool_mb)}></span>
      <span class="seg rest" style:width={pct(elsewhere)}></span>
    </span>
    {#if pool.short_at}
      <span class="refused" data-testid="host-memory-refused">
        A runner was refused memory <RelativeTime value={pool.short_at} plain />.
      </span>
    {/if}
  </span>
</Tooltip>

<style>
  :global(.host-pool-tip) {
    display: flex;
    width: 100%;
    min-width: 0;
  }
  .pool {
    display: grid;
    gap: var(--z-space-2);
    width: 100%;
    min-width: 0;
    cursor: help;
  }
  .figure {
    font-size: var(--z-text-xs);
    color: var(--z-text);
  }
  .muted {
    color: var(--z-text-subtle);
  }
  .track {
    display: flex;
    height: var(--z-space-2);
    overflow: hidden;
    border-radius: var(--z-radius-full);
    background: var(--z-surface-sunken);
  }
  .seg {
    flex: none;
    min-width: 0;
  }
  .promised {
    background: var(--z-neutral);
  }
  /* What the host keeps for itself is hatched, because it is memory that is
     there and is not anybody's to use. */
  .floor {
    background: repeating-linear-gradient(
      135deg,
      var(--z-neutral-border) 0 var(--z-nudge-2),
      transparent var(--z-nudge-2) var(--z-nudge-3)
    );
  }
  .lent {
    background: var(--z-accent);
  }
  .spare {
    background: var(--z-accent-subtle);
    box-shadow: inset 0 0 0 var(--z-border-width) var(--z-accent-border);
  }
  .rest {
    background: var(--z-border);
  }
  .refused {
    color: var(--z-pending);
    font-size: var(--z-text-2xs);
  }
  .card {
    display: grid;
    gap: var(--z-space-3);
    padding: var(--z-space-1);
    text-align: left;
    min-width: 0;
  }
  .head {
    display: grid;
  }
  .eyebrow {
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
  }
  .title {
    color: var(--z-text);
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-semibold);
  }
  .detail {
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }
  .ledger {
    display: grid;
    gap: var(--z-space-1);
    padding-top: var(--z-space-3);
    border-top: var(--z-border-width) solid var(--z-border);
  }
  .line {
    display: flex;
    justify-content: space-between;
    gap: var(--z-space-4);
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
  }
  .line b {
    color: var(--z-text);
    font-variant-numeric: tabular-nums;
    font-weight: var(--z-weight-medium);
  }
  .line b.minus::before {
    content: '− ';
  }
  .line.total {
    margin-top: var(--z-space-1);
    padding-top: var(--z-space-2);
    border-top: var(--z-border-width) solid var(--z-border);
    color: var(--z-text);
  }
  .line b.lent {
    color: var(--z-accent);
  }
  .note {
    padding-left: var(--z-space-3);
    border-left: var(--z-border-width-thick) solid var(--z-pending-border);
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }
</style>
