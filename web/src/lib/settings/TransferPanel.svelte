<script lang="ts">
  import { onMount } from 'svelte';
  import Button from '$lib/components/Button.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import RestartWait from './RestartWait.svelte';
  import { saveBlob } from './backups';
  import {
    ApiError,
    getTransferPreparation,
    prepareInstanceTransfer,
    cancelInstanceTransfer,
    exportInstanceTransfer,
    importInstanceTransfer,
    stageRestore,
    applyRestore,
  } from '$lib/api/client';
  import type { Schemas, Backup } from '$lib/api/types';

  let progress = $state<Schemas['TransferProgress'] | null>(null);
  let error = $state('');
  let busy = $state(false);
  let passphrase = $state('');
  let again = $state('');
  let file = $state<File | null>(null);
  let incomingPassphrase = $state('');
  let sourceStopped = $state(false);
  let incoming = $state<Backup | null>(null);
  let uploaded = $state<number | null>(null);
  let restarting = $state(false);

  function describe(cause: unknown) {
    error =
      cause instanceof ApiError || cause instanceof Error
        ? cause.message
        : 'The transfer did not complete. Try again.';
  }
  onMount(() => {
    const abort = new AbortController();
    const poll = () => {
      void getTransferPreparation(abort.signal)
        .then((value) => {
          progress = value;
        })
        .catch((cause) => {
          if (!abort.signal.aborted) describe(cause);
        });
    };
    poll();
    const timer = setInterval(poll, 3000);
    return () => {
      abort.abort();
      clearInterval(timer);
    };
  });

  async function prepare() {
    busy = true;
    error = '';
    try {
      progress = await prepareInstanceTransfer();
    } catch (cause) {
      describe(cause);
    } finally {
      busy = false;
    }
  }
  async function cancel() {
    busy = true;
    error = '';
    try {
      await cancelInstanceTransfer();
      progress = await getTransferPreparation();
    } catch (cause) {
      describe(cause);
    } finally {
      busy = false;
    }
  }
  async function download() {
    busy = true;
    error = '';
    try {
      const blob = await exportInstanceTransfer(passphrase);
      saveBlob(blob, 'zoomies-instance.zbk');
      passphrase = '';
      again = '';
    } catch (cause) {
      describe(cause);
    } finally {
      busy = false;
    }
  }
  async function upload() {
    if (!file) return;
    busy = true;
    error = '';
    uploaded = 0;
    incoming = null;
    try {
      incoming = await importInstanceTransfer(file, incomingPassphrase, (value) => {
        uploaded = value;
      });
      incomingPassphrase = '';
    } catch (cause) {
      describe(cause);
    } finally {
      busy = false;
      uploaded = null;
    }
  }
  async function apply() {
    if (!incoming || !sourceStopped) return;
    busy = true;
    error = '';
    try {
      await stageRestore(incoming.id, { source_stopped: true });
      await applyRestore();
      restarting = true;
    } catch (cause) {
      describe(cause);
    } finally {
      busy = false;
    }
  }
</script>

