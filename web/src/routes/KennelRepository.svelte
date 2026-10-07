<!--
  One repository: what Kennel Club concluded about it, why, and how far it could
  see.

  A repository is never shown as all clear on a read that was not whole. When
  something could not be read, the page says what and why in place of an empty
  list of findings, and a clean "Best in show" appears only when every check that
  is turned on ran against everything it needs.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { RotateCw, ShieldCheck, Trophy, Undo2 } from '@lucide/svelte';
  import {
    getKennelRepository,
    recheckKennelRepository,
    unwaiveKennelFinding,
  } from '$lib/api/client';
  import { events } from '$lib/api/sse';
  import type { KennelFinding, KennelRepository, KennelWaived } from '$lib/api/types';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Tabs from '$lib/components/Tabs.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Finding from '$lib/kennel/Finding.svelte';
  import RepositoryOverview from '$lib/kennel/RepositoryOverview.svelte';
  import WaiveDialog from '$lib/kennel/WaiveDialog.svelte';
  import {
    ERROR_WAIVER_SENTENCE,
    openFindingsText,
    waiverRole,
    worstSeverity,
  } from '$lib/kennel/words';
  import { formatAbsolute } from '$lib/format';
  import { router } from '$lib/router';
  import { fleet } from '$lib/state/fleet.svelte';
  import { prefs } from '$lib/state/prefs.svelte';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import { kennelCoverageStatus, kennelStatus, severityStatus } from '$lib/status';

  const id = $derived(router.params.id ?? '');

  /**
   * The tabs, by address: `/kennel/repositories/:id/:tab?`. A path segment and not
   * a query, so back and forward move between them and a tab has a link of its own.
   * The Overview is the default and has no segment; a tab for a stage that has not
   * shipped is not here, and an address that names one gets the default.
   */
  const TABS = [
    { id: 'overview', label: 'Overview' },
    { id: 'ci', label: 'CI' },
  ] as const;
  const base = $derived(`/kennel/repositories/${encodeURIComponent(id)}`);
  const requested = $derived(router.params.tab ?? '');
  const tab = $derived(TABS.find((t) => t.id === requested)?.id ?? 'overview');
  function openTab(next: string): void {
    router.navigate(next === 'overview' ? base : `${base}/${next}`);
  }
  $effect(() => {
    if (requested && !TABS.some((t) => t.id === requested))
      router.navigate(base, { replace: true });
  });

  let repo = $state<KennelRepository | null>(null);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let gone = $state(false);
  let reload = $state(0);
  let showing = '';

  $effect(() => {
    void reload;
    const current = id;
    // The page is not keyed, so a different repository is the same component
    // with a different id: what was showing must not outlive the change.
    if (untrack(() => showing) !== current) {
      showing = current;
      repo = null;
      error = null;
      gone = false;
    }
    const controller = new AbortController();
    loading = true;
    void getKennelRepository(current, controller.signal)
      .then((next) => {
        repo = next;
        error = null;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        error = cause;
      })
      .finally(() => (loading = false));
    return () => controller.abort();
  });

  $effect(() => {
    if (repo) router.setTitle(repo.name);
  });

  // Anything that changed this repository is announced in the shape this page
  // read it in, so a frame replaces what is held and is never merged into it.
  $effect(() =>
    events.subscribe('kennel.updated', (row) => {
      if (row.id === id) {
        repo = row;
        error = null;
      }
    }),
  );
  $effect(() =>
    events.subscribe('kennel.deleted', (row) => {
      if (row.id === id) gone = true;
    }),
  );
  $effect(() => events.subscribe('resync', () => (reload += 1)));
  let previous = '';
  $effect(() => {
    const next = fleet.connection;
    if (next === 'live' && previous && previous !== 'live') untrack(() => (reload += 1));
    previous = next;
  });

  /* -- recheck ---------------------------------------------------------------- */

  let rechecking = $state(false);

  async function recheck(): Promise<void> {
    if (!repo) return;
    rechecking = true;
    try {
      repo = await recheckKennelRepository(repo.id);
      toasts.success(
        'Recheck requested',
        'The reads happen when the request budget and the installation allow.',
      );
    } catch (cause) {
      // The cooldown and the "Kennel Club is off" answers carry their own
      // sentence, which says when to try again or where to turn it on.
      toasts.fromError(cause, 'That repository was not rechecked');
    } finally {
      rechecking = false;
    }
  }

  /* -- waiving and ending a waiver --------------------------------------------- */

  let waiveOpen = $state(false);
  let waiving = $state<KennelFinding | null>(null);

  function startWaive(finding: KennelFinding): void {
    waiving = finding;
    waiveOpen = true;
  }

  let endOpen = $state(false);
  let ending = $state<KennelWaived | null>(null);

  function startEnd(entry: KennelWaived): void {
    ending = entry;
    endOpen = true;
  }

  async function endWaiver(): Promise<boolean> {
    const entry = ending;
    if (!repo || !entry) return false;
    try {
      repo = await unwaiveKennelFinding(repo.id, entry.waiver.id);
      toasts.success('Waiver ended', 'The finding is open again.');
      return true;
    } catch (cause) {
      toasts.fromError(cause, 'That waiver was not ended');
      return false;
    }
  }

  /* -- what to say ------------------------------------------------------------- */

  const status = $derived(
    repo ? kennelStatus(repo.state, worstSeverity(repo.counts), prefs.quirkyStatus) : null,
  );

  function capitalise(word: string): string {
    return word ? word.charAt(0).toUpperCase() + word.slice(1) : word;
  }

  /** Whether there is a reason this is not a clean read, to be said in place of "nothing wrong". */
  const incomplete = $derived(repo ? !repo.complete || repo.state === 'partial' : false);
  const unread = $derived((repo?.coverage ?? []).filter((source) => source.state !== 'ok'));
