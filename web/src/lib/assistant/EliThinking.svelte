<!--
  What Eli does while an answer is on its way: the same dog icon as his avatar
  and two paw prints on a short track, in one of six routines. Every routine moves
  those three marks and nothing else, so the picture stays as light as the first
  one, and a new routine is a set of keyframes rather than a drawing.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { Dog, PawPrint } from '@lucide/svelte';
  import { thinkingBeat, THINKING_QUOTES, THINKING_QUOTE_MS } from './personality';
  let step = $state(0);
  let offset = $state(0);
  const beat = $derived(thinkingBeat(step, offset));
  onMount(() => {
    // Start each answer on a different part of the repertoire, not always lap one.
    offset = Math.floor(Math.random() * (THINKING_QUOTES.length / 2)) * 2;
    const timer = window.setInterval(() => {
      step += 1;
    }, THINKING_QUOTE_MS);
    return () => window.clearInterval(timer);
  });
</script>

<div class="thinking">
  <span class="sr-only">Eli is thinking</span>
  <div class="track" data-motion={beat.motion} aria-hidden="true">
    <span class="paw first"><PawPrint size={12} /></span>
    <span class="paw second"><PawPrint size={12} /></span>
    <span class="dog"><Dog size={28} /></span>
  </div>
  <p aria-hidden="true">{beat.quote}</p>
</div>

<style>
  .thinking {
    color: var(--z-text-muted);
    font-size: var(--z-text-sm);
  }
  .track {
    --period: 2.4s;
    position: relative;
    width: 8rem;
    height: var(--z-space-10);
    color: var(--z-accent);
  }
  .dog {
    position: absolute;
    animation: zoomies var(--period) ease-in-out infinite;
  }
  /* The paws share the dog's clock and show in the middle of each lap, so every
     routine puts its footwork there and the paws need no keyframes of their own. */
  .paw {
    position: absolute;
    top: var(--z-space-5);
    opacity: 0;
    animation: trail var(--period) infinite;
  }
  .first {
    left: var(--z-space-4);
  }
  .second {
    left: var(--z-space-10);
    animation-delay: 0.25s;
  }
  p {
    margin: 0;
    min-height: 2.5em;
  }

  /* Fetch: a sprint to the far end, a skid and a slower trot back. */
  [data-motion='fetch'] {
    --period: 3s;
  }
  [data-motion='fetch'] .dog {
    animation-name: fetch;
  }
  [data-motion='fetch'] .first {
    left: var(--z-space-12);
  }
  [data-motion='fetch'] .second {
    left: var(--z-space-16);
    animation-delay: 0.15s;
  }

  /* Sniff: nose down, shuffling along the trail the paws leave ahead of it. */
  [data-motion='sniff'] {
    --period: 3.6s;
  }
  [data-motion='sniff'] .dog {
    animation-name: sniff;
  }
  [data-motion='sniff'] .first {
    left: var(--z-space-8);
  }
  [data-motion='sniff'] .second {
    left: var(--z-space-12);
    animation-delay: 0.5s;
  }

  /* Pounce: a crouch, a spring across the track, and the same back again. */
  [data-motion='pounce'] .dog {
    animation-name: pounce;
  }
  [data-motion='pounce'] .first {
    left: var(--z-space-12);
  }
  [data-motion='pounce'] .second {
    left: var(--z-space-16);
    animation-delay: 0.1s;
  }

  /* Wiggle: on the spot, a paw tapping on either side in turn. */
  [data-motion='wiggle'] .dog {
    animation-name: wiggle;
  }
  [data-motion='wiggle'] .second {
    left: auto;
    right: var(--z-space-4);
    animation-delay: 0.6s;
  }

  /* Orbit: a loop around the track, paws at each end of it. */
  [data-motion='orbit'] .dog {
    animation-name: orbit;
  }
  [data-motion='orbit'] .first {
    left: var(--z-space-2);
  }
  [data-motion='orbit'] .second {
    left: var(--z-space-16);
    animation-delay: 0.4s;
  }

  @keyframes zoomies {
    0%,
    100% {
      transform: translateX(0) rotate(-10deg);
    }
    35% {
      transform: translate(5rem, -6px) rotate(12deg);
    }
    50% {
      transform: translateX(5rem) scaleX(-1);
    }
    85% {
      transform: translate(0, -6px) scaleX(-1) rotate(12deg);
    }
  }
  @keyframes fetch {
    0%,
    100% {
      transform: translateX(0);
    }
    28% {
      transform: translateX(5rem) rotate(10deg);
    }
    36% {
      transform: translateX(5.25rem) scaleX(-1) rotate(-8deg);
    }
    44% {
      transform: translate(5rem, -5px) scaleX(-1);
    }
    90% {
      transform: translateX(0) scaleX(-1);
    }
  }
  @keyframes sniff {
    0%,
    100% {
      transform: translateX(0) rotate(0);
    }
    20% {
      transform: translate(1rem, 3px) rotate(18deg);
    }
    40% {
      transform: translate(2rem, 1px) rotate(10deg);
    }
    60% {
      transform: translate(3rem, 3px) rotate(20deg);
    }
    80% {
      transform: translate(1.5rem, 0) rotate(6deg);
    }
  }
  @keyframes pounce {
    0%,
    100% {
      transform: translateX(0.5rem);
    }
    15% {
      transform: translate(0.5rem, 4px) scale(1.1, 0.85);
    }
    35% {
      transform: translate(4.5rem, -12px) rotate(10deg);
    }
    45% {
      transform: translate(4.5rem, 2px) scale(1.08, 0.92);
    }
    60% {
      transform: translate(4.5rem, 4px) scale(1.1, 0.85);
    }
    80% {
      transform: translate(0.5rem, -12px) rotate(-10deg);
    }
    90% {
      transform: translate(0.5rem, 2px) scale(1.08, 0.92);
    }
  }
  @keyframes wiggle {
    0%,
    60%,
    100% {
      transform: translateX(2.5rem) rotate(0);
    }
    15%,
    45% {
      transform: translateX(2.5rem) rotate(-12deg);
    }
    30% {
      transform: translate(2.5rem, -3px) rotate(12deg);
    }
  }
  @keyframes orbit {
    0%,
    100% {
      transform: translate(0, 4px) rotate(-10deg);
    }
    25% {
      transform: translate(2.5rem, -6px) rotate(6deg) scale(0.92);
    }
    50% {
      transform: translate(5rem, 4px) rotate(10deg);
    }
    75% {
      transform: translate(2.5rem, 10px) rotate(-6deg) scale(1.06);
    }
  }
  @keyframes trail {
    25%,
    65% {
      opacity: 0.5;
    }
    0%,
    100% {
      opacity: 0;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .dog,
    .paw {
      animation: none;
    }
  }
</style>
