<script lang="ts">
  import Icon from '$lib/components/Icon.svelte';
  import ProviderIcon from '$lib/components/ProviderIcon.svelte';
  import { onMount } from 'svelte';
  import { app } from '$lib/state.svelte';
  import { t, fmtBytes, fmtTime } from '$lib/i18n';
  import { accountName, type Account, type Entry } from '$lib/mock';
  import { data, refreshCloud, refreshRoots } from '$lib/data.svelte';

  const account = $derived<Account | undefined>(data.accounts[0]);
  const selected = $derived(app.selCloud);
  const selectedEntries = $derived(data.cloudFiles.filter((e) => selected.has(e.id)));
  const hasSplit = $derived(selectedEntries.some((e) => !!e.split));
  const selBytes = $derived(selectedEntries.reduce((s, e) => s + (e.size || 0), 0));

  onMount(() => {
    refreshRoots();
  });

  let listedFor = '';
  $effect(() => {
    const id = data.accounts[0]?.id;
    if (id && id !== listedFor) {
      listedFor = id;
      refreshCloud(id, '');
    }
  });

  function fileIcon(e: Entry): { icon: string; cls: string } {
    if (e.dir) return { icon: 'folder', cls: '' };
    const map: Record<string, [string, string]> = {
      img: ['image', 'icon-img'], vid: ['movie', 'icon-vid'],
      aud: ['music-note', 'icon-aud'], pdf: ['picture-as-pdf', 'icon-doc'],
      doc: ['description', 'icon-doc'], zip: ['archive', 'icon-zip']
    };
    const m = map[e.kind ?? 'doc'] ?? ['description', 'icon-zip'];
    return { icon: m[0], cls: m[1] };
  }

  function splitLabel(e: Entry): string {
    switch (e.split) {
      case 'broken': return t('files.split.broken');
      case 'incomplete': return t('files.split.incomplete');
      case 'foreign': return t('files.split.foreign');
      default: return t('files.split.ready');
    }
  }

  function toggle(id: string, e: Event): void {
    e.stopPropagation();
    app.toggleCloud(id);
  }

  function openRow(e: Entry): void {
    if (e.split) app.go('files/restore');
  }

  function baseName(path: string): string {
    if (!path) return app.lang === 'en' ? 'Downloads' : '下載';
    const parts = path.split(/[\\/]/).filter(Boolean);
    return parts[parts.length - 1] ?? path;
  }
</script>

