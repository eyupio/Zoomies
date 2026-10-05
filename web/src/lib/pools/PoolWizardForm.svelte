<!--
  Pool creation and pool editing, in the same steps.

  The draft is one object held here, so going back never loses what was typed;
  the steps are presentation only. Client-side rules run continuously and gate
  the Next button; the server's own verdict is asked for on the review step,
  before anything is created, because "the pool exists but no host can run it"
  is a much worse place to find out.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import {
    ApiError,
    createPool,
    getPoolDefaults,
    listInstallations,
    listPoolPlatforms,
    listRunnerGroups,
    updatePool,
    validatePool,
  } from '$lib/api/client';
  import type {
    Body,
    Installation,
    Pool,
    PoolPlatform,
    Resources,
    Result,
    RunnerGroup,
  } from '$lib/api/types';
  import { BRAND_LABEL, brandedLabel } from '$lib/brand';
  import { nicknamedPoolName, poolName, spinWord } from './names';
  import {
    draftErrors,
    draftFromPool,
    emptyDraft,
    poolIsTuned,
    toInteger,
    toPoolBody,
  } from './draft';
  import type { PoolDraft } from './draft';
  import { backendOffers, stepFields, stepIndex, wizardSteps, stepForField } from './vocabulary';
  import type { WizardMode } from './vocabulary';
  import { fleet } from '$lib/state/fleet.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import Wizard from '$lib/components/Wizard.svelte';
  import StepTarget from './StepTarget.svelte';
  import StepLabels from './StepLabels.svelte';
  import StepHosts from './StepHosts.svelte';
  import { hostMatchesSelector } from './hostSelector';
  import StepBackend from './StepBackend.svelte';
  import StepDocker from './StepDocker.svelte';
  import StepSize from './StepSize.svelte';
  import StepScaling from './StepScaling.svelte';
  import StepMode from './StepMode.svelte';
  import StepRunners from './StepRunners.svelte';
  import StepReview from './StepReview.svelte';
  import PoolStartupStability from './PoolStartupStability.svelte';

  interface Props {
    /** The pool being edited. Leave it out to create a new one. */
    pool?: Pool;
    oncancel: () => void;
    ondone: (pool: Pool) => void;
    class?: string;
  }

  let { pool, oncancel, ondone, class: className = '' }: Props = $props();

  const editing = $derived(pool !== undefined);

  // Captured once on purpose: the routes remount this form with a {#key} when
  // they start editing a different pool, so a live reference would be wrong.
  let draft = $state<PoolDraft>(untrack(() => (pool ? draftFromPool(pool) : emptyDraft())));
  /*
    Which of the two paths the wizard is walking.

    A new pool starts on the simple one, because that is the pool most fleets
    want and the one every default already describes. Editing opens on the
    advanced path whenever the pool has anything the simple path cannot show --
    a fixed size, a host selector, a runner override, a platform or an image --
    so that opening a tuned pool never hides the settings it was tuned with,
    and never quietly saves them away.
  */
  let mode = $state<WizardMode>(
    untrack(() => (pool && poolIsTuned(draft) ? 'advanced' : 'simple')),
  );
  const steps = $derived(wizardSteps(mode, editing));
  const fieldsByStep = $derived(stepFields(mode, editing));
  let current = $state(0);
  let touched = $state<Record<string, boolean>>({});
  let serverErrors = $state<Record<string, string>>({});
  let socketConfirmed = $state(untrack(() => pool?.docker_mode === 'host-socket'));

  /*
    Auto-naming, on creation only.

    `autoName` is the last name the wizard produced. While the field still
    holds it the name is the wizard's to keep current -- so choosing Podman on
    the backend step renames the pool -- and the moment an operator types over
    it the wizard stops touching it, because a field that rewrites itself under
    someone's cursor is worse than no help at all. Editing an existing pool
    never generates anything: its name is already in workflows.

    `autoLabel` plays the same part for the labels, with one more state:
    `null`, meaning the operator has taken the labels over. Removing the
    suggested chip has to stick, and without that third state an empty list
    looks exactly like a list nothing has been put in yet.
  */
  let kennelWord = $state(spinWord());
  let autoName = $state('');
  let autoLabel = $state<string | null>('');
  let submitting = $state(false);
  let panel = $state<HTMLDivElement | null>(null);

  let installations = $state<Installation[]>([]);
  let installationsLoading = $state(true);
  let installationsError = $state<unknown>(null);
  /** Bumped by the error state's retry, which re-runs the fetch below. */
  let installationsAttempt = $state(0);

  let groups = $state<RunnerGroup[]>([]);
  let groupsLoading = $state(false);
  let groupsError = $state<unknown>(null);

  /**
   * The fleet's own default size, and the maximum the wizard worked out from
   * the room the hosts have for it.
   *
   * `autoMax` is the same idea as `autoName` above: while the field still
   * holds what the wizard put there, the wizard keeps it current, so choosing
   * bigger runners or fewer hosts lowers the cap in front of the operator. The
   * moment they type their own it stops following, because a number that
   * rewrites itself under somebody's cursor is worse than no help at all.
   * Editing an existing pool never follows: that cap is in force right now,
   * and the Scaling step offers the fleet's figure rather than taking it.
   */
  let defaults = $state<Resources | null>(null);
  let fleetDefaults = $state<Result<'getPoolDefaults'>['runner_settings'] | null>(null);
  let autoMax = $state<string | null>('4');

  // The operating systems a runner image is published for. Served rather than
  // hard-coded so the picker cannot offer one that does not exist.
  let platforms = $state<PoolPlatform[]>([]);

  let verdict = $state<Result<'validatePool'> | null>(null);
  let validating = $state(false);
  let stabilityRevision = $state(0);
  let validateError = $state<unknown>(null);

  const reviewStep = $derived(steps.length - 1);
  // -1 on the simple path, which walks no hosts step. Everything that reads it
  // compares with `>=`, so "there is no such step" reads as "we are past it" --
  // which is what the simple path means: it restricts nothing, so the
  // placement question is answered and behind us from the start.
  const hostsStep = $derived(steps.findIndex((step) => step.id === 'hosts'));
  // The backend step counts over the hosts this pool is allowed to land on, not
  // the whole fleet: "offered by 3 hosts" is a lie if two of them are the amd64
  // boxes an arm64 pool will never touch. Placement is chosen first for exactly
  // this reason.
  const selectedHosts = $derived(
    fleet.hosts.filter((host) => hostMatchesSelector(host, draft.host_selector)),
  );
  const restrictedToHosts = $derived(Object.keys(draft.host_selector).length > 0);
  const offers = $derived(backendOffers(selectedHosts));
  const clientErrors = $derived(draftErrors(draft, socketConfirmed, offers, fleet.loaded));
  const body = $derived(toPoolBody(draft));

  /** Client rules show once a field has been left; server rules show at once. */
  const errors = $derived.by(() => {
    const out: Record<string, string> = { ...serverErrors };
    for (const [field, message] of Object.entries(clientErrors)) {
      if (touched[field]) out[field] = message;
    }
    return out;
  });

  const blocking = $derived.by(() => {
    const fields =
      current === reviewStep ? Object.keys(clientErrors) : (fieldsByStep[current] ?? []);
    return fields
      .map((field) => clientErrors[field])
      .filter((message): message is string => Boolean(message));
  });
  const canAdvance = $derived(blocking.length === 0 && !submitting);

  const installationLabel = $derived(
    installations.find((entry) => entry.id === draft.installation_id)?.target ?? '',
  );

  function touch(field: string): void {
    touched = { ...touched, [field]: true };
    if (serverErrors[field] !== undefined) {
      const rest = { ...serverErrors };
      delete rest[field];
      serverErrors = rest;
    }
  }

  function touchStep(step: number): void {
    const fields = fieldsByStep[step] ?? [];
    if (fields.length === 0) return;
    const next = { ...touched };
    for (const field of fields) next[field] = true;
    touched = next;
  }

  function goTo(step: number): void {
    current = Math.min(Math.max(step, 0), reviewStep);
  }

  /*
    The simple path's way to the settings it leaves to the fleet.

    An edit skips the fork, and a pool with nothing the simple path cannot
    show opens on that path: target, labels, docker, review. That is the right
    opening for a pool somebody came to relabel, and a dead end for the one
    they came to make elastic -- the plain automatic pool is exactly the pool
    elastic CPU is for, and the fork it never walks was the only way to the
    size step that offers it. So a simple edit offers the advanced path from
    every step, and taking it lands on the first step the simple path skipped.
    The draft is one object and the steps only ways of looking at it, so
    nothing typed so far is lost on the way.
  */
  function showEverySetting(): void {
    mode = 'advanced';
    goTo(stepIndex('hosts', 'advanced', editing));
  }

  /* -- what the fleet and GitHub can offer --------------------------------- */

  /*
    Where a fixed size opens. It is a fleet setting, so the sliders cannot have
    a figure of their own: a wizard showing two cores while the fleet says
    eight would be describing a pool it is not about to create.

    It is the fleet's *suggestion* rather than what a pool becomes -- a pool
    that names no size is sized by its host -- so the figures are put on the
    sliders and nowhere else. A pool being edited already has whatever size it
    was given and is left alone.
  */
  $effect(() => {
    const controller = new AbortController();
    getPoolDefaults(controller.signal)
      .then((response) => {
        const resources = response.suggested_resources ?? response.resources ?? {};
        defaults = resources;
        // The fleet's own timings, so the overrides step can say what each
        // setting is being overridden *from*. An input whose placeholder reads
        // "20m0s, the fleet's" is one an operator can leave alone with
        // confidence; an empty box beside the word "timeout" is one they feel
        // obliged to fill.
        fleetDefaults = response.runner_settings ?? null;
        untrack(() => {
          if (editing) return;
          if (draft.cpus === '' && resources.cpus !== undefined)
            draft.cpus = String(resources.cpus);
          if (draft.memory_mb === '' && resources.memory_mb !== undefined) {
            draft.memory_mb = String(resources.memory_mb);
          }
          // The minimum is deliberately left empty rather than opened on the
          // fleet's: an empty minimum already follows runners.minimum_*, and
          // copying today's figure in would freeze it into the pool, so a
          // later change to the fleet setting would pass this pool by.
        });
      })
      // A failure here is not worth an error state: the sliders fall back to
      // the built-in figures, and nothing on the automatic path reads them.
      .catch(() => {});
    return () => controller.abort();
  });

  /*
    The maximum follows what the fleet can actually place, until it is typed
    over. This is the half of "how many runners" that nothing else can answer:
    the cap is a number about machines, and the fleet is the only thing that
    knows how many runners of this size its hosts can hold.
  */
  const followingMax = $derived(!editing && autoMax !== null && draft.max_runners === autoMax);
  $effect(() => {
    const room = verdict?.room?.runners;
    if (editing || room === undefined || room <= 0) return;
    untrack(() => {
      if (autoMax === null) return;
      if (draft.max_runners !== autoMax) {
        // Typed over: the wizard is done with this field.
        autoMax = null;
        return;
      }
      const next = String(Math.max(room, toInteger(draft.min_runners) ?? 0, 1));
      if (next === draft.max_runners) return;
      draft.max_runners = next;
      autoMax = next;
    });
  });

  $effect(() => {
    void installationsAttempt;
    const controller = new AbortController();
    // A failure here is not worth an error state: the picker falls back to
    // "Any", which is what a pool got before platforms existed.
    listPoolPlatforms(controller.signal)
      .then((response) => {
        platforms = response.items ?? [];
      })
      .catch(() => {});
    return () => controller.abort();
  });

  $effect(() => {
    const controller = new AbortController();
    installationsLoading = true;
    installationsError = null;
    listInstallations(controller.signal)
      .then((response) => {
        const items = response.items ?? [];
        installations = items;
        // One installation is the common case; choosing it for the operator is
        // the difference between a wizard and a form.
        const only = items[0];
        if (draft.installation_id === '' && items.length === 1 && only?.id) {
          draft.installation_id = only.id;
        }
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        installationsError = cause;
      })
      .finally(() => {
        installationsLoading = false;
      });
    return () => controller.abort();
  });

  $effect(() => {
    const id = draft.installation_id;
    groups = [];
    groupsError = null;
    if (id === '') {
      groupsLoading = false;
      return;
    }
    const controller = new AbortController();
    groupsLoading = true;
    listRunnerGroups(id, controller.signal)
      .then((response) => {
        groups = response.items ?? [];
        if (!editing && !touched.runner_group && draft.runner_group === '') {
          const managed = groups.find((group) => group.name?.toLowerCase() === 'zoomies');
          if (managed?.name) draft.runner_group = managed.name;
        }
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        groupsError = cause;
      })
      .finally(() => {
        groupsLoading = false;
      });
    return () => controller.abort();
  });

  /* -- the name, and the label it implies ---------------------------------- */

  /** The names already in use, so a new pool is not offered one of them. */
  const poolNames = $derived(fleet.pools.map((p) => p.name ?? ''));

  /**
   * Whether the operator has asked for a spaniel in the name.
   *
   * Without this the dice would be undone by the next thing they typed: the
   * suggested name is recomputed as the draft changes, and a shape that needs
   * no word would quietly drop the one they just rolled.
   */
  let nicknamed = $state(false);

  // The name follows the shape as the operator fills it in: a name generated
  // before any host had connected must not go on claiming the pool is 2 vCPU
  // Ubuntu after they have said 8 vCPU Debian, and the pools already in the
  // fleet decide whether this one needs a spaniel to be told from them.
  $effect(() => {
    if (editing) return;
    const suggested = nicknamed
      ? nicknamedPoolName(kennelWord, draft, fleet.hosts, defaults)
      : poolName(kennelWord, draft, fleet.hosts, poolNames, defaults);
    untrack(() => {
      if (draft.name !== '' && draft.name !== autoName) return;
      draft.name = suggested;
      autoName = suggested;
    });
  });

  $effect(() => {
    if (editing) return;
    const suggested = brandedLabel(draft.name);
    untrack(() => {
      if (autoLabel === null) return;
      const pristine =
        autoLabel === '' ? draft.labels.length === 0 : draft.labels.join() === autoLabel;
      if (!pristine) {
        autoLabel = null;
        return;
      }
      // A name that reduces to the brand alone says nothing the server does not
      // already add on save, so there is nothing worth filling in yet.
      if (suggested === BRAND_LABEL) return;
      if (draft.labels.join() === suggested) return;
      draft.labels = [suggested];
      autoLabel = suggested;
    });
  });

  /**
   * Roll a name from the kennel, whatever is in the field now.
   *
   * The suggested name carries a spaniel only when the shape cannot tell this
   * pool from another, so the dice ask for one outright: pressing it on a pool
   * whose shape is already unique has to change something, or it reads as a
   * broken button.
   */
  function spin(): void {
    if (editing) return;
    nicknamed = true;
    kennelWord = spinWord(kennelWord);
    const next = nicknamedPoolName(kennelWord, draft, fleet.hosts, defaults);
    draft.name = next;
    autoName = next;
    touch('name');
  }

  /* -- the server's verdict, before anything is created --------------------- */

  $effect(() => {
    // From the placement step on, not only at the end. Every step after it
    // edits something the controller's count depends on -- which hosts, which
    // backend, how big a runner is -- so the answer belongs beside the setting
    // that changes it, while there is still a reason to change it.
    if (current < hostsStep) return;
    void stabilityRevision;
    const payload = body;
    const controller = new AbortController();
    validating = true;
    const timer = setTimeout(() => {
      validatePool(payload, pool?.id, controller.signal)
        .then((result) => {
          verdict = result;
          validateError = null;
        })
        .catch((cause: unknown) => {
          if (cause instanceof DOMException && cause.name === 'AbortError') return;
          validateError = cause;
        })
        .finally(() => {
          validating = false;
        });
    }, 250);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  });

  /* -- focus follows the step ----------------------------------------------- */

  let lastStep = -1;
  $effect(() => {
    const step = current;
    if (step === lastStep) return;
    const moved = lastStep !== -1;
    lastStep = step;
    // Not on the first render: the shell has just put focus on the page
    // heading, and taking it away again would undo that.
    if (moved) untrack(() => panel)?.focus();
  });

  /* -- submitting ------------------------------------------------------------ */

  function applyFieldErrors(cause: ApiError): void {
    const fields = cause.fieldErrors();
    serverErrors = fields;
    const first = Object.keys(fields)[0];
    if (first !== undefined) goTo(stepForField(first, mode, editing));
  }

  /**
   * The controller's refusal when an edit would leave this pool with no host
   * in the fleet that could run it. It is not a field error -- every figure on
   * the form is valid, and it is the machines that cannot keep up -- so it is
   * answered with the consequence stated rather than a red label on a slider.
   */
  let stranding = $state('');

  async function finish(confirm = false): Promise<void> {
    if (submitting) return;
    touchStep(current);
    const outstanding = Object.keys(clientErrors);
    if (outstanding.length > 0) {
      // Show every one of them inline, then land on the first offending step.
      const next = { ...touched };
      for (const field of outstanding) next[field] = true;
      touched = next;
      goTo(stepForField(outstanding[0] ?? 'name', mode, editing));
      return;
    }
    submitting = true;
    try {
      const payload = toPoolBody(draft, { complete: editing });
      const saved =
        pool && pool.id
          ? await updatePool(
              pool.id,
              payload as Body<'updatePool'>,
              confirm ? { confirm: true } : undefined,
            )
          : await createPool(payload);
      toasts.success(
        editing ? `Saved ${payload.name}` : `Created ${payload.name}`,
        editing
          ? 'The scheduler picks the new settings up on its next pass.'
          : 'Runners appear as soon as a job asks for these labels.',
      );
      void fleet.reconcile();
      ondone(saved);
    } catch (cause) {
      if (cause instanceof ApiError && cause.isConflict) {
        stranding = cause.message;
        return;
      }
      if (cause instanceof ApiError) applyFieldErrors(cause);
      toasts.fromError(cause, editing ? 'The pool was not saved' : 'The pool was not created');
    } finally {
      submitting = false;
    }
  }
