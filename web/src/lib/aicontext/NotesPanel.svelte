<!--
  Notes an assistant published about one repository. A body is untrusted text
  an AI wrote after reading untrusted source, so it is shown as escaped,
  preformatted text and never rendered as HTML -- the Markdown stays readable
  and nothing in it can run, link or restyle the page.
-->
<script lang="ts">
  import { ApiError, getAIContextNote, listAIContextNotes } from '$lib/api/client';
  import type { AIContextNote } from '$lib/api/types';
  import { formatAbsolute } from '$lib/format';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';

  interface Props {
    repositoryId: string;
  }
  let { repositoryId }: Props = $props();
  let open = $state(false);
  let notes = $state<AIContextNote[] | null>(null);
  let failure = $state<unknown>(null);
  let notReader = $state(false);
  let reload = $state(0);
  let reading = $state<AIContextNote | null>(null);
  let readingSlug = $state<string | null>(null);
  let readFailure = $state<unknown>(null);

  $effect(() => {
    if (!open) return;
    void reload;
    const controller = new AbortController();
    failure = null;
    void listAIContextNotes(repositoryId, controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) notes = result.items;
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        // Notes are as private as the source: someone who may configure a
        // repository but is not one of its readers is told so, not shown an error.
        if (cause instanceof ApiError && cause.isNotFound) notReader = true;
        else failure = cause;
      });
    return () => controller.abort();
  });

  let readController: AbortController | null = null;

  async function read(slug: string): Promise<void> {
    // A slower reply for the note read before must never land under this
    // one's title, so each read cancels the last and checks it is still wanted.
    readController?.abort();
    readController = null;
    if (readingSlug === slug && reading) {
      reading = null;
      readingSlug = null;
      return;
    }
    const controller = new AbortController();
    readController = controller;
    readingSlug = slug;
    reading = null;
    readFailure = null;
    try {
      const note = await getAIContextNote(repositoryId, slug, controller.signal);
      if (!controller.signal.aborted && readingSlug === slug) reading = note;
    } catch (cause) {
      if (!controller.signal.aborted && readingSlug === slug) readFailure = cause;
    }
  }

  function via(note: AIContextNote): string {
    if (note.via_kind === 'connection')
      return note.via_name ? `via ${note.via_name}` : 'via an MCP connection';
    if (note.via_kind === 'token') return `via token ${note.via_name ?? ''}`.trim();
    return 'directly';
  }
</script>

<details class="notes" bind:open>
  <summary>Assistant notes</summary>
  <p class="muted">
    Reports and plans an AI assistant published about this repository, through a connection its
    owner allowed to publish. Read them as an assistant's opinion, not a reviewed document.
  </p>
  {#if notReader}
    <p class="muted">Only this repository's source readers can read its notes.</p>
  {:else if failure}
    <ErrorState error={failure} title="Notes could not be loaded" onretry={() => (reload += 1)} />
  {:else if notes === null}
    <Skeleton lines={2} />
  {:else if notes.length === 0}
    <p class="muted">No notes yet. An assistant publishes one with the context_publish tool.</p>
  {:else}
    <ul>
      {#each notes as note (note.slug)}
        <li>
          <div class="note-heading">
            <Badge tone="accent" size="sm" label="AI-written" dot={false} />
            <Badge size="sm" label={note.kind} dot={false} />
            <strong>{note.title}</strong>
          </div>
          <p class="meta">
            Version {note.version} by {note.author_name}
            {via(note)} · {formatAbsolute(note.created_at)}{#if note.source_commit}
              · written against <code>{note.source_commit.slice(0, 12)}</code>{/if}
          </p>
          <Button size="sm" variant="ghost" onclick={() => read(note.slug)}
            >{readingSlug === note.slug && reading ? 'Hide' : 'Read'}</Button
          >
          {#if readingSlug === note.slug}
            {#if readFailure}
              <ErrorState
                error={readFailure}
                title="This note could not be loaded"
                onretry={() => read(note.slug)}
              />
            {:else if reading}
              <pre aria-label="{note.title}, as written">{reading.body}</pre>
            {:else}
              <Skeleton lines={3} />
            {/if}
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</details>

<style>
  .notes {
    margin-top: var(--z-space-4);
  }
  .notes summary {
    cursor: pointer;
  }
  .muted,
  .meta {
    color: var(--z-text-subtle);
    font-size: var(--z-text-sm);
  }
  ul {
    list-style: none;
    padding: 0;
    margin: 0;
    display: grid;
    gap: var(--z-space-4);
  }
  .note-heading {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
  }
  .meta {
    margin: var(--z-space-1) 0 var(--z-space-2);
  }
  pre {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    font-size: var(--z-text-sm);
    background: var(--z-surface-sunken);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    padding: var(--z-space-3);
    max-height: 32rem;
    overflow: auto;
  }
</style>
