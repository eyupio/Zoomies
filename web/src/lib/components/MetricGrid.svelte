<script lang="ts">
  export interface Metric {
    label: string;
    value: string;
    detail: string;
    href?: string;
    progress?: number | null;
    tone?: 'neutral' | 'success' | 'danger' | 'warning' | 'accent' | 'busy';
  }
  let { items }: { items: Metric[] } = $props();
</script>

<dl class="metrics">
  {#each items as item (item.label)}
    <div class="metric" data-tone={item.tone ?? 'neutral'}>
      <dt>
        {#if item.href}<a
            href={item.href}
            aria-label={`${item.label}: ${item.value}. ${item.detail}`}
            >{item.label}<span aria-hidden="true">↗</span></a
          >{:else}{item.label}{/if}
      </dt>
      <dd class="value">{item.value}</dd>
      <!-- A second description of the same term. Only dt and dd may sit in a
           definition list's groups: the detail used to be a bare paragraph and
           the meter a bare div, and a screen reader lost the pairing. -->
      <dd class="detail">
        {item.detail}
        {#if item.progress != null}<div class="meter" aria-hidden="true">
            <span style:width={`${Math.min(100, Math.max(0, item.progress))}%`}></span>
          </div>{/if}
      </dd>
    </div>
  {/each}
</dl>

<style>
  .metrics {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(155px, 1fr));
    gap: var(--z-space-3);
    margin: 0 0 var(--z-space-5);
  }
  .metric {
    position: relative;
    min-width: 0;
    padding: var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  dt {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  dd {
    margin: 0;
  }
  .value {
    margin: var(--z-space-2) 0;
    font-size: var(--z-text-2xl);
    font-weight: var(--z-weight-semibold);
    font-variant-numeric: tabular-nums;
    letter-spacing: var(--z-tracking-tight);
  }
  .detail {
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
  }
  [data-tone='success'] .value {
    color: var(--z-idle);
  }
  [data-tone='danger'] .value {
    color: var(--z-danger);
  }
  [data-tone='warning'] .value {
    color: var(--z-pending);
  }
  a {
    color: inherit;
    text-decoration: none;
    display: flex;
    justify-content: space-between;
    gap: var(--z-space-2);
  }
  a::after {
    content: '';
    position: absolute;
    inset: 0;
    border-radius: var(--z-radius-md);
  }
  .metric:has(a):hover {
    border-color: var(--z-accent-border);
    background: var(--z-surface-hover);
  }
  .metric:focus-within {
    outline: 2px solid var(--z-accent);
    outline-offset: 2px;
  }
  [data-tone='accent'] .value {
    color: var(--z-accent);
  }
  [data-tone='busy'] .value {
    color: var(--z-busy);
  }
  .meter {
    height: var(--z-space-1);
    background: var(--z-surface-sunken);
    border-radius: var(--z-radius-full);
    overflow: hidden;
    margin-top: var(--z-space-3);
  }
  .meter span {
    display: block;
    height: 100%;
    background: var(--z-accent);
  }
</style>
