<!--
  Step four: the jobs that need somewhere other than the label mapping says.

  Step three answers "where does ubuntu-latest go" once, for the whole
  organisation, which is what a fleet nearly always wants. This step is where
  "nearly" stops being good enough: the integration suite that needs the big
  host, the one job that has to stay where it is until somebody fixes it, the
  repository being moved a job at a time.

  So every job is listed with the answer step three already gave it, and
  changing one changes nothing else. Doing nothing here is the normal outcome --
  the step is a list of defaults an operator can read past -- and that is why
  the consolidated answer is the first option on every row rather than something
  to opt back into.

  A job the scan could not name is listed too, greyed, with the reason. Leaving
  it out would look like the wizard had missed it.
-->
<script lang="ts">
  import type { MigrationOverride, MigrationPlan, MigrationRepo } from '$lib/api/types';
  import Select from '$lib/components/Select.svelte';

  interface Props {
    plan: MigrationPlan | null;
    /** The repositories the operator chose, changed or not. */
    repos: readonly MigrationRepo[];
    mapping: Record<string, string>;
    overrides: readonly MigrationOverride[];
    onchange: (repo: string, path: string, job: string, to: string | null) => void;
  }

  let { plan, repos, mapping, overrides, onchange }: Props = $props();

  /** Follow the consolidated mapping. The empty value, because it is the default. */
  const CONSOLIDATED = '';
  /**
   * Pin this one job to the rented runner it names today. It needs a value of
   * its own because an override to "" is a real decision, and "" already means
   * "no override at all". A pool's runs-on is a runner label -- lowercase
   * letters, digits and hyphens -- so no pool can ever collide with this.
   */
  const STAY = '__github__';

  interface Row {
    path: string;
    line: number;
    job: string;
    label: string;
    /** Why this row cannot be pointed anywhere, empty when it can. */
    blocked: string;
  }

  /** One repository's jobs, in the order they appear in each file. */
  function rowsOf(repo: MigrationRepo): Row[] {
    const rows: Row[] = [];
    for (const wf of repo.workflows ?? []) {
      const path = wf.path ?? '';
      for (const rw of wf.rewrites ?? []) {
        rows.push({
          path,
          line: rw.line ?? 0,
          job: rw.job ?? '',
          label: rw.label ?? '',
          blocked: '',
        });
      }
      for (const sk of wf.skips ?? []) {
        rows.push({
          path,
          line: sk.line ?? 0,
          job: sk.job ?? '',
          label: sk.label ?? '',
          // A job with both a name and a single hosted label is one a pool can
          // be chosen for, whatever the scan decided. Anything else -- a
          // ${{ }} expression, a job already pointed somewhere deliberate, a
          // runs-on no job name could be attributed to -- is not a decision
          // this step can make, and says so instead of offering a dead select.
          blocked:
            sk.job && sk.label ? '' : (sk.reason ?? 'this job cannot be pointed at a pool here'),
        });
      }
    }
    return rows.sort((a, b) => a.path.localeCompare(b.path) || a.line - b.line);
  }

  const sections = $derived(
    repos
      .map((repo) => ({ repo: repo.repo ?? '', rows: rowsOf(repo) }))
      .filter((s) => s.rows.length > 0),
  );

  const pools = $derived(plan?.pools ?? []);

  const options = $derived([
    ...pools.map((p) => ({ value: p.runs_on ?? '', label: `${p.runs_on}, the ${p.name} pool` })),
    { value: STAY, label: 'Leave this job where it is' },
  ]);

  /** What step three decided for a label, or "" for "left where it is". */
  function consolidated(label: string): string {
    return mapping[label] ?? '';
  }

  function optionsFor(row: Row) {
    const to = consolidated(row.label);
    return [
      {
        value: CONSOLIDATED,
        label: to ? `Use the label mapping, ${to}` : 'Use the label mapping, stays where it is',
      },
      ...options,
    ];
  }

  function valueOf(repo: string, row: Row): string {
    const found = overrides.find(
      (o) => o.repo === repo && o.path === row.path && o.job === row.job,
    );
    if (!found) return CONSOLIDATED;
    return found.to === '' ? STAY : (found.to ?? CONSOLIDATED);
  }

  function pick(repo: string, row: Row, value: string): void {
    if (value === CONSOLIDATED) onchange(repo, row.path, row.job, null);
    else onchange(repo, row.path, row.job, value === STAY ? '' : value);
  }

  const count = $derived(overrides.length);
  const total = $derived(sections.reduce((n, s) => n + s.rows.length, 0));
