<!--
  Step two: which repositories, and which of their workflow files.

  Every repository the installation can see is listed, including the ones with
  nothing to migrate. That is deliberate: "acme/docs has no workflows",
  "acme/infra is already on Zoomies" and "acme/legacy is archived" are all
  answers an operator wants, and a list that silently omitted them would look
  like the scan had missed something. They are hidden by default all the same,
  with a count and a switch: in an organisation they are most of the list, and
  this step is for the ones that can move.

  The repositories that can move often have several workflow files, of which a
  release or a nightly is exactly the one to leave where it is, so a repository
  with more than one can be opened up and picked through file by file.

  An organisation is read a page at a time, because reading every workflow file
  in a thousand repositories is the most expensive thing Zoomies asks GitHub
  for. A page is appended to this list rather than replacing it, so a choice
  made on the way survives to the review.

  What can be ticked is "this repository asks for a rented runner", not "this
  repository would change under the mapping the server has guessed so far". The
  two are different, and the difference used to be a dead end: the labels are
  mapped on the *next* step, so a fleet whose pools the server could not match
  to any label offered an operator a page of checkboxes that all refused to
  tick, with nothing on screen saying why.

  Whatever cannot be ticked says so in its own words, and the two reasons that
  are the operator's to fix -- a repository the App may not read, and a scan
  that turned up nothing at all -- are said once at the top, where they cannot
  be mistaken for a broken control.
-->
<script lang="ts">
  import type { MigrationPlan, MigrationRepo, MigrationWorkflow } from '$lib/api/types';
  import Button from '$lib/components/Button.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import Input from '$lib/components/Input.svelte';
  import {
    filesIn,
    isMigratable,
    jobsIn,
    MAX_SELECTED_REPOSITORIES,
    noteFor,
    skipsIn,
  } from './eligibility';
  import {
    AlertTriangle,
    ChevronDown,
    ChevronRight,
    ExternalLink,
    FolderGit2,
  } from '@lucide/svelte';

  interface Props {
    plan: MigrationPlan | null;
    /** Repository -> the workflow paths chosen in it. A repository with none is unchosen. */
    selection?: Record<string, string[]>;
    /** How many repositories the installation can see, of which this list is a page. */
    total?: number;
    /** Set when there is another page to read. */
    hasMore?: boolean;
    busy?: boolean;
    scanningAll?: boolean;
    onpause?: () => void;
    onloadmore?: () => void;
  }

  let {
    plan,
    selection = $bindable({}),
    total = 0,
    hasMore = false,
    busy = false,
    scanningAll = false,
    onpause,
    onloadmore,
  }: Props = $props();

  /** One workflow file that asks for a runner somebody else operates. */
  interface File {
    path: string;
    jobs: number;
    skips: number;
  }

  interface Row {
    repo: string;
    jobs: number;
    workflows: number;
    skipped: number;
    note: string;
    /** The files that could move, which are what a repository is chosen by. */
    files: File[];
    migratable: boolean;
    unreadable: boolean;
  }

  function rowOf(repo: MigrationRepo): Row {
    const workflows = repo.workflows ?? [];

    // What this repository would get out of the migration, or why it is not on
    // offer. The wizard reads the same two functions, so a row that is greyed
    // out here cannot end up in the call that opens the pull requests.
    const note = noteFor(repo);

    // On the labels a file asks for, not on what the provisional mapping
    // already rewrites: the mapping is made on the next step, so a file with
    // nothing to rewrite yet is still one the operator came here to choose.
    const files = workflows
      .filter((w: MigrationWorkflow) => (w.hosted_labels ?? []).length > 0)
      .map((w: MigrationWorkflow) => ({
        path: w.path ?? '',
        jobs: (w.rewrites ?? []).length,
        skips: (w.skips ?? []).length,
      }));

    return {
      repo: repo.repo ?? '',
      jobs: jobsIn(repo),
      workflows: filesIn(repo),
      skipped: skipsIn(repo),
      note,
      files,
      migratable: isMigratable(repo),
      unreadable: Boolean(repo.error),
    };
  }

  /**
   * Hiding what cannot move is the default.
   *
   * The rest are still worth being able to see -- a repository the App cannot
   * read, and one whose jobs already point somewhere deliberate, are both
   * answers -- but they are not what this step is for, and in an organisation
   * they are most of it.
   */
  let onlyMigratable = $state(true);
  let query = $state('');

  /** Which repositories have their file list open. */
  let expanded = $state<Record<string, boolean>>({});

  const rows = $derived((plan?.repositories ?? []).map(rowOf));
  const migratable = $derived(rows.filter((r) => r.migratable));
  const unreadable = $derived(rows.filter((r) => r.unreadable));
  const missingPermissions = $derived(plan?.missing_permissions ?? []);
  // Either half is enough to say something is wrong: the hint arrives with the
  // list, but a server that named only one of the two should still be heard.
  const permissionProblem = $derived(
    missingPermissions.length > 0 || Boolean(plan?.permission_hint),
  );
  const hidden = $derived(onlyMigratable ? rows.length - migratable.length : 0);
  const visible = $derived(
    (onlyMigratable ? migratable : rows).filter((r) =>
      r.repo.toLowerCase().includes(query.trim().toLowerCase()),
    ),
  );
  const remaining = $derived(Math.max(0, total - rows.length));
  const scanTitle = $derived(
    busy
      ? 'Checking repositories'
      : hasMore
        ? 'Repository scan paused'
        : 'Repository scan complete',
  );

  const chosenIn = (repo: string): string[] => selection[repo] ?? [];
  const chosenCount = $derived(migratable.filter((r) => chosenIn(r.repo).length > 0).length);
  const allChosen = $derived(
    migratable.length > 0 &&
      migratable.every((r) => r.files.every((f) => chosenIn(r.repo).includes(f.path))),
  );
  const someChosen = $derived(migratable.some((r) => chosenIn(r.repo).length > 0));

  /** Replaces a repository's chosen files, dropping the key when none are left. */
  function set(repo: string, paths: string[]): void {
    const next = { ...selection };
    if (paths.length === 0) delete next[repo];
    else if (repo in next || Object.keys(next).length < MAX_SELECTED_REPOSITORIES)
      next[repo] = paths;
    selection = next;
  }

  function toggle(row: Row, on: boolean): void {
    set(row.repo, on ? row.files.map((f) => f.path) : []);
  }

  function toggleFile(row: Row, path: string, on: boolean): void {
    const current = chosenIn(row.repo);
    set(row.repo, on ? [...current, path] : current.filter((p) => p !== path));
  }

  function toggleAll(on: boolean): void {
    if (!on) {
      selection = {};
      return;
    }
    const next: Record<string, string[]> = { ...selection };
    for (const row of migratable) {
      if (row.repo in next || Object.keys(next).length < MAX_SELECTED_REPOSITORIES)
        next[row.repo] = row.files.map((f) => f.path);
    }
    selection = next;
  }

  function expand(repo: string): void {
    expanded = { ...expanded, [repo]: !expanded[repo] };
  }
