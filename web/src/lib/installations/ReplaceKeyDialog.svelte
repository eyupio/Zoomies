<!--
  A new private key for an installation that already exists.

  The commonest credential mistake is pasting the wrong .pem: GitHub hands the
  file over exactly once, at the end of a form with several other identifiers on
  it. The route to replace it has always existed and nothing surfaced it, so the
  only recovery was to disconnect and start again -- which takes the
  installation's pools and their runner rows with it. A key is replaceable; a
  fleet should not have to be.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { ApiError, updateInstallation } from '$lib/api/client';
  import type { Installation } from '$lib/api/types';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import Textarea from '$lib/components/Textarea.svelte';

  interface Props {
    open?: boolean;
    installation: Installation | null;
    /** Raised once the key is stored, so the page can re-verify. */
    onreplaced?: (installation: Installation) => void;
    onclose?: () => void;
  }

  let { open = $bindable(false), installation, onreplaced, onclose }: Props = $props();

  let key = $state('');
  let saving = $state(false);
  let errors = $state<Record<string, string>>({});

  // Reload for whichever installation is open, and only then: a key half typed
  // must survive an SSE update to the row behind the dialog.
  let loadedFor = $state<string | null>(null);
  $effect(() => {
    if (!open || !installation) {
      loadedFor = null;
      return;
    }
    if (loadedFor === installation.id) return;
    loadedFor = installation.id ?? null;
    key = '';
    errors = {};
  });

  // A refusal is about the text that was sent. Left in place it also disables
  // Replace, and the next save -- the only thing that cleared it -- would be
  // the one it prevents, so pasting the right file could never be submitted.
  // Only this key is dropped, and errors is read untracked, so a refusal that
  // has just arrived is not cleared by the effect it did not come from.
  $effect(() => {
    void key;
    untrack(() => {
      if (errors.private_key) delete errors.private_key;
    });
  });

  const looksLikePEM = $derived(key.trim() === '' || key.includes('BEGIN') || key.includes('KEY'));
  const keyError = $derived(
    errors.private_key ??
      (looksLikePEM
        ? ''
        : 'That does not look like a .pem file. Paste the whole thing, including the BEGIN and END lines.'),
  );

  async function save(): Promise<void> {
    if (!installation?.id || key.trim() === '') return;
    saving = true;
    errors = {};
    try {
      const updated = await updateInstallation(installation.id, { private_key: key.trim() });
      toasts.success(`${installation.target} has a new key`, 'Verifying it now.');
      open = false;
      onreplaced?.(updated ?? installation);
      onclose?.();
    } catch (cause) {
      if (cause instanceof ApiError) errors = cause.fieldErrors();
      toasts.fromError(cause, 'The key was not replaced');
    } finally {
      saving = false;
    }
  }
</script>

<Dialog
  bind:open
  title="Replace {installation?.target || 'installation'}'s private key"
  description="The new key is sealed with this instance's encryption key before it is stored, and is never shown again."
  {onclose}
>
  <Field
    label="Private key"
    error={keyError}
    hint="The .pem file GitHub gave you when the App was created, or a fresh one generated under the App's settings."
    required
  >
    {#snippet children({ id, describedBy, invalid })}
      <Textarea
        bind:value={key}
        {id}
        {describedBy}
        invalid={invalid || Boolean(errors.private_key)}
        rows={6}
        placeholder="-----BEGIN RSA PRIVATE KEY-----"
      />
    {/snippet}
  </Field>

  {#snippet footer()}
    <Button
      variant="ghost"
      onclick={() => {
        open = false;
        onclose?.();
      }}
    >
      Cancel
    </Button>
    <Button
      variant="primary"
      loading={saving}
      disabled={key.trim() === '' || Boolean(keyError)}
      onclick={save}
    >
      Replace the key
    </Button>
  {/snippet}
</Dialog>
