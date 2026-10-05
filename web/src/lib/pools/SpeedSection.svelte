<!--
  What makes a pool's runners faster, at a price: scratch space kept in memory
  instead of on the host's disk, and a cache kept between one runner and the
  next.

  Neither is on for a new pool, and neither is a size. Both trade something --
  a tmpfs is charged to the runner's memory limit, a cache is state one job can
  leave for the next -- so each says what it costs where the choice is made,
  and each is a switch with its detail behind it rather than a form that is
  always open.

  The scratch space is Auto by default: each runner decides, so a folder is in
  memory where the runner has room for it to be useful and on disk where it has
  not. That is why there is no proposal to raise the limit under Auto, and why
  the pool says where a folder stayed on disk.
-->
<script lang="ts">
  import { TriangleAlert } from '@lucide/svelte';
  import type { PoolRoom as PoolRoomShape, Result } from '$lib/api/types';
  import { formatMegabytes, pluralise } from '$lib/format';
  import Button from '$lib/components/Button.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import QuantityField from '$lib/components/QuantityField.svelte';
  import RadioGroup from '$lib/components/RadioGroup.svelte';
  import Select from '$lib/components/Select.svelte';
  import {
    CACHE_NOTCHES,
    cacheBytes,
    cacheGb,
    daemonReserveMb,
    gbLabel,
    memoryLabel,
    nearest,
    recommendedMemoryMb,
    tmpfsIsTight,
    tmpfsReserveMb,
    withValue,
  } from './sizing';
  import type { PoolDraft } from './draft';

  interface Props {
    draft: PoolDraft;
    errors: Record<string, string>;
    touch: (field: string) => void;
    /** What the controller makes of the pool, for the disk the cache may use. */
    verdict: Result<'validatePool'> | null;
  }

  let { draft, errors, touch, verdict }: Props = $props();

  /* -- the folders kept in memory ------------------------------------------- */

  /** A typed size, or undefined for one left to the memory limit. */
  function typedMb(raw: string): number | undefined {
    const n = Number(raw.trim());
    return raw.trim() !== '' && Number.isInteger(n) && n > 0 ? n : undefined;
  }

  const containerBackend = $derived(draft.backend === 'docker' || draft.backend === 'podman');
  /** The limit typed on the size section; zero where the host picks, which there is nothing to raise. */
  const typedLimitMb = $derived(draft.sizing === 'fixed' ? (typedMb(draft.memory_mb) ?? 0) : 0);
  /**
   * The limit that leaves the job the room it has now with the folders on top.
   * A tmpfs is charged to the runner's memory limit, so switching one on
   * without raising the limit leaves the job less than it had.
   */
  const workFolder = $derived({
    enabled: draft.tmpfs_work,
    sizeMb: typedMb(draft.tmpfs_work_size),
  });
  const tmpFolder = $derived({ enabled: draft.tmpfs_tmp, sizeMb: typedMb(draft.tmpfs_tmp_size) });
  /**
   * Only a Docker-in-Docker pool on the Docker backend has a sidecar, and the
   * sidecar is a second container with a memory limit of its own: the image
   * store is charged to it, and the work folder and /tmp are not.
   */
  const hasSidecar = $derived(draft.backend === 'docker' && draft.docker_mode === 'dind');
  const daemonFolder = $derived({
    enabled: hasSidecar && draft.tmpfs_daemon,
    sizeMb: typedMb(draft.tmpfs_daemon_size),
  });
  // Only when a container's folders take enough of the limit to matter, each judged
  // against the limit it is charged to, so accepting the proposal ends it rather
  // than moving it.
  const proposedLimitMb = $derived(
    tmpfsIsTight(typedLimitMb, tmpfsReserveMb(workFolder, tmpFolder)) ||
      tmpfsIsTight(typedLimitMb, daemonReserveMb(daemonFolder))
      ? recommendedMemoryMb(typedLimitMb, workFolder, tmpFolder, daemonFolder)
      : 0,
  );
  const tmpfsOn = $derived(draft.tmpfs_work || draft.tmpfs_tmp || daemonFolder.enabled);

  /* -- the cache ------------------------------------------------------------ */

  const room = $derived<PoolRoomShape | null>(verdict?.room ?? null);
  const roomTotal = $derived(room?.runners ?? 0);
  // What Auto comes to on this pool's hosts, worked out by the controller from the
  // sizes on this form: per host, what each folder is given, and the smallest
  // change that would put more in memory. Present only while a folder is Auto.
  const plan = $derived(room?.tmpfs_plan ?? null);
  const planOnDisk = $derived(plan ? (plan.in_memory ?? 0) < (plan.total ?? 0) : false);
  const cacheLimitGb = $derived(cacheGb(Number(draft.cache_size_limit) || 0));
  const cacheNotches = $derived(withValue(CACHE_NOTCHES, cacheLimitGb));
  /* The disk the smallest matching host can spare. A limit above it is not a
     limit: the disk fills first, and a host at or below its disk reserve takes
     no runner of any pool. */
  const spareGb = $derived(room?.disk_known ? Math.floor((room.smallest_disk_mb ?? 0) / 1024) : 0);
  const cacheMarks = $derived.by(() => {
    const marks: { value: number; label: string; recommended?: boolean }[] = [
      { value: 0, label: 'no limit' },
    ];
    if (spareGb > 0) {
      const fits = nearest(
        cacheNotches.filter((n) => n > 0 && n <= spareGb),
        spareGb / 2,
      );
      if (fits > 0) marks.push({ value: fits, label: 'fits every host', recommended: true });
    }
    const top = cacheNotches[cacheNotches.length - 1] ?? 500;
    if (!marks.some((m) => m.value === top)) marks.push({ value: top, label: gbLabel(top) });
    return marks;
  });
  const cacheAboveDisk = $derived(spareGb > 0 && cacheLimitGb > spareGb);
  /* The server refuses a limit on a named volume, because there is no
     directory to measure behind one. Saying so beside the slider beats finding
     out when the pool is saved. */
  const cacheNeedsPath = $derived(cacheLimitGb > 0 && !draft.cache_source.trim().startsWith('/'));

  function setCacheLimit(gb: number): void {
    draft.cache_size_limit = gb > 0 ? String(cacheBytes(gb)) : '';
    touch('cache.size_limit');
  }
