<!--
  The first three things, and nothing else.

  A brand-new administrator used to land on an Overview of four zeroes whose
  only call to action was "Create a pool" -- which opens a wizard that refuses
  on its first step, because a pool belongs to a GitHub App installation and
  there is not one yet. The single action the product offered a new operator
  was a guaranteed round trip through a dead end, and the step that actually
  had to happen first was discoverable only by noticing a word in the nav.

  So: three numbered steps, each ticked from real fleet state, each with the
  one action that advances it. A step that cannot be done yet says why rather
  than offering a button that leads to a refusal.

  It disappears for good once a job has run here, because at that point the
  fleet works and a checklist on the dashboard is clutter. The dismissal is
  per-browser: it is a nudge, not a setting, and it costs nothing to see again
  on another machine.
-->
<script lang="ts">
  import {
    ArrowRight,
    Boxes,
    Check,
    HardDrive,
    PlayCircle,
    Plug,
    UserCheck,
    X,
  } from '@lucide/svelte';
  import { listInstallations } from '$lib/api/client';
  import { events } from '$lib/api/sse';
  import { runsOn } from '$lib/brand';
  import { fleet } from '$lib/state/fleet.svelte';
  import { storage } from '$lib/state/prefs.svelte';
  import { session } from '$lib/state/session.svelte';
  import Button from '$lib/components/Button.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import IconButton from '$lib/components/IconButton.svelte';

  /**
   * Told whenever this panel appears or goes away, so the Overview can quieten
   * the problems panel's "nothing needs your attention" line while the
   * checklist is the one saying what is missing.
   */
  let { onpending }: { onpending?: (pending: boolean) => void } = $props();

  const DISMISS_KEY = 'zoomies.firstrun.dismissed';

  let installations = $state<number | null>(null);
  let dismissed = $state(storage.get(DISMISS_KEY) === '1');

  const canAdmin = $derived(session.can('admin'));
  const canOperate = $derived(session.can('operator'));

  // The Overview's fleet cache does not carry installations -- no panel on the
  // page needs them -- so this asks once, and again whenever one changes.
  async function count(): Promise<void> {
    try {
      const result = await listInstallations();
      installations = (result.items ?? []).length;
    } catch {
      // A failure here is not worth a message: every other panel on this page
      // is already saying that the controller is unreachable.
      installations = null;
    }
  }
  $effect(() => {
    void count();
  });
  $effect(() =>
    events.subscribe(['installation.updated', 'installation.deleted'], () => void count()),
  );

  const hasInstallation = $derived((installations ?? 0) > 0);
  const pools = $derived(fleet.pools);
  const hasPool = $derived(pools.length > 0);
  /**
   * Whether anything can actually run a runner.
   *
   * A `--mode controller` install has no embedded agent, so the fleet has no
   * hosts at all: without this step the operator connects GitHub, creates a
   * pool, pushes a workflow, and the job queues for ever with nothing to say
   * why. Where the controller runs an agent of its own the host already exists
   * and this row never appears, so the single-VM path is unchanged.
   */
  const hasHost = $derived(fleet.hosts.length > 0);
  /** A job has been seen here: queued, running or finished within the window. */
  const hasJobs = $derived.by(() => {
    const s = fleet.stats;
    if (!s) return false;
    return (s.queued_jobs ?? 0) + (s.running_jobs ?? 0) + (s.completed ?? 0) + (s.failed ?? 0) > 0;
  });

  /**
   * The line a workflow writes, by the product's one rule.
   *
   * Deriving it by hand -- taking the last label, since the store sorts them --
   * gave a different answer from every other surface: a pool labelled
   * [cuda, zoomies, zoomies-gpu] needs `[cuda, zoomies-gpu]`, and the last
   * label alone would send jobs to a different pool. `runsOn` is what the
   * wizard, the pool page and the installer all print.
   */
  const runsOnValue = $derived(runsOn(pools[0]?.labels ?? []));

  /**
   * Shown only while the fleet has never done its job.
   *
   * `installations === null` means the count has not landed yet: rendering the
   * panel then would flash "Connect GitHub" at an operator who connected it
   * months ago, so it waits.
   */
  const show = $derived(!dismissed && installations !== null && !hasJobs);

  const WORDS = ['No', 'One step', 'Two steps', 'Three steps', 'Four steps'];
  const remaining = $derived(
    WORDS[[!hasInstallation, !hasHost, !hasPool, true].filter(Boolean).length] ?? 'A few steps',
  );

  /**
   * The numbers on the markers.
   *
   * They are positions in this host's own list, not in a fixed spine: the host
   * step only exists on a controller with no agent of its own, and an
   * unnumbered row between numbered ones reads as though it were optional.
   */
  const steps = $derived(['admin', 'github', ...(hasHost ? [] : ['host']), 'pool', 'workflow']);
  const number = (id: string): number => steps.indexOf(id) + 1;

  $effect(() => {
    onpending?.(show);
  });

  function dismiss(): void {
    dismissed = true;
    storage.set(DISMISS_KEY, '1');
  }

  // Once a job has run the fleet is working, and the checklist has said
  // everything it has to say. Remembering that stops it coming back if the
  // stats window later empties.
  $effect(() => {
    if (hasJobs && !dismissed) dismiss();
  });
