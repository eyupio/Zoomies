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
  import { joinTokenStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import IconButton from '$lib/components/IconButton.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import { tableLayout } from '$lib/actions/tableLayout';

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

  async function revoke(): Promise<boolean> {
    const token = revoking;
    if (!token?.id) return false;
    try {
      await deleteJoinToken(token.id);
      toasts.success(`Join token ${token.prefix ?? ''} revoked`);
      onrevoked?.();
      return true;
    } catch (cause) {
      toasts.fromError(cause, 'That join token was not revoked');
      return false;
    }
  }

  /**
   * The controller mints a one-minute token of its own for the agent it runs
   * inside itself, and it is spent or expired before anybody could look. Left in
   * the list it greets every new operator with a row they never made, in the
   * state colour of something that failed. One that is still good stays: it is
   * a live credential, and tidying that away would be the wrong kind of tidy.
   */
  const shown = $derived(
    tokens.filter(
      (token) => !(token.created_by === 'system' && (token.used_at || token.usable === false)),
    ),
  );

  function labelText(labels: Record<string, string> | undefined): string {
    const entries = Object.entries(labels ?? {});
    return entries.length === 0 ? '' : entries.map(([k, v]) => `${k}=${v}`).join(' ');
  }
</script>

{#if shown.length === 0}
  <EmptyState
    compact
    title="No join tokens outstanding"
    description="A join token is minted when you add a host, is good for one enrolment, and expires on its own."
  />
{:else}
  <div class="scroll {className}">
    <!--
      Every role is spelled out rather than left to the table's own display
      type. The rows become cards on a phone, which means `display` stops
      being `table-row`, and a browser drops the implicit row and cell roles
      the moment it does -- leaving a screen reader a run of loose text with
      nothing saying which value belongs to which record.
    -->
    <!-- svelte-ignore a11y_no_redundant_roles -->
    <table
      role="table"
      use:tableLayout={{
        id: 'join-tokens',
        columns: ['prefix', 'state', 'capacity', 'labels', 'created-by', 'expires', 'actions'],
      }}
    >
      <caption class="sr-only">Outstanding join tokens</caption>
      <!-- svelte-ignore a11y_no_redundant_roles -->
      <thead role="rowgroup">
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <tr role="row">
          <th role="columnheader" scope="col">Prefix</th>
          <th role="columnheader" scope="col">State</th>
          <th role="columnheader" scope="col">Capacity</th>
          <th role="columnheader" scope="col">Labels</th>
          <th role="columnheader" scope="col">Created by</th>
          <th role="columnheader" scope="col">Expires</th>
          <th role="columnheader" scope="col"><span class="sr-only">Actions</span></th>
        </tr>
      </thead>
      <!-- svelte-ignore a11y_no_redundant_roles -->
      <tbody role="rowgroup">
        {#each shown as token (token.id)}
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <tr role="row">
            <td role="cell" data-label="Prefix" class="mono">{token.prefix ?? '--'}</td>
            <td role="cell" data-label="State">
              <Badge status={joinTokenStatus(token)} size="sm" />
            </td>
            <td role="cell" data-label="Capacity" class="tabular"
              >{formatNumber(token.capacity ?? 0)}</td
            >
            <td role="cell" data-label="Labels" class="mono labels"
              >{labelText(token.labels) || '--'}</td
            >
            <td role="cell" data-label="Created by">{token.created_by || '--'}</td>
            <td role="cell" data-label="Expires">
              {#if token.used_at}
                <span class="muted">Used <RelativeTime value={token.used_at} plain /></span>
              {:else}
                <RelativeTime value={token.expires_at} />
              {/if}
            </td>
            <td role="cell" data-label="Actions" class="actions">
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
    /* The table is wider than a phone and scrolls inside this box, but a
       mobile browser still counts what it clips towards the page's width,
       grows the layout viewport to fit, and the fixed bottom navigation grows
       with it -- so the whole page scrolls sideways. Paint containment says
       what is clipped here stays here. */
    contain: paint;
  }
  table {
    width: 100%;
    /* The frame decides the width and the columns divide it, so the list never
       scrolls sideways -- see "Tables fit the window" in the UI guidelines. */
    table-layout: fixed;
    border-collapse: separate;
    border-spacing: 0;
    font-size: var(--z-text-sm);
  }
  th {
    /* A heading has nowhere to wrap when it is one word, and one cut to
       "Last sig..." names nothing, so it breaks instead. */
    overflow-wrap: anywhere;
    padding: var(--z-space-2) var(--z-space-4);
    border-bottom: var(--z-border-width) solid var(--z-border);
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    text-align: left;
    text-transform: uppercase;
    letter-spacing: var(--z-tracking-wide);
  }
  td {
    overflow: hidden;
    overflow-wrap: anywhere;
    padding: var(--z-space-3) var(--z-space-4);
    border-bottom: var(--z-border-width) solid var(--z-border);
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
  /*
    On a phone this is 7 columns in 360 pixels, and no amount of scrolling
    makes that a table -- the same problem, and the same answer, as the usage
    report and the grids: each row becomes a card and each cell carries its own
    heading. Nothing is dropped and nothing is truncated; the list reads down
    instead of across.
  */
  @media (max-width: 768px) {
    .scroll {
      overflow-x: visible;
      /*
        Nothing is clipped here any more -- the cards fit -- and the paint
        containment that kept a wide table from growing the layout viewport
        would now cut off a menu opened from the last row of a card.
      */
      contain: none;
    }
    table {
      display: block;
    }
    thead {
      display: none;
    }
    tbody {
      display: flex;
      flex-direction: column;
      gap: var(--z-space-3);
      padding: var(--z-space-3);
    }
    tbody tr {
      display: block;
      border: var(--z-border-width) solid var(--z-border);
      border-radius: var(--z-radius-md);
      background: var(--z-surface);
    }
    tbody td {
      display: flex;
      align-items: baseline;
      justify-content: space-between;
      gap: var(--z-space-4);
      padding: var(--z-space-2) var(--z-space-3);
      border: 0;
      text-align: right;
      overflow-wrap: anywhere;
    }
    tbody td::before {
      content: attr(data-label);
      flex: none;
      color: var(--z-text-muted);
      font-size: var(--z-text-2xs);
      font-weight: var(--z-weight-medium);
      text-transform: uppercase;
      letter-spacing: var(--z-tracking-wide);
      text-align: left;
    }
  }
</style>
