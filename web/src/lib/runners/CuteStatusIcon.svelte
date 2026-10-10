<!-- Original Zoomies cocker spaniel avatar, based on the approved status previews. -->
<script lang="ts">
  import { cuteMotion } from './cute-motion';
  let { state, seed = 'zoomies' }: { state: string; seed?: string } = $props();
  const gait = $derived(cuteMotion(state, seed));
  const energetic = $derived(['busy', 'zoomies', 'maximum_zoomies'].includes(state));
  // Stable per-runner variation avoids fleet-wide synchronisation and never
  // restarts on CPU sample updates. Motion stays entirely in CSS: no timers.
  const timing = $derived.by(() => {
    let hash = 2166136261;
    for (const char of seed) hash = Math.imul(hash ^ char.charCodeAt(0), 16777619) >>> 0;
    const fraction = (hash % 1000) / 1000;
    const base = state === 'maximum_zoomies' ? 4 : state === 'zoomies' ? 6 : 9;
    return {
      duration: `${base + fraction * 3}s`,
      delay: `-${fraction * 13}s`,
      blink: `${5 + fraction * 4}s`,
    };
  });
</script>

<svg
  class="zoomies-mark"
  data-style="cute"
  data-motion={state}
  class:energetic
  class:walking={gait.walking}
  class:boosted={gait.spinning}
  viewBox="0 0 240 220"
  fill="none"
  aria-hidden="true"
  focusable="false"
  style:--lap={timing.duration}
  style:--phase={timing.delay}
  style:--blink={timing.blink}
  style:--stride={`${gait.stride}s`}
  style:--gait-phase={`${gait.phase}s`}
