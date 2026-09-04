<!--
  Webhook health.

  Two things matter here and they are different questions. "Is anything
  arriving?" is answered by the last delivery and the list below it -- and the
  API deliberately distinguishes "nothing has ever arrived" from "nothing
  recently", because the first is a broken setup and the second is a quiet
  afternoon. "Can GitHub even reach us?" is answered by the reachability check,
  which returns the specific fix rather than a status code.
-->
<script lang="ts">
  import { Radio, RefreshCw } from '@lucide/svelte';
  import { listWebhookDeliveries, testWebhookReachability } from '$lib/api/client';
  import { events } from '$lib/api/sse';
  import type { WebhookCheck, WebhookDelivery } from '$lib/api/types';
  import { deliveryStatus } from '$lib/status';
  import type { DeliveryStatus } from '$lib/status';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import LoadingBoundary from '$lib/components/LoadingBoundary.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Select from '$lib/components/Select.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';

  interface Props {
    canOperate?: boolean;
    class?: string;
  }

  let { canOperate = false, class: className = '' }: Props = $props();

  const STATUSES = [
    { value: '', label: 'Every delivery' },
    { value: 'accepted', label: 'Accepted' },
    { value: 'rejected', label: 'Rejected' },
    { value: 'error', label: 'Errored' },
  ];

  let status = $state('');
  let deliveries = $state<WebhookDelivery[]>([]);
  let lastReceived = $state<string | null>(null);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let reload = $state(0);

  let checking = $state(false);
  let check = $state<WebhookCheck | null>(null);

  $effect(() => {
    const filter = status;
    void reload;
    const controller = new AbortController();
    loading = true;
    void listWebhookDeliveries(
      { status: (filter || undefined) as 'accepted' | 'rejected' | 'error' | undefined, limit: 25 },
      controller.signal,
    )
      .then((result) => {
        deliveries = result.items ?? [];
        lastReceived = result.last_received_at ?? null;
        error = null;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        error = cause;
      })
      .finally(() => (loading = false));
    return () => controller.abort();
  });

  // A delivery arriving is exactly the news this panel exists to report.
  $effect(() => events.subscribe('webhook.delivery', () => (reload += 1)));

  async function runCheck(): Promise<void> {
    checking = true;
    try {
      check = await testWebhookReachability();
    } catch (cause) {
      toasts.fromError(cause, 'The reachability check did not complete');
    } finally {
      checking = false;
    }
  }

  const pollingOnly = $derived(session.meta?.polling_only === true);
</script>

