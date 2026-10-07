<script lang="ts">
  import { getHost } from '$lib/api/client';
  import { events } from '$lib/api/sse';
  import type { Host } from '$lib/api/types';
  import { href, router } from '$lib/router';
  import { fleet } from '$lib/state/fleet.svelte';
  import { session } from '$lib/state/session.svelte';
  import { onClockTick } from '$lib/format';
  import {
    attention as attentionOf,
    bySeverity,
    counts,
    healthSummary,
    isFinding,
    type DoctorResult,
  } from '$lib/hosts/health';
  import { cordon } from '$lib/hosts/actions';
  import { DOCTOR_COMMAND, nextStep, rebootAdvice, rebootPending } from '$lib/hosts/next-step';
  import {
    REPORT_ONLY_SAFE_DESCRIPTION,
    reportOnly,
    reportOnlySentence,
    reportOnlySubtitle,
    reportOrigin,
  } from '$lib/hosts/report-only';
  import { noReport, noReportSubtitle } from '$lib/hosts/no-report';
  import { KIND_WORDS, previewCommand, rowKind } from '$lib/hosts/row-kind';
  import { pluralise } from '$lib/format';
  import type { StatusTone } from '$lib/status';
  import { tick, untrack } from 'svelte';
  import { ServerCog } from '@lucide/svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
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
  // Whether this report can be acted on at all: a Mac, an unsupported distribution
  // and a container can be read but not tuned, and the page must not say otherwise.
  const readOnly = $derived(reportOnly(report));
  const summary = $derived(healthSummary(report, now, host?.healthy ?? true));
  // What needs doing, worst first. Counted checks only, which is what the badge
  // and `zoomies doctor` count: the other tiers are choices, not faults.
  const attention = $derived(report ? attentionOf(report) : []);

  // Who may cordon. A viewer is told what to do in the report instead, and
  // never offered a button the controller would refuse.
  const canOperate = $derived(session.can('operator'));
  let cordoning = $state(false);
  let cordonedBefore = $state(false);
  // While a request is in flight the cache already shows the optimistic value,
  // and a host frame that landed first can overwrite it. The value from before
  // the click is the only one nobody has argued with, and "safe to reboot" is
  // never said on the strength of a cordon the controller has not confirmed.
  const cordonedNow = $derived(cordoning ? cordonedBefore : host?.cordoned === true);
  const step = $derived(host ? nextStep({ host, report, cordoned: cordonedNow }) : null);
  const showStep = $derived(canOperate && step !== null);
  // The doctor hint stays wherever the panel has no command of its own, which
  // is for a viewer, and for a cordoned host with nothing waiting on it.
  const showCommand = $derived(showStep && step?.where != null);
  const missing = $derived(host && !report ? noReport({ host, now, canOperate }) : null);

  // Scoped, never getElementById: a host chooses its own check ids, and one
  // called "main" or "page-heading" must not win the lookup.
  function land(): void {
    let wanted = '';
    try {
      wanted = decodeURIComponent(location.hash.slice(1));
    } catch {
      return;
    }
    if (!wanted) return;
    const row = [...document.querySelectorAll<HTMLElement>('.health-content tr[id]')].find(
      (r) => r.id === wanted,
    );
    // Fixed since, or folded away: stay where the page put us. A closed
    // <details> is never opened to land, findings are never inside one.
    if (!row || row.closest('details:not([open])') || row.offsetParent === null) return;
    document.querySelector('.health-content tr[data-landed]')?.removeAttribute('data-landed');
    // Instant on purpose, so reduced motion needs no branch of its own.
    row.scrollIntoView({ block: 'start' });
    row.focus({ preventScroll: true });
    row.setAttribute('data-landed', '');
  }
  // One landing per navigation and hash. Without the key, the host.doctor frame
  // that swaps the report in every few seconds would scroll the page back each time.
  let landedFor = '';
  $effect(() => {
    if (!report) return;
    const key = `${router.navigation}:${location.hash}`;
    if (untrack(() => landedFor) === key) return;
    landedFor = key;
    // After App.svelte has put focus on the page heading, or it would take it back.
    void tick().then(() => requestAnimationFrame(land));
  });
  // The in-page links under "Needs attention" are plain #id links, which the
  // router leaves to the browser; this is what moves focus for them too.
  $effect(() => {
    const onHash = () => {
      landedFor = `${untrack(() => router.navigation)}:${location.hash}`;
      land();
    };
    window.addEventListener('hashchange', onHash);
    return () => window.removeEventListener('hashchange', onHash);
  });

  async function toggleCordon(): Promise<void> {
    if (!host || cordoning) return;
    cordonedBefore = host.cordoned === true;
    // A second press would send the opposite request.
    cordoning = true;
    try {
      await cordon(host, !cordonedBefore);
    } finally {
      cordoning = false;
    }
    // Uncordoning a host with nothing else waiting on it takes the panel away,
    // and a button removed under focus drops it to <body>. Put it on the page's
    // name, where route navigation puts it, unless the person has moved on.
    await tick();
    const active = document.activeElement;
    if (!showStep && (!active || active === document.body))
      document.getElementById('page-heading')?.focus();
  }
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
  // Only said where a button can appear, so a viewer or a report-only host is
  // not told about one it will never see.
  const PREVIEW_SENTENCE =
    'A Fixable row has a button that copies a read-only preview to run on the host. It shows the exact change and makes none. Zoomies never runs it.';
  const DEDICATED_SENTENCE = 'Only for a host that runs nothing but Zoomies.';
  function tierDescription(tier: (typeof tiers)[number]): string {
    const base =
      tier === 'safe' && readOnly
        ? REPORT_ONLY_SAFE_DESCRIPTION
        : tier === 'dedicated'
          ? 'Only for hosts running nothing but Zoomies. These changes are never included in safe or aggressive defaults, and do not count towards this host’s health.'
          : tier === 'aggressive'
            ? 'Optional tuning, and not counted towards this host’s health. Applying a change needs --tier aggressive and consent in the CLI.'
            : 'Read-only findings. Applying a change requires consent in the CLI.';
    if (!canOperate || readOnly) return base;
    return `${base} ${PREVIEW_SENTENCE}${tier === 'dedicated' ? ` ${DEDICATED_SENTENCE}` : ''}`;
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
  subtitle={reportOnlySubtitle(readOnly) ??
    (missing
      ? noReportSubtitle()
      : 'OS checks from the native Zoomies binary. Fixes require explicit consent on the host.')}
  breadcrumb={[{ label: 'Hosts', href: '/hosts' }, { label: host?.name || id }]}
  onrefresh={refresh}
>
  <Badge label={summary.label} tone={summary.tone} title={summary.hint} />
</PageHeader>
{#if error}<ErrorState {error} onretry={refresh} />
{:else if loading && !host}<Skeleton />
{:else if !report}
  <div class="health-content">
    {@render nextPanel()}
    {#if missing}
      <Panel title="No health report yet" description={missing.description}>
        <p>
          {missing.detail}
          {#if missing.tail === 'heartbeat' && host?.last_heartbeat}
            Its last heartbeat was <RelativeTime value={host.last_heartbeat} />.{/if}
          {#if missing.tail === 'joined'}
            This host joined <RelativeTime value={host?.created_at} />.{/if}
          {#if missing.tail === 'joined-late'}
            It joined <RelativeTime value={host?.created_at} />, so a report may still be on its
            way.{/if}
          {#if missing.tail === 'since-joined'}
            It joined <RelativeTime value={host?.created_at} />.{/if}
        </p>
        {#if missing.note}<p>{missing.note}</p>{/if}
        {#if missing.command && missing.copyLabel}
          <div class="command">
            {#if missing.commandCaption}<p>{missing.commandCaption}</p>{/if}
            <div class="command-row">
              <pre><code>{missing.command}</code></pre>
              <CopyButton value={missing.command} label={missing.copyLabel} size="md" showLabel />
            </div>
            {#if missing.after}<p>{missing.after}</p>{/if}
          </div>
        {/if}
      </Panel>
    {/if}
  </div>
{:else}
  <div class="health-content">
    <!-- First, so that a phone meets the action before a long list of findings. -->
    {@render nextPanel()}
    <Panel title="Latest host report" description={summary.hint}>
      <p>{reportOrigin(report)} · Checked <RelativeTime value={report.checked_at} /></p>
      {#if !showStep && rebootPending(report)}
        <!-- Cordon, never drain: drain cordons and then stops a runner still busy after
             five minutes (host_health_problems.go), which is the wrong advice for waiting
             for jobs to finish. -->
        <p>{rebootAdvice()}</p>
      {/if}
      {#if !showCommand && readOnly}
        <p>{reportOnlySentence(readOnly, report.os)}</p>
      {:else if !showCommand}
        <p>
          Review changes locally with <code>sudo zoomies doctor --interactive</code> or preview them
          with <code>sudo zoomies tune --dry-run</code>.
        </p>
      {/if}
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
          description={tierDescription(tier)}
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

{#snippet nextPanel()}
  {#if showStep && step && host}
    <Panel title="Next step">
      <div class="next">
        <!-- No runner count in the headline: this is the region a screen reader
             speaks when it changes, and it should change when the state does,
             not on every heartbeat that moves a count. -->
        <div class="verdict" role="status">
          <p class="headline">{step.headline}</p>
          <p>{step.detail}</p>
          {#if step.verdict === 'busy' && host.id}
            <a href={href('/runners', { host_id: host.id, state: 'busy' })}
              >Show the runners running a job</a
            >
          {/if}
        </div>
        {#if step.embeddedNote}<p>{step.embeddedNote}</p>{/if}
        <dl class="facts">
          <dt>Runners on this host</dt>
          <dd>{step.runners}</dd>
          <dt>New work</dt>
          <dd>{step.placement}</dd>
        </dl>
        <div class="act">
          <Button
            variant="secondary"
            icon={ServerCog}
            loading={cordoning}
            onclick={() => void toggleCordon()}>{step.button.label}</Button
          >
          <p>{step.button.help}</p>
        </div>
        {#if step.where}
          <div class="command">
            <p>{step.where}</p>
            <div class="command-row">
              <pre><code>{DOCTOR_COMMAND}</code></pre>
              <CopyButton value={DOCTOR_COMMAND} label="Copy command" size="md" showLabel />
            </div>
            <p>It shows each change before it makes it. Zoomies never runs it for you.</p>
          </div>
        {/if}
      </div>
    </Panel>
  {/if}
{/snippet}

{#snippet statusCell(check: DoctorResult)}
  {@const badge = statusBadge(check)}
  {@const kind = rowKind(check, readOnly !== null)}
  {@const command = kind === 'fixable' && canOperate ? previewCommand(check) : null}
  <span class="status">
    <Badge label={badge.label} tone={badge.tone} />
    {#if kind}<span class="kind">{KIND_WORDS[kind]}</span>{/if}
    {#if command}
      <CopyButton value={command} size="md" label={`Copy preview command for ${check.id}`} />
    {/if}
  </span>
{/snippet}

{#snippet checkTable(label: string, rows: DoctorResult[])}
  <!-- Keyboard focus enables horizontal scrolling on narrow screens. -->
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div class="checks" role="region" aria-label={label} tabindex="0">
    <!-- The roles are spelled out because a phone turns each row into a card
         with display:block and grid, which makes some browsers drop the table
         semantics a screen reader relies on. -->
    <!-- svelte-ignore a11y_no_redundant_roles -->
    <table role="table">
      <!-- svelte-ignore a11y_no_redundant_roles -->
      <thead role="rowgroup">
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <tr role="row">
          <th role="columnheader" scope="col">Check</th>
          <th role="columnheader" scope="col">Status</th>
          <th role="columnheader" scope="col">Current</th>
          <th role="columnheader" scope="col">Recommended</th>
          <th role="columnheader" scope="col">Why / details</th>
        </tr>
      </thead>
      <!-- svelte-ignore a11y_no_redundant_roles -->
      <tbody role="rowgroup">
        {#each rows as check (check.id)}
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <tr role="row" id={check.id} tabindex="-1">
            <th role="rowheader" scope="row">{check.title}<small>{check.id}</small></th>
            <td role="cell" data-label="Status">{@render statusCell(check)}</td>
            <td role="cell" data-label="Current">{check.current || '—'}</td>
            <td role="cell" data-label="Recommended">{check.recommended || '—'}</td>
            <td role="cell" data-label="Why / details"
              >{check.rationale}{#if check.reason}<small>{check.reason}</small>{/if}</td
            >
          </tr>
        {/each}
      </tbody>
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
  .next {
    display: grid;
    gap: var(--z-space-4);
    min-width: 0;
  }
  /* The page's own p rule is muted and carries a bottom margin; inside the
     panel the grid's gap does the spacing. */
  .next p {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .verdict {
    display: grid;
    gap: var(--z-space-2);
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border-strong);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
  }
  /* No status colour: the verdict is a sentence to read, and the colours
     belong to what a host is (docs/ui-guidelines.md), not to what to do next. */
  .headline {
    color: var(--z-text);
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
  }
  .verdict a {
    color: var(--z-accent);
    text-decoration: underline;
  }
  .facts {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: var(--z-space-2) var(--z-space-4);
    margin: 0;
    font-size: var(--z-text-sm);
  }
  .facts dt {
    color: var(--z-text-muted);
  }
  .facts dd {
    margin: 0;
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .act {
    display: grid;
    gap: var(--z-space-2);
    justify-items: start;
  }
  .act p,
  .command p {
    font-size: var(--z-text-xs);
  }
  .command {
    display: grid;
    gap: var(--z-space-2);
  }
  .command-row {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-start;
    gap: var(--z-space-3);
  }
  /* The command wraps rather than scrolling, so a phone never has to scroll
     sideways to read what it is about to copy. */
  .command-row pre {
    flex: 1 1 16rem;
    min-width: 0;
    margin: 0;
    padding: var(--z-space-3);
    background: var(--z-surface-sunken);
    border-radius: var(--z-radius-sm);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  @media (max-width: 768px) {
    .act {
      justify-items: stretch;
    }
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
    min-width: 0;
  }
  /* Five columns at 9rem each floor the table at 720px, which is right for a
     table laid out as one at desktop widths but scrolled it sideways from 769 to
     about 855px. Freeing the floors everywhere moved the desktop columns (the
     Recommended one lost a third of its width at 1181 and 1440), so only the
     tablet band takes the smaller floors and desktop is as it was. The phone
     block below must not inherit either. */
  @media (min-width: 769px) {
    th,
    td {
      min-width: 9rem;
    }
  }
  @media (min-width: 769px) and (max-width: 855px) {
    th,
    td {
      min-width: 0;
    }
    td:last-child {
      min-width: 12rem;
    }
    th[scope='row'],
    td:nth-child(2) {
      min-width: 9rem;
    }
  }
  th {
    font-weight: var(--z-weight-medium);
  }
  .status {
    display: inline-flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
  }
  .kind {
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
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
  /* data-landed is set from script, so the compiler cannot see it. */
  tr:target,
  tr:global([data-landed]) {
    background: var(--z-accent-subtle);
  }
  @media (max-width: 768px) {
    .checks {
      overflow-x: visible;
    }
    table {
      display: block;
    }
    thead {
      display: none;
    }
    tbody {
      display: flex;
      flex-direction: column;
      gap: var(--z-space-3);
      padding: var(--z-space-3);
    }
    /* No background on the card: it would out-specify the data-landed tint and
       hide landing on a phone. The panel body is already the surface colour. */
    tbody tr {
      display: grid;
      grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
      gap: var(--z-space-2) var(--z-space-4);
      padding: var(--z-space-3);
      border: var(--z-border-width) solid var(--z-border);
      border-radius: var(--z-radius-md);
    }
    tbody th,
    tbody td {
      padding: 0;
      border: 0;
    }
    tbody th[scope='row'] {
      grid-column: 1;
    }
    /* The badge names itself, so it takes the title's line and no label. */
    td:nth-of-type(1) {
      grid-column: 2;
      grid-row: 1;
      justify-self: end;
    }
    td:nth-of-type(1)::before {
      display: none;
    }
    td:nth-of-type(4) {
      grid-column: 1 / -1;
    }
    td::before {
      content: attr(data-label);
      display: block;
      color: var(--z-text-muted);
      font-size: var(--z-text-2xs);
      font-weight: var(--z-weight-medium);
      text-transform: uppercase;
      letter-spacing: var(--z-tracking-wide);
    }
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
