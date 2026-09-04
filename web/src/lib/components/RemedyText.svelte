<!--
  A sentence from the server that contains commands, rendered so the commands
  can be run rather than retyped.

  The agent writes the fix for an unusable socket as prose with the exact
  commands in backticks -- `sudo usermod -aG docker zoomies` -- because it knows
  the account, the group and the unit, and the operator should not have to
  reconstruct any of them. Shown as flat text that is a paragraph to squint at;
  shown like this it is a command with a copy button next to it.

  Anything without backticks renders exactly as it arrived, so this is safe to
  use on every server sentence whether or not it carries a command.
-->
<script lang="ts">
  import CopyButton from './CopyButton.svelte';

  interface Props {
    text: string;
    /** Show a copy button beside each command. */
    copyable?: boolean;
    class?: string;
  }

  let { text, copyable = true, class: className = '' }: Props = $props();

  interface Part {
    /** A stable key: the same command can legitimately appear twice. */
    id: string;
    text: string;
    command: boolean;
  }

  /**
   * Split on backtick-quoted runs. An unmatched trailing backtick is left as
   * text: half a command is not something to offer for copying.
   */
  function parts(input: string): Part[] {
    const out: Part[] = [];
    const pattern = /`([^`\n]+)`/g;
    let last = 0;
    let match: RegExpExecArray | null;
    while ((match = pattern.exec(input)) !== null) {
      if (match.index > last) {
        out.push({ id: `t${last}`, text: input.slice(last, match.index), command: false });
      }
      out.push({ id: `c${match.index}`, text: match[1] ?? '', command: true });
      last = match.index + match[0].length;
    }
    if (last < input.length) out.push({ id: `t${last}`, text: input.slice(last), command: false });
    return out;
  }

  const pieces = $derived(parts(text));
</script>

<span class="remedy {className}"
  >{#each pieces as piece (piece.id)}{#if piece.command}<span class="command"
        ><code>{piece.text}</code>{#if copyable}<CopyButton
            value={piece.text}
            label="Copy the command {piece.text}"
          />{/if}</span
      >{:else}{piece.text}{/if}{/each}</span
>

<style>
  .remedy {
    overflow-wrap: anywhere;
  }
  .command {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    vertical-align: baseline;
  }
  code {
    padding: 0 var(--z-space-1);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    color: var(--z-text);
    font-size: var(--z-text-xs);
    white-space: pre-wrap;
  }
</style>
