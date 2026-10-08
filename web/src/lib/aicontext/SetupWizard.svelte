<script lang="ts">
  import { tick, untrack } from 'svelte';
  import {
    ApiError,
    createAIContextDraft,
    createAIContextSetupPR,
    previewAIContextSetup,
    discoverAIContext,
    findAIContextDraft,
    getAIContextMembers,
    getAIContextRepository,
    listContextInstallations,
    listUsers,
    putAIContextMembers,
    updateAIContextConfig,
  } from '$lib/api/client';
  import type {
    AIContextConfig,
    AIContextSetupPreview,
    AIContextDiscovery,
    AIContextRepository,
    User,
  } from '$lib/api/types';
  import { archivedNote, installationHint, NOT_FOUND_NOTE } from '$lib/aicontext/preselect';
  import { router } from '$lib/router';
  import { session } from '$lib/state/session.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import { aiContextStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import RadioGroup from '$lib/components/RadioGroup.svelte';
  import Select from '$lib/components/Select.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Textarea from '$lib/components/Textarea.svelte';
  import Wizard from '$lib/components/Wizard.svelte';
  import type { WizardStep } from '$lib/components/Wizard.svelte';

  interface Props {
    draftId?: string;
    installationId?: string;
    /** GitHub's ID for the repository the person came from, if they came from one. */
    repositoryId?: number;
  }
  let { draftId = '', installationId = '', repositoryId }: Props = $props();
  const steps: readonly WizardStep[] = [
    {
      id: 'repositories',
      title: 'Repositories',
      description: 'Choose an installation and repositories.',
    },
    {
      id: 'readiness',
      title: 'Readiness',
      description: 'Check permissions before preparing setup.',
    },
    { id: 'output', title: 'Output', description: 'Where your source context will be kept.' },
    {
      id: 'configuration',
      title: 'Context',
      description: 'Source branches, exclusions and retention.',
    },
    {
      id: 'access',
      title: 'Access',
      description: 'Explicit source readers, separately from fleet roles.',
    },
    {
      id: 'review',
      title: 'Review',
      description: 'Save resumable drafts and inspect managed changes.',
    },
  ];
  let step = $state(0);
  let loading = $state(true),
    discovering = $state(false),
    busy = $state(false);
  let failure = $state<unknown>(null);
  let reload = $state(0);
  const isAdmin = $derived(session.can('admin'));
  type KnownInstallation = { id: string; target: string };
  type KnownUser = User & { id: string; username: string };
  let installations = $state<KnownInstallation[]>([]),
    users = $state<KnownUser[]>([]);
  let selectedInstallation = $state(untrack(() => installationId));
  // The repository the person came from is ticked once, when the first list arrives,
  // and from then on the choice is theirs: a different installation, a recheck or a
  // reload never ticks it again, and unticking it holds.
  const cameFromId = untrack(() => repositoryId);
  let cameFrom = $state<string | null>(null);
  let cameFromNote = $state('');
  let arrivalHandled = false;
  let discovery = $state<AIContextDiscovery | null>(null);
  let repositoryIds = $state<number[]>([]),
    readerIds = $state<string[]>([]);
  let search = $state(''),
    readerSearch = $state('');
  let destination = $state<string>('both'),
    exclusions = $state(''),
    keep = $state('3');
  let setupFrozen = $state(false);
  let resuming = $state<AIContextRepository | null>(null);
  type Outcome = {
    name: string;
    draft?: AIContextRepository;
    saved?: boolean;
    error?: string;
    blocked?: boolean;
    preview?: AIContextSetupPreview;
    setupError?: string;
    submitted?: boolean;
  };
  let outcomes = $state<Record<number, Outcome>>({});
  let showResults = $state(false);
  let resultHeading = $state<HTMLHeadingElement | null>(null);
  let operation = $state<'drafts' | 'preview' | 'publish' | null>(null);
  const retentionValid = $derived(
    Number.isInteger(Number(keep)) && Number(keep) >= 1 && Number(keep) <= 100,
  );
  function aiInstructions(result: Outcome): string {
    if (result.draft?.instructions) return result.draft.instructions;
    const guidance = result.preview?.files.find((file) => file.path === 'AGENTS.md')?.content;
    if (guidance) {
      const section = guidance
        .split('<!-- zoomies-ai-context:start -->')[1]
        ?.split('<!-- zoomies-ai-context:end -->')[0]
        ?.trim();
      if (section)
        return `For ${result.name}, use these repository context instructions:\n\n${section}`;
    }
    return `Read the Zoomies AI Context section in AGENTS.md on the default branch of ${result.name} using your authorised GitHub access, and follow its destination and freshness instructions. If the setup PR is not merged or generation has failed, report that context is not ready.`;
  }
  const exclusionsValid = $derived(
    exclusions.split('\n').filter((line) => line.trim()).length <= 100,
  );

  const selectedRepositories = $derived(
    (discovery?.repositories ?? []).filter((r) => repositoryIds.includes(r.id)),
  );
  const matchingRepositories = $derived(
    (discovery?.repositories ?? []).filter((r) =>
      r.full_name.toLowerCase().includes(search.toLowerCase()),
    ),
  );
  const matchingUsers = $derived(
    users.filter(
      (u) =>
        !u.disabled &&
        (u.username + ' ' + (u.display_name ?? ''))
          .toLowerCase()
          .includes(readerSearch.toLowerCase()),
    ),
  );
  const configValid = $derived(retentionValid && exclusionsValid);
  const canAdvance = $derived(
    !loading &&
      !discovering &&
      !failure &&
      repositoryIds.length > 0 &&
      selectedRepositories.length === repositoryIds.length &&
      selectedRepositories.every((r) => !r.archived) &&
      (step !== 1 || discovery?.can_read_contents === true) &&
      (step !== 3 || configValid) &&
      (step < 2 || destination !== 'zoomies' || discovery?.zoomies_upload_available === true),
  );
  const failed = $derived(Object.values(outcomes).filter((r) => r.error && !r.blocked));

  let loadedDraftKey = '';
  $effect(() => {
    const resumeId = draftId;
    if (loadedDraftKey !== resumeId) {
      loadedDraftKey = resumeId;
      step = 0;
      showResults = false;
      resuming = null;
      setupFrozen = false;
      outcomes = {};
      repositoryIds = [];
      readerIds = [];
    }
    void reload;
    const controller = new AbortController();
    loading = true;
    failure = null;
    void Promise.all([
      listContextInstallations(controller.signal),
      // The user directory is an administrator's. An owner chooses only
      // themselves as a reader, so they never need it.
      isAdmin ? listUsers(controller.signal) : Promise.resolve({ items: [] }),
      resumeId ? getAIContextRepository(resumeId, controller.signal) : Promise.resolve(null),
    ])
      .then(async ([installationList, userList, draft]) => {
        if (controller.signal.aborted) return;
        installations = installationList.items ?? [];
        users = (userList.items ?? []).filter((u): u is KnownUser => !!u.id && !!u.username);
        if (!isAdmin && session.identity?.id) {
          users = [
            {
              id: session.identity.id,
              username: session.identity.name ?? 'You',
              display_name: 'Me',
            } as KnownUser,
          ];
        }
        if (draft) {
          const members = await getAIContextMembers(draft.id, controller.signal);
          if (controller.signal.aborted) return;
          resuming = draft;
          selectedInstallation = draft.repository.installation_id;
          repositoryIds = [draft.repository.repository_id];
          readerIds = members.user_ids;
          destination = draft.config.destination;
          exclusions = draft.config.exclude.join('\n');
          keep = String(draft.config.keep_snapshots);
          outcomes = { [draft.repository.repository_id]: { name: draft.full_name, draft } };
          try {
            const preview = await previewAIContextSetup(draft.id, controller.signal);
            if (controller.signal.aborted) return;
            setupFrozen = !!preview.setup;
            if (setupFrozen)
              outcomes[draft.repository.repository_id] = {
                name: draft.full_name,
                draft,
                saved: true,
                preview,
                submitted: preview.setup?.state === 'awaiting_merge',
              };
          } catch {
            // Draft editing remains available when permissions or conflicts
            // prevent setup preview; the explicit review surfaces the refusal.
          }
        }
        if (selectedInstallation) {
          const result = await discoverAIContext(selectedInstallation, controller.signal);
          if (controller.signal.aborted) return;
          discovery = result;
          if (!draft) {
            exclusions = result.default_exclusions.join('\n');
            keep = String(result.default_keep_snapshots);
            // A draft already says which repository it is for, and an address that
            // names another is not allowed to change that.
            arriveAt(result);
          }
        }
      })
      .catch((cause: unknown) => {
        if (!controller.signal.aborted) failure = cause;
      })
      .finally(() => {
        if (!controller.signal.aborted) loading = false;
      });
    return () => controller.abort();
  });

  function arriveAt(result: AIContextDiscovery): void {
    if (arrivalHandled || cameFromId === undefined) return;
    arrivalHandled = true;
    const found = (result.repositories ?? []).find((r) => r.id === cameFromId);
    if (!found) {
      cameFromNote = NOT_FOUND_NOTE;
    } else if (found.archived) {
      cameFromNote = archivedNote(found.full_name);
    } else {
      repositoryIds = [found.id];
      cameFrom = found.full_name;
    }
  }
  async function chooseInstallation(id: string): Promise<void> {
    selectedInstallation = id;
    repositoryIds = [];
    discovery = null;
    failure = null;
    if (!id) return;
    discovering = true;
    try {
      discovery = await discoverAIContext(id);
      exclusions = discovery.default_exclusions.join('\n');
      keep = String(discovery.default_keep_snapshots);
    } catch (cause) {
      failure = cause;
    } finally {
      discovering = false;
    }
  }
  async function recheck(): Promise<void> {
    discovering = true;
    failure = null;
    try {
      discovery = await discoverAIContext(selectedInstallation);
    } catch (cause) {
      failure = cause;
    } finally {
      discovering = false;
    }
  }
  function chooseRepository(id: number, checked: boolean): void {
    repositoryIds = checked
      ? [...repositoryIds, id]
      : repositoryIds.filter((value) => value !== id);
  }
  async function saveDrafts(): Promise<void> {
    if (busy) return;
    busy = true;
    operation = 'drafts';
    const openingReview = !showResults;
    showResults = true;
    if (openingReview) {
      await tick();
      resultHeading?.focus();
    }
    for (const repository of selectedRepositories) {
      if (!outcomes[repository.id]) outcomes[repository.id] = { name: repository.full_name };
    }
    for (const repository of selectedRepositories) {
      const prior = outcomes[repository.id];
      if (prior?.saved || prior?.blocked) continue;
      let draft = prior?.draft;
      try {
        if (!draft) {
          try {
            draft = await findAIContextDraft(selectedInstallation, repository.id);
            outcomes[repository.id] = {
              name: repository.full_name,
              draft,
              blocked: true,
              error:
                'An existing setup draft was found. Resume it to review its saved configuration and readers.',
            };
            continue;
          } catch (cause) {
            if (!(cause instanceof ApiError && cause.status === 404)) throw cause;
          }
          draft = await createAIContextDraft(selectedInstallation, repository.id);
          outcomes[repository.id] = { name: repository.full_name, draft };
        }
        const config: AIContextConfig = {
          ...draft.config,
          destination: destination as AIContextConfig['destination'],
          exclude: exclusions
            .split('\n')
            .map((line) => line.trim())
            .filter(Boolean),
          keep_snapshots: Number(keep),
          readme_badge: undefined,
        };
        if (!setupFrozen)
          draft = await updateAIContextConfig(draft.id, { revision: draft.revision, config });
        outcomes[repository.id] = { name: repository.full_name, draft };
        await putAIContextMembers(draft.id, readerIds);
        outcomes[repository.id] = { name: repository.full_name, draft, saved: true };
      } catch (cause) {
        outcomes[repository.id] = {
          name: repository.full_name,
          draft,
          error: cause instanceof Error ? cause.message : 'This draft could not be saved.',
          blocked: cause instanceof ApiError && (cause.status === 409 || cause.status === 422),
        };
      }
    }
    busy = false;
    operation = null;
  }

  async function reviewSetups(): Promise<void> {
    await saveDrafts();
    await previewSetups();
  }
  async function previewSetups(): Promise<void> {
    if (busy) return;
    busy = true;
    operation = 'preview';
    for (const repository of selectedRepositories) {
      const result = outcomes[repository.id];
      if (!result?.saved || !result.draft || result.submitted) continue;
      try {
        const preview = await previewAIContextSetup(result.draft.id);
        outcomes[repository.id] = {
          ...result,
          preview,
          setupError: undefined,
          submitted: preview.setup?.state === 'awaiting_merge',
        };
      } catch (cause) {
        outcomes[repository.id] = {
          ...result,
          preview: undefined,
          setupError: cause instanceof Error ? cause.message : 'Setup could not be previewed.',
        };
      }
    }
    busy = false;
    operation = null;
  }
  async function createSetups(): Promise<void> {
    if (busy) return;
    busy = true;
    operation = 'publish';
    for (const repository of selectedRepositories) {
      const result = outcomes[repository.id];
      if (!result?.saved || !result.draft || !result.preview || result.submitted) continue;
      try {
        const preview = await createAIContextSetupPR(result.draft.id, {
          revision: result.preview.revision,
          plan_hash: result.preview.plan_hash,
        });
        outcomes[repository.id] = { ...result, preview, submitted: true, setupError: undefined };
      } catch (cause) {
        outcomes[repository.id] = {
          ...result,
          preview: cause instanceof ApiError && cause.status === 409 ? undefined : result.preview,
          setupError: cause instanceof Error ? cause.message : 'Setup PR could not be created.',
        };
      }
    }
    busy = false;
    operation = null;
  }
  const readySetups = $derived(
    Object.values(outcomes).filter((r) => r.saved && r.preview && !r.submitted),
  );
