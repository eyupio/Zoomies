<!--
  Step two: which repositories.

  Every repository the installation can see is listed, including the ones with
  nothing to migrate. That is deliberate: "acme/docs has no workflows" and
  "acme/infra is already on self-hosted runners" are both answers an operator
  wants, and a list that silently omitted them would look like the scan had
  missed something.

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
  import type { MigrationPlan, MigrationRepo } from '$lib/api/types';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { AlertTriangle, ExternalLink, FolderGit2 } from '@lucide/svelte';

  interface Props {
    plan: MigrationPlan | null;
    chosen?: string[];
  }

  let { plan, chosen = $bindable([]) }: Props = $props();

  interface Row {
    repo: string;
    jobs: number;
    workflows: number;
    skipped: number;
    note: string;
    migratable: boolean;
    unreadable: boolean;
  }

  function rowOf(repo: MigrationRepo): Row {
    const workflows = repo.workflows ?? [];
    const jobs = workflows.reduce((n, w) => n + (w.rewrites ?? []).length, 0);
    const changed = workflows.filter((w) => (w.rewrites ?? []).length > 0).length;
    const skipped = workflows.reduce((n, w) => n + (w.skips ?? []).length, 0);
    const labels = repo.hosted_labels ?? [];

    // What this repository would get out of the migration, in one line. The
    // order matters: an unreadable repository explains itself first, then what
    // would change, then what was found but not yet mapped -- and "no
    // workflows" is a different answer from "workflows, but nothing to move".
    const note = repo.error
      ? repo.error
      : jobs > 0
        ? `${jobs} ${jobs === 1 ? 'job' : 'jobs'} in ${changed} ${changed === 1 ? 'file' : 'files'}`
        : labels.length > 0
          ? `Runs on ${labels.join(', ')}; choose what those become on the next step`
          : workflows.length === 0
            ? 'No workflows'
            : skipped > 0
              ? `Nothing to move; ${skipped} left alone`
              : 'Nothing to move';

    return {
      repo: repo.repo ?? '',
      jobs,
      workflows: changed,
      skipped,
      note,
      migratable: !repo.error && labels.length > 0,
      unreadable: Boolean(repo.error),
    };
  }

  const rows = $derived((plan?.repositories ?? []).map(rowOf));
  const migratable = $derived(rows.filter((r) => r.migratable));
  const unreadable = $derived(rows.filter((r) => r.unreadable));
  const missingPermissions = $derived(plan?.missing_permissions ?? []);
  const allChosen = $derived(
    migratable.length > 0 && migratable.every((r) => chosen.includes(r.repo)),
  );
  const someChosen = $derived(migratable.some((r) => chosen.includes(r.repo)));

  function toggle(repo: string, on: boolean): void {
    chosen = on ? [...chosen, repo] : chosen.filter((r) => r !== repo);
  }

  function toggleAll(on: boolean): void {
    chosen = on ? migratable.map((r) => r.repo) : [];
  }
</script>

{#if rows.length === 0}
  <EmptyState
    icon={FolderGit2}
    title="No repositories"
    description="This installation can see no repositories. Check the App is installed on the account, and that it was given access to the repositories you expect."
  />
{:else}
  <p class="lede">
    {migratable.length} of {rows.length}
    {rows.length === 1 ? 'repository has' : 'repositories have'} jobs on a rented runner — GitHub's own,
    or a vendor's — that a pool here could take. The rest are listed so you can see they were looked at.
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
      {#if missingPermissions.length > 0}
        <ul>
          {#each missingPermissions as item (item)}
            <li>{item}</li>
          {/each}
        </ul>
      {/if}
      {#if plan?.permission_hint}<p>{plan.permission_hint}.</p>{/if}
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
    <div class="problem" role="status">
      <p class="problem-title">
        <AlertTriangle size={15} aria-hidden="true" />
        There is nothing here to migrate
      </p>
      <p>
        Nothing can be ticked because no repository on this page asks for a runner somebody else
        operates. Each row says which of the three reasons applies to it: the App cannot read it, it
        has no workflows, or its jobs already point somewhere deliberate — a self-hosted fleet, or a
        label this controller does not recognise as rented.
      </p>
    </div>
  {/if}

  {#if plan?.truncated}
    <p class="note">
      This is the first page of the organisation. Migrate these, then run the wizard again for the
      rest — a batch of pull requests nobody can review is not progress.
    </p>
  {/if}

  <div class="head">
    <Checkbox
      checked={allChosen}
      indeterminate={!allChosen && someChosen}
      label="Select every repository that could move"
      onchange={toggleAll}
      disabled={migratable.length === 0}
    />
  </div>

  <ul class="repos">
    {#each rows as row (row.repo)}
      <li class:inert={!row.migratable}>
        <Checkbox
          checked={chosen.includes(row.repo)}
          disabled={!row.migratable}
          label={row.repo}
          description={row.note}
          onchange={(on) => toggle(row.repo, on)}
        />
      </li>
    {/each}
  </ul>
{/if}

<style>
  .lede {
    margin: 0 0 var(--z-space-3);
    max-width: 70ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .note {
    margin: 0 0 var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-4);
    border: 1px solid var(--z-pending-border);
    border-radius: var(--z-radius-md);
    background: var(--z-pending-subtle);
    color: var(--z-pending);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }
  .problem {
    margin: 0 0 var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-4);
    border: 1px solid var(--z-pending-border);
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
  .head {
    padding-bottom: var(--z-space-2);
    border-bottom: 1px solid var(--z-border);
  }
  .repos {
    margin: 0;
    padding: 0;
    list-style: none;
    max-height: 26rem;
    overflow-y: auto;
  }
  .repos li {
    padding: var(--z-space-2) 0;
    border-bottom: 1px solid var(--z-border);
  }
  .repos li.inert {
    opacity: 0.6;
  }
</style>
