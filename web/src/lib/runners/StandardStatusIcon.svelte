<!--
  The Standard runner dog: the same Lucide dog icon as Eli's avatar and two paw
  prints, with the state's own Lucide glyph in the corner as a cue that stays
  put when motion is off. Everything is one SVG so a workflow pack is still
  three marks, the state's colour tints all of it, and each state's gesture is
  a set of keyframes for those four marks rather than a drawing.
-->
<script lang="ts">
  import {
    Activity,
    CircleDot,
    CircleMinus,
    CircleQuestionMark,
    CircleX,
    Dog,
    Handshake,
    Hourglass,
    LoaderCircle,
    PawPrint,
    TrendingUp,
    Turtle,
    Zap,
  } from '@lucide/svelte';
  import { standardMotion, type DogCue } from './standard-motion';
  let { state, seed = 'zoomies' }: { state: string; seed?: string } = $props();
  const animation = $derived(standardMotion(state, seed));
  // One glyph means one thing wherever it appears, so a cue is the icon Off
  // draws for the same state; throttled, which Off has no glyph for, is a
  // turtle rather than a pause, since pause is a button on the Queue.
  const CUES: Record<DogCue, typeof Dog> = {
    work: Activity,
    boost: TrendingUp,
    maximum: Zap,
    ready: CircleDot,
    search: LoaderCircle,
    hello: Handshake,
    pause: Turtle,
    rest: Hourglass,
    error: CircleX,
    gone: CircleMinus,
    question: CircleQuestionMark,
  };
  const Cue = $derived(CUES[animation.cue]);
</script>

<svg
  class="standard-dog"
  class:still={animation.still}
  data-style="standard"
  data-motion={animation.motion}
  data-state={animation.state}
  data-cue={animation.cue}
  viewBox="0 0 44 44"
  aria-hidden="true"
  focusable="false"
  style:--period={`${animation.duration}s`}
  style:--phase={`${animation.phase}s`}
>
  <g class="paw first"><PawPrint x="7" y="33" size={9} /></g>
  <g class="paw second"><PawPrint x="23" y="33" size={9} /></g>
  <g class="dog"><Dog x="8" y="9" size={24} /></g>
  <g class="cue"><Cue x="31" y="1" size={12} strokeWidth={2.4} /></g>
</svg>

<style>
  .standard-dog {
    display: block;
    flex: none;
    width: var(--z-avatar-size);
    height: var(--z-avatar-size);
    overflow: visible;
    color: var(--status-colour, currentColor);
  }
  .dog,
  .paw {
    transform-box: fill-box;
    transform-origin: center;
  }
  .paw {
    opacity: 0.45;
  }
  /* Each gesture names its keyframes below; the clock is the runner's own. */
  .dog {
    animation-duration: var(--period);
    animation-delay: var(--phase);
    animation-timing-function: ease-in-out;
    animation-iteration-count: infinite;
  }
  [data-motion='trot'] .dog {
    animation-name: trot;
  }
  [data-motion='trot'] .first {
    animation: step var(--period) var(--phase) ease-in-out infinite;
  }
  [data-motion='trot'] .second {
    animation: step var(--period) var(--phase) ease-in-out infinite reverse;
  }
  [data-motion='zoomies'] .dog {
    animation-name: zoomies;
  }
  [data-motion='orbit'] .dog {
    animation-name: orbit;
  }
  [data-motion='zoomies'] .paw,
  [data-motion='orbit'] .paw,
  [data-motion='sniff'] .paw {
    opacity: 0;
    animation: trail var(--period) var(--phase) infinite;
  }
  [data-motion='zoomies'] .second,
  [data-motion='orbit'] .second,
  [data-motion='sniff'] .second {
    animation-delay: calc(var(--phase) + var(--period) * 0.15);
  }
  [data-motion='wait'] .dog {
    animation-name: wait;
  }
  [data-motion='sniff'] .dog {
    animation-name: sniff;
  }
  [data-motion='wave'] .dog {
    animation-name: hello;
  }
  [data-motion='wave'] .second {
    transform-origin: bottom left;
    animation: wave var(--period) var(--phase) ease-in-out infinite;
  }
  [data-motion='settle'] .dog {
    animation-name: settle;
  }
  /* The three states that are nothing to be happy about hold a pose instead. */
  .still .dog {
    animation: none;
  }
  [data-motion='sad'] .dog {
    transform: translateY(2px) rotate(-9deg);
  }
  [data-motion='sleep'] .dog {
    transform: translateY(4px) scaleY(0.85);
    opacity: 0.7;
  }
  [data-motion='puzzled'] .dog {
    transform: rotate(12deg);
  }
  @keyframes trot {
    0%,
    50%,
    100% {
      transform: translateY(0);
    }
    25%,
    75% {
      transform: translateY(-3px);
    }
  }
  @keyframes step {
    25% {
      transform: translateY(-3px);
    }
    75% {
      transform: translateY(1px);
    }
  }
  @keyframes zoomies {
    0%,
    100% {
      transform: translateX(-6px) rotate(-8deg);
    }
    35% {
      transform: translate(6px, -3px) rotate(10deg);
    }
    50% {
      transform: translateX(6px) rotate(-6deg);
    }
    85% {
      transform: translate(-6px, -3px) rotate(8deg);
    }
  }
  @keyframes orbit {
    0%,
    100% {
      transform: translate(-6px, 0) rotate(-10deg);
    }
    25% {
      transform: translate(0, -5px) rotate(6deg) scale(0.92);
    }
    50% {
      transform: translate(6px, 0) rotate(10deg);
    }
    75% {
      transform: translate(0, 3px) rotate(-6deg) scale(1.06);
    }
  }
  @keyframes wait {
    35%,
    55% {
      transform: rotate(-10deg);
    }
  }
  @keyframes sniff {
    0%,
    100% {
      transform: rotate(0);
    }
    25%,
    65% {
      transform: translate(3px, 3px) rotate(14deg);
    }
    45% {
      transform: translate(5px, 2px) rotate(18deg);
    }
    80% {
      transform: translate(-2px, 0) rotate(-8deg);
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
      transform: translateY(-5px) rotate(-25deg);
    }
    45% {
      transform: translateY(-5px) rotate(15deg);
    }
  }
  @keyframes settle {
    50% {
      transform: translateY(3px) scaleY(0.9);
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
  /* Important because a gesture's own rule, such as the trotting paws, outranks
     a plain reset here: a media query adds no specificity of its own. */
  @media (prefers-reduced-motion: reduce) {
    .dog,
    .paw {
      animation: none !important;
    }
    .paw {
      opacity: 0.45;
    }
  }
</style>
