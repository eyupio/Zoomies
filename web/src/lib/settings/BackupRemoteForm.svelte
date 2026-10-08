<!--
  Adding an offsite destination, and editing one.

  Two things about this form are deliberate. The secrets are write-only: an
  existing destination shows "stored" rather than a masked value, because a
  field that renders dots an operator can select is a field that has to have
  been sent the secret to render them. And **Test** works before **Save** does,
  against exactly the draft in the form -- proving a secret key when it is
  typed rather than at three in the morning is the whole reason this is a page
  and not a file.
-->
<script lang="ts">
  import { supportHint } from '$lib/errors';
  import {
    ApiError,
    checkDraftBackupRemote,
    createBackupRemote,
    updateBackupRemote,
  } from '$lib/api/client';
  import type { BackupRemote } from '$lib/api/types';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import Switch from '$lib/components/Switch.svelte';

  interface Props {
    open?: boolean;
    /** The destination being edited, or null when one is being added. */
    editing?: BackupRemote | null;
    onsaved: () => void;
    onclose: () => void;
  }

  let { open = $bindable(false), editing = null, onsaved, onclose }: Props = $props();

  /* The draft. Numbers are held as strings: '' and '0' are different answers,
     and keep: 0 is the one that keeps every copy. */
  let name = $state('');
  let endpoint = $state('');
  let region = $state('');
  let bucket = $state('');
  let prefix = $state('');
  let accessKeyID = $state('');
  let secretKey = $state('');
  let passphrase = $state('');
  let clearPassphrase = $state(false);
  let pathStyle = $state<'auto' | 'path' | 'host'>('auto');
  let keep = $state('0');
  let enabled = $state(true);

  let saving = $state(false);
  let testing = $state(false);
  let errors = $state<Record<string, string>>({});
  let refusal = $state('');

  /* Reset the draft whenever the dialog opens, so a cancelled edit does not
     leak into the next one. */
  $effect(() => {
    if (!open) return;
    const r = editing;
    name = r?.name ?? '';
    endpoint = r?.endpoint ?? '';
    region = r?.region ?? '';
    bucket = r?.bucket ?? '';
    prefix = r?.prefix ?? '';
    accessKeyID = r?.access_key_id ?? '';
    secretKey = '';
    passphrase = '';
    clearPassphrase = false;
    pathStyle = r?.path_style == null ? 'auto' : r.path_style ? 'path' : 'host';
    keep = String(r?.keep ?? 0);
    enabled = r ? !r.disabled : true;
    errors = {};
    refusal = '';
  });

  const styleChoices = [
    { value: 'auto', label: 'Chosen by the endpoint' },
    { value: 'path', label: 'Path style (MinIO, Ceph)' },
    { value: 'host', label: 'Virtual-hosted (AWS)' },
  ];

  /** What both Test and Save send. */
  function draft(): Record<string, unknown> {
    const body: Record<string, unknown> = {
      name: name.trim().toLowerCase(),
      endpoint: endpoint.trim(),
      region: region.trim(),
      bucket: bucket.trim(),
      prefix: prefix.trim(),
      access_key_id: accessKeyID.trim(),
      path_style: pathStyle === 'auto' ? null : pathStyle === 'path',
      keep: Number(keep) || 0,
      enabled,
    };
    // Absent leaves what is stored; an empty string clears it. A new
    // destination always sends both, because there is nothing to leave.
    if (secretKey !== '' || !editing) body.secret_access_key = secretKey;
    if (passphrase !== '') body.passphrase = passphrase;
    else if (clearPassphrase || !editing) body.passphrase = '';
    return body;
  }

  function carry(cause: unknown): void {
    if (cause instanceof ApiError) {
      errors = cause.fieldErrors();
      refusal = Object.keys(errors).length > 0 ? '' : cause.message;
      return;
    }
    refusal = `That could not be done. ${supportHint()}`;
  }

  async function test(): Promise<void> {
    testing = true;
    errors = {};
    refusal = '';
    try {
      const result = await checkDraftBackupRemote(draft());
      if (result.ok) {
        toasts.success('It answers', `${result.where} is reachable and writable.`);
      } else {
        refusal = result.error ?? 'The destination refused the request and gave no reason.';
      }
    } catch (cause) {
      carry(cause);
    } finally {
      testing = false;
    }
  }

  async function save(): Promise<void> {
    saving = true;
    errors = {};
    refusal = '';
    try {
      if (editing) {
        await updateBackupRemote(editing.name, draft());
        toasts.success('Saved', `${name.trim()} is what the copies go to now.`);
      } else {
        await createBackupRemote(draft());
        toasts.success('Added', `Backups are copied to ${name.trim()} from now on.`);
      }
      onsaved();
      open = false;
    } catch (cause) {
      carry(cause);
    } finally {
      saving = false;
    }
  }
