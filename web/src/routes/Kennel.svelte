<!--
  Kennel Club: how the repositories this fleet serves measure up against what
  affects CI and the fleet.

  The states are written before the happy path, because most people meet this
  page off, or empty, or part-read, and a page that is only good when
  everything is on and read is a page that explains nothing the first time:

    off        what it does, what it reads and what it never does
    empty      on, with nothing looked at yet
    partial    the counts are a minimum, and the page says so above them
    attention  the repositories to open first, and the checks behind them
    best       the repositories with nothing open

  It works with Kennel Club off, because GET /kennel answers 200 with
  enabled:false: the page needs a document to explain itself from, and "off" is
  a state it describes, not a failure it reports.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { BookOpenText, Eye, ListChecks, Trophy } from '@lucide/svelte';
  import { getKennelOverview, listKennelChecks } from '$lib/api/client';
  import { events } from '$lib/api/sse';
  import type { KennelCatalogueEntry, KennelOverview } from '$lib/api/types';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import LoadingBoundary from '$lib/components/LoadingBoundary.svelte';
  import MetricGrid from '$lib/components/MetricGrid.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import ChecksTable from '$lib/kennel/ChecksTable.svelte';
  import {
    KENNEL_SETTING_HREF,
    coverageStatesText,
    countsAreAFloor,
    floorSentence,
    openFindingsText,
    worstCoverageState,
    worstSeverity,
  } from '$lib/kennel/words';
  import { formatNumber } from '$lib/format';
  import { fleet } from '$lib/state/fleet.svelte';
  import { prefs } from '$lib/state/prefs.svelte';
  import { session } from '$lib/state/session.svelte';
  import { kennelCoverageStatus, kennelStatus } from '$lib/status';

  let overview = $state<KennelOverview | null>(null);
  let catalogue = $state<KennelCatalogueEntry[]>([]);
  let loading = $state(true);
  let loaded = $state(false);
  let error = $state<unknown>(null);
  /** A refresh failed after a good load: the page keeps what it had and says so. */
  let stale = $state(false);
  let reload = $state(0);

  const canAdmin = $derived(session.can('admin'));
  const enabled = $derived(overview?.enabled === true);

  $effect(() => {
    void reload;
    const controller = new AbortController();
    loading = true;
    void getKennelOverview(controller.signal)
      .then((next) => {
        overview = next;
        error = null;
        stale = false;
        loaded = true;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        if (untrack(() => loaded)) stale = true;
        else error = cause;
      })
      .finally(() => (loading = false));
    return () => controller.abort();
  });

  // What is checked is what the Off page explains itself with. It is the
  // registry the evaluator runs, so the page and the checks cannot disagree.
  $effect(() => {
    if (!loaded || enabled) return;
    const controller = new AbortController();
    void listKennelChecks(controller.signal)
      .then((next) => (catalogue = next.items ?? []))
      .catch(() => {
        // The explanation stands without the list.
      });
    return () => controller.abort();
  });

  // The summary is computed and sent when it changes, since nothing writes a row
  // when an evaluation grows older. A frame replaces the document whole.
  $effect(() =>
    events.subscribe('kennel.summary', (next) => {
      overview = next;
      error = null;
      stale = false;
      loaded = true;
    }),
  );

  // A page-local subscription does nothing across a reconnect on its own, so
  // this one asks again when the stream says it lost its place and when it comes
  // back after being down.
  $effect(() => events.subscribe('resync', () => (reload += 1)));
  let previous = '';
  $effect(() => {
    const next = fleet.connection;
    if (next === 'live' && previous && previous !== 'live') untrack(() => (reload += 1));
    previous = next;
  });

  const floor = $derived(overview ? countsAreAFloor(overview) : false);
  const floorText = $derived(overview ? floorSentence(overview) : '');
  const bestWord = $derived(kennelStatus('best_in_show', undefined, prefs.quirkyStatus).label);

  function capitalise(word: string): string {
    return word ? word.charAt(0).toUpperCase() + word.slice(1) : word;
  }

  const metrics = $derived.by(() => {
    if (!overview) return [];
    const { states, counts } = overview;
    const atLeast = floor ? 'At least. ' : '';
    return [
      {
        label: 'Repositories',
        value: formatNumber(overview.repositories),
        detail: overview.scope === 'served' ? 'This fleet has run jobs for' : 'The App can see',
      },
      {
        label: bestWord,
        value: formatNumber(states.best_in_show),
        detail: 'Nothing worse than a note is open',
        tone: 'success' as const,
      },
      {
        label: 'Need attention',
        value: formatNumber(states.attention),
        detail: `${atLeast}An error or a warning is open`,
        tone: states.attention > 0 ? ('danger' as const) : ('neutral' as const),
      },
      {
        label: 'Partly checked',
        value: formatNumber(states.partial + states.pending),
        detail: 'Something could not be read, or not looked at yet',
      },
      {
        label: 'Errors',
        value: formatNumber(counts.error),
        detail: `${atLeast}Open across every repository`,
        tone: counts.error > 0 ? ('danger' as const) : ('neutral' as const),
      },
      {
        label: 'Warnings',
        value: formatNumber(counts.warning),
        detail: `${atLeast}Open across every repository`,
        tone: counts.warning > 0 ? ('warning' as const) : ('neutral' as const),
      },
      {
        label: 'Waived',
        value: formatNumber(counts.waived),
        detail: 'Somebody decided these are acceptable',
      },
    ];
  });
