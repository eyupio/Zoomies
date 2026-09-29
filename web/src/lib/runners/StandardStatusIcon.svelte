<!-- The Standard spaniel: a solid black English cocker. The white muzzle and
     toe tips are a stylisation that keeps the face and feet legible at 32px,
     not breed marks, and the chest keeps only a fleck, so the dog never
     reads as a penguin's white front. A domed skull with a pronounced stop,
     a square muzzle, a feathered ear at eye level, and a near-square, waisted
     body feathered in the ear's lobes, under an adult head. Four silhouettes
     (running, standing, sitting, lying) carry the states, since at 32px the
     silhouette is what survives. Every part is solid-filled, so animated
     limbs never show through. -->
<script lang="ts">
  import { standardMotion, type StandardMotionState } from './standard-motion';
  let { state, seed = 'zoomies' }: { state: string; seed?: string } = $props();
  const motion = $derived(standardMotion(state, seed));
  // Each icon's own outline filter: an id shared across a page would leave
  // every dog pointing at whichever icon rendered first.
  const uid = $props.id();
  const outline = `standard-outline-${uid}`;

  // Under the chest, three lobes of feathering in the ear's language, big
  // enough to survive at 44px as edge texture, ending before the tuck-up so
  // the waist still shows behind them; then the flank, rump and back.
  const BELLY =
    'C80 71.5 77 72.5 75.5 70.5C74 73 70.5 73 69.5 69.5C68 70 66.5 69 66 67C64.5 64 62 61 59 61C54 61 50 66 45 68C38 69.5 32 65 32 59C32 55.5 33 52 36 51Z';
  // The head is drawn once, in its own coordinates (skull centre at 0,0,
  // facing right), and placed by each pose, so every state has the same face.
  // A leg is two segments hung from their own pivots. Both ends of a joint
  // are round and centred on its pivot, so a folding leg turns like a ball
  // joint and never shows a corner.
  const P = {
    head: 'M-13 9C-18-2-12-17.5 1-17.5C9-17.5 13.5-14.5 14.5-10.5C15.5-6.5 15.5-3 16.5-1C17.5.2 19 .5 21 .5H28C32 .5 34 3 33.5 6C33 9 31 10.5 29 11C29.5 13.5 27 16 23 16L12 16.5C5 17-8 16-13 9Z',
    // White from just behind the stop, with a near-vertical back edge, so the
    // muzzle reads as a square block under a domed skull and not as a long
    // white wedge down the face -- a collie's or a springer's.
    blaze:
      'M14.2-8.5C15-5.5 15.6-2.8 16.5-1C17.5.2 19 .5 21 .5H28C32 .5 34 3 33.5 6C33 9 31 10.5 29 11C29.5 13.5 27 16 23 16L14 16.5C13.6 10 13.6-2 14.2-8.5Z',
    nose: 'M27 0C31-1.3 34.8 1 34.2 5C33.8 8 30.5 8.7 28.4 7.1C26.6 5.7 26 1.5 27 0Z',
    // Hung at eye level from a narrow root, with black skull above and
    // behind it, and widening below the jaw into lobed locks down its whole
    // back edge: a cocker's feathered ear. A smooth oval reads as a
    // flipper at 44px.
    ear: 'M-7-3C-5.5-5.5-1.5-6 0-3.5C1.5 1 1 6 1.5 11C2 16 4 20 3.5 25C3.5 28 1.5 30.5-1 30C-2.5 33-6.5 33-7.5 30C-10.5 31-13 28.5-12 25.5C-14.5 23.5-14 19.5-11.5 18C-13.5 15-12.5 11-10 9.5C-10.5 5-9.5 0-7-3Z',
    // The ear's front edge only, where it lies against the cheek: the one
    // place the ear needs a seam. Round the whole ear it becomes a sticker.
    earEdge: 'M0-3.5C1.5 1 1 6 1.5 11C2 16 4 20 3.5 24',
    waves: 'M-7.5 7C-8.5 13-6.5 18-8.5 25M-3 10C-3.5 16-1.5 21-3 27',
    // A hind leg is a drumstick -- a thigh as wide as the rump, tapering to
    // the hock, with feathered trousers down its back edge -- and a hind
    // foot; standing, the two meet at an angle, so the hock points back and
    // the hind legs never look like the front.
    hind: 'M-7.5-5C-9.5 2-9 7-7.5 10.5C-10.5 12.5-10 17-6.5 17C-8 20-6 23.5-3 24A3 3 0 0 0 3 24C5 17 8.5 7 7.5-5C4.5-9.5-4.5-9.5-7.5-5Z',
    front: 'M-4-6C-4 6-3.5 12-3 17A3 3 0 0 0 3 17C3.8 12 4.5 4 4-6Z',
    tail: 'M37 50C31 47 25 44 16.5 43.5C18.5 46 20.5 47.5 23 48.5C21 50.5 23.5 53 27 52.5C27 55 30.5 56.5 33.5 55L37 55.5Z',
    // Near square, as a cocker is: rump to forechest about the height at the
    // withers. A level topline, a deep chest and a belly that tucks up into
    // the hindquarters -- a sporting dog with a waist, not a barrel or a
    // setter's long back.
    body: `M36 51C46 49 56 49 64 46C67 40 70 33 73 26L86 36C89 42 92 48 92 54C91 61 87 66.5 81 68.5${BELLY}`,
    // Nose to the ground, the neck reaches forward and down into the head
    // instead of standing up behind it, and stops where the head covers it.
    graze: `M36 51C46 49 56 49 64 46C73 43 81 43 88 47L91 60C88 65 85 68 81 68.5${BELLY}`,
    star: 'M88.5 47C91 48 92 51 91.3 55C89.7 56.5 88.5 58.5 87.7 60.5C86.5 57.5 85 55 86 52C85.5 50 86.5 47.5 88.5 47Z',
  };
  // Every lower leg, from a round joint at y=j to the ground at y=g: a
  // pastern a little narrower than the leg and a round cat foot, as a
  // cocker's are, and a fringe of the ear's lobes down the back from y=f
  // where the leg is feathered. One drawing, so the four legs cannot drift
  // apart.
  function lower(j: number, g: number, f?: number) {
    const fringe =
      f === undefined
        ? ''
        : `V${f}C-4 ${f + 3}-6.5 ${f + 4}-6 ${f + 7}C-7 ${f + 9.5}-5.5 ${f + 12}-2.4 ${f + 11.5}`;
    return `M2.8 ${j}A2.8 2.8 0 0 0-2.8 ${j}${fringe}C-2.2 ${g - 9}-2.2 ${g - 6}-2.2 ${g - 4}C-2.2 ${g - 1.5}-1 ${g} 1 ${g}H5C7.5 ${g} 8.8 ${g - 1} 8.8 ${g - 2.8}C8.8 ${g - 5} 6 ${g - 6.5} 2 ${g - 6.5}C2 ${g - 10} 2.8 ${j + 4} 2.8 ${j}Z`;
  }
  // White only in a crescent at the toe tips: a white foot with a black edge
  // round it reads as a boot.
  const toes = (g: number) =>
    `M5 ${g - 6.2}C7.5 ${g - 5.6} 8.8 ${g - 4.4} 8.8 ${g - 2.8}C8.8 ${g - 1} 7.5 ${g} 5 ${g}H3.8C5.6 ${g - 1.3} 6.2 ${g - 4} 5 ${g - 6.2}Z`;
  // The hock is shorter than the forearm below the elbow; only the foreleg
  // is feathered below its joint -- the hind leg's feathering is on the thigh.
  const SHIN = { hind: lower(24, 41), front: lower(17, 39, 17) };
  const TOES = { hind: toes(41), front: toes(39) };
  // Sitting and lying are one drawing with different parts. Seated, the dog
  // is a slim triangle, not an egg: an upright neck over a shoulder, a chest
  // standing proud of a straight foreleg, a gap under the belly, and the
  // haunch its own mass at the rear. Lying, the hip is the high point of the
  // back half and the rump is round, so the rear is a dog's and not a seal's.
  const REST = {
    sit: {
      tail: 'M35 97C29 95.5 22 93.5 14 93C16 95.5 18 97 20.5 98C18.5 100.5 21 103 24.5 102.5C24.5 105 28 106 31 104.5L35 104Z',
      // The back dips in behind the skull at the nape and rises over the
      // withers before it runs down to the haunch -- an upright neck, not a
      // cone with a head on top. The neck stops low enough that a hung head
      // still covers it. The chest fringe hangs in two lobes from the
      // forechest and stops at the elbow, so the foreleg stays a column.
      body: 'M63 29C60.5 30.5 58.2 32.5 58 35.5C58 39 55 42 53 46C49 59 40.5 75 34 90C32 96 33 102 37 104L52 94C56 91 60 89 64 88C68 87 70.5 85 72 82C74.5 80 76.5 78.8 78 78.5C79 80.5 81.5 81 82 77C84.5 77 86 73.5 84.5 69C85.5 60 81.5 49 77 41C75 37 70 32 63 29Z',
      haunch:
        'M31 104C27 92 32 80 42 79C50 78 57 84 60 92C62 99 60 106 55 110L52 112.5H36C33 111 31.5 108 31 104Z',
      crease: 'M39 80.5C47 80 54 85 58 92C60.5 96.5 60.5 101 58 104',
      hindPaw:
        'M46 106.5C51 105.5 55 105.5 58 106.5C61 107.5 62.3 109.5 62.3 111C62.3 112.3 61 113 59 113H46Z',
      hindToes:
        'M58.5 106.7C61 107.7 62.3 109.5 62.3 111C62.3 112.3 61 113 59 113H57.5C59 111.8 59.6 109 58.5 106.7Z',
      // A straight, feathered foreleg set forward under the chest.
      legAt: 'translate(74.5 0)',
      leg: lower(74, 113, 86),
      paw: toes(113),
      // A fleck under the throat, inboard of the chest's edge -- the one
      // white a solid cocker is allowed. Any bigger and it is a white front.
      star: 'M81.5 56C83 56.5 83.6 59 83 61.5C82.6 62.6 81.8 63.4 81.2 64C80 62.2 79.2 60 79.5 58C79.8 56.8 80.5 56 81.5 56Z',
    },
    lie: {
      tail: 'M19 100C14 98.5 9 96.5 3.5 93.5C5 96.5 6.5 98.5 8.5 100C6.5 102.5 8.5 105.5 12 105C13 107.5 16 108 19 106.5Z',
      body: 'M20 112C14 110 13 102 17 96C21 89 29 86 36 86.5C44 87 50 90 56 89C62 88 66 81 70 75L72 71.5L84 76C88 84 88 94 86 101L84 112Z',
      haunch:
        'M23 104C23 96 30 91.5 38 91.5C46 91.5 52 96.5 52 103C52 109 48 112.5 42 112.5H29C25 112.5 23 110 23 104Z',
      crease: 'M29 95C35 91 44 91.5 49 97C51 99.5 51.8 102 51.5 104.5',
      // The hind foot folded under, hock at the back of the haunch and the
      // foot run forward along the ground under the belly.
      hindPaw: 'M24 107C34 106 48 106 57 106.5C61 107 62 110.5 60 112.3C59 113 57.5 113 56 113H24Z',
      hindToes:
        'M53 106.3C54.8 106.3 56 106.4 57 106.5C61 107 62 110.5 60 112.3C59 113 57.5 113 56 113H53Z',
      legAt: '',
      leg: 'M64 101C72 99.5 82 100.5 92 102C97.5 101.5 102 102.5 104 106.5C105 110.5 102 113 98 113H66C62 113 60 110 60 107C60 104 61.5 101.5 64 101Z',
      paw: 'M99.5 102.4C102 103.3 103.6 104.7 104 106.5C104.8 110 102.6 112.8 99.5 113H98C100.5 111 101 105 99.5 102.4Z',
      star: 'M84 84C87 85 88 89 87 93C85.5 92.5 84 91 83 89.5C82 87.5 82.5 85 84 84Z',
    },
  };
  const WHOOSH =
    'M9 61L21 59.5A1.5 1.5 0 0 1 21 62.5ZM13 65.5L21 64.2A1.3 1.3 0 0 1 21 66.8ZM16 70L21 68.9A1.1 1.1 0 0 1 21 71.1Z';
  const LONGEST = 'M5 57L21 55.4A1.6 1.6 0 0 1 21 58.6Z';
  // Registering's near forepaw, held out: bent at the elbow, the forearm
  // narrowing to the wrist and the paw dropped below it, wider than the
  // pastern and white-toed like every other paw -- offered, not a handle.
  // The standing foreleg is the near one, as in idle; the far one is hidden
  // behind it.
  const OFFER = {
    arm: 'M68 80C72 74 79 70.5 85 68.5C89 66.5 93 67 95 70C97 73 96.8 77.5 94.3 80.3C91.8 83 87.8 82.8 86.4 80C85.8 78.8 85.8 77.6 85.6 76.4C80.5 78 76.5 81.5 74.5 85C72.5 88 67 86 68 80Z',
    edge: 'M68.5 79C72 74 79 70.5 84.5 68.7',
    toes: 'M86.8 80.6C89.8 80.2 93.3 78.6 96 76.2C95.8 77.7 95.2 79.1 94.3 80.3C91.8 83 88 82.8 86.8 80.6Z',
  };
  // Where each state carries its head, when it is not idle's upright
  // carriage; a running dog always carries it the same way.
  const HEAD: Partial<Record<StandardMotionState, string>> = {
    provisioning: 'translate(92 58) rotate(34)',
    throttled: 'translate(84 28) rotate(-4)',
    // Hung from the poll and carried forward, so the muzzle clears the chest
    // and the forechest still stands proud below it. Tucked into the neck
    // instead, the dog becomes a black tombstone. The chest star is left
    // off, so the muzzle is never the top of a white front.
    failed: 'translate(72 34) rotate(18)',
    // Unknown looks back over its shoulder, which idle never does, so the
    // two share no frame even at 32px, where a muzzle's direction is the
    // one detail left. The skull stays over the neck and the muzzle lifts
    // over the back, so it reads as a glance back and not a dog facing left.
    unknown: 'translate(60 23) scale(-1 1) rotate(-12)',
    draining: 'translate(74 68) rotate(8)',
    // High enough that the fallen ear rests on the ground, not through it.
    removed: 'translate(72 84) rotate(6)',
  };
  const headAt = $derived(
    motion.pose === 'run'
      ? 'translate(85 25) rotate(-6)'
      : (HEAD[motion.state] ?? 'translate(69 25)'),
  );
  // Held under life size, on a longer neck: a full-size head on a slim body
  // reads as a puppy, not an adult sporting dog.
  const headScale = 0.88;
  // Each leg, far pair first so the near pair paints over them: kind, hip,
  // where in the stride its foot falls (hind pair, then front pair), whether
  // it is the far one, and its two joint angles when nothing animates it. A
  // running dog rests mid-stride, one leg of each pair reaching and the
  // other gathered under it and folding, so with motion reduced it still
  // reads as running. Both pairs at full stretch made a spread-eagled X with
  // a setter's reach; all four under the body made a dog standing still.
  type Leg = ['hind' | 'front', number, number, number, boolean, number, number];
  const RUN: Leg[] = [
    ['hind', 52, 58, 0, true, 14, -34],
    ['front', 72, 61, 0.42, true, -8, 34],
    ['hind', 46, 60, 0.08, false, 36, -12],
    ['front', 78, 63, 0.52, false, -38, 0],
  ];
  // Four evenly separated footfalls, with each side's hind foot followed
  // by its forefoot. Opposite legs are half a cycle apart, not the paired
  // hind/fore beats of a gallop. Smaller joint angles keep the paws low.
  const WALK: Leg[] = [
    ['hind', 52, 57, 0.5, true, 22, -36],
    ['front', 72, 61, 0.25, true, 12, 0],
    ['hind', 46, 59, 0, false, 34, -42],
    ['front', 78, 63, 0.75, false, -16, 6],
  ];
  const STAND: Leg[] = [
    ['hind', 52, 57, 0, true, 26, -40],
    ['front', 72, 61, 0, true, -3, 4],
    ['hind', 46, 59, 0, false, 30, -45],
    ['front', 78, 63, 0, false, -6, 5],
  ];
  // Throttled stops mid-step with a forepaw lifted -- paused, not finished.
  const POINT: Leg = ['front', 78, 63, 0, false, -56, 72];
  const legs = $derived(
    motion.walking
      ? WALK
      : motion.pose === 'run'
        ? RUN
        : motion.state === 'throttled'
          ? [...STAND.slice(0, 3), POINT]
          : STAND,
  );
  // Centres each silhouette in the box; the spin turns round the box centre.
  const shift = $derived(
    motion.pose === 'run' ? 'translate(-4 0)' : motion.pose === 'stand' ? 'translate(-6 11)' : '',
  );
