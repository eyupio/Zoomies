<!--
  Step seven: the runner timings this pool disagrees with the fleet about.

  Every control here is empty by default, and empty is the answer that means
  something: the pool follows the fleet, and keeps following it when the fleet's
  figure is changed on the Settings page. A wizard that pre-filled these with
  the fleet's values would look identical and behave differently -- every pool
  frozen on whatever the fleet meant the day it was made -- so the figures are
  shown as placeholders instead, where they can be read but not accidentally
  adopted.

  The settings are here rather than on the Scaling step because they answer a
  different question. Scaling is how many runners and for how long they idle,
  which an operator sets from the shape of their queue. These are how long each
  step of a runner's own life is allowed to take, which an operator sets from
  the shape of their images -- and the pool that needs them is usually the one
  pulling twelve gigabytes of Windows, not the one that queues oddly.
-->
<script lang="ts">
  import { TriangleAlert } from '@lucide/svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import type { Result } from '$lib/api/types';
  import type { PoolDraft } from './draft';

  interface Props {
    draft: PoolDraft;
    errors: Record<string, string>;
    touch: (field: string) => void;
    /** What the fleet answers for each of these, for the placeholders. */
    fleetDefaults: Result<'getPoolDefaults'>['runner_settings'] | null;
  }

  let { draft, errors, touch, fleetDefaults }: Props = $props();

  type Key =
    | 'provision_timeout'
    | 'drain_timeout'
    | 'max_runner_lifetime'
    | 'scale_up_delay'
    | 'docker_wait';

  interface Setting {
    key: Key;
    label: string;
    hint: string;
    /** What a zero means here, which is never "unset" and never the same twice. */
    zero: string;
    /** True for a setting that only applies to a pool giving its jobs Docker. */
    needsDaemon?: boolean;
  }

  const SETTINGS: readonly Setting[] = [
    {
      key: 'provision_timeout',
      label: 'Provision timeout',
      hint: 'How long a runner of this pool may take to register before it is given up on. Raise it for a pool with a large image or a slow registry.',
      zero: 'never give up on a runner that is still starting',
    },
    {
      key: 'drain_timeout',
      label: 'Drain timeout',
      hint: 'How long a runner may sit draining with no job left on it before its slot is taken back. A runner still finishing a job is never touched by this.',
      zero: 'leave a drain unbounded',
    },
    {
      key: 'max_runner_lifetime',
      label: 'Maximum runner lifetime',
      hint: 'A runner this old is drained the next time it is not busy. It never ends a job, so it is no answer to a hung one.',
      zero: 'let a runner live until something else removes it',
    },
    {
      key: 'scale_up_delay',
      label: 'Scale-up delay',
      hint: 'How long a job for this pool must have been queued before it counts as demand. Lower it for a pool whose jobs are short.',
      zero: 'scale the moment a job is queued',
    },
    {
      key: 'docker_wait',
      label: 'Docker wait',
      hint: 'How long a runner waits for the Docker daemon it was promised before refusing a job. Only a pool that gives its jobs a daemon does this wait.',
      zero: "leave the runner image's own wait in place",
      needsDaemon: true,
    },
  ];

  const givesDaemon = $derived(draft.docker_mode !== 'none');
  const overridden = $derived(SETTINGS.filter((s) => draft[s.key].trim() !== '').length);

  function placeholder(setting: Setting): string {
    const value = fleetDefaults?.[setting.key];
    return value ? `${value} — this fleet's` : "this fleet's";
  }

  /*
    The one relationship between these settings that is worth catching before
    the server does.

    A provision timeout inside the time a runner legitimately takes to start --
    the agent's create budget plus this pool's own Docker wait -- fails runners
    that are still coming up, and the replacement pulls the same image over the
    link that was slow to begin with. The fleet's own defaults are held out of
    that order by a test; a pool that overrides either half is not, which is
    exactly what this panel makes possible.

    It is a caution rather than an error: the server raises the same thing as a
    pool warning, and there are fleets whose images are already on every host
    where a shorter timeout is a reasonable thing to want. Saying it here means
    it is read while the number is being chosen rather than afterwards.
  */
  const CREATE_BUDGET_MINUTES = 15;

  function minutes(value: string): number | null {
    const text = value.trim();
    if (text === '') return null;
    const match = /^(\d+(?:\.\d+)?)(ms|s|m|h)$/.exec(text);
    if (!match) return null;
    const amount = Number(match[1]);
    switch (match[2]) {
      case 'ms':
        return amount / 60000;
      case 's':
        return amount / 60;
      case 'h':
        return amount * 60;
      default:
        return amount;
    }
  }

  const ladder = $derived.by(() => {
    const timeout = minutes(draft.provision_timeout);
    // Nothing typed follows the fleet, whose own order is already held; zero
    // is "never give up", which cannot condemn anything.
    if (timeout === null || timeout === 0) return '';
    const wait = givesDaemon ? (minutes(draft.docker_wait) ?? 2) : 0;
    const start = CREATE_BUDGET_MINUTES + wait;
    if (timeout > start) return '';
    const because = givesDaemon
      ? `${CREATE_BUDGET_MINUTES}m for the create and ${wait}m waiting for Docker`
      : `${CREATE_BUDGET_MINUTES}m for the create`;
    return `A runner of this pool may legitimately take ${start}m to start — ${because} — so a ${draft.provision_timeout.trim()} timeout fails runners that are still coming up, and the replacement pulls the same image again.`;
  });
</script>

<p class="lede">
  Every runner timing has a fleet-wide answer on the Settings page, and this pool follows it unless
  you say otherwise here. Leave a field empty to keep following — including after the fleet's own
  figure is changed.
</p>

<div class="settings">
  {#each SETTINGS as setting (setting.key)}
    {@const inactive = setting.needsDaemon === true && !givesDaemon}
    <Field
      label={setting.label}
      error={errors[`runner_settings.${setting.key}`]}
      hint={inactive
        ? `${setting.hint} This pool's jobs get no daemon, so it changes nothing here.`
        : `${setting.hint} 0 means: ${setting.zero}.`}
    >
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={draft[setting.key]}
          {id}
          {describedBy}
          {invalid}
          disabled={inactive}
          placeholder={placeholder(setting)}
          autocomplete="off"
          spellcheck={false}
          onblur={() => touch(`runner_settings.${setting.key}`)}
        />
      {/snippet}
    </Field>
  {/each}
</div>

{#if ladder}
  <div class="callout" role="status">
    <TriangleAlert size={16} aria-hidden="true" />
    <div>
      <p class="callout-title">Shorter than a runner of this pool takes to start</p>
      <p>{ladder}</p>
    </div>
  </div>
{/if}

<p class="summary">
  {#if overridden === 0}
    This pool follows the fleet on every runner timing.
  {:else}
    This pool overrides {overridden} of {SETTINGS.length} runner timings; the rest follow the fleet.
  {/if}
</p>

<style>
  .lede {
    margin: 0 0 var(--z-space-5);
    color: var(--z-text-muted);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
  }

  .settings {
    display: grid;
    gap: var(--z-space-4);
  }

  /* The same callout the Size step uses for the cache that outgrows its disk:
     one shape for "this is allowed, and here is what it costs". */
  .callout {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
    margin: var(--z-space-4) 0 0;
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-pending-border);
    border-radius: var(--z-radius-md);
    background: var(--z-pending-subtle);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }

  .callout p {
    margin: 0;
    max-width: 70ch;
  }

  .callout-title {
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }

  .summary {
    margin: var(--z-space-4) 0 0;
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }
</style>
