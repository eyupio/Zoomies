<!--
  Your MCP connections.

  Every client you have signed in and approved -- Claude on claude.ai, in the
  desktop app, in Claude Code -- with the role you gave it. Disconnecting is
  immediate: the client's next call is refused and it has to ask you again.
-->
<script lang="ts">
  import { Cable } from '@lucide/svelte';
  import { listOwnMCPConnections, revokeOwnMCPConnection } from '$lib/api/client';
  import type { MCPConnection } from '$lib/api/types';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import LoadingBoundary from '$lib/components/LoadingBoundary.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import ConnectionsTable from '$lib/mcp/ConnectionsTable.svelte';

  let items = $state<MCPConnection[]>([]);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let reload = $state(0);

  $effect(() => {
    void reload;
    const controller = new AbortController();
    loading = true;
    void listOwnMCPConnections(controller.signal)
      .then((result) => {
        items = result.items ?? [];
        error = null;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        error = cause;
      })
      .finally(() => (loading = false));
    return () => controller.abort();
  });

  let confirmOpen = $state(false);
  let ending = $state<MCPConnection | null>(null);

  async function disconnect(): Promise<boolean> {
    const c = ending;
    if (!c) return false;
    try {
      await revokeOwnMCPConnection(c.id);
      toasts.success(`${c.client_name} disconnected`, 'Its next call is refused.');
      reload += 1;
      return true;
    } catch (cause) {
      toasts.fromError(cause, 'That connection was not ended');
      return false;
    }
  }

  const mcpAddress = $derived(`${location.origin}/mcp`);
</script>

<PageHeader
  title="MCP connections"
  subtitle="Claude and other MCP clients you have signed in to this controller. Each works on /mcp only, at the role you chose."
  onrefresh={() => {
    reload += 1;
  }}
/>

{#if session.identity?.kind !== 'user' || session.authDisabled}
  <EmptyState
    icon={Cable}
    title="Connections belong to a person"
    description="Only a person signed in to this controller has MCP connections: an API token has none, and neither does a controller with authentication switched off."
  />
{:else}
  <LoadingBoundary
    {loading}
    {error}
    empty={!loading && !error && items.length === 0}
    onretry={() => (reload += 1)}
  >
    {#snippet skeleton()}
      <Skeleton lines={3} />
    {/snippet}
    {#snippet emptyState()}
      <EmptyState
        icon={Cable}
        title="No MCP connections"
        description="Add {mcpAddress} as a custom connector in Claude, or run claude mcp add --transport http zoomies {mcpAddress}, and approve it when Claude sends you here."
      />
    {/snippet}
    <ConnectionsTable
      {items}
      caption="Your MCP connections"
      ondisconnect={(c) => {
        ending = c;
        confirmOpen = true;
      }}
    />
  </LoadingBoundary>
{/if}

<ConfirmDialog
  bind:open={confirmOpen}
  title="Disconnect"
  name={ending?.client_name}
  description="{ending?.client_name ?? 'This client'} stops working with Zoomies immediately."
  consequences={['Its next call is refused, and it will ask you to sign in again to reconnect.']}
  confirmLabel="Disconnect"
  onconfirm={disconnect}
  oncancel={() => (ending = null)}
/>
