// Thin wrapper around the generated Wails bindings.
// In a plain browser (vite dev without Wails) every call resolves to null and
// the UI keeps using its mock fixtures.
import * as Service from '../../bindings/cloudnest/services/service';
import type * as DTO from '../../bindings/cloudnest/internal/dto/models';

export const isWails =
  typeof window !== 'undefined' && Boolean((window as unknown as { _wails?: unknown })._wails);

async function safe<T>(fn: () => Promise<T>): Promise<T | null> {
  if (!isWails) return null;
  try {
    return await fn();
  } catch (err) {
    console.warn('[backend]', err);
    return null;
  }
}

export const backend = {
  bootstrap: () => safe(() => Service.Bootstrap()),
  engineStatus: () => safe(() => Service.EngineStatus()),
  restartEngine: () => safe(() => Service.RestartEngine()),

  listAccounts: () => safe(() => Service.ListAccounts()),
  accountUsage: (id: string) => safe(() => Service.AccountUsage(id)),
  listProviders: () => safe(() => Service.ListProviders()),
  removeAccount: (id: string) => safe(() => Service.RemoveAccount(id)),

  beginConnect: (provider: string, name: string) => safe(() => Service.BeginConnect(provider, name)),
  connectState: (id: string) => safe(() => Service.ConnectState(id)),
  cancelConnect: (id: string) => safe(() => Service.CancelConnect(id)),

  listRoots: () => safe(() => Service.ListRoots()),
  listLocal: (path: string) => safe(() => Service.ListLocal(path)),
  measure: (path: string) => safe(() => Service.Measure(path)),
  listCloud: (account: string, path: string) => safe(() => Service.ListCloud(account, path)),
  createFolder: (account: string, path: string, name: string) =>
    safe(() => Service.CreateFolder(account, path, name)),

  startBackup: (req: DTO.PlanRequest) => safe(() => Service.StartBackup(req)),
  activeJobs: () => safe(() => Service.ActiveJobs()),
  history: (limit = 30) => safe(() => Service.History(limit)),
  pauseJob: (id: string) => safe(() => Service.PauseJob(id)),
  cancelJob: (id: string) => safe(() => Service.CancelJob(id)),

  getSettings: () => safe(() => Service.GetSettings()),
  saveSettings: (s: DTO.Settings) => safe(() => Service.SaveSettings(s)),
  setLanguage: (lang: string) => safe(() => Service.SetLanguage(lang)),
  setTheme: (theme: string) => safe(() => Service.SetTheme(theme)),
  markOnboarded: () => safe(() => Service.MarkOnboarded()),

  chooseFolder: (title: string) => safe(() => Service.ChooseFolder(title)),
  chooseFiles: (title: string) => safe(() => Service.ChooseFiles(title)),
  openLogFolder: () => safe(() => Service.OpenLogFolder()),
  openConfigFolder: () => safe(() => Service.OpenConfigFolder()),
  openExternal: (url: string) => safe(() => Service.OpenExternal(url)),

  minimise: () => safe(() => Service.WindowMinimise()),
  toggleMaximise: () => safe(() => Service.WindowToggleMaximise()),
  close: () => safe(() => Service.WindowClose()),
  quit: () => safe(() => Service.Quit())
};

export type { DTO };
