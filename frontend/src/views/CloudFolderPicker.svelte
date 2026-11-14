<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '$lib/components/Icon.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import { t } from '$lib/i18n';
  import type { Entry } from '$lib/mock';
  import { backend } from '$lib/backend';

  let {
    accountID,
    initial = '',
    onpick,
    onclose
  }: {
    accountID: string;
    initial?: string;
    onpick: (path: string) => void;
    onclose: () => void;
  } = $props();

  let path = $state('');
  let dirs = $state<Entry[]>([]);
  let loading = $state(false);
  let error = $state('');

  async function load(p: string): Promise<void> {
    loading = true;
    error = '';
    const res = await backend.listCloud(accountID, p);
    loading = false;
    if (!res) {
      error = 'backend';
      dirs = [];
      return;
    }
    if (res.err) {
      error = res.err.message;
      dirs = [];
      return;
    }
    path = p;
    dirs = (res.entries ?? [])
      .filter((e) => e.isDir)
      .map((e) => ({
        id: e.path || e.name,
        name: e.name,
        path: e.path,
        dir: true,
        size: 0,
        mod: new Date()
      })) as Entry[];
    dirs.sort((a, b) => a.name.localeCompare(b.name));
  }

  function up(): void {
    const idx = path.lastIndexOf('/');
    load(idx > 0 ? path.slice(0, idx) : '');
  }

  function choose(): void {
    onpick(path);
    onclose();
  }

  onMount(() => load(initial));
</script>

<Modal labelledby="picker-title" onclose={onclose}>
  <div class="dialog" style="width: 560px">
    <div class="dialog-head" style="padding-bottom:2px">
      <div class="grow">
        <div class="dialog-title" id="picker-title">{t('wizard.pickFolder')}</div>
        <div class="dialog-sub" style="font:var(--type-body-s);color:var(--m3c-on-surface-variant);margin-top:2px">
          {path || t('files.cloud')}
        </div>
      </div>
      <button class="icon-button" aria-label={t('common.close')} onclick={onclose}>
        <Icon name="close" size={20} />
      </button>
    </div>

    <div class="dialog-body" style="min-height:280px">
      <div style="display:flex;gap:6px;margin-bottom:10px">
        <button class="btn text sm" onclick={up} disabled={!path}>
          <Icon name="arrow-back" size={16} />{t('common.back')}
        </button>
        <button class="btn text sm" onclick={() => load('')} disabled={!path}>
          <Icon name="cloud" size={16} />{t('files.cloud')}
        </button>
      </div>

      {#if loading}
        <div class="pane-loading">
          {#each Array(4) as _, i (i)}
            <span class="skeleton" style={`height:14px;width:${70 - i * 10}%`}></span>
          {/each}
        </div>
      {:else if error}
        <div class="banner"><Icon name="error" size={20} /><div>{error}</div></div>
      {:else if dirs.length === 0}
        <div class="empty" style="padding:40px 16px">
          <span class="art"><Icon name="folder" size={40} /></span>
          <div class="e-title">{t('wizard.noFolders')}</div>
        </div>
      {:else}
        {#each dirs as d (d.id)}
          <button class="tier-row" onclick={() => load(d.path ?? d.name)}>
            <Icon name="folder" size={20} />
            <span class="tr-n">{d.name}</span>
            <Icon name="chevron-right" size={16} />
          </button>
        {/each}
      {/if}
    </div>

    <div class="dialog-foot">
      <button class="btn text" onclick={onclose}>{t('common.cancel')}</button>
      <div class="grow"></div>
      <button class="btn filled" disabled={loading || !!error} onclick={choose}>
        {t('wizard.useThisFolder')}
      </button>
    </div>
  </div>
</Modal>
