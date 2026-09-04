<!--
  Moving a repository's CI onto this fleet.

  Five steps, and the fourth one is the point: nothing is written until an
  operator has read the exact diff that is about to appear in somebody else's
  repository. The first three exist to make that diff correct, and the last
  reports what actually happened, repository by repository, with a link to
  every pull request.

  The wizard holds one plan at a time and re-fetches it whenever an answer that
  changes it changes. That is a round trip per edit, but the alternative --
  rewriting workflows in the browser -- would mean two implementations of the
  rewriting rules that could disagree, and the one that matters is the one on
  the server, because it is the one that commits.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { ExternalLink, GitPullRequest, RefreshCw } from '@lucide/svelte';
  import {
    ApiError,
    listInstallations,
    openMigrationPullRequests,
    planMigration,
  } from '$lib/api/client';
  import type { Installation, MigrationOutcome, MigrationPlan } from '$lib/api/types';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Wizard from '$lib/components/Wizard.svelte';
  import type { WizardStep } from '$lib/components/Wizard.svelte';
  import StepTarget from './StepTarget.svelte';
  import StepRepositories from './StepRepositories.svelte';
  import StepMapping from './StepMapping.svelte';
  import StepReview from './StepReview.svelte';
  import StepOutcome from './StepOutcome.svelte';

  interface Props {
    /** Preselected from the query string, so a link can point straight at one. */
    installationId?: string;
    oncancel?: () => void;
  }

  let { installationId = '', oncancel }: Props = $props();

  /**
   * Four steps, not five: the outcome is not a step.
   *
   * A results screen with a Back button invites an operator to walk back into a
   * review of work that is already open, and it would make the wizard's Next
   * mean "open the pull requests" on one step and "go forward" on the others.
   * Review is the last step, its button says exactly what it does, and the
   * results replace the wizard once there are any.
   */
  const STEPS: readonly WizardStep[] = [
    { id: 'target', title: 'Installation', description: 'Whose repositories.' },
    { id: 'repos', title: 'Repositories', description: 'Which ones to migrate.' },
    { id: 'mapping', title: 'Labels', description: 'What each GitHub label becomes.' },
    { id: 'review', title: 'Review', description: 'The exact change, before it is opened.' },
  ];

  let step = $state(0);
  let busy = $state(false);
  let failure = $state('');

  /* -- step one ------------------------------------------------------------ */

  let installations = $state<Installation[]>([]);
  let loadingInstallations = $state(true);
  let installationsError = $state('');
  // Seeded from the query string once, then owned by the wizard: navigating
  // back to step one must not snap the choice back to whatever the link said.
  let selected = $state(untrack(() => installationId));

  /* -- the plan ------------------------------------------------------------ */

  let plan = $state<MigrationPlan | null>(null);
  let chosen = $state<string[]>([]);
  /** Hosted label -> the runs-on value replacing it. "" means "leave it alone". */
  let mapping = $state<Record<string, string>>({});
  /** True once the operator has edited the mapping, so a re-scan stops overwriting it. */
  let mappingEdited = false;

  /* -- step five ----------------------------------------------------------- */

  let outcome = $state<MigrationOutcome | null>(null);

  $effect(() => {
    void (async () => {
      try {
        const result = await listInstallations();
        installations = result.items ?? [];
        if (!selected && installations.length === 1) selected = installations[0]?.id ?? '';
      } catch (cause) {
        installationsError =
          cause instanceof ApiError ? cause.message : 'The installations could not be listed.';
      } finally {
        loadingInstallations = false;
      }
    })();
  });

  const target = $derived(installations.find((i) => i.id === selected));

  const changedRepos = $derived(
    (plan?.repositories ?? []).filter((r) =>
      (r.workflows ?? []).some((w) => (w.rewrites ?? []).length > 0),
    ),
  );
  const selectedChanged = $derived(changedRepos.filter((r) => chosen.includes(r.repo ?? '')));
  const blockedByPermissions = $derived((plan?.missing_permissions ?? []).length > 0);

  const canAdvance = $derived.by(() => {
    if (busy) return false;
    switch (step) {
      case 0:
        return selected !== '';
      case 1:
        return chosen.length > 0;
      case 2:
        return Object.values(mapping).some((v) => v !== '');
      case 3:
        return selectedChanged.length > 0 && !blockedByPermissions;
      default:
        return true;
    }
  });

  function report(cause: unknown, fallback: string): void {
    failure = cause instanceof ApiError ? cause.message : fallback;
  }

  /**
   * Ask the server what it would change.
   *
   * `repos` is left empty on the first scan so the operator sees the whole
   * organisation and chooses from it; every scan after that is narrowed to what
   * they chose, which is both faster and a smaller slice of the installation's
   * GitHub quota.
   */
  async function scan(repos: string[]): Promise<boolean> {
    busy = true;
    failure = '';
    try {
      const result = await planMigration({
        installation_id: selected,
        ...(repos.length > 0 ? { repos } : {}),
        ...(mappingEdited ? { mapping } : {}),
      });
      plan = result;
      if (!mappingEdited) {
        // The server's proposal, plus an explicit blank for every label it
        // could not place, so the mapping step lists all of them.
        const next: Record<string, string> = {};
        for (const label of result.hosted_labels ?? []) next[label] = '';
        Object.assign(next, result.mapping ?? {});
        mapping = next;
      }
      if (repos.length === 0) {
        // Default to every repository that would actually change. Repositories
        // with nothing to do stay visible but unticked.
        chosen = (result.repositories ?? [])
          .filter((r) => (r.workflows ?? []).some((w) => (w.rewrites ?? []).length > 0))
          .map((r) => r.repo ?? '')
          .filter(Boolean);
      }
      return true;
    } catch (cause) {
      report(cause, 'The repositories could not be read.');
      return false;
    } finally {
      busy = false;
    }
  }

  async function next(): Promise<void> {
    switch (step) {
      case 0: {
        if (!(await scan([]))) step -= 1;
        return;
      }
      case 2: {
        // The mapping changed, so every diff downstream is stale.
        await scan(chosen);
        return;
      }
      default:
        return;
    }
  }

  function onMappingChange(label: string, to: string): void {
    mappingEdited = true;
    mapping = { ...mapping, [label]: to };
  }

  async function rescan(): Promise<void> {
    await scan(chosen);
  }

  /** Back to the start, keeping the installation and nothing else. */
  function restart(): void {
    outcome = null;
    plan = null;
    chosen = [];
    mapping = {};
    mappingEdited = false;
    failure = '';
    step = 0;
  }

  /** The last step. This is the only thing here that writes anything. */
  async function open(): Promise<void> {
    busy = true;
    failure = '';
    try {
      const chosenMapping: Record<string, string> = {};
      for (const [label, to] of Object.entries(mapping)) {
        if (to !== '') chosenMapping[label] = to;
      }
      const result = await openMigrationPullRequests({
        installation_id: selected,
        repos: selectedChanged.map((r) => r.repo ?? '').filter(Boolean),
        mapping: chosenMapping,
      });
      // Assigned only once the call has returned, because assigning it is what
      // swaps the wizard for the results.
      outcome = result;
      const opened = result.opened ?? 0;
      toasts.push({
        tone: opened > 0 ? 'success' : 'warning',
        title:
          opened > 0
            ? `Opened ${opened} pull ${opened === 1 ? 'request' : 'requests'}`
            : 'Nothing was opened',
        message:
          opened > 0
            ? 'Each one changes only the runs-on lines you reviewed.'
            : 'Every repository was skipped or failed. The results say why.',
      });
    } catch (cause) {
      report(cause, 'The pull requests could not be opened.');
    } finally {
      busy = false;
    }
  }
