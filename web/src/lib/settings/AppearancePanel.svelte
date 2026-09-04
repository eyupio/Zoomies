<!--
  Appearance.

  These are this browser's preferences, not the instance's: they are kept in
  local storage and never leave the machine, which the panel says out loud so
  nobody wonders why a colleague's Zoomies looks different.
-->
<script lang="ts">
  import { CLOCK_INTERVAL_MS } from '$lib/format';
  import { prefs } from '$lib/state/prefs.svelte';
  import { theme } from '$lib/state/theme.svelte';
  import type { ThemeChoice } from '$lib/state/theme.svelte';
  import RadioGroup from '$lib/components/RadioGroup.svelte';
  import Switch from '$lib/components/Switch.svelte';

  const THEMES = [
    { value: 'system', label: 'Match the system', description: 'Follows the operating system.' },
    { value: 'light', label: 'Light', description: 'Always light, whatever the system says.' },
    { value: 'dark', label: 'Dark', description: 'Always dark.' },
  ];

  const seconds = Math.round(CLOCK_INTERVAL_MS / 1000);
</script>

<div class="panel">
  <header>
    <h2>Appearance</h2>
    <p>
      Kept in this browser only. Nothing here is sent to the controller or shared with anyone else
      signing in.
    </p>
  </header>

  <div class="body">
    <section aria-labelledby="theme-heading">
      <h3 id="theme-heading">Theme</h3>
      <RadioGroup
        value={theme.choice}
        name="theme"
        options={THEMES}
        onchange={(value) => theme.set(value as ThemeChoice)}
      />
      <p class="note">
        Currently showing the {theme.resolved} palette. Both are measured for WCAG AA, so nothing becomes
        harder to read either way.
      </p>
    </section>

    <section aria-labelledby="layout-heading">
      <h3 id="layout-heading">Layout</h3>
      <Switch
        label="Collapse the navigation"
        description="Shows icons only, which gives a wide grid more room. The same thing the toggle in the sidebar does."
        checked={prefs.navCollapsed}
        onchange={(on) => (prefs.navCollapsed = on)}
      />
    </section>

    <section aria-labelledby="time-heading">
      <h3 id="time-heading">Times and motion</h3>
      <p class="note">
        Relative times -- "4m ago" -- refresh every {seconds} seconds from one shared clock, and every
        one of them carries the exact timestamp in its tooltip, so nothing is ever only approximate. Durations
        use tabular figures so columns line up.
      </p>
      <p class="note">
        Animation follows the operating system's reduced-motion setting: when that is on, Zoomies
        does not animate. There is nothing to switch here.
      </p>
    </section>

    <section aria-labelledby="density-heading">
      <h3 id="density-heading">Density</h3>
      <p class="note">
        Zoomies has one density, tuned for a dense operational grid at 13px. Row height and page
        size are the two things that actually change how much fits on screen: page size is set per
        grid, at the bottom of each one, and is remembered separately for every table.
      </p>
    </section>
  </div>
</div>

<style>
  .panel {
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  header {
    padding: var(--z-space-4) var(--z-space-5);
    border-bottom: 1px solid var(--z-border);
  }
  h2 {
    margin: 0;
    font-size: var(--z-text-lg);
    line-height: var(--z-leading-lg);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  header p {
    margin: var(--z-space-1) 0 0;
    max-width: 74ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-6);
    padding: var(--z-space-5);
  }
  h3 {
    margin: 0 0 var(--z-space-3);
    font-size: var(--z-text-2xs);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    font-weight: var(--z-weight-medium);
    color: var(--z-text-muted);
  }
  .note {
    margin: var(--z-space-3) 0 0;
    max-width: 80ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
</style>
