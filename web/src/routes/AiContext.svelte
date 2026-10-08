<script lang="ts">
  import { BookOpenText, Plus } from '@lucide/svelte';
  import {
    listAIContextRepositories,
    listReadableAIContext,
    listContextInstallations,
  } from '$lib/api/client';
  import { router } from '$lib/router';
  import { session } from '$lib/state/session.svelte';
  import { AI_CONTEXT_URL, REPOMIX_URL } from '$lib/links';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import KennelShell from '$lib/kennel/KennelShell.svelte';
  import AiContextCard from '$lib/aicontext/AiContextCard.svelte';
  import OwnersDialog from '$lib/aicontext/OwnersDialog.svelte';
  import type { AiContextItem, KnownInstallation } from '$lib/aicontext/types';
  import Select from '$lib/components/Select.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';

  const isAdmin = $derived(session.can('admin'));
  const query = $derived(router.param('q'));
  const installationId = $derived(router.param('installation_id'));
  const offset = $derived(Math.max(0, Number(router.param('offset')) || 0));
  let items = $state<AiContextItem[]>([]);
  // The installations this person may enable repositories for: all of them for
  // an administrator, otherwise only the ones an administrator made them owner of.
  let installations = $state<KnownInstallation[]>([]);
  let installationsLoaded = $state(false);
  let ownersFor = $state<KnownInstallation | null>(null);
  let ownersOpen = $state(false);
  const canConfigure = $derived(isAdmin || installations.length > 0);
  let total = $state(0);
  let loading = $state(true);
  let failure = $state<unknown>(null);
  let reload = $state(0);

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

<KennelShell current="ai-context">
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
          Assistants search and read relevant excerpts instead of loading a whole repository,
          leaving more room for the task.
        </p>
      </div>
      <div>
        <h2>Fresh, verified source</h2>
        <p>
          A GitHub workflow regenerates context after each source push. Zoomies checks it against
          Git and pins every reply to a commit.
        </p>
      </div>
      <div>
        <h2>Powered by Repomix</h2>
        <p>
          <a href={REPOMIX_URL} target="_blank" rel="noopener noreferrer"
            >Repomix<span class="sr-only"> (opens in a new tab)</span></a
          > packages and scans eligible source. Zoomies manages setup, updates and access for your assistants.
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
        For example, retrieving 5,000 tokens of context instead of loading a 100,000-token
        repository pack means 95% less repository context input. This is an illustrative example;
        actual savings depend on the task and model, and Zoomies does not currently measure token
        savings.
      </p>
      <p>
        The managed Repomix workflow preserves original source with code compression disabled.
        Savings come from exclusions and selective retrieval. Loading the entire snapshot still uses
        the full pack.
      </p>
      <a
        href={AI_CONTEXT_URL + '#how-it-reduces-token-usage'}
        target="_blank"
        rel="noopener noreferrer"
        >Read the AI Context guide<span class="sr-only"> (opens in a new tab)</span></a
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
        Your assistant needs GitHub access or a downloaded pack it can read. Private repositories
        need authorised GitHub access. MCP is optional; Zoomies-only output has no repository pack.
      </p>
      <a href={AI_CONTEXT_URL + '#use-without-mcp'} target="_blank" rel="noopener noreferrer"
        >Read the repository context guide<span class="sr-only"> (opens in a new tab)</span></a
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
        An owner can enable repositories for an installation without being an administrator.
        Ownership never grants source access.
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
        <AiContextCard
          {item}
          {installations}
          onchange={(updated) => (items = items.map((i) => (i.id === updated.id ? updated : i)))}
        />
      {/each}
    </div>
    <nav class="paging" aria-label="AI Context repository pages">
      <Button
        size="sm"
        disabled={offset === 0}
        onclick={() => router.setQuery({ offset: String(Math.max(0, offset - 50)) })}
        >Previous</Button
      ><span>{offset + 1}–{Math.min(offset + 50, total)} of {total}</span><Button
        size="sm"
        disabled={offset + 50 >= total}
        onclick={() => router.setQuery({ offset: String(offset + 50) })}>Next</Button
      >
    </nav>
  {/if}
</KennelShell>

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
  .paging {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-3);
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
