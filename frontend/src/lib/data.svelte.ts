// Reactive application data. Seeds from mock fixtures so the UI is complete
// in a plain browser, then replaces them with live rclone data in Wails.
import * as mock from './mock';
import { backend, isWails } from './backend';
import type { Account, Entry, HistoryItem, ProviderId, ProviderInfo } from './mock';

const knownProviders: ProviderId[] = [
  'google', 'onedrive', 'dropbox', 'box', 'mega', 'pcloud', 'aws', 'webdav'
];

function asProvider(value: string): ProviderId {
  return (knownProviders as string[]).includes(value) ? (value as ProviderId) : 'webdav';
}

function kindOf(name: string, mime?: string): Entry['kind'] {
  const ext = name.slice(name.lastIndexOf('.')).toLowerCase();
  if (mime?.startsWith('image/') || ['.jpg', '.jpeg', '.png', '.gif', '.webp', '.heic'].includes(ext))
    return 'img';
  if (mime?.startsWith('video/') || ['.mp4', '.mkv', '.mov', '.avi', '.webm'].includes(ext)) return 'vid';
  if (mime?.startsWith('audio/') || ['.mp3', '.m4a', '.flac', '.wav', '.ogg'].includes(ext)) return 'aud';
  if (ext === '.pdf') return 'pdf';
  if (['.zip', '.7z', '.rar', '.gz', '.tar'].includes(ext)) return 'zip';
  return 'doc';
}

function mapAccount(a: {
  id: string;
  provider: string;
  name: string;
  status: string;
  used: number;
  total: number;
  lastBackup?: number;
  email?: string;
}): Account {
  return {
    id: a.id,
    provider: asProvider(a.provider),
    name: a.name,
    nameEn: a.name,
    status: a.status === 'connected' ? 'connected' : 'relogin',
    used: a.used,
    total: a.total,
    lastBackup: a.lastBackup ? new Date(a.lastBackup * 1000) : null,
    email: a.email ?? ''
  };
}

function mapEntry(e: {
  name: string;
  path: string;
  mime?: string;
  isDir: boolean;
  size: number;
  modTime: number;
  split?: { state: string; parts: number; originalName: string } | null;
}): Entry {
  return {
    id: e.path || e.name,
    name: e.name,
    path: e.path,
    dir: e.isDir,
    kind: kindOf(e.name, e.mime),
    size: e.size,
    mod: new Date(Math.floor(e.modTime / 1e6)),
    split: e.split
      ? (e.split.state as Entry['split'])
      : undefined,
    parts: e.split?.parts
  };
}

export interface JobView {
  id: string;
  kind: string;
  state: string;
  title?: string;
  current?: string;
  bytes: number;
  totalBytes: number;
  speed: number;
  eta?: number | null;
  failedFiles?: number;
  totalFiles?: number;
  split?: boolean;
  partsDone?: number;
  partsTotal?: number;
}

export const data = $state({
  // In the real app the UI starts empty and fills from rclone. Fixtures are
  // only used by the browser preview (npm run dev without Wails).
  accounts: (isWails ? [] : mock.accounts) as Account[],
  localFiles: (isWails ? [] : mock.localFiles) as Entry[],
  cloudFiles: (isWails ? [] : mock.cloudFiles) as Entry[],
  history: (isWails ? [] : mock.history) as HistoryItem[],
  providers: mock.providers as ProviderInfo[],
  jobs: [] as JobView[],
  engine: { state: 'starting', version: '', message: '' },
  ready: false,
  live: isWails,
  localPath: '',
  localRoot: '',
  cloudPath: '',
  activeAccount: '',
  cloudLoading: false,
  cloudSlow: false,
  cloudError: '',
  localLoading: false
});

export async function refreshJobs(): Promise<void> {
  const res = await backend.activeJobs();
  if (!res || !res.items) return;
  data.jobs = res.items as JobView[];
}

function applyEngine(st: { state: string; version?: string; err?: { message?: string } | null }) {
  data.engine = {
    state: st.state,
    version: st.version ?? '',
    message: st.err?.message ?? ''
  };
}

export async function refreshEngine(): Promise<void> {
  const st = await backend.engineStatus();
  if (!st) return;
  applyEngine(st);
  if (st.state === 'ready' && !data.ready) {
    data.ready = true;
    await refreshAccounts();
    refreshHistory();
  }
}

