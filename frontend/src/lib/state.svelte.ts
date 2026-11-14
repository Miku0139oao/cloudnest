// Cloudnest — application state + hash router
import { getLang, setLang, type Lang } from './i18n';

export type ThemeSetting = 'system' | 'light' | 'dark';

interface Parsed {
  route: string;
  params: Record<string, string>;
  flags: Set<string>;
}

function parseHash(): Parsed {
  const raw = location.hash.replace(/^#/, '');
  const qi = raw.indexOf('?');
  const route = (qi >= 0 ? raw.slice(0, qi) : raw) || 'home';
  const qs = qi >= 0 ? raw.slice(qi + 1) : '';
  const params: Record<string, string> = {};
  for (const pair of qs.split('&')) {
    if (!pair) continue;
    const i = pair.indexOf('=');
    const k = decodeURIComponent(i >= 0 ? pair.slice(0, i) : pair);
    const v = i >= 0 ? decodeURIComponent(pair.slice(i + 1)) : '';
    params[k] = v;
  }
  const flags = new Set((params.state ?? '').split(',').filter(Boolean));
  return { route, params, flags };
}

export function baseFor(route: string): string {
  if (route.startsWith('wizard/')) return 'home';
  if (route.startsWith('clouds/')) return 'clouds';
  if (route === 'files/restore') return 'files';
  return route;
}

export function overlayFor(route: string): 'wizard' | 'add-provider' | 'connect' | 'restore' | null {
  if (route.startsWith('wizard/')) return 'wizard';
  if (route === 'clouds/add') return 'add-provider';
  if (route === 'clouds/connect' || route === 'clouds/oauth') return 'connect';
  if (route === 'files/restore') return 'restore';
  return null;
}

class AppState {
  route = $state('home');
  params = $state<Record<string, string>>({});
  flags = $state(new Set<string>());
  lang = $state<Lang>('zh-Hant');
  theme = $state<ThemeSetting>('system');
  systemDark = $state(false);

  selCloud = $state(new Set<string>());
  selLocal = $state(new Set<string>());
  localRoot = $state('');
  mirrorMode = $state(false);
  mirrorChecked = $state(false);
  advancedOpen = $state(false);
  fastFiles = $state(true);
  sourceChoice = $state<'folder' | 'files'>('folder');
  sourcePaths = $state<string[]>([]);
  sourceName = $state('');
  sourceFiles = $state(0);
  sourceBytes = $state(0);
  sourceEstimated = $state(false);
  destRoot = $state('電腦備份');
  /** Glob patterns the wizard must leave out of the backup. */
  excludes = $state<string[]>([]);
  excludesOpen = $state(false);
  activeJobId = $state('');
  restoreAccount = $state('');
  restorePath = $state('');
  restoreDest = $state('');
  account = $state('gdrive');
  paused = $state(false);
  splitView = $state(false);
  toast = $state<string | null>(null);

  resolvedTheme = $derived(
    this.theme === 'dark' ? 'dark' : this.theme === 'light' ? 'light' : (this.systemDark ? 'dark' : 'light')
  );

  boot(): void {
    const p = parseHash();
    this.route = p.route;
    this.params = p.params;
    this.flags = p.flags;
    const storedLang = localStorage.getItem('cloudnest.lang');
    this.lang = (p.params.lang ?? storedLang ?? 'zh-Hant') === 'en' ? 'en' : 'zh-Hant';
    this.theme = (p.params.theme as ThemeSetting) ?? 'system';
    this.mirrorMode = p.flags.has('mirror');
    this.mirrorChecked = p.flags.has('checked');
    this.advancedOpen = p.flags.has('advanced');
    this.paused = p.flags.has('paused');
    this.splitView = p.flags.has('split');
    this.fastFiles = !p.flags.has('slow');
    setLang(this.lang);
    this.applyChrome();

    const mq = window.matchMedia('(prefers-color-scheme: dark)');
    this.systemDark = mq.matches;
    mq.addEventListener('change', () => {
      this.systemDark = mq.matches;
      if (this.theme === 'system') this.applyChrome();
    });

    window.addEventListener('hashchange', () => this.syncFromHash());
  }

  syncFromHash(): void {
    const p = parseHash();
    this.route = p.route;
    this.params = p.params;
    this.flags = p.flags;
    if (p.params.lang) {
      this.lang = p.params.lang === 'en' ? 'en' : 'zh-Hant';
      setLang(this.lang);
    }
    if (p.params.theme) this.theme = p.params.theme as ThemeSetting;
    this.applyChrome();
  }

  applyChrome(): void {
    document.documentElement.dataset.theme = this.resolvedTheme;
    document.documentElement.lang = this.lang;
  }

  /** Rewrite the hash, preserving language, theme and relevant transient flags. */
  go(route: string, extra?: Record<string, string>): void {
    const q: string[] = [];
    q.push(`lang=${encodeURIComponent(this.lang)}`);
    q.push(`theme=${encodeURIComponent(this.theme)}`);
    const st: string[] = [];
    if (route === 'files' && this.selCloud.size) st.push('selected');
    if (route === 'activity' && this.paused) st.push('paused');
    if (route === 'activity' && this.splitView) st.push('split');
    if (route.startsWith('wizard/')) {
      if (this.mirrorMode) st.push('mirror');
      if (this.mirrorChecked) st.push('checked');
      if (this.advancedOpen) st.push('advanced');
      if (!this.fastFiles) st.push('slow');
    }
    if (route === 'settings' && this.advancedOpen) st.push('advanced');
    if (st.length) q.push(`state=${encodeURIComponent(st.join(','))}`);
    if (extra) for (const [k, v] of Object.entries(extra)) q.push(`${encodeURIComponent(k)}=${encodeURIComponent(v)}`);
    location.hash = `#${route}?${q.join('&')}`;
  }

  closeOverlay(): void {
    this.go(baseFor(this.route));
  }

  setLanguage(lang: Lang): void {
    this.lang = lang;
    setLang(lang);
    localStorage.setItem('cloudnest.lang', lang);
    this.applyChrome();
    this.go(this.route);
  }

  // applySaved restores the language and theme stored in settings.json. It is
  // called after bootstrap; an explicit ?lang=/?theme= in the URL still wins,
  // which keeps the preview links deterministic.
  applySaved(lang: string, theme: ThemeSetting): void {
    if (!this.params.lang && lang) {
      this.lang = lang === 'en' ? 'en' : 'zh-Hant';
      setLang(this.lang);
      localStorage.setItem('cloudnest.lang', this.lang);
    }
    if (!this.params.theme && (theme === 'light' || theme === 'dark' || theme === 'system')) {
      this.theme = theme;
    }
    this.applyChrome();
  }

  setTheme(theme: ThemeSetting): void {
    this.theme = theme;
    this.applyChrome();
    this.go(this.route);
  }

  toggleTheme(): void {
    this.setTheme(this.resolvedTheme === 'dark' ? 'light' : 'dark');
  }

  toggleCloud(id: string): void {
    const next = new Set(this.selCloud);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    this.selCloud = next;
  }

  toggleLocal(id: string): void {
    const next = new Set(this.selLocal);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    this.selLocal = next;
  }

  clearSelection(): void {
    this.selCloud = new Set();
    this.selLocal = new Set();
  }

  notify(message: string): void {
    this.toast = message;
    setTimeout(() => {
      if (this.toast === message) this.toast = null;
    }, 2600);
  }

  get step(): number {
    const m = /^wizard\/(\d)/.exec(this.route);
    return m ? Number(m[1]) : 1;
  }
}

export const app = new AppState();
export { getLang };
