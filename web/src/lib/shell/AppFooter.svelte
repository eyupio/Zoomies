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

  Its one outbound link is also the only thing in the signed-in shell that says
  where Zoomies came from. Somebody reading this page is usually looking at a
  controller a colleague installed, so the link earns its place twice over --
  see docs/brand.md.
-->
<script lang="ts">
  import { session } from '../state/session.svelte';
  import { QUICKSTART_URL } from '../links';
  import Logo from '../components/Logo.svelte';

  const version = $derived(session.meta?.version);
</script>

<footer class="app-footer">
  <div class="inner">
    <span class="brand">
      <Logo variant="mark" size={16} label="" />
      <span class="name">Zoomies</span>
      {#if version}
        <span class="version" title="The controller build this page is talking to">{version}</span>
      {/if}
    </span>
    <span class="right">
      <!-- The only link out of the product. An operator in their first fifteen
           minutes who wants to know what a runner group is, or what the
           host-socket option costs them, had to leave and guess a URL. It is a
           hyperlink, not a fetch, so an air-gapped install is unaffected. -->
      <a class="docs" href={QUICKSTART_URL} rel="noreferrer">Docs</a>
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
    gap: var(--z-space-4);
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
  .descriptor {
    flex: none;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  @media (max-width: 768px) {
    .inner {
      /* Clear of the navigation bar, which is fixed to the bottom edge here. */
      padding: var(--z-space-4) var(--z-space-3) var(--z-space-16);
    }
    .descriptor {
      display: none;
    }
  }
  .right {
    display: flex;
    align-items: center;
    gap: var(--z-space-4);
  }
  .docs {
    color: var(--z-text-subtle);
  }
  .docs:hover {
    color: var(--z-text-muted);
  }
</style>
