<!--
  API tokens.

  A token is a credential for automation: the CLI, a Prometheus scrape, a
  deploy job. The plaintext exists once, in the response that creates it, and
  the panel is built around that fact -- the value is shown in a block that says
  it will not be shown again, and everything afterwards is metadata.

  Anybody signed in manages their own tokens here, never at a role above their
  own; an administrator sees and manages everybody's. The server decides both
  -- the list it answers is already the caller's -- so this only words the page
  and offers the roles the caller may actually give.
-->
<script lang="ts">
  import { KeyRound, Plus, Trash2 } from '@lucide/svelte';
  import {
    ApiError,
    createToken,
    deleteToken,
    listTokens,
    purgeTokens,
    revokeToken,
  } from '$lib/api/client';
  import type { APIToken, Role } from '$lib/api/types';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import { ROLE_OPTIONS, roleLabel } from '$lib/roles';
  import { apiTokenStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import LoadingBoundary from '$lib/components/LoadingBoundary.svelte';
  import RadioGroup from '$lib/components/RadioGroup.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Select from '$lib/components/Select.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import OneTimeSecret from './OneTimeSecret.svelte';
  import { tableLayout } from '$lib/actions/tableLayout';

  type Minted = APIToken & { token?: string };

  const EXPIRY_OPTIONS = [
    { value: '720h', label: '30 days' },
    { value: '2160h', label: '90 days' },
    { value: '8760h', label: 'A year' },
    { value: '', label: 'Never (not recommended)' },
  ];

  /** Administrators see everybody's tokens; everyone else sees their own. */
  const everybodys = $derived(session.can('admin'));
  /** A token carries no more than the person who mints it. */
  const roleOptions = $derived(ROLE_OPTIONS.filter((option) => session.can(option.value)));

  let tokens = $state<APIToken[]>([]);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let reload = $state(0);

  $effect(() => {
    void reload;
    const controller = new AbortController();
    loading = true;
    void listTokens(controller.signal)
      .then((result) => {
        tokens = result.items ?? [];
        error = null;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        error = cause;
      })
      .finally(() => (loading = false));
    return () => controller.abort();
  });

  /* -- create -------------------------------------------------------------------- */

  let createOpen = $state(false);
  let name = $state('');
  let role = $state('viewer');
  let expiry = $state('2160h');
  let scopeText = $state('');
  let creating = $state(false);
  let errors = $state<Record<string, string>>({});
  let minted = $state<Minted | null>(null);

  function open(): void {
    name = '';
    role = 'viewer';
    expiry = '2160h';
    scopeText = '';
    errors = {};
    minted = null;
    createOpen = true;
  }

  const scopes = $derived(
    scopeText
      .split(/[\s,]+/)
      .map((s) => s.trim())
      .filter(Boolean),
  );

  async function mint(): Promise<void> {
    if (!name.trim()) return;
    creating = true;
    errors = {};
    try {
      minted = await createToken({
        name: name.trim(),
        role: role as Role,
        scopes: scopes.length > 0 ? scopes : undefined,
        expires_in: expiry || undefined,
      });
      reload += 1;
    } catch (cause) {
      if (cause instanceof ApiError) errors = cause.fieldErrors();
      toasts.fromError(cause, 'That token was not created');
    } finally {
      creating = false;
    }
  }

  /* -- revoke --------------------------------------------------------------------- */

  let revokeOpen = $state(false);
  let revoking = $state<APIToken | null>(null);

  async function revoke(): Promise<boolean> {
    const token = revoking;
    if (!token?.id) return false;
    try {
      await revokeToken(token.id);
      toasts.success(`${token.name ?? 'Token'} revoked`, 'Anything using it stops working now.');
      reload += 1;
      return true;
    } catch (cause) {
      toasts.fromError(cause, 'That token was not revoked');
      return false;
    }
  }

  /* -- delete --------------------------------------------------------------------- */

  /*
    Only a token that can no longer authenticate may be deleted. A live one
    that vanished from this list would still work, with nothing left on the
    page to say so -- so a live row offers Revoke and a spent row offers Delete,
    and never both.
  */
  function spent(token: APIToken): boolean {
    return Boolean(token.revoked) || apiTokenStatus(token).key === 'expired';
  }

  const spentCount = $derived(tokens.filter(spent).length);

  let deleteOpen = $state(false);
  let deleting = $state<APIToken | null>(null);

  async function remove(): Promise<boolean> {
    const token = deleting;
    if (!token?.id) return false;
    try {
      await deleteToken(token.id);
      toasts.success(`${token.name ?? 'Token'} deleted`, 'The audit log still records it.');
      reload += 1;
      return true;
    } catch (cause) {
      toasts.fromError(cause, 'That token was not deleted');
      return false;
    }
  }

  let purgeOpen = $state(false);

  async function purge(): Promise<boolean> {
    try {
      const result = await purgeTokens(everybodys ? { all: true } : {});
      const n = result.deleted?.length ?? 0;
      toasts.success(
        n === 1 ? '1 token deleted' : `${n} tokens deleted`,
        'The audit log still records each one.',
      );
      reload += 1;
      return true;
    } catch (cause) {
      toasts.fromError(cause, 'Those tokens were not deleted');
      return false;
    }
  }
</script>

<PageHeader
  title={everybodys ? 'API tokens' : 'Your API tokens'}
  subtitle={everybodys
    ? 'Everybody’s bearer credentials for the CLI and for automation. Zoomies keeps only the hash, so a token that is lost has to be revoked and replaced.'
    : 'Yours, for the CLI and your own scripts, at a role no higher than yours. Zoomies keeps only the hash, so a token that is lost has to be revoked and replaced.'}
  onrefresh={() => {
    reload += 1;
  }}
>
  {#if spentCount > 0}
    <Button icon={Trash2} onclick={() => (purgeOpen = true)}>Delete revoked and expired</Button>
  {/if}
  <Button variant="primary" icon={Plus} onclick={open}>Create a token</Button>
</PageHeader>

<div class="panel">
  <LoadingBoundary
    {loading}
    {error}
    empty={!loading && !error && tokens.length === 0}
    onretry={() => (reload += 1)}
  >
    {#snippet skeleton()}
      <div class="pad"><Skeleton lines={3} /></div>
    {/snippet}

    {#snippet emptyState()}
      <EmptyState
        icon={KeyRound}
        title={everybodys ? 'No API tokens' : 'You have no API tokens'}
        description="A token lets the zoomies CLI or a script talk to Zoomies without a browser session."
      >
        <Button variant="primary" icon={Plus} onclick={open}>Create a token</Button>
      </EmptyState>
    {/snippet}

    <div class="scroll">
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
          id: 'api-tokens',
          columns: ['name', 'prefix', 'role', 'scopes', 'last-used', 'expires', 'actions'],
        }}
      >
        <caption class="sr-only">API tokens</caption>
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <thead role="rowgroup">
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <tr role="row">
            <th role="columnheader" scope="col">Name</th>
            <th role="columnheader" scope="col">Prefix</th>
            <th role="columnheader" scope="col">Role</th>
            <th role="columnheader" scope="col">Scopes</th>
            <th role="columnheader" scope="col">Last used</th>
            <th role="columnheader" scope="col">Expires</th>
            <th role="columnheader" scope="col"><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <tbody role="rowgroup">
          {#each tokens as token (token.id)}
            {@const state = apiTokenStatus(token)}
            <tr role="row" class:revoked={token.revoked}>
              <td role="cell" data-label="Name" class="name">{token.name}</td>
              <td role="cell" data-label="Prefix" class="mono">{token.prefix ?? '--'}</td>
              <td role="cell" data-label="Role">{roleLabel(token.role)}</td>
              <td role="cell" data-label="Scopes" class="scopes mono">
                {#if (token.scopes ?? []).length === 0}
                  <span class="muted">Whatever the role allows</span>
                {:else}
                  {(token.scopes ?? []).join(' ')}
                {/if}
              </td>
              <td role="cell" data-label="Last used">
                {#if token.last_used_at}
                  <RelativeTime value={token.last_used_at} />
                {:else}
                  <span class="muted">Never used</span>
                {/if}
              </td>
              <td role="cell" data-label="Expires">
                <Badge status={state} size="sm" />
                {#if token.expires_at && !token.revoked}
                  <span class="second"><RelativeTime value={token.expires_at} plain /></span>
                {/if}
              </td>
              <td role="cell" data-label="Actions" class="actions">
                {#if spent(token)}
                  <Button
                    size="sm"
                    variant="ghost"
                    ariaLabel="Delete {token.name}"
                    onclick={() => {
                      deleting = token;
                      deleteOpen = true;
                    }}
                  >
                    Delete
                  </Button>
                {:else}
                  <Button
                    size="sm"
                    variant="ghost"
                    ariaLabel="Revoke {token.name}"
                    onclick={() => {
                      revoking = token;
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
    </div>
  </LoadingBoundary>
</div>

<!--
  Not dismissible while the token is on screen: it is shown exactly once, and a
  stray click on the scrim would lose the only copy. Done and Escape still close.
-->
<Dialog
  bind:open={createOpen}
  title="Create an API token"
  description={minted
    ? 'Copy it now. This is the only time it exists in plain text.'
    : 'It carries a role, and optionally narrower scopes within that role.'}
  size="md"
  dismissible={!minted}
>
  {#if minted}
    <div class="form">
      <OneTimeSecret
        what="API token"
        value={minted.token ?? ''}
        copyLabel="Copy the token"
        note={minted.expires_at
          ? undefined
          : 'It never expires, so revoke it when it is done with.'}
      />
      <p class="usage">
        Use it as <span class="mono">Authorization: Bearer &lt;token&gt;</span>, or give it to the
        CLI as <span class="mono">ZOOMIES_TOKEN</span>.
      </p>
    </div>
  {:else}
    <form
      id="mint-token-form"
      class="form"
      novalidate
      onsubmit={(event) => {
        event.preventDefault();
        void mint();
      }}
    >
      <Field label="Name" hint="What is using it: prometheus, ci-deploy." error={errors.name}>
        {#snippet children({ id, describedBy, invalid })}
          <Input bind:value={name} {id} {describedBy} {invalid} autocomplete="off" />
        {/snippet}
      </Field>

      <RadioGroup bind:value={role} name="token-role" legend="Role" options={roleOptions} />

      <Field
        label="Expires"
        hint="A token that never expires is one more thing to remember. Prefer a date."
        error={errors.expires_in}
      >
        {#snippet children({ id, describedBy, invalid })}
          <Select bind:value={expiry} options={EXPIRY_OPTIONS} {id} {describedBy} {invalid} />
        {/snippet}
      </Field>

      <Field
        label="Scopes"
        hint="Optional. Space-separated, e.g. pools:read runners:write. Empty means everything the role allows."
        error={errors.scopes}
      >
        {#snippet children({ id, describedBy, invalid })}
          <Input bind:value={scopeText} {id} {describedBy} {invalid} mono autocomplete="off" />
        {/snippet}
      </Field>
    </form>
  {/if}

  {#snippet footer()}
    {#if minted}
      <Button variant="primary" onclick={() => (createOpen = false)}>Done</Button>
    {:else}
      <Button variant="ghost" onclick={() => (createOpen = false)}>Cancel</Button>
      <Button
        variant="primary"
        type="submit"
        form="mint-token-form"
        loading={creating}
        disabled={!name.trim()}
      >
        Create token
      </Button>
    {/if}
  {/snippet}
</Dialog>

<ConfirmDialog
  bind:open={revokeOpen}
  title="Revoke token"
  name={revoking?.name}
  description="{revoking?.name ?? 'This token'} stops working immediately."
  consequences={['Anything still using it will start getting 401 responses.']}
  confirmLabel="Revoke"
  onconfirm={revoke}
  oncancel={() => (revoking = null)}
/>

<ConfirmDialog
  bind:open={deleteOpen}
  title="Delete token"
  name={deleting?.name}
  description="{deleting?.name ?? 'This token'} is removed from this list for good."
  consequences={[
    'It already cannot be used, so nothing stops working.',
    'The audit log keeps its revocation and this deletion, by prefix.',
  ]}
  confirmLabel="Delete"
  onconfirm={remove}
  oncancel={() => (deleting = null)}
/>

<ConfirmDialog
  bind:open={purgeOpen}
  title="Delete revoked and expired tokens"
  description={spentCount === 1
    ? '1 token that can no longer be used is removed from this list for good.'
    : `${spentCount} tokens that can no longer be used are removed from this list for good.`}
  consequences={[
    'Tokens that still work are left alone.',
    'The audit log keeps a row for each deletion, by prefix.',
  ]}
  confirmLabel="Delete them"
  onconfirm={purge}
/>

<style>
  .panel {
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  .pad {
    padding: var(--z-space-5);
  }
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
    padding: var(--z-space-2) var(--z-space-5);
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
    padding: var(--z-space-3) var(--z-space-5);
    border-bottom: var(--z-border-width) solid var(--z-border);
    color: var(--z-text);
    vertical-align: top;
  }
  tbody tr:last-child td {
    border-bottom: 0;
  }
  tr.revoked td {
    color: var(--z-text-subtle);
  }
  .name {
    font-weight: var(--z-weight-medium);
  }
  .scopes {
    font-size: var(--z-text-xs);
    max-width: 20rem;
    overflow-wrap: anywhere;
  }
  .second {
    display: block;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .muted {
    color: var(--z-text-subtle);
  }
  .actions {
    text-align: right;
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
