<!--
  Telling Kennel Club to stop looking at several repositories at once.

  It is the single repository's dialog (TrackingDialog) asked of a selection, and
  it asks the same of an administrator: a reason, in the length a waiver's is held
  to, because the reason is shown beside each repository's switch to whoever finds
  it quiet a year on. One reason is written once and sent for each, so each
  repository has its own request and its own audit entry, naming the person.

  The requests go one after another, not together. Every repository is its own
  decision in the audit log, so they are kept in the order they were ticked, and
  when one is refused the person is told which, by name, instead of "some".

  A 422 on the reason is the form's to show, beside the field, and nothing has
  been stopped by then. Anything else is counted and named once it is over.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { ApiError, setKennelTracking } from '$lib/api/client';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import Textarea from '$lib/components/Textarea.svelte';
  import {
    alreadyStoppedSentence,
    reasonLength,
    trackingReasonHint,
    trackingStopMany,
    WAIVER_REASON_MIN,
    type StopFailure,
  } from '$lib/kennel/words';

  /** What happened, for the page to say and to decide whether to keep the selection. */
  export interface BulkStopResult {
    stopped: string[];
    failures: StopFailure[];
  }

  interface Props {
    open?: boolean;
    /** What is to be stopped, in the order it was ticked. */
    repositories: { id: string; name: string }[];
    /** Ticked, but already not tracked, so left alone. */
    alreadyStopped?: number;
    onfinished: (result: BulkStopResult) => void;
    /** Cancel, Escape and the backdrop. Nothing has been stopped. */
    oncancel: () => void;
  }

  let {
    open = $bindable(false),
    repositories,
    alreadyStopped = 0,
    onfinished,
    oncancel,
  }: Props = $props();

  // How many names to list before saying "and N more": enough to check the
  // selection by eye, few enough that the reason is still on the screen.
  const NAMES_SHOWN = 8;

  let reason = $state('');
  let saving = $state(false);
  let done = $state(0);
  let errors = $state<Record<string, string>>({});

  // A dialog that opens on what the last one left behind has somebody else's
  // reason in it.
  $effect(() => {
    if (!open) return;
    untrack(() => {
      reason = '';
      saving = false;
      done = 0;
      errors = {};
    });
  });

  const wording = $derived(trackingStopMany(repositories.length));
  const names = $derived(repositories.slice(0, NAMES_SHOWN));
  const more = $derived(Math.max(0, repositories.length - NAMES_SHOWN));
  const left = $derived(alreadyStoppedSentence(alreadyStopped));
  const aside = $derived(
    Object.entries(errors)
      .filter(([field]) => field !== 'reason')
      .map(([field, message]) => `${field} ${message}`),
  );
  const ready = $derived(reasonLength(reason) >= WAIVER_REASON_MIN);
  const several = $derived(repositories.length > 1);

  function sentenceFor(cause: unknown): string {
    return cause instanceof ApiError ? cause.message : 'The request did not go through.';
  }

  async function submit(): Promise<void> {
    if (!ready || saving) return;
    saving = true;
    done = 0;
    errors = {};
    const sent = reason.trim();
    const stopped: string[] = [];
    const failures: StopFailure[] = [];
    for (const [index, repository] of repositories.entries()) {
      try {
        await setKennelTracking(repository.id, { tracked: false, reason: sent });
        stopped.push(repository.id);
      } catch (cause) {
        // The reason itself was refused, and it is the same reason for each, so
        // nothing was stopped: the form says what is wrong beside the field.
        if (cause instanceof ApiError && cause.status === 422 && index === 0) {
          errors = cause.fieldErrors();
          saving = false;
          return;
        }
        failures.push({ name: repository.name, message: sentenceFor(cause) });
        // No role, or no session: every request after this one would say the same,
        // so they are not sent, and are counted as not stopped.
        if (cause instanceof ApiError && (cause.status === 401 || cause.status === 403)) {
          for (const rest of repositories.slice(index + 1)) {
            failures.push({
              name: rest.name,
              message: 'Not tried: the first request was refused.',
            });
          }
          break;
        }
      }
      done = index + 1;
    }
    saving = false;
    open = false;
    onfinished({ stopped, failures });
  }
</script>

<Dialog
  bind:open
  title={wording.title}
  description={wording.description}
  dismissible={!saving}
  onclose={oncancel}
>
  <form
    id="bulk-stop-tracking-form"
    class="form"
    novalidate
    onsubmit={(event) => {
      event.preventDefault();
      void submit();
    }}
  >
    <ul class="names" aria-label="Repositories to stop tracking">
      {#each names as repository (repository.id)}<li>{repository.name}</li>{/each}
      {#if more > 0}<li class="more">and {more} more</li>{/if}
    </ul>

    {#if left}<p class="left">{left}</p>{/if}

    <ul class="consequences">
      {#each wording.consequences as sentence (sentence)}<li>{sentence}</li>{/each}
    </ul>

    {#if aside.length > 0}
      <div class="aside" role="alert">
        {#each aside as sentence (sentence)}<p>{sentence}</p>{/each}
      </div>
    {/if}

    <Field
      label="Why Kennel Club should not look at them"
      hint={trackingReasonHint(reason, several)}
      error={errors.reason}
      required
    >
      {#snippet children({ id, describedBy, invalid })}
        <Textarea bind:value={reason} {id} {describedBy} {invalid} rows={4} />
      {/snippet}
    </Field>

    {#if saving}
      <p class="progress" role="status">Stopped {done} of {repositories.length}</p>
    {/if}
  </form>

  {#snippet footer()}
    <Button variant="ghost" disabled={saving} onclick={oncancel}>Cancel</Button>
    <Button
      variant="primary"
      type="submit"
      form="bulk-stop-tracking-form"
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
  .names {
    margin: 0;
    padding-left: var(--z-space-5);
    font-size: var(--z-text-sm);
    overflow-wrap: anywhere;
    /* A hundred ticked rows must not push the reason off the screen. */
    max-height: 9rem;
    overflow-y: auto;
  }
  .more {
    color: var(--z-text-muted);
    list-style: none;
    margin-left: calc(-1 * var(--z-space-5));
  }
  .left {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
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
  .progress {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
  }
</style>