<div class="files">
  <div class="files-head">
    <span class="files-title">{t('nav.files')}</span>
    <span class="hint">{t('files.dragTip')}</span>
    <span class="grow"></span>
    <div class="search">
      <Icon name="search" size={18} />
      <input type="text" placeholder={t('files.search')} aria-label={t('files.search')} />
    </div>
    <button class="icon-button" aria-label={t('common.view')}>
      <Icon name="table-chart" size={20} />
    </button>
  </div>

  <div class="files-main">
    <section class="pane" id="drop-local">
      <div class="pane-head">
        <span class="pane-title" style="display:flex;align-items:center;gap:7px">
          <Icon name="computer" size={18} />{t('files.computer')}
        </span>
        <span style="color:var(--m3c-outline)">/</span>
        <span class="crumb"><Icon name="folder-open" size={15} />{baseName(data.localPath)}</span>
        <span class="grow"></span>
        <button class="icon-button" aria-label="More" style="width:32px;height:32px">
          <Icon name="more-vert" size={18} />
        </button>
      </div>

      <div class="pane-toolbar">
        <button class="btn tonal sm" disabled>
          <Icon name="cloud-upload" size={16} /><span>{t('files.upload')}</span>
        </button>
        {#each [['desktop', 'files.root.desktop'], ['documents', 'files.root.documents'], ['pictures', 'files.root.pictures'], ['downloads', 'files.root.downloads'], ['music', 'files.root.music']] as [id, key] (id)}
          <button class="chip interactive" class:selected={id === 'downloads'}>{t(key)}</button>
        {/each}
        <span class="spacer"></span>
        <button class="icon-button" aria-label={t('files.newFolder')}><Icon name="create-new-folder" size={20} /></button>
      </div>

      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="pane-table" tabindex="0" role="region" aria-label={`${t('files.computer')} · ${baseName(data.localPath)}`}>
        <table class="table">
          <colgroup><col style="width:40px" /><col /><col style="width:92px" /><col style="width:148px" /></colgroup>
          <thead>
            <tr>
              <th class="c-check" scope="col"><span class="sr-only">{t('files.select')}</span></th>
              <th scope="col">{t('files.name')}</th>
              <th class="c-size" scope="col">{t('files.size')}</th>
              <th class="c-time" scope="col">{t('files.modified')}</th>
            </tr>
          </thead>
          <tbody>
            {#each data.localFiles as e (e.id)}
              <tr>
                <td class="c-check"><span class="cb"><input type="checkbox" aria-label={e.name} /></span></td>
                <td>
                  <div class="file-name-cell">
                    <span class="i {fileIcon(e).cls}"><Icon name={fileIcon(e).icon} size={18} /></span>
                    <span class="cname">{e.name}</span>
                  </div>
                </td>
                <td class="c-size nums">{e.dir ? '—' : fmtBytes(e.size)}</td>
                <td class="c-time">{fmtTime(e.mod)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>

    <div class="splitter" role="separator" aria-label="resize"></div>

    <section class="pane" id="drop-cloud">
      <div class="pane-head">
        <button class="crumb" style="height:28px;background:var(--m3c-surface-container-high)">
          <span class="ptile {account?.provider ?? 'webdav'}" style="width:20px;height:20px;border-radius:6px">
            <ProviderIcon provider={account?.provider ?? 'webdav'} size={14} />
          </span>
          <span>{account ? accountName(account, app.lang) : t('files.cloud')}</span>
          <Icon name="expand-more" size={16} />
        </button>
        <span class="crumb-sep"><Icon name="chevron-right" size={16} /></span>
        <span class="crumb"><Icon name="cloud" size={15} />{t('files.cloud')}</span>
        {#if data.cloudPath}
          <span class="crumb-sep"><Icon name="chevron-right" size={15} /></span>
          <span class="crumb"><Icon name="folder-open" size={15} />{data.cloudPath}</span>
        {/if}
        <span class="grow"></span>
        <button class="icon-button" aria-label="More" style="width:32px;height:32px">
          <Icon name="more-vert" size={18} />
        </button>
      </div>

      <div class="pane-toolbar">
        <button class="btn filled sm" disabled={selectedEntries.length === 0}
          onclick={() => (hasSplit ? app.go('files/restore') : app.notify(t('files.download')))}>
          <Icon name={hasSplit ? 'history' : 'cloud-download'} size={16} />
          <span>{hasSplit ? t('files.restore') : t('files.download')}</span>
        </button>
        <span class="spacer"></span>
        <button class="icon-button" aria-label={t('files.newFolder')}><Icon name="create-new-folder" size={20} /></button>
        <button class="icon-button" aria-label={t('files.delete')}><Icon name="delete" size={20} /></button>
      </div>

      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="pane-table" tabindex="0" role="region" aria-label={`${account ? accountName(account, app.lang) : t('files.cloud')} · ${t('files.folder.backup')}`}>
        {#if data.cloudLoading}
          <div class="pane-loading">
            {#each Array(7) as _, i (i)}
              <span class="skeleton" style={`height:14px;width:${72 - i * 4}%`}></span>
            {/each}
          </div>
        {:else if data.cloudError}
          <div class="empty" style="padding-top:72px">
            <span class="art"><Icon name="cloud-off" size={44} /></span>
            <div class="e-title">{t('files.cloudError')}</div>
            <div class="e-body">{data.cloudError}</div>
            <button class="btn tonal" onclick={() => refreshCloud(account?.id ?? '', '')}>
              <Icon name="refresh" size={18} /><span>{t('common.retry')}</span>
            </button>
          </div>
        {:else if data.cloudFiles.length === 0}
          <div class="empty" style="padding-top:72px">
            <span class="art"><Icon name="folder" size={44} /></span>
            <div class="e-title">{t('files.empty.title')}</div>
            <div class="e-body">{t('files.empty.body')}</div>
          </div>
        {:else}
        <table class="table">
          <colgroup><col style="width:40px" /><col /><col style="width:92px" /><col style="width:148px" /></colgroup>
          <thead>
            <tr>
              <th class="c-check" scope="col"><span class="sr-only">{t('files.select')}</span></th>
              <th scope="col">{t('files.name')}</th>
              <th class="c-size" scope="col">{t('files.size')}</th>
              <th class="c-time" scope="col">{t('files.modified')}</th>
            </tr>
          </thead>
          <tbody>
            {#each data.cloudFiles as e (e.id)}
              <tr class:selected={selected.has(e.id)} onclick={() => app.toggleCloud(e.id)} ondblclick={() => openRow(e)}>
                <td class="c-check">
                  <span class="cb">
                    <input type="checkbox" checked={selected.has(e.id)} aria-label={e.name}
                      onclick={(ev) => toggle(e.id, ev)} />
                  </span>
                </td>
                <td>
                  <div class="file-name-cell">
                    <span class="i {fileIcon(e).cls}"><Icon name={fileIcon(e).icon} size={18} /></span>
                    <span class="cname">{e.name}</span>
                    {#if e.split}
                      <span class="chip split-chip" class:ready={e.split === 'ready'} class:broken={e.split === 'broken'} class:incomplete={e.split === 'incomplete'}>
                        <Icon name={e.split === 'broken' ? 'error' : e.split === 'incomplete' ? 'schedule' : 'verified'} size={14} />
                        {splitLabel(e)}
                      </span>
                    {/if}
                  </div>
                </td>
                <td class="c-size nums">{e.dir ? '—' : fmtBytes(e.size)}</td>
                <td class="c-time">{fmtTime(e.mod)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
        {/if}
      </div>
    </section>
  </div>

  <div class="statusbar">
    <span class="sb-left">
      <Icon name="info" size={15} />
      <span>
        {selectedEntries.length
          ? t('files.selected', { n: selectedEntries.length, size: fmtBytes(selBytes) })
          : t('files.items', { n: '1,284' })}
      </span>
    </span>
    <span class="nums">{t('files.items', { n: '1,284' })} · {fmtBytes(12.4 * 1024 ** 3)}</span>
  </div>
</div>
