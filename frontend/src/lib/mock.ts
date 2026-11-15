// Cloudnest — mock data for UI development (replaced by Go services later)
export type ProviderId =
  | 'google' | 'onedrive' | 'dropbox' | 'box'
  | 'mega' | 'pcloud' | 'aws' | 'webdav';

export type SplitState = 'ready' | 'broken' | 'incomplete' | 'foreign';

export interface Account {
  id: string;
  provider: ProviderId;
  name: string;
  nameEn: string;
  status: 'connected' | 'relogin';
  used: number;
  total: number;
  lastBackup?: Date | null;
  email: string;
  /** Google Drive without its own client_id: rclone's shared one is throttled. */
  sharedClientId?: boolean;
}

export interface Entry {
  id: string;
  name: string;
  path?: string;
  dir?: boolean;
  kind?: 'img' | 'vid' | 'aud' | 'pdf' | 'doc' | 'zip';
  size: number;
  mod: Date;
  split?: SplitState;
  parts?: number;
}

export interface ProviderInfo {
  id: ProviderId;
  name: string;
  nameEn: string;
  tier: 'A' | 'B' | 'C';
  recommended?: boolean;
}

export interface Job {
  id: string;
  kind: 'backup' | 'restore';
  source?: string;
  sourceEn?: string;
  account?: string;
  accountEn?: string;
  bytes: number;
  total: number;
  speed: number;
  eta: number;
  files?: number;
  done: number;
  current?: string;
  split?: { done: number; total: number };
}

export interface HistoryItem {
  id: string;
  kind: 'backup' | 'restore';
  /**
   * "interrupted" means the run ended but its outcome could not be observed
   * (for example rclone restarted or purged the job); it is still retryable.
   */
  state: 'done' | 'failed' | 'cancelled' | 'interrupted';
  source?: string;
  sourceEn?: string;
  name?: string;
  time: Date;
  files?: number;
  size: number;
  failed?: number;
  ok?: number;
  /** Technical reason recorded when the job failed. */
  error?: string;
}

const GB = 1024 ** 3;
const MB = 1024 ** 2;
const KB = 1024;
const now = new Date();
const minsAgo = (m: number) => new Date(now.getTime() - m * 60000);
const daysAgo = (d: number, h = 0, m = 0) =>
  new Date(now.getFullYear(), now.getMonth(), now.getDate() - d, h, m);

export const accounts: Account[] = [
  {
    id: 'gdrive', provider: 'google', name: 'Google 雲端硬碟', nameEn: 'Google Drive',
    status: 'connected', used: 42.1 * GB, total: 100 * GB, lastBackup: minsAgo(20),
    email: 'you@gmail.com'
  },
  {
    id: 'onedrive', provider: 'onedrive', name: 'OneDrive', nameEn: 'OneDrive',
    status: 'connected', used: 112 * GB, total: 1024 * GB, lastBackup: daysAgo(1, 9, 8),
    email: 'you@outlook.com'
  }
];

export const localRoots = [
  { id: 'desktop', key: 'files.root.desktop', icon: 'computer' },
  { id: 'documents', key: 'files.root.documents', icon: 'description' },
  { id: 'pictures', key: 'files.root.pictures', icon: 'image' },
  { id: 'downloads', key: 'files.root.downloads', icon: 'download' },
  { id: 'music', key: 'files.root.music', icon: 'music-note' }
];

export const backupSource = {
  name: '下載', nameEn: 'Downloads', files: 1284, bytes: 12.4 * GB,
  largeFiles: 3, largeParts: 12
};

export const localFiles: Entry[] = [
  { id: 'l1', name: '2025 北海道旅遊', dir: true, size: 0, mod: daysAgo(2, 19, 22) },
  { id: 'l2', name: '家庭影片_2024.mp4', kind: 'vid', size: 4.2 * GB, mod: daysAgo(5, 21, 4) },
  { id: 'l3', name: 'IMG_2043.HEIC', kind: 'img', size: 4.8 * MB, mod: minsAgo(48) },
  { id: 'l4', name: '婚禮精修', dir: true, size: 0, mod: daysAgo(9, 11, 30) },
  { id: 'l5', name: 'Q4 預算規劃.xlsx', kind: 'doc', size: 1.2 * MB, mod: daysAgo(1, 16, 45) },
  { id: 'l6', name: '發票_2025-10.pdf', kind: 'pdf', size: 480 * KB, mod: daysAgo(3, 10, 12) },
  { id: 'l7', name: '錄音_會議.m4a', kind: 'aud', size: 28 * MB, mod: daysAgo(4, 14, 2) },
  { id: 'l8', name: '網站備份.zip', kind: 'zip', size: 312 * MB, mod: daysAgo(6, 8, 40) },
  { id: 'l9', name: '專案', dir: true, size: 0, mod: daysAgo(7, 15, 18) }
];

