<!--
  Draining and deleting runners, from the grid and from the detail page.

  Both pages ask the same question, so they ask it in the same words. The two
  distinctions that matter to an operator are stated every time, with the count
  in front of them:

    * draining never interrupts a running job -- the runner finishes what it is
      on and then exits;
    * deleting with "destroy immediately" does interrupt it, and GitHub will
      mark that job failed.

  Typing the name is required only for the destructive combination, which is
  the one that cannot be taken back.
-->
<script lang="ts">
  import { bulkRunners, deleteRunner, drainRunner } from '$lib/api/client';
  import type { Runner } from '$lib/api/types';
  import { pluralise } from '$lib/format';
  import { fleet } from '$lib/state/fleet.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';

  interface Props {
    open?: boolean;
    action: 'drain' | 'delete';
    /** One runner for a row action, several for a bulk one. */
    targets: readonly Runner[];
    /** Called once the controller has accepted the request. */
    ondone?: () => void;
    oncancel?: () => void;
  }

  let { open = $bindable(false), action, targets, ondone, oncancel }: Props = $props();

  let force = $state(false);

  // Every time the dialog opens it opens with the safe option chosen.
  $effect(() => {
    if (open) force = false;
  });

  const count = $derived(targets.length);
  const single = $derived(count === 1);
  const first = $derived(targets[0]);
  const name = $derived(single ? (first?.name ?? '') : '');
  const busy = $derived(targets.filter((runner) => runner.state === 'busy').length);
  const finished = $derived(
    targets.filter((runner) => runner.state === 'removed' || runner.state === 'failed').length,
  );

  const title = $derived(
    action === 'drain'
      ? single
        ? 'Drain runner'
        : 'Drain runners'
      : single
        ? 'Delete runner'
        : 'Delete runners',
  );

  const description = $derived.by(() => {
    if (action === 'drain') {
      return single
        ? `${name || 'This runner'} finishes the job it is on and then exits.`
        : `${pluralise(count, 'runner')} finish the jobs they are on and then exit.`;
    }
    return single
      ? `Deleting ${name || 'this runner'} removes it from the fleet and deregisters it from GitHub.`
      : `Deleting ${pluralise(count, 'runner')} removes them from the fleet and deregisters them from GitHub.`;
  });

  const consequences = $derived.by(() => {
    const lines: string[] = [];
    if (action === 'drain') {
      lines.push(
        busy > 0
          ? single
            ? 'The job it is running now is allowed to finish. Draining never interrupts work in progress.'
            : `${busy} of them are running a job now. Each one finishes it; draining never interrupts work in progress.`
          : single
            ? 'It is not running a job, so it will exit shortly.'
            : 'None of them is running a job, so they will exit shortly.',
      );
      lines.push(
        single
          ? 'No new jobs are sent to it while it drains.'
          : 'No new jobs are sent to them while they drain.',
      );
    } else if (force) {
      lines.push(
        busy > 0
          ? single
            ? 'The job it is running now is interrupted, and GitHub will mark that job failed.'
            : `${pluralise(busy, 'job')} running right now will be interrupted, and GitHub will mark ${busy === 1 ? 'it' : 'them'} failed.`
          : single
            ? 'It is destroyed immediately.'
            : 'They are destroyed immediately.',
      );
    } else {
      lines.push(
        busy > 0
          ? single
            ? 'It finishes the job it is on first, then exits. Nothing is interrupted.'
            : `${busy} of them are running a job now. Each finishes it first, so nothing is interrupted.`
          : single
            ? 'It drains first, then exits.'
            : 'They drain first, then exit.',
      );
    }
    if (finished > 0) {
      lines.push(
        single
          ? 'It has already finished, so the controller may refuse.'
          : `${finished} of them have already finished and will be skipped.`,
      );
    }
    if (action === 'delete') {
      lines.push('If its pool is still short of runners, the scheduler makes a replacement.');
    }
    return lines;
  });

  const confirmLabel = $derived(
    action === 'drain'
      ? single
        ? 'Drain runner'
        : `Drain ${pluralise(count, 'runner')}`
      : single
        ? 'Delete runner'
        : `Delete ${pluralise(count, 'runner')}`,
  );

  /** Typing is asked for only when work in progress is about to be interrupted. */
  const typedName = $derived(action === 'delete' && force ? (single ? name : 'delete') : undefined);

  async function runSingle(runner: Runner): Promise<boolean> {
    const id = runner.id ?? '';
    if (id === '') return false;
    if (action === 'drain') {
      // The badge flips to draining straight away and rolls back with the
      // server's own words if the controller refuses.
      const result = await fleet.optimistic(
        id,
        { state: 'draining' },
        () => drainRunner(id),
        'That runner was not drained',
      );
      if (result === undefined) return false;
      toasts.success(`Draining ${runner.name ?? 'the runner'}`, 'It exits once its job is done.');
      return true;
    }
    try {
      await deleteRunner(id, { force });
      toasts.success(
        `Deleting ${runner.name ?? 'the runner'}`,
        force ? 'It is being destroyed now.' : 'It exits once its job is done.',
      );
      return true;
    } catch (cause) {
      toasts.fromError(cause, 'That runner was not deleted');
      return false;
    }
  }

  async function runBulk(ids: string[]): Promise<boolean> {
    try {
      const response = await bulkRunners({
        action,
        ids,
        ...(action === 'delete' ? { force } : {}),
      });
      const results = response.results ?? [];
      const failed = results.filter((result) => result.ok === false);
      const verb = action === 'drain' ? 'draining' : 'deleting';
      if (failed.length === 0) {
        toasts.success(`${pluralise(ids.length, 'runner')} ${verb}`);
      } else {
        const reason = failed[0]?.error;
        toasts.error(
          `${failed.length} of ${pluralise(ids.length, 'runner')} could not be ${action === 'drain' ? 'drained' : 'deleted'}`,
          reason
            ? `The first failure said: ${reason}. The rest went through.`
            : 'The rest went through. Open one that did not change to see what the controller said.',
        );
      }
      return true;
    } catch (cause) {
      toasts.fromError(
        cause,
        action === 'drain' ? 'Those runners were not drained' : 'Those runners were not deleted',
      );
      return false;
    }
  }

  async function confirm(): Promise<void> {
    const ids = targets.map((runner) => runner.id ?? '').filter((id) => id !== '');
    if (ids.length === 0) return;
    const ok = single && first ? await runSingle(first) : await runBulk(ids);
    void fleet.reconcile();
    if (ok) ondone?.();
  }
</script>

<ConfirmDialog
  bind:open
  {title}
  name={typedName}
  {description}
  {consequences}
  {confirmLabel}
  requireName={typedName !== undefined}
  tone={action === 'delete' ? 'danger' : 'default'}
  onconfirm={confirm}
  {oncancel}
>
  {#if action === 'delete'}
    <Checkbox
      bind:checked={force}
      label={single ? 'Destroy it immediately' : 'Destroy them immediately'}
      description="Without this, a runner finishes the job it is on and then exits. With it, work in progress is interrupted."
    />
  {/if}
</ConfirmDialog>
