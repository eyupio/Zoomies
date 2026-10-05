<!--
  Step one: how much of this pool you want to decide.

  It is the first screen because it is the first decision, and because the two
  answers lead to genuinely different amounts of work. A fleet's first pool
  needs a name, a target and a label; every other figure on the old seven-step
  form had a right answer the controller already knew and was asking for
  anyway. Making someone walk past five screens of correct defaults to reach
  the review step taught them that those screens matter, which is the opposite
  of true.

  The two cards say what each path *does* rather than how long it is, because
  "simple" and "advanced" are only meaningful next to what they decide. The
  automatic card names the share these hosts would actually hand a runner --
  computed from the fleet in front of the operator, not from a constant -- so
  the claim is checkable on the screen that makes it.

  An edit never reaches this step. The pool has already answered the question,
  the wizard reads the answer off it to choose a path, and an operator who
  opened a pool to change one setting should not have to pass a screen about
  how much of it they want to decide. A simple edit offers the advanced path
  from every step instead, so what this fork would have opened is still one
  click away.
-->
<script lang="ts">
  import { Check, Sliders, Wand } from '@lucide/svelte';
  import type { Host } from '$lib/api/types';
  import { formatMegabytes, pluralise } from '$lib/format';
  import type { WizardMode } from './vocabulary';

  interface Props {
    mode: WizardMode;
    hosts: readonly Host[];
    hostsKnown: boolean;
  }

  let { mode = $bindable('simple'), hosts, hostsKnown }: Props = $props();

  /*
    What the automatic path would actually hand a runner, on the hosts this
    fleet has right now.

    It is the share the controller computes -- allocatable CPU divided by the
    host's slots -- shown for the smallest and largest hosts so that an
    operator with unequal machines can see the thing this path exists for: one
    pool, correctly sized on both. A fleet of one host shows one figure.

    The arithmetic is only ever used to describe; what a runner is actually
    given is the controller's, and the review step asks it for the real
    numbers. A browser that worked out placement here would disagree with the
    fleet the moment a pool used docker-in-docker.
  */
  const shares = $derived.by(() => {
    const out: { host: string; cpus: number; memoryMb: number }[] = [];
    for (const host of hosts) {
      const slots = host.effective_capacity ?? host.capacity ?? 0;
      const cpus = host.allocatable_cpus ?? 0;
      const memory = host.allocatable_memory_mb ?? 0;
      if (slots < 1 || cpus <= 0) continue;
      out.push({
        host: host.name ?? '',
        cpus: Math.floor((cpus / slots) * 100) / 100,
        memoryMb: Math.floor(memory / slots),
      });
    }
    return out.sort((a, b) => a.cpus - b.cpus);
  });

  const smallest = $derived(shares[0]);
  const largest = $derived(shares[shares.length - 1]);
  const spread = $derived(
    smallest !== undefined && largest !== undefined && smallest.cpus !== largest.cpus,
  );

  function share(entry: { cpus: number; memoryMb: number }): string {
    return `${entry.cpus} CPU and ${formatMegabytes(entry.memoryMb)}`;
  }

  const options: { value: WizardMode; icon: typeof Wand; title: string; blurb: string }[] = [
    {
      value: 'simple',
      icon: Wand,
      title: 'Automatic',
      blurb: 'Name it, label it, done. Everything else follows this fleet.',
    },
    {
      value: 'advanced',
      icon: Sliders,
      title: 'Advanced',
      blurb: 'Choose the size, the hosts, the backend and the runner timings yourself.',
    },
  ];
</script>

<p class="lede">
  Most pools need a name, a label and nothing else — every other setting has an answer this fleet
  already knows. Choose what you want to decide.
</p>

