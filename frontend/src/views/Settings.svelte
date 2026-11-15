<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { app } from '$lib/state.svelte';
  import { t, fmtTime } from '$lib/i18n';
  import { appVersion, engineVersion, accountName } from '$lib/mock';
  import { data } from '$lib/data.svelte';
  import { backend, type DTO } from '$lib/backend';

  type Section = 'general' | 'backup' | 'advanced' | 'about';
  let section = $state<Section>(app.advancedOpen ? 'advanced' : 'general');

  let transfers = $state(4);
  let autoRetry = $state(true);
  let fastFiles = $state(app.fastFiles);
  let proxy = $state(false);
  let verbose = $state(false);
  let bwLimit = $state('off');
  let partSize = $state(256);

  let schedEnabled = $state(false);
  let schedTime = $state('03:00');
  let schedSource = $state('');
  let schedAccount = $state('');
  let schedDest = $state('電腦備份');
  let schedMode = $state<'additive' | 'mirror'>('additive');
  let schedLastRun = $state(0);
  // Set when a saved schedule has no account but accounts may still be loading;
  // the effect below fills it in once the account list is available. This must
  // be reactive, otherwise the effect runs once before accounts load and never
  // repairs the schedule.
  let healAccountOnLoad = $state(false);

  // The scheduler only starts when it has both a source and an account. Until
  // then the switch may be on but enabling the schedule would never run it.
  const scheduleAccountValid = $derived(
    !!schedAccount &&
      (data.accounts.length === 0 || data.accounts.some((a) => a.id === schedAccount))
  );
  const scheduleReady = $derived(scheduleAccountValid && !!schedSource);
  const scheduleCanEnable = $derived(data.accounts.length > 0);

  onMount(async () => {
    const s = await backend.getSettings();
    if (!s) {
      schedAccount = data.activeAccount || data.accounts[0]?.id || '';
      return;
    }
    transfers = s.backup?.transfers ?? 4;
    autoRetry = s.backup?.autoRetry ?? true;
    fastFiles = s.backup?.fastFiles ?? true;
    bwLimit = s.backup?.bwLimit ?? 'off';
    partSize = s.backup?.partSizeMB ?? 256;
    proxy = s.advanced?.proxy ?? false;
    verbose = s.advanced?.verbose ?? false;
    const sch = s.schedule;
    if (sch) {
      schedEnabled = sch.enabled ?? false;
      schedTime = sch.time || '03:00';
      schedSource = sch.sourcePaths?.[0] ?? '';
      schedAccount = sch.accountId || data.activeAccount || data.accounts[0]?.id || '';
      schedDest = sch.destRoot || '電腦備份';
      schedMode = sch.mode === 'mirror' ? 'mirror' : 'additive';
      schedLastRun = sch.lastRun ?? 0;
      // Repair a saved schedule that cannot fire: default it to the active
      // account, and persist enabled:false when a source is still missing.
      if (schedEnabled) {
        if (!schedSource) {
          void saveSettings(false);
        } else if (!sch.accountId) {
          if (schedAccount) void saveSettings(false);
          else healAccountOnLoad = true;
        }
      }
    }
  });

  $effect(() => {
    // Read the account state before the early return so the effect tracks it
    // and re-runs when the list finally populates.
    const fallback = data.activeAccount || data.accounts[0]?.id || '';
    if (!healAccountOnLoad || !fallback) return;
    healAccountOnLoad = false;
    schedAccount = fallback;
    void saveSettings(false);
  });

  async function pickScheduleSource(): Promise<void> {
    const picked = await backend.chooseFolder(t('settings.scheduleSource'));
    if (picked?.path) {
      schedSource = picked.path;
      persist();
    }
  }

  function persist(): void {
    void saveSettings(true);
  }

  async function saveSettings(notify: boolean): Promise<void> {
    // Never persist enabled:true without a source and a valid account; the
    // backend would keep a schedule that can never start.
    const s: DTO.Settings = {
      language: app.lang,
      theme: app.theme,
      onboarded: true,
      backup: { transfers, bwLimit, autoRetry, fastFiles, partSizeMB: partSize },
      schedule: {
        enabled: schedEnabled && scheduleReady,
        sourcePaths: schedSource ? [schedSource] : [],
        accountId: schedAccount,
        destRoot: schedDest,
        mode: schedMode,
        fastFiles,
        time: schedTime,
        lastRun: schedLastRun
      },
      advanced: { proxy, verbose, enginePath: '' },
      window: { width: 0, height: 0, x: 0, y: 0 }
    };
    await backend.saveSettings(s);
    backend.applyBwLimit(bwLimit);
    app.fastFiles = fastFiles;
    if (notify) app.notify(t('settings.saved'));
    const fresh = await backend.getSettings();
    if (fresh?.schedule) schedLastRun = fresh.schedule.lastRun ?? schedLastRun;
  }

  function resetAll(): void {
    transfers = 4;
    autoRetry = true;
    fastFiles = true;
    proxy = false;
    verbose = false;
    bwLimit = 'off';
    partSize = 256;
    schedEnabled = false;
    schedTime = '03:00';
    schedSource = '';
    schedAccount = '';
    schedDest = '電腦備份';
    schedMode = 'additive';
    schedLastRun = 0;
    persist();
  }

  const nav: { id: Section; key: string; icon: string }[] = [
    { id: 'general', key: 'settings.general', icon: 'settings' },
    { id: 'backup', key: 'settings.backup', icon: 'cloud-upload' },
    { id: 'advanced', key: 'settings.advanced', icon: 'shield' },
    { id: 'about', key: 'settings.about', icon: 'info' }
  ];
