<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    open = true,
    variant = 'dialog',
    labelledby,
    onclose,
    children
  }: {
    open?: boolean;
    variant?: 'dialog' | 'sheet';
    labelledby: string;
    onclose?: () => void;
    children: Snippet;
  } = $props();

  let el = $state<HTMLDialogElement | null>(null);

  $effect(() => {
    if (!el) return;
    if (open && !el.open) {
      try {
        el.showModal();
      } catch {
        /* already open or detached */
      }
    } else if (!open && el.open) {
      el.close();
    }
  });

  function handleClose(): void {
    // Ignore closes caused by the parent unmounting this dialog.
    if (el?.isConnected) onclose?.();
  }
</script>

<dialog
  bind:this={el}
  class="dlg {variant === 'sheet' ? 'sheet-dialog' : ''}"
  {...({ closedby: 'any' } as Record<string, string>)}
  aria-labelledby={labelledby}
  onclose={handleClose}
>
  {@render children()}
</dialog>
