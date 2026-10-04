<script lang="ts">
  import { getHost } from '$lib/api/client';
  import { events } from '$lib/api/sse';
  import type { Host } from '$lib/api/types';
  import { router } from '$lib/router';
  import { fleet } from '$lib/state/fleet.svelte';
  import { onClockTick } from '$lib/format';
  import { healthSummary } from '$lib/hosts/health';
  import Badge from '$lib/components/Badge.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  const id = $derived(router.params.id ?? '');
  let fetched = $state<Host | null>(null);
  let error = $state<unknown>(null);
  let loading = $state(true);
  let now = $state(Date.now());
  $effect(() => onClockTick((t) => (now = t)));
  const host = $derived(fleet.hosts.find((h) => h.id === id) ?? fetched);
  const report = $derived(host?.doctor);
  const summary = $derived(healthSummary(report, now, host?.healthy ?? true));
  const tiers = ['safe', 'aggressive', 'dedicated'] as const;
  async function refresh(): Promise<void> {
    try {
      fetched = await getHost(id);
      error = null;
    } catch (cause) {
      error = cause;
    } finally {
      loading = false;
    }
  }
  $effect(() => {
    const hostId = id;
    const controller = new AbortController();
    loading = true;
    fetched = null;
    getHost(hostId, controller.signal)
      .then((h) => {
        fetched = h;
        error = null;
      })
      .catch((cause: unknown) => {
        if (!(cause instanceof DOMException && cause.name === 'AbortError')) error = cause;
      })
      .finally(() => {
        if (!controller.signal.aborted) loading = false;
      });
    const off = events.subscribe('host.updated', (h) => {
      if (h.id === hostId) fetched = h;
    });
    return () => {
      controller.abort();
      off();
    };
  });
</script>

<PageHeader
  title={host?.name || 'Host health'}
  subtitle="OS checks from the native Zoomies binary. Fixes require explicit consent on the host."
  breadcrumb={[{ label: 'Hosts', href: '/hosts' }, { label: host?.name || id }]}
  onrefresh={refresh}
>
  <Badge label={summary.label} tone={summary.tone} title={summary.hint} />
</PageHeader>
{#if error}<ErrorState {error} onretry={refresh} />
{:else if loading && !host}<Skeleton />
{:else if !report}
  <Panel
    title="No health report yet"
    description="Check zoomies-host-health.service for a container deployment, or update the native agent. Run zoomies doctor directly on the host for an immediate report."
    ><p>No tuning can be applied from this page.</p></Panel
  >
{:else}
  <div class="health-content">
    <Panel title="Latest host report" description={summary.hint}>
      <p>{report.os} · {report.distro} · Checked <RelativeTime value={report.checked_at} /></p>
      {#if report.reboot_pending}<p>
          A reboot is pending. Drain the host before rebooting manually. Zoomies never reboots it.
        </p>{/if}
      <p>
        Review changes locally with <code>sudo zoomies doctor --interactive</code> or preview them
        with <code>sudo zoomies tune --dry-run</code>.
      </p>
    </Panel>
    {#each tiers as tier (tier)}
      {@const checks = report.results.filter((r) => r.tier === tier)}
      {#if checks.length}
        <Panel
          title={tier === 'safe'
            ? 'Safe checks'
            : tier === 'aggressive'
              ? 'Aggressive checks'
              : 'Dedicated host checks'}
          description={tier === 'dedicated'
            ? 'Only for hosts running nothing but Zoomies. These changes are never included in safe or aggressive defaults.'
            : 'Read-only findings. Applying a change requires consent in the CLI.'}
          flush
        >
          <!-- Keyboard focus enables horizontal scrolling on narrow screens. -->
          <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
          <div class="checks" role="region" aria-label={`${tier} host checks`} tabindex="0">
            <table>
              <thead
                ><tr
                  ><th scope="col">Check</th><th scope="col">Status</th><th scope="col">Current</th
                  ><th scope="col">Recommended</th><th scope="col">Why / details</th></tr
                ></thead
              >
              <tbody
                >{#each checks as check (check.id)}<tr>
                    <th scope="row">{check.title}<small>{check.id}</small></th>
                    <td
                      ><Badge
                        label={check.status === 'warn'
                          ? 'Warning'
                          : check.status === 'skip'
                            ? 'Skipped'
                            : check.status === 'error'
                              ? 'Error'
                              : 'OK'}
                        tone={check.status === 'warn'
                          ? 'pending'
                          : check.status === 'error'
                            ? 'danger'
                            : check.status === 'ok'
                              ? 'idle'
                              : 'neutral'}
                      /></td
                    >
                    <td>{check.current || '—'}</td><td>{check.recommended || '—'}</td><td
                      >{check.rationale}{#if check.reason}<small>{check.reason}</small>{/if}</td
                    >
                  </tr>{/each}</tbody
              >
            </table>
          </div>
        </Panel>
      {/if}
    {/each}
  </div>
{/if}

<style>
  .health-content {
    display: grid;
    gap: var(--z-space-4);
  }
  p {
    color: var(--z-text-muted);
    margin: 0 0 var(--z-space-3);
  }
  code {
    font-family: var(--z-font-mono);
    overflow-wrap: anywhere;
  }
  .checks {
    overflow-x: auto;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: var(--z-text-sm);
  }
  th,
  td {
    padding: var(--z-space-3) var(--z-space-4);
    text-align: left;
    vertical-align: top;
    border-bottom: var(--z-border-width) solid var(--z-border);
    overflow-wrap: anywhere;
    min-width: 9rem;
  }
  th {
    font-weight: var(--z-weight-medium);
  }
  small {
    display: block;
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
    margin-top: var(--z-space-1);
  }
  thead {
    background: var(--z-surface-raised);
  }
</style>