</script>

<Dialog
  bind:open
  title={editing ? `Edit ${editing.name}` : 'Add a backup destination'}
  description="Any S3-compatible bucket: AWS, MinIO, Ceph, Backblaze B2, Cloudflare R2, Garage. Zoomies never creates the bucket."
  size="md"
  {onclose}
>
  <div class="form">
    <Field
      label="Name"
      hint="What this destination is called here. Lower-case letters, digits and dashes."
      error={errors.name}
    >
      <Input bind:value={name} placeholder="offsite" autocomplete="off" />
    </Field>

    <Field
      label="Endpoint"
      hint="The service's URL. The scheme decides whether the connection is encrypted, http:// sends the key and the backup in the clear."
      error={errors.endpoint}
    >
      <Input
        bind:value={endpoint}
        placeholder="https://s3.eu-west-2.amazonaws.com"
        autocomplete="off"
      />
    </Field>

    <div class="pair">
      <Field label="Bucket" error={errors.bucket}>
        <Input bind:value={bucket} placeholder="acme-zoomies" autocomplete="off" />
      </Field>
      <Field label="Region" hint="Empty is us-east-1." error={errors.region}>
        <Input bind:value={region} placeholder="eu-west-2" autocomplete="off" />
      </Field>
    </div>

    <Field
      label="Prefix"
      hint="The key prefix inside the bucket, so one bucket can hold several fleets."
      error={errors.prefix}
    >
      <Input bind:value={prefix} placeholder="prod" autocomplete="off" />
    </Field>

    <div class="pair">
      <Field label="Access key id" error={errors.access_key_id}>
        <Input bind:value={accessKeyID} placeholder="AKIA…" autocomplete="off" />
      </Field>
      <Field
        label="Secret access key"
        hint={editing?.has_secret_key
          ? 'One is stored. Type a new one to replace it, or leave this empty to keep it.'
          : 'Sealed with this fleet’s encryption key and never shown again.'}
        error={errors.secret_access_key}
      >
        <Input type="password" bind:value={secretKey} autocomplete="new-password" />
      </Field>
    </div>

    <Field
      label="Passphrase"
      hint="Seals the archive before it leaves this host, so the bucket holds something its owner cannot open. Nothing here can recover a lost one, keep it with the encryption key."
      error={errors.passphrase}
    >
      <Input type="password" bind:value={passphrase} autocomplete="new-password" />
    </Field>
    {#if editing?.encrypted}
      <label class="clear">
        <input type="checkbox" bind:checked={clearPassphrase} />
        Remove the stored passphrase and upload the plain archive
      </label>
    {/if}

    <div class="pair">
      <Field label="Request style" hint="Unset is right unless the service disagrees.">
        <Select bind:value={pathStyle} options={styleChoices} />
      </Field>
      <Field
        label="Copies to keep"
        hint="0 keeps every copy. This bucket's own retention, not the fleet's."
        error={errors.keep}
      >
        <Input type="number" min={0} bind:value={keep} />
      </Field>
    </div>

    <Switch bind:checked={enabled} label="Send backups to this destination" />

    {#if refusal}
      <p class="refusal">{refusal}</p>
    {/if}
  </div>

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
    <Button variant="secondary" onclick={test} loading={testing}>Test</Button>
    <Button variant="primary" onclick={save} loading={saving}>
      {editing ? 'Save' : 'Add destination'}
    </Button>
  {/snippet}
</Dialog>

<style>
  .form {
    display: grid;
    gap: var(--z-space-4);
  }
  .pair {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--z-space-4);
  }
  .clear {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin-top: calc(-1 * var(--z-space-2));
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .refusal {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-danger);
    overflow-wrap: anywhere;
  }

  @media (max-width: 40rem) {
    .pair {
      grid-template-columns: 1fr;
    }
  }
</style>
