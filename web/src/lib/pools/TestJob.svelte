<!--
  A job to run before there is a real one.

  Activation is a job running on a runner this fleet started, and the only ways
  to reach it were editing a real repository's workflow or running the migration
  wizard, which opens pull requests. Nobody should have to do either to find out
  whether what they have just installed works. This is a complete workflow that
  touches nothing of theirs, with the three things to do with it spelled out.

  It is a disclosure rather than a panel because it appears in two places that
  want different things of it: the setup checklist, where it is the next action
  and arrives open, and a pool's page, which an operator visits for other
  reasons and which should not grow by a screen.
-->
<script lang="ts">
  import CopyButton from '$lib/components/CopyButton.svelte';
  import { TEST_JOB_FILE, TEST_JOB_NAME, testJobWorkflow } from './test-job';

  interface Props {
    /** The pool's labels, so the file asks for this pool and no other. */
    labels?: readonly string[];
    /** Open on arrival. */
    open?: boolean;
    class?: string;
  }

  let { labels = [], open = false, class: className = '' }: Props = $props();

  const workflow = $derived(testJobWorkflow(labels));
</script>

<details class="testjob {className}" {open}>
  <summary>Or run a test job first</summary>
  <div class="body">
    <p>
      A complete workflow that touches nothing of yours: it prints the runner's name, waits five
      seconds and stops. Add it to any repository the GitHub App can see.
    </p>
    <figure>
      <figcaption>
        <code>{TEST_JOB_FILE}</code>
        <CopyButton value={workflow} label="Copy the test workflow" showLabel />
      </figcaption>
      <!--
        The scroll container is a focusable group, as the diff viewer's is: on a
        phone the longest line is wider than the card, and a region a finger can
        scroll and a keyboard cannot is a WCAG 2.1.1 failure.
      -->
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="scroll" tabindex="0" role="group" aria-label="The test workflow">
        <pre><code>{workflow}</code></pre>
      </div>
    </figure>
    <ol>
      <li>
        Commit it to the repository's default branch. GitHub only offers <strong
          >Run workflow</strong
        > for a file there.
      </li>
      <li>
        Open the repository's <strong>Actions</strong> tab, choose
        <strong>{TEST_JOB_NAME}</strong> and press <strong>Run workflow</strong>.
      </li>
      <li>
        Come back here. The job queues, a runner starts for it, and this page shows how long that
        took.
      </li>
    </ol>
  </div>
</details>

<style>
  .testjob {
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  summary {
    padding: var(--z-space-2) var(--z-space-3);
    cursor: pointer;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    padding: 0 var(--z-space-3) var(--z-space-3);
  }
  p,
  ol {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  /* Numbered, because the order is the instruction. Not a flex column: a list
     item that is a flex item stops being a list item, and loses its number. */
  ol {
    padding-left: var(--z-space-5);
    list-style: decimal;
  }
  li + li {
    margin-top: var(--z-space-1);
  }
  strong {
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
  }
  figure {
    margin: 0;
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
    overflow: hidden;
  }
  /* Wraps rather than squeezing: on a phone the file name and the button do not
     fit on one line, and a path broken into five pieces is unreadable. */
  figcaption {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-2);
    padding: var(--z-space-2) var(--z-space-3);
    border-bottom: var(--z-border-width) solid var(--z-border);
    font-size: var(--z-text-2xs);
    color: var(--z-text-muted);
  }
  figcaption code {
    min-width: 0;
    font-family: var(--z-font-mono);
  }
  .scroll {
    overflow-x: auto;
  }
  pre {
    margin: 0;
    padding: var(--z-space-3);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
  }
</style>
