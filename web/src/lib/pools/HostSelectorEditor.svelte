<!--
  Which hosts a pool is allowed to land on.

  A pool's host_selector is a flat map matched against each host, so everything
  here -- the two dropdowns and every custom row -- is one entry in that map.
  It is split apart for editing because `os` and `arch` are not labels anyone
  types: the agent reports them, so they can be offered as a closed list of the
  values the fleet actually has, where a free-text field would let an operator
  write `arm` and silently match nothing.

  The live count underneath is the point of the whole component. A selector is
  the one pool setting whose mistake looks like health -- the pool is enabled,
  its labels are right, and it never places a runner -- so the answer to "which
  machines does this reach" is on screen while it is being typed rather than
  a page later.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { Cpu, MonitorCog, Plus, ServerOff, Trash2 } from '@lucide/svelte';
  import type { Host } from '$lib/api/types';
  import { pluralise } from '$lib/format';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import Field from '$lib/components/Field.svelte';
  import IconButton from '$lib/components/IconButton.svelte';
  import Input from '$lib/components/Input.svelte';
  import RadioGroup from '$lib/components/RadioGroup.svelte';
  import Select from '$lib/components/Select.svelte';
  import { hostMatchesSelector, reportedValues, REPORTED_KEYS } from './hostSelector';

  interface Props {
    /** The pool's host_selector, edited in place. */
    selector: Record<string, string>;
    /** Every host the fleet knows about, for the counts and the value lists. */
    hosts: readonly Host[];
    /** False until the fleet cache has landed, so we do not cry wolf. */
    hostsKnown: boolean;
    /** Whether the operator has chosen to keep this pool to some hosts. */
    restricting: boolean;
    onchange: (next: Record<string, string>) => void;
    onrestrict: (next: boolean) => void;
    class?: string;
  }

  let {
    selector,
    hosts,
    hostsKnown,
    restricting,
    onchange,
    onrestrict,
    class: className = '',
  }: Props = $props();

  /**
   * The custom rows are local state, not derived: a half-typed row is a key
   * with no value yet, which cannot live in the selector map without meaning
   * "match hosts whose label is empty". Seeded from the pool being edited and
   * pushed back up as rows are completed -- untracked because it is the
   * starting point that is wanted, not a subscription that would rewrite the
   * row under the cursor every time the value is echoed back down.
   */
  let customRows = $state(
    untrack(() =>
      Object.entries(selector)
        .filter(([key]) => !REPORTED_KEYS.includes(key as (typeof REPORTED_KEYS)[number]))
        .map(([key, value]) => ({ key, value })),
    ),
  );

  const os = $derived(selector.os ?? '');
  const arch = $derived(selector.arch ?? '');

  const osOptions = $derived(reportedValues(hosts, 'os', os));
  const archOptions = $derived(reportedValues(hosts, 'arch', arch));

  const matching = $derived(hosts.filter((host) => hostMatchesSelector(host, selector)));
  const unrestricted = $derived(Object.keys(selector).length === 0);

  // Matching and taking work are different questions, and a selector that picks
  // out only a cordoned host answers the first yes and the second no. Saying
  // just "1 host matches" here and then "no host can run this pool" on the
  // controller's check at the foot of the page reads as the editor contradicting
  // itself, so the gap is named
  // where the rule that caused it is being edited.
  const availableMatches = $derived(
    matching.filter((host) => host.healthy !== false && host.cordoned !== true),
  );
  const unavailableMatches = $derived(matching.length - availableMatches.length);

  const MODES = [
    {
      value: 'any',
      label: 'Any host in the fleet',
      description: 'Runners land wherever there is room. New hosts join in automatically.',
    },
    {
      value: 'matching',
      label: 'Only hosts that match',
      description:
        'Keep this pool on particular machines \u2014 an architecture, an OS, or your own labels.',
    },
  ];

  function push(next: Record<string, string>): void {
    // An empty value is "no rule", never "match the empty string".
    const cleaned = Object.fromEntries(
      Object.entries(next).filter(([key, value]) => key.trim() !== '' && value !== ''),
    );
    onchange(cleaned);
  }

  function customEntries(): Record<string, string> {
    const out: Record<string, string> = {};
    for (const row of customRows) {
      const key = row.key.trim();
      if (key && !REPORTED_KEYS.includes(key as (typeof REPORTED_KEYS)[number]))
        out[key] = row.value;
    }
    return out;
  }

  function setReported(key: string, value: string): void {
    push({ ...selector, ...customEntries(), [key]: value });
  }

  function syncCustom(): void {
    push({ os, arch, ...customEntries() });
  }

  function chooseMode(value: string): void {
    const next = value === 'matching';
    onrestrict(next);
    // Going back to "any host" throws the rules away rather than remembering
    // them invisibly: a selector that is not shown must not still be applied.
    if (!next) {
      customRows = [];
      onchange({});
    }
  }

  function addRow(): void {
    customRows = [...customRows, { key: '', value: '' }];
  }

  function removeRow(index: number): void {
    customRows = customRows.filter((_, i) => i !== index);
    syncCustom();
  }

  const duplicate = $derived.by(() => {
    const seen: string[] = [];
    for (const row of customRows) {
      const key = row.key.trim();
      if (!key) continue;
      if (seen.includes(key)) return key;
      seen.push(key);
    }
    return '';
  });

  const reserved = $derived(
    customRows.some((row) =>
      REPORTED_KEYS.includes(row.key.trim() as (typeof REPORTED_KEYS)[number]),
    ),
  );

  const uid = $props.id();
  const duplicateErrorId = $derived(duplicate ? `${uid}-duplicate-error` : undefined);
  const reservedErrorId = $derived(reserved ? `${uid}-reserved-error` : undefined);

  /** Which of the two error messages, if any, a given key's input is invalid against. */
  function keyDescribedBy(key: string): string | undefined {
    const ids = [
      Boolean(duplicate) && key.trim() === duplicate ? duplicateErrorId : undefined,
      REPORTED_KEYS.includes(key.trim() as (typeof REPORTED_KEYS)[number])
        ? reservedErrorId
        : undefined,
    ].filter(Boolean);
    return ids.length > 0 ? ids.join(' ') : undefined;
  }