<section class="panel {className}" aria-labelledby="webhook-heading">
  <header>
    <div>
      <h2 id="webhook-heading">Webhook delivery</h2>
      <p>
        {#if lastReceived}
          Last delivery <RelativeTime value={lastReceived} plain />.
        {:else}
          Nothing has ever arrived at this controller, which is different from nothing arriving
          lately: it usually means GitHub cannot reach the webhook URL at all.
        {/if}
      </p>
    </div>
    <Button
      variant="secondary"
      icon={RefreshCw}
      loading={checking}
      disabled={!canOperate}
      onclick={runCheck}
    >
      Check reachability
    </Button>
  </header>

  <div class="body">
    {#if pollingOnly && !check}
      <p class="note">
        No webhook has been received, so the controller is polling GitHub for queued jobs instead.
        That works, but it reacts more slowly and spends more of the API quota.
      </p>
    {/if}

    {#if check}
      <div class="check" class:bad={check.reachable === false} class:good={check.reachable}>
        <p class="check-head">
          <Badge
            tone={check.reachable ? 'idle' : 'danger'}
            label={check.reachable ? 'Reachable' : 'Not reachable'}
            size="sm"
          />
          {#if check.url}<span class="mono url">{check.url}</span>{/if}
        </p>
        {#if check.message}<p class="check-line">{check.message}</p>{/if}
        {#if check.fix}
          <p class="check-line"><strong>Fix:</strong> {check.fix}</p>
        {/if}
        {#if check.reachable === false && check.polling_available}
          <p class="check-line">
            Until that is sorted, this controller can fall back to polling GitHub for queued jobs.
            Set <span class="mono">github.poll_fallback</span> in the configuration file and restart it;
            jobs will start more slowly, but they will start.
          </p>
        {/if}
        {#if check.last_delivery_at}
          <p class="check-line muted">
            Last delivery seen <RelativeTime value={check.last_delivery_at} plain />.
          </p>
        {/if}
      </div>
    {/if}

    <div class="filter">
      <Select
        bind:value={status}
        options={STATUSES}
        size="sm"
        ariaLabel="Filter deliveries by outcome"
      />
    </div>

    <LoadingBoundary
      {loading}
      {error}
      empty={!loading && !error && deliveries.length === 0}
      onretry={() => (reload += 1)}
    >
      {#snippet skeleton()}
        <Skeleton lines={4} />
      {/snippet}

      {#snippet emptyState()}
        <EmptyState
          icon={Radio}
          compact
          title={status ? 'No deliveries with that outcome' : 'No deliveries recorded'}
          description={status
            ? 'Nothing recent matches. Try another outcome, or clear the filter.'
            : 'Zoomies records every delivery GitHub makes, accepted or not. An empty list with a workflow running means the deliveries are not arriving.'}
        />
      {/snippet}

      <ul class="deliveries">
        {#each deliveries as delivery (delivery.id)}
          {@const meta = deliveryStatus(delivery.status as DeliveryStatus | undefined)}
          <li>
            <span class="badge"><Badge status={meta} size="sm" title={meta.hint} /></span>
            <span class="what">
              <span class="event mono">
                {delivery.event}{delivery.action ? `.${delivery.action}` : ''}
              </span>
              {#if delivery.repo}<span class="repo">{delivery.repo}</span>{/if}
              {#if delivery.error}<span class="failed">{delivery.error}</span>{/if}
            </span>
            <span class="when"><RelativeTime value={delivery.received_at} /></span>
          </li>
        {/each}
      </ul>
    </LoadingBoundary>
  </div>
</section>

<style>
  .panel {
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--z-space-4);
    padding: var(--z-space-4) var(--z-space-5);
    border-bottom: 1px solid var(--z-border);
  }
  h2 {
    margin: 0;
    font-size: var(--z-text-lg);
    line-height: var(--z-leading-lg);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  header p {
    margin: var(--z-space-1) 0 0;
    max-width: 78ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    padding: var(--z-space-5);
  }
  .note {
    margin: 0;
    padding: var(--z-space-3);
    border: 1px solid var(--z-pending-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-pending-subtle);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text);
  }
  .check {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    padding: var(--z-space-4);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
  }
  .check.good {
    border-color: var(--z-idle-border);
    background: var(--z-idle-subtle);
  }
  .check.bad {
    border-color: var(--z-danger-border);
    background: var(--z-danger-subtle);
  }
  .check-head {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    margin: 0;
  }
  .url {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
    overflow-wrap: anywhere;
  }
  .check-line {
    margin: 0;
    max-width: 78ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text);
  }
  .check-line.muted {
    color: var(--z-text-muted);
  }
  .filter {
    max-width: 200px;
  }
  .deliveries {
    display: flex;
    flex-direction: column;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .deliveries li {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: baseline;
    gap: var(--z-space-3);
    padding: var(--z-space-2) 0;
    border-bottom: 1px solid var(--z-border);
  }
  .deliveries li:last-child {
    border-bottom: 0;
  }
  .badge {
    align-self: center;
  }
  .what {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: var(--z-space-2);
    min-width: 0;
  }
  .event {
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
  .repo {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
    overflow-wrap: anywhere;
  }
  .failed {
    font-size: var(--z-text-xs);
    color: var(--z-danger);
    overflow-wrap: anywhere;
  }
  .when {
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
    white-space: nowrap;
  }
</style>
