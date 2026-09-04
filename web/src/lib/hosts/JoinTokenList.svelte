<!--
  Join tokens that have been minted and not yet used.

  A join token is a credential that turns any machine into a host, so leaving
  spent or forgotten ones lying about is worth being able to see. Only the
  prefix is ever shown -- the secret existed once, in the dialog that made it.
-->
<script lang="ts">
  import { Trash2 } from '@lucide/svelte';
  import { deleteJoinToken } from '$lib/api/client';
  import type { JoinToken } from '$lib/api/types';
  import { formatNumber } from '$lib/format';
  import { toasts } from '$lib/state/toasts.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import IconButton from '$lib/components/IconButton.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';

  interface Props {
    tokens: readonly JoinToken[];
    onrevoked?: () => void;
    class?: string;
  }

  let { tokens, onrevoked, class: className = '' }: Props = $props();

  let revoking = $state<JoinToken | null>(null);
  let confirmOpen = $state(false);

  function ask(token: JoinToken): void {
    revoking = token;
    confirmOpen = true;
  }

  async function revoke(): Promise<void> {
    const token = revoking;
    if (!token?.id) return;
    try {
      await deleteJoinToken(token.id);
      toasts.success(`Join token ${token.prefix ?? ''} revoked`);
      onrevoked?.();
    } catch (cause) {
      toasts.fromError(cause, 'That join token was not revoked');
    }
  }

  function labelText(labels: Record<string, string> | undefined): string {
    const entries = Object.entries(labels ?? {});
    return entries.length === 0 ? '' : entries.map(([k, v]) => `${k}=${v}`).join(' ');
  }
</script>

{#if tokens.length === 0}
  <EmptyState
    compact
    title="No join tokens outstanding"
    description="A join token is minted when you add a host, is good for one enrolment, and expires on its own."
  />
{:else}
  <div class="scroll {className}">
    <table>
      <caption class="sr-only">Outstanding join tokens</caption>
      <thead>
        <tr>
          <th scope="col">Prefix</th>
          <th scope="col">State</th>
          <th scope="col">Capacity</th>
          <th scope="col">Labels</th>
          <th scope="col">Created by</th>
          <th scope="col">Expires</th>
          <th scope="col"><span class="sr-only">Actions</span></th>
        </tr>
      </thead>
      <tbody>
        {#each tokens as token (token.id)}
          <tr>
            <td class="mono">{token.prefix ?? '--'}</td>
            <td>
              {#if token.used_at}
                <Badge tone="neutral" label="Used" size="sm" />
              {:else if token.usable === false}
                <Badge tone="draining" label="Expired" size="sm" />
              {:else}
                <Badge tone="idle" label="Usable" size="sm" />
              {/if}
            </td>
            <td class="tabular">{formatNumber(token.capacity ?? 0)}</td>
            <td class="mono labels">{labelText(token.labels) || '--'}</td>
            <td>{token.created_by || '--'}</td>
            <td>
              {#if token.used_at}
                <span class="muted">Used <RelativeTime value={token.used_at} plain /></span>
              {:else}
                <RelativeTime value={token.expires_at} />
              {/if}
            </td>
            <td class="actions">
              {#if !token.used_at}
                <IconButton
                  icon={Trash2}
                  label="Revoke the join token {token.prefix ?? ''}"
                  size="sm"
                  onclick={() => ask(token)}
                />
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}

<ConfirmDialog
  bind:open={confirmOpen}
  title="Revoke join token"
  name={revoking?.prefix}
  description="The token {revoking?.prefix ??
    ''} stops working immediately. A host part-way through enrolling with it will fail and have to be given a new one."
  confirmLabel="Revoke"
  onconfirm={revoke}
/>

<style>
  .scroll {
    overflow-x: auto;
  }
  table {
    width: 100%;
    border-collapse: separate;
    border-spacing: 0;
    font-size: var(--z-text-sm);
  }
  th {
    padding: var(--z-space-2) var(--z-space-4);
    border-bottom: 1px solid var(--z-border);
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    text-align: left;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    white-space: nowrap;
  }
  td {
    padding: var(--z-space-3) var(--z-space-4);
    border-bottom: 1px solid var(--z-border);
    color: var(--z-text);
    vertical-align: middle;
  }
  tbody tr:last-child td {
    border-bottom: 0;
  }
  .labels {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
    overflow-wrap: anywhere;
  }
  .muted {
    color: var(--z-text-subtle);
  }
  .actions {
    text-align: right;
    white-space: nowrap;
  }
</style>