</script>

<div class="settings">
  <nav class="settings-nav">
    <h2 class="nv-title">{t('settings.title')}</h2>
    {#each nav as n (n.id)}
      <button class:active={section === n.id} onclick={() => (section = n.id)}>
        <Icon name={n.icon} size={18} />{t(n.key)}
      </button>
    {/each}
  </nav>

  <div class="settings-main">
    <div class="settings-wrap">
      {#if section === 'general'}
        <section class="set-section">
          <h2>{t('settings.general')}</h2>
          <div class="set-card">
            <div class="set-row">
              <div class="sl">
                <div class="st">{t('settings.language')}</div>
                <div class="sd">{t('settings.languageSub')}</div>
              </div>
              <div class="sc">
                <div class="segmented">
                  <button type="button" aria-pressed={app.lang === 'zh-Hant'} onclick={() => app.setLanguage('zh-Hant')}>繁體中文</button>
                  <button type="button" aria-pressed={app.lang === 'en'} onclick={() => app.setLanguage('en')}>English</button>
                </div>
              </div>
            </div>
            <div class="set-row">
              <div class="sl">
                <div class="st">{t('settings.appearance')}</div>
                <div class="sd">{t('settings.appearanceSub')}</div>
              </div>
              <div class="sc">
                <div class="segmented">
                  <button type="button" aria-pressed={app.theme === 'system'} onclick={() => app.setTheme('system')}>{t('settings.system')}</button>
                  <button type="button" aria-pressed={app.theme === 'light'} onclick={() => app.setTheme('light')}>{t('settings.light')}</button>
                  <button type="button" aria-pressed={app.theme === 'dark'} onclick={() => app.setTheme('dark')}>{t('settings.dark')}</button>
                </div>
              </div>
            </div>
          </div>
        </section>
      {:else if section === 'backup'}
        <section class="set-section">
          <h2>{t('settings.backup')}</h2>
          <div class="set-card">
            <div class="set-row">
              <div class="sl">
                <div class="st">{t('settings.transfers')}</div>
                <div class="sd">{t('settings.transfersSub')}</div>
              </div>
              <div class="sc">
                <div class="stepper">
                  <button aria-label="minus" onclick={() => { transfers = Math.max(1, transfers - 1); persist(); }}>
                    <Icon name="remove" size={18} />
                  </button>
                  <span class="value">{transfers}</span>
                  <button aria-label="plus" onclick={() => { transfers = Math.min(8, transfers + 1); persist(); }}>
                    <Icon name="add" size={18} />
                  </button>
                </div>
              </div>
            </div>
            <div class="set-row">
              <div class="sl">
                <div class="st">{t('settings.speed')}</div>
                <div class="sd">{t('settings.speedSub')}</div>
              </div>
              <div class="sc">
                <select class="textfield" style="height:36px;min-width:132px;border-radius:12px;background:var(--m3c-surface-container-high);border:0"
                  bind:value={bwLimit} onchange={persist} aria-label={t('settings.speed')}>
                  <option value="off">{t('common.unlimited')}</option>
                  <option value="1M">1 MB/s</option>
                  <option value="5M">5 MB/s</option>
                  <option value="10M">10 MB/s</option>
                  <option value="50M">50 MB/s</option>
                </select>
              </div>
            </div>
            <div class="set-row">
              <div class="sl">
                <div class="st">{t('settings.retry')}</div>
                <div class="sd">{t('settings.retrySub')}</div>
              </div>
              <div class="sc">
                <input type="checkbox" class="switch" role="switch" bind:checked={autoRetry} onchange={persist} />
              </div>
            </div>
            <div class="set-row">
              <div class="sl">
                <div class="st">{t('settings.fast')}</div>
                <div class="sd">{t('settings.fastSub', { size: '256 MB' })}</div>
              </div>
              <div class="sc">
                <input type="checkbox" class="switch" role="switch" bind:checked={fastFiles}
                  onchange={() => { app.fastFiles = fastFiles; persist(); }} />
              </div>
            </div>
            <div class="set-row">
              <div class="sl">
                <div class="st">{t('settings.parts')}</div>
                <div class="sd">{t('settings.partsSub')}</div>
              </div>
              <div class="sc">
                <select class="textfield" style="height:36px;min-width:132px;border-radius:12px;background:var(--m3c-surface-container-high);border:0"
                  bind:value={partSize} onchange={persist} aria-label={t('settings.parts')}>
                  <option value={128}>128 MB</option>
                  <option value={256}>256 MB</option>
                  <option value={512}>512 MB</option>
                  <option value={1024}>1 GB</option>
                </select>
              </div>
            </div>
            <div class="set-row">
              <div class="sl">
                <div class="st">{t('settings.schedule')}</div>
                <div class="sd">{t('settings.scheduleSub')}</div>
              </div>
              <div class="sc">
                <input type="checkbox" class="switch" role="switch" bind:checked={schedEnabled}
                  disabled={!scheduleCanEnable} onchange={persist} />
              </div>
            </div>
            {#if !scheduleCanEnable}
              <div class="field-hint" style="padding:0 18px 12px">{t('settings.scheduleNoAccount')}</div>
            {:else if schedEnabled && !scheduleReady}
              <div class="field-hint" style="padding:0 18px 12px">{t('settings.scheduleIncomplete')}</div>
            {/if}
            {#if schedEnabled}
              <div class="set-row">
                <div class="sl">
                  <div class="st">{t('settings.scheduleTime')}</div>
                  <div class="sd">{t('settings.scheduleTimeSub')}</div>
                </div>
                <div class="sc">
                  <input type="time" class="textfield" style="height:36px;border-radius:12px;background:var(--m3c-surface-container-high);border:0;padding:0 12px"
                    bind:value={schedTime} onchange={persist} />
                </div>
              </div>
              {#if schedLastRun > 0}
                <div class="set-row">
                  <div class="sl">
                    <div class="st">{t('settings.scheduleLastUpdated')}</div>
                    <div class="sd">{t('settings.scheduleLastUpdatedSub')}</div>
                  </div>
                  <div class="sc">
                    <span class="nums">{fmtTime(new Date(schedLastRun * 1000))}</span>
                  </div>
                </div>
              {/if}
              <div class="set-row">
                <div class="sl">
                  <div class="st">{t('settings.scheduleSource')}</div>
                  <div class="sd truncate">{schedSource || t('settings.scheduleSourceEmpty')}</div>
                </div>
                <div class="sc">
                  <button class="btn text sm" onclick={pickScheduleSource}>{t('common.change')}</button>
                </div>
              </div>
              <div class="set-row">
                <div class="sl">
                  <div class="st">{t('settings.scheduleAccount')}</div>
                  <div class="sd">{t('settings.scheduleAccountSub')}</div>
                </div>
                <div class="sc">
                  <select class="textfield" style="height:36px;min-width:150px;border-radius:12px;background:var(--m3c-surface-container-high);border:0"
                    bind:value={schedAccount} onchange={persist} aria-label={t('settings.scheduleAccount')}>
                    <option value="">{t('settings.scheduleAccountNone')}</option>
                    {#each data.accounts as a (a.id)}
                      <option value={a.id}>{accountName(a, app.lang)}</option>
                    {/each}
                  </select>
                </div>
              </div>
              <div class="set-row">
                <div class="sl">
                  <div class="st">{t('settings.scheduleDest')}</div>
                  <div class="sd">{t('settings.scheduleDestSub')}</div>
                </div>
                <div class="sc">
                  <input class="textfield" style="height:36px;width:180px;border-radius:12px;background:var(--m3c-surface-container-high);border:0;padding:0 12px"
                    bind:value={schedDest} onchange={persist} aria-label={t('settings.scheduleDest')} />
                </div>
              </div>
              <div class="set-row">
                <div class="sl">
                  <div class="st">{t('settings.scheduleMode')}</div>
                  <div class="sd">{t('settings.scheduleModeSub')}</div>
                </div>
                <div class="sc">
                  <div class="segmented">
                    <button type="button" aria-pressed={schedMode === 'additive'}
                      onclick={() => { schedMode = 'additive'; persist(); }}>{t('wizard.s3.modeAdd').split('（')[0]}</button>
                    <button type="button" aria-pressed={schedMode === 'mirror'}
                      onclick={() => { schedMode = 'mirror'; persist(); }}>{t('settings.scheduleMirror')}</button>
                  </div>
                </div>
              </div>
              <div style="padding:0 18px 14px">
                <div class="privacy"><Icon name="info" size={16} /><div>{t('settings.scheduleHint')}</div></div>
              </div>
            {/if}
          </div>
        </section>
      {:else if section === 'advanced'}
        <section class="set-section">
          <h2>{t('settings.advanced')}</h2>
          <div class="advanced-warning">
            <Icon name="warning" size={18} /><div>{t('settings.advancedWarn')}</div>
          </div>
          <div class="set-card">
            <div class="set-row">
              <div class="sl">
                <div class="st">{t('settings.engine')}</div>
                <div class="sd">{t('settings.engineSub')}</div>
              </div>
              <div class="sc">
                <div class="engine-line">
                  <span class="dot"></span><span class="nums">{data.engine.version || engineVersion}</span>
                  <button class="btn text sm" onclick={() => backend.openConfigFolder()}>{t('common.view')}</button>
                </div>
              </div>
            </div>
            <div class="set-row">
              <div class="sl">
                <div class="st">{t('settings.logs')}</div>
                <div class="sd">{t('settings.logsSub')}</div>
              </div>
              <div class="sc">
                <button class="btn text sm" onclick={() => backend.openLogFolder()}>{t('common.view')}</button>
              </div>
            </div>
            <div class="set-row">
              <div class="sl">
                <div class="st">{t('settings.proxy')}</div>
                <div class="sd">{t('settings.proxySub')}</div>
              </div>
              <div class="sc"><input type="checkbox" class="switch" role="switch" bind:checked={proxy} onchange={persist} /></div>
            </div>
            <div class="set-row">
              <div class="sl">
                <div class="st">{t('settings.verbose')}</div>
                <div class="sd">{t('settings.verboseSub')}</div>
              </div>
              <div class="sc"><input type="checkbox" class="switch" role="switch" bind:checked={verbose} onchange={persist} /></div>
            </div>
            <div class="set-row danger">
              <div class="sl">
                <div class="st">{t('settings.reset')}</div>
                <div class="sd">{t('settings.resetSub')}</div>
              </div>
              <div class="sc">
                <button class="btn text-danger sm" onclick={resetAll}>
                  {t('settings.resetAction')}
                </button>
              </div>
            </div>
          </div>
        </section>
      {:else}
        <section class="set-section">
          <h2>{t('settings.about')}</h2>
          <div class="set-card">
            <div class="set-row">
              <div class="sl">
                <div class="st">{t('settings.version', { v: appVersion })}</div>
                <div class="sd">Cloudnest / 雲栖</div>
              </div>
              <div class="sc">
                <button class="btn text sm" onclick={() => backend.openExternal('https://github.com/Miku0139oao/cloudnest/releases')}>
                  {t('settings.checkUpdate')}
                </button>
              </div>
            </div>
            <div class="set-row">
              <div class="sl"><div class="st">{t('settings.license')}</div></div>
              <div class="sc"><button class="btn text sm" onclick={() => backend.openExternal('https://github.com/rclone/rclone/blob/master/COPYING')}>{t('common.view')}</button></div>
            </div>
            <div style="padding:14px 18px">
              <div class="privacy"><Icon name="shield" size={17} /><div>{t('settings.privacy')}</div></div>
            </div>
          </div>
        </section>
      {/if}
    </div>
  </div>
</div>
