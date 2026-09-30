<!--
  The backup schedule, edited where its effects are.

  These three keys used to be findable only in Configuration, among a hundred
  others, which meant an operator looking at a list of backups and wanting a
  different interval had to leave the page that prompted the thought. They are
  the same settings, saved through the same PATCH and shown with the same row
  the Configuration page uses -- so a value set in the environment reads as
  pinned here exactly as it does there, and there is one editor rather than two
  that could disagree.
-->
<script lang="ts">
  import { supportHint } from '$lib/errors';
  import { ApiError, getSettings, updateSettings } from '$lib/api/client';
  import type { Problem, Setting, Settings } from '$lib/api/types';
  import { toasts } from '$lib/state/toasts.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import SettingRow from './SettingRow.svelte';

  interface Props {
    /** A change to the schedule changes what the page above it says. */
    onchanged: () => void;
  }

  let { onchanged }: Props = $props();

  let settings = $state<Settings | null>(null);
  let loading = $state(true);

  $effect(() => {
    const controller = new AbortController();
    loading = true;
    void getSettings(controller.signal)
      .then((result) => (settings = result))
      // A page that cannot read its settings still lists backups, so the
      // section goes quiet rather than taking the page down with it.
      .catch(() => (settings = null))
      .finally(() => (loading = false));
    return () => controller.abort();
  });

  const rows = $derived<readonly Setting[]>(
    (settings?.settings ?? []).filter((s) => s.section === 'backup'),
  );

  const findingsBySetting = $derived.by(() => {
    const map: Record<string, Problem[]> = {};
    for (const finding of settings?.findings ?? []) {
      if (!finding.setting) continue;
      (map[finding.setting] ??= []).push(finding);
    }
    return map;
  });

  /** Send one key, and return what was wrong with it for the row to show. */
  async function save(key: string, value: unknown): Promise<string> {
    try {
      const result = await updateSettings({ [key]: value } as Record<string, unknown>);
      settings = result;
      const now = result.settings?.find((s) => s.key === key);
      if (value === null) {
        toasts.success(`${key} reset`, 'It is back to the configuration file or the default.');
      } else if (now?.pending) {
        toasts.success(`${key} saved`, 'It takes effect the next time the controller restarts.');
      } else {
        toasts.success(`${key} changed`, 'It is in force now.');
      }
      onchanged();
      return '';
    } catch (cause) {
      if (cause instanceof ApiError) {
        const errors = cause.fieldErrors();
        if (errors[key]) return errors[key];
        return cause.message;
      }
      return `That change could not be made. ${supportHint()}`;
    }
  }
</script>

<section class="schedule" aria-labelledby="backup-schedule">
  <header>
    <h2 id="backup-schedule">Schedule and retention</h2>
    <p>
      What the controller takes of its own accord, where it puts it, and how many it keeps. The same
      settings appear in the configuration export; a value the environment is holding is pinned here
      too.
    </p>
  </header>

  {#if loading}
    <div class="pad"><Skeleton lines={3} /></div>
  {:else if rows.length === 0}
    <p class="none">These settings could not be read. The Configuration page has them too.</p>
  {:else}
    <div class="rows">
      {#each rows as setting (setting.key)}
        <SettingRow {setting} findings={findingsBySetting[setting.key] ?? []} onsave={save} />
      {/each}
    </div>
  {/if}
</section>

<style>
  .schedule {
    border-bottom: var(--z-border-width) solid var(--z-border);
  }
  header {
    padding: var(--z-space-4) var(--z-space-5) 0;
  }
  h2 {
    margin: 0;
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  header p {
    margin: var(--z-space-1) 0 0;
    max-width: 80ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .rows {
    margin-top: var(--z-space-3);
  }
  .pad {
    padding: var(--z-space-4) var(--z-space-5);
  }
  .none {
    margin: 0;
    padding: var(--z-space-3) var(--z-space-5) var(--z-space-4);
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
</style>
