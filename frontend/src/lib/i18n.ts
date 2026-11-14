// Cloudnest — i18n + formatters (runtime locale state)
import { dict, type Lang } from './dict';

export type { Lang };

let current: Lang = 'zh-Hant';

export function getLang(): Lang {
  return current;
}

export function setLang(lang: Lang): void {
  current = lang;
}

export function t(key: string, vars?: Record<string, string | number>): string {
  let s = dict[current][key] ?? dict['zh-Hant'][key] ?? key;
  if (vars) {
    s = s.replace(/\{(\w+)\}/g, (_, k: string) =>
      vars[k] !== undefined ? String(vars[k]) : `{${k}}`
    );
  }
  return s;
}

export function fmtBytes(n: number | null | undefined): string {
  if (n === null || n === undefined || Number.isNaN(n)) return '—';
  if (n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  const dec = v >= 100 ? 0 : 1;
  return `${v.toFixed(dec).replace(/\.0$/, '')} ${units[i]}`;
}

function pad(n: number): string {
  return n < 10 ? `0${n}` : `${n}`;
}

export function fmtTime(date: Date | number | string): string {
  const d = date instanceof Date ? date : new Date(date);
  const now = new Date();
  const same =
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate();
  const yest = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1);
  const isYest =
    d.getFullYear() === yest.getFullYear() &&
    d.getMonth() === yest.getMonth() &&
    d.getDate() === yest.getDate();
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  if (same) return (current === 'en' ? 'Today ' : '今天 ') + hm;
  if (isYest) return (current === 'en' ? 'Yesterday ' : '昨天 ') + hm;
  if (current === 'en') {
    return `${d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })} ${hm}`;
  }
  return `${d.getMonth() + 1}月 ${d.getDate()}日 ${hm}`;
}

export function fmtDuration(sec: number | null | undefined): string {
  if (sec === null || sec === undefined) return '—';
  const s = Math.max(0, Math.round(sec));
  if (s < 60) return current === 'en' ? `${Math.max(1, s)} sec` : `${Math.max(1, s)} 秒`;
  const m = Math.round(s / 60);
  if (m < 60) return current === 'en' ? `${m} min` : `${m} 分鐘`;
  const h = Math.floor(m / 60);
  const rm = m % 60;
  if (current === 'en') return `${h} hr${rm ? ` ${rm} min` : ''}`;
  return `${h} 小時${rm ? ` ${rm} 分` : ''}`;
}