<div class="choices" role="radiogroup" aria-label="How much of this pool to decide">
  {#each options as option (option.value)}
    {@const selected = mode === option.value}
    <label class="choice" class:selected>
      <input
        type="radio"
        name="pool-wizard-mode"
        value={option.value}
        checked={selected}
        onchange={() => (mode = option.value)}
      />
      <span class="head">
        <option.icon size={18} aria-hidden="true" />
        <span class="title">{option.title}</span>
        {#if selected}<Check class="tick" size={16} aria-hidden="true" />{/if}
      </span>
      <span class="blurb">{option.blurb}</span>
    </label>
  {/each}
</div>

{#if mode === 'simple'}
  <div class="detail">
    <h3>What this fleet decides for you</h3>
    <ul>
      <li>
        <strong>Size</strong> — each runner is given one slot's share of whichever host it lands on,
        as a real limit rather than a promise.
        {#if !hostsKnown}
          The share is worked out per host.
        {:else if shares.length === 0}
          The share is worked out once a host has reported what machine it is.
        {:else if spread && smallest && largest}
          Today that is {share(smallest)} on
          <code>{smallest.host}</code>, and {share(largest)} on <code>{largest.host}</code> — one pool,
          sized correctly on both.
        {:else if smallest}
          Today that is {share(smallest)} on
          {pluralise(shares.length, 'host')}.
        {/if}
      </li>
      <li>
        <strong>Hosts</strong> — any machine in the fleet that can run it. Nothing is excluded, so a host
        added next month picks up this pool's work without an edit.
      </li>
      <li>
        <strong>Backend and image</strong> — Docker, on the published runner image for the host's platform.
      </li>
      <li>
        <strong>Timings</strong> — this fleet's own, and they keep following it when you change them.
      </li>
    </ul>
    <p class="after">
      All of it is editable afterwards. Switching to advanced never loses anything you have already
      typed.
    </p>
  </div>
{:else}
  <div class="detail">
    <h3>What you will be asked</h3>
    <ul>
      <li><strong>Hosts</strong> — which machines this pool may land on.</li>
      <li><strong>Backend</strong> — Docker, Podman or the host itself, and the runner image.</li>
      <li>
        <strong>Size</strong> — a fixed CPU and memory limit on every host, or the automatic share.
      </li>
      <li><strong>Scaling</strong> — how many runners, and how long an idle one waits.</li>
      <li>
        <strong>Runners</strong> — the provision, drain and lifetime timings, where this pool disagrees
        with the fleet.
      </li>
    </ul>
    <p class="after">
      Every one of them opens on the answer the automatic path would have used, so you only change
      what you mean to.
    </p>
  </div>
{/if}

<style>
  .lede {
    margin: 0 0 var(--z-space-5);
    color: var(--z-text-muted);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
  }

  .choices {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--z-space-3);
  }

  /* --z-bp-md, written out: a media query is evaluated before custom
     properties exist. */
  @media (max-width: 768px) {
    .choices {
      grid-template-columns: minmax(0, 1fr);
    }
  }

  .choice {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    padding: var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-lg);
    background: var(--z-surface-raised);
    cursor: pointer;
    transition:
      border-color var(--z-motion-fast) var(--z-ease),
      background var(--z-motion-fast) var(--z-ease);
  }

  .choice:hover {
    border-color: var(--z-border-strong);
    background: var(--z-surface-hover);
  }

  .choice.selected {
    border-color: var(--z-accent);
    background: var(--z-accent-subtle);
  }

  /*
    The radio is the control -- it carries the keyboard behaviour and the
    accessible name -- and the card is its label. The input is made invisible
    rather than taken out of the tree, and it is stretched over the whole card
    rather than shrunk to a pixel: a one-pixel input is a one-pixel click
    target, which is as awkward for a pointer as it is for a test driving one.
  */
  .choice input {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    margin: 0;
    opacity: 0;
    cursor: pointer;
  }

  /* The card's own text sits under the input, which is the click target, so
     nothing inside it can swallow a press meant for the radio. */
  .head,
  .blurb {
    position: relative;
    pointer-events: none;
  }

  /* The ring is drawn on the card because the input it belongs to is out of
     sight; :focus-visible on the input is what decides, so a mouse click
     selects without lighting it up. */
  .choice:has(input:focus-visible) {
    box-shadow: var(--z-focus-ring);
  }

  .head {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    color: var(--z-text);
  }

  .title {
    font-weight: var(--z-weight-semibold);
  }

  .head :global(.tick) {
    margin-inline-start: auto;
    color: var(--z-accent);
  }

  .blurb {
    color: var(--z-text-muted);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
  }

  .detail {
    margin-top: var(--z-space-5);
    padding: var(--z-space-4);
    border-radius: var(--z-radius-lg);
    background: var(--z-surface-sunken);
  }

  .detail h3 {
    margin: 0 0 var(--z-space-3);
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-semibold);
    letter-spacing: var(--z-tracking-wide);
    text-transform: uppercase;
    color: var(--z-text-muted);
  }

  .detail ul {
    display: grid;
    gap: var(--z-space-2);
    margin: 0;
    padding: 0;
    list-style: none;
    color: var(--z-text-muted);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
  }

  .detail strong {
    color: var(--z-text);
    font-weight: var(--z-weight-medium);
  }

  .after {
    margin: var(--z-space-3) 0 0;
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-sm);
  }
</style>
