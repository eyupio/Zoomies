<!--
  Creating a pool and editing one, on one page.

  It used to be a wizard: eight steps to create, four to edit, a fork before
  them, and a row of step markers that could not be pressed. A pool is a
  document of settings, not a procedure, and what a first pool needs is three
  of them. So this is one page, the same for both, made of sections that are
  each a decision -- who the pool is for, which hosts, what a runner is, how big,
  how many, what makes it faster -- and each says its current answer on its own
  row without being opened. A new pool opens on the first and leaves the rest
  at their defaults; an existing one opens on none, and any section is one tap
  away from any other.

  The draft is one object held here, so closing a section never loses what was
  typed in it; the sections are presentation only. Client-side rules run
  continuously and the sticky bar says how many are being broken. The server's
  own verdict is asked for as the draft changes, not only at the end, because
  "the pool exists but no host can run it" is a much worse place to find out and
  every section edits something its count depends on.
-->
<script lang="ts">
  import { onMount, tick, untrack } from 'svelte';
  import {
    ApiError,
    createPool,
    getPoolDefaults,
    listInstallations,
    listPoolPlatforms,
    listRunnerGroups,
    updatePool,
    validatePool,
  } from '$lib/api/client';
  import type {
    Body,
    Installation,
    Pool,
    PoolPlatform,
    Resources,
    Result,
    RunnerGroup,
  } from '$lib/api/types';
  import { BRAND_LABEL, brandedLabel } from '$lib/brand';
  import { fleet } from '$lib/state/fleet.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { nicknamedPoolName, poolName, spinWord } from './names';
  import { draftErrors, draftFromPool, emptyDraft, toInteger, toPoolBody } from './draft';
  import type { PoolDraft } from './draft';
  import { hostMatchesSelector } from './hostSelector';
  import { SECTIONS, editedSections, sectionForField, sectionHead } from './sections';
  import type { SectionId } from './sections';
  import { summarise } from './summaries';
  import { statusLine } from './verdict';
  import { backendOffers } from './vocabulary';
  import BasicsSection from './BasicsSection.svelte';
  import HostsSection from './HostsSection.svelte';
  import PoolActionBar from './PoolActionBar.svelte';
  import PoolCheck from './PoolCheck.svelte';
  import PoolRail from './PoolRail.svelte';
  import PoolSection from './PoolSection.svelte';
  import RunnerSection from './RunnerSection.svelte';
  import ScalingSection from './ScalingSection.svelte';
  import SizeSection from './SizeSection.svelte';
  import SpeedSection from './SpeedSection.svelte';

  interface Props {
    /** The pool being edited. Leave it out to create a new one. */
    pool?: Pool;
    oncancel: () => void;
    ondone: (pool: Pool) => void;
    class?: string;
  }

  let { pool, oncancel, ondone, class: className = '' }: Props = $props();

  const editing = $derived(pool !== undefined);

  // Captured once on purpose: the routes remount this form with a {#key} when
  // they start editing a different pool, so a live reference would be wrong.
  let draft = $state<PoolDraft>(untrack(() => (pool ? draftFromPool(pool) : emptyDraft())));
  // Where the editor started, so an edit can say which sections it has changed
  // and Save can say there is nothing to save.
  const initial: PoolDraft = untrack(() => $state.snapshot(draft) as PoolDraft);

  /*
    Which sections are open.

    A new pool opens on the first and leaves the rest shut: they all have an
    answer already, and the row says what it is. An existing pool opens on none,
    because an operator who came to change one setting is better served by seven
    lines they can read than by a form they have to scroll. A link to one of
    them -- `#size` -- opens it, see `onMount` below.
  */
  let opened = $state<Record<SectionId, boolean>>({
    basics: untrack(() => pool === undefined),
    hosts: false,
    runner: false,
    size: false,
    scaling: false,
    speed: false,
  });
  const allOpen = $derived(SECTIONS.every((section) => opened[section.id]));

  let touched = $state<Record<string, boolean>>({});
  let serverErrors = $state<Record<string, string>>({});
  let socketConfirmed = $state(untrack(() => pool?.docker_mode === 'host-socket'));

  /*
    Auto-naming, on creation only.

    `autoName` is the last name the editor produced. While the field still
    holds it the name is the editor's to keep current -- so choosing Podman in
    the runner section renames the pool -- and the moment an operator types over
    it the editor stops touching it, because a field that rewrites itself under
    someone's cursor is worse than no help at all. Editing an existing pool
    never generates anything: its name is already in workflows.

    `autoLabel` plays the same part for the labels, with one more state:
    `null`, meaning the operator has taken the labels over. Removing the
    suggested chip has to stick, and without that third state an empty list
    looks exactly like a list nothing has been put in yet.
  */
  let kennelWord = $state(spinWord());
  let autoName = $state('');
  let autoLabel = $state<string | null>('');
  let submitting = $state(false);

  let installations = $state<Installation[]>([]);
  let installationsLoading = $state(true);
  let installationsError = $state<unknown>(null);
  /** Bumped by the error state's retry, which re-runs the fetch below. */
  let installationsAttempt = $state(0);

  let groups = $state<RunnerGroup[]>([]);
  let groupsLoading = $state(false);
  let groupsError = $state<unknown>(null);

  /**
   * The fleet's own default size, and the maximum the editor worked out from
   * the room the hosts have for it.
   *
   * `autoMax` is the same idea as `autoName` above: while the field still
   * holds what the editor put there, the editor keeps it current, so choosing
   * bigger runners or fewer hosts lowers the cap in front of the operator. The
   * moment they type their own it stops following, because a number that
   * rewrites itself under somebody's cursor is worse than no help at all.
   * Editing an existing pool never follows: that cap is in force right now,
   * and the scaling section offers the fleet's figure rather than taking it.
   */
  let defaults = $state<Resources | null>(null);
  let fleetDefaults = $state<Result<'getPoolDefaults'>['runner_settings'] | null>(null);
  let autoMax = $state<string | null>('4');

  // The operating systems a runner image is published for. Served rather than
  // hard-coded so the picker cannot offer one that does not exist.
  let platforms = $state<PoolPlatform[]>([]);

  let verdict = $state<Result<'validatePool'> | null>(null);
  let validating = $state(false);
  let stabilityRevision = $state(0);
  let validateError = $state<unknown>(null);

  // The backend section counts over the hosts this pool is allowed to land on,
  // not the whole fleet: "offered by 3 hosts" is a lie if two of them are the
  // amd64 boxes an arm64 pool will never touch. Placement comes first on the
  // page for exactly this reason.
  const selectedHosts = $derived(
    fleet.hosts.filter((host) => hostMatchesSelector(host, draft.host_selector)),
  );
  const restrictedToHosts = $derived(Object.keys(draft.host_selector).length > 0);
  const offers = $derived(backendOffers(selectedHosts));
  const dindHosts = $derived(offers.find((offer) => offer.kind === draft.backend)?.dindHosts ?? 0);
  const clientErrors = $derived(draftErrors(draft, socketConfirmed, offers, fleet.loaded));
  const body = $derived(toPoolBody(draft));

  /**
   * What is shown beside the controls. Client rules show once a field has been
   * left; the controller's own refusals of a field show once it has been left
   * too, and server rules from a failed save show at once. A refusal of a field
   * nobody has touched is on the controller's panel instead, so it is never
   * both.
   */
  const errors = $derived.by(() => {
    const out: Record<string, string> = { ...serverErrors };
    for (const issue of verdict?.errors ?? []) {
      if (touched[issue.field] && out[issue.field] === undefined) out[issue.field] = issue.message;
    }
    for (const [field, message] of Object.entries(clientErrors)) {
      if (touched[field]) out[field] = message;
    }
    return out;
  });

  /** How many of what is shown belong to each section, for the rows and the rail. */
  const problems = $derived.by(() => {
    const out: Record<SectionId, number> = {
      basics: 0,
      hosts: 0,
      runner: 0,
      size: 0,
      scaling: 0,
      speed: 0,
    };
    for (const field of Object.keys(errors)) {
      const section = sectionForField(field);
      if (section) out[section] += 1;
    }
    return out;
  });

  const edited = $derived(editing ? editedSections(draft, initial) : new Set<SectionId>());
  const dirty = $derived(!editing || edited.size > 0);

  const installationLabel = $derived(
    installations.find((entry) => entry.id === draft.installation_id)?.target ?? '',
  );
  const summaries = $derived(
    summarise(draft, {
      installation: installationLabel,
      hostsTotal: fleet.loaded ? fleet.hosts.length : null,
      hostsMatching: fleet.loaded ? selectedHosts.length : null,
    }),
  );
  const railItems = $derived(
    SECTIONS.map((section) => ({
      id: section.id,
      title: section.title,
      problems: problems[section.id],
      edited: edited.has(section.id),
    })),
  );

  // The controller's refusals the browser has not already put beside a control.
  const refusals = $derived(
    (verdict?.errors ?? []).filter((issue) => clientErrors[issue.field] === undefined),
  );
  const status = $derived(
    statusLine({
      blockers: Object.keys(clientErrors).length,
      refusals: refusals.length,
      editing,
      dirty,
      unreachable: validateError !== null,
      verdict,
      fleetKnown: fleet.loaded,
    }),
  );

  function touch(field: string): void {
    touched = { ...touched, [field]: true };
    if (serverErrors[field] !== undefined) {
      const rest = { ...serverErrors };
      delete rest[field];
      serverErrors = rest;
    }
  }

  /* -- moving about the page ----------------------------------------------------- */

  /**
   * Open a section, bring it into view and put the cursor where it is needed.
   *
   * What is wrong comes first -- the first control the browser has marked
   * invalid -- and the section's own heading is the fallback, so a keyboard or a
   * screen reader always arrives somewhere. The address gains `#size`, as a
   * replacement rather than an entry: the section is a place on this page, and
   * the back button should leave it.
   */
  async function jump(id: SectionId): Promise<void> {
    opened[id] = true;
    await tick();
    const section = document.getElementById(`pool-${id}`);
    if (!section) return;
    const calm = matchMedia('(prefers-reduced-motion: reduce)').matches;
    section.scrollIntoView({ behavior: calm ? 'auto' : 'smooth', block: 'start' });
    const target =
      section.querySelector<HTMLElement>('[aria-invalid="true"]') ??
      section.querySelector<HTMLElement>('button[aria-expanded]');
    target?.focus({ preventScroll: true });
    history.replaceState(history.state, '', `${location.pathname}${location.search}#${id}`);
  }

  function toggleAll(): void {
    const next = !allOpen;
    for (const section of SECTIONS) opened[section.id] = next;
  }

  onMount(() => {
    // A link to a section opens it: `?edit=1#size`.
    function followHash(): void {
      const wanted = location.hash.replace(/^#(pool-)?/, '');
      const found = SECTIONS.find((section) => section.id === wanted);
      if (found) void jump(found.id);
    }
    followHash();
    addEventListener('hashchange', followHash);
    return () => removeEventListener('hashchange', followHash);
  });

  /**
   * The topmost section with something wrong in it, whichever way it was found:
   * a rule of the browser's, a refusal from the controller, or one from a save.
   */
  function firstProblem(): SectionId | null {
    const fields = [
      ...Object.keys(clientErrors),
      ...(verdict?.errors ?? []).map((issue) => issue.field),
      ...Object.keys(serverErrors),
    ];
    for (const section of SECTIONS) {
      if (fields.some((field) => section.fields.includes(field))) return section.id;
    }
    return null;
  }

  /** Show everything that is wrong, and take the cursor to the first of it. */
  function reveal(): void {
    const next = { ...touched };
    for (const field of Object.keys(clientErrors)) next[field] = true;
    touched = next;
    const where = firstProblem();
    if (where) void jump(where);
  }

  /* -- what the fleet and GitHub can offer --------------------------------- */

  /*
    Where a fixed size opens. It is a fleet setting, so the sliders cannot have
    a figure of their own: an editor showing two cores while the fleet says
    eight would be describing a pool it is not about to create.

    It is the fleet's *suggestion* rather than what a pool becomes -- a pool
    that names no size is sized by its host -- so the figures are put on the
    sliders and nowhere else. A pool being edited already has whatever size it
    was given and is left alone.
  */
  $effect(() => {
    const controller = new AbortController();
    getPoolDefaults(controller.signal)
      .then((response) => {
        const resources = response.suggested_resources ?? response.resources ?? {};
        defaults = resources;
        // The fleet's own timings, so the timings row can say what each
        // setting is being overridden *from*. An input whose placeholder reads
        // "20m0s, the fleet's" is one an operator can leave alone with
        // confidence; an empty box beside the word "timeout" is one they feel
        // obliged to fill.
        fleetDefaults = response.runner_settings ?? null;
        untrack(() => {
          if (editing) return;
          if (draft.cpus === '' && resources.cpus !== undefined)
            draft.cpus = String(resources.cpus);
          if (draft.memory_mb === '' && resources.memory_mb !== undefined) {
            draft.memory_mb = String(resources.memory_mb);
          }
          // The minimum is deliberately left empty rather than opened on the
          // fleet's: an empty minimum already follows runners.minimum_*, and
          // copying today's figure in would freeze it into the pool, so a
          // later change to the fleet setting would pass this pool by.
        });
      })
      // A failure here is not worth an error state: the sliders fall back to
      // the built-in figures, and nothing on the automatic path reads them.
      .catch(() => {});
    return () => controller.abort();
  });

  /*
    The maximum follows what the fleet can actually place, until it is typed
    over. This is the half of "how many runners" that nothing else can answer:
    the cap is a number about machines, and the fleet is the only thing that
    knows how many runners of this size its hosts can hold.
  */
  const followingMax = $derived(!editing && autoMax !== null && draft.max_runners === autoMax);
  $effect(() => {
    const room = verdict?.room?.runners;
    if (editing || room === undefined || room <= 0) return;
    untrack(() => {
      if (autoMax === null) return;
      if (draft.max_runners !== autoMax) {
        // Typed over: the editor is done with this field.
        autoMax = null;
        return;
      }
      const next = String(Math.max(room, toInteger(draft.min_runners) ?? 0, 1));
      if (next === draft.max_runners) return;
      draft.max_runners = next;
      autoMax = next;
    });
  });

  $effect(() => {
    void installationsAttempt;
    const controller = new AbortController();
    // A failure here is not worth an error state: the picker falls back to
    // "Any", which is what a pool got before platforms existed.
    listPoolPlatforms(controller.signal)
      .then((response) => {
        platforms = response.items ?? [];
      })
      .catch(() => {});
    return () => controller.abort();
  });

  $effect(() => {
    const controller = new AbortController();
    installationsLoading = true;
    installationsError = null;
    listInstallations(controller.signal)
      .then((response) => {
        const items = response.items ?? [];
        installations = items;
        // One installation is the common case; choosing it for the operator is
        // the difference between an editor and a form.
        const only = items[0];
        if (draft.installation_id === '' && items.length === 1 && only?.id) {
          draft.installation_id = only.id;
        }
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        installationsError = cause;
      })
      .finally(() => {
        installationsLoading = false;
      });
    return () => controller.abort();
  });

  $effect(() => {
    const id = draft.installation_id;
    groups = [];
    groupsError = null;
    if (id === '') {
      groupsLoading = false;
      return;
    }
    const controller = new AbortController();
    groupsLoading = true;
    listRunnerGroups(id, controller.signal)
      .then((response) => {
        groups = response.items ?? [];
        if (!editing && !touched.runner_group && draft.runner_group === '') {
          const managed = groups.find((group) => group.name?.toLowerCase() === 'zoomies');
          if (managed?.name) draft.runner_group = managed.name;
        }
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        groupsError = cause;
      })
      .finally(() => {
        groupsLoading = false;
      });
    return () => controller.abort();
  });

  /* -- the name, and the label it implies ---------------------------------- */

  /** The names already in use, so a new pool is not offered one of them. */
  const poolNames = $derived(fleet.pools.map((p) => p.name ?? ''));

  /**
   * Whether the operator has asked for a spaniel in the name.
   *
   * Without this the dice would be undone by the next thing they typed: the
   * suggested name is recomputed as the draft changes, and a shape that needs
   * no word would quietly drop the one they just rolled.
   */
  let nicknamed = $state(false);

  // The name follows the shape as the operator fills it in: a name generated
  // before any host had connected must not go on claiming the pool is 2 vCPU
  // Ubuntu after they have said 8 vCPU Debian, and the pools already in the
  // fleet decide whether this one needs a spaniel to be told from them.
  $effect(() => {
    if (editing) return;
    const suggested = nicknamed
      ? nicknamedPoolName(kennelWord, draft, fleet.hosts, defaults)
      : poolName(kennelWord, draft, fleet.hosts, poolNames, defaults);
    untrack(() => {
      if (draft.name !== '' && draft.name !== autoName) return;
      draft.name = suggested;
      autoName = suggested;
    });
  });

  $effect(() => {
    if (editing) return;
    const suggested = brandedLabel(draft.name);
    untrack(() => {
      if (autoLabel === null) return;
      const pristine =
        autoLabel === '' ? draft.labels.length === 0 : draft.labels.join() === autoLabel;
      if (!pristine) {
        autoLabel = null;
        return;
      }
      // A name that reduces to the brand alone says nothing the server does not
      // already add on save, so there is nothing worth filling in yet.
      if (suggested === BRAND_LABEL) return;
      if (draft.labels.join() === suggested) return;
      draft.labels = [suggested];
      autoLabel = suggested;
    });
  });

  /**
   * Roll a name from the kennel, whatever is in the field now.
   *
   * The suggested name carries a spaniel only when the shape cannot tell this
   * pool from another, so the dice ask for one outright: pressing it on a pool
   * whose shape is already unique has to change something, or it reads as a
   * broken button.
   */
  function spin(): void {
    if (editing) return;
    nicknamed = true;
    kennelWord = spinWord(kennelWord);
    const next = nicknamedPoolName(kennelWord, draft, fleet.hosts, defaults);
    draft.name = next;
    autoName = next;
    touch('name');
  }

  /* -- the server's verdict, before anything is created --------------------- */

  $effect(() => {
    // As the draft changes, and from the first render: every section edits
    // something the controller's count depends on -- which hosts, which
    // backend, how big a runner is -- so the answer belongs beside the setting
    // that changes it, while there is still a reason to change it.
    void stabilityRevision;
    const payload = body;
    const controller = new AbortController();
    validating = true;
    const timer = setTimeout(() => {
      validatePool(payload, pool?.id, controller.signal)
        .then((result) => {
          verdict = result;
          validateError = null;
        })
        .catch((cause: unknown) => {
          if (cause instanceof DOMException && cause.name === 'AbortError') return;
          validateError = cause;
        })
        .finally(() => {
          validating = false;
        });
    }, 250);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  });

  /* -- submitting ------------------------------------------------------------ */

  function applyFieldErrors(cause: ApiError): void {
    const fields = cause.fieldErrors();
    serverErrors = fields;
    const first = Object.keys(fields)[0];
    const where = first === undefined ? null : sectionForField(first);
    if (where) void jump(where);
  }

  /**
   * The controller's refusal when an edit would leave this pool with no host
   * in the fleet that could run it. It is not a field error -- every figure on
   * the form is valid, and it is the machines that cannot keep up -- so it is
   * answered with the consequence stated rather than a red label on a slider.
   */
  let stranding = $state('');

  async function finish(confirm = false): Promise<void> {
    if (submitting) return;
    // Pressed while something is wrong: say what, and go there. The button is
    // never disabled for this, because a disabled button cannot explain itself.
    if (Object.keys(clientErrors).length > 0) {
      reveal();
      return;
    }
    submitting = true;
    try {
      const payload = toPoolBody(draft, { complete: editing });
      const saved =
        pool && pool.id
          ? await updatePool(
              pool.id,
              payload as Body<'updatePool'>,
              confirm ? { confirm: true } : undefined,
            )
          : await createPool(payload);
      toasts.success(
        editing ? `Saved ${payload.name}` : `Created ${payload.name}`,
        editing
          ? 'The scheduler picks the new settings up on its next pass.'
          : 'Runners appear as soon as a job asks for these labels.',
      );
      void fleet.reconcile();
      ondone(saved);
    } catch (cause) {
      if (cause instanceof ApiError && cause.isConflict) {
        stranding = cause.message;
        return;
      }
      if (cause instanceof ApiError) applyFieldErrors(cause);
      toasts.fromError(cause, editing ? 'The pool was not saved' : 'The pool was not created');
    } finally {
      submitting = false;
    }
  }
</script>

<div class="editor {className}">
  <PoolRail items={railItems} onjump={(id) => void jump(id)} />

  <div class="main">
    {#if editing}
      <div class="listing">
        <p class="eyebrow">Settings</p>
        <button type="button" class="expand" onclick={toggleAll}>
          {allOpen ? 'Collapse all' : 'Expand all'}
        </button>
      </div>
    {/if}

    <PoolSection
      {...sectionHead('basics')}
      summary={summaries.basics}
      bind:open={opened.basics}
      problems={problems.basics}
      edited={edited.has('basics')}
    >
      <BasicsSection
        {draft}
        {errors}
        {touch}
        {editing}
        {installations}
        loading={installationsLoading}
        error={installationsError}
        onretry={() => (installationsAttempt += 1)}
        {groups}
        {groupsLoading}
        {groupsError}
        onspin={editing ? undefined : spin}
        hostsKnown={fleet.loaded}
        {dindHosts}
        onopen={(id) => void jump(id)}
      />
    </PoolSection>

    {#if !editing}
      <div class="listing">
        <div>
          <p class="eyebrow">Fine-tune (optional)</p>
          <p class="sub">Each of these already has an answer that suits most pools.</p>
        </div>
        <button type="button" class="expand" onclick={toggleAll}>
          {allOpen ? 'Collapse all' : 'Expand all'}
        </button>
      </div>
    {/if}

    <PoolSection
      {...sectionHead('hosts')}
      summary={summaries.hosts}
      bind:open={opened.hosts}
      problems={problems.hosts}
      edited={edited.has('hosts')}
    >
      <HostsSection
        {draft}
        {touch}
        hosts={fleet.hosts}
        hostsKnown={fleet.loaded}
        {verdict}
        {validating}
      />
    </PoolSection>

    <PoolSection
      {...sectionHead('runner')}
      summary={summaries.runner}
      bind:open={opened.runner}
      problems={problems.runner}
      edited={edited.has('runner')}
    >
      <RunnerSection
        {draft}
        {errors}
        {touch}
        {offers}
        {platforms}
        hosts={fleet.hosts}
        hostsKnown={fleet.loaded}
        restricted={restrictedToHosts}
        bind:socketConfirmed
      />
    </PoolSection>

    <PoolSection
      {...sectionHead('size')}
      summary={summaries.size}
      bind:open={opened.size}
      problems={problems.size}
      edited={edited.has('size')}
    >
      <SizeSection {draft} {errors} {touch} {defaults} {verdict} {validating} />
    </PoolSection>

    <PoolSection
      {...sectionHead('scaling')}
      summary={summaries.scaling}
      bind:open={opened.scaling}
      problems={problems.scaling}
      edited={edited.has('scaling')}
    >
      <ScalingSection
        {draft}
        {errors}
        {touch}
        {verdict}
        {validating}
        following={followingMax}
        {fleetDefaults}
      />
    </PoolSection>

    <PoolSection
      {...sectionHead('speed')}
      summary={summaries.speed}
      bind:open={opened.speed}
      problems={problems.speed}
      edited={edited.has('speed')}
    >
      <SpeedSection {draft} {errors} {touch} {verdict} />
    </PoolSection>

    <PoolCheck
      {draft}
      {body}
      {editing}
      {installationLabel}
      {verdict}
      {validating}
      error={validateError}
      covered={Object.keys(errors)}
      onshow={(id) => void jump(id)}
      onfixed={() => stabilityRevision++}
    />

    <PoolActionBar
      tone={status.tone}
      message={status.text}
      actionLabel={status.action}
      onaction={reveal}
      submitLabel={editing ? 'Save changes' : 'Create pool'}
      canSubmit={dirty}
      busy={submitting}
      onsubmit={() => void finish()}
      {oncancel}
    />
  </div>
</div>

<ConfirmDialog
  bind:open={
    () => stranding !== '',
    (open) => {
      if (!open) stranding = '';
    }
  }
  title="Save a pool with nowhere to run?"
  name={draft.name}
  description={stranding}
  consequences={[
    'Nothing has been saved yet.',
    'Saved as it is, jobs with these labels queue until a host that fits joins the fleet.',
    'Adjusting a host to match, or asking for less here, is the other way out.',
  ]}
  confirmLabel="Save anyway"
  busy={submitting}
  onconfirm={async () => {
    await finish(true);
    return true;
  }}
/>

<style>
  .editor {
    display: grid;
    grid-template-columns: var(--z-settings-rail-width) minmax(0, 1fr);
    gap: var(--z-space-6);
    align-items: start;
  }
  .main {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    min-width: 0;
    max-width: 56rem;
  }
  .listing {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    justify-content: space-between;
    gap: var(--z-space-2) var(--z-space-4);
    margin-top: var(--z-space-3);
  }
  .eyebrow {
    margin: 0;
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-semibold);
    letter-spacing: var(--z-tracking-wide);
    text-transform: uppercase;
    color: var(--z-text-muted);
  }
  .sub {
    margin: var(--z-nudge-2) 0 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
  }
  .expand {
    padding: 0;
    border: 0;
    background: none;
    color: var(--z-accent);
    font: inherit;
    font-size: var(--z-text-sm);
    cursor: pointer;
    text-decoration: underline;
  }

  /* --z-bp-lg, written out: below it the rail is not drawn, and the sections
     are the way about. */
  @media (max-width: 1180px) {
    .editor {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