</script>

{#if loadingInstallations}
  <div class="loading">
    <Skeleton width="220px" height="1.5rem" />
    <Skeleton width="100%" height="12rem" />
  </div>
{:else if installationsError}
  <ErrorState title="GitHub is not reachable" description={installationsError} />
{:else if installations.length === 0}
  <ErrorState
    title="No installation to migrate"
    description="A migration reads and writes repositories through a GitHub App installation, and this controller has none. Connect one on the Installations page first."
  />
{:else if outcome}
  <StepOutcome {outcome} />
  <p class="summary">
    <Button variant="secondary" onclick={restart}>Migrate more repositories</Button>
    {#if target?.web_url}
      <a href={target.web_url} target="_blank" rel="noopener noreferrer">
        Open {target.target} on GitHub
        <ExternalLink size={12} aria-hidden="true" />
      </a>
    {/if}
  </p>
{:else}
  <Wizard
    steps={STEPS}
    bind:current={step}
    {canAdvance}
    {busy}
    finishLabel="Open the pull requests"
    onnext={next}
    onfinish={open}
    {oncancel}
  >
    {#snippet children(_s: WizardStep, index: number)}
      {#if failure}
        <p class="failure" role="alert">{failure}</p>
      {/if}

      {#if index === 0}
        <StepTarget {installations} bind:selected />
      {:else if index === 1}
        <StepRepositories {plan} bind:chosen />
      {:else if index === 2}
        <StepMapping {plan} {mapping} onchange={onMappingChange} />
      {:else}
        <StepReview
          {plan}
          repos={selectedChanged}
          {busy}
          target={target?.target ?? ''}
          onrescan={rescan}
        />
      {/if}
    {/snippet}
  </Wizard>

  {#if step === 3 && plan}
    <p class="summary">
      <GitPullRequest size={14} aria-hidden="true" />
      {selectedChanged.length}
      {selectedChanged.length === 1 ? 'pull request' : 'pull requests'}, one per repository, each on
      its own branch.
      <Button size="sm" variant="ghost" icon={RefreshCw} onclick={rescan} disabled={busy}>
        Re-read from GitHub
      </Button>
    </p>
  {/if}
{/if}

<style>
  .loading {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
  }
  .failure {
    margin: 0 0 var(--z-space-4);
    padding: var(--z-space-3) var(--z-space-4);
    border: 1px solid var(--z-failed-border);
    border-radius: var(--z-radius-md);
    background: var(--z-failed-subtle);
    color: var(--z-failed);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
  }
  .summary {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: var(--z-space-4) 0 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .summary a {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    color: var(--z-accent);
  }
</style>
