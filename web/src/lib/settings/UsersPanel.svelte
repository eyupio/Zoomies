<!--
  Accounts.

  One rule shapes this whole panel: there is always at least one enabled
  administrator. The API enforces it and answers 409, and rather than showing
  that as an error this says plainly what happened and what to do instead --
  being refused because you are the last administrator is not a mistake, it is
  the system doing its job.
-->
<script lang="ts">
  import { KeyRound, Pencil, Plus, ShieldCheck, ShieldOff, Trash2, UserX } from '@lucide/svelte';
  import {
    ApiError,
    createUser,
    deleteUser,
    listUsers,
    resetUserPassword,
    resetUserTwoStep,
    updateUser,
  } from '$lib/api/client';
  import type { Role, User } from '$lib/api/types';
  import { MIN_PASSWORD_LENGTH } from '$lib/passwords';
  import { ROLE_OPTIONS, roleLabel } from '$lib/roles';
  import { accountStatus } from '$lib/status';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import DropdownMenu from '$lib/components/DropdownMenu.svelte';
  import type { MenuItem } from '$lib/components/DropdownMenu.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import LoadingBoundary from '$lib/components/LoadingBoundary.svelte';
  import RadioGroup from '$lib/components/RadioGroup.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import { tableLayout } from '$lib/actions/tableLayout';

  let users = $state<User[]>([]);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let reload = $state(0);

  /** The last-administrator refusal, in our words rather than as an error. */
  let guard = $state('');

  /*
    Nothing here arrives over the stream: accounts change when an administrator
    changes them, possibly in another browser. Refreshing re-reads the list --
    through a counter rather than by remounting, so a half-typed form is not
    thrown away -- and asks who we are again, which is how a role granted a
    minute ago starts to apply.
  */
  async function refreshPage(): Promise<void> {
    reload += 1;
    await session.refresh();
  }

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
   * The server's sentence for the last-administrator rule.
   *
   * It has no code of its own -- it and a duplicate username are both a plain
   * 409 -- so the two are told apart by what the message says.
   */
  const LAST_ADMIN = /last enabled administrator/i;

  /**
   * Handle a failure from an account change that has no dialog to show it in.
   *
   * The last-administrator refusal is explained instead of being shouted about:
   * nothing was changed, and the way forward is to promote somebody else first.
   * Any other 409 is an ordinary failure and is toasted like one.
   */
  function handle(cause: unknown, what: string): void {
    if (cause instanceof ApiError && cause.isConflict && LAST_ADMIN.test(cause.message)) {
      guard = cause.message;
      return;
    }
    toasts.fromError(cause, what);
  }

  /**
   * A 409 that names no field, to be shown where the operator is looking.
   *
   * The page-level banner sits behind the modal, which is inert while it is
   * open, so a refusal shown there cannot be seen until the operator has closed
   * the form that raised it.
   */
  function inDialog(cause: unknown): string | null {
    if (!(cause instanceof ApiError) || !cause.isConflict) return null;
    return Object.keys(cause.fieldErrors()).length === 0 ? cause.message : null;
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
      : newPassword.length < MIN_PASSWORD_LENGTH
        ? `At least ${MIN_PASSWORD_LENGTH} characters. This one has ${newPassword.length}.`
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
      // Adding is the one change that can meet a duplicate username, and the
      // username is what has to change.
      const refusal = inDialog(cause);
      if (refusal) createErrors = { username: refusal };
      else handle(cause, 'That account was not created');
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
  /** A refusal that names no field, kept in the dialog so what was typed survives it. */
  let editRefusal = $state('');

  function openEdit(user: User): void {
    editing = user;
    editRole = user.role ?? 'viewer';
    editDisplayName = user.display_name ?? '';
    editEmail = user.email ?? '';
    editErrors = {};
    editRefusal = '';
    editOpen = true;
  }

  async function save(): Promise<void> {
    const user = editing;
    if (!user?.id) return;
    saving = true;
    editErrors = {};
    editRefusal = '';
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
      const refusal = inDialog(cause);
      if (refusal) editRefusal = refusal;
      else handle(cause, 'That account was not updated');
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
    resetPassword.length > 0 && resetPassword.length < MIN_PASSWORD_LENGTH
      ? `At least ${MIN_PASSWORD_LENGTH} characters. This one has ${resetPassword.length}.`
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
    if (!user?.id || resetPassword.length < MIN_PASSWORD_LENGTH) return;
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

  /*
    Resetting somebody's two-step verification is the answer to a lost phone,
    and exactly what somebody holding only a stolen password would ask for --
    so the confirmation says to check who is asking, and the reset is audited.
  */
  let twoStepOpen = $state(false);
  let twoStepFor = $state<User | null>(null);

  async function resetTwoStep(): Promise<boolean> {
    const user = twoStepFor;
    if (!user?.id) return false;
    try {
      await resetUserTwoStep(user.id);
      toasts.success(
        `Two-step verification reset for ${user.username ?? 'the account'}`,
        'Their sessions have ended. Their password alone signs them in next time.',
      );
      reload += 1;
      return true;
    } catch (cause) {
      handle(cause, 'Two-step verification was not reset');
      return false;
    }
  }

  let deleteOpen = $state(false);
  let deleting = $state<User | null>(null);

  async function remove(): Promise<boolean> {
    const user = deleting;
    if (!user?.id) return false;
    try {
      await deleteUser(user.id);
      toasts.success(`${user.username ?? 'Account'} deleted`);
      reload += 1;
      return true;
    } catch (cause) {
      handle(cause, 'That account was not deleted');
      return false;
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
        id: 'two-step',
        label: 'Reset two-step verification',
        icon: ShieldOff,
        disabled: !user.two_step_enabled,
        onSelect: () => {
          twoStepFor = user;
          twoStepOpen = true;
        },
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

<PageHeader
  title="Users"
  subtitle="Everyone who can sign in, and what each of them may do."
  onrefresh={refreshPage}
>
  <Button variant="primary" icon={Plus} onclick={openCreate}>Add an account</Button>
</PageHeader>

<div class="panel">
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
          id: 'accounts',
          columns: ['account', 'role', 'state', 'last-signed-in', 'actions'],
        }}
      >
        <caption class="sr-only">Accounts</caption>
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <thead role="rowgroup">
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <tr role="row">
            <th role="columnheader" scope="col">Account</th>
            <th role="columnheader" scope="col">Role</th>
            <th role="columnheader" scope="col">State</th>
            <th role="columnheader" scope="col">Last signed in</th>
            <th role="columnheader" scope="col"><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <tbody role="rowgroup">
          {#each users as user (user.id)}
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <tr role="row">
              <td role="cell" data-label="Account">
                <span class="name">{user.username}</span>
                {#if user.display_name}<span class="second">{user.display_name}</span>{/if}
                {#if user.email}<span class="second">{user.email}</span>{/if}
                {#if user.oidc_subject}<span class="second">Single sign-on</span>{/if}
              </td>
              <td role="cell" data-label="Role">
                {roleLabel(user.role)}
              </td>
              <td role="cell" data-label="State">
                <Badge status={accountStatus(user.disabled)} size="sm" />
                {#if user.must_change_password}
                  <span class="second">Must change password</span>
                {/if}
                {#if user.two_step_enabled}
                  <span class="second">Two-step on</span>
                {/if}
              </td>
              <td role="cell" data-label="Last signed in">
                {#if user.last_login_at}
                  <RelativeTime value={user.last_login_at} />
                {:else}
                  <span class="never">Never</span>
                {/if}
              </td>
              <td role="cell" data-label="Actions" class="actions">
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

<!--
  Under the list rather than above it: the count is the rule that shapes the
  page, and the roles are what somebody choosing one for a new account needs
  to see, in the words the form uses.
-->
<div class="foot">
  {#if !loading && !error}
    <p class="admins">
      {admins === 1
        ? 'One enabled administrator. Zoomies refuses any change that would leave none.'
        : `${admins} enabled administrators. Zoomies refuses any change that would leave none.`}
    </p>
  {/if}
  <dl class="roles">
    {#each ROLE_OPTIONS as role (role.value)}
      <div>
        <dt>{role.label}</dt>
        <dd>{role.description}</dd>
      </div>
    {/each}
  </dl>
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
        ? `At least ${MIN_PASSWORD_LENGTH} characters. Leave it empty for an account that signs in through single sign-on.`
        : `At least ${MIN_PASSWORD_LENGTH} characters.`}
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
    {#if editRefusal}
      <p class="warn" role="alert">{editRefusal}</p>
    {/if}
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
      hint="At least {MIN_PASSWORD_LENGTH} characters. Send it to them over something private; it is not emailed."
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
      disabled={resetPassword.length < MIN_PASSWORD_LENGTH}
      onclick={doReset}
    >
      Reset password
    </Button>
  {/snippet}
</Dialog>

<ConfirmDialog
  bind:open={twoStepOpen}
  title="Reset two-step verification"
  name={twoStepFor?.username}
  description="{twoStepFor?.username ??
    'This account'} loses their authenticator and recovery codes, and every session they have ends."
  consequences={[
    'Their password alone signs them in next time, or they set two-step up again if this instance requires it.',
    'Confirm the request some other way than the one it came by: a reset is what somebody with a stolen password would ask for.',
    'The reset is recorded in the audit log under your name.',
  ]}
  confirmLabel="Reset two-step"
  onconfirm={resetTwoStep}
  oncancel={() => (twoStepFor = null)}
/>

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
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  .foot {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    margin: var(--z-space-4) var(--z-space-1) 0;
  }
  .admins {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .roles {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr));
    gap: var(--z-space-2) var(--z-space-4);
    margin: 0;
  }
  .roles dt {
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
  }
  .roles dd {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .guard {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
    margin: var(--z-space-4) var(--z-space-5) 0;
    padding: var(--z-space-4);
    border: var(--z-border-width) solid var(--z-pending-border);
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
    border: var(--z-border-width) solid var(--z-pending-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-pending-subtle);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text);
  }
  /*
    On a phone this is 5 columns in 360 pixels, and no amount of scrolling
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
