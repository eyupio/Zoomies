<!--
  Step one: which GitHub installation the runners register with, and what to
  call the pool.

  With no installation there is nothing to register against, so this step says
  that plainly and links to the page that fixes it rather than offering an empty
  select the operator would poke at.
-->
<script lang="ts">
  import { Plug } from '@lucide/svelte';
  import type { Installation, RunnerGroup } from '$lib/api/types';
  import { installationStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import type { PoolDraft } from './PoolWizardForm.svelte';

  interface Props {
    draft: PoolDraft;
    errors: Record<string, string>;
    touch: (field: string) => void;
    installations: readonly Installation[];
    loading: boolean;
    error: unknown;
    /** Fetch the installations again after a failure. */
    onretry?: () => void;
    groups: readonly RunnerGroup[];
    groupsLoading: boolean;
    groupsError: unknown;
  }

  let {
    draft,
    errors,
    touch,
    installations,
    loading,
    error,
    onretry,
    groups,
    groupsLoading,
    groupsError,
  }: Props = $props();

  const installationOptions = $derived(
    installations.map((entry) => ({
      value: entry.id ?? '',
      label: `${entry.target ?? 'unnamed'} (${entry.target_type ?? 'org'})`,
    })),
  );

  const chosen = $derived(installations.find((entry) => entry.id === draft.installation_id));

  const groupOptions = $derived([
    { value: '', label: 'Default' },
    ...groups.map((group) => ({ value: group.name ?? '', label: group.name ?? 'unnamed' })),
  ]);
</script>

<Field
  label="Pool name"
  required
  error={errors['name']}
  hint="Operators see this everywhere: in runner names, the audit log and the CLI."
>
  {#snippet children({ id, describedBy, invalid })}
    <Input
      bind:value={draft.name}
      {id}
      {describedBy}
      {invalid}
      mono
      placeholder="linux-x64"
      autocomplete="off"
      onblur={() => touch('name')}
    />
  {/snippet}
</Field>

{#if error}
  <ErrorState {error} title="Installations could not be listed" {onretry} />
{:else if loading}
  <div class="loading">
    <Skeleton width="30%" height="0.75rem" />
    <Skeleton height="2rem" />
  </div>
{:else if installations.length === 0}
  <EmptyState
    icon={Plug}
    title="No GitHub installations yet"
    description="A pool registers its runners with a GitHub App installation, so Zoomies needs one before it can make a pool."
  >
    <Button variant="primary" href="/installations">Connect an installation</Button>
  </EmptyState>
{:else}
  <Field
    label="GitHub installation"
    required
    error={errors['installation_id']}
    hint="Runners in this pool register against this organisation or repository."
  >
    {#snippet children({ id, describedBy, invalid })}
      <Select
        bind:value={draft.installation_id}
        options={installationOptions}
        placeholder="Choose an installation"
        {id}
        {describedBy}
        {invalid}
        required
        onchange={() => touch('installation_id')}
      />
    {/snippet}
  </Field>

  {#if chosen}
    <p class="chosen">
      <Badge status={installationStatus(chosen.healthy)} size="sm" />
      {#if chosen.healthy === false}
        <span
          >This installation last failed its check{chosen.last_error
            ? `: ${chosen.last_error}`
            : '.'} Runners registering against it are likely to fail until it is fixed.</span
        >
      {:else}
        <span
          >{chosen.pool_count ?? 0} other {(chosen.pool_count ?? 0) === 1
            ? 'pool uses'
            : 'pools use'}
          this installation.</span
        >
      {/if}
    </p>
  {/if}

  <Field
    label="Runner group"
    error={errors['runner_group']}
    hint="Runner groups decide which repositories may use these runners. Leave it as the default unless your organisation has set groups up."
  >
    {#snippet children({ id, describedBy, invalid })}
      {#if groupsLoading}
        <Skeleton height="2rem" />
      {:else}
        <Select
          bind:value={draft.runner_group}
          options={groupOptions}
          {id}
          {describedBy}
          {invalid}
          onchange={() => touch('runner_group')}
        />
      {/if}
    {/snippet}
  </Field>

  {#if groupsError}
    <p class="note">
      Runner groups could not be listed, so only the default is offered. The pool can still be
      created; verify the installation to find out why GitHub refused.
    </p>
  {/if}
{/if}

<style>
  .loading {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
  }
  .chosen {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .note {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-pending);
  }
</style>
