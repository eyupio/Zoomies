<!--
  Backups.

  Everything a fleet is, in one copy that can be put back. The panel is built
  around the three moments an operator has with backups: taking one (a button,
  and a schedule the controller keeps on its own); getting one off the machine
  (download, optionally sealed with a passphrase) or back onto it (upload); and
  the one that replaces the fleet, which is staged, confirmed by name, and
  applied by a restart this page watches.

  A restore never happens under the running controller, because nothing can
  swap the database a process has open. The page says so rather than
  pretending: "restore" stages, "restart" applies, and the banner between them
  is the honest state.
-->
<script lang="ts">
  import { tableLayout } from '$lib/actions/tableLayout';
  import {
    ArchiveRestore,
    CircleCheck,
    DatabaseBackup,
    Download,
    HardDrive,
    Lock,
    Scissors,
    ShieldCheck,
    Trash2,
    TriangleAlert,
    Upload,
  } from '@lucide/svelte';
  import {
    ApiError,
    applyRestore,
    backupDownloadUrl,
    cancelRestore,
    deleteBackup,
    dismissRestoreOutcome,
    downloadBackupEncrypted,
    listBackups,
    pruneBackups,
    stageRestore,
    takeBackup,
    uploadBackup,
    verifyBackup,
  } from '$lib/api/client';
  import type { Backup, BackupVerification, Backups } from '$lib/api/types';
  import { formatAbsolute, formatBytes, pluralise } from '$lib/format';
  import { toasts } from '$lib/state/toasts.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import LoadingBoundary from '$lib/components/LoadingBoundary.svelte';
  import Pagination from '$lib/components/Pagination.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import RowActions from '$lib/components/RowActions.svelte';
  import type { RowAction } from '$lib/components/RowActions.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import Tooltip from '$lib/components/Tooltip.svelte';
  import BackupRemotes from './BackupRemotes.svelte';
  import BackupSchedule from './BackupSchedule.svelte';
  import RestartWait from './RestartWait.svelte';
  import { describeInterval, keyStatus, saveBlob, schemaShort, sourceOf } from './backups';
  import PageHeader from '$lib/components/PageHeader.svelte';

  let page = $state<Backups | null>(null);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let reload = $state(0);

  $effect(() => {
    void reload;
    const controller = new AbortController();
    loading = true;
    void listBackups(controller.signal)
      .then((result) => {
        page = result;
        error = null;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        error = cause;
      })
      .finally(() => (loading = false));
    return () => controller.abort();
  });

  const items = $derived<readonly Backup[]>(page?.items ?? []);
  const newest = $derived(items.find((b) => b.source !== 'uploaded' && !b.problem) ?? null);
  const staged = $derived(page?.staged_restore ?? null);
  const lastRestore = $derived(page?.last_restore ?? null);
  const schedule = $derived(page?.schedule);
  /* The destinations a copy actually goes to: not the ones switched off, not
     the stored ones the file overrides, and not the ones whose secrets this
     controller's key will not open. Anything else would promise an offsite
     copy that is not being made. */
  const sending = $derived(
    (page?.remotes ?? []).filter((r) => !r.disabled && !r.shadowed && !r.problem),
  );

  /*
    The list pages.

    A fleet keeping ninety daily copies is ninety rows, and on a phone each one
    is a card six lines tall -- the restore an operator came for is most of a
    minute of scrolling away. Newest first is the order the API gives them in,
    so the first page is always the one that matters, and the size is
    remembered nowhere: this is a page somebody opens twice a year.
  */
  const PER_PAGE = [10, 25, 50] as const;
  let limit = $state<number>(PER_PAGE[0]);
  let offset = $state(0);
  const shown = $derived(items.slice(offset, offset + limit));
  // A delete or a prune can empty the page that was being read. Step back to
  // the last one that has rows rather than showing an empty list with a
  // count that says otherwise.
  $effect(() => {
    if (offset > 0 && offset >= items.length) {
      offset = Math.max(0, (Math.ceil(items.length / limit) - 1) * limit);
    }
  });

  /* -- taking one ----------------------------------------------------------- */

  let taking = $state(false);

  async function take(): Promise<void> {
    taking = true;
    try {
      const taken = await takeBackup();
      // Where the copy went, and where it is going: the offsite pass is part
      // of taking a backup, but it happens after the request has answered, so
      // an operator who has just pressed the button is told rather than left
      // to guess from a "last copy" time that has not moved yet.
      toasts.success(
        'Backup taken',
        `${taken.id}, ${formatBytes(taken.bytes)}, checked and written to ${page?.directory ?? 'the backup directory'}.` +
          (sending.length > 0
            ? ` It is on its way to ${pluralise(sending.length, 'destination', 'destinations')}.`
            : ''),
      );
      reload += 1;
    } catch (cause) {
      toasts.fromError(cause, 'That backup was not taken');
    } finally {
      taking = false;
    }
  }

  /* -- retention, on demand ------------------------------------------------- */

  /*
    Retention otherwise runs as part of taking a backup. An operator who has
    just lowered backup.keep from thirty to seven is then holding thirty until
    the next one -- and with backup.interval off, holding thirty forever. This
    is the button that says "now", and it prunes the buckets as well, because
    the two numbers are set on the same page and neither of them should need a
    backup to take effect.
  */
  let pruneOpen = $state(false);
  let pruneBusy = $state(false);

  async function prune(): Promise<boolean> {
    pruneBusy = true;
    try {
      const report = await pruneBackups();
      const offsite = report.remotes.reduce((n, r) => n + r.removed.length, 0);
      const refused = report.remotes.filter((r) => r.error);
      const where = [
        `${pluralise(report.removed.length, 'copy', 'copies')} here`,
        ...(report.remotes.length > 0 ? [`${offsite} offsite`] : []),
      ].join(', ');
      if (report.error || refused.length > 0) {
        toasts.warning(
          'Retention was partly applied',
          [report.error, ...refused.map((r) => `${r.name}: ${r.error}`)].filter(Boolean).join(' '),
        );
      } else if (report.removed.length === 0 && offsite === 0) {
        toasts.success('Nothing to remove', 'Every copy is one this fleet is meant to be keeping.');
      } else {
        toasts.success('Retention applied', `Removed ${where}.`);
      }
      reload += 1;
      return true;
    } catch (cause) {
      toasts.fromError(cause, 'Retention was not applied');
      return false;
    } finally {
      pruneBusy = false;
    }
  }

  /* -- verifying ------------------------------------------------------------ */

  let verifyOpen = $state(false);
  let verifying = $state<Backup | null>(null);
  let verification = $state<BackupVerification | null>(null);
  let verifyBusy = $state(false);

  async function verify(backup: Backup): Promise<void> {
    verifying = backup;
    verification = null;
    verifyOpen = true;
    verifyBusy = true;
    try {
      verification = await verifyBackup(backup.id);
    } catch (cause) {
      verifyOpen = false;
      toasts.fromError(cause, 'That backup could not be verified');
    } finally {
      verifyBusy = false;
    }
  }

  /* -- downloading ---------------------------------------------------------- */

  let encryptOpen = $state(false);
  let encrypting = $state<Backup | null>(null);
  let passphrase = $state('');
  let passphraseAgain = $state('');
  let encryptBusy = $state(false);
  let encryptError = $state('');

  const passphraseTooShort = $derived(passphrase.length > 0 && passphrase.length < 8);
  const passphraseMismatch = $derived(passphraseAgain.length > 0 && passphraseAgain !== passphrase);
  const canEncrypt = $derived(passphrase.length >= 8 && passphraseAgain === passphrase);

  function openEncrypt(backup: Backup): void {
    encrypting = backup;
    passphrase = '';
    passphraseAgain = '';
    encryptError = '';
    encryptOpen = true;
  }

  async function downloadEncrypted(): Promise<void> {
    const backup = encrypting;
    if (!backup || !canEncrypt) return;
    encryptBusy = true;
    encryptError = '';
    try {
      const blob = await downloadBackupEncrypted(backup.id, passphrase);
      saveBlob(blob, `${backup.id}.tar.gz.enc`);
      encryptOpen = false;
      toasts.success(
        'Encrypted backup downloaded',
        'Keep the passphrase with it: the file opens with nothing else, and nothing here remembers it.',
      );
    } catch (cause) {
      encryptError =
        cause instanceof ApiError ? cause.message : 'The download did not complete. Try again.';
    } finally {
      encryptBusy = false;
    }
  }

  /* -- uploading ------------------------------------------------------------ */

  let uploadOpen = $state(false);
  let uploadFile = $state<File | null>(null);
  let uploadPassphrase = $state('');
  let uploadBusy = $state(false);
  let uploadProgress = $state(0);
  let uploadError = $state('');
  let fileInput = $state<HTMLInputElement | null>(null);

  const uploadLooksEncrypted = $derived(uploadFile?.name.endsWith('.enc') ?? false);

  function openUpload(): void {
    uploadFile = null;
    uploadPassphrase = '';
    uploadProgress = 0;
    uploadError = '';
    uploadOpen = true;
  }

  function pickFile(event: Event): void {
    const input = event.currentTarget as HTMLInputElement;
    uploadFile = input.files?.[0] ?? null;
    uploadError = '';
  }

  async function upload(): Promise<void> {
    if (!uploadFile) return;
    uploadBusy = true;
    uploadError = '';
    uploadProgress = 0;
    try {
      const uploaded = await uploadBackup(
        uploadFile,
        uploadPassphrase,
        (f) => (uploadProgress = f),
      );
      uploadOpen = false;
      toasts.success(
        'Backup uploaded',
        `${uploaded.id} was unpacked, verified and listed. Retention never removes an upload.`,
      );
      reload += 1;
    } catch (cause) {
      if (cause instanceof ApiError) {
        const fields = cause.fieldErrors();
        uploadError = fields.passphrase ?? fields.file ?? cause.message;
      } else {
        uploadError = 'The upload did not complete. Try again.';
      }
    } finally {
      uploadBusy = false;
    }
  }

  /* -- deleting ------------------------------------------------------------- */

  let deleteOpen = $state(false);
  let deleting = $state<Backup | null>(null);

  async function remove(): Promise<boolean> {
    const backup = deleting;
    if (!backup) return false;
    try {
      await deleteBackup(backup.id);
      toasts.success(`${backup.id} deleted`, 'The directory and everything in it is gone.');
      reload += 1;
      return true;
    } catch (cause) {
      toasts.fromError(cause, 'That backup was not deleted');
      return false;
    }
  }

  /* -- restoring ------------------------------------------------------------ */

  let restoreOpen = $state(false);
  let restoring = $state<Backup | null>(null);
  let revokeTokens = $state(false);
  let resetAgents = $state(false);

  function openRestore(backup: Backup): void {
    restoring = backup;
    revokeTokens = false;
    resetAgents = false;
    restoreOpen = true;
  }

  async function stage(): Promise<boolean> {
    const backup = restoring;
    if (!backup) return false;
    try {
      await stageRestore(backup.id, {
        revoke_api_tokens: revokeTokens,
        reset_agent_tokens: resetAgents,
      });
      toasts.success(
        'Restore staged',
        'Nothing has changed yet. Restart the controller from the banner above to apply it.',
      );
      reload += 1;
      return true;
    } catch (cause) {
      toasts.fromError(cause, 'That restore was not staged');
      return false;
    }
  }

  let cancelling = $state(false);
  async function cancel(): Promise<void> {
    cancelling = true;
    try {
      await cancelRestore();
      toasts.info('Restore cancelled', 'The fleet carries on with the database it has.');
      reload += 1;
    } catch (cause) {
      toasts.fromError(cause, 'The restore was not cancelled');
    } finally {
      cancelling = false;
    }
  }

  let applyOpen = $state(false);
  let restarting = $state<string | null>(null);

  async function apply(): Promise<boolean> {
    try {
      await applyRestore();
      restarting = `restoring ${staged?.backup_id ?? 'the staged backup'}`;
      return true;
    } catch (cause) {
      toasts.fromError(cause, 'The controller was not restarted');
      return false;
    }
  }

  async function dismiss(): Promise<void> {
    try {
      await dismissRestoreOutcome();
      reload += 1;
    } catch (cause) {
      toasts.fromError(cause, 'That could not be dismissed');
    }
  }

  /* -- the row's actions ---------------------------------------------------- */

  function actionsFor(backup: Backup): RowAction[] {
    const stagedHere = staged?.backup_id === backup.id;
    return [
      {
        id: 'verify',
        label: 'Verify',
        icon: ShieldCheck,
        onSelect: () => void verify(backup),
      },
      {
        id: 'download',
        label: 'Download',
        icon: Download,
        onSelect: () => window.location.assign(backupDownloadUrl(backup.id)),
      },
      {
        id: 'download-encrypted',
        label: 'Download encrypted',
        icon: Lock,
        onSelect: () => openEncrypt(backup),
      },
      {
        id: 'restore',
        label: 'Restore',
        icon: ArchiveRestore,
        disabled: !backup.restorable || (staged !== null && !stagedHere),
        reason: !backup.restorable
          ? backup.restore_problem
          : staged && !stagedHere
            ? `${staged.backup_id} is already staged; cancel that first.`
            : undefined,
        onSelect: () => openRestore(backup),
      },
      {
        id: 'delete',
        label: 'Delete',
        icon: Trash2,
        danger: true,
        disabled: stagedHere,
        reason: stagedHere ? 'It is staged to be restored. Cancel the restore first.' : undefined,
        onSelect: () => {
          deleting = backup;
          deleteOpen = true;
        },
      },
    ];
  }
