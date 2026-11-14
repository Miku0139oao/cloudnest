<script lang="ts">
  import { iconPaths } from '$lib/icons';

  let { name, size = 24 }: { name: string; size?: number } = $props();
  const body = $derived(iconPaths[name] ?? iconPaths['description']);
</script>

<svg class="i" width={size} height={size} viewBox="0 0 24 24" aria-hidden="true" focusable="false">
  {@html body}
</svg>
