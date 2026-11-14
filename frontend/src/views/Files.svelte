<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '$lib/components/Icon.svelte';
  import ProviderIcon from '$lib/components/ProviderIcon.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import { app } from '$lib/state.svelte';
  import { t, fmtBytes, fmtTime } from '$lib/i18n';
  import { accountName, type Account, type Entry } from '$lib/mock';
  import { data, refreshCloud, refreshLocal, refreshRoots } from '$lib/data.svelte';
  import { backend } from '$lib/backend';

  let q = $state('');
  let newFolderOpen = $state(false);
  let newFolderName = $state('');
  let deleteOpen = $state(false);
  let busy = $state(false);

  const account = $derived<Account | undefined>(
    data.accounts.find((a) => a.id === data.activeAccount) ?? data.accounts[0]
  );

  const localList = $derived(
    q ? data.localFiles.filter((e) => e.name.toLowerCase().includes(q.toLowerCase())) : data.localFiles
  );
  const cloudList = $derived(
    q ? data.cloudFiles.filter((e) => e.name.toLowerCase().includes(q.toLowerCase())) : data.cloudFiles
  );

  const selectedLocal = $derived(data.localFiles.filter((e) => app.selLocal.has(e.id)));
  const selectedCloud = $derived(data.cloudFiles.filter((e) => app.selCloud.has(e.id)));
  const hasSplit = $derived(selectedCloud.some((e) => !!e.split));
  const selCount = $derived(selectedLocal.length + selectedCloud.length);
  const selBytes = $derived(
    [...selectedLocal, ...selectedCloud].reduce((s, e) => s + (e.size || 0), 0)
  );

  function baseName(path: string, fallback: string): string {
    if (!path) return fallback;
    const parts = path.split(/[\\/]/).filter(Boolean);
    return parts[parts.length - 1] ?? path;
  }

  const rootKeys: Record<string, string> = {
    desktop: 'files.root.desktop',
    documents: 'files.root.documents',
    pictures: 'files.root.pictures',
    downloads: 'files.root.downloads',
    music: 'files.root.music'
  };

  function rootKey(id: string): string {
    return rootKeys[id] ?? 'files.root.downloads';
  }

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

  function toggleLocal(id: string, ev: Event): void {
    ev.stopPropagation();
    app.toggleLocal(id);
  }

  function pathsOf(entries: Entry[]): string[] {
    return entries.map((e) => e.path ?? e.id).filter((p): p is string => Boolean(p));
  }

  function openLocal(e: Entry): void {
    if (e.dir && e.path) {
      refreshLocal(e.path);
      app.clearSelection();
    }
  }

  function openCloud(e: Entry): void {
    if (e.split && account) {
      app.restoreAccount = account.id;
      app.restorePath = e.path ?? e.id;
      app.go('files/restore');
      return;
    }
    if (e.dir && e.path && account) {
      refreshCloud(account.id, e.path);
      app.clearSelection();
    }
  }

  function upLocal(): void {
    if (!data.localPath) return;
    if (data.localPath === data.localRoot) return;
    const idx = Math.max(data.localPath.lastIndexOf('\\'), data.localPath.lastIndexOf('/'));
    if (idx > 0) refreshLocal(data.localPath.slice(0, idx));
  }

  function upCloud(): void {
    if (!account || !data.cloudPath) return;
    const idx = data.cloudPath.lastIndexOf('/');
    refreshCloud(account.id, idx > 0 ? data.cloudPath.slice(0, idx) : '');
  }

  function switchAccount(id: string): void {
    data.activeAccount = id;
    app.clearSelection();
    refreshCloud(id, '');
    const el = document.getElementById('account-menu');
    if (el && 'hidePopover' in el) (el as HTMLElement).hidePopover();
  }

  async function upload(): Promise<void> {
    if (!account || selectedLocal.length === 0) return;
    busy = true;
    const res = (await backend.upload(account.id, data.cloudPath, pathsOf(selectedLocal))) as
      | { err?: { message?: string } }
      | null;
    busy = false;
    if (res?.err) {
      app.notify(res.err.message ?? 'upload failed');
      return;
    }
    app.clearSelection();
    app.notify(t('files.uploadStarted', { n: selectedLocal.length }));
    app.go('activity');
  }

  async function download(): Promise<void> {
    if (!account || selectedCloud.length === 0) return;
    busy = true;
    const res = (await backend.download(
      account.id,
      pathsOf(selectedCloud),
      data.localPath
    )) as { err?: { message?: string } } | null;
    busy = false;
    if (res?.err) {
      app.notify(res.err.message ?? 'download failed');
      return;
    }
    app.clearSelection();
    app.notify(t('files.downloadStarted', { n: selectedCloud.length }));
    app.go('activity');
  }

  async function removeSelected(): Promise<void> {
    if (!account || selectedCloud.length === 0) return;
    busy = true;
    const res = (await backend.deleteCloud(account.id, pathsOf(selectedCloud))) as
      | { err?: { message?: string } }
      | null;
    busy = false;
    deleteOpen = false;
    if (res?.err) {
      app.notify(res.err.message ?? 'delete failed');
      return;
    }
    app.clearSelection();
    await refreshCloud(account.id, data.cloudPath);
  }

  async function createFolder(): Promise<void> {
    if (!account || !newFolderName.trim()) return;
    busy = true;
    await backend.createFolder(account.id, data.cloudPath, newFolderName.trim());
    busy = false;
    newFolderOpen = false;
    newFolderName = '';
    await refreshCloud(account.id, data.cloudPath);
  }

  onMount(() => {
    refreshRoots();
  });

  let listedFor = '';
  $effect(() => {
    const id = account?.id;
    if (id && id !== listedFor) {
      listedFor = id;
      refreshCloud(id, '');
    }
  });
