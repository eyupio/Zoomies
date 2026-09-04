<!--
  API tokens.

  A token is a credential for automation: the CLI, a Prometheus scrape, a
  deploy job. The plaintext exists once, in the response that creates it, and
  the panel is built around that fact -- the value is shown in a block that says
  it will not be shown again, and everything afterwards is metadata.
-->
<script lang="ts">
  import { KeyRound, Plus } from '@lucide/svelte';
  import { ApiError, createToken, listTokens, revokeToken } from '$lib/api/client';
  import type { APIToken, Role } from '$lib/api/types';
  import { parseGoDuration } from '$lib/format';
  import { toasts } from '$lib/state/toasts.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import LoadingBoundary from '$lib/components/LoadingBoundary.svelte';
  import RadioGroup from '$lib/components/RadioGroup.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Select from '$lib/components/Select.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import OneTimeSecret from './OneTimeSecret.svelte';

  type Minted = APIToken & { token?: string };

  const ROLE_OPTIONS = [
    { value: 'viewer', label: 'Viewer', description: 'Reads everything except secrets.' },
    { value: 'operator', label: 'Operator', description: 'Acts on the fleet and manages pools.' },
    {
      value: 'admin',
      label: 'Administrator',
      description: 'Everything, including accounts and settings. Give this out sparingly.',
    },
  ];

  const EXPIRY_OPTIONS = [
    { value: '720h', label: '30 days' },
    { value: '2160h', label: '90 days' },
    { value: '8760h', label: 'A year' },
    { value: '', label: 'Never (not recommended)' },
  ];

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

  async function revoke(): Promise<void> {
    const token = revoking;
    if (!token?.id) return;
    try {
      await revokeToken(token.id);
      toasts.success(`${token.name ?? 'Token'} revoked`, 'Anything using it stops working now.');
      reload += 1;
    } catch (cause) {
      toasts.fromError(cause, 'That token was not revoked');
    }
  }

  function expiryState(token: APIToken): { label: string; tone: 'idle' | 'pending' | 'neutral' } {
    if (token.revoked) return { label: 'Revoked', tone: 'neutral' };
    if (!token.expires_at) return { label: 'Never expires', tone: 'pending' };
    const remaining = new Date(token.expires_at).getTime() - Date.now();
    if (remaining <= 0) return { label: 'Expired', tone: 'neutral' };
    if (remaining < (parseGoDuration('168h') ?? 0))
      return { label: 'Expires soon', tone: 'pending' };
    return { label: 'Active', tone: 'idle' };
  }
</script>

<div class="panel">
  <header>
    <div>
      <h2>API tokens</h2>
      <p>
        Bearer credentials for the CLI and for automation. Zoomies keeps only the hash, so a token
        that is lost has to be revoked and replaced.
      </p>
    </div>
    <Button variant="primary" icon={Plus} onclick={open}>Create a token</Button>
  </header>

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
        title="No API tokens"
        description="A token lets the zoomies CLI or a script talk to this controller without a browser session."
      >
        <Button variant="primary" icon={Plus} onclick={open}>Create a token</Button>
      </EmptyState>
    {/snippet}

    <div class="scroll">
      <table>
        <caption class="sr-only">API tokens</caption>
        <thead>
          <tr>
            <th scope="col">Name</th>
            <th scope="col">Prefix</th>
            <th scope="col">Role</th>
            <th scope="col">Scopes</th>
            <th scope="col">Last used</th>
            <th scope="col">Expires</th>
            <th scope="col"><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          {#each tokens as token (token.id)}
            {@const state = expiryState(token)}
            <tr class:revoked={token.revoked}>
              <td class="name">{token.name}</td>
              <td class="mono">{token.prefix ?? '--'}</td>
              <td>{ROLE_OPTIONS.find((r) => r.value === token.role)?.label ?? token.role}</td>
              <td class="scopes mono">
                {#if (token.scopes ?? []).length === 0}
                  <span class="muted">Whatever the role allows</span>
                {:else}
                  {(token.scopes ?? []).join(' ')}
                {/if}
              </td>
              <td>
                {#if token.last_used_at}
                  <RelativeTime value={token.last_used_at} />
                {:else}
                  <span class="muted">Never used</span>
                {/if}
              </td>
              <td>
                <Badge tone={state.tone} label={state.label} size="sm" />
                {#if token.expires_at && !token.revoked}
                  <span class="second"><RelativeTime value={token.expires_at} plain /></span>
                {/if}
              </td>
              <td class="actions">
                {#if !token.revoked}
                  <Button
                    size="sm"
                    variant="ghost"
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

<Dialog
  bind:open={createOpen}
  title="Create an API token"
  description={minted
    ? 'Copy it now. This is the only time it exists in plain text.'
    : 'It carries a role, and optionally narrower scopes within that role.'}
  size="md"
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
    <div class="form">
      <Field label="Name" hint="What is using it: prometheus, ci-deploy." error={errors.name}>
        {#snippet children({ id, describedBy, invalid })}
          <Input bind:value={name} {id} {describedBy} {invalid} autocomplete="off" />
        {/snippet}
      </Field>

      <RadioGroup bind:value={role} name="token-role" legend="Role" options={ROLE_OPTIONS} />

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
    </div>
  {/if}

  {#snippet footer()}
    {#if minted}
      <Button variant="primary" onclick={() => (createOpen = false)}>Done</Button>
    {:else}
      <Button variant="ghost" onclick={() => (createOpen = false)}>Cancel</Button>
      <Button variant="primary" loading={creating} disabled={!name.trim()} onclick={mint}>
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

<style>
  .panel {
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--z-space-4);
    padding: var(--z-space-4) var(--z-space-5);
    border-bottom: 1px solid var(--z-border);
  }
  h2 {
    margin: 0;
    font-size: var(--z-text-lg);
    line-height: var(--z-leading-lg);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  header p {
    margin: var(--z-space-1) 0 0;
    max-width: 74ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .pad {
    padding: var(--z-space-5);
  }
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
    padding: var(--z-space-2) var(--z-space-5);
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
    padding: var(--z-space-3) var(--z-space-5);
    border-bottom: 1px solid var(--z-border);
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
</style>
