<script lang="ts">
  import { fleet } from '$lib/state/fleet.svelte';
  import { prefs } from '$lib/state/prefs.svelte';
  import { session } from '$lib/state/session.svelte';
  import MetricGrid, { type Metric } from '$lib/components/MetricGrid.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import ChartPanel from '$lib/components/ChartPanel.svelte';
  import LifecycleFlow from './LifecycleFlow.svelte';
  import ProvisioningPulse from './ProvisioningPulse.svelte';
  import StateBreakdown from './StateBreakdown.svelte';
  import { poolSignals, finite, queueTone } from './signals';
  import { formatNumber, NO_VALUE } from '$lib/format';
  import { runnerStatus } from '$lib/status';
  let { poolId = '', compact = false }: { poolId?: string; compact?: boolean } = $props();
  const signal = $derived(poolSignals(fleet.pools, fleet.stats).find((p) => p.pool.id === poolId));
  const r = $derived(fleet.stats?.runners);
  const queue = $derived(poolId ? signal?.queued : finite(fleet.stats?.fleet?.queued_jobs));
  const busy = $derived(poolId ? signal?.busy : finite(r?.busy));
  const idle = $derived(poolId ? signal?.idle : finite(r?.idle));
  const scope = $derived(poolId ? (signal?.pool.name ?? 'Selected pool') : 'Fleet');
  const suffix = $derived(poolId ? `&pool_id=${encodeURIComponent(poolId)}` : '');
  const num = (value: number | null | undefined) =>
    value == null ? NO_VALUE : formatNumber(value);
  const metrics = $derived<Metric[]>([
    {
      label: 'Job queue depth',
      value: num(queue),
      detail: 'Jobs waiting to start',
      tone: queueTone(queue, session.meta?.queue_warning_threshold),
      href: `/jobs?state=queued${suffix}`,
    },
    {
      label: 'Busy runners',
      value: num(busy),
      detail: 'Executing a job',
      tone: 'busy',
      href: `/runners?state=busy${suffix}`,
    },
    {
      label: 'Idle runners',
      value: num(idle),
      detail: 'Registered and waiting for work',
      tone: 'success',
      href: `/runners?state=idle${suffix}`,
    },
    ...(poolId
      ? [
          {
            label: 'Live runners',
            value: num(signal?.live),
            detail: 'Includes startup and draining',
            href: `/runners?pool_id=${encodeURIComponent(poolId)}`,
          },
          {
            label: 'Pool headroom',
            value: num(signal?.headroom),
            detail: `Configured maximum: ${num(signal?.max)}; host fit still applies`,
            tone: signal?.atCeiling ? ('warning' as const) : ('neutral' as const),
          },
          {
            label: 'Busy share',
            value: signal?.live ? `${Math.round((100 * (busy ?? 0)) / signal.live)}%` : NO_VALUE,
            detail: 'Busy / live runners right now',
            progress: signal?.live ? (100 * (busy ?? 0)) / signal.live : null,
          },
        ]
      : [
          {
            label: 'Starting runners',
            value: r ? num((r.provisioning ?? 0) + (r.registering ?? 0)) : NO_VALUE,
            detail: 'Provisioning + registering',
            tone: 'accent' as const,
            href: '/runners?state=provisioning&state=registering',
          },
          {
            label: 'Draining runners',
            value: num(finite(r?.draining)),
            detail: 'Finishing or being removed',
            href: '/runners?state=draining',
          },
          {
            label: 'Failed runners',
            value: num(finite(r?.failed)),
            detail: 'Retained failed runner records',
            tone: (r?.failed ?? 0) > 0 ? ('danger' as const) : ('neutral' as const),
            href: '/runners?state=failed',
          },
        ]),
  ]);
  const segments = $derived(
    poolId
      ? [
          {
            label: runnerStatus('busy', prefs.quirkyStatus).label,
            value: busy ?? 0,
            tone: 'busy' as const,
            href: `/runners?state=busy${suffix}`,
            hint: runnerStatus('busy', prefs.quirkyStatus).hint,
          },
          {
            label: runnerStatus('idle', prefs.quirkyStatus).label,
            value: idle ?? 0,
            tone: 'idle' as const,
            href: `/runners?state=idle${suffix}`,
            hint: runnerStatus('idle', prefs.quirkyStatus).hint,
          },
          {
            label: 'Other live',
            value: Math.max(0, (signal?.live ?? 0) - (busy ?? 0) - (idle ?? 0)),
            tone: 'pending' as const,
            href: `/runners?pool_id=${encodeURIComponent(poolId)}`,
            hint: 'Starting up, or draining after its last job.',
          },
        ]
      : [
          {
            label: runnerStatus('provisioning', prefs.quirkyStatus).label,
            value: r?.provisioning ?? 0,
            tone: 'pending' as const,
            href: '/runners?state=provisioning',
            hint: runnerStatus('provisioning', prefs.quirkyStatus).hint,
          },
          {
            label: runnerStatus('registering', prefs.quirkyStatus).label,
            value: r?.registering ?? 0,
            tone: 'accent' as const,
            href: '/runners?state=registering',
            hint: runnerStatus('registering', prefs.quirkyStatus).hint,
          },
          {
            label: runnerStatus('idle', prefs.quirkyStatus).label,
            value: r?.idle ?? 0,
            tone: 'idle' as const,
            href: '/runners?state=idle',
            hint: runnerStatus('idle', prefs.quirkyStatus).hint,
          },
          {
            label: runnerStatus('busy', prefs.quirkyStatus).label,
            value: r?.busy ?? 0,
            tone: 'busy' as const,
            href: '/runners?state=busy',
            hint: runnerStatus('busy', prefs.quirkyStatus).hint,
          },
          {
            label: runnerStatus('draining', prefs.quirkyStatus).label,
            value: r?.draining ?? 0,
            tone: 'draining' as const,
            href: '/runners?state=draining',
            hint: runnerStatus('draining', prefs.quirkyStatus).hint,
          },
        ],
  );
