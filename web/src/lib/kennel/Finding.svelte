<!--
  One finding: how bad it is, what is wrong, what to change, and the pools or
  runs it is about.

  The title, detail and fix are sentences the evaluator wrote from fixed
  templates, with only integers and enumerated words in them. The evidence is
  the one place a name somebody chose reaches this page, so it is rendered as
  text and nowhere else, and is linked only when it is an identifier the
  controller made.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { KennelFinding } from '$lib/api/types';
  import Badge from '$lib/components/Badge.svelte';
  import RemedyText from '$lib/components/RemedyText.svelte';
  import { severityStatus } from '$lib/status';

  interface Props {
    finding: KennelFinding;
    /** What can be done about it: the buttons that apply. */
    actions?: Snippet;
  }

  let { finding, actions }: Props = $props();

  // The same gate the controller applies, applied again: a link is built from
  // text, so it is built only from text that has the shape of what it links to.
  const POOL_ID = /^pool_[A-Za-z0-9]{1,40}$/;

  const evidence = $derived(
    (finding.evidence ?? [])
      .filter((item) => item.ref || item.label)
      .map((item, index) => ({
        key: `${index}:${item.kind}:${item.ref}`,
        kind: item.kind === 'pool' ? 'Pool' : 'Run',
        text: item.label || item.ref,
        href: item.kind === 'pool' && POOL_ID.test(item.ref) ? `/pools/${item.ref}` : undefined,
      })),
  );
</script>

<article class="finding" data-severity={finding.severity} aria-label={finding.title}>
  <header>
    <Badge status={severityStatus(finding.severity)} size="sm" />
    <h3>{finding.title}</h3>
  </header>
  <p class="detail"><RemedyText text={finding.detail} /></p>
  <div class="fix">
    <h4>What to change</h4>
    <p><RemedyText text={finding.fix} /></p>
  </div>
  {#if evidence.length > 0}
    <div class="evidence">
      <h4>Where it was seen</h4>
      <ul>
        {#each evidence as item (item.key)}
          <li>
            <span class="kind">{item.kind}</span>
            {#if item.href}<a href={item.href}>{item.text}</a>{:else}<span>{item.text}</span>{/if}
          </li>
        {/each}
      </ul>
    </div>
  {/if}
  <footer>
    <code class="code">{finding.code}</code>
    {#if finding.subject}<span class="subject">About {finding.subject}</span>{/if}
    {#if actions}<div class="actions">{@render actions()}</div>{/if}
  </footer>
</article>

<style>
  .finding {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    padding: var(--z-space-5);
    border: var(--z-border-width) solid var(--z-border);
    border-left-width: var(--z-border-width-rail);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  .finding[data-severity='error'] {
    border-left-color: var(--z-danger);
  }
  .finding[data-severity='warning'] {
    border-left-color: var(--z-pending);
  }
  .finding[data-severity='info'] {
    border-left-color: var(--z-accent);
  }
  header {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-3);
  }
  h3 {
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  h4 {
    margin: 0 0 var(--z-space-1);
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    text-transform: uppercase;
    letter-spacing: var(--z-tracking-wide);
    color: var(--z-text-muted);
  }
  p {
    margin: 0;
    max-width: 75ch;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
  }
  .detail {
    color: var(--z-text-muted);
  }
  ul {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  li {
    display: inline-flex;
    align-items: baseline;
    gap: var(--z-space-2);
    padding: var(--z-nudge-2) var(--z-space-2);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    font-size: var(--z-text-xs);
    overflow-wrap: anywhere;
  }
  .kind {
    color: var(--z-text-subtle);
    text-transform: uppercase;
    font-size: var(--z-text-2xs);
    letter-spacing: var(--z-tracking-wide);
  }
  footer {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-3);
  }
  .code {
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .subject {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .actions {
    margin-left: auto;
    display: flex;
    gap: var(--z-space-2);
  }
</style>
