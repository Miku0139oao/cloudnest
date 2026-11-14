<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '$lib/components/Icon.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import { app } from '$lib/state.svelte';
  import { t, fmtBytes } from '$lib/i18n';
  import { restore as mockRestore } from '$lib/mock';
  import { data } from '$lib/data.svelte';
  import { backend, isWails } from '$lib/backend';

  interface Info {
    state: string;
    parts: number;
    partCount: number;
    originalName: string;
    size: number;
    missing?: number[];
  }

  let info = $state<Info | null>(null);
  let dest = $state('');
  let error = $state('');
  let busy = $state(false);

  const ready = $derived(info?.state === 'ready');

  onMount(async () => {
    if (!isWails) {
      info = {
        state: 'ready',
        parts: mockRestore.parts,
        partCount: mockRestore.parts,
        originalName: mockRestore.name,
        size: mockRestore.size
      };
      dest = mockRestore.dest;
      return;
    }
    dest = data.localRoot;
    const res = await backend.inspectSplit(app.restoreAccount, app.restorePath);
    if (!res) {
      error = 'backend unavailable';
      return;
    }
    if (res.err) {
      error = res.err.detail || res.err.message;
      return;
    }
    info = res as Info;
  });

  async function chooseDest(): Promise<void> {
    const picked = await backend.chooseFolder(t('restore.where'));
    if (picked?.path) dest = picked.path;
  }

  async function startRestore(): Promise<void> {
    if (!info) return;
    if (!dest) {
      error = t('restore.destMissing');
      return;
    }
    busy = true;
    const res = (await backend.restoreSplit(app.restoreAccount, app.restorePath, dest)) as
      | { err?: { message?: string; detail?: string } }
      | null;
    busy = false;
    if (!res || res.err) {
      error = res?.err?.detail || res?.err?.message || 'restore failed';
      return;
    }
    app.notify(t('restore.started'));
    app.go('activity');
  }
</script>

<Modal labelledby="restore-title" onclose={() => app.closeOverlay()}>
  <div class="dialog restore-dialog">
    <div class="dialog-head" style="padding-bottom:2px">
      <div class="grow">
        <div class="dialog-title" id="restore-title">
          {t('restore.title', { name: info?.originalName ?? '…' })}
        </div>
      </div>
      <button class="icon-button" aria-label={t('common.close')} onclick={() => app.closeOverlay()}>
        <Icon name="close" size={20} />
      </button>
    </div>

    <div class="dialog-body">
      {#if info}
        <div class="restore-file">
          <span class="rtile"><Icon name="movie" size={28} /></span>
          <div class="grow">
            <div class="rn">{info.originalName}</div>
            <div class="rs">{t('restore.sub', { size: fmtBytes(info.size), parts: info.partCount })}</div>
            <div class="rs">{app.restorePath}</div>
          </div>
          <span class="status" class:ok={ready} class:err={!ready}>
            <span class="dot"></span>{ready ? t('files.split.ready') : t('files.split.broken')}
          </span>
        </div>

        {#if ready}
          <div class="restore-where">{t('restore.where')}</div>
          <div class="restore-dest">
            <button class="chip selected" onclick={chooseDest}>
              <Icon name="folder" size={16} />{dest || t('restore.other')}
            </button>
            <button class="chip interactive" onclick={chooseDest}>
              <Icon name="folder-open" size={16} />{t('restore.other')}
            </button>
          </div>
          <div class="rs" style="margin-top:6px;color:var(--m3c-on-surface-variant)">
            {t('restore.hint')}
          </div>
        {:else}
          <div class="banner" style="margin-top:14px">
            <Icon name="warning" size={20} />
            <div>
              <div class="b-title">{t('restore.notReady')}</div>
              {#if info.missing?.length}
                <div>{t('restore.missingParts', { parts: info.missing.join(', ') })}</div>
              {/if}
            </div>
          </div>
        {/if}
      {:else if error}
        <div class="banner">
          <Icon name="error" size={20} />
          <div>{error}</div>
        </div>
      {:else}
        <div class="pane-loading">
          {#each Array(3) as _, i (i)}
            <span class="skeleton" style={`height:14px;width:${70 - i * 12}%`}></span>
          {/each}
        </div>
      {/if}

      {#if error && info}
        <div class="banner" style="margin-top:12px">
          <Icon name="error" size={20} /><div>{error}</div>
        </div>
      {/if}
    </div>

    <div class="dialog-foot">
      <button class="btn text" onclick={() => app.closeOverlay()}>{t('common.cancel')}</button>
      <div class="grow"></div>
      <button class="btn filled" disabled={!ready || !dest || busy} onclick={startRestore}>
        <Icon name="history" size={18} />
        <span>{t('restore.start', { size: fmtBytes(info?.size ?? 0) })}</span>
      </button>
    </div>
  </div>
</Modal>
