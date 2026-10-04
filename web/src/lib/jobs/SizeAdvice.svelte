<!--
  Which jobs would be better off naming a size, and who is pinned to one.

  The report is the answer to "where is routing a guess": a job that writes only
  the base label is sent to a class on a best-effort basis, and a job that writes
  the class label is promised it. Each entry says what the job's own runs
  showed, what its `runs-on` says, and what to write -- worded by the
  controller, which knows -- and an operator may pin the job to the class its
  runs call for instead of editing the workflow.

  Nothing is shown while size routing is off and nothing is pinned: there is no
  class to compare a label with, and a panel explaining a feature that is off
  would be the first thing every Jobs page met.
-->
<script lang="ts">
  import { Pin, PinOff } from '@lucide/svelte';
  import {
    deleteSizePin,
    getAutoPools,
    listLabelAdvice,
    listSizePins,
    setSizePin,
  } from '$lib/api/client';
  import type { AutoPools, LabelAdvice, SizeClass, SizePin } from '$lib/api/types';
  import { formatNumber, pluralise } from '$lib/format';
  import { AUTO_POOLS_URL } from '$lib/links';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Select from '$lib/components/Select.svelte';
  import { SIZE_CLASSES, classWord } from '$lib/hosts/tags';
  import { adviceWords, pinKey, pinScope, sentence } from './size';

  interface Props {
    /** Bumped by the page's refresh button, which is what makes this read again. */
    refresh?: number;
  }

  let { refresh = 0 }: Props = $props();

  const LIMIT = 25;

  const canOperate = $derived(session.can('operator'));

  let routing = $state<AutoPools['size_routing'] | null>(null);
  let advice = $state<LabelAdvice[]>([]);
  let total = $state(0);
  let counts = $state<Record<string, number>>({});
  let pins = $state<SizePin[]>([]);
  let reload = $state(0);

  $effect(() => {
    void refresh;
    void reload;
    const controller = new AbortController();
    const signal = controller.signal;
    // Each read stands on its own: a failure of one leaves the others on screen,
    // and none of them is worth a toast, because this is context for the grid.
    void getAutoPools(signal)
      .then((result) => (routing = result.size_routing))
      .catch(() => undefined);
    void listLabelAdvice({ limit: LIMIT }, signal)
      .then((page) => {
        advice = page.items;
        total = page.total ?? page.items.length;
        counts = page.counts ?? {};
      })
      .catch(() => undefined);
    void listSizePins(signal)
      .then((result) => (pins = result.items))
      .catch(() => undefined);
    return () => controller.abort();
  });

  const visible = $derived((routing !== null && routing !== 'off') || pins.length > 0);
  // How many there is of each kind, whatever page is showing: "2 name a class
  // that is too small · 1 names none".
  const countWords = $derived(
    Object.entries(counts)
      .filter(([, n]) => n > 0)
      .map(([kind, n]) => `${formatNumber(n)} ${adviceWords(kind).label.toLowerCase()}`)
      .join(' · '),
  );
  const pinned = $derived(new Set(pins.map(pinKey)));

  /* -- pinning ------------------------------------------------------------------ */

  async function pin(
    row: { repo: string; workflow?: string; job_name?: string },
    cls: SizeClass,
  ): Promise<boolean> {
    try {
      const result = await setSizePin({
        repo: row.repo,
        class: cls,
        ...(row.workflow ? { workflow: row.workflow } : {}),
        ...(row.job_name ? { job_name: row.job_name } : {}),
      });
      toasts.success(
        `Pinned ${pinScope(row)} to ${cls}`,
        result.reclassified > 0
          ? `${pluralise(result.reclassified, 'job')} already waiting moved to the ${cls} class.`
          : 'Jobs queued from now on are put in that class.',
      );
      reload += 1;
      return true;
    } catch (cause) {
      toasts.fromError(cause, 'That pin was not set');
      return false;
    }
  }

  async function unpin(row: SizePin): Promise<void> {
    try {
      await deleteSizePin({
        repo: row.repo,
        ...(row.workflow ? { workflow: row.workflow } : {}),
        ...(row.job_name ? { job_name: row.job_name } : {}),
      });
      toasts.success(`Removed the pin on ${pinScope(row)}`);
      reload += 1;
    } catch (cause) {
      toasts.fromError(cause, 'That pin was not removed');
    }
  }

  /* -- the form that pins a repository or one job -------------------------------- */

  let repo = $state('');
  let workflow = $state('');
  let jobName = $state('');
  let cls = $state<SizeClass>('medium');
  let saving = $state(false);

  // A job is named by its workflow and its own name together, so one without
  // the other would pin something nobody asked for.
  const scopeError = $derived(
    (workflow.trim() === '') !== (jobName.trim() === '')
      ? 'Name both the workflow and the job, or neither to pin the whole repository.'
      : '',
  );
  const repoError = $derived(
    /^[^/\s]+\/[^/\s]+$/.test(repo.trim()) ? '' : 'Use owner/name, as GitHub writes it.',
  );

  async function submit(): Promise<void> {
    if (repoError || scopeError || saving) return;
    saving = true;
    try {
      // `pin` reports a refusal itself, in a toast, so there is nothing to catch
      // here: the form is cleared only when the pin was set.
      const ok = await pin(
        { repo: repo.trim(), workflow: workflow.trim(), job_name: jobName.trim() },
        cls,
      );
      if (ok) {
        repo = '';
        workflow = '';
        jobName = '';
      }
    } finally {
      saving = false;
    }
  }
