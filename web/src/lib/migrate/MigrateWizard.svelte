<!--
  Moving a repository's CI onto this fleet.

  Five steps, and the last one is the point: nothing is written until an
  operator has read the exact diff that is about to appear in somebody else's
  repository. The first four exist to make that diff correct -- one answer per
  label for the whole organisation, then the jobs that need a different one --
  and the results screen reports what actually happened, repository by
  repository, with a link to every pull request.

  The wizard holds one plan at a time and re-fetches it whenever an answer that
  changes it changes. That is a round trip per edit, but the alternative --
  rewriting workflows in the browser -- would mean two implementations of the
  rewriting rules that could disagree, and the one that matters is the one on
  the server, because it is the one that commits.
-->
<script lang="ts">
  import { onDestroy, untrack } from 'svelte';
  import { ExternalLink, GitPullRequest, Plug, RefreshCw } from '@lucide/svelte';
  import {
    ApiError,
    listInstallations,
    openMigrationPullRequests,
    planMigration,
  } from '$lib/api/client';
  import type {
    Installation,
    MigrationOutcome,
    MigrationOverride,
    MigrationPlan,
    MigrationRepo,
  } from '$lib/api/types';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Wizard from '$lib/components/Wizard.svelte';
  import type { WizardStep } from '$lib/components/Wizard.svelte';
  import { isMigratable, MAX_SELECTED_REPOSITORIES } from './eligibility';
  import StepTarget from './StepTarget.svelte';
  import StepRepositories from './StepRepositories.svelte';
  import StepMapping from './StepMapping.svelte';
  import StepOverrides from './StepOverrides.svelte';
  import StepReview from './StepReview.svelte';
  import StepOutcome from './StepOutcome.svelte';

  interface Props {
    /** Preselected from the query string, so a link can point straight at one. */
    installationId?: string;
    oncancel?: () => void;
  }

  let { installationId = '', oncancel }: Props = $props();

  /**
   * Five steps, not six: the outcome is not a step.
   *
   * A results screen with a Back button invites an operator to walk back into a
   * review of work that is already open, and it would make the wizard's Next
   * mean "open the pull requests" on one step and "go forward" on the others.
   * Review is the last step, its button says exactly what it does, and the
   * results replace the wizard once there are any.
   *
   * Labels and Exceptions are two steps rather than one screen because they are
   * two different questions. Labels is the one nearly every fleet answers and
   * nobody should have to scroll past; Exceptions is a list of jobs that is as
   * long as the organisation, and burying the first inside the second would
   * make the common case the hard one.
   */
  const STEPS: readonly WizardStep[] = [
    { id: 'target', title: 'Installation', description: 'Which installation to read through.' },
    { id: 'repos', title: 'Repositories', description: 'Which ones to migrate.' },
    { id: 'mapping', title: 'Labels', description: 'What each GitHub label becomes.' },
    { id: 'overrides', title: 'Exceptions', description: 'Any job that needs a different pool.' },
    { id: 'review', title: 'Review', description: 'The exact change, before it is opened.' },
  ];

  let step = $state(0);
  let busy = $state(false);
  let failure = $state('');

  /* -- step one ------------------------------------------------------------ */

  let installations = $state<Installation[]>([]);
  let loadingInstallations = $state(true);
  let installationsError = $state('');
  /** Bumped by the error state's retry, which re-runs the fetch below. */
  let installationsAttempt = $state(0);
  // Seeded from the query string once, then owned by the wizard: navigating
  // back to step one must not snap the choice back to whatever the link said.
  let selected = $state(untrack(() => installationId));

  /* -- the plan ------------------------------------------------------------ */

  let plan = $state<MigrationPlan | null>(null);
  /**
   * Repository -> the workflow paths chosen in it.
   *
   * A repository is chosen when it has at least one path here, which is what
   * lets a repository with a dozen workflow files send only the two that should
   * move. The apply call carries the same shape, so what is committed is what
   * was ticked rather than "everything in that repository that happens to
   * match" -- including a workflow file somebody added since the scan.
   */
  let selection = $state<Record<string, string[]>>({});
  /** The cursor for the page of repositories after the ones on screen. */
  let nextCursor = $state('');
  /** How many repositories the installation can see, of which those are a page. */
  let totalRepos = $state(0);
  let scanningAll = $state(false);
  let pendingScan: AbortController | undefined;
  let scannedInstallation = '';

  onDestroy(() => {
    scanningAll = false;
    pendingScan?.abort();
  });
  /** Hosted label -> the runs-on value replacing it. "" means "leave it alone". */
  let mapping = $state<Record<string, string>>({});
  /** True once the operator has edited the mapping, so a re-scan stops overwriting it. */
  let mappingEdited = false;
  /**
   * The exceptions to that mapping, one per job. Empty is the normal state:
   * a fleet that wants one answer per label never touches this.
   */
  let overrides = $state<MigrationOverride[]>([]);
  /**
   * Whether each pull request also adds the badge to the README. It starts
   * ticked -- the badge is how the people who read a README find out what
   * runs its CI -- and the review step is where it is unticked.
   */
  let badge = $state(true);

  /* -- the outcome --------------------------------------------------------- */

  let outcome = $state<MigrationOutcome | null>(null);

  $effect(() => {
    void installationsAttempt;
    loadingInstallations = true;
    installationsError = '';
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

  const chosen = $derived(Object.keys(selection));

  /**
   * Every chosen file, whether or not it currently changes.
   *
   * The exceptions step works from this rather than from `selectedChanged`: a
   * repository whose only label is unmapped changes nothing yet, and pointing
   * one of its jobs at a pool by hand is exactly what that step is for. It is
   * still narrowed to the ticked files, because a file nobody chose is not one
   * an exception could reach.
   */
  const selectedFiles = $derived.by(() => {
    const out: MigrationRepo[] = [];
    for (const repo of plan?.repositories ?? []) {
      const paths = selection[repo.repo ?? ''] ?? [];
      // A re-scan can find that a repository was archived, or migrated by
      // somebody else, since its files were ticked. Dropping it here keeps the
      // review, the counts and the pull requests on one decision.
      if (paths.length === 0 || !isMigratable(repo)) continue;
      out.push({
        ...repo,
        workflows: (repo.workflows ?? []).filter((w) => paths.includes(w.path ?? '')),
      });
    }
    return out;
  });

  /**
   * Exceptions for files still in the selection. Untick a file and its
   * exceptions stop being sent -- they are not forgotten, so ticking it again
   * brings them back, but nothing the operator cannot see can reach a commit.
   */
  const liveOverrides = $derived(
    overrides.filter((o) => (selection[o.repo ?? ''] ?? []).includes(o.path ?? '')),
  );

  /**
   * The repositories the review step shows, narrowed to the chosen files.
   *
   * Narrowing here rather than in the review means the diffs, the counts and
   * the pull requests all come from one decision: a file nobody ticked is not
   * shown, not counted, and not committed.
   */
  const selectedChanged = $derived(
    selectedFiles.filter((r) => (r.workflows ?? []).some((w) => (w.rewrites ?? []).length > 0)),
  );
  const blockedByPermissions = $derived((plan?.missing_permissions ?? []).length > 0);

  const canAdvance = $derived.by(() => {
    if (busy) return false;
    switch (step) {
      case 0:
        return selected !== '';
      case 1:
        return chosen.length > 0 && chosen.length <= MAX_SELECTED_REPOSITORIES;
      case 2:
        // An operator who maps nothing here can still point individual jobs at
        // a pool on the next step, so this is the one step Next does not gate:
        // it gates the pull requests, on the last step, where it matters.
        return true;
      case 3:
        return true;
      case 4:
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
   * `repos` is left empty on the first scan so the operator sees the
   * organisation and chooses from it; every scan after that is narrowed to what
   * they chose, which is both faster and a smaller slice of the installation's
   * GitHub quota.
   *
   * `cursor` reads the next page of an organisation too large to read at once.
   * A page is merged into the plan rather than replacing it, because the point
   * of paging is to end up looking at one list of everything you have looked
   * at, with the choices you made on the way still ticked.
   */
  async function scan(repos: string[], cursor = ''): Promise<boolean> {
    pendingScan?.abort();
    const controller = new AbortController();
    pendingScan = controller;
    busy = true;
    failure = '';
    try {
      const result = await planMigration(
        {
          installation_id: selected,
          ...(repos.length > 0 ? { repos } : {}),
          ...(cursor !== '' ? { cursor } : {}),
          ...(mappingEdited ? { mapping } : {}),
          ...(liveOverrides.length > 0 ? { overrides: liveOverrides } : {}),
        },
        controller.signal,
      );
      if (controller.signal.aborted) return false;
      const existing = new Set((plan?.repositories ?? []).map((r) => r.repo));
      const narrowed = repos.length > 0;
      plan = narrowed ? mergeNarrowed(plan, result) : mergePage(plan, result, cursor !== '');
      if (narrowed) {
        const next = { ...selection };
        for (const repo of result.repositories ?? []) {
          const name = repo.repo ?? '';
          const paths = (repo.workflows ?? [])
            .filter((w) => (w.hosted_labels ?? []).length > 0)
            .map((w) => w.path);
          const kept = (next[name] ?? []).filter((path) => paths.includes(path));
          if (!isMigratable(repo) || kept.length === 0) delete next[name];
          else next[name] = kept;
        }
        selection = next;
      }
      if (!narrowed) {
        nextCursor = result.next_cursor ?? '';
        totalRepos = result.total_repos ?? (plan?.repositories ?? []).length;
      }
      if (!mappingEdited) {
        // The server's proposal, plus an explicit blank for every label it
        // could not place, so the mapping step lists all of them.
        const next: Record<string, string> = {};
        for (const label of plan?.hosted_labels ?? []) next[label] = '';
        Object.assign(next, cursor ? mapping : {}, result.mapping ?? {});
        mapping = next;
      }
      if (!narrowed)
        chooseByDefault(
          (result.repositories ?? []).filter((r) => !cursor || !existing.has(r.repo)),
        );
      return true;
    } catch (cause) {
      if (!controller.signal.aborted) report(cause, 'The repositories could not be read.');
      return false;
    } finally {
      if (pendingScan === controller) busy = false;
    }
  }

  /** A fresh scan, or the page after the one on screen appended to it. */
  function mergePage(
    current: MigrationPlan | null,
    page: MigrationPlan,
    append: boolean,
  ): MigrationPlan {
    if (!append || !current) return page;
    const seen = new Set((current.repositories ?? []).map((r) => r.repo));
    return {
      ...page,
      repositories: [
        ...(current.repositories ?? []),
        ...(page.repositories ?? []).filter((r) => !seen.has(r.repo)),
      ],
      hosted_labels: [
        ...new Set([...(current.hosted_labels ?? []), ...(page.hosted_labels ?? [])]),
      ].sort(),
    };
  }

  /**
   * A re-scan of the chosen repositories, folded back into the whole list.
   *
   * The repositories nobody chose keep the plan they were read with. Their diff
   * is stale under a mapping that has since changed, which is why the step that
   * shows a diff shows only the repositories that were chosen and re-read.
   */
  function mergeNarrowed(current: MigrationPlan | null, fresh: MigrationPlan): MigrationPlan {
    if (!current) return fresh;
    const byName = new Map((fresh.repositories ?? []).map((r) => [r.repo, r]));
    const kept = (current.repositories ?? []).map((r) => byName.get(r.repo) ?? r);
    for (const repo of fresh.repositories ?? []) {
      if (!kept.some((r) => r.repo === repo.repo)) kept.push(repo);
    }
    return { ...fresh, repositories: kept };
  }

  /**
   * Ticks every workflow file that asks for a rented runner, in the
   * repositories a scan has just brought in.
   *
   * On the labels, not on "would change under the mapping so far": the mapping
   * is chosen on the step after this one, so ticking on what the server's
   * provisional mapping already rewrites would hide from an operator the very
   * files they came here to map. Choices already made elsewhere in the list are
   * left alone.
   */
  function chooseByDefault(repos: readonly MigrationRepo[]): void {
    const next = { ...selection };
    for (const repo of repos) {
      const name = repo.repo ?? '';
      // isMigratable, not just repo.error: an archived repository is read-only
      // on GitHub, so ticking its files would walk the operator all the way to
      // a pull request nothing could open.
      if (!name || name in next || !isMigratable(repo)) continue;
      const paths = (repo.workflows ?? [])
        .filter((w) => (w.hosted_labels ?? []).length > 0)
        .map((w) => w.path ?? '')
        .filter(Boolean);
      if (paths.length > 0 && Object.keys(next).length < MAX_SELECTED_REPOSITORIES)
        next[name] = paths;
    }
    selection = next;
  }

  /** Keep paging until the installation is checked, or the operator pauses. */
  async function loadMore(): Promise<void> {
    if (busy || scanningAll || nextCursor === '') return;
    scanningAll = true;
    const seen: string[] = [];
    try {
      while (scanningAll && nextCursor !== '') {
        if (seen.includes(nextCursor)) {
          failure =
            'GitHub returned the same page twice. Pause here and try the remaining repositories again.';
          break;
        }
        seen.push(nextCursor);
        if (!(await scan([], nextCursor))) break;
      }
    } finally {
      scanningAll = false;
    }
  }

  async function next(): Promise<void> {
    switch (step) {
      case 0: {
        if (scannedInstallation === selected && plan) {
          void loadMore();
          return;
        }
        if (scannedInstallation !== selected) {
          plan = null;
          selection = {};
          nextCursor = '';
          totalRepos = 0;
          mapping = {};
          mappingEdited = false;
          overrides = [];
          scannedInstallation = selected;
        }
        if (!(await scan([]))) step -= 1;
        else void loadMore();
        return;
      }
      case 1:
        // Returning to this step can change which labels need mapping. Refresh
        // the chosen repositories before showing that next question again.
        if (!(await scan(chosen))) step -= 1;
        else if (chosen.length === 0) {
          failure =
            'The chosen repositories no longer have accessible workflows to migrate. Choose another repository.';
          step -= 1;
        }
        return;
      case 2:
      case 3: {
        // The mapping or the exceptions changed, so every diff downstream is
        // stale. A scan that failed leaves the old plan in place, and the next
        // step must not show it as if it were the new answer's -- what the
        // pull requests would contain is built from those, not from what is on
        // screen.
        if (!(await scan(chosen))) step -= 1;
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

  /**
   * One job's exception. `to` of null is "follow the label mapping", which is
   * the absence of an exception rather than an exception to nothing -- so it
   * drops the entry instead of storing an empty one.
   */
  function onOverrideChange(repo: string, path: string, job: string, to: string | null): void {
    const rest = overrides.filter((o) => !(o.repo === repo && o.path === path && o.job === job));
    overrides = to === null ? rest : [...rest, { repo, path, job, to }];
  }

  async function rescan(): Promise<void> {
    await scan(chosen);
  }

  /** Back to the start, keeping the installation and nothing else. */
  function restart(): void {
    outcome = null;
    badge = true;
    scannedInstallation = '';
    scanningAll = false;
    plan = null;
    selection = {};
    nextCursor = '';
    totalRepos = 0;
    mapping = {};
    mappingEdited = false;
    overrides = [];
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
      // The files, not just the repositories: a repository where only two of
      // its five workflows were ticked must get a pull request touching two.
      const workflows: Record<string, string[]> = {};
      for (const repo of selectedChanged) {
        workflows[repo.repo ?? ''] = (repo.workflows ?? [])
          .filter((w) => (w.rewrites ?? []).length > 0)
          .map((w) => w.path ?? '')
          .filter(Boolean);
      }
      const result = await openMigrationPullRequests({
        installation_id: selected,
        repos: selectedChanged.map((r) => r.repo ?? '').filter(Boolean),
        mapping: chosenMapping,
        overrides: liveOverrides,
        workflows,
        badge,
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
  <ErrorState
    title="GitHub is not reachable"
    description={installationsError}
    onretry={() => (installationsAttempt += 1)}
  />
{:else if installations.length === 0}
  <!-- A step not taken yet is not a failure, and red means failure here. Every
       other page that wants something done first says so neutrally and carries
       the button, so this one does too: it used to be a red alert with the
       instruction as plain text and nothing to press. -->
  <EmptyState
    icon={Plug}
    title="Connect GitHub to migrate"
    description="A migration reads and rewrites workflows through a GitHub App installation, and Zoomies has none yet."
  >
    {#if session.can('admin')}
      <Button variant="primary" href="/installations?connect=1">Connect GitHub</Button>
    {:else}
      <p class="need-admin">An administrator can connect one.</p>
    {/if}
  </EmptyState>
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
    nextLabel={step === 1 && nextCursor !== '' ? `Continue with ${chosen.length} selected` : 'Next'}
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
        <StepRepositories
          {plan}
          bind:selection
          total={totalRepos}
          hasMore={nextCursor !== ''}
          {busy}
          {scanningAll}
          onpause={() => (scanningAll = false)}
          onloadmore={loadMore}
        />
      {:else if index === 2}
        <StepMapping {plan} {mapping} onchange={onMappingChange} />
      {:else if index === 3}
        <StepOverrides
          {plan}
          repos={selectedFiles}
          {mapping}
          overrides={liveOverrides}
          onchange={onOverrideChange}
        />
      {:else}
        <StepReview
          {plan}
          repos={selectedChanged}
          {busy}
          target={target?.target ?? ''}
          bind:badge
          onrescan={rescan}
        />
      {/if}
    {/snippet}
  </Wizard>

  {#if step === 4 && plan}
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
  .need-admin {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .loading {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
  }
  .failure {
    margin: 0 0 var(--z-space-4);
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-danger-border);
    border-radius: var(--z-radius-md);
    background: var(--z-danger-subtle);
    color: var(--z-danger);
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
