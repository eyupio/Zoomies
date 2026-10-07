<script lang="ts">
  import { BookOpenText, Plus } from '@lucide/svelte';
  import {
    listAIContextRepositories,
    listReadableAIContext,
    listContextInstallations,
    recheckAIContext,
    regenerateAIContext,
  } from '$lib/api/client';
  import type { AIContextRepository } from '$lib/api/types';
  import { router } from '$lib/router';
  import { toasts } from '$lib/state/toasts.svelte';
  import { session } from '$lib/state/session.svelte';
  import { formatAbsolute } from '$lib/format';
  import { AI_CONTEXT_URL, REPOMIX_URL } from '$lib/links';
  import { aiContextStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import NotesPanel from '$lib/aicontext/NotesPanel.svelte';
  import OwnersDialog from '$lib/aicontext/OwnersDialog.svelte';
  import Select from '$lib/components/Select.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';

  const isAdmin = $derived(session.can('admin'));
  const query = $derived(router.param('q'));
  const installationId = $derived(router.param('installation_id'));
  const offset = $derived(Math.max(0, Number(router.param('offset')) || 0));
  let items = $state<
    (
      | AIContextRepository
      | { id: string; full_name: string; instructions?: string; badge_markdown?: string }
    )[]
  >([]);
  // The installations this person may enable repositories for: all of them for
  // an administrator, otherwise only the ones an administrator made them owner of.
  type KnownInstallation = { id: string; target: string };
  let installations = $state<KnownInstallation[]>([]);
  let installationsLoaded = $state(false);
  let ownersFor = $state<KnownInstallation | null>(null);
  let ownersOpen = $state(false);
  const canConfigure = $derived(isAdmin || installations.length > 0);
  let total = $state(0);
  let loading = $state(true);
  let failure = $state<unknown>(null);
  let reload = $state(0);
  let checking = $state<string | null>(null);
  let checkFailure = $state<{
    id: string;
    cause: unknown;
    retry: 'recheck' | 'regenerate';
  } | null>(null);

  type Diagnosis = NonNullable<AIContextRepository['diagnosis']>;

  // What Zoomies will do about a failed run, in a sentence. "Will not" covers both
  // a cause no run can fix and one it has already tried as often as it will:
  // either way it is up to the person, and Regenerate is how to try once they have.
  function retryText(retry: Diagnosis['retry']): string {
    if (retry?.automatic && retry.next_at) {
      const left = retry.attempts_left;
      return `Zoomies will start the workflow again from ${formatAbsolute(retry.next_at)} (${left} ${left === 1 ? 'attempt' : 'attempts'} left for this commit).`;
    }
    return 'Zoomies will not start the workflow again by itself. Use Regenerate once this is fixed.';
  }

  // The run's address comes from GitHub, but a link in the page is only ever
  // followed if it is https.
  function runLink(url: string | undefined): string | undefined {
    return url?.startsWith('https://') ? url : undefined;
  }

  async function recheck(id: string) {
    checking = id;
    checkFailure = null;
    try {
      const verified = await recheckAIContext(id);
      items = items.map((item) => (item.id === id ? verified : item));
    } catch (cause) {
      checkFailure = { id, cause, retry: 'recheck' };
    } finally {
      checking = null;
    }
  }

  // Regenerating asks GitHub to run the workflow; it does not verify anything, so
  // the card keeps showing the old state until the next recheck says otherwise.
  let regenerating = $state<string | null>(null);

  async function regenerate(id: string) {
    regenerating = id;
    checkFailure = null;
    try {
      const started = await regenerateAIContext(id);
      items = items.map((item) => (item.id === id ? started : item));
      toasts.success(
        'Workflow started',
        'Recheck context once the run finishes; it usually takes a few minutes.',
      );
    } catch (cause) {
      checkFailure = { id, cause, retry: 'regenerate' };
    } finally {
      regenerating = null;
    }
  }

  $effect(() => {
    if (!installationsLoaded) return;
    const admin = canConfigure,
      q = query,
      page = offset,
      installation = installationId;
    void reload;
    const controller = new AbortController();
    loading = true;
    const timer = setTimeout(() => {
      const request = admin
        ? listAIContextRepositories(page, q, installation, controller.signal)
        : listReadableAIContext(page, q, controller.signal);
      void request
        .then((result) => {
          if (controller.signal.aborted) return;
          items = result.items;
          total = result.total;
          failure = null;
        })
        .catch((cause: unknown) => {
          if (!controller.signal.aborted) failure = cause;
        })
        .finally(() => {
          if (!controller.signal.aborted) loading = false;
        });
    }, 150);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  });
  $effect(() => {
    void reload;
    const controller = new AbortController();
    void listContextInstallations(controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) installations = result.items ?? [];
      })
      .catch(() => {
        /* Without the list this person is treated as a reader, which is the safe side. */
      })
      .finally(() => {
        if (!controller.signal.aborted) installationsLoaded = true;
      });
    return () => controller.abort();
  });