</script>

<PageHeader
  title={repo?.name ?? 'Repository'}
  subtitle={repo ? undefined : 'What Kennel Club concluded about one repository.'}
  breadcrumb={[
    { label: 'Kennel Club', href: '/kennel' },
    { label: 'Repositories', href: '/kennel/repositories' },
  ]}
  onrefresh={() => {
    reload += 1;
  }}
>
  {#snippet meta()}
    {#if repo && status}
      <p class="meta">
        <Badge {status} size="sm" />
        {#if repo.visibility}
          <Badge tone="neutral" label={capitalise(repo.visibility)} size="sm" dot={false} />
        {/if}
        <span>
          {#if repo.evaluated_at}
            Evaluated <RelativeTime value={repo.evaluated_at} />
          {:else}
            Not evaluated yet
          {/if}
        </span>
      </p>
    {/if}
  {/snippet}
  {#if repo && session.can('operator')}
    <Button icon={RotateCw} loading={rechecking} onclick={() => void recheck()}>Recheck</Button>
  {/if}
</PageHeader>

<div class="content">
  {#if gone}
    <EmptyState
      icon={Trophy}
      title="Kennel Club no longer tracks this repository"
      description="It has not had a job served by this fleet for long enough, so Kennel Club let it go. It comes back if one runs again."
    >
      <Button href="/kennel/repositories">All repositories</Button>
    </EmptyState>
  {:else if loading && !repo}
    <Skeleton lines={8} />
  {:else if error && !repo}
    <ErrorState {error} title="That repository could not be read" onretry={() => (reload += 1)} />
  {:else if repo}
    <!-- A constant, because a snippet does not keep what the branch learned about `repo`. -->
    {@const current = repo}
    <Tabs tabs={TABS} value={tab} label="Repository sections" onchange={openTab}>
      {#if tab === 'overview'}
        <RepositoryOverview repo={current} refreshKey={reload} />
      {:else}
        {#if incomplete}
          <div class="notice" role="note">
            <p>
              <strong>This is not an all clear.</strong>
              {#if unread.length > 0 || current.skipped.length > 0}
                Some of what Kennel Club needs could not be read, so the checks that depend on it
                did not run. What it could read is below.
              {:else}
                Every check that could run did, but not against everything it needs.
              {/if}
            </p>
          </div>
        {/if}

        <section class="findings" aria-labelledby="findings-heading">
          <h2 id="findings-heading">
            Open findings
            <span class="count">{openFindingsText(current.counts)}</span>
          </h2>
          {#if current.findings.length > 0}
            {#each current.findings as finding (finding.code + '\u0000' + finding.subject)}
              <Finding {finding}>
                {#snippet actions()}
                  {#if session.can('operator')}
                    {#if session.can(waiverRole(finding.severity))}
                      <Button
                        size="sm"
                        icon={ShieldCheck}
                        ariaLabel="Waive: {finding.title}"
                        onclick={() => startWaive(finding)}
                      >
                        Waive
                      </Button>
                    {:else}
                      <!-- Said, not hidden or greyed: a sentence reads on a phone and to a screen reader. -->
                      <span class="refusal">{ERROR_WAIVER_SENTENCE}</span>
                    {/if}
                  {/if}
                {/snippet}
              </Finding>
            {/each}
          {:else if current.state === 'pending'}
            <EmptyState
              compact
              title="Not looked at yet"
              description="Kennel Club has not evaluated this repository. It will, on its next pass, or when you press Recheck."
            />
          {:else if incomplete}
            <EmptyState
              compact
              title="Nothing open, and not an all clear"
              description="Nothing was found in what could be read. The sources that could not be read are listed below."
            />
          {:else}
            <EmptyState
              compact
              icon={Trophy}
              title={kennelStatus('best_in_show', undefined, prefs.quirkyStatus).label}
              description="Every check that is turned on ran against everything it needs, and nothing is open."
            />
          {/if}
        </section>

        <Panel
          title="What Kennel Club could see"
          description="Each source of facts about this repository, and whether it could be read."
        >
          <ul class="coverage">
            {#each current.coverage as source (source.source)}
              <li>
                <div class="head">
                  <strong>{source.label}</strong>
                  <Badge status={kennelCoverageStatus(source.state)} size="sm" />
                </div>
                <p>{source.reason}</p>
                {#if source.permission && source.state !== 'ok'}
                  <p class="permission">
                    Needs the App's <strong>{source.permission}</strong> permission.
                  </p>
                {/if}
              </li>
            {/each}
          </ul>
          {#if current.skipped.length > 0}
            <h3 class="skipped-heading">Checks that did not run</h3>
            <ul class="skipped">
              {#each current.skipped as skip (skip.code)}
                <li><code>{skip.code}</code>: {skip.reason}</li>
              {/each}
            </ul>
          {/if}
          {#if current.disabled.length > 0}
            <p class="note">
              Turned off in Settings, and so not counted as a gap:
              {current.disabled.join(', ')}.
            </p>
          {/if}
        </Panel>

        {#if current.waived.length > 0 || current.lapsed.length > 0}
          <details class="waived" open={current.waived.length > 0}>
            <summary>
              Waived
              <span class="count">{current.waived.length}</span>
            </summary>
            {#each current.waived as entry (entry.waiver.id)}
              <div class="waiver">
                <p class="what">
                  <Badge status={severityStatus(entry.finding.severity)} size="sm" />
                  <strong>{entry.finding.title}</strong>
                </p>
                <p class="reason">{entry.waiver.reason}</p>
                <p class="by">
                  Waived by {entry.waiver.by}
                  <RelativeTime value={entry.waiver.at} plain />, until
                  {formatAbsolute(entry.waiver.expires_at)}.
                </p>
                <!-- Any operator may end any waiver: ending one only makes Kennel Club stricter. -->
                {#if session.can('operator')}
                  <div class="end">
                    <Button
                      size="sm"
                      icon={Undo2}
                      ariaLabel="End the waiver: {entry.finding.title}"
                      onclick={() => startEnd(entry)}
                    >
                      End waiver
                    </Button>
                  </div>
                {/if}
              </div>
            {/each}
            {#each current.lapsed as waiver (waiver.id)}
              <div class="waiver lapsed">
                <p class="what"><strong>{waiver.code}</strong> is open again</p>
                <p class="by">
                  A waiver by {waiver.by} no longer covers it: it ended, or the finding got worse.
                </p>
              </div>
            {/each}
          </details>
        {/if}

        <WaiveDialog
          bind:open={waiveOpen}
          repositoryId={current.id}
          finding={waiving}
          onwaived={(next) => (repo = next)}
        />

        <ConfirmDialog
          bind:open={endOpen}
          title="End waiver"
          name={ending?.finding.title}
          description="The finding is open again straight away."
          consequences={[
            'It counts against this repository again.',
            'To waive it again, somebody will have to give a reason.',
          ]}
          confirmLabel="End waiver"
          tone="default"
          onconfirm={endWaiver}
        />

        <p class="next">
          {#if current.next_due_at}
            Next read from GitHub <RelativeTime value={current.next_due_at} />.
          {:else}
            Due to be read now.
          {/if}
        </p>
      {/if}
    </Tabs>
  {/if}
</div>

<style>
  .content {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-5);
  }
  .meta {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .notice {
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-accent-border);
    border-radius: var(--z-radius-md);
    background: var(--z-accent-subtle);
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
  .notice p {
    margin: 0;
  }
  .findings {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
  }
  h2 {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: var(--z-space-3);
    margin: 0;
    font-size: var(--z-text-lg);
    line-height: var(--z-leading-lg);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .count {
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-normal);
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
  .head {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-2);
  }
  .coverage p,
  .note {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
  }
  .permission {
    color: var(--z-text);
  }
  .skipped-heading {
    margin: var(--z-space-4) 0 var(--z-space-2);
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-semibold);
  }
  .skipped {
    margin: 0;
    padding-left: var(--z-space-5);
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
  }
  .note {
    margin-top: var(--z-space-3);
  }
  .waived {
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  .waived summary {
    cursor: pointer;
    padding: var(--z-space-3) var(--z-space-5);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .waiver {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    padding: var(--z-space-3) var(--z-space-5);
    border-top: var(--z-border-width) solid var(--z-border);
  }
  .waiver p {
    margin: 0;
    font-size: var(--z-text-sm);
    overflow-wrap: anywhere;
  }
  .what {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-2);
  }
  .reason {
    color: var(--z-text);
  }
  .by {
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
  }
  .end {
    margin-top: var(--z-space-1);
  }
  .refusal {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .next {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
</style>
