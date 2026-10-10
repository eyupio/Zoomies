<script lang="ts">
  import { onMount } from 'svelte';
  import { Dog, PawPrint } from '@lucide/svelte';
  const quotes = [
    'Give me a sniff. There is an answer here somewhere.',
    'Brain zoomies in progress. Mind the paws.',
    'Fetching a thought. Hopefully not a tennis ball.',
    'One ear on your question. One ear on the biscuit tin.',
    'Chasing the useful bit. It is surprisingly speedy.',
    'Good questions deserve a proper tail-wag.',
  ];
  let index = $state(0);
  onMount(() => {
    const timer = window.setInterval(() => {
      index = (index + 1) % quotes.length;
    }, 4500);
    return () => window.clearInterval(timer);
  });
</script>

<div class="thinking">
  <span class="sr-only">Eli is thinking</span>
  <div class="track" aria-hidden="true">
    <span class="paw first"><PawPrint size={12} /></span>
    <span class="paw second"><PawPrint size={12} /></span>
    <span class="dog"><Dog size={28} /></span>
  </div>
  <p aria-hidden="true">{quotes[index]}</p>
</div>

<style>
  .thinking {
    color: var(--z-text-muted);
    font-size: var(--z-text-sm);
  }
  .track {
    position: relative;
    width: 8rem;
    height: var(--z-space-10);
    color: var(--z-accent);
  }
  .dog {
    position: absolute;
    animation: zoomies 2.4s ease-in-out infinite;
  }
  .paw {
    position: absolute;
    top: var(--z-space-5);
    opacity: 0;
    animation: trail 2.4s infinite;
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
