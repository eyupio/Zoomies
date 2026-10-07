<!--
  Kennel Club's on/off switch, where Kennel Club is.

  Turning it on used to mean leaving for Settings, finding the setting among
  eighty-odd and coming back. It is the same setting -- `kennel.enabled`, saved
  the same way, audited the same way -- but changed from the page it governs.

  Turning it off asks first, and says what it does, because it takes the
  checking away from everybody who uses the fleet. Turning it on does not: the
  page that explains itself when Kennel Club is off already says what it reads.

  Only an administrator can change it, and everybody else sees the state and who
  can, instead of a switch that answers 403.

  The switch shows what the controller says, not what was asked for. If the
  change is refused, or accepted and then overruled by the environment, it goes
  back to where it was and the sentence under it says why.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import {
    KENNEL_NOW_OFF,
    KENNEL_NOW_ON,
    KENNEL_SWITCH_LABEL,
    KENNEL_TURN_OFF,
    kennelSwitchSentence,
  } from '$lib/kennel/words';
  import { fleet } from '$lib/state/fleet.svelte';
  import { kennelClub } from '$lib/state/kennel.svelte';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';

  interface Props {
    /** Lets the place that holds the switch size it, since the switch does not know where it is. */
    class?: string;
  }

  let { class: className = '' }: Props = $props();

  const canAdmin = $derived(session.can('admin'));
  const enabled = $derived(kennelClub.enabled);

  $effect(() => kennelClub.follow());

  // The stream opens after the page has loaded, and nothing is replayed to it, so a
  // change that landed in between would never be heard. Asking again whenever the
  // stream comes up is the same answer the pages beside this one give.
  let previous = '';
  $effect(() => {
    const next = fleet.connection;
    if (next === 'live' && previous && previous !== 'live')
      untrack(() => void kennelClub.refresh());
    previous = next;
  });

  // What the pill shows follows the controller. A press moves it for a moment and
  // `settle` puts it back to the truth, so a refused change cannot leave it lying.
  let shown = $derived(enabled === true);

  let confirming = $state(false);
  let busy = $state(false);
  let problem = $state('');

  function settle(): void {
    shown = untrack(() => kennelClub.enabled) === true;
  }

  async function apply(next: boolean): Promise<boolean> {
    busy = true;
    problem = '';
    const result = await kennelClub.set(next);
    busy = false;
    settle();
    if (!result.ok) {
      problem = result.message;
      return false;
    }
    const said = next ? KENNEL_NOW_ON : KENNEL_NOW_OFF;
    toasts.success(said.title, said.detail);
    return true;
  }

  function pressed(next: boolean): void {
    if (next) {
      void apply(true);
      return;
    }
    // Off is asked about first; the pill goes back until it is confirmed.
    settle();
    confirming = true;
  }
</script>

<div class="kennel-switch {className}">
  <Switch
    bind:checked={shown}
    label={KENNEL_SWITCH_LABEL}
    disabled={!canAdmin || busy || enabled === null}
    describedBy="kennel-switch-state"
    onchange={pressed}
  />
  <p id="kennel-switch-state" class="state">{kennelSwitchSentence(enabled, canAdmin)}</p>
  {#if problem}
    <p class="problem" role="alert">{problem}</p>
  {/if}
</div>

<ConfirmDialog
  bind:open={confirming}
  title={KENNEL_TURN_OFF.title}
  description={KENNEL_TURN_OFF.description}
  consequences={KENNEL_TURN_OFF.consequences}
  confirmLabel="Turn off"
  tone="default"
  {busy}
  onconfirm={() => apply(false)}
/>

<style>
  .kennel-switch {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    padding: var(--z-space-2);
  }
  .state,
  .problem {
    margin: 0;
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
    color: var(--z-text-muted);
  }
  .problem {
    color: var(--z-danger);
  }
</style>