</script>

<section aria-label="Runner operational context" class="context">
  <div class="scope">
    <span
      >{scope} context · {fleet.connection === 'live'
        ? 'live snapshot'
        : 'last known snapshot'}</span
    ><a href={`/usage?group_by=pool${poolId ? `&entity=${encodeURIComponent(poolId)}` : ''}`}
      >Explore usage history</a
    >
  </div>
  {#if !fleet.stats}<Skeleton lines={3} />{:else}<MetricGrid items={metrics} />{/if}
  <ProvisioningPulse {poolId} />
  {#if !compact && fleet.stats}<ChartPanel
      title="Runner lifecycle"
      description={poolId
        ? 'How this pool\u2019s live runners divide up right now. Failed and removed records are excluded from this bar.'
        : 'The states a runner passes through, in the order the controller allows, with how many are at each step right now. Failed and removed records are excluded from the bar.'}
    >
      {#if !poolId && r}<LifecycleFlow runners={r} />{/if}
      <StateBreakdown {segments} label="Live runner states" noun="live runners" />
    </ChartPanel>{/if}
  <p class="note">
    Job queue depth is waiting GitHub work, including jobs whose provisioning demand is held; it is
    not a count of runner creation requests.
    {#if compact}These figures describe the whole pool.{:else}Search, host and state filters below
      do not change this {poolId ? 'pool' : 'fleet'} context.{/if}
  </p>
</section>

<style>
  .context {
    margin-bottom: var(--z-space-5);
  }
  .scope {
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    gap: var(--z-space-2);
    font-size: var(--z-text-xs);
    margin: var(--z-space-3) 0;
    color: var(--z-text-muted);
  }
  a {
    color: var(--z-accent);
  }
  .note {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
    margin: var(--z-space-3) 0 0;
    max-width: 110ch;
  }
</style>