</script>

{#if total === 0}
  <p class="lede">
    None of the repositories you picked has a job to point anywhere, so there is nothing to make an
    exception for.
  </p>
{:else}
  <p class="lede">
    Every job below already has an answer from the label mapping, and leaving them all alone is the
    usual thing to do. Change one and it changes only that job, in that file, in that repository,
    the review step will show the diff either way.
    {#if count > 0}
      <strong>{count} {count === 1 ? 'job is' : 'jobs are'} an exception.</strong>
    {/if}
  </p>

  <div class="repos">
    {#each sections as section (section.repo)}
      <section>
        <h3>{section.repo}</h3>
        <ul>
          {#each section.rows as row, i (`${row.path}:${row.line}:${i}`)}
            <li class:blocked={row.blocked !== ''}>
              <span class="where">
                <code class="path">{row.path}</code>
                <code class="job">{row.job || `line ${row.line}`}</code>
              </span>
              {#if row.blocked}
                <span class="reason">{row.blocked}</span>
              {:else}
                <code class="label">{row.label}</code>
                <Select
                  value={valueOf(section.repo, row)}
                  options={optionsFor(row)}
                  size="sm"
                  ariaLabel="Where {row.job} in {row.path} runs, in {section.repo}"
                  onchange={(value) => pick(section.repo, row, value)}
                  class="to"
                />
              {/if}
            </li>
          {/each}
        </ul>
      </section>
    {/each}
  </div>
{/if}

<style>
  .lede {
    margin: 0 0 var(--z-space-4);
    max-width: 70ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .lede strong {
    color: var(--z-text);
    font-weight: var(--z-weight-medium);
  }
  .repos {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-5);
  }
  h3 {
    margin: 0 0 var(--z-space-2);
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-medium);
  }
  ul {
    margin: 0;
    padding: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
  }
  li {
    display: grid;
    grid-template-columns: minmax(0, 18rem) minmax(0, 8rem) minmax(0, 1fr);
    align-items: center;
    gap: var(--z-space-3);
  }
  li.blocked {
    grid-template-columns: minmax(0, 18rem) minmax(0, 1fr);
  }
  .where {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    min-width: 0;
  }
  .path,
  .job,
  .label {
    font-family: var(--z-font-mono);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .path {
    font-size: var(--z-text-2xs);
    color: var(--z-text-subtle);
  }
  .job {
    font-size: var(--z-text-xs);
    color: var(--z-text);
  }
  .label {
    padding: var(--z-space-1) var(--z-space-2);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    font-size: var(--z-text-xs);
  }
  .reason {
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-subtle);
  }

  /*
    The desktop row has three useful columns, but on a phone those columns leave
    the reason with only a few characters of width and push the pool control
    against the edge of the panel. Read each job from top to bottom instead:
    its identity, its current label, then the decision. Blocked jobs use the
    same shape, with the reason taking the decision's place.
  */
  @media (max-width: 768px) {
    ul {
      gap: 0;
    }
    li,
    li.blocked {
      grid-template-columns: minmax(0, 1fr);
      align-items: stretch;
      gap: var(--z-space-2);
      padding: var(--z-space-3) 0;
      border-bottom: var(--z-border-width) solid var(--z-border);
    }
    li:first-child {
      padding-top: var(--z-space-1);
    }
    li:last-child {
      padding-bottom: 0;
      border-bottom: 0;
    }
    .path,
    .job,
    .label {
      overflow-wrap: anywhere;
    }
    .label {
      white-space: normal;
    }
  }
</style>
