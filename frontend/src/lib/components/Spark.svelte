<script lang="ts">
  let { series, w = 132, h = 34 }: { series: number[]; w?: number; h?: number } = $props();

  const step = $derived(w / (series.length - 1));
  const pts = $derived(
    series.map((v, i) => `${(i * step).toFixed(1)},${(h - v * (h - 4) - 2).toFixed(1)}`)
  );
  const line = $derived(`M${pts.join(' L')}`);
  const area = $derived(`${line} L${w},${h} L0,${h} Z`);
</script>

<svg class="spark" width={w} height={h} viewBox={`0 0 ${w} ${h}`} aria-hidden="true">
  <path class="area" d={area} />
  <path class="line" d={line} />
</svg>
