<!--
  The quiet line at the foot of every signed-in page: which product this is,
  which build of it is running, and what it does.

  It exists because the shell is otherwise anonymous once you are past the
  sign-in screen -- the sidebar masthead is the only mark on the page, and it is
  gone entirely when the navigation is collapsed or the window is a phone. A
  footer is also the honest place for a version string: an operator reading a
  bug report needs it on whatever page they happen to be looking at, not only in
  Settings.

  It stays a hairline. This is an operations dashboard, and nothing down here
  may compete with the fleet above it.

  Its two outbound links are also the only things in the signed-in shell that
  say where Zoomies came from and who makes it. Somebody reading this page is
  usually looking at a controller a colleague installed, so both earn their
  place -- see docs/brand.md. The credit matches the one in the footer of every
  page on zoomies.sh, so the product and its site read as one thing.
-->
<script lang="ts">
  import { session } from '../state/session.svelte';
  import { DEVELOPER_NAME, DEVELOPER_URL, QUICKSTART_URL } from '../links';
  import Logo from '../components/Logo.svelte';

  const version = $derived(session.meta?.version);
  const versionChannel = $derived(session.meta?.version_channel);
</script>

<footer class="app-footer">
  <div class="inner">
    <span class="brand">
      <Logo variant="mark" size={16} label="" />
      <span class="name">Zoomies</span>
      {#if version}
        <span class="version" title="The controller build this page is talking to">{version}</span>
      {/if}
      {#if versionChannel}
        <span class="channel" title="The published channel carrying Zoomies build"
          >:{versionChannel}</span
        >
      {/if}
    </span>
    <span class="right">
      <!-- Both of these leave the product, so both open in a new tab: an
           operator watching a fleet should not lose the page they were on to
           go and read what a runner group is. They are hyperlinks, not
           fetches, so an air-gapped install is unaffected -- the link simply
           does not resolve, which is the same as it being absent. -->
      <a class="docs" href={QUICKSTART_URL} target="_blank" rel="noopener noreferrer">
        Docs<span class="sr-only"> (opens in a new tab)</span>
      </a>
      <span class="credit">
        Developed by
        <a href={DEVELOPER_URL} target="_blank" rel="noopener noreferrer">
          {DEVELOPER_NAME}<span class="sr-only"> (opens in a new tab)</span>
        </a>
      </span>
      <span class="descriptor">Self-hosted Git runners</span>
    </span>
  </div>
</footer>

<style>
  .app-footer {
    color: var(--z-text-subtle);
    font-size: var(--z-text-2xs);
  }
  /*
    The measure is set on an inner element, not on the footer itself: an auto
    inline margin on a flex item shrinks it to its content and centres it, and
    the shell's column is a flex column.
  */
  .inner {
    display: flex;
    align-items: center;
    justify-content: space-between;
    /*
      Wrap, never overflow. Between the phone breakpoint and a comfortably wide
      window the version, both links and the descriptor are wider than the
      column, and every one of them refuses to shrink, so the row used to size
      the whole document and the page scrolled sideways for the sake of a
      footer. Rows of their own are the honest answer.
    */
    flex-wrap: wrap;
    gap: var(--z-space-2) var(--z-space-4);
    max-width: var(--z-content-max);
    margin: 0 auto;
    padding: var(--z-space-4) var(--z-space-6) var(--z-space-8);
  }
  .brand {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    min-width: 0;
  }
  .name {
    font-weight: var(--z-weight-semibold);
    letter-spacing: -0.005em;
    color: var(--z-text-muted);
  }
  .version {
    font-family: var(--z-font-mono);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .channel {
    padding: 0 var(--z-space-2);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-full);
    color: var(--z-text-muted);
    font-family: var(--z-font-mono);
    white-space: nowrap;
  }
  .descriptor {
    flex: none;
    letter-spacing: var(--z-tracking-wider);
    text-transform: uppercase;
  }
  @media (max-width: 768px) {
    .inner {
      /* Clear of the navigation bar, which is fixed to the bottom edge here. */
      padding: var(--z-space-4) var(--z-space-3) calc(var(--z-space-16) + var(--z-safe-bottom));
    }
    .descriptor {
      display: none;
    }
  }
  .right {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-1) var(--z-space-4);
    min-width: 0;
  }
  .docs,
  .credit a {
    color: var(--z-text-subtle);
  }
  .docs:hover,
  .credit a:hover,
  .credit a:focus-visible {
    color: var(--z-text-muted);
    text-decoration: underline;
    text-underline-offset: var(--z-underline-offset);
  }
  .credit {
    flex: none;
    white-space: nowrap;
  }
  .credit a {
    font-weight: var(--z-weight-medium);
  }
</style>
