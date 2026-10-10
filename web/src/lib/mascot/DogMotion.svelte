<script lang="ts">
  import { dogPhase, type DogMotion, type DogCue } from './dog-motion';
  let {
    motion = 'zoomies',
    cue = 'none',
    seed = 'eli',
    duration = 2.4,
    status,
    style: visualStyle,
  }: {
    motion?: DogMotion;
    cue?: DogCue;
    seed?: string;
    duration?: number;
    status?: string;
    style?: 'standard';
  } = $props();
  const still = $derived(['sad', 'sleep', 'puzzled'].includes(motion));
</script>

<svg
  class="dog-motion"
  class:still
  data-style={visualStyle}
  data-motion={motion}
  data-state={status}
  data-cue={cue}
  viewBox="0 0 80 64"
  fill="none"
  stroke="currentColor"
  stroke-width="2.5"
  stroke-linecap="round"
  stroke-linejoin="round"
  aria-hidden="true"
  focusable="false"
  style:--period={`${duration}s`}
  style:--phase={`${-dogPhase(seed) * duration}s`}
>
  {#if ['zoomies', 'orbit', 'trot'].includes(motion)}
    <g class="trail"
      ><path d="M7 34h8M10 41h5" />
      {#if cue === 'maximum'}<path d="M3 27h13" />{/if}
    </g>
  {/if}
  {#if motion === 'sniff'}
    <g class="scent"
      ><circle cx="57" cy="43" r="1" /><circle cx="64" cy="38" r="1" /><circle
        cx="68"
        cy="31"
        r="1"
      /></g
    >
  {/if}
  <g class="traveller">
    <g class="pup">
      <path class="ear left" d="M30 22C23 15 18 20 18 30Q19 38 27 32" />
      <path class="ear right" d="M49 22C56 15 61 20 61 30Q60 38 52 32" />
      <path class="face" d="M29 23Q39 16 50 23L53 35Q54 47 40 48Q25 47 26 35Z" />
      {#if motion === 'sleep' || motion === 'settle'}
        <path d="M31 33q3 3 6 0M43 33q3 3 6 0" />
      {:else if motion === 'sad'}
        <path d="m31 31 5 2m8 0 5-2M33 35v1m13-1v1" />
      {:else}
        <path d="M33 32v2m13-2v2" />
      {/if}
      <path d="M37 38h6l-3 3zM40 41v2" />
      {#if motion === 'sad'}<path d="M36 45q4-3 8 0" />
      {:else}<path d="M35 43q5 5 10 0" />{/if}
      <g class="paws">
        <path class="paw left-paw" d="M30 49q-5 0-5 4h11q0-4-6-4" />
        <path class="paw right-paw" d="M49 49q-5 0-5 4h11q0-4-6-4" />
      </g>
    </g>
  </g>
  <!-- These marks stay visible with motion disabled, so speed and colour are
       never the only ways to tell runner states apart. -->
  <g class="cue">
    {#if cue === 'work'}<path d="M63 10h7m-7 5h7" />
    {:else if cue === 'boost'}<path d="m61 16 5-5 5 5" />
    {:else if cue === 'maximum'}<path d="m61 12 5-5 5 5m-10 6 5-5 5 5" />
    {:else if cue === 'ready'}<path d="m60 13 4 4 7-8" />
    {:else if cue === 'search'}<circle cx="65" cy="12" r="4" /><path d="m68 15 4 4" />
    {:else if cue === 'hello'}<path d="m62 9 2 3m5-5v4m6 0-3 2" />
    {:else if cue === 'pause'}<path d="M63 8v10m7-10v10" />
    {:else if cue === 'rest'}<path d="M62 8h9l-9 10h9" />
    {:else if cue === 'error'}<path d="m62 9 9 9m0-9-9 9" />
    {:else if cue === 'gone'}<path d="M61 13h11" />
    {:else if cue === 'question'}<path d="M62 10q0-5 7-3 5 3-1 6l-2 1v2m0 5v.1" />{/if}
  </g>
</svg>

<style>
  .dog-motion {
    display: block;
    width: 100%;
    height: 100%;
    overflow: visible;
  }
  .pup,
  .ear,
  .paw,
  .traveller {
    transform-box: fill-box;
    transform-origin: center;
  }
  .pup {
    animation: wiggle var(--period) var(--phase) ease-in-out infinite;
  }
  .trail {
    opacity: 0.55;
    animation: fade var(--period) var(--phase) ease-in-out infinite;
  }
  .cue {
    stroke-width: 3;
  }
  [data-motion='zoomies'] .pup {
    animation-name: dash;
  }
  [data-motion='sniff'] .pup {
    animation-name: sniff;
  }
  [data-motion='orbit'] .pup {
    animation-name: orbit;
  }
  [data-motion='trot'] .pup {
    animation-name: trot;
  }
  [data-motion='wave'] .pup {
    animation-name: hello;
  }
  [data-motion='settle'] .pup {
    animation-name: settle;
  }
  [data-motion='wait'] .pup {
    animation-name: wait;
  }
  [data-motion='sniff'] .scent {
    animation: fade var(--period) var(--phase) ease-in-out infinite;
  }
  [data-motion='wave'] .right-paw {
    transform-origin: bottom left;
    animation: wave var(--period) var(--phase) ease-in-out infinite;
  }
  [data-motion='trot'] .left-paw,
  [data-motion='zoomies'] .left-paw {
    animation: step var(--period) var(--phase) ease-in-out infinite;
  }
  [data-motion='trot'] .right-paw,
  [data-motion='zoomies'] .right-paw {
    animation: step var(--period) var(--phase) ease-in-out infinite reverse;
  }
  [data-motion='sad'] .pup {
    transform: translateY(3px) rotate(-8deg);
  }
  [data-motion='sleep'] .pup {
    transform: translateY(5px) scaleY(0.85);
  }
  [data-motion='puzzled'] .pup {
    transform: rotate(12deg);
  }
  .still .pup {
    animation: none;
  }
  @keyframes dash {
    0%,
    100% {
      transform: translateX(-10px) rotate(-8deg);
    }
    35% {
      transform: translate(12px, -5px) rotate(10deg);
    }
    50% {
      transform: translateX(12px) rotate(-8deg);
    }
    85% {
      transform: translate(-10px, -5px) rotate(8deg);
    }
  }
  @keyframes sniff {
    0%,
    100% {
      transform: rotate(0);
    }
    25%,
    65% {
      transform: translate(5px, 5px) rotate(12deg);
    }
    45% {
      transform: translate(8px, 3px) rotate(16deg);
    }
    80% {
      transform: translate(-3px, 0) rotate(-10deg);
    }
  }
  @keyframes wiggle {
    0%,
    60%,
    100% {
      transform: rotate(0);
    }
    15%,
    45% {
      transform: rotate(-12deg);
    }
    30% {
      transform: rotate(12deg) translateY(-3px);
    }
  }
  @keyframes orbit {
    0%,
    100% {
      transform: translate(-10px, 0) rotate(-12deg);
    }
    25% {
      transform: translate(0, -9px) rotate(8deg) scale(0.9);
    }
    50% {
      transform: translate(12px, 0) rotate(12deg);
    }
    75% {
      transform: translate(0, 4px) rotate(-8deg) scale(1.05);
    }
  }
  @keyframes trot {
    0%,
    50%,
    100% {
      transform: translateY(0);
    }
    25%,
    75% {
      transform: translateY(-4px);
    }
  }
  @keyframes step {
    25% {
      transform: translateY(-4px);
    }
    75% {
      transform: translateY(1px);
    }
  }
  @keyframes hello {
    30%,
    60% {
      transform: rotate(-6deg);
    }
  }
  @keyframes wave {
    25%,
    65% {
      transform: translateY(-10px) rotate(-25deg);
    }
    45% {
      transform: translateY(-10px) rotate(15deg);
    }
  }
  @keyframes settle {
    50% {
      transform: translateY(4px) scaleY(0.9);
    }
  }
  @keyframes wait {
    35%,
    55% {
      transform: rotate(-10deg);
    }
  }
  @keyframes fade {
    0%,
    100% {
      opacity: 0.2;
    }
    50% {
      opacity: 0.7;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .dog-motion :global(*) {
      animation: none !important;
    }
  }
</style>