</script>

{#if visible}
  <details class="advice" data-testid="size-advice">
    <summary>
      Size labels and pins
      {#if total > 0}<span class="count tabular" aria-label="{total} jobs with advice"
          >{formatNumber(total)}</span
        >{/if}
    </summary>

    <div class="body">
      <section aria-labelledby="advice-heading">
        <h3 id="advice-heading">Jobs that would benefit from an explicit size label</h3>
        <p class="lead">
          Worked out from what each job's own runs used, and only for a job with at least five
          measured runs. Writing the class label in <span class="mono">runs-on</span> is what turns
          routing from a best effort into a guarantee.
          <a href={AUTO_POOLS_URL} target="_blank" rel="noopener noreferrer">How it works</a>
        </p>

        {#if advice.length === 0}
          <p class="none" data-testid="advice-none">
            Nothing to suggest. Every job with enough measured runs asks for about what it uses.
          </p>
        {:else}
          {#if countWords}
            <p class="counts" data-testid="advice-counts">{countWords}</p>
          {/if}
          <ul class="rows">
            {#each advice as item (pinKey(item) + item.kind)}
              <li data-testid="advice-row">
                <div class="row-head">
                  <Badge
                    tone="neutral"
                    size="sm"
                    dot={false}
                    label={adviceWords(item.kind).label}
                    title={adviceWords(item.kind).hint}
                  />
                  <span class="job"
                    >{item.repo} · {item.workflow} · <strong>{item.job_name}</strong></span
                  >
                  <span class="muted tabular">{pluralise(item.runs, 'run')} measured</span>
                </div>
                <p>{sentence(item.message)}</p>
                <p class="fix">{sentence(item.fix)}</p>
                {#if canOperate}
                  <div class="actions">
                    <Button
                      size="sm"
                      variant="secondary"
                      icon={Pin}
                      disabled={pinned.has(pinKey(item))}
                      onclick={() => pin(item, item.class)}
                    >
                      Pin to {classWord(item.class)}
                    </Button>
                  </div>
                {/if}
              </li>
            {/each}
          </ul>
          {#if total > advice.length}
            <p class="muted more">
              Showing the {advice.length} that cost the most, of {formatNumber(total)}.
              <span class="mono">zoomies jobs advice</span> lists the rest.
            </p>
          {/if}
        {/if}
      </section>

      <section aria-labelledby="pins-heading">
        <h3 id="pins-heading">Pins</h3>
        <p class="lead">
          A pin puts a job, or every job in a repository, in a class whatever its runs say. It is
          the answer where the workflow cannot be changed, and it comes before a job's history but
          after a size label the job writes itself.
        </p>
        {#if pins.length === 0}
          <p class="none" data-testid="pins-none">Nothing is pinned.</p>
        {:else}
          <ul class="rows" data-testid="pins">
            {#each pins as item (pinKey(item))}
              <li data-testid="pin-row">
                <div class="row-head">
                  <Badge tone="neutral" size="sm" dot={false} label={classWord(item.class)} />
                  <span class="job">{pinScope(item)}</span>
                  {#if item.created_at}
                    <span class="muted"
                      >{item.created_by ? `by ${item.created_by} ` : ''}<RelativeTime
                        value={item.created_at}
                        plain
                      /></span
                    >
                  {/if}
                  {#if canOperate}
                    <Button size="sm" variant="ghost" icon={PinOff} onclick={() => unpin(item)}>
                      Remove
                      <span class="sr-only">the pin on {pinScope(item)}</span>
                    </Button>
                  {/if}
                </div>
              </li>
            {/each}
          </ul>
        {/if}

        {#if canOperate}
          <form
            class="pin-form"
            aria-label="Pin a repository or a job"
            onsubmit={(event) => {
              event.preventDefault();
              void submit();
            }}
          >
            <Field label="Repository" error={repoError && repo !== '' ? repoError : ''}>
              {#snippet children({ id, describedBy, invalid })}
                <Input
                  bind:value={repo}
                  {id}
                  {describedBy}
                  {invalid}
                  size="sm"
                  mono
                  placeholder="acme/api"
                  autocomplete="off"
                />
              {/snippet}
            </Field>
            <Field label="Workflow" hint="Leave out to pin the whole repository.">
              {#snippet children({ id, describedBy })}
                <Input
                  bind:value={workflow}
                  {id}
                  {describedBy}
                  size="sm"
                  mono
                  placeholder="ci.yml"
                  autocomplete="off"
                />
              {/snippet}
            </Field>
            <Field label="Job" error={scopeError}>
              {#snippet children({ id, describedBy, invalid })}
                <Input
                  bind:value={jobName}
                  {id}
                  {describedBy}
                  {invalid}
                  size="sm"
                  mono
                  placeholder="build"
                  autocomplete="off"
                />
              {/snippet}
            </Field>
            <Field label="Class">
              {#snippet children({ id, describedBy })}
                <Select
                  bind:value={cls}
                  {id}
                  {describedBy}
                  size="sm"
                  options={SIZE_CLASSES.map((c) => ({ value: c, label: classWord(c) }))}
                  onchange={(value) => (cls = value as SizeClass)}
                />
              {/snippet}
            </Field>
            <div class="submit">
              <Button
                type="submit"
                variant="primary"
                size="sm"
                icon={Pin}
                loading={saving}
                disabled={Boolean(repoError || scopeError)}
              >
                Pin
              </Button>
            </div>
          </form>
        {/if}
      </section>
    </div>
  </details>
{/if}

<style>
  .advice {
    margin: 0 0 var(--z-space-5);
  }
  summary {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    padding: var(--z-space-3);
    color: var(--z-accent);
    font-size: var(--z-text-sm);
    cursor: pointer;
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  .advice[open] summary {
    margin-bottom: var(--z-space-3);
  }
  .count {
    padding: 0 var(--z-space-2);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-full);
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-5);
    padding: var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  h3 {
    margin: 0 0 var(--z-space-2);
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .lead,
  .none,
  .more,
  .counts {
    margin: 0 0 var(--z-space-3);
    max-width: 90ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .rows {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .rows li {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    padding-bottom: var(--z-space-3);
    border-bottom: var(--z-border-width) solid var(--z-border);
  }
  .rows li:last-child {
    border-bottom: 0;
    padding-bottom: 0;
  }
  .row-head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2) var(--z-space-3);
  }
  .job {
    font-size: var(--z-text-sm);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .rows p {
    margin: 0;
    max-width: 90ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .rows p.fix {
    color: var(--z-text);
  }
  .actions {
    display: flex;
  }
  .muted {
    color: var(--z-text-subtle);
    font-size: var(--z-text-xs);
  }
  a {
    color: var(--z-accent);
  }
  .pin-form {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(11rem, 1fr));
    gap: var(--z-space-3);
    align-items: start;
    margin-top: var(--z-space-4);
    padding-top: var(--z-space-4);
    border-top: var(--z-border-width) solid var(--z-border);
  }
  .submit {
    align-self: end;
  }
</style>