</script>

<section class="scan" aria-label="Repository scan">
  <div class="scan-heading">
    <div role="status" aria-live="polite">
      <h3>{scanTitle}</h3>
      <p class="scan-count">
        {rows.length} of {Math.max(total, rows.length)} repositories checked{hasMore
          ? ' so far'
          : ''}
      </p>
    </div>
    {#if scanningAll}
      <Button size="sm" variant="secondary" onclick={onpause}>Pause scan</Button>
    {:else if busy}
      <span class="muted">Finishing this batch…</span>
    {:else if hasMore}
      <Button size="sm" variant="primary" onclick={onloadmore}>Scan remaining repositories</Button>
    {/if}
  </div>
  <progress
    aria-label="Repositories checked"
    value={rows.length}
    max={Math.max(total, rows.length, 1)}
  ></progress>
  <div class="scan-totals">
    <span><strong>{migratable.length}</strong> with jobs to migrate{hasMore ? ' so far' : ''}</span>
    <span><strong>{chosenCount}</strong> selected</span>
    {#if hasMore}<span><strong>{remaining || 'More'}</strong> still to check</span>{/if}
  </div>
  {#if hasMore}
    <p class="scan-note">
      {scanningAll
        ? 'More repositories are being checked automatically. Your selections stay in place.'
        : busy
          ? 'The current batch will finish before the scan pauses.'
          : 'The scan is incomplete. Resume it to find more repositories, or continue with your selection. Unchecked repositories will not be included.'}
    </p>
  {/if}
</section>

<p class="lede">
  Choose the repositories to move, then open any file list to narrow the change. Nothing is written
  until you review and open the pull requests.
</p>

{#if unreadable.length > 0}
  <div class="problem" role="alert">
    <p class="problem-title">
      <AlertTriangle size={15} aria-hidden="true" />
      {unreadable.length}
      {unreadable.length === 1 ? 'repository could' : 'repositories could'} not be read
    </p>
    <p>
      Their workflows were never looked at, so they cannot be migrated from here whatever they
      contain. The first one says: <span class="mono">{unreadable[0]?.note}</span>
    </p>
  </div>
{/if}

<!--
    Its own block, and not a paragraph inside the one above, because the two
    are not the same problem and do not always arrive together. This is what
    the App may do, which the server establishes by asking GitHub rather than
    by inferring it from what failed. Tucked inside the unreadable block it was
    invisible in the case that needs it most: an App that was never granted
    Contents gets a 404 for every repository, which reads as "no workflows", so
    nothing is unreadable, nothing can be migrated, and the one sentence that
    explains why was hidden behind a count of zero.
  -->
{#if permissionProblem}
  <div class="problem" role="alert">
    <p class="problem-title">
      <AlertTriangle size={15} aria-hidden="true" />
      The App is missing permissions this wizard needs
    </p>
    {#if missingPermissions.length > 0}
      <ul>
        {#each missingPermissions as item (item)}
          <li>{item}</li>
        {/each}
      </ul>
    {/if}
    {#if plan?.permission_hint}<p>{plan.permission_hint}.</p>{/if}
    <p>
      Until they are granted, a repository the App cannot read is indistinguishable from one with no
      workflows in it, so what is listed below may be an incomplete picture of what could move.
    </p>
    {#if plan?.settings_url}
      <p>
        <a href={plan.settings_url} target="_blank" rel="noopener noreferrer">
          Review the App's permissions on GitHub
          <ExternalLink size={12} aria-hidden="true" />
        </a>
      </p>
    {/if}
  </div>
{/if}

{#if migratable.length === 0}
  <EmptyState
    icon={FolderGit2}
    compact
    title={hasMore || busy
      ? 'No matches in the repositories checked so far'
      : permissionProblem || unreadable.length > 0
        ? 'The scan needs attention'
        : rows.length === 0
          ? 'No repositories available'
          : 'No repositories to migrate'}
    description={hasMore || busy
      ? 'This is a partial result. Repositories that can migrate may be in the batches still to check.'
      : permissionProblem || unreadable.length > 0
        ? 'Resolve the access problems above before treating this as a complete result.'
        : rows.length === 0
          ? 'Check that the App has access to the repositories you expect.'
          : 'All accessible repositories have been checked. Show the other repositories below to see why they have no jobs to move.'}
  />
{/if}

<p class="batch-note">
  Choose up to {MAX_SELECTED_REPOSITORIES} repositories per migration. {chosenCount >=
  MAX_SELECTED_REPOSITORIES
    ? 'This batch is full. Deselect a repository to choose another; migrate the rest in a later batch.'
    : 'Repositories found later stay available in this list.'}
</p>

<div class="search">
  <Input
    type="search"
    bind:value={query}
    ariaLabel="Search checked repositories"
    placeholder="Search checked repositories…"
  />
  <span class="muted">{visible.length} shown{hasMore ? ' · checked repositories only' : ''}</span>
</div>

<div class="controls">
  <Checkbox
    checked={allChosen}
    indeterminate={!allChosen && someChosen}
    label={migratable.length > MAX_SELECTED_REPOSITORIES
      ? 'Select a batch of up to 25 repositories'
      : hasMore
        ? 'Select every checked file that could move'
        : 'Select every file that could move'}
    onchange={toggleAll}
    disabled={migratable.length === 0}
  />
  <Switch
    checked={onlyMigratable}
    label="Only repositories with something to move"
    onchange={(on) => (onlyMigratable = on)}
  />
</div>

{#if query.trim() && visible.length === 0 && rows.length > 0}
  <EmptyState
    compact
    title="No checked repositories match your search"
    description={hasMore
      ? 'Clear the search or finish the scan to check the remaining repositories.'
      : 'Try another name, or show repositories with no jobs to move.'}
  />
{/if}
<ul class="repos" aria-label="Checked repositories">
  {#each visible as row (row.repo)}
    {@const chosen = chosenIn(row.repo)}
    <li class:inert={!row.migratable}>
      <div class="repo">
        <Checkbox
          checked={chosen.length > 0 && chosen.length === row.files.length}
          indeterminate={chosen.length > 0 && chosen.length < row.files.length}
          disabled={!row.migratable ||
            (chosen.length === 0 && chosenCount >= MAX_SELECTED_REPOSITORIES)}
          label={row.repo}
          description={chosen.length > 0 && chosen.length < row.files.length
            ? `${row.note}, ${chosen.length} of ${row.files.length} files chosen`
            : row.note}
          onchange={(on) => toggle(row, on)}
        />
        {#if row.files.length > 1}
          <button
            type="button"
            class="disclose"
            aria-expanded={expanded[row.repo] ?? false}
            aria-label="{row.files.length} files in {row.repo}"
            onclick={() => expand(row.repo)}
          >
            {#if expanded[row.repo]}
              <ChevronDown size={13} aria-hidden="true" />
            {:else}
              <ChevronRight size={13} aria-hidden="true" />
            {/if}
            {row.files.length} files
          </button>
        {/if}
      </div>

      {#if row.files.length > 1 && expanded[row.repo]}
        <ul class="files">
          {#each row.files as file (file.path)}
            <li>
              <Checkbox
                checked={chosen.includes(file.path)}
                disabled={!row.migratable ||
                  (chosen.length === 0 && chosenCount >= MAX_SELECTED_REPOSITORIES)}
                label={file.path}
                description="{file.jobs} {file.jobs === 1 ? 'job' : 'jobs'} to move{file.skips > 0
                  ? `, ${file.skips} left alone`
                  : ''}"
                onchange={(on) => toggleFile(row, file.path, on)}
              />
            </li>
          {/each}
        </ul>
      {/if}
    </li>
  {/each}
</ul>

<p class="footer">
  <span>
    {chosenCount}
    {chosenCount === 1 ? 'repository' : 'repositories'} chosen{hidden > 0
      ? `. ${hidden} that cannot move ${hidden === 1 ? 'is' : 'are'} hidden`
      : ''}{hasMore && total > rows.length ? `. ${total - rows.length} not read yet` : ''}.
  </span>
  <Button size="sm" variant="ghost" disabled={chosenCount === 0} onclick={() => (selection = {})}
    >Clear selection</Button
  >
</p>

<style>
  .scan {
    padding: var(--z-space-4);
    margin-bottom: var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
  }
  .scan-heading,
  .scan-totals,
  .search {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: var(--z-space-3);
  }
  .scan h3 {
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
  }
  .scan-count,
  .scan-note {
    margin: var(--z-space-2) 0 0;
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }
  .scan-totals {
    justify-content: flex-start;
    gap: var(--z-space-4);
    font-size: var(--z-text-xs);
  }
  .scan-totals strong {
    color: var(--z-text);
  }
  progress {
    display: block;
    width: 100%;
    height: var(--z-space-2);
    margin: var(--z-space-3) 0;
    border: 0;
    border-radius: var(--z-radius-full);
    overflow: hidden;
    accent-color: var(--z-accent);
    background: var(--z-border);
  }
  progress::-webkit-progress-bar {
    background: var(--z-border);
  }
  progress::-webkit-progress-value {
    background: var(--z-accent);
  }
  progress::-moz-progress-bar {
    background: var(--z-accent);
  }
  .batch-note {
    margin: 0 0 var(--z-space-3);
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }
  .search {
    margin-bottom: var(--z-space-3);
  }
  .muted {
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
  }

  .lede {
    margin: 0 0 var(--z-space-3);
    max-width: 70ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .problem {
    margin: 0 0 var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-pending-border);
    border-radius: var(--z-radius-md);
    background: var(--z-pending-subtle);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text);
  }
  .problem p {
    margin: 0 0 var(--z-space-2);
    max-width: 78ch;
  }
  .problem p:last-child {
    margin-bottom: 0;
  }
  .problem-title {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    font-weight: var(--z-weight-semibold);
    color: var(--z-pending);
  }
  .problem ul {
    margin: 0 0 var(--z-space-2);
    padding-left: var(--z-space-5);
  }
  .problem a {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    color: var(--z-accent);
    font-weight: var(--z-weight-medium);
  }
  .mono {
    font-family: var(--z-font-mono);
    overflow-wrap: anywhere;
  }
  .controls {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-4);
    flex-wrap: wrap;
    padding-bottom: var(--z-space-2);
    border-bottom: var(--z-border-width) solid var(--z-border);
  }
  .repo {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--z-space-3);
  }
  .disclose {
    flex: none;
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    padding: 0;
    border: 0;
    background: none;
    cursor: pointer;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .disclose:hover {
    color: var(--z-accent);
  }
  .files {
    margin: var(--z-space-2) 0 0 var(--z-space-6);
    padding: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
  }
  .footer {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-3);
    flex-wrap: wrap;
    margin: var(--z-space-3) 0 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .repos {
    margin: 0;
    padding: 0;
    list-style: none;
    max-height: 26rem;
    overflow-y: auto;
  }
  .repos > li {
    padding: var(--z-space-2) 0;
    border-bottom: var(--z-border-width) solid var(--z-border);
  }
  .repos > li.inert {
    opacity: 0.6;
  }
</style>
