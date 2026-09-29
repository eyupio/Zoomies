<!--
  MCP clients, and everybody's connections.

  Most controllers never need a client made here: Claude registers itself when
  it connects. This page is for the two cases that do -- a controller with
  open registration turned off, and an administrator who would rather Claude
  used a client they named and can revoke by name. A confidential client's
  secret is shown once, in the same one-time block an API token uses.
-->
<script lang="ts">
  import { Cable, Plug, Plus } from '@lucide/svelte';
  import {
    ApiError,
    createMCPClient,
    listMCPClients,
    listMCPConnections,
    revokeMCPClient,
    revokeMCPConnection,
    rotateMCPClientSecret,
  } from '$lib/api/client';
  import type { MCPClient, MCPConnection } from '$lib/api/types';
  import { toasts } from '$lib/state/toasts.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import LoadingBoundary from '$lib/components/LoadingBoundary.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import Textarea from '$lib/components/Textarea.svelte';
  import CardTable from '$lib/mcp/CardTable.svelte';
  import ConnectionsTable from '$lib/mcp/ConnectionsTable.svelte';
  import OneTimeSecret from './OneTimeSecret.svelte';

  const CLAUDE_CALLBACK = 'https://claude.ai/api/mcp/auth_callback';

  const KIND_LABEL: Record<string, string> = {
    admin: 'Created here',
    dynamic: 'Registered itself',
    metadata: 'Metadata document',
  };

  let clients = $state<MCPClient[]>([]);
  let connections = $state<MCPConnection[]>([]);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let reload = $state(0);

  $effect(() => {
    void reload;
    const controller = new AbortController();
    loading = true;
    void Promise.all([listMCPClients(controller.signal), listMCPConnections(controller.signal)])
      .then(([c, g]) => {
        clients = c.items ?? [];
        connections = g.items ?? [];
        error = null;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        error = cause;
      })
      .finally(() => (loading = false));
    return () => controller.abort();
  });

  /* -- create and rotate ---------------------------------------------------- */

  let createOpen = $state(false);
  let name = $state('');
  let redirectText = $state(CLAUDE_CALLBACK);
  let confidential = $state(true);
  let creating = $state(false);
  let errors = $state<Record<string, string>>({});
  /** A client whose secret -- new or rotated -- is on screen for the one time it exists. */
  let shown = $state<MCPClient | null>(null);

  function open(): void {
    name = 'Claude';
    redirectText = CLAUDE_CALLBACK;
    confidential = true;
    errors = {};
    shown = null;
    createOpen = true;
  }

  const redirects = $derived(
    redirectText
      .split(/[\s,]+/)
      .map((s) => s.trim())
      .filter(Boolean),
  );

  async function create(): Promise<void> {
    if (!name.trim()) return;
    creating = true;
    errors = {};
    try {
      shown = await createMCPClient({ name: name.trim(), redirect_uris: redirects, confidential });
      reload += 1;
    } catch (cause) {
      if (cause instanceof ApiError) errors = cause.fieldErrors();
      toasts.fromError(cause, 'That client was not created');
    } finally {
      creating = false;
    }
  }

  /* Rotating is not undone by closing the dialog: the old secret stops
     working the moment the new one exists, so every client configured with
     it fails its next refresh until somebody pastes the new one in. That is
     worth one confirmation, the same as revoking. */
  let rotateOpen = $state(false);
  let rotating = $state<MCPClient | null>(null);

  async function rotate(): Promise<boolean> {
    const client = rotating;
    if (!client) return false;
    try {
      shown = await rotateMCPClientSecret(client.id);
      createOpen = true;
      reload += 1;
      return true;
    } catch (cause) {
      toasts.fromError(cause, 'The secret was not rotated');
      return false;
    }
  }

  /* -- revoke --------------------------------------------------------------- */

  let revokeOpen = $state(false);
  let revoking = $state<MCPClient | null>(null);

  async function revoke(): Promise<boolean> {
    const c = revoking;
    if (!c) return false;
    try {
      await revokeMCPClient(c.id);
      toasts.success(`${c.name} revoked`, 'Every connection made with it has ended.');
      reload += 1;
      return true;
    } catch (cause) {
      toasts.fromError(cause, 'That client was not revoked');
      return false;
    }
  }

  let endOpen = $state(false);
  let ending = $state<MCPConnection | null>(null);

  async function endConnection(): Promise<boolean> {
    const c = ending;
    if (!c) return false;
    try {
      await revokeMCPConnection(c.id);
      toasts.success(`${c.username}'s ${c.client_name} disconnected`);
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
  title="MCP clients"
  subtitle="The OAuth clients that may ask somebody to connect them to /mcp. Claude registers itself; make one here to hand Claude a client ID instead."
  onrefresh={() => {
    reload += 1;
  }}
>
  <Button variant="primary" icon={Plus} onclick={open}>Create a client</Button>
</PageHeader>

<LoadingBoundary {loading} {error} onretry={() => (reload += 1)}>
  {#snippet skeleton()}
    <Skeleton lines={4} />
  {/snippet}

  <section class="block" aria-labelledby="clients-heading">
    <h2 id="clients-heading">Clients</h2>
    {#if clients.length === 0}
      <EmptyState
        icon={Plug}
        title="No MCP clients yet"
        description="When somebody adds {mcpAddress} to Claude, the client it registers is listed here. Create one to give Claude a client ID and secret instead."
      />
    {:else}
      <CardTable>
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <table role="table">
          <caption class="sr-only">MCP clients</caption>
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <thead role="rowgroup">
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <tr role="row">
              <th role="columnheader" scope="col">Name</th>
              <th role="columnheader" scope="col">Client ID</th>
              <th role="columnheader" scope="col">Kind</th>
              <th role="columnheader" scope="col">Connections</th>
              <th role="columnheader" scope="col">Last used</th>
              <th role="columnheader" scope="col"><span class="sr-only">Actions</span></th>
            </tr>
          </thead>
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <tbody role="rowgroup">
            {#each clients as client (client.id)}
              <tr role="row" class:ended={client.revoked_at}>
                <td role="cell" data-label="Name" class="name">{client.name}</td>
                <td role="cell" data-label="Client ID" class="mono small">{client.client_id}</td>
                <td role="cell" data-label="Kind">
                  {KIND_LABEL[client.kind] ?? client.kind}
                  {#if client.confidential}<span class="second">With a secret</span>{/if}
                </td>
                <td role="cell" data-label="Connections">
                  {#if client.revoked_at}
                    <Badge tone="neutral" label="Revoked" size="sm" />
                  {:else}
                    {client.connections}
                  {/if}
                </td>
                <td role="cell" data-label="Last used">
                  {#if client.last_used_at}
                    <RelativeTime value={client.last_used_at} />
                  {:else}
                    <span class="muted">Never</span>
                  {/if}
                </td>
                <td role="cell" data-label="Actions" class="actions">
                  {#if !client.revoked_at}
                    {#if client.kind === 'admin' && client.confidential}
                      <Button
                        size="sm"
                        variant="ghost"
                        onclick={() => {
                          rotating = client;
                          rotateOpen = true;
                        }}
                      >
                        Rotate secret
                      </Button>
                    {/if}
                    <Button
                      size="sm"
                      variant="ghost"
                      onclick={() => {
                        revoking = client;
                        revokeOpen = true;
                      }}
                    >
                      Revoke
                    </Button>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </CardTable>
    {/if}
  </section>

  <section class="block" aria-labelledby="connections-heading">
    <h2 id="connections-heading">Everybody's connections</h2>
    {#if connections.length === 0}
      <EmptyState
        icon={Cable}
        title="Nobody has connected an MCP client"
        description="Each person sees and ends their own under Settings, MCP connections."
      />
    {:else}
      <ConnectionsTable
        items={connections}
        showUser
        caption="Everybody's MCP connections"
        ondisconnect={(c) => {
          ending = c;
          endOpen = true;
        }}
      />
    {/if}
  </section>
</LoadingBoundary>

<!--
  A secret is shown exactly once, so a stray click on the scrim must not lose it.
  A public client has only its ID, which can be looked up again, and stays
  dismissible.
-->
<Dialog
  bind:open={createOpen}
  title={shown ? `${shown.name}` : 'Create an MCP client'}
  description={shown
    ? 'Put these in Claude’s Add custom connector dialog, under Advanced settings.'
    : 'A client ID to type into an MCP client’s OAuth settings, such as Claude’s custom connector form.'}
  size="md"
  dismissible={!shown?.client_secret}
>
  {#if shown}
    <div class="form">
      <OneTimeSecret what="client ID" value={shown.client_id} copyLabel="Copy the client ID" />
      {#if shown.client_secret}
        <OneTimeSecret
          what="client secret"
          value={shown.client_secret}
          copyLabel="Copy the client secret"
          note="This is the only time it is shown. Rotate it here if it is lost."
        />
      {/if}
      <p class="usage">
        The connector's URL is <span class="mono">{mcpAddress}</span>. Claude's OAuth Client ID and
        OAuth Client Secret fields are under Advanced settings.
      </p>
    </div>
  {:else}
    <form
      id="mcp-client-form"
      class="form"
      onsubmit={(e) => {
        e.preventDefault();
        void create();
      }}
    >
      <Field
        label="Name"
        hint="What people see when they are asked to approve it."
        error={errors.name}
      >
        {#snippet children({ id, describedBy, invalid })}
          <Input bind:value={name} {id} {describedBy} {invalid} autocomplete="off" />
        {/snippet}
      </Field>
      <Field
        label="Redirect URIs"
        hint="One per line. Claude's apps return to {CLAUDE_CALLBACK}; add http://localhost/callback for Claude Code, whose port may differ."
        error={errors.redirect_uris}
      >
        {#snippet children({ id, describedBy, invalid })}
          <Textarea bind:value={redirectText} {id} {describedBy} {invalid} rows={3} mono />
        {/snippet}
      </Field>
      <Switch
        bind:checked={confidential}
        label="Give it a secret"
        description="Claude presents it as well as PKCE when it exchanges a code. Off makes a public client, identified by its ID alone."
      />
    </form>
  {/if}

  {#snippet footer()}
    {#if shown}
      <Button variant="primary" onclick={() => (createOpen = false)}>Done</Button>
    {:else}
      <Button variant="ghost" onclick={() => (createOpen = false)}>Cancel</Button>
      <Button
        variant="primary"
        type="submit"
        form="mcp-client-form"
        loading={creating}
        disabled={!name.trim()}
      >
        Create client
      </Button>
    {/if}
  {/snippet}
</Dialog>

<ConfirmDialog
  bind:open={rotateOpen}
  title="Rotate secret"
  name={rotating?.name}
  description="{rotating?.name ??
    'This client'} gets a new secret, shown once. The old one stops working now."
  consequences={[
    `Anything still holding the old secret cannot sign anybody in or refresh a connection until it is given the new one${rotating?.connections ? ` — ${rotating.connections} connection${rotating.connections === 1 ? '' : 's'} will stop at their next refresh` : ''}.`,
  ]}
  confirmLabel="Rotate"
  onconfirm={rotate}
  oncancel={() => (rotating = null)}
/>

<ConfirmDialog
  bind:open={revokeOpen}
  title="Revoke client"
  name={revoking?.name}
  description="{revoking?.name ?? 'This client'} can no longer ask anybody to connect."
  consequences={[
    `Every connection made with it ends now${revoking?.connections ? ` — ${revoking.connections} of them` : ''}.`,
  ]}
  confirmLabel="Revoke"
  onconfirm={revoke}
  oncancel={() => (revoking = null)}
/>

<ConfirmDialog
  bind:open={endOpen}
  title="End connection"
  name={ending?.client_name}
  description="{ending?.username ?? 'Their'}’s {ending?.client_name ??
    'client'} stops working with Zoomies immediately."
  consequences={['They can connect it again by signing in from the client.']}
  confirmLabel="End it"
  onconfirm={endConnection}
  oncancel={() => (ending = null)}
/>

<style>
  .block {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    margin-bottom: var(--z-space-8);
  }
  h2 {
    margin: 0;
    font-size: var(--z-text-lg);
    line-height: var(--z-leading-lg);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .name {
    font-weight: var(--z-weight-medium);
  }
  .mono {
    font-family: var(--z-font-mono);
  }
  .small {
    font-size: var(--z-text-xs);
  }
  .second {
    display: block;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .muted {
    color: var(--z-text-subtle);
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    padding-bottom: var(--z-space-2);
  }
  .usage {
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
</style>
