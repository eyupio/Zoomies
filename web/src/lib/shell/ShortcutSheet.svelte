<!--
  The shortcut sheet, opened with `?`. It is the single written description of
  the key map, generated from the same table the shortcuts are installed from.
-->
<script lang="ts">
  import { SHORTCUTS } from '../keys';
  import Dialog from '../components/Dialog.svelte';

  interface Props {
    open?: boolean;
  }

  let { open = $bindable(false) }: Props = $props();
</script>

<Dialog
  bind:open
  title="Keyboard shortcuts"
  description="Everything here works from any page."
  size="md"
>
  <div class="groups">
    {#each SHORTCUTS as group (group.title)}
      <section>
        <h3>{group.title}</h3>
        <dl>
          {#each group.items as item (item.description)}
            <div class="row">
              <dt>
                {#each item.keys as key (key)}<kbd>{key}</kbd>{/each}
              </dt>
              <dd>{item.description}</dd>
            </div>
          {/each}
        </dl>
      </section>
    {/each}
  </div>
</Dialog>

<style>
  .groups {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: var(--z-space-6);
    padding-bottom: var(--z-space-4);
  }
  h3 {
    margin: 0 0 var(--z-space-3);
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-semibold);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--z-text-subtle);
  }
  dl {
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
  }
  .row {
    display: flex;
    align-items: baseline;
    gap: var(--z-space-3);
  }
  dt {
    flex: none;
    display: flex;
    gap: 2px;
    min-width: 74px;
  }
  dd {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
  }
  kbd {
    padding: 1px var(--z-space-1);
    border: 1px solid var(--z-border);
    border-bottom-width: 2px;
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-2xs);
    color: var(--z-text);
  }
</style>
