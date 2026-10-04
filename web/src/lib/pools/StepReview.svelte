<!--
  The last step: what the controller makes of it.

  The point of this step is that the server's opinion arrives *before* anything
  is created. A pool that no host can run is a pool that will never make a
  runner, and finding that out afterwards costs an operator an hour of staring
  at an empty queue.
-->
<script lang="ts">
  import { CircleCheck, TriangleAlert } from '@lucide/svelte';
  import type { Pool, PoolCreate, Result } from '$lib/api/types';
  import Button from '$lib/components/Button.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import PoolConfig from './PoolConfig.svelte';
  import PoolFit from './PoolFit.svelte';
  import PoolRoom from './PoolRoom.svelte';
  import PoolWarnings from './PoolWarnings.svelte';
  import { isStartupFinding } from './startupStability';
  import { FIELD_LABELS, stepForField, wizardSteps } from './PoolVocabulary.svelte';
  import type { WizardMode } from './PoolVocabulary.svelte';
  import type { PoolDraft } from './PoolWizardForm.svelte';

  interface Props {
    draft: PoolDraft;
    body: PoolCreate;
    editing: boolean;
    installationLabel: string;
    /** Which path the wizard is walking, so a jump lands on the right step. */
    mode: WizardMode;
    verdict: Result<'validatePool'> | null;
    validating: boolean;
    error: unknown;
    ongoto: (step: number) => void;
  }

  let { draft, body, editing, installationLabel, mode, verdict, validating, error, ongoto }: Props =
    $props();

  // The image is the server's answer where there is one: a pool that gives
  // its jobs a daemon runs the stock image's Docker variant, and this step
  // exists to show the pool that will be made rather than the one typed.
  //
  // A pool that names no image -- which is every pool the automatic path makes
  // -- stores nothing, so the answer arrives as the effective one instead, and
  // is rendered as what it is: an image chosen for this pool rather than typed
  // into it. Without it the step that says what will be made said nothing about
  // the image, including for the pool that has just asked for a Docker daemon.
  const preview = $derived<Pool>({
    ...body,
    image: verdict?.image ?? body.image,
    effective_image: verdict?.effective_image,
    installation_target: installationLabel,
  });

  const fieldErrors = $derived(verdict?.errors ?? []);
  const warnings = $derived(verdict?.warnings ?? []);
  const matching = $derived(verdict?.matching_hosts);
  const excluded = $derived(verdict?.excluded_hosts ?? []);
  // PoolFit renders this one, with the fleet's own account of which host is
  // there and what it could not do. Showing it again in the warnings list
  // below would be the same paragraph twice.
  // PoolRoom renders the three room warnings itself, with the fix one click
  // away, so listing them again underneath would be the same facts twice.
  const ROOM_CODES = ['pool.max_above_room', 'pool.host_overcommitted', 'pool.cache_above_disk'];
  const otherWarnings = $derived(
    warnings.filter(
      (w) =>
        w.code !== 'pool.no_matching_hosts' &&
        !ROOM_CODES.includes(w.code ?? '') &&
        !isStartupFinding(w),
    ),
  );
  const happy = $derived(
    verdict !== null &&
      verdict.valid &&
      fieldErrors.length === 0 &&
      warnings.length === 0 &&
      excluded.length === 0 &&
      (matching === undefined || matching > 0),
  );

  function label(field: string): string {
    return FIELD_LABELS[field] ?? field;
  }

  function stepName(field: string): string {
    return (
      wizardSteps(mode, editing)[stepForField(field, mode, editing)]?.title ?? 'the first step'
    );
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
    <PoolFit {verdict} {validating} />

    <!-- The same count the Size and Scaling steps showed, so the last screen
         before the pool exists says what its hosts can actually hold of it. -->
    <PoolRoom
      room={verdict.room ?? null}
      cpus={body.resources?.cpus ?? 0}
      memoryMb={body.resources?.memory_mb ?? 0}
      profile={body.size_from_profile === true}
      maxRunners={body.max_runners ?? 0}
      {validating}
    />

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
              <Button
                size="sm"
                variant="ghost"
                onclick={() => ongoto(stepForField(issue.field, mode, editing))}
              >
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
  .errors {
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-danger-border);
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
