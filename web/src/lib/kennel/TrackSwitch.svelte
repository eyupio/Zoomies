<!--
  Whether Kennel Club is looking at one repository, where the repository is.

  Every repository is tracked until somebody says otherwise. Stopping it silences
  its errors, so it is an administrator's decision and asks for a reason first;
  starting it again can only make Kennel Club stricter, so it is an operator's and
  asks for nothing. Anybody who cannot do what the switch would do next sees the
  state and who can, instead of a switch that answers 403.

  The switch shows what the controller says, not what was asked for: a press moves
  it for a moment and it settles back to the repository the page holds, so a
  refused change cannot leave it lying.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { setKennelTracking } from '$lib/api/client';
  import type { KennelRepository } from '$lib/api/types';
  import Switch from '$lib/components/Switch.svelte';
  import TrackingDialog from '$lib/kennel/TrackingDialog.svelte';
  import { TRACK_SWITCH_LABEL, TRACKING_NOW_STARTED, trackSentence } from '$lib/kennel/words';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';

  interface Props {
    repo: KennelRepository;
    /** The repository as the controller now has it, to replace what the page holds. */
    onchange: (repository: KennelRepository) => void;
  }

  let { repo, onchange }: Props = $props();

  const tracked = $derived(repo.tracking.tracked);
  const canAdmin = $derived(session.can('admin'));
  const canOperator = $derived(session.can('operator'));
  // What the switch would do next is what decides who may press it.
  const mayPress = $derived(tracked ? canAdmin : canOperator);

  let shown = $derived(tracked);
  let stopping = $state(false);
  let busy = $state(false);

  function settle(): void {
    shown = untrack(() => tracked);
  }

  async function start(): Promise<void> {
    busy = true;
    try {
      const next = await setKennelTracking(repo.id, { tracked: true });
      toasts.success(TRACKING_NOW_STARTED.title, TRACKING_NOW_STARTED.detail);
      onchange(next);
    } catch (cause) {
      toasts.fromError(cause, 'That repository was not tracked');
    } finally {
      busy = false;
      settle();
    }
  }

  function pressed(next: boolean): void {
    if (next) {
      void start();
      return;
    }
    // Stopping is asked about first, with the reason; the pill goes back until it is confirmed.
    settle();
    stopping = true;
  }
</script>

<div class="track">
  <Switch
    bind:checked={shown}
    label={TRACK_SWITCH_LABEL}
    disabled={!mayPress || busy}
    describedBy="track-state"
    onchange={pressed}
  />
  <p id="track-state" class="state">
    {trackSentence(tracked, { admin: canAdmin, operator: canOperator })}
  </p>
</div>

<TrackingDialog bind:open={stopping} repositoryId={repo.id} name={repo.name} onstopped={onchange} />

<style>
  .track {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    min-width: 0;
  }
  .state {
    margin: 0;
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
    color: var(--z-text-muted);
    overflow-wrap: anywhere;
  }
</style>
