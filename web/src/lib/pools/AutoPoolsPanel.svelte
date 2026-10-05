<!--
  What the controller is doing about size classes, above the pools it keeps.

  Off is one quiet sentence, because most fleets will never turn this on and a
  panel of zeroes would be the first thing they met on the Pools page. Once
  either switch is set the panel is a summary line that opens to the rest: what
  the two switches are doing, the pools and the hosts in them, what it would do
  and has not (the whole of `shadow`), what it could not do and what to change,
  and where each class begins. It opens by itself when something there needs an
  operator.

  Every sentence that explains a decision is the controller's. This arranges
  them.
-->
<script lang="ts">
  import type { AutoPools } from '$lib/api/types';
  import { classWord } from '$lib/hosts/tags';
  import { describeWindow, pluralise } from '$lib/format';
  import { AUTO_POOLS_URL } from '$lib/links';
  import Badge from '$lib/components/Badge.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import { limitWords, modeHint, modeWords, pendingWords, runnerWords } from './auto';

  interface Props {
    status: AutoPools | null;
  }

  let { status }: Props = $props();

  const off = $derived(status?.auto_pools === 'off' && status?.size_routing === 'off');
  const hostCount = $derived((status?.pools ?? []).reduce((n, p) => n + p.hosts.length, 0));
  const slotCount = $derived((status?.pools ?? []).reduce((n, p) => n + p.slots, 0));
  // Opened for the operator when there is something to read that is not routine:
  // a change waiting on them, or one the controller could not make.
  const attention = $derived(
    status !== null &&
      (Boolean(status.problem) ||
        status.findings.length > 0 ||
        (status.auto_pools === 'shadow' && status.pending.length > 0)),
  );
</script>

