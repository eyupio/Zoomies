<!--
  Step five: what the controller makes of it.

  The point of this step is that the server's opinion arrives *before* anything
  is created. A pool that no host can run is a pool that will never make a
  runner, and finding that out afterwards costs an operator an hour of staring
  at an empty queue.
-->
<script lang="ts">
  import { CircleCheck, ServerOff, TriangleAlert, Wrench } from '@lucide/svelte';
  import type { Pool, PoolCreate, Result } from '$lib/api/types';
  import { pluralise } from '$lib/format';
  import Button from '$lib/components/Button.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import PoolConfig from './PoolConfig.svelte';
  import RemedyText from '$lib/components/RemedyText.svelte';
  import PoolWarnings from './PoolWarnings.svelte';
  import { FIELD_LABELS, WIZARD_STEPS, stepForField } from './PoolVocabulary.svelte';
  import type { PoolDraft } from './PoolWizardForm.svelte';

  interface Props {
    draft: PoolDraft;
    body: PoolCreate;
    editing: boolean;
    installationLabel: string;
    verdict: Result<'validatePool'> | null;
    validating: boolean;
    error: unknown;
    ongoto: (step: number) => void;
  }

  let { draft, body, editing, installationLabel, verdict, validating, error, ongoto }: Props =
    $props();

  const preview = $derived<Pool>({ ...body, installation_target: installationLabel });

  const fieldErrors = $derived(verdict?.errors ?? []);
  const warnings = $derived(verdict?.warnings ?? []);
  const matching = $derived(verdict?.matching_hosts);
  // The server says the same thing as the banner below, only with the detail
  // the fleet knows: which host is there and what its agent could not reach.
  // It is shown inside the banner rather than a second time under it.
  const noHost = $derived(warnings.find((w) => w.code === 'pool.no_matching_hosts'));
  const otherWarnings = $derived(warnings.filter((w) => w.code !== 'pool.no_matching_hosts'));
  const happy = $derived(
    verdict !== null &&
      verdict.valid &&
      fieldErrors.length === 0 &&
      warnings.length === 0 &&
      (matching === undefined || matching > 0),
  );

  function label(field: string): string {
    return FIELD_LABELS[field] ?? field;
  }

  function stepName(field: string): string {
    return WIZARD_STEPS[stepForField(field)]?.title ?? 'the first step';
  }
</script>

<section class="summary" aria-label="What will be {editing ? 'saved' : 'created'}">
  <h3>{body.name || 'This pool'}</h3>
  <PoolConfig pool={preview} showId={false} />
</section>

<section class="verdict" aria-label="The controller's check">
  <h3>Checked against the controller</h3>

  {#if error}
    <ErrorState
      {error}
      title="This pool could not be checked"
      compact
      description="The controller did not answer the dry run, so the warnings below may be incomplete. Creating the pool will still be validated on the server."
    />
  {:else if validating && verdict === null}
    <div class="checking" aria-busy="true">
      <Skeleton width="45%" height="0.9rem" />
      <Skeleton lines={2} />
    </div>
  {:else if verdict}
    {#if matching !== undefined}
      <div class="hosts" class:none={matching === 0}>
        {#if matching === 0}
          <p class="hosts-title">
            <ServerOff size={15} aria-hidden="true" />
            No connected host can run this pool
          </p>
          <p class="hosts-body">
            Nothing matches this pool's backend and host selector, so it will never make a runner
            and every job asking for these labels will sit in the queue. Choose a backend one of
            your hosts offers, or connect a host that does, before you rely on it.
          </p>
          {#if noHost?.detail}
            <p class="hosts-body"><RemedyText text={noHost.detail} /></p>
          {/if}
          {#if noHost?.fix}
            <p class="hosts-fix">
              <Wrench size={13} aria-hidden="true" />
              <span><RemedyText text={noHost.fix} /></span>
            </p>
          {/if}
        {:else}
          <p class="hosts-title">
            <CircleCheck size={15} aria-hidden="true" />
            {pluralise(matching, 'connected host')} can run this pool
          </p>
        {/if}
      </div>
    {/if}

    {#if fieldErrors.length > 0}
      <div class="errors" role="group" aria-label="What the controller rejected">
        <p class="errors-title">
          <TriangleAlert size={15} aria-hidden="true" />
          The controller will not accept this pool yet
        </p>
        <ul>
          {#each fieldErrors as issue (issue.field + issue.message)}
            <li>
              <span class="field">{label(issue.field)}</span>
              <span class="message">{issue.message}</span>
              <Button size="sm" variant="ghost" onclick={() => ongoto(stepForField(issue.field))}>
                Back to {stepName(issue.field)}
              </Button>
            </li>
          {/each}
        </ul>
      </div>
    {/if}

    {#if otherWarnings.length > 0}
      <div class="warnings">
        <p class="warnings-title">This pool will be created with warnings</p>
        <PoolWarnings warnings={otherWarnings} bare />
      </div>
    {/if}

    {#if happy}
      <p class="ok">
        <CircleCheck size={15} aria-hidden="true" />
        Nothing to flag. The controller accepts this pool as it stands.
      </p>
    {/if}
  {/if}
</section>

{#if !editing}
  <Checkbox
    bind:checked={draft.enabled}
    label="Enable this pool as soon as it is created"
    description="A disabled pool makes no runners. Leave it off if you want to look it over first."
  />
{/if}

<style>
  .summary,
  .verdict {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
  }
  h3 {
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .checking {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
  }
  .hosts {
    padding: var(--z-space-3) var(--z-space-4);
    border: 1px solid var(--z-idle-border);
    border-radius: var(--z-radius-md);
    background: var(--z-idle-subtle);
  }
  .hosts.none {
    border: 2px solid var(--z-danger-border);
    background: var(--z-danger-subtle);
  }
  .hosts-title {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-idle);
  }
  .hosts.none .hosts-title {
    color: var(--z-danger);
  }
  .hosts-body {
    margin: var(--z-space-2) 0 0;
    max-width: 70ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text);
  }
  .hosts-fix {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
    margin: var(--z-space-2) 0 0;
    max-width: 70ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .errors {
    padding: var(--z-space-3) var(--z-space-4);
    border: 1px solid var(--z-danger-border);
    border-radius: var(--z-radius-md);
    background: var(--z-danger-subtle);
  }
  .errors-title {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0 0 var(--z-space-2);
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-danger);
  }
  .errors ul {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .errors li {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    font-size: var(--z-text-base);
  }
  .field {
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
  }
  .message {
    color: var(--z-text-muted);
  }
  .warnings-title {
    margin: 0 0 var(--z-space-2);
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    color: var(--z-text-muted);
  }
  .ok {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-base);
    color: var(--z-idle);
  }
</style>