</script>

<div class="placement {className}">
  <RadioGroup
    name="pool-placement"
    legend="Where these runners run"
    value={restricting ? 'matching' : 'any'}
    options={MODES}
    onchange={chooseMode}
  />

  {#if restricting}
    <div class="rules">
      <div class="reported">
        <Field label="Operating system" hint="What the host's agent reports.">
          {#snippet children({ id, describedBy })}
            <Select
              {id}
              {describedBy}
              value={os}
              placeholder="Any"
              options={[
                { value: '', label: 'Any operating system' },
                ...osOptions.map((entry) => ({
                  value: entry.value,
                  label: entry.hosts
                    ? `${entry.value} (${pluralise(entry.hosts, 'host')})`
                    : `${entry.value} (no connected host)`,
                })),
              ]}
              onchange={(value) => setReported('os', value)}
            />
          {/snippet}
        </Field>

        <Field label="Architecture" hint="What the host's agent reports.">
          {#snippet children({ id, describedBy })}
            <Select
              {id}
              {describedBy}
              value={arch}
              placeholder="Any"
              options={[
                { value: '', label: 'Any architecture' },
                ...archOptions.map((entry) => ({
                  value: entry.value,
                  label: entry.hosts
                    ? `${entry.value} (${pluralise(entry.hosts, 'host')})`
                    : `${entry.value} (no connected host)`,
                })),
              ]}
              onchange={(value) => setReported('arch', value)}
            />
          {/snippet}
        </Field>
      </div>

      <div class="custom">
        <p class="custom-title">Your own labels</p>
        <p class="custom-hint">
          Anything else a host is labelled with, on the Hosts page. Both the key and the value have
          to match exactly.
        </p>

        {#if customRows.length > 0}
          <div class="head" aria-hidden="true">
            <span>Key</span>
            <span>Value</span>
            <span></span>
          </div>
          {#each customRows as row, index (index)}
            <div class="row">
              <Input
                bind:value={row.key}
                size="sm"
                mono
                ariaLabel="Rule {index + 1} key"
                oninput={syncCustom}
                invalid={(Boolean(duplicate) && row.key.trim() === duplicate) ||
                  REPORTED_KEYS.includes(row.key.trim() as (typeof REPORTED_KEYS)[number])}
                describedBy={keyDescribedBy(row.key)}
              />
              <Input
                bind:value={row.value}
                size="sm"
                mono
                ariaLabel="Rule {index + 1} value"
                oninput={syncCustom}
              />
              <IconButton
                icon={Trash2}
                label="Remove the rule {row.key || index + 1}"
                size="sm"
                onclick={() => removeRow(index)}
              />
            </div>
          {/each}
        {/if}

        {#if duplicate}
          <!-- role="alert" so the failure is spoken when it appears; the
               describedby link on the offending inputs alone is only read if
               the field is revisited. -->
          <p class="rule-error" id={duplicateErrorId} role="alert">
            Two rules are both called <span class="mono">{duplicate}</span>. The last one would win,
            so rename or remove one.
          </p>
        {/if}
        {#if reserved}
          <p class="rule-error" id={reservedErrorId} role="alert">
            <span class="mono">os</span> and <span class="mono">arch</span> are set by the dropdowns above.
            A rule here with the same name is ignored.
          </p>
        {/if}

        <div>
          <Button size="sm" variant="secondary" icon={Plus} onclick={addRow}>Add a rule</Button>
        </div>
      </div>
    </div>
  {/if}

  {#if hostsKnown}
    <!--
      The promise, checked against the same rule the scheduler uses. Naming the
      hosts rather than only counting them is what turns "2 hosts" into
      something an operator can recognise as right or wrong at a glance.
    -->
    <div class="match" class:none={matching.length === 0} aria-live="polite">
      {#if matching.length === 0}
        <p class="match-title">
          <ServerOff size={15} aria-hidden="true" />
          No connected host matches
        </p>
        <p class="match-body">
          This pool would never place a runner, and every job asking for its labels would sit in the
          queue. Relax a rule, or label a host to match.
        </p>
      {:else}
        <p class="match-title">
          {#if restricting && !unrestricted}
            <Cpu size={15} aria-hidden="true" />
          {:else}
            <MonitorCog size={15} aria-hidden="true" />
          {/if}
          {matching.length === hosts.length
            ? `Every connected host matches (${pluralise(hosts.length, 'host')})`
            : `${matching.length} of ${pluralise(hosts.length, 'connected host')} match`}
        </p>
        <p class="match-hosts">
          {#each matching.slice(0, 8) as host (host.id)}
            <Badge
              tone={host.cordoned === true || host.healthy === false ? 'danger' : 'neutral'}
              size="sm"
              label={host.name ?? ''}
              dot={false}
              title={host.cordoned === true
                ? 'Cordoned: it matches, but takes no new runners'
                : host.healthy === false
                  ? 'Not heartbeating, so it takes no new runners'
                  : undefined}
            />
          {/each}
          {#if matching.length > 8}
            <span class="more">and {matching.length - 8} more</span>
          {/if}
        </p>
        {#if availableMatches.length === 0}
          <p class="match-body">
            {matching.length === 1 ? 'It is' : 'They are all'} cordoned or not heartbeating, so this pool
            would still have nowhere to place a runner.
          </p>
        {:else if unavailableMatches > 0}
          <p class="match-body">
            {unavailableMatches} of them {unavailableMatches === 1 ? 'is' : 'are'} cordoned or not heartbeating
            and would take no runners today.
          </p>
        {/if}
        {#if restricting && unrestricted}
          <p class="match-body">
            No rule is set yet, so this still reaches every host. Add one, or choose “Any host”.
          </p>
        {/if}
      {/if}
    </div>
  {/if}
</div>

<style>
  .placement {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
  }
  .rules {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    padding: var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
  }
  .reported {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(min(100%, 14rem), 1fr));
    gap: var(--z-space-3);
  }
  .custom {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
  }
  .custom-title {
    margin: 0;
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
  }
  .custom-hint {
    margin: 0;
    max-width: 60ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .head,
  .row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) var(--z-space-6);
    align-items: center;
    gap: var(--z-space-2);
  }
  .head span {
    font-size: var(--z-text-2xs);
    text-transform: uppercase;
    letter-spacing: var(--z-tracking-wide);
    color: var(--z-text-muted);
  }
  .rule-error {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-danger);
  }
  .match {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    padding: var(--z-space-3);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
  }
  .match.none {
    border-color: var(--z-danger-border);
    border-left: var(--z-border-width-rail) solid var(--z-danger);
    background: var(--z-danger-subtle);
  }
  .match-title {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
  }
  .match-body {
    margin: 0;
    max-width: 66ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .match-hosts {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-1);
    margin: 0;
  }
  /*
    A badge holds a status word and does not wrap, which is right for every
    other one of them. These carry a host name chosen by whoever enrolled the
    machine, and `ip-10-0-31-44.eu-west-1.compute.internal` is wider than a
    phone on its own: it took the page sideways rather than the name onto a
    second line, which is the wrong way round.
  */
  .match-hosts :global(.badge) {
    max-width: 100%;
    white-space: normal;
    overflow-wrap: anywhere;
  }
  .more {
    font-size: var(--z-text-2xs);
    color: var(--z-text-muted);
    align-self: center;
  }
  .mono {
    font-family: var(--z-font-mono);
  }
</style>