{#if status}
  {#if off}
    <p class="off" data-testid="auto-pools-off">
      Automatic pools are off. Zoomies can keep a pool for each size of host, and send each job to
      the pool its size calls for. Nothing changes until you set
      <span class="mono">scheduler.auto_pools</span>
      or <span class="mono">scheduler.size_routing</span>, and each has a setting that only reports.
      <a href={AUTO_POOLS_URL} target="_blank" rel="noopener noreferrer">How it works</a>
    </p>
  {:else}
    <details class="panel" open={attention} data-testid="auto-pools-panel">
      <summary>
        <span class="title">Automatic pools</span>
        <Badge
          tone={status.auto_pools === 'on' ? 'accent' : 'neutral'}
          label="Pools: {modeWords(status.auto_pools)}"
          size="sm"
          dot={false}
        />
        <Badge
          tone={status.size_routing === 'on' ? 'accent' : 'neutral'}
          label="Routing: {modeWords(status.size_routing)}"
          size="sm"
          dot={false}
        />
        <span class="counts tabular">
          {pluralise(status.pools.length, 'pool')} · {pluralise(hostCount, 'host')} · {pluralise(
            slotCount,
            'runner slot',
          )}
        </span>
      </summary>

      <div class="body">
        <ul class="modes">
          <li><strong>Pools</strong> {modeHint('auto_pools', status.auto_pools)}</li>
          <li><strong>Routing</strong> {modeHint('size_routing', status.size_routing)}</li>
        </ul>

        {#if status.problem}
          <p class="problem" role="status">{status.problem}.</p>
        {/if}

        {#if status.findings.length > 0}
          <section aria-labelledby="auto-findings-heading" data-testid="auto-pools-findings">
            <h3 id="auto-findings-heading">Could not be done</h3>
            <ul class="items">
              {#each status.findings as finding (finding.code + finding.subject)}
                <li>
                  <span>{finding.message}</span>
                  <span class="muted">{finding.fix}</span>
                </li>
              {/each}
            </ul>
          </section>
        {/if}

        {#if status.pending.length > 0}
          <section aria-labelledby="auto-pending-heading" data-testid="auto-pools-pending">
            <h3 id="auto-pending-heading">
              {status.auto_pools === 'shadow' ? 'What it would do' : 'Waiting to be done'}
            </h3>
            <ul class="items">
              {#each status.pending as change (change.kind + change.key)}
                <li>
                  <span><strong>{pendingWords(change.kind)}</strong> {change.pool}</span>
                  <span class="muted">{change.cause}</span>
                </li>
              {/each}
            </ul>
          </section>
        {/if}

        {#if status.pools.length > 0}
          <section aria-labelledby="auto-pools-heading" data-testid="auto-pools-list">
            <h3 id="auto-pools-heading">Pools it keeps</h3>
            <ul class="items">
              {#each status.pools as pool (pool.key)}
                <li>
                  <span>
                    {#if pool.pool_id}<a href="/pools/{pool.pool_id}">{pool.name}</a
                      >{:else}{pool.name}{/if}
                  </span>
                  <span class="muted"
                    >{pluralise(pool.hosts.length, 'host')}{pool.hosts.length > 0
                      ? ` (${pool.hosts.join(', ')})`
                      : ''} · {pluralise(pool.slots, 'runner slot')}</span
                  >
                </li>
              {/each}
            </ul>
          </section>
        {/if}

        {#if status.skipped.length > 0}
          <section aria-labelledby="auto-skipped-heading" data-testid="auto-pools-skipped">
            <h3 id="auto-skipped-heading">Hosts that count towards no pool</h3>
            <ul class="items">
              {#each status.skipped as skip (skip.host_id)}
                <li>
                  <span><a href="/hosts/{skip.host_id}">{skip.host}</a></span>
                  <span class="muted">{skip.message}</span>
                </li>
              {/each}
            </ul>
          </section>
        {/if}

        <section aria-labelledby="auto-classes-heading" data-testid="auto-pools-classes">
          <h3 id="auto-classes-heading">Classes</h3>
          <dl class="classes">
            {#each status.classes as limits (limits.class)}
              <div>
                <dt>
                  {classWord(limits.class)}
                  {#if limits.class === status.default_class}
                    <span class="muted">(default for a job nothing is known about)</span>
                  {/if}
                </dt>
                <dd>
                  hosts {limitWords(limits)} · runner {runnerWords(limits)} ·
                  <span class="mono">{limits.label}</span>
                </dd>
              </div>
            {/each}
          </dl>
          <p class="foot">
            A host holds its class for {describeWindow(status.hold) || 'a while'} before moving to another.
            A job waits {describeWindow(status.fallback_wait) || 'a while'} for room in its own class
            before it is offered another. A host silent for
            {describeWindow(status.host_grace) || 'a while'} stops counting. A pool it keeps cannot be
            deleted while <span class="mono">scheduler.auto_pools</span> is on, because it would
            make the pool again; pause it to take it out of use. With the setting at report only or
            off, a pool it left behind can be deleted.
            {#if status.at}Last pass <RelativeTime value={status.at} plain />.{/if}
            <a href={AUTO_POOLS_URL} target="_blank" rel="noopener noreferrer">How it works</a>
          </p>
        </section>
      </div>
    </details>
  {/if}
{/if}

<style>
  .off {
    margin: 0 0 var(--z-space-4);
    max-width: 90ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-subtle);
  }
  .panel {
    margin-bottom: var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  summary {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2) var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-4);
    cursor: pointer;
    font-size: var(--z-text-sm);
  }
  .title {
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .counts {
    margin-left: auto;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    padding: var(--z-space-3) var(--z-space-4) var(--z-space-4);
    border-top: var(--z-border-width) solid var(--z-border);
  }
  .modes,
  .items {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    margin: 0;
    padding: 0;
    list-style: none;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .modes strong {
    color: var(--z-text);
    font-weight: var(--z-weight-semibold);
    margin-right: var(--z-space-1);
  }
  .items li {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    padding-bottom: var(--z-space-2);
    border-bottom: var(--z-border-width) solid var(--z-border);
    color: var(--z-text);
  }
  .items li:last-child {
    border-bottom: 0;
    padding-bottom: 0;
  }
  .muted {
    color: var(--z-text-muted);
  }
  .problem {
    margin: 0;
    padding: var(--z-space-2) var(--z-space-3);
    border: var(--z-border-width) solid var(--z-pending-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-pending-subtle);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  h3 {
    margin: 0 0 var(--z-space-2);
    font-size: var(--z-text-2xs);
    text-transform: uppercase;
    letter-spacing: var(--z-tracking-wide);
    color: var(--z-text-muted);
    font-weight: var(--z-weight-medium);
  }
  .classes {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }
  .classes dt {
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .classes dd {
    margin: 0;
    color: var(--z-text-muted);
  }
  .foot {
    margin: var(--z-space-3) 0 0;
    max-width: 90ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-subtle);
  }
  a {
    color: var(--z-accent);
  }
</style>