export const cloudFiles: Entry[] = [
  { id: 'c0', name: '家庭影片_2024.mp4', kind: 'vid', size: 4.2 * GB, mod: daysAgo(6, 14, 12), split: 'ready', parts: 16 },
  { id: 'c1', name: '2025 北海道旅遊', dir: true, size: 0, mod: daysAgo(2, 19, 25) },
  { id: 'c2', name: 'IMG_2043.HEIC', kind: 'img', size: 4.8 * MB, mod: minsAgo(46) },
  { id: 'c3', name: 'Q4 預算規劃.xlsx', kind: 'doc', size: 1.2 * MB, mod: daysAgo(1, 16, 48) },
  { id: 'c4', name: '發票_2025-10.pdf', kind: 'pdf', size: 480 * KB, mod: daysAgo(3, 10, 15) },
  { id: 'c5', name: '婚禮精修_備份.zip', kind: 'zip', size: 2.1 * GB, mod: daysAgo(9, 12, 2), split: 'broken', parts: 9 },
  { id: 'c6', name: '備份_2023.zip', kind: 'zip', size: 6.4 * GB, mod: daysAgo(12, 3, 30), split: 'incomplete', parts: 16 },
  { id: 'c7', name: '錄音_會議.m4a', kind: 'aud', size: 28 * MB, mod: daysAgo(4, 14, 10) },
  { id: 'c8', name: '網站備份.zip', kind: 'zip', size: 312 * MB, mod: daysAgo(6, 8, 44) }
];

export const restore = {
  id: 'c0',
  name: '家庭影片_2024.mp4',
  size: 4.2 * GB,
  parts: 16,
  partsDone: 16,
  created: daysAgo(6, 14, 12),
  account: 'Google 雲端硬碟',
  accountEn: 'Google Drive',
  remotePath: '電腦備份 / 下載',
  remotePathEn: 'Computer backup / Downloads',
  dest: '下載',
  destEn: 'Downloads',
  free: 128 * GB
};

export const jobs = {
  active: {
    id: 'job-1', kind: 'backup' as const,
    source: '下載', sourceEn: 'Downloads',
    account: 'Google 雲端硬碟', accountEn: 'Google Drive',
    bytes: 3.2 * GB, total: 10.2 * GB, speed: 12.4 * MB, eta: 240,
    files: 1284, done: 412, current: 'IMG_2043.HEIC',
    split: { done: 7, total: 16 }
  } satisfies Job,
  paused: {
    id: 'job-2', kind: 'backup' as const,
    source: '2025 北海道旅遊', sourceEn: '2025 Hokkaido',
    account: 'OneDrive', accountEn: 'OneDrive',
    bytes: 1.1 * GB, total: 6.8 * GB, speed: 0, eta: 360,
    files: 842, done: 210, current: 'DSC_0812.ARW'
  } satisfies Job
};

export const history: HistoryItem[] = [
  { id: 'h1', kind: 'backup', state: 'done', source: '文件', sourceEn: 'Documents', time: minsAgo(20), files: 512, size: 3.1 * GB },
  { id: 'h2', kind: 'backup', state: 'done', source: '圖片', sourceEn: 'Pictures', time: daysAgo(1, 9, 8), files: 2048, size: 8.4 * GB },
  { id: 'h3', kind: 'backup', state: 'failed', source: '下載', sourceEn: 'Downloads', time: daysAgo(6, 22, 40), files: 3, size: 12.4 * GB, failed: 3, ok: 1281 },
  { id: 'h4', kind: 'restore', state: 'done', name: '家庭影片_2024.mp4', time: daysAgo(6, 14, 12), size: 4.2 * GB }
];

export const providers: ProviderInfo[] = [
  { id: 'google', name: 'Google 雲端硬碟', nameEn: 'Google Drive', tier: 'A', recommended: true },
  { id: 'onedrive', name: 'OneDrive', nameEn: 'OneDrive', tier: 'A' },
  { id: 'dropbox', name: 'Dropbox', nameEn: 'Dropbox', tier: 'A' },
  { id: 'box', name: 'Box', nameEn: 'Box', tier: 'A' },
  { id: 'mega', name: 'MEGA', nameEn: 'MEGA', tier: 'B' },
  { id: 'pcloud', name: 'pCloud', nameEn: 'pCloud', tier: 'B' },
  { id: 'aws', name: 'Amazon S3', nameEn: 'Amazon S3', tier: 'C' },
  { id: 'webdav', name: 'WebDAV', nameEn: 'WebDAV', tier: 'C' }
];

export const speedSeries = (() => {
  const out: number[] = [];
  for (let i = 0; i < 60; i++) {
    const base = 0.62 + 0.16 * Math.sin(i / 5.5) + 0.08 * Math.sin(i / 2.1 + 1.2);
    const noise = (Math.random() - 0.5) * 0.025;
    out.push(Math.max(0.35, Math.min(1, base + noise)));
  }
  return out;
})();

export function accountName(a: Account, lang: string): string {
  return lang === 'en' ? a.nameEn : a.name;
}

export function providerName(p: ProviderInfo, lang: string): string {
  return lang === 'en' ? p.nameEn : p.name;
}

export const appVersion = '0.1.0';
export const engineVersion = 'v1.70.3';