</script>

<div class="files">
  <div class="files-head">
    <span class="files-title">{t('nav.files')}</span>
    <span class="hint">{t('files.dragTip')}</span>
    <span class="grow"></span>
    <div class="search">
      <Icon name="search" size={18} />
      <input type="text" placeholder={t('files.search')} aria-label={t('files.search')} bind:value={q} />
      {#if q}
        <button class="icon-button" style="width:24px;height:24px" aria-label="clear" onclick={() => (q = '')}>
          <Icon name="close" size={16} />
        </button>
      {/if}
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
        <button class="crumb" onclick={upLocal} disabled={!data.localRoot || data.localPath === data.localRoot}>
          <Icon name="arrow-back" size={15} />
        </button>
        <button class="crumb" onclick={() => data.localRoot && refreshLocal(data.localRoot)}>
          <Icon name="folder-open" size={15} />{baseName(data.localPath, app.lang === 'en' ? 'Downloads' : '下載')}
        </button>
        <span class="grow"></span>
        <button class="icon-button" aria-label="More" style="width:32px;height:32px">
          <Icon name="more-vert" size={18} />
        </button>
      </div>

      <div class="pane-toolbar">
        <button class="btn tonal sm" disabled={selectedLocal.length === 0 || busy} onclick={upload}>
          <Icon name="cloud-upload" size={16} /><span>{t('files.upload')}</span>
        </button>
        {#if data.roots.length}
          {#each data.roots as r (r.id)}
            <button class="chip interactive" class:selected={data.localPath === r.path}
              onclick={() => { refreshLocal(r.path); app.clearSelection(); }}>
              {t(rootKey(r.id))}
            </button>
          {/each}
        {:else}
          {#each [['desktop', 'files.root.desktop'], ['documents', 'files.root.documents'], ['pictures', 'files.root.pictures'], ['downloads', 'files.root.downloads'], ['music', 'files.root.music']] as [id, key] (id)}
            <button class="chip interactive" class:selected={id === 'downloads'}>{t(key)}</button>
          {/each}
        {/if}
        <span class="spacer"></span>
        <button class="icon-button" aria-label={t('files.newFolder')} disabled={!account} onclick={() => (newFolderOpen = true)}>
          <Icon name="create-new-folder" size={20} />
        </button>
      </div>

      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="pane-table" tabindex="0" role="region" aria-label={`${t('files.computer')} · ${baseName(data.localPath, '')}`}>
        {#if data.localLoading}
          <div class="pane-loading">
            {#each Array(7) as _, i (i)}
              <span class="skeleton" style={`height:14px;width:${72 - i * 4}%`}></span>
            {/each}
          </div>
        {:else if data.localFiles.length === 0}
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
              {#each localList as e (e.id)}
                <tr class:selected={app.selLocal.has(e.id)} onclick={() => app.toggleLocal(e.id)}
                  ondblclick={() => openLocal(e)}>
                  <td class="c-check">
                    <span class="cb">
                      <input type="checkbox" checked={app.selLocal.has(e.id)} aria-label={e.name}
                        onclick={(ev) => toggleLocal(e.id, ev)} />
                    </span>
                  </td>
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
        {/if}
      </div>
    </section>

    <div class="splitter" role="separator" aria-label="resize"></div>

    <section class="pane" id="drop-cloud">
      <div class="pane-head">
        {#if account}
          <button class="crumb" style="height:28px;background:var(--m3c-surface-container-high);anchor-name:--account-chip"
            popovertarget="account-menu">
            <span class="ptile {account.provider}" style="width:20px;height:20px;border-radius:6px">
              <ProviderIcon provider={account.provider} size={14} />
            </span>
            <span>{accountName(account, app.lang)}</span>
            <Icon name="expand-more" size={16} />
          </button>
          <div id="account-menu" popover class="account-menu" style="position-anchor:--account-chip">
            {#each data.accounts as a (a.id)}
              <button onclick={() => switchAccount(a.id)}>
                <span class="ptile {a.provider}" style="width:20px;height:20px;border-radius:6px">
                  <ProviderIcon provider={a.provider} size={14} />
                </span>
                <span class="grow">{accountName(a, app.lang)}</span>
                {#if a.id === account.id}<Icon name="check" size={16} />{/if}
              </button>
            {/each}
            <hr class="divider" />
            <button onclick={() => app.go('clouds/add')}>
              <Icon name="add" size={18} />{t('clouds.add')}
            </button>
          </div>
        {:else}
          <button class="crumb" onclick={() => app.go('clouds/add')}>
            <Icon name="add" size={15} />{t('clouds.add')}
          </button>
        {/if}
        <span class="crumb-sep"><Icon name="chevron-right" size={16} /></span>
        <button class="crumb" onclick={() => account && refreshCloud(account.id, '')}>
          <Icon name="cloud" size={15} />{t('files.cloud')}
        </button>
        {#if data.cloudPath}
          <span class="crumb-sep"><Icon name="chevron-right" size={15} /></span>
          <button class="crumb" onclick={upCloud}><Icon name="arrow-back" size={15} /></button>
          <span class="crumb"><Icon name="folder-open" size={15} />{baseName(data.cloudPath, data.cloudPath)}</span>
        {/if}
        <span class="grow"></span>
        <button class="icon-button" aria-label="More" style="width:32px;height:32px">
          <Icon name="more-vert" size={18} />
        </button>
      </div>

      <div class="pane-toolbar">
        <button class="btn filled sm" disabled={selectedCloud.length === 0 || busy || !account}
          onclick={() => (hasSplit ? app.go('files/restore') : download())}>
          <Icon name={hasSplit ? 'history' : 'cloud-download'} size={16} />
          <span>{hasSplit ? t('files.restore') : t('files.download')}</span>
        </button>
        <span class="spacer"></span>
        <button class="icon-button" aria-label={t('common.retry')} disabled={!account || data.cloudLoading}
          onclick={() => account && refreshCloud(account.id, data.cloudPath, true)}>
          <Icon name="refresh" size={20} />
        </button>
        <button class="icon-button" aria-label={t('files.newFolder')} disabled={!account} onclick={() => (newFolderOpen = true)}>
          <Icon name="create-new-folder" size={20} />
        </button>
        <button class="icon-button" aria-label={t('files.delete')} disabled={selectedCloud.length === 0 || busy}
          onclick={() => (deleteOpen = true)}>
          <Icon name="delete" size={20} />
        </button>
      </div>

      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="pane-table" tabindex="0" role="region" aria-label={`${account ? accountName(account, app.lang) : t('files.cloud')} · ${t('files.folder.backup')}`}>
        {#if data.cloudLoading}
          <div class="pane-loading">
            {#each Array(7) as _, i (i)}
              <span class="skeleton" style={`height:14px;width:${72 - i * 4}%`}></span>
            {/each}
            {#if data.cloudSlow}
              <div class="hint" style="margin-top:6px">{t('files.slow')}</div>
            {/if}
          </div>
        {:else if !account}
          <div class="empty" style="padding-top:72px">
            <span class="art"><Icon name="cloud" size={44} /></span>
            <div class="e-title">{t('clouds.empty')}</div>
            <div class="e-body">{t('onboarding.sub')}</div>
            <button class="btn filled" onclick={() => app.go('clouds/add')}>
              <Icon name="add" size={18} /><span>{t('clouds.add')}</span>
            </button>
          </div>
        {:else if data.cloudError}
          <div class="empty" style="padding-top:72px">
            <span class="art"><Icon name="cloud-off" size={44} /></span>
            <div class="e-title">{t('files.cloudError')}</div>
            <div class="e-body">{data.cloudError}</div>
            <button class="btn tonal" onclick={() => refreshCloud(account.id, data.cloudPath)}>
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
              {#each cloudList as e (e.id)}
                <tr class:selected={app.selCloud.has(e.id)} onclick={() => app.toggleCloud(e.id)}
                  ondblclick={() => openCloud(e)}>
                  <td class="c-check">
                    <span class="cb">
                      <input type="checkbox" checked={app.selCloud.has(e.id)} aria-label={e.name}
                        onclick={(ev) => { ev.stopPropagation(); app.toggleCloud(e.id); }} />
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
        {selCount
          ? t('files.selected', { n: selCount, size: fmtBytes(selBytes) })
          : t('files.items', { n: (data.cloudFiles.length || data.localFiles.length).toLocaleString('en-US') })}
      </span>
    </span>
    <span class="nums">
      {t('files.items', { n: data.localFiles.length.toLocaleString('en-US') })} ·
      {t('files.items', { n: data.cloudFiles.length.toLocaleString('en-US') })}
    </span>
  </div>
</div>

{#if newFolderOpen}
  <Modal labelledby="newfolder-title" onclose={() => (newFolderOpen = false)}>
    <div class="dialog" style="width: 460px">
      <div class="dialog-head" style="padding-bottom:2px">
        <div class="grow"><div class="dialog-title" id="newfolder-title">{t('files.newFolder')}</div></div>
        <button class="icon-button" aria-label={t('common.close')} onclick={() => (newFolderOpen = false)}>
          <Icon name="close" size={20} />
        </button>
      </div>
      <div class="dialog-body">
        <input class="textfield" style="width:100%" placeholder={t('files.name')} bind:value={newFolderName}
          onkeydown={(e) => e.key === 'Enter' && createFolder()} />
      </div>
      <div class="dialog-foot">
        <button class="btn text" onclick={() => (newFolderOpen = false)}>{t('common.cancel')}</button>
        <div class="grow"></div>
        <button class="btn filled" disabled={!newFolderName.trim() || busy} onclick={createFolder}>
          {t('common.done')}
        </button>
      </div>
    </div>
  </Modal>
{/if}

{#if deleteOpen}
  <Modal labelledby="delete-title" onclose={() => (deleteOpen = false)}>
    <div class="dialog" style="width: 520px">
      <div class="dialog-head" style="padding-bottom:2px">
        <div class="grow">
          <div class="dialog-title" id="delete-title">
            {t('files.deleteTitle', { n: selectedCloud.length })}
          </div>
        </div>
        <button class="icon-button" aria-label={t('common.close')} onclick={() => (deleteOpen = false)}>
          <Icon name="close" size={20} />
        </button>
      </div>
      <div class="dialog-body">
        <p style="margin:4px 0 8px;color:var(--m3c-on-surface-variant)">{t('files.deleteBody')}</p>
      </div>
      <div class="dialog-foot">
        <!-- svelte-ignore a11y_autofocus -->
        <button class="btn text" autofocus onclick={() => (deleteOpen = false)}>{t('common.cancel')}</button>
        <div class="grow"></div>
        <button class="btn danger-filled" disabled={busy} onclick={removeSelected}>
          <Icon name="delete" size={18} /><span>{t('files.delete')}</span>
        </button>
      </div>
    </div>
  </Modal>
{/if}
