<script lang="ts">
  let {
    pct,
    size = 56,
    label = ''
  }: { pct: number; size?: number; label?: string } = $props();

  const stroke = $derived(size >= 100 ? 8 : 5);
  const r = $derived((size - stroke) / 2);
  const c = $derived(2 * Math.PI * r);
  const off = $derived(c * (1 - Math.max(0, Math.min(1, pct))));
</script>

<span class="ring" style={`width:${size}px;height:${size}px`}>
  <svg width={size} height={size} aria-hidden="true">
    <circle class="track" cx={size / 2} cy={size / 2} r={r} stroke-width={stroke} />
    <circle
      class="value"
      cx={size / 2}
      cy={size / 2}
      r={r}
      stroke-width={stroke}
      stroke-dasharray={c.toFixed(2)}
      stroke-dashoffset={off.toFixed(2)}
    />
  </svg>
  <span class="ring-text">
    {#if label}
      {@html label}
    {:else}
      <span class="pct">{Math.round(pct * 100)}%</span>
    {/if}
  </span>
</span>