</script>

<PageHeader
  title="Backups"
  subtitle="One consistent copy of this fleet's database, with a manifest saying what it needs. The encryption key is never in a backup unless it was taken with one, so keep the key too, and copy the directory somewhere that is not this disk."
  onrefresh={() => {
    reload += 1;
  }}
>
  <Button
    variant="secondary"
    icon={Scissors}
    onclick={() => (pruneOpen = true)}
    loading={pruneBusy}
    disabled={restarting !== null || (items.length === 0 && (page?.remotes.length ?? 0) === 0)}
  >
    Prune now
  </Button>
  <Button variant="secondary" icon={Upload} onclick={openUpload} disabled={restarting !== null}>
    Upload a backup
  </Button>
  <Button
    variant="primary"
    icon={DatabaseBackup}
    onclick={take}
    loading={taking}
    disabled={restarting !== null}
  >
    Back up now
  </Button>
</PageHeader>

<div class="panel">
  {#if restarting}
    <RestartWait reason={restarting} />
  {/if}

  <LoadingBoundary {loading} {error} onretry={() => (reload += 1)}>
    {#snippet skeleton()}
      <div class="pad"><Skeleton lines={6} /></div>
    {/snippet}

    {#if page}
      {#if staged && !restarting}
        <section class="banner staged" aria-labelledby="staged-restore">
          <ArchiveRestore size={18} aria-hidden="true" />
          <div class="banner-body">
            <h2 id="staged-restore">A restore is staged and waiting for a restart</h2>
            <p>
              <span class="mono">{staged.backup_id}</span>
              {#if staged.taken_at}, taken {formatAbsolute(staged.taken_at)},{/if}
              will replace this fleet's database the next time the controller starts.
              {staged.requested_by ? `${staged.requested_by} asked for it` : 'It was asked for'}
              <RelativeTime value={staged.requested_at} plain />. Nothing has changed yet: every
              check a restore makes has passed, and the fleet runs on the database it has until you
              restart.
              {#if staged.revoke_api_tokens}Every API token will be revoked.{/if}
              {#if staged.reset_agent_tokens}Every agent will have to join again.{/if}
            </p>
            <div class="banner-actions">
              <Button variant="danger" icon={ArchiveRestore} onclick={() => (applyOpen = true)}>
                Restart and restore
              </Button>
              <Button variant="ghost" onclick={cancel} loading={cancelling}
                >Cancel the restore</Button
              >
            </div>
          </div>
        </section>
      {/if}

      {#if lastRestore}
        <section
          class="banner"
          class:ok={lastRestore.ok}
          class:failed={!lastRestore.ok}
          aria-labelledby="last-restore"
        >
          {#if lastRestore.ok}
            <CircleCheck size={18} aria-hidden="true" />
          {:else}
            <TriangleAlert size={18} aria-hidden="true" />
          {/if}
          <div class="banner-body">
            <h2 id="last-restore">
              {lastRestore.ok
                ? `${lastRestore.backup_id} was restored`
                : `The restore of ${lastRestore.backup_id} did not finish`}
            </h2>
            {#if lastRestore.ok}
              <p>
                Applied <RelativeTime value={lastRestore.attempted_at} plain />.
                {#if lastRestore.report?.moved_aside}
                  The database that was there is kept at
                  <span class="mono">{lastRestore.report.moved_aside}</span>.
                {/if}
                The fleet came back fenced; lift the fence from the problems drawer once you have checked
                what a restore does not bring with it.
              </p>
              {#if lastRestore.report?.invalidated?.length}
                <ul class="lines">
                  {#each lastRestore.report.invalidated as line (line)}
                    <li>{line}</li>
                  {/each}
                </ul>
              {/if}
            {:else}
              <p>
                Attempted <RelativeTime value={lastRestore.attempted_at} plain />. The reason below
                says which database the controller started on.
              </p>
              <p class="why">{lastRestore.error}</p>
            {/if}
            <div class="banner-actions">
              <Button size="sm" variant="ghost" onclick={dismiss}>Dismiss</Button>
            </div>
          </div>
        </section>
      {/if}

      {#if schedule?.last_error}
        <section class="banner failed" aria-labelledby="schedule-failed">
          <TriangleAlert size={18} aria-hidden="true" />
          <div class="banner-body">
            <h2 id="schedule-failed">The scheduled backup is failing</h2>
            <p class="why">{schedule.last_error}</p>
            <p>
              The controller tries again every fifteen minutes. Taking one now shows the same error,
              or proves it fixed.
            </p>
          </div>
        </section>
      {/if}

      <dl class="facts">
        <div>
          <dt>Last backup</dt>
          <dd>
            {#if newest}
              <RelativeTime value={newest.taken_at} />
              <span class="second"
                >{sourceOf(newest).label.toLowerCase()} · {formatBytes(newest.bytes)}</span
              >
            {:else}
              <span class="unset">None yet</span>
            {/if}
          </dd>
        </div>
        <div>
          <dt>Schedule</dt>
          <dd>
            {#if schedule?.enabled}
              {describeInterval(schedule.interval)}
              <span class="second">
                {#if schedule.next_due_at}
                  next <RelativeTime value={schedule.next_due_at} plain />, keeping {schedule.keep ===
                  0
                    ? 'every one'
                    : pluralise(schedule.keep, 'copy', 'copies')}
                {/if}
              </span>
            {:else}
              <span class="unset">Off</span>
              <span class="second">
                <a href="/settings/configuration?setting=backup.interval">Set backup.interval</a>
                to have the controller take one itself.
              </span>
            {/if}
          </dd>
        </div>
        <div>
          <dt>Directory</dt>
          <dd class="path">
            <CopyButton value={page.directory} label="Copy the backup directory" showValue />
            <span class="second">
              {#if page.disk_free_bytes !== null && page.disk_free_bytes !== undefined}
                {formatBytes(page.disk_free_bytes)} free ·
              {/if}
              the database is {formatBytes(page.database_bytes)}
            </span>
          </dd>
        </div>
        <div>
          <dt>Encryption key</dt>
          <dd>
            {#if page.key_fingerprint}
              <span class="mono">{page.key_fingerprint}</span>
              <span class="second">the fingerprint a backup must match to be restored here</span>
            {:else}
              <span class="unset">None loaded</span>
            {/if}
          </dd>
        </div>
      </dl>

      <BackupSchedule
        onchanged={() => {
          reload += 1;
        }}
      />

      <BackupRemotes
        remotes={page.remotes}
        disabled={restarting !== null}
        onchanged={() => {
          reload += 1;
        }}
      />

      {#if items.length === 0}
        <EmptyState
          icon={HardDrive}
          title="No backups yet"
          description={schedule?.enabled
            ? `The controller takes one ${describeInterval(schedule.interval)} and the first is due shortly. Take one now if you would rather not wait.`
            : 'Scheduled backups are off, so nothing is copied unless somebody asks. Take one now, or set backup.interval.'}
        >
          <Button variant="primary" icon={DatabaseBackup} onclick={take} loading={taking}>
            Back up now
          </Button>
        </EmptyState>
      {:else}
        <div class="scroll">
          <!--
            Every role is spelled out rather than left to the table's own
            display type: the rows become cards on a phone, and a browser drops
            the implicit row and cell roles the moment display stops being
            table-row.
          -->
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <table
            role="table"
            use:tableLayout={{
              id: 'backups',
              columns: ['taken', 'source', 'size', 'schema', 'key', 'actions'],
            }}
          >
            <caption class="sr-only">Backups</caption>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <thead role="rowgroup">
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <tr role="row">
                <th role="columnheader" scope="col">Taken</th>
                <th role="columnheader" scope="col">Source</th>
                <th role="columnheader" scope="col">Size</th>
                <th role="columnheader" scope="col">Schema</th>
                <th role="columnheader" scope="col">Key</th>
                <th role="columnheader" scope="col"><span class="sr-only">Actions</span></th>
              </tr>
            </thead>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <tbody role="rowgroup">
              {#each shown as backup (backup.id)}
                {@const source = sourceOf(backup)}
                {@const key = keyStatus(backup)}
                <tr
                  role="row"
                  class:staged-row={staged?.backup_id === backup.id}
                  class:broken={!!backup.problem}
                >
                  <td role="cell" data-label="Taken" class="taken">
                    <RelativeTime value={backup.taken_at} />
                    <span class="second mono">{backup.id}</span>
                    {#if backup.problem}
                      <span class="second problem">{backup.problem}</span>
                    {:else if !backup.restorable && backup.restore_problem}
                      <span class="second problem">{backup.restore_problem}</span>
                    {/if}
                  </td>
                  <td role="cell" data-label="Source">
                    <Tooltip text={source.hint}>
                      <Badge tone={source.tone} label={source.label} size="sm" dot={false} />
                    </Tooltip>
                    {#if backup.taken_by}
                      <span class="second">by {backup.taken_by}</span>
                    {/if}
                  </td>
                  <td role="cell" data-label="Size" class="tabular">{formatBytes(backup.bytes)}</td>
                  <td role="cell" data-label="Schema">
                    <span class="mono">{schemaShort(backup.schema_latest)}</span>
                    {#if backup.version}
                      <span class="second">Zoomies {backup.version}</span>
                    {/if}
                  </td>
                  <td role="cell" data-label="Key">
                    <Tooltip text={key.hint}>
                      <Badge tone={key.tone} label={key.label} size="sm" dot={false} />
                    </Tooltip>
                    {#if backup.secrets > 0}
                      <span class="second">
                        opens {pluralise(backup.secrets, 'credential')}
                      </span>
                    {/if}
                  </td>
                  <td role="cell" data-label="Actions" class="actions">
                    <RowActions actions={actionsFor(backup)} subject={backup.id} />
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        {#if items.length > PER_PAGE[0]}
          <div class="pager">
            <Pagination
              total={items.length}
              {limit}
              {offset}
              noun="backups"
              sizes={PER_PAGE}
              onpage={(next) => (offset = next)}
              onlimit={(next) => {
                limit = next;
                offset = 0;
              }}
            />
          </div>
        {/if}
      {/if}
    {/if}
  </LoadingBoundary>
</div>

<!-- Retention, now -->
<ConfirmDialog
  bind:open={pruneOpen}
  title="Apply retention now"
  description="Retention normally runs when a backup is taken. This runs it now, against the numbers this fleet is set to keep, and nothing it deletes can be brought back from here."
  consequences={[
    schedule?.keep === 0
      ? 'Nothing is removed from this host: backup.keep is 0, which keeps every copy.'
      : `This host keeps ${pluralise(schedule?.keep ?? 0, 'copy', 'copies')}; anything older goes.`,
    ...sending.map((r) =>
      r.keep > 0
        ? `${r.name} keeps ${pluralise(r.keep, 'copy', 'copies')}; anything older is deleted from ${r.where}.`
        : `${r.name} keeps every copy, so nothing is removed from ${r.where}.`,
    ),
    'A backup you uploaded or brought back from a bucket is never counted and never removed.',
  ]}
  confirmLabel="Apply retention"
  busy={pruneBusy}
  onconfirm={prune}
/>

<!-- Verify -->
<Dialog
  bind:open={verifyOpen}
  title="Verify {verifying?.id ?? 'backup'}"
  description="The file is re-read and asked the questions a restore would."
  size="sm"
>
  {#if verifyBusy || !verification}
    <div class="verify-wait" aria-busy="true"><Skeleton lines={4} /></div>
  {:else}
    <ul class="checks" data-ok={verification.ok}>
      <li>
        <Badge
          tone={verification.integrity === 'ok' ? 'idle' : 'danger'}
          label={verification.integrity === 'ok' ? 'Sound' : 'Damaged'}
          size="sm"
          dot={false}
        />
        <span
          >SQLite's own integrity check{verification.integrity === 'ok'
            ? ' passes'
            : `: ${verification.integrity}`}.</span
        >
      </li>
      <li>
        {#if !verification.digest_known}
          <Badge tone="neutral" label="No digest" size="sm" dot={false} />
          <span>The backup has no manifest to compare the file against.</span>
        {:else if verification.digest_matches}
          <Badge tone="idle" label="Unchanged" size="sm" dot={false} />
          <span>The database is byte for byte what the manifest recorded.</span>
        {:else}
          <Badge tone="danger" label="Altered" size="sm" dot={false} />
          <span>The database is not the file the manifest recorded.</span>
        {/if}
      </li>
      <li>
        <Badge
          tone={verification.schema_readable ? 'idle' : 'danger'}
          label={verification.schema_readable ? 'Readable' : 'Newer'}
          size="sm"
          dot={false}
        />
        <span>
          {#if verification.schema_readable}
            This build can open it: {pluralise(
              verification.migrations,
              'migration',
            )}{verification.latest ? `, up to ${schemaShort(verification.latest)}` : ''}.
          {:else}
            Written by a newer release than Zoomies.
          {/if}
        </span>
      </li>
    </ul>
    {#if verification.ok}
      <p class="verdict ok">
        <CircleCheck size={14} aria-hidden="true" /> This backup can be restored here.
      </p>
    {:else}
      <ul class="problems">
        {#each verification.problems as problem (problem)}
          <li>{problem}</li>
        {/each}
      </ul>
    {/if}
  {/if}
  {#snippet footer()}
    <Button variant="primary" onclick={() => (verifyOpen = false)}>Done</Button>
  {/snippet}
</Dialog>

<!-- Encrypted download -->
<Dialog
  bind:open={encryptOpen}
  title="Download encrypted"
  description="The archive is sealed with a passphrase before it leaves Zoomies, so it can sit on a laptop or in a shared drive."
  size="sm"
>
  <form
    id="encrypt-form"
    class="form"
    onsubmit={(e) => {
      e.preventDefault();
      void downloadEncrypted();
    }}
  >
    <Field
      label="Passphrase"
      hint="At least eight characters. Nothing here remembers it: the file opens with this and nothing else."
      error={passphraseTooShort ? 'Use at least eight characters.' : encryptError}
    >
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={passphrase}
          {id}
          {describedBy}
          {invalid}
          type="password"
          autocomplete="new-password"
        />
      {/snippet}
    </Field>
    <Field
      label="Passphrase again"
      error={passphraseMismatch ? 'The two do not match.' : undefined}
    >
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={passphraseAgain}
          {id}
          {describedBy}
          {invalid}
          type="password"
          autocomplete="new-password"
        />
      {/snippet}
    </Field>
    <p class="fine">
      The database inside holds every account's password hash and every sealed credential. The
      instance key keeps the credentials sealed; the passphrase keeps the rest.
    </p>
  </form>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (encryptOpen = false)} disabled={encryptBusy}
      >Cancel</Button
    >
    <Button
      variant="primary"
      type="submit"
      form="encrypt-form"
      icon={Lock}
      loading={encryptBusy}
      disabled={!canEncrypt}
    >
      Download
    </Button>
  {/snippet}
</Dialog>

<!-- Upload -->
<Dialog
  bind:open={uploadOpen}
  title="Upload a backup"
  description="An archive this page downloaded, from Zoomies or another. It is unpacked, verified, and listed; retention never removes it."
  size="sm"
>
  <form
    id="upload-form"
    class="form"
    onsubmit={(e) => {
      e.preventDefault();
      void upload();
    }}
  >
    <Field
      label="Archive"
      hint=".tar.gz, or .tar.gz.enc for one downloaded with a passphrase."
      error={uploadError && !uploadLooksEncrypted ? uploadError : undefined}
    >
      {#snippet children({ id, describedBy })}
        <div class="filepick">
          <!-- The button beside it is the control. A tab stop here would be clipped away with the input and draw no focus ring. -->
          <input
            bind:this={fileInput}
            {id}
            type="file"
            accept=".gz,.enc,application/gzip,application/octet-stream"
            aria-describedby={describedBy}
            tabindex="-1"
            class="sr-only"
            onchange={pickFile}
            disabled={uploadBusy}
          />
          <Button
            variant="secondary"
            icon={Upload}
            onclick={() => fileInput?.click()}
            disabled={uploadBusy}
          >
            Choose a file
          </Button>
          <span class="filename" class:unset={!uploadFile}>
            {uploadFile
              ? `${uploadFile.name} · ${formatBytes(uploadFile.size)}`
              : 'Nothing chosen yet'}
          </span>
        </div>
      {/snippet}
    </Field>
    <Field
      label="Passphrase"
      hint={uploadLooksEncrypted
        ? 'The one it was downloaded with.'
        : 'Only for an encrypted archive. Leave it empty for a plain one.'}
      error={uploadError && uploadLooksEncrypted ? uploadError : undefined}
    >
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={uploadPassphrase}
          {id}
          {describedBy}
          {invalid}
          type="password"
          autocomplete="off"
          disabled={uploadBusy}
        />
      {/snippet}
    </Field>
    {#if uploadBusy}
      <progress aria-label="Upload progress" value={uploadProgress} max={1}></progress>
      <p class="fine" aria-live="polite">
        {uploadProgress >= 1
          ? 'Sent. Unpacking and verifying…'
          : `${Math.round(uploadProgress * 100)}% sent`}
      </p>
    {/if}
  </form>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (uploadOpen = false)} disabled={uploadBusy}
      >Cancel</Button
    >
    <Button
      variant="primary"
      type="submit"
      form="upload-form"
      icon={Upload}
      loading={uploadBusy}
      disabled={!uploadFile}
    >
      Upload
    </Button>
  {/snippet}
</Dialog>

<!-- Delete -->
<ConfirmDialog
  bind:open={deleteOpen}
  title="Delete backup"
  name={deleting?.id}
  description="{deleting?.id ??
    'This backup'} is removed from the controller's disk, with its manifest."
  consequences={[
    `${formatBytes(deleting?.total_bytes)} is freed.`,
    'A copy kept somewhere else is unaffected.',
  ]}
  confirmLabel="Delete backup"
  requireName
  onconfirm={remove}
  oncancel={() => (deleting = null)}
/>

<!-- Stage a restore -->
<ConfirmDialog
  bind:open={restoreOpen}
  title="Restore backup"
  name={restoring?.id}
  description="The whole fleet goes back to what it was when {restoring?.id ??
    'this backup'} was taken{restoring
    ? `, ${formatAbsolute(restoring.taken_at)}`
    : ''}. Nothing changes until you restart the controller: this stages the restore, checking now everything a restore checks."
  consequences={[
    'Everything done since the backup — pools, hosts, jobs, settings, users — is replaced by what the backup holds. The database being replaced is kept beside it, not deleted.',
    'Everyone is signed out, and every unredeemed join token is removed.',
    'The fleet comes back fenced: it decides as normal and applies nothing until an administrator lifts the fence.',
  ]}
  confirmLabel="Stage the restore"
  requireName
  onconfirm={stage}
  oncancel={() => (restoring = null)}
>
  <div class="options">
    <Switch
      bind:checked={revokeTokens}
      label="Revoke every API token as well"
      description="If the backup may have been read by anyone else. Whatever automation holds a token needs a new one."
    />
    <Switch
      bind:checked={resetAgents}
      label="Make every agent join again"
      description="Same, or you are rebuilding the fleet's hosts anyway. Each agent exits with the command to rejoin."
    />
  </div>
</ConfirmDialog>

<!-- Apply: the restart -->
<ConfirmDialog
  bind:open={applyOpen}
  title="Restart the controller"
  description="The controller stops now. Its service manager starts it again, and the new process restores {staged?.backup_id ??
    'the staged backup'} before it opens the database."
  consequences={[
    'The UI and the API are unavailable for a few seconds. Runners keep working: a controller restart never kills a job.',
    'If nothing restarts the process — it was run by hand in a terminal, say — start it again yourself; the restore is applied whoever starts it.',
    'This page watches the restart and reloads when the controller is back.',
  ]}
  confirmLabel="Restart and restore"
  onconfirm={apply}
/>

<style>
  .panel {
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  .pad {
    padding: var(--z-space-5);
  }

  /* -- banners ---------------------------------------------------------- */
  .banner {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
    padding: var(--z-space-4) var(--z-space-5);
    border-bottom: var(--z-border-width) solid var(--z-border);
    color: var(--z-text-muted);
  }
  .banner.staged {
    background: var(--z-draining-subtle);
    box-shadow: inset var(--z-nudge-1) 0 0 0 var(--z-draining);
    color: var(--z-draining);
  }
  .banner.ok {
    background: var(--z-idle-subtle);
    box-shadow: inset var(--z-nudge-1) 0 0 0 var(--z-idle);
    color: var(--z-idle);
  }
  .banner.failed {
    background: var(--z-danger-subtle);
    box-shadow: inset var(--z-nudge-1) 0 0 0 var(--z-danger);
    color: var(--z-danger);
  }
  .banner-body {
    min-width: 0;
    flex: 1;
  }
  .banner h2 {
    margin: 0;
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .banner p {
    margin: var(--z-space-1) 0 0;
    max-width: 80ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .banner .why {
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .banner-actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    margin-top: var(--z-space-3);
  }
  .lines {
    margin: var(--z-space-2) 0 0;
    padding-left: var(--z-space-5);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }

  /* -- the facts -------------------------------------------------------- */
  .facts {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr));
    gap: var(--z-space-4) var(--z-space-6);
    margin: 0;
    padding: var(--z-space-4) var(--z-space-5);
    border-bottom: var(--z-border-width) solid var(--z-border);
  }
  .facts > div {
    min-width: 0;
  }
  dt {
    margin-bottom: var(--z-space-1);
    font-size: var(--z-text-2xs);
    text-transform: uppercase;
    letter-spacing: var(--z-tracking-wide);
    color: var(--z-text-muted);
  }
  dd {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .path {
    min-width: 0;
  }
  .second {
    display: block;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-subtle);
  }
  .second.problem {
    color: var(--z-danger);
  }
  .unset {
    color: var(--z-text-subtle);
    font-style: italic;
  }
  .facts a {
    color: var(--z-accent);
  }

  /* -- the table -------------------------------------------------------- */
  .pager {
    padding: var(--z-space-2) var(--z-space-5);
    border-top: var(--z-border-width) solid var(--z-border);
  }
  .scroll {
    overflow-x: auto;
    contain: paint;
  }
  table {
    width: 100%;
    table-layout: fixed;
    border-collapse: separate;
    border-spacing: 0;
    font-size: var(--z-text-sm);
  }
  th {
    overflow-wrap: anywhere;
    padding: var(--z-space-2) var(--z-space-5);
    border-bottom: var(--z-border-width) solid var(--z-border);
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    text-align: left;
    text-transform: uppercase;
    letter-spacing: var(--z-tracking-wide);
  }
  td {
    overflow: hidden;
    overflow-wrap: anywhere;
    padding: var(--z-space-3) var(--z-space-5);
    border-bottom: var(--z-border-width) solid var(--z-border);
    color: var(--z-text);
    vertical-align: top;
  }
  tbody tr:last-child td {
    border-bottom: 0;
  }
  tr.staged-row td {
    background: var(--z-draining-subtle);
  }
  tr.broken td {
    color: var(--z-text-subtle);
  }
  .taken {
    font-weight: var(--z-weight-medium);
  }
  .actions {
    text-align: right;
    /* The toolbar's buttons must not be cut off at the cell's edge. */
    overflow: visible;
  }
  th:last-child,
  td.actions {
    width: 12rem;
  }
  @media (max-width: 768px) {
    thead {
      display: none;
    }
    table,
    tbody,
    tr,
    td {
      display: block;
      width: auto;
    }
    tr {
      padding: var(--z-space-3) var(--z-space-5);
      border-bottom: var(--z-border-width) solid var(--z-border);
    }
    td {
      padding: var(--z-space-1) 0;
      border-bottom: 0;
    }
    td::before {
      content: attr(data-label);
      display: block;
      font-size: var(--z-text-2xs);
      text-transform: uppercase;
      letter-spacing: var(--z-tracking-wide);
      color: var(--z-text-muted);
    }
    td.actions {
      text-align: left;
      width: auto;
    }
    td.actions::before {
      content: none;
    }
  }

  /* -- dialogs ---------------------------------------------------------- */
  .form {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    padding-bottom: var(--z-space-2);
  }
  .fine {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .filepick {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-3);
  }
  .filename {
    font-size: var(--z-text-sm);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  progress {
    display: block;
    width: 100%;
    height: var(--z-space-2);
    border: 0;
    border-radius: var(--z-radius-full);
    overflow: hidden;
    accent-color: var(--z-accent);
    background: var(--z-border);
  }
  progress::-webkit-progress-bar {
    background: var(--z-border);
  }
  progress::-webkit-progress-value {
    background: var(--z-accent);
  }
  progress::-moz-progress-bar {
    background: var(--z-accent);
  }
  .options {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
  }
  .verify-wait {
    padding-bottom: var(--z-space-2);
  }
  .checks {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .checks li {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
  }
  .verdict {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: var(--z-space-4) 0 var(--z-space-2);
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
  }
  .verdict.ok {
    color: var(--z-idle);
  }
  .problems {
    margin: var(--z-space-4) 0 var(--z-space-2);
    padding-left: var(--z-space-5);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-danger);
  }
</style>
