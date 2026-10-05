<!--
  Events: what the Overview's feed is allowed to tell you.

  One switch per category, each row saying what an entry of that kind is, so
  the choice is made against the thing itself rather than against a word. The
  categories that have no history to show -- the ones the feed can only fill
  from the live stream -- say so, because a category that is on and empty
  otherwise looks exactly like one that is broken.

  What goes right is a category as much as what goes wrong, and both are on by
  default: the ones that are off to begin with are off because something else
  in the product already carries them, not because they are good news.

  It is this browser's choice, like the theme and the dismissed problems: what
  belongs on one operator's dashboard is not a fleet setting, and nothing here
  changes what the API, `zoomies status` or an alerting rule reports.
-->
<script lang="ts">
  import { feed } from '$lib/state/feed.svelte';
  import { FEED_GROUPS, type FeedCategoryID } from '$lib/feed/categories';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Switch from '$lib/components/Switch.svelte';

  const categories = $derived(feed.categories);
  const on = $derived(categories.filter((row) => row.on).length);
  /*
    Under headings rather than in one long list, and the headings are only
    a way to read them: nothing branches on a group, and a category is what it
    reports, not where it is printed.
  */
  const groups = $derived(
    FEED_GROUPS.map((label) => ({
      label,
      rows: categories.filter((row) => row.category.group === label),
    })).filter((group) => group.rows.length > 0),
  );
  const uid = $props.id();
</script>

<PageHeader
  title="Events"
  subtitle="Which of the fleet's events the Overview's feed shows. Kept in this browser, and never anything the controller reports elsewhere."
/>

{#each groups as group, i (group.label)}
  <section class="group" aria-labelledby="feed-group-{uid}-{i}">
    <h2 id="feed-group-{uid}-{i}">{group.label}</h2>
    <div class="settings">
      {#each group.rows as row (row.category.id)}
        <div class="setting">
          <div class="text">
            <p class="label">
              <row.category.icon size={14} aria-hidden="true" />
              {row.category.label}
            </p>
            <p class="description">
              {row.category.description}
              {#if !row.category.history}
                <span class="live"
                  >Shown from the moment it happens; this one has no past to load.</span
                >
              {/if}
            </p>
          </div>
          <Switch
            label={row.category.label}
            hideLabel
            checked={row.on}
            onchange={(want) => feed.setShown(row.category.id as FeedCategoryID, want)}
          />
        </div>
      {/each}
    </div>
  </section>
{/each}

<p class="note">
  {on} of {categories.length} kinds are on. The feed keeps the last few hundred entries this tab has seen
  and shows the newest twelve; everything in it also lives on the page it is about, which is where the
  whole of it is — every decision on the pool, every failure on the runner, every recorded action on the
  Audit page.
</p>

<style>
  .group {
    margin-bottom: var(--z-space-5);
  }
  .group h2 {
    margin: 0 0 var(--z-space-2);
    padding: 0 var(--z-space-1);
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
    font-weight: var(--z-weight-medium);
    letter-spacing: var(--z-tracking-wide);
    text-transform: uppercase;
    color: var(--z-text-subtle);
  }
  .settings {
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  .setting {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--z-space-6);
    padding: var(--z-space-4) var(--z-space-5);
    border-bottom: var(--z-border-width) solid var(--z-border);
  }
  .setting:last-child {
    border-bottom: 0;
  }
  .text {
    flex: 1 1 20rem;
    min-width: 0;
  }
  .label {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text);
  }
  .description {
    margin: var(--z-nudge-2) 0 0;
    max-width: 64ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .live {
    color: var(--z-text-subtle);
  }
  /* The control sits on the label's line, not the description's. */
  .setting > :global(:last-child) {
    flex: none;
    margin-top: var(--z-nudge-1);
  }
  .note {
    margin: var(--z-space-4) var(--z-space-1) 0;
    max-width: 80ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-subtle);
  }
  @media (max-width: 768px) {
    .setting {
      flex-direction: column;
      gap: var(--z-space-3);
    }
    /*
      The basis above is a width for the row layout. Down here the main axis
      is vertical, so the same declaration asks for 20rem of *height*, and the
      switch ends up a screenful below the sentence it belongs to. In a column
      the text is as tall as the text.
    */
    .text {
      flex: initial;
    }
    /* Nothing to sit on a line with, so nothing to nudge against. */
    .setting > :global(:last-child) {
      margin-top: 0;
    }
  }
</style>
