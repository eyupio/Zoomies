<!--
  Accounts.

  One rule shapes this whole panel: there is always at least one enabled
  administrator. The API enforces it and answers 409, and rather than showing
  that as an error this says plainly what happened and what to do instead --
  being refused because you are the last administrator is not a mistake, it is
  the system doing its job.
-->
<script lang="ts">
  import { KeyRound, Pencil, Plus, ShieldCheck, Trash2, UserX } from '@lucide/svelte';
  import {
    ApiError,
    createUser,
    deleteUser,
    listUsers,
    resetUserPassword,
    updateUser,
  } from '$lib/api/client';
  import type { Role, User } from '$lib/api/types';
  import { accountStatus } from '$lib/status';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import DropdownMenu from '$lib/components/DropdownMenu.svelte';
  import type { MenuItem } from '$lib/components/DropdownMenu.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import LoadingBoundary from '$lib/components/LoadingBoundary.svelte';
  import RadioGroup from '$lib/components/RadioGroup.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';

  const ROLE_OPTIONS = [
    { value: 'viewer', label: 'Viewer', description: 'Reads everything except secrets.' },
    { value: 'operator', label: 'Operator', description: 'Acts on the fleet and manages pools.' },
    {
      value: 'admin',
      label: 'Administrator',
      description: 'Also manages accounts, tokens, installations and settings.',
    },
  ];

  const MIN_PASSWORD = 12;

  let users = $state<User[]>([]);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let reload = $state(0);

  /** The last-administrator refusal, in our words rather than as an error. */
  let guard = $state('');

  $effect(() => {
    void reload;
    const controller = new AbortController();
    loading = true;
    void listUsers(controller.signal)
      .then((result) => {
        users = result.items ?? [];
        error = null;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        error = cause;
      })
      .finally(() => (loading = false));
    return () => controller.abort();
  });

  const admins = $derived(users.filter((u) => u.role === 'admin' && !u.disabled).length);

  /**
   * Handle a failure from any account change.
   *
   * A 409 here is only ever the last-administrator rule, so it is explained
   * instead of being shouted about: nothing was changed, and the way forward is
   * to promote somebody else first.
   */
  function handle(cause: unknown, what: string): void {
    if (cause instanceof ApiError && cause.isConflict) {
      guard = cause.message;
      return;
    }
    toasts.fromError(cause, what);
  }

  /* -- create ------------------------------------------------------------------ */

  let createOpen = $state(false);
  let newUsername = $state('');
  let newDisplayName = $state('');
  let newEmail = $state('');
  let newRole = $state('viewer');
  let newPassword = $state('');
  let creating = $state(false);
  let createErrors = $state<Record<string, string>>({});

  const passwordOptional = $derived(session.oidcEnabled);
  const passwordError = $derived(
    newPassword === '' && passwordOptional
      ? ''
      : newPassword.length < MIN_PASSWORD
        ? `At least ${MIN_PASSWORD} characters. This one has ${newPassword.length}.`
        : '',
  );

  function openCreate(): void {
    newUsername = '';
    newDisplayName = '';
    newEmail = '';
    newRole = 'viewer';
    newPassword = '';
    createErrors = {};
    createOpen = true;
  }

  async function create(): Promise<void> {
    if (!newUsername.trim() || passwordError) return;
    creating = true;
    createErrors = {};
    try {
      await createUser({
        username: newUsername.trim(),
        display_name: newDisplayName.trim() || undefined,
        email: newEmail.trim() || undefined,
        role: newRole as Role,
        password: newPassword || undefined,
      });
      toasts.success(
        `${newUsername.trim()} added`,
        newPassword
          ? 'They will be asked to choose their own password when they first sign in.'
          : 'They sign in through single sign-on.',
      );
      createOpen = false;
      reload += 1;
    } catch (cause) {
      if (cause instanceof ApiError) createErrors = cause.fieldErrors();
      handle(cause, 'That account was not created');
    } finally {
      creating = false;
    }
  }

  /* -- edit --------------------------------------------------------------------- */

  let editOpen = $state(false);
  let editing = $state<User | null>(null);
  let editRole = $state('viewer');
  let editDisplayName = $state('');
  let editEmail = $state('');
  let saving = $state(false);
  let editErrors = $state<Record<string, string>>({});

  function openEdit(user: User): void {
    editing = user;
    editRole = user.role ?? 'viewer';
    editDisplayName = user.display_name ?? '';
    editEmail = user.email ?? '';
    editErrors = {};
    editOpen = true;
  }

  async function save(): Promise<void> {
    const user = editing;
    if (!user?.id) return;
    saving = true;
    editErrors = {};
    try {
      await updateUser(user.id, {
        role: editRole as Role,
        display_name: editDisplayName.trim(),
        email: editEmail.trim(),
      });
      toasts.success(`${user.username ?? 'Account'} updated`);
      editOpen = false;
      reload += 1;
      if (user.id === session.identity?.id) await session.refresh();
    } catch (cause) {
      if (cause instanceof ApiError) editErrors = cause.fieldErrors();
      handle(cause, 'That account was not updated');
    } finally {
      saving = false;
    }
  }

  /* -- disable, reset and delete -------------------------------------------------- */

  async function setDisabled(user: User, disabled: boolean): Promise<void> {
    if (!user.id) return;
    try {
      await updateUser(user.id, { disabled });
      toasts.success(
        disabled
          ? `${user.username ?? 'Account'} disabled`
          : `${user.username ?? 'Account'} enabled`,
        disabled ? 'Their sessions stop working at the next request.' : '',
      );
      reload += 1;
    } catch (cause) {
      handle(cause, 'That account was not changed');
    }
  }

  let resetOpen = $state(false);
  let resetting = $state<User | null>(null);
  let resetPassword = $state('');
  let resetBusy = $state(false);
  let resetErrors = $state<Record<string, string>>({});

  const resetError = $derived(
    resetPassword.length > 0 && resetPassword.length < MIN_PASSWORD
      ? `At least ${MIN_PASSWORD} characters. This one has ${resetPassword.length}.`
      : '',
  );

  function openReset(user: User): void {
    resetting = user;
    resetPassword = '';
    resetErrors = {};
    resetOpen = true;
  }

  async function doReset(): Promise<void> {
    const user = resetting;
    if (!user?.id || resetPassword.length < MIN_PASSWORD) return;
    resetBusy = true;
    resetErrors = {};
    try {
      await resetUserPassword(user.id, { new_password: resetPassword });
      toasts.success(
        `Password reset for ${user.username ?? 'the account'}`,
        'They must choose their own the next time they sign in. Send them this one over something private.',
      );
      resetOpen = false;
      resetPassword = '';
      reload += 1;
    } catch (cause) {
      if (cause instanceof ApiError) resetErrors = cause.fieldErrors();
      handle(cause, 'That password was not reset');
    } finally {
      resetBusy = false;
    }
  }

  let deleteOpen = $state(false);
  let deleting = $state<User | null>(null);

  async function remove(): Promise<void> {
    const user = deleting;
    if (!user?.id) return;
    try {
      await deleteUser(user.id);
      toasts.success(`${user.username ?? 'Account'} deleted`);
      reload += 1;
    } catch (cause) {
      handle(cause, 'That account was not deleted');
    }
  }

  function actionsFor(user: User): MenuItem[] {
    const self = user.id === session.identity?.id;
    return [
      { id: 'edit', label: 'Edit role and details', icon: Pencil, onSelect: () => openEdit(user) },
      {
        id: 'password',
        label: 'Reset password',
        icon: KeyRound,
        onSelect: () => openReset(user),
      },
      {
        id: 'disabled',
        label: user.disabled ? 'Enable this account' : 'Disable this account',
        icon: UserX,
        disabled: self && !user.disabled,
        onSelect: () => void setDisabled(user, !user.disabled),
      },
      {
        id: 'delete',
        label: 'Delete this account',
        icon: Trash2,
        danger: true,
        separated: true,
        disabled: self,
        onSelect: () => {
          deleting = user;
          deleteOpen = true;
        },
      },
    ];
  }
