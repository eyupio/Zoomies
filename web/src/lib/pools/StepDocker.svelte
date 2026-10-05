<!--
  The simple path's one question that a fleet cannot answer for you.

  Everything else the automatic path decides has a right answer the controller
  already knows: the size is a slot's share of whatever host the runner lands
  on, the hosts are all of them, the image is the published one for the host's
  platform. Whether the jobs *in this pool* build container images is not in
  that list -- nothing in a name, a label or a host says it -- and until it was
  asked here, the only way to turn Docker on was to know that it lived behind
  the advanced path.

  It is a yes or no rather than the three-way docker_mode, because the third
  answer hands every job on the pool root on the host. That one stays on the
  advanced path, where it is chosen deliberately and confirmed.

  The cost is on the card that carries it: a daemon is a privileged sidecar
  container beside each runner, and the two share the slot they land in, so a
  job here gets half of one rather than a host holding half as many runners.
-->
<script lang="ts">
  import { Check, Container, Package } from '@lucide/svelte';
  import type { DockerMode } from '$lib/api/types';
  import type { PoolDraft } from './draft';

  interface Props {
    draft: PoolDraft;
    touch: (field: string) => void;
  }

  let { draft, touch }: Props = $props();

  // The simple path offers two of the three. A pool that already asks for the
  // host socket is opened on the advanced path, so this never has to render a
  // choice it cannot express.
  const wantsDocker = $derived((draft.docker_mode ?? 'none') !== 'none');

  const options: {
    value: DockerMode;
    icon: typeof Package;
    title: string;
    blurb: string;
  }[] = [
    {
      value: 'none',
      icon: Package,
      title: 'No',
      blurb:
        'Nothing extra. Most pools never build an image, and a runner without a daemon starts faster.',
    },
    {
      value: 'dind',
      icon: Container,
      title: 'Yes',
      blurb:
        'Each runner gets a Docker daemon of its own, and an image with the client to reach it.',
    },
  ];

  function choose(value: DockerMode): void {
    draft.docker_mode = value;
    touch('docker_mode');
  }
</script>

<p class="lede">
  A job that runs <code>docker build</code>, or names a <code>container:</code> or
  <code>services:</code> block, needs a Docker daemon of its own. Nothing else in this pool has to change
  either way.
</p>

<div
  class="choices"
  role="radiogroup"
  aria-label="Whether jobs in this pool build container images"
>
  {#each options as option (option.value)}
    {@const selected = (draft.docker_mode ?? 'none') === option.value}
    <label class="choice" class:selected>
      <input
        type="radio"
        name="pool-wants-docker"
        value={option.value}
        checked={selected}
        onchange={() => choose(option.value)}
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

{#if wantsDocker}
  <div class="detail">
    <h3>What that costs</h3>
    <ul>
      <li>
        The daemon runs in a <strong>privileged container</strong> beside each runner. A job that gets
        into it is on the host's kernel, which is why this is off unless asked for.
      </li>
      <li>
        The runner and its daemon <strong>share one slot</strong> of whichever host they land on, so a
        host holds as many runners of this pool as of any other — each with a little less machine to itself.
      </li>
    </ul>
    <p class="after">
      The runner image follows automatically: the published image plus a Docker client, under the
      same tag. Nothing to pin, and the review step shows what it settled on.
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

  /* The radio carries the keyboard behaviour and the accessible name, and the
     card is its label: the input is stretched over the card rather than shrunk
     to a pixel, which is the same arrangement the setup step uses. */
  .choice input {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    margin: 0;
    opacity: 0;
    cursor: pointer;
  }

  .head,
  .blurb {
    position: relative;
    pointer-events: none;
  }

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
