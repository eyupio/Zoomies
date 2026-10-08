<!--
  Telling Kennel Club to stop looking at a repository.

  It silences that repository's errors, so it is an administrator's decision and
  it asks for a reason, in the length a waiver's is held to: the reason is shown
  beside the switch to whoever finds the repository quiet, which can be a year on
  and not the person who stopped it. Starting again asks for nothing.

  The controller says what is wrong with a request all at once, and the form puts
  each answer beside the field it is about. What is not about a field -- the role
  it needs, Kennel Club turned off -- is a toast, as it is everywhere else.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { ApiError, setKennelTracking } from '$lib/api/client';
  import type { KennelRepository } from '$lib/api/types';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import Textarea from '$lib/components/Textarea.svelte';
  import {
    reasonLength,
    TRACKING_NOW_STOPPED,
    TRACKING_STOP,
    trackingReasonHint,
    WAIVER_REASON_MIN,
  } from '$lib/kennel/words';
  import { toasts } from '$lib/state/toasts.svelte';

  interface Props {
    open?: boolean;
    repositoryId: string;
    name: string;
    /** The repository as the controller now has it, to replace what the page holds. */
    onstopped: (repository: KennelRepository) => void;
  }

  let { open = $bindable(false), repositoryId, name, onstopped }: Props = $props();

  let reason = $state('');
  let saving = $state(false);
  let errors = $state<Record<string, string>>({});

  // A dialog that opens on what the last one left behind has somebody else's
  // reason in it.
  $effect(() => {
    if (!open) return;
    untrack(() => {
      reason = '';
      saving = false;
      errors = {};
    });
  });

  const aside = $derived(
    Object.entries(errors)
      .filter(([field]) => field !== 'reason')
      .map(([field, message]) => `${field} ${message}`),
  );
  const ready = $derived(reasonLength(reason) >= WAIVER_REASON_MIN);

  async function submit(): Promise<void> {
    if (!ready) return;
    saving = true;
    errors = {};
    try {
      const repository = await setKennelTracking(repositoryId, {
        tracked: false,
        reason: reason.trim(),
      });
      open = false;
      onstopped(repository);
      toasts.success(TRACKING_NOW_STOPPED.title, TRACKING_NOW_STOPPED.detail);
    } catch (cause) {
      // A 422 is the form's to show, beside the field; every other refusal -- the
      // role it needs, Kennel Club turned off -- carries a sentence that says what
      // to do about it.
      if (cause instanceof ApiError && cause.status === 422) errors = cause.fieldErrors();
      else toasts.fromError(cause, 'That repository was not stopped');
    } finally {
      saving = false;
    }
  }
</script>

<Dialog bind:open title={TRACKING_STOP.title} description={TRACKING_STOP.description}>
  <form
    id="stop-tracking-form"
    class="form"
    novalidate
    onsubmit={(event) => {
      event.preventDefault();
      void submit();
    }}
  >
    <p class="what"><strong>{name}</strong></p>

    <ul class="consequences">
      {#each TRACKING_STOP.consequences as sentence (sentence)}<li>{sentence}</li>{/each}
    </ul>

    {#if aside.length > 0}
      <div class="aside" role="alert">
        {#each aside as sentence (sentence)}<p>{sentence}</p>{/each}
      </div>
    {/if}

    <Field
      label="Why Kennel Club should not look at it"
      hint={trackingReasonHint(reason)}
      error={errors.reason}
      required
    >
      {#snippet children({ id, describedBy, invalid })}
        <Textarea bind:value={reason} {id} {describedBy} {invalid} rows={4} />
      {/snippet}
    </Field>
  </form>

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
    <Button
      variant="primary"
      type="submit"
      form="stop-tracking-form"
      loading={saving}
      disabled={!ready}
    >
      Stop tracking
    </Button>
  {/snippet}
</Dialog>

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
  }
  .what {
    margin: 0;
    font-size: var(--z-text-sm);
    overflow-wrap: anywhere;
  }
  .consequences {
    margin: 0;
    padding-left: var(--z-space-5);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .consequences li + li {
    margin-top: var(--z-space-1);
  }
  .aside {
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-danger-border);
    border-radius: var(--z-radius-md);
    background: var(--z-danger-subtle);
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
  .aside p {
    margin: 0;
  }
</style>