>
  <circle cx="120" cy="114" r="86" fill="var(--z-surface-sunken)" />
  <ellipse class="shadow" cx="120" cy="192" rx="55" ry="7" fill="var(--z-border)" />
  {#if gait.spinning}
    <g
      class="speed"
      fill="none"
      stroke="var(--z-avatar-accent)"
      stroke-width="3"
      stroke-linecap="round"><path d="M23 104h20M17 122h16M30 141h16M194 98h14M199 117h21" /></g
    >
  {/if}
  <g class="dog">
    {#if energetic}
      <!-- Rear feet sit behind the torso; their roots stay covered as they
           step. Four beats read as a walk even in this front-facing pose. -->
      <g class="hind-l">
        <path class="fur" d="M92 153Q78 153 77 169L73 181Q74 187 86 185L99 162Z" />
      </g>
      <g class="hind-r">
        <path class="fur" d="M145 153Q159 153 162 169L167 181Q166 188 154 185L137 162Z" />
      </g>
    {/if}
    <path class="tail fur" d="M149 161Q188 133 193 151Q194 168 163 177Z" />
    <path class="fur" d="M83 155Q82 134 110 132L133 133Q158 140 157 173L145 183H92Z" />
    <path class="white" d="M104 138Q119 146 139 139L137 169Q118 180 102 166Z" />
    <g class="face">
      <g class="ear-l">
        <path
          class="fur"
          d="M84 66Q61 59 51 81Q45 96 45 119Q38 128 49 132Q45 143 56 143Q61 155 71 145Q84 149 88 135L93 83Z"
        />
        <path class="shine" d="M65 88Q55 109 59 125" />
      </g>
      <g class="ear-r">
        <path
          class="fur"
          d="M153 66Q176 61 185 82Q189 97 190 120Q198 132 186 135Q189 146 177 146Q170 156 161 144Q150 148 147 131L145 81Z"
        />
        <path class="shine" d="M174 88Q182 106 177 127" />
      </g>
      <path
        class="fur"
        d="M76 89Q76 59 100 53L98 45L114 50L124 42L128 51Q161 51 165 84L163 117Q158 140 121 146Q87 144 77 120Z"
      />
      <path
        fill="var(--z-avatar-cream)"
        d="M114 55Q124 52 129 56L127 77Q124 92 131 104L140 120L99 124Q100 104 111 90Q119 74 114 55Z"
      />
      <path class="shine" d="M91 76Q96 65 104 64M140 65Q151 68 154 78" />
      {#if state === 'removed'}
        <path class="line" d="M88 103q10 8 20 0m28 0q10 8 20 0" />
      {:else}
        <g class="eyes"
          ><ellipse cx="98" cy="102" rx="12" ry="15" fill="var(--z-avatar-cream)" /><ellipse
            cx="146"
            cy="101"
            rx="12"
            ry="15"
            fill="var(--z-avatar-cream)"
          /><ellipse cx="102" cy="103" rx="7" ry="10" fill="var(--z-avatar-ink)" /><ellipse
            cx="149"
            cy="102"
            rx="7"
            ry="10"
            fill="var(--z-avatar-ink)"
          /><circle cx="104" cy="99" r="2.7" fill="var(--z-avatar-white)" /><circle
            cx="151"
            cy="98"
            r="2.7"
            fill="var(--z-avatar-white)"
          /></g
        >
      {/if}
      {#if energetic}
        <path
          d="M101 127Q122 117 143 125Q141 148 123 150Q105 146 101 127Z"
          fill="var(--z-avatar-ink)"
        />
        <g class="tongue">
          <path
            d="M116 135Q124 132 131 136L130 145Q124 154 117 146Z"
            fill="var(--z-avatar-tongue)"
          /><path d="M124 138v7" stroke="var(--z-avatar-tongue-line)" stroke-width="1.4" />
        </g>
      {:else}
        <path class="line" d={state === 'failed' ? 'M109 139q13-10 26 0' : 'M107 131q16 12 31-1'} />
      {/if}
      <path
        class="white"
        d="M121 115Q108 110 98 119Q88 132 105 136Q115 139 122 131Q131 139 143 132Q153 123 140 117Q133 112 121 115Z"
      />
      <path
        d="M113 114Q122 109 133 114Q133 122 123 125Q114 123 113 114Z"
        fill="var(--z-avatar-ink)"
      /><path
        d="M119 114h7"
        stroke="var(--z-avatar-shine)"
        stroke-width="2"
        stroke-linecap="round"
      />
      <g class="freckles" fill="var(--z-avatar-freckle)"
        ><circle cx="102" cy="126" r="1.3" /><circle cx="108" cy="129" r="1.1" /><circle
          cx="138"
          cy="125"
          r="1.3"
        /></g
      >
      <path
        d="M92 145Q121 156 148 142L149 150Q120 166 92 153Z"
        fill="currentColor"
        stroke="var(--z-avatar-ink)"
        stroke-width="2"
      />
      <circle
        cx="123"
        cy="158"
        r="6"
        fill="var(--z-avatar-tag)"
        stroke="var(--z-avatar-ink)"
        stroke-width="1.5"
      /><path
        d="M120 156h5l-5 4h5"
        fill="none"
        stroke="var(--z-avatar-tag-ink)"
        stroke-width="1.3"
      />
    </g>
    <g class="paw-l"
      ><path class="white" d="M83 162Q96 155 105 167L105 181Q87 192 76 180Q72 171 83 162Z" /><path
        class="line"
        d="M84 179v5M92 179v6"
      /></g
    >
    <g class="paw-r"
      ><path class="white" d="M136 164Q147 155 159 165Q174 178 160 186L139 184Z" /><path
        class="line"
        d="M149 179v6M158 178v6"
      /></g
    >
  </g>
</svg>

<style>
  .zoomies-mark {
    width: var(--z-avatar-size);
    height: var(--z-avatar-size);
    flex: none;
    overflow: hidden;
  }
  .fur {
    fill: var(--z-avatar-fur);
    stroke: var(--z-avatar-ink);
    stroke-width: 2.4;
    stroke-linejoin: round;
  }
  .white {
    fill: var(--z-avatar-cream);
    stroke: var(--z-avatar-ink);
    stroke-width: 2.4;
    stroke-linejoin: round;
  }
  .shine {
    fill: none;
    stroke: var(--z-avatar-shine);
    stroke-width: 3;
    stroke-linecap: round;
  }
  .line {
    fill: none;
    stroke: var(--z-avatar-ink);
    stroke-width: 2.4;
    stroke-linecap: round;
  }
  .dog,
  .face,
  .ear-l,
  .ear-r,
  .paw-l,
  .paw-r,
  .hind-l,
  .hind-r,
  .shadow,
  .tail,
  .eyes,
  .speed,
  .tongue {
    transform-box: view-box;
    animation-duration: var(--lap);
    animation-delay: var(--phase);
    animation-timing-function: ease-in-out;
    animation-iteration-count: infinite;
  }
  .dog {
    transform-origin: 120px 180px;
    animation-name: breathe;
  }
  .face {
    transform-origin: 120px 123px;
  }
  .ear-l {
    transform-origin: 84px 73px;
  }
  .ear-r {
    transform-origin: 151px 72px;
  }
  .paw-l {
    transform-origin: 89px 169px;
  }
  .paw-r {
    transform-origin: 148px 169px;
  }
  .tail {
    transform-origin: 151px 165px;
    animation-name: tail-wag;
  }
  .eyes {
    transform-origin: 120px 102px;
    animation-name: blink;
    animation-duration: var(--blink);
  }
  .tongue {
    transform-origin: 124px 133px;
    animation-name: pant;
  }
  /* One stride clock for weight transfer, feet and follow-through. The
     slow lifecycle gestures retain their own lap and do not inherit it. */
  .energetic .dog,
  .energetic .face,
  .energetic .ear-l,
  .energetic .ear-r,
  .energetic .paw-l,
  .energetic .paw-r,
  .energetic .hind-l,
  .energetic .hind-r,
  .energetic .tail,
  .energetic .tongue,
  .energetic .shadow,
  .speed {
    animation-duration: var(--stride);
    animation-delay: var(--gait-phase);
  }
  .hind-l {
    transform-origin: 92px 157px;
  }
  .hind-r {
    transform-origin: 145px 157px;
  }
  .shadow {
    transform-origin: 120px 192px;
  }
  .walking .dog {
    animation-name: walk-body;
  }
  .walking .face {
    animation-name: walk-face;
  }
  .walking .paw-l,
  .walking .paw-r,
  .walking .hind-l,
  .walking .hind-r {
    animation-name: walk-step;
  }
  .walking .paw-r {
    animation-delay: calc(var(--gait-phase) - var(--stride) * 0.5);
  }
  .walking .hind-l {
    animation-delay: calc(var(--gait-phase) - var(--stride) * 0.25);
  }
  .walking .hind-r {
    animation-delay: calc(var(--gait-phase) - var(--stride) * 0.75);
  }
  .walking .ear-l {
    animation-name: walk-ear-left;
  }
  .walking .ear-r {
    animation-name: walk-ear-right;
  }
  .energetic .tail {
    animation-name: gait-tail;
  }
  .energetic .tongue {
    animation-name: gait-pant;
  }
  .boosted .dog {
    animation-name: run-body;
  }
  .boosted .face {
    animation-name: run-face;
  }
  .boosted .ear-l {
    animation-name: run-ear-left;
  }
  .boosted .ear-r {
    animation-name: run-ear-right;
  }
  .boosted .paw-l,
  .boosted .paw-r {
    animation-name: run-forepaw;
  }
  .boosted .paw-r {
    animation-delay: calc(var(--gait-phase) - var(--stride) * 0.1);
  }
  .boosted .hind-l,
  .boosted .hind-r {
    animation-name: run-hindpaw;
  }
  .boosted .hind-r {
    animation-delay: calc(var(--gait-phase) - var(--stride) * 0.1);
  }
  .boosted .shadow {
    animation-name: run-shadow;
  }
  .speed {
    animation-name: breeze;
  }
  [data-motion='zoomies'] {
    --hop: -5px;
  }
  [data-motion='maximum_zoomies'] {
    --hop: -7px;
  }
  [data-motion='throttled'] .face,
  [data-motion='idle'] .face {
    animation-name: patient-tilt;
  }
  [data-motion='throttled'] .ear-r {
    animation-name: ear-twitch;
  }
  [data-motion='provisioning'] .face {
    animation-name: sniff;
  }
  [data-motion='registering'] .face {
    animation-name: patient-tilt;
  }
  [data-motion='registering'] .paw-r {
    animation-name: wave;
  }
  [data-motion='draining'] .face {
    animation-name: settle;
  }
  [data-motion='draining'] .tail {
    animation-name: none;
  }
  [data-motion='failed'] .dog,
  [data-motion='failed'] .tail,
  [data-motion='removed'] .dog,
  [data-motion='removed'] .tail,
  [data-motion='unknown'] .dog,
  [data-motion='unknown'] .tail {
    animation-name: none;
  }
  [data-motion='failed'] .face {
    transform: rotate(-8deg);
  }
  [data-motion='removed'] .face {
    transform: translateY(4px) rotate(5deg);
  }
  /* Small alternating weight shifts, with a long ground contact and a
     short paw recovery. The head counters the roll; ears lag the head. */
  @keyframes walk-body {
    0%,
    100% {
      transform: translate(-1.5px, 0) rotate(-1.2deg);
    }
    25% {
      transform: translate(0, -1px) rotate(0deg);
    }
    50% {
      transform: translate(1.5px, 0) rotate(1.2deg);
    }
    75% {
      transform: translate(0, -1px) rotate(0deg);
    }
  }
  @keyframes walk-face {
    0%,
    100% {
      transform: rotate(1deg);
    }
    50% {
      transform: rotate(-1deg);
    }
  }
  @keyframes walk-step {
    0%,
    100% {
      transform: translateY(0) rotate(0deg);
    }
    60% {
      transform: translateY(1px) rotate(2deg);
    }
    80% {
      transform: translateY(-5px) rotate(-3deg);
    }
  }
  @keyframes walk-ear-left {
    0%,
    100% {
      transform: rotate(1deg);
    }
    30% {
      transform: rotate(-3deg);
    }
    80% {
      transform: rotate(3deg);
    }
  }
  @keyframes walk-ear-right {
    0%,
    100% {
      transform: rotate(-1deg);
    }
    30% {
      transform: rotate(-3deg);
    }
    80% {
      transform: rotate(3deg);
    }
  }
  /* Hind push-off, a low airborne arc, forepaw landing and gathering.
     The paws lead the landing and the ears settle just after the torso. */
  @keyframes run-body {
    0%,
    100% {
      transform: translateY(1px) rotate(-0.8deg);
    }
    24% {
      transform: translateY(-2px) rotate(0deg);
    }
    44% {
      transform: translateY(var(--hop)) rotate(0.8deg);
    }
    68% {
      transform: translateY(1px) rotate(0deg);
    }
    82% {
      transform: translateY(2px) rotate(-0.5deg);
    }
  }
  @keyframes run-face {
    0%,
    100% {
      transform: translateY(0) rotate(0.8deg);
    }
    44% {
      transform: translateY(1px) rotate(-0.8deg);
    }
    72% {
      transform: translateY(2px) rotate(0deg);
    }
  }
  @keyframes run-forepaw {
    0%,
    100% {
      transform: translateY(-2px) rotate(-2deg);
    }
    32% {
      transform: translateY(-7px) rotate(-5deg);
    }
    60% {
      transform: translateY(2px) rotate(2deg);
    }
    78% {
      transform: translateY(0) rotate(0deg);
    }
  }
  @keyframes run-hindpaw {
    0%,
    100% {
      transform: translateY(1px) rotate(0deg);
    }
    26% {
      transform: translateY(-3px) rotate(3deg);
    }
    52% {
      transform: translateY(-6px) rotate(-4deg);
    }
    82% {
      transform: translateY(0) rotate(0deg);
    }
  }
  @keyframes run-ear-left {
    0%,
    100% {
      transform: rotate(-2deg);
    }
    48% {
      transform: rotate(-10deg);
    }
    76% {
      transform: rotate(3deg);
    }
  }
  @keyframes run-ear-right {
    0%,
    100% {
      transform: rotate(2deg);
    }
    48% {
      transform: rotate(10deg);
    }
    76% {
      transform: rotate(-3deg);
    }
  }
  @keyframes gait-tail {
    0%,
    100% {
      transform: rotate(-5deg);
    }
    50% {
      transform: rotate(8deg);
    }
  }
  @keyframes gait-pant {
    0%,
    100% {
      transform: scaleY(0.98);
    }
    55% {
      transform: scaleY(1.04);
    }
  }
  @keyframes run-shadow {
    0%,
    100% {
      transform: scaleX(1);
      opacity: 1;
    }
    44% {
      transform: scaleX(0.9);
      opacity: 0.8;
    }
    75% {
      transform: scaleX(1.02);
      opacity: 1;
    }
  }
  @keyframes tail-wag {
    0%,
    4%,
    12%,
    20%,
    28%,
    100% {
      transform: rotate(0);
    }
    8%,
    24% {
      transform: rotate(-12deg);
    }
    16% {
      transform: rotate(17deg);
    }
  }
  @keyframes blink {
    0%,
    39%,
    43%,
    100% {
      transform: scaleY(1);
    }
    41% {
      transform: scaleY(0.08);
    }
  }
  @keyframes pant {
    0%,
    4%,
    12%,
    20%,
    100% {
      transform: scaleY(0.9);
    }
    8%,
    16% {
      transform: scaleY(1.12);
    }
  }
  @keyframes breeze {
    0%,
    100% {
      opacity: 0.3;
      transform: translateX(0);
    }
    44% {
      opacity: 0.65;
      transform: translateX(-3px);
    }
  }
  @keyframes breathe {
    0%,
    100% {
      transform: scale(1);
    }
    50% {
      transform: scale(1.01, 1.02);
    }
  }
  @keyframes patient-tilt {
    0%,
    12%,
    65%,
    100% {
      transform: rotate(0);
    }
    24%,
    46% {
      transform: rotate(9deg);
    }
  }
  @keyframes ear-twitch {
    0%,
    65%,
    76%,
    100% {
      transform: rotate(0);
    }
    68%,
    73% {
      transform: rotate(-9deg);
    }
    70% {
      transform: rotate(3deg);
    }
  }
  @keyframes sniff {
    0%,
    8%,
    30%,
    100% {
      transform: translateY(0) rotate(0);
    }
    14%,
    23% {
      transform: translateY(-3px) rotate(-8deg);
    }
    18% {
      transform: translateY(-1px) rotate(-5deg);
    }
  }
  @keyframes wave {
    0%,
    10%,
    35%,
    100% {
      transform: translateY(0);
    }
    17%,
    29% {
      transform: translateY(-14px) rotate(12deg);
    }
    23% {
      transform: translateY(-14px) rotate(-6deg);
    }
  }
  @keyframes settle {
    0%,
    10%,
    50%,
    100% {
      transform: translateY(0);
    }
    25%,
    35% {
      transform: translateY(3px) rotate(4deg);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .hind-l,
    .hind-r,
    .shadow,
    .dog,
    .face,
    .ear-l,
    .ear-r,
    .paw-l,
    .paw-r,
    .tail,
    .eyes,
    .speed,
    .tongue {
      animation: none !important;
    }
    .speed {
      opacity: 0.55;
    }
  }
</style>