</script>

<div class="panel">
  <header>
    <div>
      <h2>Accounts</h2>
      <p>
        {admins === 1
          ? 'One enabled administrator. Zoomies will refuse any change that would leave none.'
          : `${admins} enabled administrators.`}
      </p>
    </div>
    <Button variant="primary" icon={Plus} onclick={openCreate}>Add an account</Button>
  </header>

  {#if guard}
    <div class="guard" role="alert">
      <ShieldCheck size={16} aria-hidden="true" />
      <div>
        <p class="guard-heading">Nothing was changed</p>
        <p class="guard-body">
          Zoomies keeps at least one enabled administrator, so that an instance can never lock
          everybody out of itself. Give another account the administrator role first, then try this
          again.
        </p>
        <p class="guard-quote">The server said: {guard}</p>
      </div>
      <Button size="sm" variant="ghost" onclick={() => (guard = '')}>Dismiss</Button>
    </div>
  {/if}

  <LoadingBoundary
    {loading}
    {error}
    empty={!loading && !error && users.length === 0}
    onretry={() => (reload += 1)}
  >
    {#snippet skeleton()}
      <div class="pad"><Skeleton lines={4} /></div>
    {/snippet}

    {#snippet emptyState()}
      <EmptyState
        title="No accounts"
        description="Every person who signs in has an account here. There should be at least one."
      >
        <Button variant="primary" icon={Plus} onclick={openCreate}>Add an account</Button>
      </EmptyState>
    {/snippet}

    <div class="scroll">
      <table>
        <caption class="sr-only">Accounts</caption>
        <thead>
          <tr>
            <th scope="col">Account</th>
            <th scope="col">Role</th>
            <th scope="col">State</th>
            <th scope="col">Last signed in</th>
            <th scope="col"><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          {#each users as user (user.id)}
            <tr>
              <td>
                <span class="name">{user.username}</span>
                {#if user.display_name}<span class="second">{user.display_name}</span>{/if}
                {#if user.email}<span class="second">{user.email}</span>{/if}
                {#if user.oidc_subject}<span class="second">Single sign-on</span>{/if}
              </td>
              <td>
                {ROLE_OPTIONS.find((r) => r.value === user.role)?.label ?? user.role}
              </td>
              <td>
                <Badge status={accountStatus(user.disabled)} size="sm" />
                {#if user.must_change_password}
                  <span class="second">Must change password</span>
                {/if}
              </td>
              <td>
                {#if user.last_login_at}
                  <RelativeTime value={user.last_login_at} />
                {:else}
                  <span class="never">Never</span>
                {/if}
              </td>
              <td class="actions">
                <DropdownMenu
                  items={actionsFor(user)}
                  label="Actions for {user.username ?? 'this account'}"
                  size="sm"
                />
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </LoadingBoundary>
</div>

<!-- Create -->
<Dialog
  bind:open={createOpen}
  title="Add an account"
  description="They will be asked to choose their own password the first time they sign in."
>
  <div class="form">
    <Field label="Username" error={createErrors.username} required>
      {#snippet children({ id, describedBy, invalid })}
        <Input bind:value={newUsername} {id} {describedBy} {invalid} autocomplete="off" />
      {/snippet}
    </Field>
    <Field label="Display name" hint="Optional." error={createErrors.display_name}>
      {#snippet children({ id, describedBy, invalid })}
        <Input bind:value={newDisplayName} {id} {describedBy} {invalid} />
      {/snippet}
    </Field>
    <Field label="Email" hint="Optional." error={createErrors.email}>
      {#snippet children({ id, describedBy, invalid })}
        <Input bind:value={newEmail} {id} {describedBy} {invalid} type="email" />
      {/snippet}
    </Field>
    <RadioGroup bind:value={newRole} name="new-role" legend="Role" options={ROLE_OPTIONS} />
    <Field
      label="Password"
      hint={passwordOptional
        ? `At least ${MIN_PASSWORD} characters. Leave it empty for an account that signs in through single sign-on.`
        : `At least ${MIN_PASSWORD} characters.`}
      error={createErrors.password ?? passwordError}
    >
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={newPassword}
          {id}
          {describedBy}
          invalid={invalid || Boolean(passwordError)}
          type="password"
          autocomplete="new-password"
        />
      {/snippet}
    </Field>
  </div>

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (createOpen = false)}>Cancel</Button>
    <Button
      variant="primary"
      loading={creating}
      disabled={!newUsername.trim() || Boolean(passwordError)}
      onclick={create}
    >
      Add account
    </Button>
  {/snippet}
</Dialog>

<!-- Edit -->
<Dialog bind:open={editOpen} title="Edit {editing?.username ?? 'account'}">
  <div class="form">
    <Field label="Display name" error={editErrors.display_name}>
      {#snippet children({ id, describedBy, invalid })}
        <Input bind:value={editDisplayName} {id} {describedBy} {invalid} />
      {/snippet}
    </Field>
    <Field label="Email" error={editErrors.email}>
      {#snippet children({ id, describedBy, invalid })}
        <Input bind:value={editEmail} {id} {describedBy} {invalid} type="email" />
      {/snippet}
    </Field>
    <RadioGroup bind:value={editRole} name="edit-role" legend="Role" options={ROLE_OPTIONS} />
    {#if editing?.id === session.identity?.id && editRole !== 'admin'}
      <p class="warn">
        This is your own account. Taking the administrator role away from it means you will not be
        able to put it back.
      </p>
    {/if}
  </div>

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (editOpen = false)}>Cancel</Button>
    <Button variant="primary" loading={saving} onclick={save}>Save changes</Button>
  {/snippet}
</Dialog>

<!-- Reset password -->
<Dialog
  bind:open={resetOpen}
  title="Reset password for {resetting?.username ?? 'account'}"
  description="They will have to choose their own the next time they sign in."
  size="sm"
>
  <div class="form">
    <Field
      label="New password"
      hint="At least {MIN_PASSWORD} characters. Send it to them over something private; it is not emailed."
      error={resetErrors.new_password ?? resetError}
    >
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={resetPassword}
          {id}
          {describedBy}
          invalid={invalid || Boolean(resetError)}
          type="password"
          autocomplete="new-password"
        />
      {/snippet}
    </Field>
  </div>

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (resetOpen = false)}>Cancel</Button>
    <Button
      variant="primary"
      loading={resetBusy}
      disabled={resetPassword.length < MIN_PASSWORD}
      onclick={doReset}
    >
      Reset password
    </Button>
  {/snippet}
</Dialog>

<ConfirmDialog
  bind:open={deleteOpen}
  title="Delete account"
  name={deleting?.username}
  description="{deleting?.username ??
    'This account'} is deleted and its sessions stop working immediately."
  consequences={[
    'Anything they did stays in the audit log, under their name.',
    'API tokens they created keep working until they are revoked.',
  ]}
  confirmLabel="Delete account"
  requireName
  onconfirm={remove}
  oncancel={() => (deleting = null)}
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
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .guard {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
    margin: var(--z-space-4) var(--z-space-5) 0;
    padding: var(--z-space-4);
    border: 1px solid var(--z-pending-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-pending-subtle);
  }
  .guard-heading {
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .guard-body {
    margin: var(--z-space-1) 0 0;
    max-width: 74ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text);
  }
  .guard-quote {
    margin: var(--z-space-2) 0 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
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
  .name {
    display: block;
    font-weight: var(--z-weight-medium);
  }
  .second {
    display: block;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .never {
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
  .warn {
    margin: 0;
    padding: var(--z-space-3);
    border: 1px solid var(--z-pending-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-pending-subtle);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text);
  }
</style>
