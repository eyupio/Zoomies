<!--
  Step one: which GitHub installation the runners register with, and what to
  call the pool.

  With no installation there is nothing to register against, so this step says
  that plainly and links to the page that fixes it rather than offering an empty
  select the operator would poke at.
-->
<script lang="ts">
  import { Dices, Plug } from '@lucide/svelte';
  import type { Installation, RunnerGroup } from '$lib/api/types';
  import { installationStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Field from '$lib/components/Field.svelte';
  import IconButton from '$lib/components/IconButton.svelte';
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
    /** Roll another name. Left out when editing, where a name is already in use. */
    onspin?: () => void;
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
    onspin,
  }: Props = $props();

  // Purely so the dice turn when they are rolled. Cleared on animationend
  // rather than on a timer, so the two cannot disagree about how long the
  // animation is -- and under prefers-reduced-motion, where the token is 1ms,
  // it ends immediately and nothing spins.
  let rolling = $state(false);

  function spin(): void {
    rolling = true;
    onspin?.();
  }

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
  hint={onspin
    ? 'Filled in from the kennel and what the fleet runs on. Type over it, or roll the dice for another.'
    : 'Operators see this everywhere: in runner names, the audit log and the CLI.'}
>
  {#snippet children({ id, describedBy, invalid })}
    <Input
      bind:value={draft.name}
      {id}
      {describedBy}
      {invalid}
      mono
      placeholder="zoomies-linux-x64"
      autocomplete="off"
      onblur={() => touch('name')}
    >
      {#snippet trailing()}
        {#if onspin}
          <span class="dice" class:rolling onanimationend={() => (rolling = false)}>
            <IconButton icon={Dices} label="Spin a new name" size="sm" onclick={spin} />
          </span>
        {/if}
      {/snippet}
    </Input>
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
    title="No GitHub connection yet"
    description="A pool registers its runners with a GitHub App installation, so Zoomies needs one before it can make a pool."
  >
    <Button variant="primary" href="/installations">Connect GitHub</Button>
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
  .dice {
    display: inline-flex;
  }
  .rolling {
    animation: roll var(--z-motion-slow) var(--z-ease);
  }
  @keyframes roll {
    from {
      transform: rotate(0turn);
    }
    to {
      transform: rotate(1turn);
    }
  }
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
