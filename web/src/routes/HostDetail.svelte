<script lang="ts">
  import { getHost } from '$lib/api/client';
  import { events } from '$lib/api/sse';
  import type { Host } from '$lib/api/types';
  import { router } from '$lib/router';
  import { fleet } from '$lib/state/fleet.svelte';
  import { onClockTick } from '$lib/format';
  import {
    attention as attentionOf,
    bySeverity,
    counts,
    healthSummary,
    isFinding,
    type DoctorResult,
  } from '$lib/hosts/health';
  import { pluralise } from '$lib/format';
  import type { StatusTone } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import HostDoctorCommand from '$lib/hosts/HostDoctorCommand.svelte';
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
  // What needs doing, worst first. Counted checks only, which is what the badge
  // and `zoomies doctor` count: the other tiers are choices, not faults.
  const attention = $derived(report ? attentionOf(report) : []);
  const tiers = ['safe', 'aggressive', 'dedicated'] as const;
  // A warning that does not count says so in its own word and tone, or the
  // header would read "Health OK" above a column of amber "Warning" badges.
  function statusBadge(check: DoctorResult): { label: string; tone: StatusTone } {
    if (check.status === 'error') return { label: 'Error', tone: 'danger' };
    if (check.status === 'warn')
      return counts(check)
        ? { label: 'Warning', tone: 'pending' }
        : { label: 'Suggestion', tone: 'neutral' };
    if (check.status === 'ok') return { label: 'OK', tone: 'idle' };
    return { label: 'Skipped', tone: 'neutral' };
  }
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
    <Panel
      title="Read this report from a terminal"
      description="From any machine that has zoomies installed, with the controller's address and a short-lived token already in the command."
    >
      <HostDoctorCommand hostId={host?.id ?? id} />
    </Panel>
    {#if attention.length}
      <Panel
        title="Needs attention"
        description="{pluralise(
          attention.length,
          'check',
        )} below what Zoomies recommends for a CI host, worst first. Select one to see its row."
      >
        <ul class="attention">
          {#each attention as check (check.id)}
            {@const badge = statusBadge(check)}
            <li>
              <Badge label={badge.label} tone={badge.tone} />
              <a href="#{check.id}">{check.title}</a>
              <span class="now"
                >now <code>{check.current || '—'}</code>, recommended
                <code>{check.recommended || '—'}</code></span
              >
            </li>
          {/each}
        </ul>
      </Panel>
    {/if}
    {#each tiers as tier (tier)}
      {@const checks = bySeverity(report.results.filter((r) => r.tier === tier))}
      {@const findings = checks.filter(isFinding)}
      {@const rest = checks.filter((r) => !isFinding(r))}
      {#if checks.length}
        <Panel
          title={tier === 'safe'
            ? 'Safe checks'
            : tier === 'aggressive'
              ? 'Aggressive checks'
              : 'Dedicated host checks'}
          description={tier === 'dedicated'
            ? 'Only for hosts running nothing but Zoomies. These changes are never included in safe or aggressive defaults, and do not count towards this host’s health.'
            : tier === 'aggressive'
              ? 'Optional tuning, and not counted towards this host’s health. Applying a change needs --tier aggressive and consent in the CLI.'
              : 'Read-only findings. Applying a change requires consent in the CLI.'}
          flush
        >
          {#if findings.length}{@render checkTable(`${tier} host checks`, findings)}{/if}
          {#if rest.length}
            <!-- Folded when there is something to find, so the finding is the
                 first thing on the page rather than the tenth row under nine
                 passing ones. Open when there is nothing, so a healthy host's
                 page shows what was checked. -->
            <details class="rest" open={!findings.length}>
              <summary>{pluralise(rest.length, 'passing or skipped check')}</summary>
              {@render checkTable(`${tier} passing and skipped host checks`, rest)}
            </details>
          {/if}
        </Panel>
      {/if}
    {/each}
  </div>
{/if}

{#snippet checkTable(label: string, rows: DoctorResult[])}
  <!-- Keyboard focus enables horizontal scrolling on narrow screens. -->
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div class="checks" role="region" aria-label={label} tabindex="0">
    <table>
      <thead
        ><tr
          ><th scope="col">Check</th><th scope="col">Status</th><th scope="col">Current</th><th
            scope="col">Recommended</th
          ><th scope="col">Why / details</th></tr
        ></thead
      >
      <tbody
        >{#each rows as check (check.id)}
          {@const badge = statusBadge(check)}
          <tr id={check.id}>
            <th scope="row">{check.title}<small>{check.id}</small></th>
            <td><Badge label={badge.label} tone={badge.tone} /></td>
            <td>{check.current || '—'}</td><td>{check.recommended || '—'}</td><td
              >{check.rationale}{#if check.reason}<small>{check.reason}</small>{/if}</td
            >
          </tr>{/each}</tbody
      >
    </table>
  </div>
{/snippet}

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
  /* A link from "Needs attention" lands the row under the top bar otherwise,
     and the row it landed on should say so. */
  tr[id] {
    scroll-margin-top: calc(var(--z-topbar-height) + var(--z-space-4));
  }
  tr:target {
    background: var(--z-accent-subtle);
  }
  .attention {
    display: grid;
    gap: var(--z-space-3);
    margin: 0;
    padding: 0;
    list-style: none;
    font-size: var(--z-text-sm);
  }
  .attention li {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: var(--z-space-2) var(--z-space-3);
  }
  .attention a {
    color: var(--z-accent);
    text-decoration: underline;
  }
  .now {
    color: var(--z-text-muted);
    overflow-wrap: anywhere;
  }
  .rest summary {
    cursor: pointer;
    padding: var(--z-space-3) var(--z-space-4);
    color: var(--z-text-muted);
    font-size: var(--z-text-sm);
  }
  .rest[open] summary {
    border-bottom: var(--z-border-width) solid var(--z-border);
  }
  .rest tbody tr:last-child > * {
    border-bottom: 0;
  }
</style>