</script>

<Wizard
  class={className}
  {steps}
  bind:current
  {canAdvance}
  busy={submitting}
  finishLabel={editing ? 'Save changes' : 'Create pool'}
  cancelLabel="Cancel"
  onnext={() => touchStep(current)}
  onback={() => touchStep(current)}
  onfinish={() => void finish()}
  {oncancel}
>
  {#snippet children(step)}
    <div class="step" bind:this={panel} tabindex="-1" role="group" aria-label={step.title}>
      {#if step.id === 'mode'}
        <StepMode bind:mode hosts={fleet.hosts} hostsKnown={fleet.loaded} />
      {:else if step.id === 'target'}
        <StepTarget
          {draft}
          {errors}
          {touch}
          {installations}
          loading={installationsLoading}
          error={installationsError}
          onretry={() => (installationsAttempt += 1)}
          {groups}
          {groupsLoading}
          {groupsError}
          onspin={editing ? undefined : spin}
        />
      {:else if step.id === 'labels'}
        <StepLabels {draft} {errors} {touch} />
      {:else if step.id === 'docker'}
        <StepDocker {draft} {touch} />
      {:else if step.id === 'hosts'}
        <StepHosts
          {draft}
          {touch}
          hosts={fleet.hosts}
          hostsKnown={fleet.loaded}
          {verdict}
          {validating}
        />
      {:else if step.id === 'backend'}
        <StepBackend
          {draft}
          {errors}
          {touch}
          {offers}
          {platforms}
          hosts={fleet.hosts}
          hostsKnown={fleet.loaded}
          restricted={restrictedToHosts}
          bind:socketConfirmed
        />
      {:else if step.id === 'size'}
        <StepSize {draft} {errors} {touch} {defaults} {verdict} {validating} />
      {:else if step.id === 'scaling'}
        <StepScaling {draft} {errors} {touch} {verdict} {validating} following={followingMax} />
      {:else if step.id === 'runners'}
        <StepRunners {draft} {errors} {touch} {fleetDefaults} />
      {:else}
        <StepReview
          {draft}
          {body}
          {editing}
          {installationLabel}
          {mode}
          {verdict}
          {validating}
          error={validateError}
          ongoto={goTo}
        />
      {/if}

      {#if draft.sizing !== 'fixed' && (draft.backend === 'docker' || draft.backend === 'podman')}
        <PoolStartupStability
          warnings={verdict?.warnings ?? []}
          onfixed={() => stabilityRevision++}
        />
      {/if}

      {#if editing && mode === 'simple'}
        <div class="more">
          <p>
            Hosts, backend, size, scaling and the runner timings follow the fleet, and elastic CPU
            with them. Nothing typed here is lost on the way to them.
          </p>
          <Button size="sm" onclick={showEverySetting}>Show every setting</Button>
        </div>
      {/if}

      {#if blocking.length > 0}
        <div class="blocking">
          <p class="blocking-title">
            {steps[current + 1] ? 'Before the next step' : 'Before this pool can be saved'}
          </p>
          <ul>
            {#each blocking as message, index (index)}
              <li>{message}</li>
            {/each}
          </ul>
        </div>
      {/if}
    </div>
  {/snippet}
</Wizard>

<ConfirmDialog
  bind:open={
    () => stranding !== '',
    (open) => {
      if (!open) stranding = '';
    }
  }
  title="Save a pool with nowhere to run?"
  name={draft.name}
  description={stranding}
  consequences={[
    'Nothing has been saved yet.',
    'Saved as it is, jobs with these labels queue until a host that fits joins the fleet.',
    'Adjusting a host to match, or asking for less here, is the other way out.',
  ]}
  confirmLabel="Save anyway"
  busy={submitting}
  onconfirm={async () => {
    await finish(true);
    return true;
  }}
/>

<style>
  .step {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-5);
  }
  /*
    The step is focused programmatically when the wizard advances, so that a
    screen reader lands on the new content. That is not a keyboard tab, so
    :focus-visible is the right test: it draws no ring for the move the wizard
    made, and still draws one if somebody tabs here themselves.
  */
  .step:focus:not(:focus-visible) {
    outline: none;
  }
  .blocking {
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
  }
  .blocking-title {
    margin: 0;
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    color: var(--z-text-muted);
  }
  .blocking ul {
    margin: var(--z-space-1) 0 0;
    padding-left: var(--z-space-5);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .more {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
  }
  .more p {
    flex: 1 1 auto;
    margin: 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
  }
</style>