<section class="transfer" aria-labelledby="instance-transfer-title">
  <h2 id="instance-transfer-title">Move a complete instance</h2>
  <p>
    Keep your fleet configuration, history and credentials when moving to another controller. The
    destination keeps its own operator access and encryption key.
  </p>
  {#if restarting}
    <RestartWait reason="Importing the complete instance and keeping it fenced for verification." />
  {:else}
    <details>
      <summary>Move this instance to another controller</summary>
      <div class="steps">
        <p>
          Prepare in one click. New work pauses, running jobs finish naturally, and Zoomies drains
          idle runners and checks cleanup before fencing the instance for cutover.
        </p>
        {#if progress?.ready}
          <p role="status">Ready to export. The instance is fenced.</p>
        {:else if progress?.draining}
          <p role="status">
            Preparing: {progress.busy_runners} busy runners, {progress.live_runners} runners remaining,
            {progress.pending_cleanup} awaiting cleanup, {progress.active_jobs} running jobs and {progress.machine_operations}
            machine operations. Offline hosts or unfinished cleanup need attention before export.
          </p>
          <Button onclick={() => void cancel()} loading={busy}>Cancel preparation</Button>
        {:else}
          <Button
            variant="primary"
            onclick={() => void prepare()}
            loading={busy}
            disabled={!progress}>Prepare for transfer</Button
          >
        {/if}
        {#if progress?.ready}
          <Field
            label="Archive passphrase"
            hint="At least eight characters. Keep it to open the archive on the destination."
          >
            {#snippet children({ id, describedBy, invalid })}<Input
                {id}
                {describedBy}
                {invalid}
                type="password"
                autocomplete="new-password"
                bind:value={passphrase}
              />{/snippet}
          </Field>
          <Field label="Passphrase again">
            {#snippet children({ id, describedBy, invalid })}<Input
                {id}
                {describedBy}
                {invalid}
                type="password"
                autocomplete="new-password"
                bind:value={again}
              />{/snippet}
          </Field>
          <Button
            variant="primary"
            onclick={() => void download()}
            loading={busy}
            disabled={passphrase.length < 8 || passphrase !== again}
            >Download complete instance</Button
          >
          <p>
            After downloading, stop this controller before importing. Keep it stopped while the
            destination runs. Repoint agents and GitHub webhooks, verify machine ownership, then
            lift the destination's recovery fence. To abandon cutover, first check that no
            destination is running, then lift this instance's recovery fence.
          </p>
        {/if}
      </div>
    </details>
    <details>
      <summary>Bring an instance to this controller</summary>
      <div class="steps">
        <p>
          Use an empty destination with its own operator account or API token. Its current bootstrap
          database is kept for rollback. The imported instance starts fenced; existing sessions and
          unused enrolment tokens are invalidated.
        </p>
        <label class="file-label" for="instance-transfer-file">Encrypted instance archive</label>
        <input
          id="instance-transfer-file"
          type="file"
          accept=".zbk,.enc"
          disabled={busy}
          onchange={(event) => {
            file = event.currentTarget.files?.[0] ?? null;
            incoming = null;
          }}
        />
        <Field label="Archive passphrase">
          {#snippet children({ id, describedBy, invalid })}<Input
              {id}
              {describedBy}
              {invalid}
              type="password"
              autocomplete="off"
              bind:value={incomingPassphrase}
            />{/snippet}
        </Field>
        <Button onclick={() => void upload()} loading={busy} disabled={!file || !incomingPassphrase}
          >Verify archive</Button
        >
        {#if uploaded !== null}<progress
            max="1"
            value={uploaded}
            aria-label="Instance archive upload"
          ></progress>{/if}
        {#if incoming?.transfer}
          <p>
            Archive verified: {incoming.transfer.inventory.installations ?? 0} installations, {incoming
              .transfer.inventory.hosts ?? 0} hosts, {incoming.transfer.inventory.jobs ?? 0} jobs and
            {incoming.transfer.inventory.runner_sessions ?? 0} completed runner sessions.
          </p>
          <label class="ack"
            ><input type="checkbox" bind:checked={sourceStopped} />The source controller is stopped
            and will stay stopped while this instance runs.</label
          >
          <Button
            variant="primary"
            onclick={() => void apply()}
            loading={busy}
            disabled={!sourceStopped}>Import and restart</Button
          >
          <p>
            Sign in again using this destination's operator credentials after restart. Team accounts
            using external sign-in need a destination identity mapping.
          </p>
        {/if}
      </div>
    </details>
  {/if}
  {#if error}<p class="error" role="alert">{error}</p>{/if}
</section>

<style>
  .transfer {
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    padding: var(--z-space-5);
    margin-bottom: var(--z-space-5);
    background: var(--z-surface);
  }
  h2 {
    font-size: var(--z-text-lg);
    font-weight: 600;
  }
  p {
    color: var(--z-text-muted);
    margin-block: var(--z-space-3);
  }
  details {
    margin-top: var(--z-space-4);
  }
  summary {
    cursor: pointer;
    font-weight: 600;
  }
  .steps {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: var(--z-space-3);
    margin-top: var(--z-space-3);
  }
  .steps :global(.field) {
    width: 100%;
  }
  .ack {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
  }
  .file-label {
    font-weight: 500;
  }
  .error {
    color: var(--z-danger);
  }
</style>
