<script lang="ts">
  import { BookOpenText, Plus } from '@lucide/svelte';
  import {
    listAIContextRepositories,
    listReadableAIContext,
    listContextInstallations,
    recheckAIContext,
  } from '$lib/api/client';
  import type { AIContextRepository } from '$lib/api/types';
  import { router } from '$lib/router';
  import { session } from '$lib/state/session.svelte';
  import { formatAbsolute } from '$lib/format';
  import { aiContextStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
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
  let checkFailure = $state<{ id: string; cause: unknown } | null>(null);

  async function recheck(id: string) {
    checking = id;
    checkFailure = null;
    try {
      const verified = await recheckAIContext(id);
      items = items.map((item) => (item.id === id ? verified : item));
    } catch (cause) {
      checkFailure = { id, cause };
    } finally {
      checking = null;
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
  subtitle="Prepare repositories for AI coding assistants. Source access is an explicit choice for each person and connection."
  onrefresh={() => {
    reload += 1;
  }}
>
  {#if canConfigure}<Button variant="primary" icon={Plus} href="/ai-context/setup"
      >Enable repositories</Button
    >{/if}
</PageHeader>

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
      >{:else if canConfigure}<Button variant="primary" href="/ai-context/setup"
        >Enable repositories</Button
      >{:else}<Button href="/settings/connections">MCP connections</Button>{/if}
  </EmptyState>
{:else}
  <div class="repositories">
    {#each items as item (item.id)}
      <section class="repository" aria-label={item.full_name}>
        <div class="heading">
          <h2>{item.full_name}</h2>
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
            {#if item.freshness.failure}<p class="verification-failure">
                {item.freshness.failure}
              </p>{/if}
          {/if}
          {#if checkFailure?.id === item.id}<ErrorState
              error={checkFailure.cause}
              title="Verification could not finish"
              onretry={() => recheck(item.id)}
            />{/if}
          <div class="actions">
            {#if item.setup_state === 'awaiting_merge'}<Button
                size="sm"
                loading={checking === item.id}
                disabled={checking !== null}
                onclick={() => recheck(item.id)}>Recheck context</Button
              >{/if}
            {#if item.setup_pr_url}<Button size="sm" newTab href={item.setup_pr_url}
                >Open setup PR</Button
              >{/if}
            <Button size="sm" href="/ai-context/setup?draft_id={encodeURIComponent(item.id)}"
              >{item.setup_state ? 'View setup' : 'Resume setup'}</Button
            >
            {#if item.setup_state}
              {#each ['reinstall', 'amend', 'remove'] as mode (mode)}
                <Button
                  size="sm"
                  href="/ai-context/setup?draft_id={encodeURIComponent(item.id)}&mode={mode}"
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
        <details class="assistant-guidance">
          <summary>AI instructions and README badge</summary>
          <p>
            Copy these instructions into any AI prompt. Check that generation and verification have
            completed before relying on context.
          </p>
          <div class="actions">
            {#if item.instructions}<CopyButton
                value={item.instructions}
                label="Copy AI instructions"
                showLabel
              />{/if}
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
  .owners {
    margin-bottom: var(--z-space-4);
  }
  .owners summary {
    cursor: pointer;
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
  .paging {
    margin-top: var(--z-space-5);
  }
</style>
