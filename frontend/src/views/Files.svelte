<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import Icon from '$lib/components/Icon.svelte';
  import ProviderIcon from '$lib/components/ProviderIcon.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import CloudFolderPicker from './CloudFolderPicker.svelte';
  import { app } from '$lib/state.svelte';
  import { t, fmtBytes, fmtTime } from '$lib/i18n';
  import { accountName, type Account, type Entry } from '$lib/mock';
  import { data, refreshCloud, refreshLocal, refreshRoots } from '$lib/data.svelte';
  import { backend, isWails } from '$lib/backend';
  import { Events } from '@wailsio/runtime';

  type PaneKind = 'cloud' | 'local';
  type DestMode = 'move' | 'copy';

  interface FailLike {
    message?: string;
    detail?: string;
  }
  interface EmptyRes {
    err?: FailLike | null;
  }
  interface OpRes {
    done: number;
    failed?: string[];
    err?: FailLike | null;
  }
  interface DetailRes {
    path: string;
    name: string;
    size: number;
    modTime: string;
    isDir: boolean;
    sha256?: string;
    md5?: string;
    err?: FailLike | null;
  }
  interface ShareRes {
    url: string;
    err?: FailLike | null;
  }
  interface SearchHitView {
    path: string;
    name: string;
    size: number;
    modTime?: string;
    split?: boolean;
  }
  interface SearchRes {
    items: SearchHitView[];
    truncated: boolean;
    err?: FailLike | null;
  }
  interface CheckRes {
    same: number;
    differ: number;
    missing: number;
    status?: string;
    examples?: string[];
    err?: FailLike | null;
  }
  interface PickRes {
    path?: string;
    paths?: string[];
    cancelled: boolean;
    err?: FailLike | null;
  }

  let q = $state('');
  let newFolderOpen = $state(false);
  let newFolderName = $state('');
  let deleteOpen = $state(false);
  let busy = $state(false);

  /** Which pane last had focus; the whole-cloud search button uses this. */
  let focusPane = $state<PaneKind>('cloud');

  // Rename -----------------------------------------------------------------
  let renameTarget = $state<{ kind: PaneKind; entry: Entry } | null>(null);
  let renameName = $state('');
  let renameError = $state('');
  let renameBusy = $state(false);

  // Move / copy destination -------------------------------------------------
  let destOpen = $state<{ mode: DestMode; kind: PaneKind; entries: Entry[] } | null>(null);
  let destChoice = $state<'current' | 'account' | 'folder'>('current');
  let destAccount = $state('');
  let destFolder = $state('');
  let destError = $state('');
  let destBusy = $state(false);
  let pickerFor = $state<'dest' | 'verify' | null>(null);

  // Details ----------------------------------------------------------------
  let detailEntry = $state<Entry | null>(null);
  let detailLoading = $state(false);
  let detailError = $state('');
  let detailData = $state<{ name: string; size: number; mod: Date; sha256: string; md5: string } | null>(null);
  let copiedField = $state<'sha256' | 'md5' | ''>('');
  let copyTimer: ReturnType<typeof setTimeout> | undefined;

  // Share link -------------------------------------------------------------
  let shareEntry = $state<Entry | null>(null);
  let shareUrl = $state('');
  let shareError = $state('');
  let shareLoading = $state(false);
  let shareCopied = $state(false);
  let shareTimer: ReturnType<typeof setTimeout> | undefined;

  // Empty trash ------------------------------------------------------------
  let cleanupOpen = $state(false);
  let cleanupBusy = $state(false);
  let cleanupError = $state('');

  // Compare backup ---------------------------------------------------------
  let verifyOpen = $state(false);
  let verifyRoot = $state('');
  let verifyBusy = $state(false);
  let verifyError = $state('');
  let verifyResult = $state<Required<Pick<CheckRes, 'same' | 'differ' | 'missing' | 'examples'>> & { status: string } | null>(null);

  // Whole-cloud search -----------------------------------------------------
  let searchOpen = $state(false);
  let searchLoading = $state(false);
  let searchError = $state('');
  let searchQuery = $state('');
  let searchHits = $state<SearchHitView[]>([]);
  let searchTruncated = $state(false);

  const account = $derived<Account | undefined>(
    data.accounts.find((a) => a.id === data.activeAccount) ?? data.accounts[0]
  );
  const accountLabel = $derived(account ? accountName(account, app.lang) : '');
  const localLabel = $derived(baseName(data.localPath, t('files.computer')));

  type SortKey = 'name' | 'size' | 'time';
  const PAGE_STEP = 400;
  let sortKey = $state<SortKey>('name');
  let sortAsc = $state(true);
  let localLimit = $state(PAGE_STEP);
  let cloudLimit = $state(PAGE_STEP);
  // Selection anchors for shift-click ranges.
  let anchorLocal = -1;
  let anchorCloud = -1;
  // Which pane the keyboard shortcuts apply to.
  let activePane = $state<'local' | 'cloud'>('cloud');

  function sortEntries(list: Entry[]): Entry[] {
    const out = list.slice();
    out.sort((a, b) => {
      // Folders stay above files, like Explorer.
      if (!!a.dir !== !!b.dir) return a.dir ? -1 : 1;
      let cmp = 0;
      if (sortKey === 'name') {
        cmp = a.name.localeCompare(b.name, app.lang === 'en' ? 'en' : 'zh-Hant', { numeric: true });
      } else if (sortKey === 'size') {
        cmp = (a.size || 0) - (b.size || 0);
      } else {
        cmp = (a.mod?.getTime?.() ?? 0) - (b.mod?.getTime?.() ?? 0);
      }
      return sortAsc ? cmp : -cmp;
    });
    return out;
  }

  function setSort(key: SortKey): void {
    if (sortKey === key) sortAsc = !sortAsc;
    else {
      sortKey = key;
      sortAsc = true;
    }
  }

  const localList = $derived(
    sortEntries(q ? data.localFiles.filter((e) => e.name.toLowerCase().includes(q.toLowerCase())) : data.localFiles)
  );
  const cloudList = $derived(
    sortEntries(q ? data.cloudFiles.filter((e) => e.name.toLowerCase().includes(q.toLowerCase())) : data.cloudFiles)
  );
  // Large folders render incrementally: a few hundred rows at a time keeps the
  // first paint fast without hiding anything from the user.
  const localShown = $derived(localList.slice(0, localLimit));
  const cloudShown = $derived(cloudList.slice(0, cloudLimit));

  $effect(() => {
    data.localPath;
    q;
    localLimit = PAGE_STEP;
    anchorLocal = -1;
  });
  $effect(() => {
    data.cloudPath;
    data.activeAccount;
    q;
    cloudLimit = PAGE_STEP;
    anchorCloud = -1;
  });

  function toggleLocalRow(entry: Entry, ev: MouseEvent): void {
    activePane = 'local';
    const list = localShown;
    const idx = list.findIndex((e) => e.id === entry.id);
    if (ev.shiftKey && anchorLocal >= 0 && idx >= 0) {
      const [from, to] = anchorLocal < idx ? [anchorLocal, idx] : [idx, anchorLocal];
      app.selLocal = new Set(list.slice(from, to + 1).map((e) => e.id));
      return;
    }
    anchorLocal = idx;
    app.toggleLocal(entry.id);
  }

  function toggleCloudRow(entry: Entry, ev: MouseEvent): void {
    activePane = 'cloud';
    const list = cloudShown;
    const idx = list.findIndex((e) => e.id === entry.id);
    if (ev.shiftKey && anchorCloud >= 0 && idx >= 0) {
      const [from, to] = anchorCloud < idx ? [anchorCloud, idx] : [idx, anchorCloud];
      app.selCloud = new Set(list.slice(from, to + 1).map((e) => e.id));
      return;
    }
    anchorCloud = idx;
    app.toggleCloud(entry.id);
  }

  function onFilesKeydown(ev: KeyboardEvent): void {
    const target = ev.target as HTMLElement | null;
    if (target?.closest('input, textarea, select, [contenteditable="true"]')) return;
    if (document.querySelector('dialog[open]')) return;
    if (ev.key === 'Escape') {
      app.clearSelection();
      return;
    }
    if ((ev.ctrlKey || ev.metaKey) && ev.key.toLowerCase() === 'a') {
      ev.preventDefault();
      if (activePane === 'local') app.selLocal = new Set(localList.map((e) => e.id));
      else app.selCloud = new Set(cloudList.map((e) => e.id));
    }
  }

  const selectedLocal = $derived(data.localFiles.filter((e) => app.selLocal.has(e.id)));
  const selectedCloud = $derived(data.cloudFiles.filter((e) => app.selCloud.has(e.id)));
  const hasSplit = $derived(selectedCloud.some((e) => !!e.split));
  const selCount = $derived(selectedLocal.length + selectedCloud.length);
  const selBytes = $derived(
    [...selectedLocal, ...selectedCloud].reduce((s, e) => s + (e.size || 0), 0)
  );

  const renameValidation = $derived.by(() => {
    if (!renameTarget) return '';
    const name = renameName.trim();
    if (!name) return t('files.nameRequired');
    if (name === '.' || name === '..' || /[\\/:*?"<>|]/.test(name)) {
      return t('files.nameInvalid');
    }
    return '';
  });

  const destSourceAccount = $derived(account?.id ?? '');
  const destOtherAccounts = $derived(data.accounts.filter((a) => a.id !== destSourceAccount));

  const destResolvedAccount = $derived.by(() => {
    if (!destOpen || destOpen.kind === 'local') return '';
    if (destChoice === 'account') return destAccount;
    return destSourceAccount;
  });

  const destResolvedDir = $derived.by(() => {
    if (!destOpen) return '';
    if (destOpen.kind === 'local') return destFolder;
    if (destChoice === 'current') return data.cloudPath;
    if (destChoice === 'folder') return destFolder;
    return '';
  });

  const destCopyOnly = $derived(
    !!destOpen &&
    destOpen.kind === 'cloud' &&
    (destOpen.mode === 'copy' ||
      (destOpen.mode === 'move' && destResolvedAccount !== destSourceAccount))
  );

  const destValid = $derived.by(() => {
    if (!destOpen) return false;
    if (destOpen.kind === 'local') {
      return !!destFolder && !sameLocalPath(destFolder, data.localPath);
    }
    if (destChoice === 'account') {
      return !!destAccount && destAccount !== destSourceAccount;
    }
    if (destChoice === 'folder') {
      return !!destFolder && destFolder !== data.cloudPath;
    }
    return destOpen.mode === 'move';
  });

  const destHint = $derived.by(() => {
    if (!destOpen) return '';
    if (destOpen.kind === 'local') {
      if (!destFolder) return t('files.chooseFolderFirst');
      if (sameLocalPath(destFolder, data.localPath)) return t('files.sameFolder');
      return '';
    }
    if (destChoice === 'account' && destOtherAccounts.length === 0) {
      return t('files.copyNeedsOther');
    }
    if (destChoice === 'folder') {
      if (!destFolder) return t('files.chooseFolderFirst');
      if (destFolder === data.cloudPath) return t('files.sameFolder');
    }
    return '';
  });

  function baseName(path: string, fallback: string): string {
    if (!path) return fallback;
    const parts = path.split(/[\\/]/).filter(Boolean);
    return parts[parts.length - 1] ?? path;
  }

  function sameLocalPath(a: string, b: string): boolean {
    const clean = (p: string) => p.replace(/[\\/]+$/, '').toLowerCase();
    return clean(a) === clean(b);
  }

  function failText(err?: FailLike | null): string {
    return err?.detail || err?.message || t('files.backendError');
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

  function menuEntries(kind: PaneKind, entry: Entry): Entry[] {
    if (kind === 'cloud') {
      return app.selCloud.has(entry.id) && selectedCloud.length > 1 ? selectedCloud : [entry];
    }
    return app.selLocal.has(entry.id) && selectedLocal.length > 1 ? selectedLocal : [entry];
  }

  function closeMenu(id: string): void {
    const el = document.getElementById(id);
    if (el && 'hidePopover' in el) (el as HTMLElement).hidePopover();
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

  // Files dropped from Explorer arrive as real paths through Wails. The pane
  // under the pointer decides what happens; only the cloud pane accepts them.
  function handleDrop(ev: { data?: { files?: string[]; x?: number; y?: number } }): void {
    const files = ev.data?.files ?? [];
    if (files.length === 0) return;
    const hit = document.elementFromPoint(ev.data?.x ?? 0, ev.data?.y ?? 0);
    const pane = hit?.closest('#drop-cloud, #drop-local');
    if (pane?.id === 'drop-local') {
      app.notify(t('files.dropLocalHint'));
      return;
    }
    dropUpload(files);
  }

  async function dropUpload(paths: string[]): Promise<void> {
    if (!account) {
      app.notify(t('files.noAccount'));
      return;
    }
    const res = (await backend.upload(account.id, data.cloudPath, paths)) as
      | { err?: { message?: string } }
      | null;
    if (res?.err) {
      app.notify(res.err.message ?? 'upload failed');
      return;
    }
    app.notify(t('files.dropUploaded', { n: paths.length }));
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
    const count = selectedCloud.length;
    busy = true;
    const res = (await backend.deleteCloud(account.id, pathsOf(selectedCloud))) as
      | { err?: FailLike | null }
      | null;
    busy = false;
    deleteOpen = false;
    if (res?.err) {
      app.notify(failText(res.err));
      return;
    }
    app.clearSelection();
    await refreshCloud(account.id, data.cloudPath, true);
    app.notify(t('files.deleted', { n: count }));
  }

  async function createFolder(): Promise<void> {
    if (!account || !newFolderName.trim()) return;
    busy = true;
    await backend.createFolder(account.id, data.cloudPath, newFolderName.trim());
    busy = false;
    newFolderOpen = false;
    newFolderName = '';
    app.clearSelection();
    await refreshCloud(account.id, data.cloudPath, true);
    app.notify(t('files.folderCreated'));
  }

  // ---------------------------------------------------------------- rename

  function openRename(kind: PaneKind, entry: Entry): void {
    renameTarget = { kind, entry };
    renameName = entry.name;
    renameError = '';
  }

  async function submitRename(): Promise<void> {
    if (!renameTarget || renameBusy || renameValidation) return;
    const target = renameTarget;
    const name = renameName.trim();
    renameBusy = true;
    renameError = '';
    try {
      if (target.kind === 'cloud') {
        if (!account) return;
        const res = (await backend.renameCloud(
          account.id,
          target.entry.path ?? target.entry.id,
          name
        )) as EmptyRes | null;
        if (res?.err) {
          renameError = failText(res.err);
          return;
        }
        renameTarget = null;
        app.clearSelection();
        await refreshCloud(account.id, data.cloudPath, true);
        app.notify(t('files.renamed'));
      } else {
        const res = (await backend.renameLocal(
          target.entry.path ?? target.entry.id,
          name
        )) as EmptyRes | null;
        if (res?.err) {
          renameError = failText(res.err);
          return;
        }
        renameTarget = null;
        app.clearSelection();
        await refreshLocal(data.localPath);
        app.notify(t('files.renamed'));
      }
    } finally {
      renameBusy = false;
    }
  }

  // --------------------------------------------------------- move and copy

  function openDest(mode: DestMode, kind: PaneKind, entries: Entry[]): void {
    if (entries.length === 0) return;
    if (kind === 'cloud' && !account) return;
    destOpen = { mode, kind, entries };
    destError = '';
    destBusy = false;
    destFolder = '';
    destAccount = account?.id ?? '';
    if (kind === 'local') {
      destChoice = 'folder';
      return;
    }
    if (mode === 'move') {
      destChoice = 'current';
      destFolder = data.cloudPath;
      return;
    }
    const other = data.accounts.find((a) => a.id !== account?.id);
    if (other) {
      destChoice = 'account';
      destAccount = other.id;
    } else {
      destChoice = 'folder';
      destFolder = data.cloudPath;
    }
  }

  function selectDestChoice(choice: 'current' | 'account' | 'folder'): void {
    destChoice = choice;
    if (!destOpen || destOpen.kind === 'local') return;
    destError = '';
    if (choice === 'current') {
      destAccount = account?.id ?? '';
      destFolder = data.cloudPath;
    } else if (choice === 'account') {
      destAccount = data.accounts.find((a) => a.id !== account?.id)?.id ?? '';
      destFolder = '';
    } else {
      destAccount = account?.id ?? '';
      destFolder = data.cloudPath;
    }
  }

  async function pickLocalFolder(): Promise<void> {
    const res = (await backend.chooseFolder(t('files.chooseFolder'))) as PickRes | null;
    if (!res || res.cancelled) return;
    if (res.path) {
      destFolder = res.path;
      destError = '';
    }
  }

  async function submitDest(): Promise<void> {
    if (!destOpen || !destValid || destBusy) return;
    const op = destOpen;
    const paths = pathsOf(op.entries);
    destBusy = true;
    destError = '';
    try {
      if (op.kind === 'local') {
        const res = (await backend.moveLocal(paths, destFolder)) as OpRes | null;
        if (res?.err) {
          destError = failText(res.err);
          return;
        }
        if (res?.failed?.length) {
          destError = failText(res.err);
          return;
        }
        destOpen = null;
        app.clearSelection();
        await refreshLocal(data.localPath);
        app.notify(t('files.moved', { n: op.entries.length }));
        return;
      }

      const src = destSourceAccount;
      const dst = destResolvedAccount;
      const dir = destResolvedDir;

      if (destCopyOnly) {
        const res = (await backend.copyCloud(src, paths, dst, dir)) as OpRes | null;
        if (res?.err) {
          destError = failText(res.err);
          return;
        }
        if (res?.failed?.length) {
          destError = failText(res.err);
          return;
        }
        destOpen = null;
        app.clearSelection();
        if (dst === src) await refreshCloud(src, data.cloudPath, true);
        app.notify(t('files.copyStarted', { n: op.entries.length }));
        return;
      }

      const res = (await backend.moveCloud(src, paths, dir)) as OpRes | null;
      if (res?.err) {
        destError = failText(res.err);
        return;
      }
      if (res?.failed?.length) {
        destError = failText(res.err);
        return;
      }
      destOpen = null;
      app.clearSelection();
      await refreshCloud(src, data.cloudPath, true);
      app.notify(t('files.moved', { n: op.entries.length }));
    } finally {
      destBusy = false;
    }
  }

  function pickDestFolder(path: string): void {
    destFolder = path;
    destError = '';
  }

  // --------------------------------------------------------------- details

  function mockHash(seed: string, len: number): string {
    let h = 2166136261;
    let out = '';
    let i = 0;
    const size = Math.max(1, seed.length);
    while (out.length < len) {
      h ^= seed.charCodeAt(i % size) + i;
      h = Math.imul(h, 16777619);
      out += (h >>> 0).toString(16).padStart(8, '0');
      i++;
    }
    return out.slice(0, len);
  }

  async function openDetail(entry: Entry): Promise<void> {
    detailEntry = entry;
    detailData = null;
    detailError = '';
    copiedField = '';

    if (!isWails) {
      detailData = {
        name: entry.name,
        size: entry.size,
        mod: entry.mod,
        sha256: mockHash(`${entry.name}:sha256`, 64),
        md5: mockHash(`${entry.name}:md5`, 32)
      };
      return;
    }
    if (!account) return;
    detailLoading = true;
    const res = (await backend.fileDetail(account.id, entry.path ?? entry.id)) as DetailRes | null;
    detailLoading = false;
    if (!res) {
      detailError = t('files.backendError');
      return;
    }
    if (res.err) {
      detailError = failText(res.err);
      return;
    }
    detailData = {
      name: res.name,
      size: res.size,
      mod: new Date(res.modTime),
      sha256: res.sha256 ?? '',
      md5: res.md5 ?? ''
    };
  }

  async function copyHash(value: string, field: 'sha256' | 'md5'): Promise<void> {
    if (!value) return;
    try {
      await navigator.clipboard.writeText(value);
      copiedField = field;
      if (copyTimer) clearTimeout(copyTimer);
      copyTimer = setTimeout(() => {
        if (copiedField === field) copiedField = '';
      }, 2000);
    } catch {
      // Clipboard access can be blocked; leave the label as it was.
    }
  }

  // ----------------------------------------------------------- share links

  async function openShare(entry: Entry): Promise<void> {
    shareEntry = entry;
    shareUrl = '';
    shareError = '';
    shareCopied = false;

    if (!isWails) {
      shareUrl = `https://drive.google.com/file/d/${encodeURIComponent(entry.path ?? entry.name)}/view?usp=sharing`;
      return;
    }
    if (!account) return;
    shareLoading = true;
    const res = (await backend.shareLink(account.id, entry.path ?? entry.id)) as ShareRes | null;
    shareLoading = false;
    if (!res) {
      shareError = t('files.backendError');
      return;
    }
    if (res.err) {
      shareError = failText(res.err);
      return;
    }
    shareUrl = res.url;
  }

  async function copyShareLink(): Promise<void> {
    if (!shareUrl) return;
    try {
      await navigator.clipboard.writeText(shareUrl);
      shareCopied = true;
      if (shareTimer) clearTimeout(shareTimer);
      shareTimer = setTimeout(() => (shareCopied = false), 2000);
    } catch {
      // Clipboard access can be blocked; leave the label as it was.
    }
  }

  function openShareInBrowser(): void {
    if (!shareUrl) return;
    void backend.openExternal(shareUrl);
  }

  // ------------------------------------------------------------ empty trash

  function openCleanup(): void {
    if (!account) return;
    cleanupError = '';
    cleanupOpen = true;
  }

  async function doCleanup(): Promise<void> {
    if (!account || cleanupBusy) return;
    cleanupBusy = true;
    cleanupError = '';
    const res = (await backend.cleanupTrash(account.id)) as EmptyRes | null;
    cleanupBusy = false;
    if (res?.err) {
      cleanupError = failText(res.err);
      return;
    }
    cleanupOpen = false;
    app.clearSelection();
    await refreshCloud(account.id, data.cloudPath, true);
    app.notify(t('files.cleanupDone'));
  }

  // ---------------------------------------------------------- compare backup

  function openVerify(): void {
    if (!account) return;
    verifyOpen = true;
    verifyRoot = data.cloudPath;
    verifyResult = null;
    verifyError = '';
  }

  async function runVerify(): Promise<void> {
    if (!account || verifyBusy) return;
    verifyBusy = true;
    verifyError = '';
    verifyResult = null;

    if (!isWails) {
      verifyResult = {
        same: data.localFiles.filter((e) => !e.dir).length,
        differ: 0,
        missing: 0,
        examples: [],
        status: 'success'
      };
      verifyBusy = false;
      return;
    }

    const res = (await backend.verifyBackup(
      account.id,
      data.localPath,
      verifyRoot
    )) as CheckRes | null;
    verifyBusy = false;
    if (!res) {
      verifyError = t('files.backendError');
      return;
    }
    if (res.err) {
      verifyError = failText(res.err);
      return;
    }
    verifyResult = {
      same: res.same,
      differ: res.differ,
      missing: res.missing,
      examples: res.examples ?? [],
      status: res.status ?? ''
    };
  }

  function pickVerifyFolder(path: string): void {
    verifyRoot = path;
    verifyResult = null;
    verifyError = '';
  }

  // ------------------------------------------------------- whole-cloud search

  async function runSearchAll(): Promise<void> {
    if (!account || !q.trim()) return;
    searchOpen = true;
    searchQuery = q.trim();
    searchLoading = true;
    searchError = '';
    searchHits = [];
    searchTruncated = false;

    if (!isWails) {
      const needle = searchQuery.toLowerCase();
      searchHits = data.cloudFiles
        .filter((e) => e.name.toLowerCase().includes(needle))
        .map((e) => ({
          path: e.path ?? e.name,
          name: e.name,
          size: e.size,
          split: !!e.split
        }));
      searchLoading = false;
      return;
    }

    const res = (await backend.searchCloud(account.id, searchQuery)) as SearchRes | null;
    searchLoading = false;
    if (!res) {
      searchError = t('files.backendError');
      return;
    }
    if (res.err) {
      searchError = failText(res.err);
      return;
    }
    searchHits = (res.items ?? []).map((h) => ({
      path: h.path,
      name: h.name,
      size: h.size,
      modTime: h.modTime,
      split: !!h.split
    }));
    searchTruncated = !!res.truncated;
  }

  function openSearchHit(hit: SearchHitView): void {
    if (!account) return;
    const idx = hit.path.lastIndexOf('/');
    const dir = idx >= 0 ? hit.path.slice(0, idx) : '';
    data.cloudPath = dir;
    app.clearSelection();
    searchOpen = false;
    refreshCloud(account.id, dir);
  }

  onMount(() => {
    refreshRoots();
    window.addEventListener('keydown', onFilesKeydown);
    let off: (() => void) | undefined;
    if (isWails) {
      // Wails reports drops on the Go side; it forwards the real paths here.
      off = Events.On('cloudnest:files-dropped', handleDrop as (ev: any) => void);
    }
    return () => {
      window.removeEventListener('keydown', onFilesKeydown);
      off?.();
    };
  });

  onDestroy(() => {
    if (copyTimer) clearTimeout(copyTimer);
    if (shareTimer) clearTimeout(shareTimer);
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
    <div class="search-col">
      <div class="search">
        <Icon name="search" size={18} />
        <input type="text" placeholder={t('files.search')} aria-label={t('files.search')} bind:value={q} />
        {#if q}
          <button class="icon-button" style="width:24px;height:24px" aria-label="clear" onclick={() => (q = '')}>
            <Icon name="close" size={16} />
          </button>
        {/if}
      </div>
      {#if q.trim() && focusPane === 'cloud'}
        <button class="btn text sm search-all" onclick={runSearchAll}>
          <Icon name="search" size={15} />{t('files.searchAll')}
        </button>
      {/if}
    </div>
    <button class="icon-button" aria-label={t('common.view')}>
      <Icon name="table-chart" size={20} />
    </button>
  </div>

  <div class="files-main">
    <section class="pane" id="drop-local" role="group"
      aria-label={t('files.computer')}
      onfocusin={() => (focusPane = 'local')}
      onpointerdown={() => (focusPane = 'local')}>
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
            <colgroup><col style="width:40px" /><col /><col style="width:92px" /><col style="width:148px" /><col style="width:44px" /></colgroup>
            <thead>
              <tr>
                <th class="c-check" scope="col"><span class="sr-only">{t('files.select')}</span></th>
                <th scope="col" aria-sort={sortKey === 'name' ? (sortAsc ? 'ascending' : 'descending') : 'none'}>
                  <button type="button" class="th-sort" onclick={() => setSort('name')}>
                    {t('files.name')}{#if sortKey === 'name'}<span class="sort-ind">{sortAsc ? '▲' : '▼'}</span>{/if}
                  </button>
                </th>
                <th class="c-size" scope="col" aria-sort={sortKey === 'size' ? (sortAsc ? 'ascending' : 'descending') : 'none'}>
                  <button type="button" class="th-sort" onclick={() => setSort('size')}>
                    {t('files.size')}{#if sortKey === 'size'}<span class="sort-ind">{sortAsc ? '▲' : '▼'}</span>{/if}
                  </button>
                </th>
                <th class="c-time" scope="col" aria-sort={sortKey === 'time' ? (sortAsc ? 'ascending' : 'descending') : 'none'}>
                  <button type="button" class="th-sort" onclick={() => setSort('time')}>
                    {t('files.modified')}{#if sortKey === 'time'}<span class="sort-ind">{sortAsc ? '▲' : '▼'}</span>{/if}
                  </button>
                </th>
                <th class="c-actions" scope="col"><span class="sr-only">{t('files.actions')}</span></th>
              </tr>
            </thead>
            <tbody>
              {#each localShown as e, i (e.id)}
                <tr class:selected={app.selLocal.has(e.id)} onclick={(ev) => toggleLocalRow(e, ev)}
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
                  <td class="c-actions">
                    <button class="icon-button row-menu-btn" aria-label={t('files.actions')} title={t('files.actions')}
                      style={`anchor-name:--local-row-${i}`} popovertarget={`local-row-menu-${i}`}
                      onclick={(ev) => ev.stopPropagation()} ondblclick={(ev) => ev.stopPropagation()}>
                      <Icon name="more-vert" size={18} />
                    </button>
                    <div id={`local-row-menu-${i}`} popover class="account-menu row-menu"
                      style={`position-anchor:--local-row-${i}`}>
                      <button type="button"
                        onclick={() => { closeMenu(`local-row-menu-${i}`); openRename('local', e); }}>
                        <Icon name="description" size={18} />{t('files.rename')}
                      </button>
                      <button type="button"
                        onclick={() => { closeMenu(`local-row-menu-${i}`); openDest('move', 'local', menuEntries('local', e)); }}>
                        <Icon name="open-in-new" size={18} />{t('files.move')}
                      </button>
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
        {#if localList.length > localShown.length}
          <div class="show-more">
            <button class="btn text sm" onclick={() => (localLimit += PAGE_STEP)}>
              {t('files.showMore', { n: localList.length - localShown.length })}
            </button>
          </div>
        {/if}
      </div>
    </section>

    <div class="splitter" role="separator" aria-label="resize"></div>

    <section class="pane" id="drop-cloud" role="group"
      aria-label={t('files.cloud')}
      onfocusin={() => (focusPane = 'cloud')}
      onpointerdown={() => (focusPane = 'cloud')}>
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
        <button class="icon-button" aria-label={t('files.move')} title={t('files.move')}
          disabled={selectedCloud.length === 0 || busy || !account}
          onclick={() => openDest('move', 'cloud', selectedCloud)}>
          <Icon name="open-in-new" size={20} />
        </button>
        <button class="icon-button" aria-label={t('files.copy')} title={t('files.copy')}
          disabled={selectedCloud.length === 0 || busy || !account}
          onclick={() => openDest('copy', 'cloud', selectedCloud)}>
          <Icon name="content-copy" size={20} />
        </button>
        <button class="icon-button" aria-label={t('files.cleanup')} title={t('files.cleanup')}
          disabled={!account || busy} onclick={openCleanup}>
          <Icon name="delete-forever" size={20} />
        </button>
        <button class="icon-button" aria-label={t('files.verify')} title={t('files.verify')}
          disabled={!account || busy} onclick={openVerify}>
          <Icon name="verified" size={20} />
        </button>
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
            <colgroup><col style="width:40px" /><col /><col style="width:92px" /><col style="width:148px" /><col style="width:44px" /></colgroup>
            <thead>
              <tr>
                <th class="c-check" scope="col"><span class="sr-only">{t('files.select')}</span></th>
                <th scope="col" aria-sort={sortKey === 'name' ? (sortAsc ? 'ascending' : 'descending') : 'none'}>
                  <button type="button" class="th-sort" onclick={() => setSort('name')}>
                    {t('files.name')}{#if sortKey === 'name'}<span class="sort-ind">{sortAsc ? '▲' : '▼'}</span>{/if}
                  </button>
                </th>
                <th class="c-size" scope="col" aria-sort={sortKey === 'size' ? (sortAsc ? 'ascending' : 'descending') : 'none'}>
                  <button type="button" class="th-sort" onclick={() => setSort('size')}>
                    {t('files.size')}{#if sortKey === 'size'}<span class="sort-ind">{sortAsc ? '▲' : '▼'}</span>{/if}
                  </button>
                </th>
                <th class="c-time" scope="col" aria-sort={sortKey === 'time' ? (sortAsc ? 'ascending' : 'descending') : 'none'}>
                  <button type="button" class="th-sort" onclick={() => setSort('time')}>
                    {t('files.modified')}{#if sortKey === 'time'}<span class="sort-ind">{sortAsc ? '▲' : '▼'}</span>{/if}
                  </button>
                </th>
                <th class="c-actions" scope="col"><span class="sr-only">{t('files.actions')}</span></th>
              </tr>
            </thead>
            <tbody>
              {#each cloudShown as e, i (e.id)}
                <tr class:selected={app.selCloud.has(e.id)} onclick={(ev) => toggleCloudRow(e, ev)}
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
                  <td class="c-actions">
                    <button class="icon-button row-menu-btn" aria-label={t('files.actions')} title={t('files.actions')}
                      style={`anchor-name:--cloud-row-${i}`} popovertarget={`cloud-row-menu-${i}`}
                      onclick={(ev) => ev.stopPropagation()} ondblclick={(ev) => ev.stopPropagation()}>
                      <Icon name="more-vert" size={18} />
                    </button>
                    <div id={`cloud-row-menu-${i}`} popover class="account-menu row-menu"
                      style={`position-anchor:--cloud-row-${i}`}>
                      <button type="button"
                        onclick={() => { closeMenu(`cloud-row-menu-${i}`); openRename('cloud', e); }}>
                        <Icon name="description" size={18} />{t('files.rename')}
                      </button>
                      <button type="button"
                        onclick={() => { closeMenu(`cloud-row-menu-${i}`); openDest('move', 'cloud', menuEntries('cloud', e)); }}>
                        <Icon name="open-in-new" size={18} />{t('files.move')}
                      </button>
                      <button type="button"
                        onclick={() => { closeMenu(`cloud-row-menu-${i}`); openDest('copy', 'cloud', menuEntries('cloud', e)); }}>
                        <Icon name="content-copy" size={18} />{t('files.copy')}
                      </button>
                      <button type="button"
                        onclick={() => { closeMenu(`cloud-row-menu-${i}`); openDetail(e); }}>
                        <Icon name="info" size={18} />{t('files.details')}
                      </button>
                      {#if !e.split}
                        <hr class="divider" />
                        <button type="button"
                          onclick={() => { closeMenu(`cloud-row-menu-${i}`); openShare(e); }}>
                          <Icon name="link" size={18} />{t('files.share')}
                        </button>
                      {/if}
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
        {#if cloudList.length > cloudShown.length}
          <div class="show-more">
            <button class="btn text sm" onclick={() => (cloudLimit += PAGE_STEP)}>
              {t('files.showMore', { n: cloudList.length - cloudShown.length })}
            </button>
          </div>
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

{#if renameTarget}
  <Modal labelledby="rename-title" onclose={() => (renameTarget = null)}>
    <div class="dialog" style="width: 460px">
      <div class="dialog-head" style="padding-bottom:2px">
        <div class="grow">
          <div class="dialog-title" id="rename-title">{t('files.rename')}</div>
          <div class="dialog-note" style="margin:2px 0 0">{renameTarget.entry.name}</div>
        </div>
        <button class="icon-button" aria-label={t('common.close')} onclick={() => (renameTarget = null)}>
          <Icon name="close" size={20} />
        </button>
      </div>
      <div class="dialog-body">
        <!-- svelte-ignore a11y_autofocus -->
        <input class="textfield" style="width:100%" aria-label={t('files.name')} autofocus
          aria-invalid={!!renameValidation} bind:value={renameName}
          onkeydown={(e) => e.key === 'Enter' && submitRename()} />
        {#if renameValidation}
          <div class="field-hint">{renameValidation}</div>
        {/if}
        {#if renameError}
          <div class="banner" style="margin-top: 12px">
            <Icon name="error" size={20} /><div>{renameError}</div>
          </div>
        {/if}
      </div>
      <div class="dialog-foot">
        <button class="btn text" onclick={() => (renameTarget = null)}>{t('common.cancel')}</button>
        <div class="grow"></div>
        <button class="btn filled" disabled={!!renameValidation || renameBusy} onclick={submitRename}>
          {t('common.save')}
        </button>
      </div>
    </div>
  </Modal>
{/if}

{#if destOpen}
  <Modal labelledby="dest-title" onclose={() => (destOpen = null)}>
    <div class="dialog" style="width: 540px">
      <div class="dialog-head" style="padding-bottom:2px">
        <div class="grow">
          <div class="dialog-title" id="dest-title">
            {destOpen.mode === 'move' ? t('files.moveTitle') : t('files.copyTitle')}
          </div>
          <div class="dialog-note" style="margin:2px 0 0">
            {destOpen.entries.length === 1 ? destOpen.entries[0].name : t('files.selected', { n: destOpen.entries.length, size: fmtBytes(selBytes) })}
          </div>
        </div>
        <button class="icon-button" aria-label={t('common.close')} onclick={() => (destOpen = null)}>
          <Icon name="close" size={20} />
        </button>
      </div>
      <div class="dialog-body">
        {#if destOpen.kind === 'local'}
          <p class="dialog-note">{t('files.localMoveBody')}</p>
          <div class="dest-pick">
            <div class="sl">
              <div class="st">{t('files.otherFolder')}</div>
              <div class="sd truncate">{destFolder || t('files.noFolderChosen')}</div>
            </div>
            <button class="btn tonal sm" onclick={pickLocalFolder}>
              <Icon name="folder-open" size={16} />{t('files.chooseFolder')}
            </button>
          </div>
          {#if destFolder}
            <div class="dialog-note" style="margin-top:8px">
              {t('files.sourceFolder')}: {data.localPath || '—'}
            </div>
          {/if}
        {:else}
          <p class="dialog-note">
            {destOpen.mode === 'move' ? t('files.moveBody') : t('files.copyBody')}
          </p>
          <div class="dest-options">
            {#if destOpen.mode === 'move'}
              <label class="dest-choice" class:selected={destChoice === 'current'}>
                <input type="radio" name="dest-choice" value="current"
                  checked={destChoice === 'current'} onchange={() => selectDestChoice('current')} />
                <span class="dc-body">
                  <span class="dc-title">{t('files.currentFolder')}</span>
                  <span class="dc-sub truncate">{data.cloudPath || t('files.cloudRoot')}</span>
                </span>
              </label>
            {/if}
            <label class="dest-choice" class:selected={destChoice === 'account'}
              class:disabled={destOtherAccounts.length === 0}>
              <input type="radio" name="dest-choice" value="account"
                checked={destChoice === 'account'} disabled={destOtherAccounts.length === 0}
                onchange={() => selectDestChoice('account')} />
              <span class="dc-body">
                <span class="dc-title">{t('files.otherAccount')}</span>
              </span>
            </label>
            <label class="dest-choice" class:selected={destChoice === 'folder'}>
              <input type="radio" name="dest-choice" value="folder"
                checked={destChoice === 'folder'} onchange={() => selectDestChoice('folder')} />
              <span class="dc-body">
                <span class="dc-title">{t('files.otherFolder')}</span>
                <span class="dc-sub truncate">
                  {destChoice === 'folder' && destFolder ? destFolder : t('files.noFolderChosen')}
                </span>
              </span>
            </label>
          </div>

          {#if destChoice === 'account'}
            <select class="textfield dest-select" bind:value={destAccount} aria-label={t('files.otherAccount')}>
              {#each destOtherAccounts as a (a.id)}
                <option value={a.id}>{accountName(a, app.lang)}</option>
              {/each}
            </select>
            {#if destOpen.mode === 'move' && destResolvedAccount !== destSourceAccount}
              <div class="field-hint neutral">{t('files.crossAccountHint')}</div>
            {/if}
          {:else if destChoice === 'folder'}
            <div class="dest-pick">
              <div class="sl">
                <div class="st">{t('files.otherFolder')}</div>
                <div class="sd truncate">{destFolder || t('files.cloudRoot')}</div>
              </div>
              <button class="btn tonal sm" onclick={() => (pickerFor = 'dest')}>
                <Icon name="folder-open" size={16} />{t('files.chooseFolder')}
              </button>
            </div>
          {/if}
        {/if}

        {#if destHint}
          <div class="field-hint">{destHint}</div>
        {/if}

        {#if destError}
          <div class="banner" style="margin-top: 12px">
            <Icon name="error" size={20} /><div>{destError}</div>
          </div>
        {/if}
      </div>
      <div class="dialog-foot">
        <button class="btn text" onclick={() => (destOpen = null)}>{t('common.cancel')}</button>
        <div class="grow"></div>
        <button class="btn filled" disabled={!destValid || destBusy} onclick={submitDest}>
          <Icon name={destCopyOnly ? 'content-copy' : 'open-in-new'} size={18} />
          <span>{destCopyOnly ? t('files.copy') : t('files.move')}</span>
        </button>
      </div>
    </div>
  </Modal>
{/if}

{#if pickerFor === 'dest' && account && destOpen}
  <CloudFolderPicker accountID={destResolvedAccount || account.id} initial={destFolder}
    onpick={pickDestFolder} onclose={() => (pickerFor = null)} />
{/if}

{#if detailEntry}
  <Modal labelledby="detail-title" onclose={() => (detailEntry = null)}>
    <div class="dialog" style="width: 580px">
      <div class="dialog-head" style="padding-bottom:2px">
        <div class="grow">
          <div class="dialog-title" id="detail-title">{detailData?.name ?? detailEntry.name}</div>
          <div class="dialog-note" style="margin:2px 0 0">{t('files.details')}</div>
        </div>
        <button class="icon-button" aria-label={t('common.close')} onclick={() => (detailEntry = null)}>
          <Icon name="close" size={20} />
        </button>
      </div>
      <div class="dialog-body">
        {#if detailLoading}
          <div class="pane-loading">
            {#each Array(4) as _, i (i)}
              <span class="skeleton" style={`height:14px;width:${70 - i * 12}%`}></span>
            {/each}
          </div>
        {:else if detailError}
          <div class="banner"><Icon name="error" size={20} /><div>{detailError}</div></div>
        {:else if detailData}
          <div class="detail-list">
            <div><span>{t('files.name')}</span><b style="user-select:text">{detailData.name}</b></div>
            <div><span>{t('files.size')}</span><b class="nums">{fmtBytes(detailData.size)}</b></div>
            <div><span>{t('files.modified')}</span><b>{fmtTime(detailData.mod)}</b></div>
          </div>
          {#if detailEntry.dir}
            <div class="field-hint neutral" style="margin-top:12px">{t('files.dirNoHash')}</div>
          {:else}
            <div class="hash-row">
              <span class="hash-label">SHA-256</span>
              <code class="hash-value">{detailData.sha256 || '—'}</code>
              <button class="icon-button" aria-label={t('files.copyHash')} title={t('files.copyHash')}
                disabled={!detailData.sha256} onclick={() => copyHash(detailData?.sha256 ?? '', 'sha256')}>
                <Icon name="content-copy" size={18} />
              </button>
              {#if copiedField === 'sha256'}<span class="copied-label">{t('files.copied')}</span>{/if}
            </div>
            <div class="hash-row">
              <span class="hash-label">MD5</span>
              <code class="hash-value">{detailData.md5 || '—'}</code>
              <button class="icon-button" aria-label={t('files.copyHash')} title={t('files.copyHash')}
                disabled={!detailData.md5} onclick={() => copyHash(detailData?.md5 ?? '', 'md5')}>
                <Icon name="content-copy" size={18} />
              </button>
              {#if copiedField === 'md5'}<span class="copied-label">{t('files.copied')}</span>{/if}
            </div>
          {/if}
        {/if}
      </div>
      <div class="dialog-foot">
        <div class="grow"></div>
        <button class="btn filled" onclick={() => (detailEntry = null)}>{t('common.close')}</button>
      </div>
    </div>
  </Modal>
{/if}

{#if shareEntry}
  <Modal labelledby="share-title" onclose={() => (shareEntry = null)}>
    <div class="dialog" style="width: 560px">
      <div class="dialog-head" style="padding-bottom:2px">
        <div class="grow">
          <div class="dialog-title" id="share-title">{t('files.share')}</div>
          <div class="dialog-note" style="margin:2px 0 0">{shareEntry.name}</div>
        </div>
        <button class="icon-button" aria-label={t('common.close')} onclick={() => (shareEntry = null)}>
          <Icon name="close" size={20} />
        </button>
      </div>
      <div class="dialog-body">
        {#if shareLoading}
          <div class="pane-loading">
            {#each Array(2) as _, i (i)}
              <span class="skeleton" style={`height:14px;width:${72 - i * 12}%`}></span>
            {/each}
          </div>
        {:else if shareError}
          <div class="banner"><Icon name="error" size={20} /><div>{shareError}</div></div>
        {:else}
          <input class="textfield share-input" readonly value={shareUrl} aria-label={t('files.shareLink')}
            onclick={(e) => (e.currentTarget as HTMLInputElement).select()} />
          <div style="display:flex;align-items:center;gap:8px;margin-top:12px">
            <button class="btn tonal sm" disabled={!shareUrl} onclick={copyShareLink}>
              <Icon name="content-copy" size={16} />{t('files.copyLink')}
            </button>
            <button class="btn text sm" disabled={!shareUrl} onclick={openShareInBrowser}>
              <Icon name="open-in-new" size={16} />{t('files.openBrowser')}
            </button>
            {#if shareCopied}<span class="copied-label">{t('files.copied')}</span>{/if}
          </div>
        {/if}
      </div>
      <div class="dialog-foot">
        <div class="grow"></div>
        <button class="btn filled" onclick={() => (shareEntry = null)}>{t('common.close')}</button>
      </div>
    </div>
  </Modal>
{/if}

{#if cleanupOpen && account}
  <Modal labelledby="cleanup-title" onclose={() => (cleanupOpen = false)}>
    <div class="dialog" style="width: 520px">
      <div class="dialog-head" style="padding-bottom:2px">
        <div class="grow">
          <div class="dialog-title" id="cleanup-title">
            {t('files.cleanupTitle', { name: accountLabel })}
          </div>
        </div>
        <button class="icon-button" aria-label={t('common.close')} onclick={() => (cleanupOpen = false)}>
          <Icon name="close" size={20} />
        </button>
      </div>
      <div class="dialog-body">
        <p style="margin:4px 0 8px;color:var(--m3c-on-surface-variant)">{t('files.cleanupBody')}</p>
        {#if cleanupError}
          <div class="banner" style="margin-top: 12px">
            <Icon name="error" size={20} /><div>{cleanupError}</div>
          </div>
        {/if}
      </div>
      <div class="dialog-foot">
        <!-- svelte-ignore a11y_autofocus -->
        <button class="btn text" autofocus onclick={() => (cleanupOpen = false)}>{t('common.cancel')}</button>
        <div class="grow"></div>
        <button class="btn danger-filled" disabled={cleanupBusy} onclick={doCleanup}>
          <Icon name="delete-forever" size={18} /><span>{t('files.cleanup')}</span>
        </button>
      </div>
    </div>
  </Modal>
{/if}

{#if verifyOpen && account}
  <Modal labelledby="verify-title" onclose={() => (verifyOpen = false)}>
    <div class="dialog" style="width: 560px">
      <div class="dialog-head" style="padding-bottom:2px">
        <div class="grow">
          <div class="dialog-title" id="verify-title">{t('files.verifyTitle')}</div>
        </div>
        <button class="icon-button" aria-label={t('common.close')} onclick={() => (verifyOpen = false)}>
          <Icon name="close" size={20} />
        </button>
      </div>
      <div class="dialog-body">
        <p class="dialog-note">{t('files.verifyBody', { local: localLabel, account: accountLabel })}</p>
        <div class="dest-pick">
          <div class="sl">
            <div class="st">{t('files.destFolder')}</div>
            <div class="sd truncate">{verifyRoot || t('files.cloudRoot')}</div>
          </div>
          <button class="btn tonal sm" onclick={() => (pickerFor = 'verify')}>
            <Icon name="folder-open" size={16} />{t('files.chooseFolder')}
          </button>
        </div>

        {#if verifyResult}
          <div class="verify-counts">
            <div class="vc"><b class="nums">{verifyResult.same}</b><span>{t('files.same')}</span></div>
            <div class="vc differ"><b class="nums">{verifyResult.differ}</b><span>{t('files.differ')}</span></div>
            <div class="vc missing"><b class="nums">{verifyResult.missing}</b><span>{t('files.missing')}</span></div>
          </div>
          {#if verifyResult.examples.length}
            <div class="verify-examples">
              <div class="ve-title">{t('files.examples')}</div>
              {#each verifyResult.examples.slice(0, 5) as ex (ex)}
                <div class="ve-path truncate">{ex}</div>
              {/each}
            </div>
          {/if}
          <p class="dialog-note" style="margin-top:12px">
            {verifyResult.differ || verifyResult.missing
              ? t('files.verifyDifferBody')
              : t('files.verifySameBody')}
          </p>
        {:else if verifyError}
          <div class="banner" style="margin-top: 12px">
            <Icon name="error" size={20} /><div>{verifyError}</div>
          </div>
        {/if}
      </div>
      <div class="dialog-foot">
        <button class="btn text" onclick={() => (verifyOpen = false)}>{t('common.close')}</button>
        <div class="grow"></div>
        <button class="btn filled" disabled={verifyBusy || !data.localPath} onclick={runVerify}>
          <Icon name="verified" size={18} /><span>{t('files.startVerify')}</span>
        </button>
      </div>
    </div>
  </Modal>
{/if}

{#if pickerFor === 'verify' && account}
  <CloudFolderPicker accountID={account.id} initial={verifyRoot}
    onpick={pickVerifyFolder} onclose={() => (pickerFor = null)} />
{/if}

{#if searchOpen}
  <Modal labelledby="search-title" onclose={() => (searchOpen = false)}>
    <div class="dialog" style="width: 620px">
      <div class="dialog-head" style="padding-bottom:2px">
        <div class="grow">
          <div class="dialog-title" id="search-title">{t('files.searchAllTitle')}</div>
          <div class="dialog-note" style="margin:2px 0 0">{searchQuery}</div>
        </div>
        <button class="icon-button" aria-label={t('common.close')} onclick={() => (searchOpen = false)}>
          <Icon name="close" size={20} />
        </button>
      </div>
      <div class="dialog-body" style="min-height:260px">
        {#if searchLoading}
          <div class="pane-loading">
            {#each Array(4) as _, i (i)}
              <span class="skeleton" style={`height:14px;width:${70 - i * 10}%`}></span>
            {/each}
          </div>
        {:else if searchError}
          <div class="banner"><Icon name="error" size={20} /><div>{searchError}</div></div>
        {:else if searchHits.length === 0}
          <div class="empty" style="padding:40px 16px">
            <span class="art"><Icon name="search" size={40} /></span>
            <div class="e-title">{t('files.searchNone')}</div>
          </div>
        {:else}
          <div class="search-results">
            {#each searchHits as h (h.path)}
              <button type="button" class="search-hit" onclick={() => openSearchHit(h)}>
                <span class="sh-icon"><Icon name={h.split ? 'folder-special' : 'description'} size={18} /></span>
                <span class="sh-body">
                  <span class="sh-name truncate">{h.name}</span>
                  <span class="sh-path truncate">{h.path}</span>
                </span>
                <span class="sh-size nums">{h.split ? t('files.split.neutral') : fmtBytes(h.size)}</span>
              </button>
            {/each}
          </div>
          {#if searchTruncated}
            <div class="field-hint neutral">{t('files.searchTruncated')}</div>
          {/if}
        {/if}
      </div>
      <div class="dialog-foot">
        <div class="grow"></div>
        <button class="btn filled" onclick={() => (searchOpen = false)}>{t('common.close')}</button>
      </div>
    </div>
  </Modal>
{/if}