</script>

<PageHeader
  title="AI Context"
  subtitle="Spend fewer tokens loading your code. Give AI assistants the relevant source, ready after every push."
  onrefresh={() => {
    reload += 1;
  }}
>
  {#if canConfigure}<Button variant="primary" icon={Plus} href="/kennel/ai-context/setup"
      >Enable repositories</Button
    >{/if}
</PageHeader>

<section class="context-benefits" aria-label="AI Context benefits">
  <div class="benefit-grid">
    <div>
      <h2>Less token overhead</h2>
      <p>
        Assistants search and read relevant excerpts instead of loading a whole repository, leaving
        more room for the task.
      </p>
    </div>
    <div>
      <h2>Fresh, verified source</h2>
      <p>
        A GitHub workflow regenerates context after each source push. Zoomies checks it against Git
        and pins every reply to a commit.
      </p>
    </div>
    <div>
      <h2>Powered by Repomix</h2>
      <p>
        <a href={REPOMIX_URL} target="_blank" rel="noopener noreferrer">Repomix</a> packages and scans
        eligible source. Zoomies manages setup, updates and access for your assistants.
      </p>
    </div>
  </div>
  <details class="context-explainer">
    <summary>How token savings work</summary>
    <p>
      Enable a repository, review and merge its setup PR, then copy its AI instructions into your
      conversation. With MCP, assistants can search and read within a reply budget. With GitHub
      access, they can use the generated repository pack directly.
    </p>
    <p>
      For example, retrieving 5,000 tokens of context instead of loading a 100,000-token repository
      pack means 95% less repository context input. This is an illustrative example; actual savings
      depend on the task and model, and Zoomies does not currently measure token savings.
    </p>
    <p>
      The managed Repomix workflow preserves original source with code compression disabled. Savings
      come from exclusions and selective retrieval. Loading the entire snapshot still uses the full
      pack.
    </p>
    <a
      href={AI_CONTEXT_URL + '#how-it-reduces-token-usage'}
      target="_blank"
      rel="noopener noreferrer">Read the AI Context guide</a
    >
  </details>
  <details class="context-explainer">
    <summary>Use repository context without MCP</summary>
    <p>
      Choose Repository or Repository and Zoomies. GitHub Actions keeps a JSON source pack on the
      zoomies-ai-context branch. Copy the repository's AI instructions into a conversation to tell
      your assistant to check the manifest and use that pack before browsing source files.
    </p>
    <p>
      Your assistant needs GitHub access or a downloaded pack it can read. Private repositories need
      authorised GitHub access. MCP is optional; Zoomies-only output has no repository pack.
    </p>
    <a href={AI_CONTEXT_URL + '#use-without-mcp'} target="_blank" rel="noopener noreferrer"
      >Read the repository context guide</a
    >
  </details>
  <p class="access-note">
    GitHub permissions control direct repository access. Through Zoomies, source access is an
    explicit choice for each person and connection.
    <a href="/settings/connections">Manage connection access</a>
  </p>
</section>

{#if isAdmin && installations.length > 0}
  <details class="owners">
    <summary>Installation owners</summary>
    <p class="muted">
      An owner can enable repositories for an installation without being an administrator. Ownership
      never grants source access.
    </p>
    <div class="actions">
      {#each installations as installation (installation.id)}
        <Button
          size="sm"
          onclick={() => {
            ownersFor = installation;
            ownersOpen = true;
          }}>Owners of {installation.target}</Button
        >
      {/each}
    </div>
  </details>
  <OwnersDialog bind:open={ownersOpen} installation={ownersFor} />
{/if}

<div class="filters">
  <Field label="Search repositories" hideLabel>
    {#snippet children({ id, describedBy })}<Input
        {id}
        {describedBy}
        type="search"
        value={query}
        placeholder="Search repositories"
        oninput={(event) =>
          router.setQuery({ q: (event.target as HTMLInputElement).value || null, offset: null })}
      />{/snippet}
  </Field>
  {#if canConfigure}
    <Field label="Installation" hideLabel>
      {#snippet children({ id, describedBy })}<Select
          {id}
          {describedBy}
          value={installationId}
          options={[
            { value: '', label: 'All installations' },
            ...installations.map((i) => ({ value: i.id, label: i.target })),
          ]}
          onchange={(value) => router.setQuery({ installation_id: value || null, offset: null })}
        />{/snippet}
    </Field>
  {/if}
</div>

{#if loading || !installationsLoaded}<Skeleton lines={4} />
{:else if failure}<ErrorState
    error={failure}
    title="Repositories could not be loaded"
    onretry={() => {
      reload += 1;
    }}
  />
{:else if items.length === 0}
  <EmptyState
    icon={BookOpenText}
    title={query || installationId
      ? 'No matching repositories'
      : canConfigure
        ? 'Prepare your first repository'
        : 'No shared repositories yet'}
    description={query || installationId
      ? 'Try another search or clear the filters to see all repositories.'
      : canConfigure
        ? 'Choose repositories, output and source readers. Save your setup drafts, then review repository changes before context is published.'
        : 'Repositories appear here only after an administrator or installation owner makes them available and explicitly adds you as a source reader.'}
  >
    {#if query || installationId}<Button
        onclick={() => router.setQuery({ q: null, installation_id: null, offset: null })}
        >Clear filters</Button
      >{:else if canConfigure}<Button variant="primary" href="/kennel/ai-context/setup"
        >Enable repositories</Button
      >{:else}<Button href="/settings/connections">MCP connections</Button>{/if}
  </EmptyState>
{:else}
  <div class="repositories">
    {#each items as item (item.id)}
      <section class="repository" aria-label={item.full_name}>
        <div class="heading">
          <h2>{item.full_name}</h2>
          <div class="badges">
            <Badge
              status={aiContextStatus(
                'config' in item
                  ? item.freshness?.state
                    ? item.freshness.state
                    : item.available
                      ? 'available'
                      : item.setup_state === 'awaiting_merge'
                        ? 'awaiting_merge'
                        : item.setup_state === 'pending'
                          ? 'working'
                          : 'draft'
                  : 'available',
              )}
              label={'config' in item
                ? item.config.disabled
                  ? 'Removed from Zoomies'
                  : undefined
                : 'Shared with you'}
            />
            {#if 'config' in item && item.workflow_outdated && !item.config.disabled}<Badge
                tone="accent"
                label="Workflow out of date"
                title="The workflow was written by an earlier Zoomies release. It still works; Reinstall / repair moves it to the current generator."
              />{/if}
          </div>
        </div>
        {#if 'config' in item}
          <dl>
            <div>
              <dt>Source branch</dt>
              <dd>{item.config.source_branch}</dd>
            </div>
            <div>
              <dt>Output</dt>
              <dd>
                {item.config.destination === 'both'
                  ? 'Repository and Zoomies'
                  : item.config.destination === 'repository'
                    ? 'Repository'
                    : 'Zoomies'}
              </dd>
            </div>
            <div>
              <dt>Installation</dt>
              <dd>
                {installations.find((i) => i.id === item.repository.installation_id)?.target ??
                  item.repository.installation_id}
              </dd>
            </div>
          </dl>
          {#if item.freshness}
            <dl>
              <div>
                <dt>Last checked</dt>
                <dd>{formatAbsolute(item.freshness.checked_at)}</dd>
              </div>
              {#if item.freshness.published_commit}<div>
                  <dt>Last verified commit</dt>
                  <dd><code>{item.freshness.published_commit.slice(0, 12)}</code></dd>
                </div>{/if}
            </dl>
            {#if item.diagnosis}
              <div class="diagnosis" role="group" aria-label="Why the last workflow run failed">
                <h3>{item.diagnosis.title}</h3>
                <p>{item.diagnosis.detail}</p>
                <p><strong>What to do.</strong> {item.diagnosis.fix}</p>
                <p class="muted">{retryText(item.diagnosis.retry)}</p>
                {#if runLink(item.diagnosis.run_url)}<Button
                    size="sm"
                    newTab
                    href={runLink(item.diagnosis.run_url)}>Open the failed run</Button
                  >{/if}
              </div>
            {:else if item.freshness.failure}<p class="verification-failure">
                {item.freshness.failure}
              </p>{/if}
          {/if}
          {#if checkFailure?.id === item.id}<ErrorState
              error={checkFailure.cause}
              title={checkFailure.retry === 'regenerate'
                ? 'The workflow could not be started'
                : 'Verification could not finish'}
              onretry={() =>
                checkFailure?.retry === 'regenerate' ? regenerate(item.id) : recheck(item.id)}
            />{/if}
          <div class="actions">
            {#if item.setup_state === 'awaiting_merge'}<Button
                size="sm"
                loading={checking === item.id}
                disabled={checking !== null || regenerating !== null}
                onclick={() => recheck(item.id)}>Recheck context</Button
              >{/if}
            {#if item.setup_state === 'awaiting_merge' && item.freshness && item.freshness.state !== 'awaiting_merge'}<Button
                size="sm"
                loading={regenerating === item.id}
                disabled={regenerating !== null || checking !== null}
                onclick={() => regenerate(item.id)}>Regenerate</Button
              >{/if}
            {#if item.setup_pr_url}<Button size="sm" newTab href={item.setup_pr_url}
                >Open setup PR</Button
              >{/if}
            <Button size="sm" href="/kennel/ai-context/setup?draft_id={encodeURIComponent(item.id)}"
              >{item.setup_state ? 'View setup' : 'Resume setup'}</Button
            >
            {#if item.setup_state}
              {#each ['reinstall', 'amend', 'remove'] as mode (mode)}
                <Button
                  size="sm"
                  variant={(mode === 'reinstall' &&
                    (item.workflow_outdated || item.diagnosis?.action === 'repair')) ||
                  (mode === 'amend' && item.diagnosis?.action === 'exclusions')
                    ? 'primary'
                    : 'secondary'}
                  href="/kennel/ai-context/setup?draft_id={encodeURIComponent(item.id)}&mode={mode}"
                  >{mode === 'reinstall'
                    ? 'Reinstall / repair'
                    : mode === 'amend'
                      ? 'Amend'
                      : 'Remove'}</Button
                >
              {/each}
            {/if}
          </div>
        {:else}<Button size="sm" href="/settings/connections">Choose connection access</Button>{/if}
        {#if item.instructions}
          <div class="actions assistant-prompt">
            <CopyButton value={item.instructions} label="Copy AI instructions" showLabel />
          </div>
        {/if}
        {#if !('config' in item) || item.available}<NotesPanel repositoryId={item.id} />{/if}
        <details class="assistant-guidance">
          <summary>AI instructions and README badge</summary>
          <p>
            Paste the copied instructions into your AI conversation. Repository output works through
            GitHub access without MCP; Zoomies-only output needs a connected assistant. The
            instructions explain which route is available and how to check freshness.
          </p>
          <div class="actions">
            {#if item.badge_markdown}<CopyButton
                value={item.badge_markdown}
                label="Copy badge Markdown"
                showLabel
              />{/if}
          </div>
          {#if item.badge_markdown}
            <p class="muted">
              The Zoomies AI Context badge shows workflow status, not context freshness. Private
              badges require GitHub access.
            </p>
          {/if}
          {#if item.instructions}<details>
              <summary>Preview AI instructions</summary>
              <pre>{item.instructions}</pre>
            </details>{/if}
        </details>
      </section>
    {/each}
  </div>
  <nav class="paging" aria-label="AI Context repository pages">
    <Button
      size="sm"
      disabled={offset === 0}
      onclick={() => router.setQuery({ offset: String(Math.max(0, offset - 50)) })}>Previous</Button
    ><span>{offset + 1}–{Math.min(offset + 50, total)} of {total}</span><Button
      size="sm"
      disabled={offset + 50 >= total}
      onclick={() => router.setQuery({ offset: String(offset + 50) })}>Next</Button
    >
  </nav>
{/if}

<style>
  .context-benefits {
    margin-bottom: var(--z-space-5);
    padding: var(--z-space-5);
    background: var(--z-surface);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    overflow-wrap: anywhere;
  }
  .benefit-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(min(100%, 240px), 1fr));
    gap: var(--z-space-5);
  }
  .benefit-grid > div {
    min-width: 0;
  }
  .context-benefits p {
    margin: var(--z-space-2) 0 0;
    color: var(--z-text-muted);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
  }
  .context-benefits a {
    color: var(--z-accent);
    text-decoration: underline;
    text-underline-offset: var(--z-underline-offset);
  }
  .context-explainer {
    margin-top: var(--z-space-4);
    padding-top: var(--z-space-4);
    border-top: var(--z-border-width) solid var(--z-border);
    font-size: var(--z-text-sm);
  }
  .context-explainer summary {
    cursor: pointer;
    font-weight: var(--z-weight-medium);
  }
  .context-explainer > a {
    display: inline-block;
    margin-top: var(--z-space-3);
  }
  .context-benefits .access-note {
    margin-top: var(--z-space-4);
  }
  .owners {
    margin-bottom: var(--z-space-4);
  }
  .owners summary {
    cursor: pointer;
  }
  .assistant-prompt {
    margin-top: var(--z-space-4);
  }
  .assistant-guidance {
    margin-top: var(--z-space-4);
  }
  .assistant-guidance summary {
    cursor: pointer;
  }
  .assistant-guidance pre {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    font-size: var(--z-text-sm);
  }
  .badges {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
  }
  .diagnosis {
    margin-bottom: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-4);
    background: var(--z-surface-sunken);
    border-left: var(--z-border-width-rail) solid var(--z-danger-border);
    border-radius: var(--z-radius-sm);
    font-size: var(--z-text-sm);
  }
  .diagnosis h3 {
    margin: 0 0 var(--z-space-2);
    font-size: var(--z-text-base);
  }
  .diagnosis p {
    margin: 0 0 var(--z-space-2);
  }
  .verification-failure {
    color: var(--z-danger);
    font-size: var(--z-text-sm);
    margin-bottom: var(--z-space-3);
  }
  .filters {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-3);
    margin-bottom: var(--z-space-5);
  }
  .filters :global(.field) {
    flex: 1;
    min-width: min(100%, 240px);
  }
  .repositories {
    display: grid;
    gap: var(--z-space-4);
  }
  .repository {
    min-width: 0;
    background: var(--z-surface);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    padding: var(--z-space-5);
    overflow-wrap: anywhere;
  }
  .heading,
  .paging {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-3);
  }
  h2 {
    min-width: 0;
    margin: 0;
    font-size: var(--z-text-base);
  }
  dl {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-5);
    margin: var(--z-space-4) 0;
  }
  dt {
    color: var(--z-text-subtle);
    font-size: var(--z-text-xs);
  }
  dd {
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-sm);
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--z-space-3);
    flex-wrap: wrap;
  }
  /* Right-aligned buttons that wrap leave each row ragged on the left and
     the first one floating; on a phone they read as a list from the edge. */
  @media (max-width: 768px) {
    .actions {
      justify-content: flex-start;
    }
  }
  .paging {
    margin-top: var(--z-space-5);
  }
</style>