</script>

{#if containerBackend}
  <fieldset class="group">
    <legend>Scratch space in memory</legend>
    <p class="hint">
      A runner's work folder and <code>/tmp</code> normally live on its host's disk. In memory they remove
      the wait on every checkout, install and build. They are charged to the runner's memory limit, and
      gone when the runner is.
    </p>

    <Checkbox
      bind:checked={draft.tmpfs_work}
      label="Keep the work folder in memory"
      description="The checkout, build output and the runner's own temporary files. The one worth having."
      onchange={() => touch('tmpfs.work.enabled')}
    />
    {#if draft.tmpfs_work}
      <Field
        label="Work folder size (MB)"
        error={errors['tmpfs.work.size_mb']}
        hint="Leave it empty to fit it to the memory limit: up to 4096 MB, and no more than half of the limit."
      >
        {#snippet children({ id, describedBy, invalid })}
          <Input
            bind:value={draft.tmpfs_work_size}
            {id}
            {describedBy}
            {invalid}
            inputmode="numeric"
            placeholder="sized from the memory limit"
            autocomplete="off"
            onblur={() => touch('tmpfs.work.size_mb')}
          />
        {/snippet}
      </Field>
    {/if}

    <Checkbox
      bind:checked={draft.tmpfs_tmp}
      label="Keep /tmp in memory as well"
      description="Some toolchains put their heaviest traffic there, and some jobs leave gigabytes behind. Off unless you ask."
      onchange={() => touch('tmpfs.tmp.enabled')}
    />
    {#if draft.tmpfs_tmp}
      <Field
        label="/tmp size (MB)"
        error={errors['tmpfs.tmp.size_mb']}
        hint="Leave it empty to fit it to the memory limit: up to 1024 MB."
      >
        {#snippet children({ id, describedBy, invalid })}
          <Input
            bind:value={draft.tmpfs_tmp_size}
            {id}
            {describedBy}
            {invalid}
            inputmode="numeric"
            placeholder="sized from the memory limit"
            autocomplete="off"
            onblur={() => touch('tmpfs.tmp.size_mb')}
          />
        {/snippet}
      </Field>
    {/if}

    {#if hasSidecar}
      <Checkbox
        bind:checked={draft.tmpfs_daemon}
        label="Keep the Docker image store in memory"
        description="The Docker-in-Docker sidecar writes every image a job pulls and every layer it builds here, which is usually most of this pool's disk traffic. It is charged to the sidecar's own memory limit, and an image bigger than the store does not pull — so size it for the largest image your jobs use."
        onchange={() => touch('tmpfs.daemon.enabled')}
      />
      {#if draft.tmpfs_daemon}
        <Field
          label="Image store size (MB)"
          error={errors['tmpfs.daemon.size_mb']}
          hint="Leave it empty to fit it to the sidecar's memory limit: up to 8192 MB, and no more than half of it."
        >
          {#snippet children({ id, describedBy, invalid })}
            <Input
              bind:value={draft.tmpfs_daemon_size}
              {id}
              {describedBy}
              {invalid}
              inputmode="numeric"
              placeholder="sized from the memory limit"
              autocomplete="off"
              onblur={() => touch('tmpfs.daemon.size_mb')}
            />
          {/snippet}
        </Field>
      {/if}
    {/if}

    {#if tmpfsOn}
      <RadioGroup
        bind:value={
          () => (draft.tmpfs_auto ? 'auto' : 'always'), (v) => (draft.tmpfs_auto = v === 'auto')
        }
        name="tmpfs-placement"
        legend="Placement"
        options={[
          {
            value: 'auto',
            label: 'Auto (recommended)',
            description:
              'Each runner decides: a folder is in memory where the runner has room for it to be useful (2 GB for the work folder, 1 GB for /tmp, 4 GB for the image store), and on disk where it has not. Safe for a pool whose hosts differ, and the pool says where a folder stayed on disk.',
          },
          {
            value: 'always',
            label: 'Always in memory',
            description:
              'Every runner gets the folders in memory, fitted as small as its limit demands. A runner too small for a folder fills it and fails jobs with "no space left on device".',
          },
        ]}
      />
    {/if}

    {#if tmpfsOn && draft.tmpfs_auto && plan}
      <div class="callout" class:ok={!planOnDisk} role="status" data-testid="tmpfs-plan">
        <div>
          <p class="callout-title">
            {#if planOnDisk}
              Auto puts {plan.in_memory} of {plan.total} folders in memory on your hosts
            {:else}
              Auto puts every folder in memory on your hosts
            {/if}
          </p>
          <ul class="plan">
            {#each plan.hosts as h (h.host)}
              <li>
                <strong>{h.host}</strong>, a runner has {memoryLabel(h.runner_mb ?? 0)}{h.daemon_mb
                  ? ` and its sidecar ${memoryLabel(h.daemon_mb)}`
                  : ''}:
                {#each h.folders ?? [] as f, i (f.name)}{i > 0 ? ', ' : ' '}{f.name}
                  {(f.mb ?? 0) > 0 ? `${memoryLabel(f.mb ?? 0)} in memory` : 'on disk'}{/each}
              </li>
            {/each}
          </ul>
          {#if planOnDisk}
            <p>
              A folder stays on disk where a runner is too small for it to be useful, which beats
              filling it and failing jobs. To put more in memory:
            </p>
            <ul class="plan">
              {#if plan.share}
                <li>
                  Give the sidecar {plan.share.percent}% of a slot — {plan.share.in_memory} of {plan.total}
                  folders in memory, no runners lost.
                  <Button
                    variant="secondary"
                    size="sm"
                    onclick={() => (draft.daemon_share = String(plan.share?.percent ?? ''))}
                  >
                    Set the share to {plan.share.percent}%
                  </Button>
                </li>
              {/if}
              {#each plan.sizes ?? [] as o (o.host)}
                <li>
                  {#if o.lever === 'standard'}
                    Raise <strong>{o.host}</strong>'s standard runner memory to {memoryLabel(
                      o.memory_mb ?? 0,
                    )}
                    ({pluralise(o.slots ?? 0, 'slot')}, now {o.slots_now}) under
                    <a href="/hosts">Runner sizes</a>.
                  {:else}
                    Lower <strong>{o.host}</strong>'s capacity to {pluralise(o.slots ?? 0, 'slot')} so
                    each runner is about {memoryLabel(o.memory_mb ?? 0)}, on the
                    <a href="/hosts">Hosts</a> page.
                  {/if}
                </li>
              {/each}
              {#if !plan.share && (plan.sizes ?? []).length === 0}
                <li>No change to these hosts' runner sizes puts the work folder in memory.</li>
              {/if}
            </ul>
          {/if}
        </div>
      </div>
    {/if}

    {#if tmpfsOn && !draft.tmpfs_auto && proposedLimitMb > 0}
      <div class="callout" role="status">
        <TriangleAlert size={16} aria-hidden="true" />
        <div>
          <p class="callout-title">Raise the memory limit to {memoryLabel(proposedLimitMb)}</p>
          <p>
            These folders may fill up to {memoryLabel(proposedLimitMb - typedLimitMb)}, and they
            come out of the {memoryLabel(typedLimitMb)} limit rather than being added to it. At
            {memoryLabel(proposedLimitMb)} the job keeps the room it has now.
          </p>
          <Button
            variant="secondary"
            size="sm"
            onclick={() => (draft.memory_mb = String(proposedLimitMb))}
          >
            Set the limit to {memoryLabel(proposedLimitMb)}
          </Button>
        </div>
      </div>
    {:else if tmpfsOn && !draft.tmpfs_auto && draft.sizing === 'automatic'}
      <p class="echo">
        This pool's runners are each given a share of their host{hasSidecar
          ? ', split between the runner and its Docker sidecar'
          : ''}, and the folders are fitted into half of the container's part — so they cannot take
        the memory a job needs. Choose a fixed size to set the limit yourself{hasSidecar
          ? ', which gives each container the whole of it'
          : ''}.
      </p>
    {/if}
  </fieldset>
{/if}

<fieldset class="group">
  <legend>Performance cache</legend>
  <p class="hint">
    Mounted at <code>/opt/zoomies-cache</code> and kept between runners. This is disposable build acceleration
    — dependencies and build outputs — not persistent workflow storage, and it may be evicted at any time.
  </p>

  <Checkbox
    bind:checked={draft.cache_enabled}
    label="Keep a cache between runners"
    description="Faster builds, at the price of state one job can leave for the next within the boundary below."
    onchange={() => touch('cache.enabled')}
  />

  {#if draft.cache_enabled}
    {#if containerBackend}
      <!--
        The tool cache is the one thing in a cache that a later job runs rather
        than reads, which is why it is its own choice and why it says so.
      -->
      <Checkbox
        bind:checked={draft.cache_tools}
        label="Keep a tool cache as well"
        description="What setup-python, setup-node, setup-go and setup-java download is kept in the host's shared folder for the next runner, within the same boundary. A job that can write to it can replace a tool the next job runs."
        onchange={() => touch('cache.tools')}
      />
    {/if}
    <div class="pair">
      <Field
        label="Isolation scope"
        error={errors['cache.scope']}
        hint="Pool: one cache all this pool's jobs share. Repository: a cache per repository, which is only as private as the pool's labels."
      >
        {#snippet children({ id, describedBy, invalid })}
          <Select
            bind:value={draft.cache_scope}
            options={[
              { value: 'pool', label: 'Pool' },
              { value: 'repository', label: 'Repository' },
            ]}
            {id}
            {describedBy}
            {invalid}
          />
        {/snippet}
      </Field>

      <Field
        label="Host path or volume prefix"
        error={errors['cache.source']}
        hint="An absolute path on the host, or a named-volume prefix. A size limit needs the path: there is nothing to measure inside a volume. The shared folder is already visible to a containerised controller."
      >
        {#snippet children({ id, describedBy, invalid })}
          <Input
            bind:value={draft.cache_source}
            {id}
            {describedBy}
            {invalid}
            mono
            placeholder="/var/lib/zoomies/shared/cache/pools"
            autocomplete="off"
            onblur={() => touch('cache.source')}
          />
        {/snippet}
      </Field>
    </div>

    <Field
      label="Cache size limit"
      error={errors['cache.size_limit']}
      hint="Kept by evicting whole entries, least recently used first, between one runner and the next."
    >
      {#snippet children({ id, describedBy })}
        <QuantityField
          {id}
          quantity="gb"
          values={cacheNotches}
          value={cacheLimitGb}
          label="Cache size limit"
          valuetext={gbLabel}
          marks={cacheMarks}
          tone={cacheAboveDisk ? 'warning' : 'accent'}
          empty={{ value: 0, placeholder: 'no limit' }}
          {describedBy}
          onchange={(v) => setCacheLimit(v ?? 0)}
        />
      {/snippet}
    </Field>

    {#if cacheAboveDisk}
      <div class="callout" role="status">
        <TriangleAlert size={16} aria-hidden="true" />
        <div>
          <p class="callout-title">More cache than the smallest host can spare</p>
          <p>
            {room?.smallest_disk_host} has {formatMegabytes(room?.smallest_disk_mb ?? 0)} free behind
            its work directory, and this cache may grow to {gbLabel(cacheLimitGb)}. Eviction happens
            between one runner and the next, so the disk fills first — and a host at or below its
            disk reserve takes no runner of any pool, not just this one.
          </p>
        </div>
      </div>
    {:else if cacheNeedsPath}
      <div class="callout" role="status">
        <TriangleAlert size={16} aria-hidden="true" />
        <div>
          <p class="callout-title">A size limit needs a host path</p>
          <p>
            The limit is kept by evicting entries from a directory on the host, and there is nothing
            to measure inside a named volume. Give the source above an absolute path, or leave the
            limit at no limit.
          </p>
        </div>
      </div>
    {:else if spareGb > 0 && cacheLimitGb === 0}
      <p class="echo">
        With no limit the cache grows until the disk does. The smallest host this pool reaches has
        {formatMegabytes(room?.smallest_disk_mb ?? 0)} free.
      </p>
    {/if}

    {#if draft.cache_scope === 'repository'}
      <Field
        label="Cache repository (owner/name)"
        error={errors['cache.repository']}
        hint="Leave it empty when the pool's installation is scoped to a single repository — it already says which one."
      >
        {#snippet children({ id, describedBy, invalid })}
          <Input
            bind:value={draft.cache_repository}
            {id}
            {describedBy}
            {invalid}
            placeholder="acme/widgets"
            autocomplete="off"
            onblur={() => touch('cache.repository')}
          />
        {/snippet}
      </Field>
    {/if}

    {#if roomTotal > 0 && draft.cache_scope === 'pool'}
      <p class="echo">
        One cache per host, shared by every runner of this pool on it — up to {pluralise(
          roomTotal,
          'runner',
        )} across the fleet.
      </p>
    {/if}
  {/if}
</fieldset>
