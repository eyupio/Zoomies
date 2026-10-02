<script lang="ts">
  import { untrack } from 'svelte';
  import {
    ApiError,
    createAIContextDraft,
    discoverAIContext,
    findAIContextDraft,
    getAIContextMembers,
    getAIContextRepository,
    listInstallations,
    listUsers,
    putAIContextMembers,
    updateAIContextConfig,
  } from '$lib/api/client';
  import type {
    AIContextConfig,
    AIContextDiscovery,
    AIContextRepository,
    Installation,
    User,
  } from '$lib/api/types';
  import { router } from '$lib/router';
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
  }
  let { draftId = '', installationId = '' }: Props = $props();
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
    { id: 'review', title: 'Review', description: 'Review and save resumable setup drafts.' },
  ];
  let step = $state(0);
  let loading = $state(true),
    discovering = $state(false),
    busy = $state(false);
  let failure = $state<unknown>(null);
  let reload = $state(0);
  type KnownInstallation = Installation & { id: string; target: string };
  type KnownUser = User & { id: string; username: string };
  let installations = $state<KnownInstallation[]>([]),
    users = $state<KnownUser[]>([]);
  let selectedInstallation = $state(untrack(() => installationId));
  let discovery = $state<AIContextDiscovery | null>(null);
  let repositoryIds = $state<number[]>([]),
    readerIds = $state<string[]>([]);
  let search = $state(''),
    readerSearch = $state('');
  let destination = $state<string>('both'),
    exclusions = $state(''),
    keep = $state('3');
  let resuming = $state<AIContextRepository | null>(null);
  type Outcome = {
    name: string;
    draft?: AIContextRepository;
    saved?: boolean;
    error?: string;
    blocked?: boolean;
  };
  let outcomes = $state<Record<number, Outcome>>({});
  let showResults = $state(false);

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
  const configValid = $derived(
    Number.isInteger(Number(keep)) &&
      Number(keep) >= 1 &&
      Number(keep) <= 100 &&
      exclusions.split('\n').filter((line) => line.trim()).length <= 100,
  );
  const canAdvance = $derived(
    !loading &&
      !discovering &&
      !failure &&
      repositoryIds.length > 0 &&
      selectedRepositories.length === repositoryIds.length &&
      selectedRepositories.every((r) => !r.archived) &&
      (step !== 1 || discovery?.can_read_contents === true) &&
      (step !== 3 || configValid) &&
      (step < 2 || destination !== 'zoomies'),
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
      outcomes = {};
      repositoryIds = [];
      readerIds = [];
    }
    void reload;
    const controller = new AbortController();
    loading = true;
    failure = null;
    void Promise.all([
      listInstallations(controller.signal),
      listUsers(controller.signal),
      resumeId ? getAIContextRepository(resumeId, controller.signal) : Promise.resolve(null),
    ])
      .then(async ([installationList, userList, draft]) => {
        if (controller.signal.aborted) return;
        installations = (installationList.items ?? []).filter(
          (i): i is KnownInstallation => !!i.id && !!i.target,
        );
        users = (userList.items ?? []).filter((u): u is KnownUser => !!u.id && !!u.username);
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
        }
        if (selectedInstallation) {
          const result = await discoverAIContext(selectedInstallation, controller.signal);
          if (controller.signal.aborted) return;
          discovery = result;
          if (!draft) {
            exclusions = result.default_exclusions.join('\n');
            keep = String(result.default_keep_snapshots);
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
    showResults = true;
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
        };
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
  }
</script>

{#if loading}<Skeleton lines={4} />
{:else if showResults}
  <section class="result-panel" aria-labelledby="result-heading">
    <h2 id="result-heading" tabindex="-1">Setup drafts</h2>
    <p class="muted">
      Saved drafts can be resumed after a restart. Repository context becomes operational only after
      setup is reviewed, merged and successfully built.
    </p>
    <div class="choices" aria-live="polite">
      {#each Object.entries(outcomes) as [id, result] (id)}
        <section class="result">
          <div class="row">
            <h3>{result.name}</h3>
            <Badge
              label={result.saved ? 'Draft saved' : result.error ? 'Needs attention' : 'Saving'}
            />
          </div>
          {#if result.error}<p role="alert">{result.error}</p>{/if}
          {#if result.draft}<Button
              size="sm"
              disabled={busy}
              href="/ai-context/setup?draft_id={encodeURIComponent(result.draft.id)}"
              >Resume draft</Button
            >{/if}
        </section>
      {/each}
    </div>
    <div class="actions">
      <Button href="/ai-context" disabled={busy}>Back to AI Context</Button
      >{#if failed.length}<Button variant="primary" loading={busy} onclick={saveDrafts}
          >Retry failed drafts</Button
        >{/if}
    </div>
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
    finishLabel="Save setup drafts"
    onfinish={saveDrafts}
    oncancel={() => router.navigate('/ai-context')}
  >
    {#snippet children(current)}
      {#if current.id === 'repositories'}
        <div class="form">
          <Field
            label="GitHub installation"
            hint="Choose one installation at a time. No repository is preselected."
            required
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
                  description={`${repository.private ? 'Private' : 'Public'} · ${repository.archived ? 'Archived — unavailable for setup' : 'Source branch: ' + repository.default_branch}`}
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
          <Button loading={discovering} onclick={() => void recheck()}>Recheck permissions</Button
          ><Button href="/installations">Manage installations</Button>
        </div>
      {:else if current.id === 'output'}
        <RadioGroup
          bind:value={destination}
          name="context-output"
          legend="Output destination"
          options={[
            {
              value: 'both',
              label: 'Repository and Zoomies',
              description:
                'Publish repository context and make it available through Zoomies after verified ingestion.',
            },
            {
              value: 'repository',
              label: 'Repository',
              description:
                'Keep generated context on a dedicated repository branch and as an Actions artifact.',
            },
            {
              value: 'zoomies',
              label: 'Zoomies only',
              description: 'Unavailable until secure workflow uploads are supported.',
              disabled: true,
            },
          ]}
        />
        <p class="muted">
          Both includes an additional source copy in Zoomies. Explicit reader membership and
          connection consent control access to that copy.
        </p>
      {:else if current.id === 'configuration'}
        <div class="form">
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
            hint="One repository-relative pattern per line. Secrets, dependencies and generated files are excluded by default."
            >{#snippet children({ id, describedBy })}<Textarea
                {id}
                {describedBy}
                bind:value={exclusions}
                rows={7}
                mono
              />{/snippet}</Field
          >
          <Field
            label="Snapshots to retain"
            hint="Keep between 1 and 100 successful snapshots."
            error={!configValid
              ? 'Use a whole retention count from 1 to 100 and at most 100 exclusions.'
              : ''}
            >{#snippet children({ id, describedBy, invalid })}<Input
                {id}
                {describedBy}
                {invalid}
                bind:value={keep}
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
            Choose source readers explicitly. Administrator and fleet roles do not add readers
            automatically. Each person then chooses which of their MCP connections may read these
            repositories.
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
                disabled={readerIds.length >= 200 && !readerIds.includes(user.id)}
                onchange={(checked) => {
                  readerIds = checked
                    ? [...readerIds, user.id]
                    : readerIds.filter((id) => id !== user.id);
                }}
              />{/each}
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
              <dd>{destination === 'both' ? 'Repository and Zoomies' : 'Repository'}</dd>
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
          <h3>Repository setup paths</h3>
          <ul class="paths">
            <li><code>.github/workflows/zoomies-ai-context.yml</code></li>
            <li><code>zoomies-ai-context.config.json</code></li>
            <li>Generated branch: <code>zoomies-ai-context</code></li>
            <li>Generated files: <code>.zoomies/ai-context/</code></li>
            <li>Zoomies AI Context README badge and instruction guidance</li>
          </ul>
          <p class="muted">
            Drafts are not active context. Creating setup pull requests will be a separate reviewed
            action; it is not available in this preparation flow yet.
          </p>
        </div>
      {/if}
    {/snippet}
  </Wizard>
{/if}

<style>
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
  .result-panel > .choices {
    margin-top: var(--z-space-4);
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
