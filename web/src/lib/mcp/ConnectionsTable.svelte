<!--
  MCP connections: which client, whose, at what role, and when it was last
  used. The account page shows a person their own; the MCP clients page shows
  an administrator everybody's, with the owner as a column.
-->
<script lang="ts">
  import type { MCPConnection } from '$lib/api/types';
  import { roleLabel } from '$lib/roles';
  import Button from '$lib/components/Button.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import CardTable from './CardTable.svelte';

  interface Props {
    items: MCPConnection[];
    /** Show whose each connection is: the administrator's view. */
    showUser?: boolean;
    caption: string;
    ondisconnect: (connection: MCPConnection) => void;
  }

  let { items, showUser = false, caption, ondisconnect }: Props = $props();
</script>

<CardTable>
  <!-- svelte-ignore a11y_no_redundant_roles -->
  <table role="table">
    <caption class="sr-only">{caption}</caption>
    <!-- svelte-ignore a11y_no_redundant_roles -->
    <thead role="rowgroup">
      <!-- svelte-ignore a11y_no_redundant_roles -->
      <tr role="row">
        <th role="columnheader" scope="col">Client</th>
        {#if showUser}<th role="columnheader" scope="col">Person</th>{/if}
        <th role="columnheader" scope="col">Role</th>
        <th role="columnheader" scope="col">Connected</th>
        <th role="columnheader" scope="col">Last used</th>
        <th role="columnheader" scope="col"><span class="sr-only">Actions</span></th>
      </tr>
    </thead>
    <!-- svelte-ignore a11y_no_redundant_roles -->
    <tbody role="rowgroup">
      {#each items as connection (connection.id)}
        <tr role="row">
          <td role="cell" data-label="Client" class="name">{connection.client_name}</td>
          {#if showUser}<td role="cell" data-label="Person">{connection.username}</td>{/if}
          <td role="cell" data-label="Role">{roleLabel(connection.role)}</td>
          <td role="cell" data-label="Connected"><RelativeTime value={connection.created_at} /></td>
          <td role="cell" data-label="Last used">
            {#if connection.last_used_at}
              <RelativeTime value={connection.last_used_at} />
            {:else}
              <span class="muted">Not yet</span>
            {/if}
          </td>
          <td role="cell" data-label="Actions" class="actions">
            <Button size="sm" variant="ghost" onclick={() => ondisconnect(connection)}>
              Disconnect
            </Button>
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
</CardTable>

<style>
  .name {
    font-weight: var(--z-weight-medium);
  }
  .muted {
    color: var(--z-text-subtle);
  }
</style>
