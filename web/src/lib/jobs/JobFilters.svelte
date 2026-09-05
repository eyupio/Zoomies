<script module lang="ts">
  import type { JobState } from '$lib/api/types';

  /** Everything the Jobs page filters by. It lives in the URL, so a view is shareable. */
  export interface JobFilterState {
    q: string;
    repo: string[];
    workflow: string[];
    pool_id: string[];
    label: string[];
    conclusion: string[];
    state: JobState[];
    /** Calendar dates, `YYYY-MM-DD`. Converted to instants when the API is called. */
    since: string;
    until: string;
    unmatched: boolean;
  }

  export const EMPTY_JOB_FILTERS: JobFilterState = {
    q: '',
    repo: [],
    workflow: [],
    pool_id: [],
    label: [],
    conclusion: [],
    state: [],
    since: '',
    until: '',
    unmatched: false,
  };
</script>

<!--
  The Jobs page's filters.

  The menus are populated from GET /jobs/facets, which reports what the database
  actually holds rather than what happens to be on this page -- so the
  repository list offers every repository that has ever run a job here, not the
  fifty most recent.

  Nothing is stored here: the page owns the state and puts it in the URL.
-->
<script lang="ts">
  import { Search } from '@lucide/svelte';
  import { registerSearch } from '$lib/keys';
  import { JOB_STATES } from '$lib/api/types';
  import type { Pool } from '$lib/api/types';
  import { jobStatus } from '$lib/status';
  import FilterBar from '$lib/components/FilterBar.svelte';
  import type { FilterChip } from '$lib/components/FilterBar.svelte';
  import Input from '$lib/components/Input.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import DateRange from './DateRange.svelte';
  import FacetMenu from './FacetMenu.svelte';
  import type { FacetOption } from './FacetMenu.svelte';

  interface Props {
    value: JobFilterState;
    /** Distinct values from GET /jobs/facets. */
    facets: { repos?: string[]; workflows?: string[]; conclusions?: string[] };
    pools: readonly Pool[];
    /** Labels worth offering: those the pools answer to, plus those on this page. */
    labelOptions: readonly string[];
    onchange: (patch: Partial<JobFilterState>) => void;
    onclear: () => void;
  }

  let { value, facets, pools, labelOptions, onchange, onclear }: Props = $props();

  let searchElement = $state<HTMLInputElement | null>(null);

  // `/` focuses this page's search rather than opening the palette.
  $effect(() => registerSearch(searchElement));

  const repoOptions = $derived<FacetOption[]>(
    (facets.repos ?? []).map((r) => ({ value: r, label: r })),
  );
  const workflowOptions = $derived<FacetOption[]>(
    (facets.workflows ?? []).map((w) => ({ value: w, label: w })),
  );
  const poolOptions = $derived<FacetOption[]>(
    pools.map((p) => ({
      value: p.id ?? '',
      label: p.name ?? p.id ?? '',
      hint: (p.labels ?? []).join(', '),
    })),
  );
  const labelChoices = $derived<FacetOption[]>(labelOptions.map((l) => ({ value: l, label: l })));
  const conclusionOptions = $derived<FacetOption[]>(
    (facets.conclusions ?? []).map((c) => ({
      value: c,
      label: jobStatus('completed', c).label,
    })),
  );
  const stateOptions = $derived<FacetOption[]>(
    JOB_STATES.map((s) => ({ value: s, label: jobStatus(s).label, hint: jobStatus(s).hint })),
  );

  const poolName = $derived(new Map(pools.map((p) => [p.id ?? '', p.name ?? p.id ?? ''])));

  function listChip(
    key: 'repo' | 'workflow' | 'pool_id' | 'label' | 'conclusion' | 'state',
    label: string,
    display: (v: string) => string = (v) => v,
  ): FilterChip[] {
    return value[key].map((v) => ({
      id: `${key}:${v}`,
      label,
      value: display(v),
      onremove: () =>
        onchange({ [key]: value[key].filter((x) => x !== v) } as Partial<JobFilterState>),
    }));
  }

  const chips = $derived<FilterChip[]>([
    ...(value.q
      ? [{ id: 'q', label: 'Text', value: value.q, onremove: () => onchange({ q: '' }) }]
      : []),
    ...listChip('repo', 'Repository'),
    ...listChip('workflow', 'Workflow'),
    ...listChip('pool_id', 'Pool', (v) => poolName.get(v) ?? v),
    ...listChip('label', 'Label'),
    ...listChip('conclusion', 'Outcome', (v) => jobStatus('completed', v).label),
    ...listChip('state', 'State', (v) => jobStatus(v as JobState).label),
    ...(value.since
      ? [
          {
            id: 'since',
            label: 'From',
            value: value.since,
            onremove: () => onchange({ since: '' }),
          },
        ]
      : []),
    ...(value.until
      ? [{ id: 'until', label: 'To', value: value.until, onremove: () => onchange({ until: '' }) }]
      : []),
    ...(value.unmatched
      ? [
          {
            id: 'unmatched',
            label: 'Only',
            value: 'unmatched jobs',
            onremove: () => onchange({ unmatched: false }),
          },
        ]
      : []),
  ]);
</script>

<FilterBar {chips} onclear={chips.length > 0 ? onclear : undefined}>
  <div class="search">
    <Input
      bind:element={searchElement}
      value={value.q}
      type="search"
      size="sm"
      icon={Search}
      placeholder="Search repository, workflow or job name"
      ariaLabel="Search jobs"
      oninput={(event) => onchange({ q: (event.currentTarget as HTMLInputElement).value })}
    />
  </div>

  <FacetMenu
    label="Repository"
    options={repoOptions}
    selected={value.repo}
    emptyHint="No repository has run a job here yet."
    onchange={(next) => onchange({ repo: next })}
  />
  <FacetMenu
    label="Workflow"
    options={workflowOptions}
    selected={value.workflow}
    emptyHint="No workflow has run a job here yet."
    onchange={(next) => onchange({ workflow: next })}
  />
  <FacetMenu
    label="Pool"
    options={poolOptions}
    selected={value.pool_id}
    emptyHint="There are no pools yet."
    onchange={(next) => onchange({ pool_id: next })}
  />
  <FacetMenu
    label="Label"
    options={labelChoices}
    selected={value.label}
    emptyHint="Labels appear once a pool defines them or a job asks for them."
    onchange={(next) => onchange({ label: next })}
  />
  <FacetMenu
    label="Outcome"
    options={conclusionOptions}
    selected={value.conclusion}
    emptyHint="No job has finished here yet."
    onchange={(next) => onchange({ conclusion: next })}
  />
  <FacetMenu
    label="State"
    options={stateOptions}
    selected={value.state}
    onchange={(next) => onchange({ state: next as JobState[] })}
  />

  <DateRange
    since={value.since}
    until={value.until}
    label="Queued between"
    onchange={(next) => onchange(next)}
  />

  <Switch
    label="Unmatched only"
    description="Queued jobs no enabled pool claims"
    checked={value.unmatched}
    onchange={(on) => onchange({ unmatched: on })}
  />
</FilterBar>

<style>
  .search {
    min-width: 260px;
    flex: 1 1 260px;
    max-width: 380px;
  }
</style>
