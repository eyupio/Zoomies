<!--
  A repository's AI Context, on the repository's own page.

  It is the card the AI Context page lists, found by the pair Kennel Club already
  holds for the repository: the installation and GitHub's ID for it. It adds no
  switch and no pause. AI Context is not something that is on or off for a
  repository; it is set up by a reviewed pull request and removed by another, and
  this tab leaves both where they are.

  The API answers 404 to somebody who may not configure the repository's
  installation, whether or not the repository has AI Context, so that a person
  cannot learn what they were not told. The tab has to be as careful: for such a
  person "no record" is not "not set up", and it says what it cannot know, and
  offers nothing they could not do.
-->
<script lang="ts">
  import { BookOpenText } from '@lucide/svelte';
  import { ApiError, findAIContextDraft, listContextInstallations } from '$lib/api/client';
  import type { AIContextRepository, KennelRepository } from '$lib/api/types';
  import AiContextCard from '$lib/aicontext/AiContextCard.svelte';
  import type { KnownInstallation } from '$lib/aicontext/types';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';

  interface Props {
    repo: KennelRepository;
  }

  let { repo }: Props = $props();

  type Answer =
    | { kind: 'loading' }
    | { kind: 'found'; item: AIContextRepository }
    // The API's 404 (or 403): there is no record, or this person may not be told.
    | { kind: 'silent' }
    | { kind: 'failed'; cause: unknown };

  let answer = $state<Answer>({ kind: 'loading' });
  let installations = $state<KnownInstallation[]>([]);
  let reload = $state(0);

  // Asked of the pair and not of the page, so a different repository is a new question.
  const installationId = $derived(repo.installation_id);
  const repositoryId = $derived(repo.repository_id);

  // The installations this person may configure: all of them for an administrator,
  // the ones they own otherwise. Not knowing is treated as owning none, which is the
  // safe side, as it is on the AI Context page.
  const mayConfigure = $derived(installations.some((i) => i.id === installationId));

  $effect(() => {
    void reload;
    const installation = installationId;
    const repository = repositoryId;
    const controller = new AbortController();
    answer = { kind: 'loading' };
    void listContextInstallations(controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) installations = result.items ?? [];
      })
      .catch(() => {
        if (!controller.signal.aborted) installations = [];
      });
    void findAIContextDraft(installation, repository, controller.signal)
      .then((item) => {
        if (!controller.signal.aborted) answer = { kind: 'found', item };
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        answer =
          cause instanceof ApiError && (cause.status === 404 || cause.status === 403)
            ? { kind: 'silent' }
            : { kind: 'failed', cause };
      });
    return () => controller.abort();
  });
</script>

{#if answer.kind === 'loading'}
  <Skeleton lines={5} />
{:else if answer.kind === 'failed'}
  <ErrorState
    error={answer.cause}
    title="AI Context could not be read"
    onretry={() => (reload += 1)}
  />
{:else if answer.kind === 'found'}
  <AiContextCard
    item={answer.item}
    {installations}
    named={false}
    onchange={(updated) => (answer = { kind: 'found', item: updated })}
  />
{:else if mayConfigure}
  <EmptyState
    icon={BookOpenText}
    title="AI Context is not set up for this repository"
    description="Set it up to give AI assistants this repository's source as a verified pack that is refreshed after every push. Setup opens on this repository's installation, where you choose the repository and review the pull request before anything is published."
  >
    <Button
      variant="primary"
      href="/kennel/ai-context/setup?installation_id={encodeURIComponent(installationId)}"
      >Set up AI Context</Button
    >
  </EmptyState>
{:else}
  <EmptyState
    icon={BookOpenText}
    title="Zoomies cannot say whether AI Context is set up here"
    description="Setting it up, and seeing how it is doing, needs an administrator or the owner of this repository's installation. If you have been given access to its source, it is listed under AI Context."
  >
    <Button href="/kennel/ai-context">Open AI Context</Button>
  </EmptyState>
{/if}