export async function boot(): Promise<void> {
  const b = await backend.bootstrap();
  if (!b) return;
  applyEngine(b.engine);
  data.live = true;
  if (b.accounts?.length) {
    data.accounts = b.accounts.map(mapAccount);
    data.activeAccount = data.accounts[0].id;
  }
  if (b.engine.state === 'ready') {
    data.ready = true;
    await refreshAccounts();
    refreshHistory();
  }
}

export async function refreshAccounts(): Promise<void> {
  const res = await backend.listAccounts();
  if (!res || !res.items) return;
  data.accounts = res.items.map(mapAccount);
  if (!data.activeAccount && data.accounts.length) {
    data.activeAccount = data.accounts[0].id;
  }
}

export async function refreshUsage(): Promise<void> {
  const snapshot = data.accounts.slice();
  // Sequential: concurrent operations/about calls contend with file listings
  // inside the rclone Drive backend.
  for (const a of snapshot) {
    const u = await backend.accountUsage(a.id);
    if (!u || u.err || u.total <= 0) continue;
    const next = data.accounts.slice();
    const idx = next.findIndex((x) => x.id === a.id);
    if (idx < 0) continue;
    next[idx] = { ...next[idx], used: u.used, total: u.total };
    data.accounts = next;
  }
}

export async function refreshHistory(): Promise<void> {
  const res = await backend.history(30);
  if (!res || !res.items) return;
  data.history = res.items.map((r) => ({
    id: r.id,
    kind: r.kind === 'restore' ? 'restore' : 'backup',
    state: r.state === 'failed' ? 'failed' : r.state === 'cancelled' ? 'cancelled' : 'done',
    source: r.title,
    sourceEn: r.title,
    name: r.title,
    time: new Date((r.finishedAt || r.startedAt) * 1000),
    files: r.files,
    size: r.bytes,
    failed: r.failed,
    ok: Math.max(0, r.files - r.failed)
  }));
}

let cloudSeq = 0;
const cloudCache = new Map<string, { entries: Entry[]; at: number }>();

export async function refreshCloud(accountID: string, path: string, force = false): Promise<void> {
  const key = `${accountID}|${path}`;
  const cached = cloudCache.get(key);
  if (cached && !force && Date.now() - cached.at < 60_000) {
    data.cloudFiles = cached.entries;
    data.cloudPath = path;
    data.cloudLoading = false;
    data.cloudError = '';
    return;
  }
  const seq = ++cloudSeq;
  data.cloudLoading = true;
  data.cloudSlow = false;
  data.cloudError = '';
  const slowTimer = setTimeout(() => {
    if (seq === cloudSeq) data.cloudSlow = true;
  }, 4000);
  try {
    const res = await backend.listCloud(accountID, path);
    if (seq !== cloudSeq) return; // superseded by a newer request
    if (!res) {
      data.cloudError = 'backend';
      return;
    }
    if (res.err) {
      console.warn('[cloud]', res.err);
      data.cloudError = res.err.message;
      data.cloudFiles = [];
      return;
    }
    try {
      const mapped = (res.entries ?? []).map(mapEntry);
      data.cloudFiles = mapped;
      data.cloudPath = path;
      cloudCache.set(key, { entries: mapped, at: Date.now() });
    } catch (mapErr) {
      data.cloudError = String(mapErr);
    }
  } finally {
    clearTimeout(slowTimer);
    data.cloudSlow = false;
    // Always clear the spinner. A newer request may still be running, but
    // showing a stale list briefly beats an infinite skeleton.
    data.cloudLoading = false;
  }
}

export async function refreshLocal(path: string): Promise<void> {
  data.localLoading = true;
  const res = await backend.listLocal(path);
  data.localLoading = false;
  if (!res) return;
  data.localFiles = (res.entries ?? []).map(mapEntry);
  data.localPath = path;
}

export async function refreshRoots(): Promise<string> {
  const res = await backend.listRoots();
  if (!res || !res.items?.length) return '';
  const downloads = res.items.find((r) => r.id === 'downloads') ?? res.items[0];
  data.localRoot = downloads.path;
  data.localPath = downloads.path;
  await refreshLocal(downloads.path);
  return downloads.path;
}
