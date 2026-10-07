<!--
  Deciding that one warning on one host is deliberate.

  Nothing here touches the host: the controller records the decision, stops
  counting the check, and the host's own `zoomies doctor` still lists it. The
  form asks for a reason because everyone who can see the host reads it and the
  audit log keeps it, and for an end because a decision nobody is asked to make
  again is how a kernel pinned below the baseline rots.

  The controller says what is wrong with a request all at once, and the form puts
  each answer beside the field it is about. What is not about a field -- a refusal
  for the role, a check that changed while the person was deciding -- is a toast,
  as it is everywhere else.
-->
<script lang="ts">
  import { tick, untrack } from 'svelte';
  import { acceptHostCheck, ApiError } from '$lib/api/client';
  import type { Host } from '$lib/api/types';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import Select from '$lib/components/Select.svelte';
  import Textarea from '$lib/components/Textarea.svelte';
  import { niceValue } from '$lib/hosts/check-value';
  import type { DoctorResult } from '$lib/hosts/health';
  import {
    ACCEPT_DEFAULT_DAYS,
    ACCEPT_EXPIRY_CHOICES,
    acceptEndsAt,
    untilDay,
  } from '$lib/hosts/accept';
  import { REASON_MIN, reasonHint, reasonLength } from '$lib/reason';
  import { toasts } from '$lib/state/toasts.svelte';

  interface Props {
    open?: boolean;
    hostId: string;
    hostName: string;
    /** The check being accepted. Held by the page, so the dialog can close without losing it mid-animation. */
    check: DoctorResult | null;
    /** The host as the controller answered, to replace what the page holds. */
    onaccepted: (host: Host) => void;
  }

  let { open = $bindable(false), hostId, hostName, check, onaccepted }: Props = $props();

  let reason = $state('');
  let days = $state<string>(ACCEPT_DEFAULT_DAYS);
  let saving = $state(false);
  let field = $state<HTMLTextAreaElement | null>(null);
  let errors = $state<Record<string, string>>({});

  // A dialog that opens on what the last one left behind has somebody else's
  // reason in it.
  $effect(() => {
    if (!open) return;
    untrack(() => {
      reason = '';
      days = ACCEPT_DEFAULT_DAYS;
      saving = false;
      errors = {};
    });
    // The reason is the one thing to type, so the cursor starts in it.
    void tick().then(() => requestAnimationFrame(() => field?.focus()));
  });

  /** What the controller said that is not about a field this form has. */
  const FIELD_LABEL: Record<string, string> = { check_id: 'The check', seen_current: 'The value' };
  const aside = $derived(
    Object.entries(errors)
      .filter(([field]) => field !== 'reason' && field !== 'expires_at')
      .map(([field, message]) => `${FIELD_LABEL[field] ?? field} ${message}`),
  );

  const ready = $derived(reasonLength(reason) >= REASON_MIN);

  async function submit(): Promise<void> {
    if (!check || !ready) return;
    saving = true;
    errors = {};
    try {
      // The value the person was looking at, not the one a newer report may
      // have brought: the controller refuses with 409 if they differ.
      const host = await acceptHostCheck(hostId, {
        check_id: check.id,
        seen_current: check.current,
        reason: reason.trim(),
        expires_at: acceptEndsAt(Number(days), new Date()),
      });
      const until = host.doctor?.results.find((r) => r.id === check.id)?.accepted?.expires_at;
      open = false;
      onaccepted(host);
      toasts.success(
        `Accepted: ${check.title}`,
        `On ${hostName}${until ? `, until ${untilDay(until)}` : ''}.`,
      );
    } catch (cause) {
      // A 422 is the form's to show, beside the fields; every other refusal
      // carries a sentence that says what to do about it.
      if (cause instanceof ApiError && cause.status === 422) errors = cause.fieldErrors();
      else toasts.fromError(cause, 'That check was not accepted');
    } finally {
      saving = false;
    }
  }
</script>

<Dialog
  bind:open
  title="Accept this check as deliberate"
  description="Zoomies stops counting {check?.title ??
    'it'} on {hostName}. It changes nothing on the host, and zoomies doctor run on the host still lists it. If the value changes, it counts again."
>
  {#if check}
    <form
      id="accept-check-form"
      class="form"
      novalidate
      onsubmit={(event) => {
        event.preventDefault();
        void submit();
      }}
    >
      <p class="what">
        <Badge label="Warning" tone="pending" size="sm" />
        <strong>{check.title}</strong>
        <span class="now"
          >Now “{niceValue(check.current) || '—'}” — recommended “{niceValue(check.recommended) ||
            '—'}”</span
        >
      </p>

      {#if aside.length > 0}
        <div class="aside" role="alert">
          {#each aside as sentence (sentence)}<p>{sentence}</p>{/each}
        </div>
      {/if}

      <Field
        label="Why this is deliberate"
        hint="{reasonHint(
          reason,
          'deliberate',
        )} Everyone who can see this host reads it, and it goes in the audit log."
        error={errors.reason}
        required
      >
        {#snippet children({ id, describedBy, invalid })}
          <Textarea
            bind:value={reason}
            {id}
            {describedBy}
            {invalid}
            rows={4}
            bind:element={field}
          />
        {/snippet}
      </Field>

      <Field
        label="Accept until"
        hint="When it ends the check counts again. A changed value ends it sooner."
        error={errors.expires_at}
      >
        {#snippet children({ id, describedBy, invalid })}
          <Select bind:value={days} options={ACCEPT_EXPIRY_CHOICES} {id} {describedBy} {invalid} />
        {/snippet}
      </Field>
    </form>
  {/if}

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
    <Button
      variant="primary"
      type="submit"
      form="accept-check-form"
      loading={saving}
      disabled={!ready}
    >
      Accept check
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
  .now {
    color: var(--z-text-muted);
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