</script>

{#snippet head()}
  <g class="head">
    <path class="coat" d={P.head} />
    <path class="blaze" d={P.blaze} />
    <path class="ink" d={P.nose} />
    {#if motion.running}
      <!-- Carried to the jaw's front edge: a sliver of white left between the
           mouth and the chin reads as a fang, a snarl rather than a pant. -->
      <path
        class="ink"
        d="M30 11.5Q23 15.5 16 11.5Q20 20.5 27.5 16.5C29.5 15.5 30.3 13.5 30 11.5Z"
      />
    {:else}
      <path
        class="mouth"
        d={motion.state === 'failed' ? 'M28.5 14Q23 11 17 13.5' : 'M28.5 12Q22.5 15 16.5 11.5'}
      />
    {/if}
    {#if motion.state === 'removed'}
      <path class="lid" d="M1-4Q5-1 9-4" />
    {:else}
      <g class="eye">
        <ellipse class="blaze" cx="5" cy="-4.5" rx="4" ry="4.6" />
        <circle class="ink pupil" cx="6.3" cy="-4" r="3" />
        <circle class="blaze pupil" cx="7.2" cy="-5.3" r="1.1" />
        {#if motion.state === 'draining'}
          <path class="coat" d="M.6-4.2A4.4 4.4 0 0 1 9.4-4.2Z" />
        {/if}
      </g>
    {/if}
    <!-- Drawn steeper than it looks: the hung head turns it 18 degrees, and
         it must still rise towards the nose to read as worry, not a scowl. -->
    {#if motion.state === 'failed'}<path class="lid" d="M-.5-7.5Q3.5-9.5 6.5-13" />{/if}
    <g class="ear">
      <path class="rim" d={P.earEdge} />
      <path class="ear-coat" d={P.ear} />
      <path class="wave" d={P.waves} />
    </g>
  </g>
{/snippet}

{#snippet leg([kind, x, y, beat, far, thigh, shin]: Leg)}
  {@const fill = far ? 'far' : 'coat'}
  <g transform="translate({x} {y})">
    <g
      class="leg {kind}"
      style:--delay={`${motion.phase - motion.stride * beat}s`}
      style:transform={`rotate(${thigh}deg)`}
    >
      <g class="shin" style:transform={`rotate(${shin}deg)`}>
        <path class={fill} d={SHIN[kind]} />
        {#if !far}<path class="blaze" d={TOES[kind]} />{/if}
      </g>
      <path class={fill} d={P[kind]} />
    </g>
  </g>
{/snippet}

<svg
  class="standard-mark"
  class:running={motion.running && !motion.walking}
  class:walking={motion.walking}
  class:spinning={motion.spinning}
  class:still={motion.still}
  data-style="standard"
  data-motion={motion.state}
  data-pose={motion.pose}
  viewBox="0 0 120 120"
  aria-hidden="true"
  focusable="false"
  style:--stride={`${motion.stride}s`}
  style:--phase={`${motion.phase}s`}
  style:--spin={`${motion.spin}s`}
  style:--spin-phase={`${motion.spinPhase}s`}
  style:--gesture={`${motion.gesture}s`}
  style:--gesture-phase={`${motion.gesturePhase}s`}
  style:--blink={`${motion.blink}s`}
  style:--blink-phase={`${motion.blinkPhase}s`}
>
  <!-- One outline round the silhouette alone, grown from the drawing's own
       shape: no seam where black parts overlap, and nothing drawn twice. The
       browser rounds the dilation to whole device pixels, so at 44px and
       32px it is a one-pixel edge whatever its radius; one dilation is
       therefore enough, and a galloping dog re-rasterises it every frame,
       so it is kept to the cheapest filter that draws it. -->
  <filter id={outline}>
    <feMorphology in="SourceAlpha" operator="dilate" radius="1.5" result="grown" />
    <feFlood class="line" />
    <feComposite in2="grown" operator="in" />
    <feMerge><feMergeNode /><feMergeNode in="SourceGraphic" /></feMerge>
  </filter>
  {#if motion.spinning}
    <!-- Graded dashes level with the thighs, below the tail at every point
         of its swing, so nothing uncovers them stride by stride. Maximum
         zoomies trails a fourth, longest one, so the two still tell apart
         with motion reduced, when neither spins. -->
    <path class="whoosh" d={motion.state === 'maximum_zoomies' ? WHOOSH + LONGEST : WHOOSH} />
  {/if}
  {#if motion.state === 'provisioning'}
    {#each [0, 1, 2] as i (i)}
      <circle class="scent" cx={103 + i * 5} cy={113 - i * 4} r="3.2" style:--i={i} />
    {/each}
  {/if}
  <g class="spin">
    {#if motion.spinning}
      <!-- A comet on the path the skull has just swept, fat where it
           touches the back of the head and tapering over the shoulders, so it
           is still two pixels wide at 44px and reads as the head's own blur. -->
      <path
        class="swish"
        d="M69 8.8A52 52 0 0 0 39.5 16A47 47 0 0 1 67.8 15.7A3.5 3.5 0 0 0 69 8.8Z"
      />
    {/if}
    <g transform={shift}>
      <g class="dog" filter="url(#{outline})">
        {#if motion.pose === 'run' || motion.pose === 'stand'}
          {#each legs as l, i (i)}{@render leg(l)}{/each}
          <path class="tail coat" d={P.tail} />
          <path class="coat" d={motion.state === 'provisioning' ? P.graze : P.body} />
          <path class="blaze" d={P.star} />
        {:else}
          {@const r = REST[motion.pose]}
          <path class="tail coat" d={r.tail} />
          <path class="coat" d={r.body} />
          <!-- Failed hangs its head over the chest, where a star would join
               the muzzle into one white front. -->
          {#if motion.state !== 'failed'}<path class="blaze" d={r.star} />{/if}
          <path class="coat" d={r.hindPaw} />
          <path class="blaze" d={r.hindToes} />
          <g class="haunch">
            <path class="coat" d={r.haunch} />
            <path class="wave" d={r.crease} />
          </g>
          <g transform={r.legAt}>
            <path class="coat" d={r.leg} />
            <path class="blaze" d={r.paw} />
          </g>
          {#if motion.state === 'registering'}
            <g class="offer">
              <path class="rim" d={OFFER.edge} />
              <path class="coat" d={OFFER.arm} />
              <path class="blaze" d={OFFER.toes} />
            </g>
          {/if}
        {/if}
        <g transform="{headAt} scale({headScale})">{@render head()}</g>
      </g>
    </g>
  </g>
</svg>

<style>
  .standard-mark {
    width: var(--z-avatar-size);
    height: var(--z-avatar-size);
    flex: none;
    overflow: hidden;
  }
  .standard-mark :global(*) {
    transform-box: view-box;
  }
  .coat {
    fill: var(--z-standard-coat);
  }
  .ear-coat {
    fill: var(--z-standard-ear);
  }
  .blaze {
    fill: var(--z-standard-blaze);
  }
  .far {
    fill: var(--z-standard-far);
  }
  .ink {
    fill: var(--z-standard-ink);
  }
  .line {
    flood-color: var(--z-standard-line);
  }
  /* The seams that have to show: the ear's front edge against the cheek and
     an offered forearm across the chest. Drawn under the part, so only the
     half outside it shows, as a rim of light. */
  .rim {
    fill: none;
    stroke: var(--z-standard-rim);
    stroke-width: 3;
  }
  /* Features are drawn at least as heavy as the outline, or the face is
     the first thing lost at 32px. */
  .mouth,
  .lid {
    fill: none;
    stroke-width: 2.8;
    stroke-linecap: round;
  }
  .mouth {
    stroke: var(--z-standard-ink);
  }
  .lid {
    stroke: var(--z-standard-blaze);
  }
  /* The ear's locks and the haunch's crease: finer than a feature, since
     they are modelling rather than expression and should fade before the
     face does. The locks are drawn in the coat's colour on the lighter ear. */
  .wave {
    fill: none;
    stroke: var(--z-standard-rim);
    stroke-width: 1.8;
    stroke-linecap: round;
  }
  .ear .wave {
    stroke: var(--z-standard-coat);
  }
  .swish,
  .whoosh,
  .scent {
    fill: var(--z-standard-cue);
  }

  /* Pivots, each in its own part's coordinates. */
  .head {
    transform-origin: -6px 10px;
  }
  .ear {
    transform-origin: -3px -5px;
  }
  .eye {
    transform-origin: 5px -4.5px;
    animation: blink var(--blink) var(--blink-phase) infinite;
  }
  .leg {
    transform-origin: 0 0;
  }
  .shin {
    transform-origin: 0 17px;
  }
  .hind .shin {
    transform-origin: 0 24px;
  }
  .tail {
    transform-origin: 37px 52px;
  }
  [data-pose='sit'] .tail {
    transform-origin: 35px 100px;
  }
  .offer {
    transform-origin: 72px 81px;
  }

  /* Running: the body pitches over each stride and the legs work a rotary
     gallop, each foot a beat behind the last. */
  .running .dog {
    transform-origin: 60px 64px;
    animation: gallop var(--stride) var(--phase) ease-in-out infinite;
  }
  .running .head {
    animation: gallop-head var(--stride) var(--phase) ease-in-out infinite;
  }
  .running .tail {
    animation: tail-stream var(--stride) var(--phase) ease-in-out infinite;
  }
  .running .ear {
    animation: ear-stream var(--stride) var(--phase) ease-in-out infinite;
  }
  .running .hind {
    animation: hind-thigh var(--stride) var(--delay) linear infinite;
  }
  .running .front {
    animation: front-thigh var(--stride) var(--delay) linear infinite;
  }
  .running .hind .shin {
    animation: hind-shin var(--stride) var(--delay) linear infinite;
  }
  .running .front .shin {
    animation: front-shin var(--stride) var(--delay) linear infinite;
  }
  /* An unhurried walk: a long planted sweep, then a short, low paw
     recovery. Everything shares the stride clock so weight, head and ears
     stay coordinated even when each runner starts at a different phase. */
  .walking .dog {
    transform-origin: 60px 64px;
    animation: walk-body var(--stride) var(--phase) ease-in-out infinite;
  }
  .walking .head {
    animation: walk-head var(--stride) var(--phase) ease-in-out infinite;
  }
  .walking .ear {
    animation: walk-ear var(--stride) var(--phase) ease-in-out infinite;
  }
  .walking .tail {
    animation: walk-tail var(--stride) var(--phase) ease-in-out infinite;
  }
  .walking .hind {
    animation: walk-hind var(--stride) var(--delay) linear infinite;
  }
  .walking .front {
    animation: walk-front var(--stride) var(--delay) linear infinite;
  }
  .walking .hind .shin {
    animation: walk-hock var(--stride) var(--delay) linear infinite;
  }
  .walking .front .shin {
    animation: walk-elbow var(--stride) var(--delay) linear infinite;
  }
  @keyframes walk-body {
    0%,
    100% {
      transform: translateY(0.5px) rotate(-1.2deg);
    }
    25% {
      transform: translateY(-0.6px) rotate(0deg);
    }
    50% {
      transform: translateY(0.5px) rotate(1.2deg);
    }
    75% {
      transform: translateY(-0.6px) rotate(0deg);
    }
  }
  @keyframes walk-head {
    0%,
    100% {
      transform: rotate(1.8deg);
    }
    25%,
    75% {
      transform: rotate(0deg);
    }
    50% {
      transform: rotate(-1.8deg);
    }
  }
  @keyframes walk-ear {
    0%,
    100% {
      transform: rotate(1deg);
    }
    30% {
      transform: rotate(6deg);
    }
    55% {
      transform: rotate(1deg);
    }
    80% {
      transform: rotate(5deg);
    }
  }
  @keyframes walk-tail {
    0%,
    100% {
      transform: rotate(-3deg);
    }
    50% {
      transform: rotate(5deg);
    }
  }
  @keyframes walk-front {
    0%,
    100% {
      transform: rotate(-20deg);
    }
    65% {
      transform: rotate(18deg);
    }
    80% {
      transform: rotate(4deg);
    }
  }
  @keyframes walk-elbow {
    0%,
    65%,
    100% {
      transform: rotate(0deg);
    }
    80% {
      transform: rotate(30deg);
    }
    92% {
      transform: rotate(12deg);
    }
  }
  @keyframes walk-hind {
    0%,
    100% {
      transform: rotate(8deg);
    }
    65% {
      transform: rotate(42deg);
    }
    82% {
      transform: rotate(26deg);
    }
  }
  @keyframes walk-hock {
    0%,
    100% {
      transform: rotate(-24deg);
    }
    65% {
      transform: rotate(-36deg);
    }
    82% {
      transform: rotate(-60deg);
    }
  }
  /* The swish is shown and hidden in a single step rather than faded, so a
     moving part is never half transparent. Hidden is also its resting
     state, so with motion reduced there is no arc beside a dog that is not
     turning. It turns with the dog, inside the spin, so it needs no
     rotation of its own. */
  .swish {
    visibility: hidden;
  }
  .spinning .spin {
    transform-origin: 60px 60px;
    animation: zoomies-spin var(--spin) var(--spin-phase) infinite;
  }
  .spinning .swish {
    animation: spin-show var(--spin) var(--spin-phase) step-end infinite;
  }
  /* Speed lines trail a dog running straight; they step out while it turns. */
  .spinning .whoosh {
    animation: spin-hide var(--spin) var(--spin-phase) step-end infinite;
  }

  /* Waiting: long holds and one small gesture at a time, each in its own
     window of the shared period, so a page of idle runners reads as mostly
     still rather than twitching in bursts. */
  [data-motion='idle'] .head,
  [data-motion='registering'] .head {
    animation: look var(--gesture) var(--gesture-phase) ease-in-out infinite;
  }
  [data-motion='idle'] .ear,
  [data-motion='throttled'] .ear {
    animation: twitch var(--gesture) var(--gesture-phase) ease-in-out infinite;
  }
  [data-motion='idle'] .tail,
  [data-motion='throttled'] .tail {
    animation: patient-wag var(--gesture) var(--gesture-phase) ease-in-out infinite;
  }
  [data-motion='provisioning'] .head {
    animation: sniff var(--gesture) var(--gesture-phase) ease-in-out infinite;
  }
  /* Each dot a quarter-second behind the last, and never a positive delay,
     so no dot sits at full size before its animation begins. */
  .scent {
    transform-origin: calc(103px + var(--i) * 5px) calc(113px - var(--i) * 4px);
    animation: scent var(--gesture) calc(var(--gesture-phase) + (var(--i) - 2) * 0.25s) ease-in-out
      infinite;
  }
  /* The paw is drawn held out, so registering is never a second idle dog
     with motion or without it; it shakes once the head has settled. */
  [data-motion='registering'] .offer {
    animation: shake var(--gesture) var(--gesture-phase) ease-in-out infinite;
  }
  [data-motion='draining'] .head {
    animation: settle var(--gesture) var(--gesture-phase) ease-in-out infinite;
  }
  /* Failure hangs its head, tucks its tail forward under the haunch until
     only the feathered tip shows between the hind foot and the foreleg, and
     looks up from under its brow -- a concerned dog, not an angry one. The
     haunch draws in a little, so with the head down the dog is still a slim
     triangle and not a mound. */
  [data-motion='failed'] .ear {
    transform: rotate(-8deg);
  }
  [data-motion='failed'] .tail {
    transform: translate(14px, 9px) scale(-0.75, 0.75);
  }
  /* Raised, so the fan at the back cannot pass for a forepaw once the head
     has turned to face it. */
  [data-motion='unknown'] .tail {
    transform: rotate(22deg);
  }
  [data-motion='failed'] .haunch {
    transform-origin: 34px 112px;
    transform: scale(0.88);
  }
  [data-motion='failed'] .pupil {
    transform: translate(0.6px, -1.4px);
  }
  /* Nose to the ground, the ear hangs just behind the eye, not across it. */
  [data-motion='provisioning'] .ear {
    transform: rotate(-6deg);
  }
  /* Asleep, the ear falls forward over the jaw onto the paws, as a sleeping
     cocker's does, rather than lying along the back like a wing. */
  [data-motion='removed'] .ear {
    transform: rotate(-8deg);
  }
  .standard-mark.still :global(*) {
    animation: none !important;
  }

  /* Hind push-off, suspension, forepaw landing and gathering. Head pitch
     counters the shoulder rotation, while ears settle just after landing.
     The feet, torso and face share one clock instead of bouncing separately. */
  @keyframes gallop {
    0%,
    100% {
      transform: translateY(1px) rotate(-2deg);
    }
    22% {
      transform: translateY(-1px) rotate(-3deg);
    }
    44% {
      transform: translateY(-3px) rotate(0deg);
    }
    66% {
      transform: translateY(0.5px) rotate(3deg);
    }
    82% {
      transform: translateY(1.5px) rotate(1deg);
    }
  }
  @keyframes gallop-head {
    0%,
    100% {
      transform: rotate(1deg);
    }
    22% {
      transform: rotate(2.5deg);
    }
    44% {
      transform: rotate(0deg);
    }
    66% {
      transform: rotate(-3deg);
    }
    82% {
      transform: rotate(-1deg);
    }
  }
  /* Each foot reaches, lands, sweeps back under the body and folds on the
     way forward again -- the fold is what reads as a gallop at 44px. */
  @keyframes front-thigh {
    0%,
    100% {
      transform: rotate(-42deg);
    }
    40% {
      transform: rotate(30deg);
    }
    58% {
      transform: rotate(32deg);
    }
    82% {
      transform: rotate(-28deg);
    }
  }
  @keyframes front-shin {
    0%,
    40%,
    100% {
      transform: rotate(-6deg);
    }
    62% {
      transform: rotate(60deg);
    }
    84% {
      transform: rotate(40deg);
    }
  }
  @keyframes hind-thigh {
    0%,
    100% {
      transform: rotate(44deg);
    }
    18% {
      transform: rotate(46deg);
    }
    62% {
      transform: rotate(-32deg);
    }
  }
  @keyframes hind-shin {
    0%,
    100% {
      transform: rotate(14deg);
    }
    30% {
      transform: rotate(-60deg);
    }
    62% {
      transform: rotate(-10deg);
    }
  }
  @keyframes tail-stream {
    0%,
    100% {
      transform: rotate(-4deg);
    }
    28% {
      transform: rotate(-10deg);
    }
    70% {
      transform: rotate(2deg);
    }
  }
  @keyframes ear-stream {
    0%,
    100% {
      transform: rotate(12deg);
    }
    36% {
      transform: rotate(26deg);
    }
    72% {
      transform: rotate(8deg);
    }
    88% {
      transform: rotate(14deg);
    }
  }
  @keyframes blink {
    0%,
    43%,
    47%,
    100% {
      transform: scaleY(1);
    }
    45% {
      transform: scaleY(0.1);
    }
  }
  /* A brief whole-dog spin between long running stretches. Shrunk while it
     turns so the nose and feet never leave the box. */
  @keyframes zoomies-spin {
    0%,
    64% {
      transform: rotate(0deg) scale(1);
    }
    67% {
      transform: rotate(-12deg) scale(0.86);
    }
    78% {
      transform: rotate(360deg) scale(0.86);
    }
    82%,
    100% {
      transform: rotate(360deg) scale(1);
    }
  }
  @keyframes spin-show {
    68% {
      visibility: visible;
    }
    77% {
      visibility: hidden;
    }
  }
  @keyframes spin-hide {
    64% {
      visibility: hidden;
    }
    82% {
      visibility: visible;
    }
  }
  @keyframes look {
    0%,
    8%,
    35%,
    100% {
      transform: rotate(0);
    }
    14%,
    28% {
      transform: rotate(-8deg);
    }
  }
  @keyframes twitch {
    0%,
    50%,
    56%,
    100% {
      transform: rotate(0);
    }
    52%,
    54% {
      transform: rotate(-8deg);
    }
  }
  @keyframes patient-wag {
    0%,
    70%,
    78%,
    88%,
    100% {
      transform: rotate(0);
    }
    74%,
    83% {
      transform: rotate(12deg);
    }
  }
  /* Three quick snuffles at nose level, then a long pause. */
  @keyframes sniff {
    0%,
    6%,
    12%,
    18%,
    30%,
    100% {
      transform: translateY(0) rotate(0);
    }
    9%,
    15%,
    21% {
      transform: translateY(2.5px) rotate(4deg);
    }
  }
  /* Never under 0.7, or at 32px a dot is under a pixel across. */
  @keyframes scent {
    0%,
    44%,
    100% {
      transform: scale(0.7);
    }
    22% {
      transform: scale(1);
    }
  }
  @keyframes shake {
    0%,
    45%,
    68%,
    100% {
      transform: rotate(0);
    }
    50%,
    60% {
      transform: rotate(-8deg);
    }
    55%,
    64% {
      transform: rotate(5deg);
    }
  }
  @keyframes settle {
    0%,
    100% {
      transform: rotate(0);
    }
    45%,
    60% {
      transform: rotate(10deg) translateY(4px);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .standard-mark :global(*) {
      animation: none !important;
    }
  }
</style>