</script>

<PageHeader
  title="Kennel Club"
  subtitle="How the repositories this fleet serves measure up against what affects CI and the fleet."
  onrefresh={() => {
    reload += 1;
  }}
>
  {#snippet meta()}
    {#if enabled && overview?.oldest_evaluation}
      <p class="summary">
        Oldest evaluation <RelativeTime value={overview.oldest_evaluation} />
      </p>
    {/if}
  {/snippet}
  {#if enabled}
    <Button icon={ListChecks} href="/kennel/repositories">All repositories</Button>
  {/if}
  <Button icon={BookOpenText} href="/kennel/ai-context">AI Context</Button>
</PageHeader>

<div class="content">
  <LoadingBoundary
    loading={loading && !loaded}
    error={loaded ? null : error}
    onretry={() => (reload += 1)}
  >
    {#snippet skeleton()}
      <div class="tiles">
        {#each [0, 1, 2, 3] as tile (tile)}
          <div class="tile-skeleton">
            <Skeleton width="50%" height="0.875rem" />
            <Skeleton width="30%" height="1.75rem" />
          </div>
        {/each}
      </div>
      <Skeleton lines={5} />
    {/snippet}

    {#if overview}
      {#if stale}
        <div class="notice" role="status">
          <p>
            The last refresh did not get through, so this is what was last received.
            <Button size="sm" variant="ghost" onclick={() => (reload += 1)}>Try again</Button>
          </p>
        </div>
      {/if}

      {#if !overview.enabled}
        <Panel title="Kennel Club is off">
          <div class="explain">
            <p>
              Kennel Club looks at the repositories your fleet has run jobs for and says which of
              them put the fleet at risk, or make it work harder than it should: a public repository
              whose pull requests run on a machine that keeps state between jobs, or a label no pool
              serves.
            </p>
            <h3>What it reads</h3>
            <ul>
              <li>
                GitHub's own record of each repository: its name and whether it is public. Nothing
                more than the App already has permission to see.
              </li>
              <li>
                For a public repository this fleet has run jobs for, how each recent workflow run
                was started and from where. Private repositories cost no request beyond the shared
                list.
              </li>
              <li>Zoomies' own record of its jobs and pools.</li>
            </ul>
            <h3>What it never does</h3>
            <ul>
              <li>Read a repository's files or a workflow's contents.</li>
              <li>Change a repository, a setting or a workflow.</li>
              <li>
                Spend more than its share of GitHub's request limit, or read at all while an
                installation is held for a rate limit.
              </li>
            </ul>
            {#if canAdmin}
              <p>
                <Button variant="primary" icon={Eye} href={KENNEL_SETTING_HREF}
                  >Turn on in Settings</Button
                >
              </p>
            {:else}
              <p class="note">
                An administrator can turn it on under Settings, Configuration, kennel.enabled.
              </p>
            {/if}
          </div>
        </Panel>

        {#if catalogue.length > 0}
          <Panel title="What it checks" flush>
            <ChecksTable rows={catalogue} label="What Kennel Club checks" />
          </Panel>
        {/if}
      {:else if overview.repositories === 0}
        <EmptyState
          icon={Trophy}
          title="Nothing to look at yet"
          description="Kennel Club looks at repositories your fleet has run jobs for. None yet."
        />
        <Panel title="What it checks" flush>
          <ChecksTable rows={overview.checks} label="What Kennel Club checks" />
        </Panel>
      {:else}
        {#if floorText}
          <p class="floor" role="note">{floorText}</p>
        {/if}

        <MetricGrid items={metrics} />

        {#each overview.unavailable as note (note.installation_id)}
          {@const status = kennelCoverageStatus(note.state)}
          <div class="notice" role="note">
            <p>
              <Badge {status} size="sm" />
              <strong>{note.target}</strong>: {note.reason}
              <span class="since">since <RelativeTime value={note.since} plain /></span>
            </p>
          </div>
        {/each}

        <Panel
          title="Needs attention"
          description="The repositories with the worst findings open, worst first."
          flush
        >
          {#snippet actions()}
            <a href="/kennel/repositories?state=attention">See all</a>
          {/snippet}
          {#if overview.attention.length === 0}
            <EmptyState
              compact
              title="Nothing needs attention"
              description="No repository has an error or a warning open."
            />
          {:else}
            <ul class="rows">
              {#each overview.attention as row (row.id)}
                <li>
                  <a href="/kennel/repositories/{encodeURIComponent(row.id)}/ci">
                    <span class="name">{row.name}</span>
                    <Badge
                      status={kennelStatus(
                        row.state,
                        worstSeverity(row.counts),
                        prefs.quirkyStatus,
                      )}
                      size="sm"
                    />
                    {#if row.visibility}
                      <Badge
                        tone="neutral"
                        label={capitalise(row.visibility)}
                        size="sm"
                        dot={false}
                      />
                    {/if}
                    <span class="counts">{openFindingsText(row.counts)}</span>
                  </a>
                </li>
              {/each}
            </ul>
          {/if}
        </Panel>

        <Panel
          title="By check"
          description="Which repositories have each check open: the way to ask which of them have no job timeouts, say."
          flush
        >
          <ChecksTable
            rows={overview.checks}
            label="Checks and the repositories that have each open"
          />
        </Panel>

        <Panel
          title="What Kennel Club can see"
          description="Each source of facts, and how far it could be read for the repositories above."
        >
          {#if overview.coverage.length === 0}
            <p class="note">Nothing has been read yet.</p>
          {:else}
            <ul class="coverage">
              {#each overview.coverage as source (source.source)}
                {@const worst = worstCoverageState(source.states)}
                <li>
                  <div class="head">
                    <strong>{source.label}</strong>
                    <Badge status={kennelCoverageStatus(worst)} size="sm" />
                  </div>
                  <p>{coverageStatesText(source.states)}</p>
                  {#if source.permission && worst !== 'ok'}
                    <p class="permission">
                      Needs the App's <strong>{source.permission}</strong> permission.
                      {#if canAdmin}<a href="/installations">Open installations</a>{/if}
                    </p>
                  {/if}
                </li>
              {/each}
            </ul>
          {/if}
        </Panel>
      {/if}
    {/if}
  </LoadingBoundary>
</div>

<style>
  .content {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-5);
  }
  .summary {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .tiles {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(155px, 1fr));
    gap: var(--z-space-3);
  }
  .tile-skeleton {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    padding: var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  .floor {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
  }
  .notice {
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-accent-border);
    border-radius: var(--z-radius-md);
    background: var(--z-accent-subtle);
    color: var(--z-text);
    font-size: var(--z-text-sm);
  }
  .notice p {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
  }
  .since {
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
  }
  .explain {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    max-width: 70ch;
  }
  .explain p,
  .explain ul {
    margin: 0;
    color: var(--z-text-muted);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
  }
  .explain ul {
    padding-left: var(--z-space-5);
    list-style: disc;
  }
  .explain h3 {
    margin: var(--z-space-2) 0 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .note {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text-subtle);
  }
  .rows {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .rows li + li {
    border-top: var(--z-border-width) solid var(--z-border);
  }
  .rows a {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-5);
    color: var(--z-text);
    text-decoration: none;
  }
  .rows a:hover {
    background: var(--z-surface-hover);
  }
  .name {
    font-family: var(--z-font-mono);
    font-size: var(--z-text-sm);
    overflow-wrap: anywhere;
  }
  .counts {
    margin-left: auto;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .coverage {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
    gap: var(--z-space-4);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .coverage li {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    min-width: 0;
  }
  .coverage .head {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    flex-wrap: wrap;
  }
  .coverage p {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
  }
  .permission {
    color: var(--z-text);
  }
  @media (max-width: 768px) {
    .counts {
      margin-left: 0;
      flex-basis: 100%;
    }
  }
</style>
