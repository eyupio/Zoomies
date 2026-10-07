<!--
  One configuration setting.

  The row answers four questions without being asked, because they are the four
  an operator has when a setting is not what they expected: what is it for,
  what is it set to, where did that value come from, and can I change it here.
  The last two are the ones that used to need a support conversation -- a value
  in the file and a value in the environment look identical once they are in the
  process, and "I changed it and nothing happened" is what that looks like from
  the outside.

  The editor is typed. A switch is a switch, a list is a list of chips, a choice
  is a menu. The row used to send whatever was in a text box, which for a
  boolean meant sending the string "on" and for a list meant sending the
  comma-joined display string back as a single value.

  Any finding the validator produced about this setting is shown right here: a
  warning three screens away from the thing it is about is a warning nobody
  acts on.
-->
<script lang="ts">
  import { Check, Pencil, RotateCcw, X } from '@lucide/svelte';
  import type { Problem, Setting } from '$lib/api/types';
  import { severityStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import IconButton from '$lib/components/IconButton.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import Tooltip from '$lib/components/Tooltip.svelte';
  import LabelInput from '$lib/pools/LabelInput.svelte';
  import QuantityField from '$lib/components/QuantityField.svelte';
  import { CPU_NOTCHES, withValue } from '$lib/pools/sizing';
  import { describeSource, displayValue, isUnset, settingQuantity } from './settings';
  import { goDuration, parseQuantity, type Quantity } from '$lib/units';

  const SECOND = 1000;
  const MINUTE = 60 * SECOND;
  const HOUR = 60 * MINUTE;
  const DAY = 24 * HOUR;
  /* The notches a setting's slider moves between, by what it measures. The
     field beside it takes any figure, and one off the notches joins them. */
  const NOTCHES: Record<Quantity, readonly number[]> = {
    cpus: CPU_NOTCHES,
    mb: [512, 1024, 2048, 4096, 8192, 16384, 32768, 65536, 131072],
    gb: [1, 2, 5, 10, 20, 50, 100, 200, 500, 1024],
    count: [1, 2, 3, 4, 5, 6, 8, 10, 12, 16, 20, 25, 30, 40, 50, 75, 100, 200, 500, 1000],
    ms: [
      SECOND,
      2 * SECOND,
      5 * SECOND,
      10 * SECOND,
      15 * SECOND,
      30 * SECOND,
      MINUTE,
      2 * MINUTE,
      5 * MINUTE,
      10 * MINUTE,
      15 * MINUTE,
      30 * MINUTE,
      HOUR,
      2 * HOUR,
      3 * HOUR,
      6 * HOUR,
      12 * HOUR,
      DAY,
      2 * DAY,
      3 * DAY,
      7 * DAY,
      14 * DAY,
      30 * DAY,
      60 * DAY,
      90 * DAY,
      180 * DAY,
      365 * DAY,
    ],
  };

  interface Props {
    setting: Setting;
    findings?: readonly Problem[];
    /**
     * This is the row a link asked for. It is marked rather than merely
     * scrolled to, because a page that jumps and then looks exactly as it did
     * leaves somebody hunting for what moved.
     */
    sought?: boolean;
    /** Returns an error message, or an empty string when the change was accepted. */
    onsave: (key: string, value: unknown) => Promise<string>;
    class?: string;
  }

  let { setting, findings = [], sought = false, onsave, class: className = '' }: Props = $props();

  /* The section is already the heading above, so the row shows what is left. */
  const leaf = $derived(setting.key.split('.').slice(1).join('.') || setting.key);
  const shown = $derived(displayValue(setting));
  const unset = $derived(isUnset(setting));
  const source = $derived(describeSource(setting));

  /*
    A secret is never sent to the browser, so there is nothing to put back and
    no default to compare against. Clearing one is still offered -- that is how
    a credential is removed.
  */
  const resettable = $derived(setting.stored === true && setting.editable === true);

  /*
    A value the environment is holding is the single most confusing state a
    configuration can be in -- the page shows one number, the file shows
    another, and nothing an operator does here changes either. So the row says
    so twice: in the badge, which names the variable, and down its edge, which
    is visible while scrolling past.
  */
  const pinned = $derived(setting.source === 'environment');

  let editing = $state(false);
  let saving = $state(false);
  let failure = $state('');

  /* One draft per kind, so each editor binds to something of its own type. */
  let text = $state('');
  let flag = $state(false);
  let list = $state<string[]>([]);
  let pairs = $state<string[]>([]);
  let amount = $state<number | null>(null);
  let unreadable = $state('');

  /* A size is edited as one: a slider for choosing and a field that reads
     4g, 4096mb or 1.5, written back as 4 GB. */
  const quantity = $derived(settingQuantity(setting));
  const amountNotches = $derived.by(() => {
    const base = quantity ? NOTCHES[quantity] : [];
    /* Zero joins the notches only where it is already an answer this
       setting gives -- the build cache target's "leave the daemon alone", a
       lifetime of 0s that means none. A count starts at it anyway. */
    const zero =
      quantity === 'count' || [setting.default, setting.value].some((v) => v === 0 || v === '0s');
    return withValue(zero ? [0, ...base] : [...base], amount ?? 0);
  });

  function start(): void {
    failure = '';
    unreadable = '';
    const value = setting.value;
    switch (setting.kind) {
      case 'bool':
      case 'optional_bool':
        flag = value === true;
        break;
      case 'strings':
        list = Array.isArray(value) ? [...(value as string[])] : [];
        break;
      case 'labels':
        pairs = Object.entries((value ?? {}) as Record<string, string>).map(
          ([k, v]) => `${k}=${v}`,
        );
        break;
      default:
        text = value === null || value === undefined ? '' : String(value);
        if (typeof value === 'number') amount = value;
        else if (typeof value === 'string' && quantity === 'ms') {
          const parsed = parseQuantity(value, 'ms');
          amount = parsed.ok ? parsed.value : null;
        } else amount = null;
    }
    editing = true;
  }

  function cancel(): void {
    editing = false;
    failure = '';
  }

  /** What the editor currently holds, in the shape the API takes. */
  function draft(): unknown {
    switch (setting.kind) {
      case 'bool':
      case 'optional_bool':
        return flag;
      case 'strings':
        return list;
      case 'labels': {
        const out: Record<string, string> = {};
        for (const pair of pairs) {
          const at = pair.indexOf('=');
          if (at > 0) out[pair.slice(0, at).trim()] = pair.slice(at + 1).trim();
        }
        return out;
      }
      case 'duration':
        // Go's spelling, which is what the controller reads: 7d goes as 168h.
        return amount === null ? text.trim() : goDuration(amount);
      case 'int':
      case 'float': {
        if (quantity) return amount;
        const n = Number(text.trim());
        return Number.isFinite(n) ? n : text.trim();
      }
      default:
        return text.trim();
    }
  }

  async function commit(): Promise<void> {
    // The field has already said what it could not read; saving now would
    // send the last value it could, which is not what was typed.
    if (unreadable) return;
    await send(draft());
  }

  /*
    Null is how the API is told to forget a setting, so the key falls back to
    whatever the configuration file or the built-in default says. It is the
    same word for every kind, which is what makes "put it back" one action
    rather than one per type.
  */
  async function reset(): Promise<void> {
    await send(null);
  }

  async function send(value: unknown): Promise<void> {
    saving = true;
    const message = await onsave(setting.key, value);
    saving = false;
    if (message) {
      failure = message;
      return;
    }
    editing = false;
  }

  /** A label editor is a list editor; the chips just happen to contain '='. */
  const labelPlaceholder = 'Type key=value, then press Enter';
</script>

<div
  class="row {className}"
  class:has-findings={findings.length > 0}
  class:sought
  id="setting-{setting.key}"
  class:awaiting-restart={setting.pending}
  class:pinned
>
  <div class="key">
    <span class="label">{setting.label}</span>
    <span class="leaf mono">{leaf}</span>
    {#if setting.summary}
      <p class="summary">
        {setting.summary}
        {#if setting.live === false && setting.editable && !setting.pending}
          <!--
            Most of the fleet's settings are applied when the controller starts,
            so a badge here would be a badge on seventy rows out of eighty-eight
            and would mean nothing on any of them. It is a clause instead, on the
            line that already explains the setting.
          -->
          <span class="aside" title={setting.restart_reason ?? undefined}>
            Applies on restart.
          </span>
        {/if}
      </p>
    {/if}
  </div>

  {#if editing}
    <div
      class="editor"
      class:wide={setting.kind === 'strings' || setting.kind === 'labels' || quantity !== null}
    >
      {#if setting.kind === 'bool' || setting.kind === 'optional_bool'}
        <Switch bind:checked={flag} label="New value for {setting.key}" hideLabel />
        <span class="hint">{flag ? 'on' : 'off'}</span>
      {:else if setting.kind === 'enum'}
        <Select
          bind:value={text}
          options={(setting.choices ?? []).map((c) => ({ value: c, label: c }))}
          size="sm"
          ariaLabel="New value for {setting.key}"
        />
      {:else if setting.kind === 'strings'}
        <LabelInput bind:value={list} placeholder="Type a value, then press Enter" />
      {:else if setting.kind === 'labels'}
        <LabelInput bind:value={pairs} placeholder={labelPlaceholder} />
      {:else if quantity}
        <div class="amount">
          <QuantityField
            bind:value={amount}
            bind:error={unreadable}
            {quantity}
            whole={setting.kind === 'int'}
            values={amountNotches}
            label="New value for {setting.key}"
            fieldLabel="New value for {setting.key}"
          />
        </div>
      {:else}
        <Input
          bind:value={text}
          size="sm"
          mono
          type={setting.kind === 'int' || setting.kind === 'float' ? 'number' : 'text'}
          step={setting.kind === 'float' ? 0.5 : undefined}
          ariaLabel="New value for {setting.key}"
          placeholder={setting.kind === 'duration' ? 'e.g. 30s, 5m, 168h' : undefined}
        />
      {/if}
    </div>
    <div class="actions">
      <IconButton
        icon={Check}
        label="Save {setting.key}"
        size="sm"
        variant="secondary"
        loading={saving}
        onclick={commit}
      />
      <IconButton icon={X} label="Cancel editing {setting.key}" size="sm" onclick={cancel} />
    </div>
  {:else}
    <!--
      Where the value came from sits beside the value, not under the
      description: it is a fact about the value, and the question it answers
      -- "why is this not what I set?" -- is asked while looking at it.
    -->
    <div class="value">
      <span class="shown mono" class:unset>{shown}</span>
      {#if pinned}
        <Badge tone="pending" label="Set by {setting.env}" size="sm" dot={false} />
      {/if}
      {#if setting.pending}
        <Badge
          tone="draining"
          label="Waiting for a restart"
          size="sm"
          dot={false}
          title="It is saved. Zoomies cannot apply it to itself, so it takes effect the next time it starts."
        />
      {/if}
      {#if source && !pinned}
        <Tooltip text={source.detail}>
          <Badge tone={source.tone} label={source.label} size="sm" dot={false} />
        </Tooltip>
      {/if}
    </div>
    <div class="actions">
      {#if setting.editable}
        <Button size="sm" variant="ghost" icon={Pencil} onclick={start}>Change</Button>
        {#if resettable}
          <Tooltip
            text="Forget the stored value, so this goes back to the configuration file or the built-in default."
          >
            <Button size="sm" variant="ghost" icon={RotateCcw} loading={saving} onclick={reset}>
              Reset
            </Button>
          </Tooltip>
        {/if}
      {/if}
    </div>
  {/if}

  {#if failure}
    <p class="failure" role="alert">{failure}</p>
  {/if}
  {#if !setting.editable && setting.reason}
    <p class="reason">{setting.reason}</p>
  {/if}

  {#if findings.length > 0}
    <ul class="findings">
      {#each findings as finding (finding.code)}
        {@const meta = severityStatus(finding.severity)}
        <li>
          <Badge status={meta} size="sm" />
          <div>
            <p class="finding-title">{finding.title}</p>
            {#if finding.detail}<p class="finding-detail">{finding.detail}</p>{/if}
            {#if finding.fix}<p class="finding-fix"><strong>Fix:</strong> {finding.fix}</p>{/if}
            <!--
              A stored value the validator is unhappy with is the one that can
              lock somebody out: this page is behind the controller, and a
              value that stops it starting cannot be undone from here. The way
              back is said now, while the page still loads, rather than found
              at the moment it does not.
            -->
            {#if finding.undo && finding.source === 'database'}
              <p class="finding-detail">
                Stored here. If it ever stops the controller starting, this page will not load — run <code
                  >{finding.undo}</code
                > against the stopped controller.
              </p>
            {/if}
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  /*
    Three columns, so that every Change button on the page lands on the same
    vertical line however long the value beside it is. A ragged right edge down
    eighty-eight rows reads as carelessness, and it makes the one action on the
    row harder to find than it should be.
  */
  .row {
    display: grid;
    grid-template-columns: minmax(0, 24rem) minmax(0, 1fr) auto;
    align-items: start;
    gap: var(--z-space-2) var(--z-space-4);
    padding: var(--z-space-3) var(--z-space-5);
    border-bottom: var(--z-border-width) solid var(--z-border);
  }
  .row.has-findings {
    background: var(--z-surface-sunken);
  }
  /*
    A setting that is saved but not yet in force is the one state on this page
    that is about the future rather than the present, so it is marked down the
    edge rather than with another badge among the badges.
  */
  /*
    The row a link arrived for. An outline rather than a background, so it does
    not compete with the severity backgrounds a row may already be wearing --
    a setting somebody was sent to is usually one the validator is unhappy
    about, and the two would otherwise fight.
  */
  .row.sought {
    outline: var(--z-border-width-thick) solid var(--z-accent);
    outline-offset: calc(-1 * var(--z-border-width-thick));
    scroll-margin-top: var(--z-space-10);
  }
  .row.awaiting-restart {
    box-shadow: inset var(--z-nudge-1) 0 0 0 var(--z-draining);
  }
  .row.pinned {
    background: var(--z-pending-subtle);
    box-shadow: inset var(--z-nudge-1) 0 0 0 var(--z-pending);
  }
  /* A pinned value is the one the page is not in charge of, so it is not
     dressed up as one somebody is about to change. */
  .row.pinned .shown {
    color: var(--z-text-muted);
  }
  .key {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    min-width: 0;
  }
  .label {
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
  }
  .leaf {
    font-size: var(--z-text-2xs);
    color: var(--z-text-subtle);
    overflow-wrap: anywhere;
  }
  .aside {
    color: var(--z-text-subtle);
  }
  .summary {
    margin: 0;
    max-width: 52ch;
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
    color: var(--z-text-muted);
  }
  .value,
  .editor {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
    min-width: 0;
    /* Line the value up with the key's first line rather than its baseline. */
    min-height: var(--z-space-6);
  }
  .editor.wide {
    align-items: flex-start;
  }
  .amount {
    flex: 1;
    min-width: 0;
  }
  .shown {
    font-size: var(--z-text-sm);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .shown.unset {
    color: var(--z-text-subtle);
    font-style: italic;
  }
  .actions {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: var(--z-space-1);
    min-height: var(--z-space-6);
  }
  .hint {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .failure {
    grid-column: 1 / -1;
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-danger);
  }
  .reason {
    grid-column: 1 / -1;
    margin: 0;
    max-width: 76ch;
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
    color: var(--z-text-subtle);
  }
  .findings {
    grid-column: 1 / -1;
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .findings li {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
  }
  .finding-title {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
  .finding-detail,
  .finding-fix {
    margin: var(--z-space-1) 0 0;
    max-width: 76ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  /*
    The key column leaves a phone about forty pixels to hold a value and the
    button that changes it. Change kept its own width, sat past the right edge
    where it could not be pressed, and took the page sideways with it -- and the
    fixed navigation, which is laid out against the document, went with it. So
    the two stack.
  */
  @media (max-width: 768px) {
    .row {
      grid-template-columns: minmax(0, 1fr);
    }
    .actions {
      justify-content: flex-start;
    }
  }
</style>
