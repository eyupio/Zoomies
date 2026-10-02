<script lang="ts">
  import { BookOpenText, Plus } from '@lucide/svelte';
  import {
    listAIContextRepositories,
    listReadableAIContext,
    listInstallations,
  } from '$lib/api/client';
  import type { AIContextRepository, Installation } from '$lib/api/types';
  import { router } from '$lib/router';
  import { session } from '$lib/state/session.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Select from '$lib/components/Select.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';

  const canConfigure = $derived(session.can('admin'));
  const query = $derived(router.param('q'));
  const installationId = $derived(router.param('installation_id'));
  const offset = $derived(Math.max(0, Number(router.param('offset')) || 0));
  let items = $state<(AIContextRepository | { id: string; full_name: string })[]>([]);
  type KnownInstallation = Installation & { id: string; target: string };
  let installations = $state<KnownInstallation[]>([]);
  let total = $state(0);
  let loading = $state(true);
  let failure = $state<unknown>(null);
  let reload = $state(0);

  $effect(() => {
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
    if (!canConfigure) return;
    const controller = new AbortController();
    void listInstallations(controller.signal)
      .then((result) => {
        if (!controller.signal.aborted)
          installations = (result.items ?? []).filter(
            (i): i is KnownInstallation => !!i.id && !!i.target,
          );
      })
      .catch(() => {
        /* The configuration list remains usable without installation labels. */
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

{#if loading}<Skeleton lines={4} />
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
    title={query || installationId ? 'No matching repositories' : 'Prepare your first repository'}
    description={canConfigure
      ? 'Choose repositories, output and source readers. Save your setup drafts, then review repository changes before context is published.'
      : 'Repositories appear here only after an administrator makes them available and explicitly adds you as a source reader.'}
  >
    {#if canConfigure}<Button variant="primary" href="/ai-context/setup">Enable repositories</Button
      >{:else}<Button href="/settings/connections">MCP connections</Button>{/if}
  </EmptyState>
{:else}
  <div class="repositories">
    {#each items as item (item.id)}
      <section class="repository" aria-label={item.full_name}>
        <div class="heading">
          <h2>{item.full_name}</h2>
          <Badge
            label={'config' in item
              ? item.available
                ? 'Source access available'
                : item.setup_state === 'awaiting_merge'
                  ? 'Awaiting merge'
                  : item.setup_state === 'pending'
                    ? 'Setup pending'
                    : 'Draft'
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
          <div class="actions">
            {#if item.setup_pr_url}<Button size="sm" href={item.setup_pr_url}>Open setup PR</Button
              >{/if}
            <Button size="sm" href="/ai-context/setup?draft_id={encodeURIComponent(item.id)}"
              >{item.setup_state ? 'View setup' : 'Resume setup'}</Button
            >
          </div>
        {:else}<Button size="sm" href="/settings/connections">Choose connection access</Button>{/if}
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