</script>

{#if show}
  <section class="firstrun" aria-labelledby="firstrun-heading">
    <header>
      <div>
        <h2 id="firstrun-heading">Finish setting up</h2>
        <p>{remaining} between here and a job running on your own runner.</p>
      </div>
      <IconButton icon={X} label="Hide this checklist" size="sm" onclick={dismiss} />
    </header>

    <ol>
      <li class="done">
        <span class="marker" aria-hidden="true"><Check size={13} /></span>
        <div class="body">
          <p class="title">
            <UserCheck size={14} aria-hidden="true" />
            Create an administrator<span class="sr-only">, done</span>
          </p>
          <p class="why">Done -- you are signed in as {session.identity?.name ?? 'the admin'}.</p>
        </div>
        <div class="action"></div>
      </li>

      <li class:done={hasInstallation}>
        <span class="marker" aria-hidden="true">
          {#if hasInstallation}<Check size={13} />{:else}{number('github')}{/if}
        </span>
        <div class="body">
          <p class="title">
            <Plug size={14} aria-hidden="true" />
            Connect GitHub
            {#if hasInstallation}<span class="sr-only">, done</span>{/if}
          </p>
          <p class="why">
            Zoomies authenticates as a GitHub App: it is how the controller sees queued jobs and
            registers runners. Nothing can run until one is installed.
          </p>
        </div>
        <div class="action">
          {#if hasInstallation}
            <a href="/installations">Installed</a>
          {:else if canAdmin}
            <Button variant="primary" size="sm" href="/installations" iconAfter={ArrowRight}>
              Connect GitHub
            </Button>
          {:else}
            <p class="blocked">An administrator connects this.</p>
          {/if}
        </div>
      </li>

      {#if !hasHost}
        <li>
          <span class="marker" aria-hidden="true">{number('host')}</span>
          <div class="body">
            <p class="title">
              <HardDrive size={14} aria-hidden="true" />
              Add a host
            </p>
            <p class="why">
              A host is a machine running the Zoomies agent; it is where runners are actually
              created. This controller is not running one, so nothing has anywhere to go yet.
            </p>
          </div>
          <div class="action">
            {#if canAdmin}
              <Button variant="primary" size="sm" href="/hosts" iconAfter={ArrowRight}>
                Add a host
              </Button>
            {:else}
              <p class="blocked">An administrator enrols one.</p>
            {/if}
          </div>
        </li>
      {/if}

      <li class:done={hasPool} class:waiting={!hasInstallation}>
        <span class="marker" aria-hidden="true">
          {#if hasPool}<Check size={13} />{:else}{number('pool')}{/if}
        </span>
        <div class="body">
          <p class="title">
            <Boxes size={14} aria-hidden="true" />
            Create a pool
            {#if hasPool}<span class="sr-only">, done</span>{/if}
          </p>
          <p class="why">
            A pool decides what labels your runners answer to, how they run, and how many of them
            exist at once.
          </p>
        </div>
        <div class="action">
          {#if hasPool}
            <a href="/pools">{pools.length === 1 ? '1 pool' : `${pools.length} pools`}</a>
          {:else if !hasInstallation}
            <!-- Never offer an action whose first screen is a refusal: the pool
                 wizard's first step is the installation this step has not got. -->
            <p class="blocked">After GitHub is connected.</p>
          {:else if canOperate}
            <Button variant="primary" size="sm" href="/pools/new" iconAfter={ArrowRight}>
              Create a pool
            </Button>
          {:else}
            <p class="blocked">An operator creates this.</p>
          {/if}
        </div>
      </li>

      <li class:waiting={!hasPool}>
        <span class="marker" aria-hidden="true">{number('workflow')}</span>
        <div class="body">
          <p class="title">
            <PlayCircle size={14} aria-hidden="true" />
            Point a workflow at it
          </p>
          <p class="why">
            Change <code>runs-on</code> in a workflow and push. The job queues, the scheduler starts a
            runner for it, and it appears on this page.
          </p>
          {#if hasPool}
            <p class="runs-on">
              <code>runs-on: {runsOnValue}</code>
              <CopyButton value={`runs-on: ${runsOnValue}`} label="Copy the runs-on line" />
            </p>
          {/if}
        </div>
        <div class="action">
          {#if hasPool}
            <Button variant="secondary" size="sm" href="/migrate" iconAfter={ArrowRight}>
              Rewrite workflows
            </Button>
          {:else}
            <p class="blocked">After a pool exists.</p>
          {/if}
        </div>
      </li>
    </ol>
  </section>
{/if}

<style>
  /*
    Accent-tinted rather than a plain surface: this is the one thing on the page
    asking for attention, and it has to win against four metric tiles without
    borrowing a status colour operators have learned to read as a fleet state.
  */
  .firstrun {
    border: 1px solid var(--z-accent-border, var(--z-border-strong));
    border-radius: var(--z-radius-md);
    background: var(--z-accent-subtle);
  }
  header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--z-space-4);
    padding: var(--z-space-4) var(--z-space-5) var(--z-space-2);
  }
  h2 {
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  header p {
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  ol {
    display: flex;
    flex-direction: column;
    margin: 0;
    padding: 0 var(--z-space-5) var(--z-space-4);
    list-style: none;
  }
  li {
    display: grid;
    grid-template-columns: var(--z-space-6) minmax(0, 1fr) auto;
    align-items: start;
    gap: var(--z-space-3);
    padding: var(--z-space-3) 0;
  }
  li + li {
    border-top: 1px solid var(--z-border);
  }
  /* A step that cannot start yet is quieter, but never hidden: the operator
     should be able to read the whole path before walking it. */
  li.waiting .body {
    opacity: 0.72;
  }
  .marker {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: var(--z-space-5);
    height: var(--z-space-5);
    border: 1px solid var(--z-border-strong);
    border-radius: var(--z-radius-full);
    background: var(--z-surface);
    font-size: var(--z-text-2xs);
    color: var(--z-text-muted);
  }
  li.done .marker {
    border-color: var(--z-idle-border);
    background: var(--z-idle-subtle);
    color: var(--z-idle);
  }
  .body {
    min-width: 0;
  }
  .title {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
  }
  li.done .title {
    color: var(--z-text-muted);
  }
  .why {
    margin: var(--z-space-1) 0 0;
    max-width: 62ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
    text-wrap: pretty;
  }
  .runs-on {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: var(--z-space-2) 0 0;
  }
  code {
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
    padding: 2px var(--z-space-2);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    color: var(--z-text);
  }
  .why code {
    padding: 0;
    background: none;
  }
  .action {
    display: flex;
    align-items: center;
    min-height: var(--z-space-5);
  }
  .action a {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .blocked {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
    text-align: right;
  }
  @media (max-width: 720px) {
    li {
      grid-template-columns: var(--z-space-6) minmax(0, 1fr);
    }
    .action {
      grid-column: 2;
      margin-top: var(--z-space-2);
    }
    .blocked {
      text-align: left;
    }
  }
</style>
