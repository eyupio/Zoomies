<!--
  The glyph for one of the memory badges' icon names.

  The model (memory-badges.ts) names an icon by what it means -- memory, swap, a
  folder -- and is free of components so that a unit test can read it; this is the
  one place a name becomes a glyph, so a card and a pill can never disagree about
  which is which.
-->
<script lang="ts">
  import {
    ArrowDownToLine,
    ArrowUpToLine,
    Eye,
    Folder,
    FolderClock,
    HardDrive,
    Layers,
    MemoryStick,
    TriangleAlert,
  } from '@lucide/svelte';
  import type { LucideIcon } from '@lucide/svelte';
  import type { BadgeIcon } from './memory-badges';

  let { icon, size = 12 }: { icon: BadgeIcon; size?: number } = $props();

  const glyphs: Record<BadgeIcon, LucideIcon> = {
    memory: MemoryStick,
    // Memory spilling down to the host's disk.
    swap: ArrowDownToLine,
    eye: Eye,
    ceiling: ArrowUpToLine,
    alert: TriangleAlert,
    folder: Folder,
    // A folder with a clock: the one that is thrown away.
    tmp: FolderClock,
    image: Layers,
    disk: HardDrive,
  };
  const Glyph = $derived(glyphs[icon]);
</script>

<Glyph {size} strokeWidth={2} aria-hidden="true" />
