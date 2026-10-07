<!--
  Deciding that a finding is acceptable here.

  A waiver is a person's recorded decision: a reason, an owner and an end. The
  form asks for the reason and the end; the owner is whoever is signed in. It
  asks for a reason because the audit log is read a year later by somebody who
  was not in the room, and for an end because a decision nobody is asked to make
  again is how an exception becomes the rule.

  The controller says what is wrong with a request all at once, and the form
  puts each answer beside the field it is about. What is not about a field -- a
  refusal for the role, a repository that has too many waivers -- is a toast, as
  it is everywhere else.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { ApiError, waiveKennelFinding } from '$lib/api/client';
  import type { KennelFinding, KennelRepository } from '$lib/api/types';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import Select from '$lib/components/Select.svelte';
  import Textarea from '$lib/components/Textarea.svelte';
  import { formatAbsolute } from '$lib/format';
  import {
    reasonHint,
    reasonLength,
    waiverEndsAt,
    WAIVER_DEFAULT_DAYS,
    WAIVER_EXPIRY_CHOICES,
    WAIVER_REASON_MIN,
  } from '$lib/kennel/words';
  import { toasts } from '$lib/state/toasts.svelte';
  import { severityStatus } from '$lib/status';

  interface Props {
    open?: boolean;
    repositoryId: string;
    /** The finding being waived. Held by the page, so the dialog can close without losing it mid-animation. */
    finding: KennelFinding | null;
    /** The repository as the controller worked it out again, to replace what the page holds. */
    onwaived: (repository: KennelRepository) => void;
  }

  let { open = $bindable(false), repositoryId, finding, onwaived }: Props = $props();

  let reason = $state('');
  let days = $state(WAIVER_DEFAULT_DAYS);
  let saving = $state(false);
  let errors = $state<Record<string, string>>({});

  // A dialog that opens on what the last one left behind has somebody else's
  // reason in it.
  $effect(() => {
    if (!open) return;
    untrack(() => {
      reason = '';
      days = WAIVER_DEFAULT_DAYS;
      saving = false;
      errors = {};
    });
  });

  /** What the controller said that is not about a field this form has. */
  const FIELD_LABEL: Record<string, string> = { code: 'The check', subject: 'The subject' };
  const aside = $derived(
    Object.entries(errors)
      .filter(([field]) => field !== 'reason' && field !== 'expires_at')
      .map(([field, message]) => `${FIELD_LABEL[field] ?? field} ${message}`),
  );

  const ready = $derived(reasonLength(reason) >= WAIVER_REASON_MIN);

  async function submit(): Promise<void> {
    if (!finding || !ready) return;
    saving = true;
    errors = {};
    try {
      const repository = await waiveKennelFinding(repositoryId, {
        code: finding.code,
        subject: finding.subject,
        reason: reason.trim(),
        expires_at: waiverEndsAt(Number(days), new Date()),
      });
      const waiver = repository.waived.find(
        (entry) => entry.finding.code === finding.code && entry.finding.subject === finding.subject,
      )?.waiver;
      open = false;
      onwaived(repository);
      toasts.success(
        'Finding waived',
        waiver
          ? `It is listed under Waived until ${formatAbsolute(waiver.expires_at)}.`
          : 'It is listed under Waived.',
      );
    } catch (cause) {
      // A 422 is the form's to show, beside the fields; every other refusal --
      // the role it needs, a repository with too many waivers, Kennel Club
      // turned off -- carries a sentence that says what to do about it.
      if (cause instanceof ApiError && cause.status === 422) errors = cause.fieldErrors();
      else toasts.fromError(cause, 'That finding was not waived');
    } finally {
      saving = false;
    }
  }
</script>

<Dialog
  bind:open
  title="Waive this finding"
  description="Kennel Club stops counting it until the waiver ends. It stays listed under Waived, with your name and your reason, and comes back by itself if it gets worse."
>
  {#if finding}
    <form
      id="waive-finding-form"
      class="form"
      novalidate
      onsubmit={(event) => {
        event.preventDefault();
        void submit();
      }}
    >
      <p class="what">
        <Badge status={severityStatus(finding.severity)} size="sm" />
        <strong>{finding.title}</strong>
      </p>

      {#if aside.length > 0}
        <div class="aside" role="alert">
          {#each aside as sentence (sentence)}<p>{sentence}</p>{/each}
        </div>
      {/if}

      <Field
        label="Why this is acceptable here"
        hint={reasonHint(reason)}
        error={errors.reason}
        required
      >
        {#snippet children({ id, describedBy, invalid })}
          <Textarea bind:value={reason} {id} {describedBy} {invalid} rows={4} />
        {/snippet}
      </Field>

      <Field label="Waive for" hint="Then Kennel Club asks again." error={errors.expires_at}>
        {#snippet children({ id, describedBy, invalid })}
          <Select bind:value={days} options={WAIVER_EXPIRY_CHOICES} {id} {describedBy} {invalid} />
        {/snippet}
      </Field>
    </form>
  {/if}

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
    <Button
      variant="primary"
      type="submit"
      form="waive-finding-form"
      loading={saving}
      disabled={!ready}
    >
      Waive
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
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-sm);
    overflow-wrap: anywhere;
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