</script>

{#if loading}<Skeleton lines={4} />
{:else if showResults}
  <section class="result-panel" aria-labelledby="result-heading">
    <h2 id="result-heading" bind:this={resultHeading} tabindex="-1">Review repository changes</h2>
    <p class="muted">
      Saved drafts can be resumed after a restart. Merge and successful generation make repository
      output available. Assistant access also requires verified ingestion.
    </p>
    <p class="review-progress" role="status">
      {#if operation === 'drafts'}Saving repository drafts…
      {:else if operation === 'preview'}Checking proposed files…
      {:else if operation === 'publish'}Creating setup pull requests…
      {:else}{Object.values(outcomes).filter((r) => r.submitted).length} of {Object.keys(outcomes)
          .length} setup PRs created · {readySetups.length} ready to review{/if}
    </p>
    <div class="results" aria-busy={busy}>
      {#each Object.entries(outcomes) as [id, result] (id)}
        <section class="result">
          <div class="row">
            <h3>{result.name}</h3>
            <Badge
              status={aiContextStatus(
                result.submitted
                  ? 'awaiting_merge'
                  : result.setupError || result.error
                    ? 'attention'
                    : result.preview
                      ? 'review'
                      : result.saved
                        ? 'draft'
                        : 'working',
              )}
            />
          </div>
          {#if result.error}<p class="refusal" role="alert">{result.error}</p>{/if}
          {#if result.setupError}<p class="refusal" role="alert">{result.setupError}</p>{/if}
          {#if result.preview}
            {#if result.submitted && result.preview.setup?.pr_url}
              <div class="repository-actions">
                <Button size="sm" newTab href={result.preview.setup.pr_url}>Open setup PR</Button>
              </div>
              <p class="muted">
                Merge and successful generation are required. Assistant source access remains a
                separate choice.
              </p>
              <div class="assistant-setup">
                <h4>Use AI Context with your assistant</h4>
                <p>
                  Copy these instructions into your AI conversation. For repository output, they
                  tell an assistant with GitHub access to use the generated pack first, without MCP.
                  Claude Code can also read the managed guidance in CLAUDE.md.
                </p>
                <CopyButton value={aiInstructions(result)} label="Copy AI instructions" showLabel />
                {#if destination !== 'repository'}
                  <p>
                    For the Zoomies route, connect your assistant and choose this repository under
                    Settings → MCP connections → Source access. Repository and Zoomies output also
                    supports direct GitHub access without MCP.
                  </p>
                  <Button size="sm" href="/settings/connections">Manage MCP connections</Button>
                {:else}
                  <p>Context lives on the zoomies-ai-context branch under .zoomies/ai-context/.</p>
                {/if}
              </div>
            {:else}
              <p class="muted">
                Proposed against source commit <code>{result.preview.base_commit.slice(0, 12)}</code
                >. Existing text is preserved; changed managed files block setup.
              </p>
              {#each result.preview.files as file (file.path)}
                <details class="file-preview">
                  <summary
                    ><span class="file-path">{file.path}</span><span class="file-kind"
                      >{file.previous_sha ? 'Update' : 'New file'}</span
                    ></summary
                  >
                  <div class="preview-tools">
                    <CopyButton value={file.content} label="Copy contents" showLabel />
                  </div>
                  <Textarea
                    readonly
                    mono
                    rows={10}
                    ariaLabel={`Proposed ${file.path}`}
                    value={file.content}
                  />
                </details>
              {/each}
            {/if}
          {/if}
          {#if result.draft}<div class="repository-actions">
              <Button
                size="sm"
                disabled={busy}
                href="/kennel/ai-context/setup?draft_id={encodeURIComponent(result.draft.id)}"
                >{result.submitted ? 'View setup' : 'Resume draft'}</Button
              >
            </div>{/if}
        </section>
      {/each}
    </div>
    <div class="actions">
      <div class="secondary-actions">
        <Button href="/kennel/ai-context" disabled={busy}>Back to AI Context</Button>
      </div>
      <div class="submit-actions">
        {#if failed.length}<Button
            disabled={busy}
            loading={operation === 'drafts'}
            onclick={reviewSetups}>Retry failed drafts</Button
          >{/if}
        {#if Object.values(outcomes).some((r) => r.saved && !r.submitted)}
          <Button disabled={busy} loading={operation === 'preview'} onclick={previewSetups}
            >Recheck setup previews</Button
          >
        {/if}
        {#if readySetups.length}
          <Button
            variant="primary"
            disabled={busy}
            loading={operation === 'publish'}
            onclick={createSetups}>Create setup PRs</Button
          >
        {/if}
      </div>
    </div>
    <p class="muted publication-note">
      Creating setup PRs writes only the reviewed files on a new branch. Configuration is frozen
      once publication starts so retries recover the same proposal. No repository becomes available
      to assistants until verified ingestion.
    </p>
  </section>
{:else if installations.length === 0 && !failure}
  <EmptyState
    title="Connect GitHub first"
    description="AI Context reads repository identity and permissions through an existing GitHub App installation."
    ><Button variant="primary" href="/installations">Connect GitHub</Button></EmptyState
  >
{:else}
  {#if failure}<ErrorState
      error={failure}
      title="Setup could not be loaded"
      onretry={() => {
        reload += 1;
      }}
    />{/if}
  <Wizard
    {steps}
    bind:current={step}
    {canAdvance}
    {busy}
    finishLabel="Review setup changes"
    onfinish={reviewSetups}
    oncancel={() => router.navigate('/kennel/ai-context')}
  >
    {#snippet children(current)}
      {#if current.id === 'repositories'}
        <div class="form">
          <Field label="GitHub installation" hint={installationHint(cameFrom)} required
            >{#snippet children({ id, describedBy })}<Select
                {id}
                {describedBy}
                value={selectedInstallation}
                disabled={discovering || !!resuming}
                options={installations.map((i) => ({ value: i.id, label: i.target }))}
                placeholder="Choose an installation"
                onchange={(value) => void chooseInstallation(value)}
              />{/snippet}</Field
          >
          {#if discovering}<Skeleton lines={3} />{:else if discovery}
            <Field label="Search repositories" hideLabel
              >{#snippet children({ id })}<Input
                  {id}
                  bind:value={search}
                  type="search"
                  placeholder="Search repositories"
                />{/snippet}</Field
            >
            {#if repositoryIds.length > 0 && selectedRepositories.length !== repositoryIds.length}
              <p role="alert">
                The saved repository is no longer visible to this installation. Ask its
                administrator to restore access before resuming setup.
              </p>
            {/if}
            {#if cameFromNote}<p role="status">{cameFromNote}</p>{/if}
            {#if discovery.capped}<p role="status">
                Showing a bounded selection of 500 repositories. Larger installations may have
                additional repositories outside this list.
              </p>{/if}
            <p class="muted" aria-live="polite">
              {repositoryIds.length} of 20 repositories selected
            </p>
            <div class="choices">
              {#each matchingRepositories as repository (repository.id)}
                <Checkbox
                  label={repository.full_name}
                  description={`${repository.private ? 'Private' : 'Public'} · ${repository.archived ? 'Archived, unavailable for setup' : 'Source branch: ' + repository.default_branch}`}
                  checked={repositoryIds.includes(repository.id)}
                  disabled={repository.archived ||
                    !!resuming ||
                    (repositoryIds.length >= 20 && !repositoryIds.includes(repository.id))}
                  onchange={(checked) => chooseRepository(repository.id, checked)}
                />
              {/each}
              {#if matchingRepositories.length === 0}<p class="muted">
                  No matching repositories.
                </p>{/if}
            </div>
          {/if}
        </div>
      {:else if current.id === 'readiness'}
        <div class="form">
          <p>
            {discovery?.can_read_contents
              ? 'Contents read permission is available.'
              : 'Contents read permission is missing. Grant it to the GitHub App and recheck before continuing.'}
          </p>
          {#if discovery?.missing_setup_permissions.length}<div>
              <h3>Before setup pull requests</h3>
              <ul>
                {#each discovery.missing_setup_permissions as permission (permission)}<li>
                    {permission}
                  </li>{/each}
              </ul>
              <p class="muted">
                You can save a draft with read permission. Opening setup pull requests will also
                require these write permissions.
              </p>
            </div>{:else}<p>Managed setup write permissions are available.</p>{/if}
          {#if selectedRepositories.length !== repositoryIds.length}<p role="alert">
              A selected repository is no longer visible to this installation. Return to repository
              selection or ask its administrator to restore access.
            </p>{/if}
          <div class="repository-actions">
            <Button loading={discovering} onclick={() => void recheck()}>Recheck permissions</Button
            ><Button href="/installations">Manage installations</Button>
          </div>
        </div>
      {:else if current.id === 'output'}
        <RadioGroup
          bind:value={destination}
          name="context-output"
          legend="Output destination"
          options={[
            {
              value: 'both',
              disabled: setupFrozen,
              label: 'Repository and Zoomies',
              description:
                'Use the generated repository pack without MCP, or connect an assistant for verified search and reads through Zoomies.',
            },
            {
              value: 'repository',
              disabled: setupFrozen,
              label: 'Repository',
              description:
                'Use the generated JSON pack with existing GitHub access and copied AI instructions. No MCP connection required.',
            },
            {
              value: 'zoomies',
              label: 'Zoomies only',
              description: discovery?.zoomies_upload_available
                ? 'The workflow uploads context straight to Zoomies with a short-lived GitHub Actions token. No generated branch is written.'
                : 'Needs server.external_url set to an https address GitHub can reach.',
              disabled: setupFrozen || !discovery?.zoomies_upload_available,
            },
          ]}
        />
        <p class="muted">
          GitHub permissions control direct access to repository packs. Copy AI instructions after
          setup to tell your assistant where to find the pack and to use it first.
        </p>
        <p class="muted">
          Repository and Zoomies, and Zoomies only, keep a verified source copy in Zoomies. Explicit
          reader membership and connection consent control access to that copy.
        </p>
        <p class="muted">
          The Zoomies AI Context badge is added to your README automatically. Its Markdown is also
          available from AI Context.
        </p>
      {:else if current.id === 'configuration'}
        <div class="form">
          {#if setupFrozen}<p role="status">
              This reviewed setup has already been submitted. Its configuration is frozen so retries
              recover the same pull request. Use the AI Context repository card to reinstall, amend
              or remove it after merging or closing the previous PR.
              <a href="/kennel/ai-context">Manage AI Context</a>
            </p>{/if}
          <div>
            <h3>Source branches</h3>
            <ul>
              {#each selectedRepositories as repository (repository.id)}<li>
                  {repository.full_name}:
                  <code>{resuming?.config.source_branch ?? repository.default_branch}</code>
                </li>{/each}
            </ul>
          </div>
          <Field
            label="Exclusions"
            error={!exclusionsValid ? 'Use at most 100 exclusion patterns.' : ''}
            help="Patterns are relative to the repository root. Mandatory credential exclusions always apply."
            hint="One repository-relative pattern per line. Secrets, dependencies and generated files are excluded by default."
            >{#snippet children({ id, describedBy, invalid })}<Textarea
                {id}
                {describedBy}
                {invalid}
                bind:value={exclusions}
                disabled={setupFrozen}
                rows={7}
                mono
              />{/snippet}</Field
          >
          <Field
            label="Snapshots to retain"
            hint="Keep between 1 and 100 Zoomies snapshots once ingestion is available. Repository output follows normal Git history."
            error={!retentionValid ? 'Use a whole number from 1 to 100.' : ''}
            >{#snippet children({ id, describedBy, invalid })}<Input
                {id}
                {describedBy}
                {invalid}
                bind:value={keep}
                disabled={setupFrozen}
                inputmode="numeric"
              />{/snippet}</Field
          >
          <p class="muted">
            Refresh follows the selected source branch. Saving configuration does not run an AI
            agent or change application code.
          </p>
        </div>
      {:else if current.id === 'access'}
        <div class="form">
          <p>
            {isAdmin
              ? 'Choose source readers explicitly.'
              : 'Choose whether you read these repositories yourself; an administrator assigns other readers.'}
            Administrator and fleet roles do not add readers automatically. Each person then chooses which
            of their MCP connections may read these repositories.
          </p>
          <Field label="Search source readers" hideLabel
            >{#snippet children({ id })}<Input
                {id}
                bind:value={readerSearch}
                type="search"
                placeholder="Search people"
              />{/snippet}</Field
          >
          <p class="muted" aria-live="polite">
            {readerIds.length} readers selected. No selection means no Zoomies source readers.
          </p>
          <div class="choices">
            {#each matchingUsers as user (user.id)}<Checkbox
                label={user.display_name || user.username}
                description={user.username}
                checked={readerIds.includes(user.id)}
                disabled={setupFrozen || (readerIds.length >= 200 && !readerIds.includes(user.id))}
                onchange={(checked) => {
                  readerIds = checked
                    ? [...readerIds, user.id]
                    : readerIds.filter((id) => id !== user.id);
                }}
              />{/each}
            {#if matchingUsers.length === 0}<p class="muted">
                {readerSearch
                  ? 'No matching people. Try another search.'
                  : 'No eligible source readers. You can continue without adding anyone.'}
              </p>{/if}
          </div>
        </div>
      {:else}
        <div class="form">
          <p>
            Save {repositoryIds.length} resumable setup {repositoryIds.length === 1
              ? 'draft'
              : 'drafts'}. No repository files or source grants for app connections are written by
            this action.
          </p>
          <dl>
            <div>
              <dt>Output</dt>
              <dd>
                {destination === 'both'
                  ? 'Repository and Zoomies'
                  : destination === 'zoomies'
                    ? 'Zoomies only'
                    : 'Repository'}
              </dd>
            </div>
            <div>
              <dt>Retention</dt>
              <dd>{keep} successful snapshots</dd>
            </div>
            <div>
              <dt>Source readers</dt>
              <dd>{readerIds.length} explicitly selected</dd>
            </div>
          </dl>
          <h3>Selected repositories</h3>
          <ul class="paths">
            {#each selectedRepositories as repository (repository.id)}<li>
                {repository.full_name}
              </li>{/each}
          </ul>
          <h3>Repository setup paths</h3>
          <ul class="paths">
            <li><code>.github/workflows/zoomies-ai-context.yml</code></li>
            <li><code>zoomies-ai-context.config.json</code></li>
            <li>Generated branch: <code>zoomies-ai-context</code></li>
            <li>Generated files: <code>.zoomies/ai-context/</code></li>
            <li>Zoomies AI Context README badge and instruction guidance</li>
          </ul>
          <p class="muted">
            Drafts are not active context. The next screen previews every proposed file. Create
            setup PRs only after reviewing those changes; opening a PR does not enable source
            access.
          </p>
        </div>
      {/if}
    {/snippet}
  </Wizard>
{/if}

<style>
  .file-preview {
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    padding: var(--z-space-3);
    min-width: 0;
  }
  .file-preview summary {
    cursor: pointer;
    overflow-wrap: anywhere;
    font-size: var(--z-text-sm);
  }
  .file-path {
    font-family: var(--z-font-mono);
  }
  .file-kind {
    color: var(--z-text-subtle);
    margin-left: var(--z-space-2);
    white-space: nowrap;
  }
  .preview-tools {
    display: flex;
    justify-content: flex-end;
    margin: var(--z-space-3) 0;
  }
  .results {
    display: grid;
    gap: var(--z-space-4);
    margin-top: var(--z-space-4);
    min-width: 0;
  }
  .review-progress {
    margin-top: var(--z-space-4);
    color: var(--z-text-muted);
    font-size: var(--z-text-sm);
  }
  .refusal {
    padding: var(--z-space-3);
    border-left: var(--z-border-width-rail) solid var(--z-danger);
    background: var(--z-danger-subtle);
    overflow-wrap: anywhere;
  }
  .repository-actions,
  .secondary-actions,
  .submit-actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-3);
  }
  .row h3 {
    min-width: 0;
    overflow-wrap: anywhere;
    flex: 1;
    margin: 0;
  }
  .result-panel h2 {
    margin: 0 0 var(--z-space-3);
    font-size: var(--z-text-lg);
  }
  @media (max-width: 600px) {
    .result-panel {
      padding: var(--z-space-4);
    }
    .result {
      padding: var(--z-space-3);
    }
    .actions,
    .secondary-actions,
    .submit-actions {
      width: 100%;
    }
    .submit-actions {
      flex-direction: column;
    }
    .submit-actions :global(.btn) {
      width: 100%;
    }
  }

  .publication-note {
    margin-top: var(--z-space-4);
  }

  .form,
  .choices {
    display: grid;
    gap: var(--z-space-4);
  }
  .choices {
    max-height: 400px;
    overflow: auto;
    padding: var(--z-space-1);
    overflow-wrap: anywhere;
  }
  .muted {
    color: var(--z-text-subtle);
  }
  p {
    margin: 0;
  }
  h3 {
    margin: 0 0 var(--z-space-2);
    font-size: var(--z-text-base);
  }
  .row,
  .actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-3);
    justify-content: space-between;
  }
  .actions {
    margin-top: var(--z-space-5);
  }
  .result-panel {
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
    padding: var(--z-space-5);
  }
  .result {
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    padding: var(--z-space-4);
    display: grid;
    gap: var(--z-space-3);
  }

  dl {
    display: grid;
    gap: var(--z-space-3);
    margin: 0;
  }
  dt {
    color: var(--z-text-subtle);
  }
  dd {
    margin: var(--z-space-1) 0 0;
  }
  code {
    overflow-wrap: anywhere;
  }
  .paths {
    padding-left: var(--z-space-5);
  }
</style>
