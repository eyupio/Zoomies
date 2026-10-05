<!--
  What the controller makes of the pool, before anything is created.

  This used to be the last of eight steps, which meant the one thing an
  operator most needed to know -- that a pool no host can run would never make a
  runner -- was behind seven screens of other things, and a first-time reader
  who pressed Create early never saw it. It is on the page now, under the
  sections and above the button that saves, and the sticky bar beside the button
  says the same thing in a line.

  The point is unchanged: the server's opinion arrives *before* the pool exists.
  A pool that no host can run is a pool that will never make a runner, and
  finding that out afterwards costs an operator an hour of staring at an empty
  queue. What the hosts can hold of it is on the sections that change it -- the
  hosts, the size and the count -- so it is not repeated here.

  Every setting, written out the way the pool's own page writes them, is behind
  a disclosure for the operator who wants the whole of it in one place.
-->
<script lang="ts">
  import { CircleCheck, TriangleAlert } from '@lucide/svelte';
  import type { Pool, PoolCreate, Result } from '$lib/api/types';
  import Button from '$lib/components/Button.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import PoolConfig from './PoolConfig.svelte';
  import PoolFit from './PoolFit.svelte';
  import PoolMore from './PoolMore.svelte';
  import PoolStartupStability from './PoolStartupStability.svelte';
  import PoolWarnings from './PoolWarnings.svelte';
  import { sectionDef, sectionForField } from './sections';
  import type { SectionId } from './sections';
  import { visibleWarnings } from './verdict';
  import { FIELD_LABELS } from './vocabulary';
  import type { PoolDraft } from './draft';

  interface Props {
    draft: PoolDraft;
    body: PoolCreate;
    editing: boolean;
    installationLabel: string;
    verdict: Result<'validatePool'> | null;
    validating: boolean;
    error: unknown;
    /**
     * The fields the browser is already reporting beside their controls, so the
     * controller's refusal of the same thing is not said a second time.
     */
    covered: readonly string[];
    /** Open the section that can fix a refusal. */
    onshow: (id: SectionId, field?: string) => void;
    /** Bumped when a startup recommendation was applied, so the dry run is asked again. */
    onfixed: () => void;
  }

  let {
    draft,
    body,
    editing,
    installationLabel,
    verdict,
    validating,
    error,
    covered,
    onshow,
    onfixed,
  }: Props = $props();

  // The image is the server's answer where there is one: a pool that gives
  // its jobs a daemon runs the stock image's Docker variant, and this exists to
  // show the pool that will be made rather than the one typed.
  //
  // A pool that names no image -- which is every pool the quick path makes --
  // stores nothing, so the answer arrives as the effective one instead, and is
  // rendered as what it is: an image chosen for this pool rather than typed
  // into it. Without it the page that says what will be made said nothing about
  // the image, including for the pool that has just asked for a Docker daemon.
  const preview = $derived<Pool>({
    ...body,
    image: verdict?.image ?? body.image,
    effective_image: verdict?.effective_image,
    installation_target: installationLabel,
  });

  // What the controller refuses that the browser did not already say: the name
  // of a pool that exists, a host selector nothing answers. The rest is beside
  // the controls it is about, and listing it twice would be the same sentence on
  // two screens.
  const fieldErrors = $derived(
    (verdict?.errors ?? []).filter((issue) => !covered.includes(issue.field)),
  );
  const warnings = $derived(verdict?.warnings ?? []);
  const matching = $derived(verdict?.matching_hosts);
  const excluded = $derived(verdict?.excluded_hosts ?? []);
  const otherWarnings = $derived(visibleWarnings(verdict));
  const happy = $derived(
    verdict !== null &&
      verdict.valid &&
      fieldErrors.length === 0 &&
      warnings.length === 0 &&
      excluded.length === 0 &&
      (matching === undefined || matching > 0),
  );

  let everyOpen = $state(false);

  function label(field: string): string {
    return FIELD_LABELS[field] ?? field;
  }
</script>

<section class="check" aria-label="The controller's check">
  <h2>Checked against the controller</h2>

  {#if error}
    <ErrorState
      {error}
      title="This pool could not be checked"
      compact
      description="The controller did not answer the dry run, so the warnings below may be incomplete. Creating the pool will still be validated on the server."
    />
  {:else if validating && verdict === null}
    <div class="checking" aria-busy="true">
      <Skeleton width="45%" height="0.9rem" />
      <Skeleton lines={2} />
    </div>
  {:else if verdict}
    <PoolFit {verdict} {validating} />

    {#if fieldErrors.length > 0}
      <div class="errors" role="group" aria-label="What the controller rejected">
        <p class="errors-title">
          <TriangleAlert size={15} aria-hidden="true" />
          The controller will not accept this pool yet
        </p>
        <ul>
          {#each fieldErrors as issue (issue.field + issue.message)}
            {@const where = sectionForField(issue.field)}
            <li>
              <span class="field">{label(issue.field)}</span>
              <span class="message">{issue.message}</span>
              {#if where}
                <Button size="sm" variant="ghost" onclick={() => onshow(where, issue.field)}>
                  Show in {sectionDef(where).title}
                </Button>
              {/if}
            </li>
          {/each}
        </ul>
      </div>
    {/if}

    {#if otherWarnings.length > 0}
      <div class="warnings">
        <p class="warnings-title">
          This pool will be {editing ? 'saved' : 'created'} with warnings
        </p>
        <PoolWarnings warnings={otherWarnings} bare />
      </div>
    {/if}

    {#if happy}
      <p class="ok">
        <CircleCheck size={15} aria-hidden="true" />
        Nothing to flag. The controller accepts this pool as it stands.
      </p>
    {/if}
  {/if}

  {#if draft.sizing !== 'fixed' && (draft.backend === 'docker' || draft.backend === 'podman')}
    <PoolStartupStability warnings={verdict?.warnings ?? []} {onfixed} />
  {/if}

  {#if !editing}
    <Checkbox
      bind:checked={draft.enabled}
      label="Enable this pool as soon as it is created"
      description="A disabled pool makes no runners. Leave it off if you want to look it over first."
    />
  {/if}

  <PoolMore
    title="Every setting, as it will be {editing ? 'saved' : 'created'}"
    note="the pool's own page, before it exists"
    bind:open={everyOpen}
  >
    <section class="summary" aria-label="What will be {editing ? 'saved' : 'created'}">
      <h3>{body.name || 'This pool'}</h3>
      <PoolConfig pool={preview} showId={false} />
    </section>
  </PoolMore>
</section>

<style>
  .check {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    min-width: 0;
    padding: var(--z-space-5);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  h2 {
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  h3 {
    margin: 0 0 var(--z-space-3);
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .checking {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
  }
  .errors {
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-danger-border);
    border-radius: var(--z-radius-md);
    background: var(--z-danger-subtle);
  }
  .errors-title {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0 0 var(--z-space-2);
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-danger);
  }
  .errors ul {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .errors li {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    font-size: var(--z-text-base);
  }
  .field {
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
  }
  .message {
    color: var(--z-text-muted);
    overflow-wrap: anywhere;
  }
  .warnings-title {
    margin: 0 0 var(--z-space-2);
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    color: var(--z-text-muted);
  }
  .ok {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-base);
    color: var(--z-idle);
  }
  .summary {
    min-width: 0;
  }
  .summary :global(code) {
    overflow-wrap: anywhere;
  }

  /* --z-bp-md, written out: a media query is evaluated before custom
     properties exist. */
  @media (max-width: 768px) {
    .check {
      padding: var(--z-space-4);
    }
  }
</style>
