<!--
  Step two: which repositories.

  Every repository the installation can see is listed, including the ones with
  nothing to migrate. That is deliberate: "acme/docs has no workflows" and
  "acme/infra is already on self-hosted runners" are both answers an operator
  wants, and a list that silently omitted them would look like the scan had
  missed something.

  Only the repositories that would actually change are ticked by default.
-->
<script lang="ts">
  import type { MigrationPlan, MigrationRepo } from '$lib/api/types';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { FolderGit2 } from '@lucide/svelte';

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
  }

  function rowOf(repo: MigrationRepo): Row {
    const workflows = repo.workflows ?? [];
    const jobs = workflows.reduce((n, w) => n + (w.rewrites ?? []).length, 0);
    const changed = workflows.filter((w) => (w.rewrites ?? []).length > 0).length;
    const skipped = workflows.reduce((n, w) => n + (w.skips ?? []).length, 0);

    // What this repository would get out of the migration, in one line. The
    // order matters: an unreadable repository explains itself first, and
    // "no workflows" is a different answer from "workflows, but nothing to
    // move".
    const note = repo.error
      ? repo.error
      : jobs > 0
        ? `${jobs} ${jobs === 1 ? 'job' : 'jobs'} in ${changed} ${changed === 1 ? 'file' : 'files'}`
        : workflows.length === 0
          ? 'No workflows'
          : skipped > 0
            ? `Nothing to move; ${skipped} left alone`
            : 'Nothing to move';

    return { repo: repo.repo ?? '', jobs, workflows: changed, skipped, note, migratable: jobs > 0 };
  }

  const rows = $derived((plan?.repositories ?? []).map(rowOf));
  const migratable = $derived(rows.filter((r) => r.migratable));
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
    {rows.length === 1 ? 'repository has' : 'repositories have'} jobs on a GitHub-hosted runner that a
    pool here could take. The rest are listed so you can see they were looked at.
  </p>

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
      label="Select every repository that would change"
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
    border-bottom: 1px solid var(--z-border-subtle, var(--z-border));
  }
  .repos li.inert {
    opacity: 0.6;
  }
</style>
