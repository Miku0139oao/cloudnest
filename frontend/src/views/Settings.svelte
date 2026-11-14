<script lang="ts">
  import Icon from '$lib/components/Icon.svelte';
  import { app } from '$lib/state.svelte';
  import { t } from '$lib/i18n';
  import { appVersion, engineVersion } from '$lib/mock';
  import { data } from '$lib/data.svelte';

  type Section = 'general' | 'backup' | 'advanced' | 'about';
  let section = $state<Section>(app.advancedOpen ? 'advanced' : 'general');

  let transfers = $state(4);
  let autoRetry = $state(true);
  let fastFiles = $state(app.fastFiles);
  let proxy = $state(false);
  let verbose = $state(false);

  const nav: { id: Section; key: string; icon: string }[] = [
    { id: 'general', key: 'settings.general', icon: 'settings' },
    { id: 'backup', key: 'settings.backup', icon: 'cloud-upload' },
    { id: 'advanced', key: 'settings.advanced', icon: 'shield' },
    { id: 'about', key: 'settings.about', icon: 'info' }
  ];

  function saved(): void {
    app.notify(t('settings.saved'));
  }
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
                  <button aria-label="minus" onclick={() => { transfers = Math.max(1, transfers - 1); saved(); }}>
                    <Icon name="remove" size={18} />
                  </button>
                  <span class="value">{transfers}</span>
                  <button aria-label="plus" onclick={() => { transfers = Math.min(8, transfers + 1); saved(); }}>
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
                <button class="textfield" style="height:36px;min-width:132px;justify-content:space-between;border-radius:999px;background:var(--m3c-surface-container-high)">
                  <span>{t('common.unlimited')}</span><Icon name="expand-more" size={18} />
                </button>
              </div>
            </div>
            <div class="set-row">
              <div class="sl">
                <div class="st">{t('settings.retry')}</div>
                <div class="sd">{t('settings.retrySub')}</div>
              </div>
              <div class="sc">
                <input type="checkbox" class="switch" role="switch" bind:checked={autoRetry} onchange={saved} />
              </div>
            </div>
            <div class="set-row">
              <div class="sl">
                <div class="st">{t('settings.fast')}</div>
                <div class="sd">{t('settings.fastSub', { size: '256 MB' })}</div>
              </div>
              <div class="sc">
                <input type="checkbox" class="switch" role="switch" bind:checked={fastFiles}
                  onchange={() => { app.fastFiles = fastFiles; saved(); }} />
              </div>
            </div>
            <div class="set-row">
              <div class="sl">
                <div class="st">{t('settings.parts')}</div>
                <div class="sd">{t('settings.partsSub')}</div>
              </div>
              <div class="sc"></div>
            </div>
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
                  <button class="btn text sm" onclick={() => app.notify(engineVersion)}>{t('common.view')}</button>
                </div>
              </div>
            </div>
            <div class="set-row">
              <div class="sl">
                <div class="st">{t('settings.proxy')}</div>
                <div class="sd">{t('settings.proxySub')}</div>
              </div>
              <div class="sc"><input type="checkbox" class="switch" role="switch" bind:checked={proxy} onchange={saved} /></div>
            </div>
            <div class="set-row">
              <div class="sl">
                <div class="st">{t('settings.verbose')}</div>
                <div class="sd">{t('settings.verboseSub')}</div>
              </div>
              <div class="sc"><input type="checkbox" class="switch" role="switch" bind:checked={verbose} onchange={saved} /></div>
            </div>
            <div class="set-row danger">
              <div class="sl">
                <div class="st">{t('settings.reset')}</div>
                <div class="sd">{t('settings.resetSub')}</div>
              </div>
              <div class="sc">
                <button class="btn text-danger sm" onclick={() => app.notify(t('settings.resetAction'))}>
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
                <button class="btn text sm" onclick={() => app.notify(t('settings.checkUpdate'))}>
                  {t('settings.checkUpdate')}
                </button>
              </div>
            </div>
            <div class="set-row">
              <div class="sl"><div class="st">{t('settings.license')}</div></div>
              <div class="sc"><button class="btn text sm" onclick={() => app.notify(t('settings.license'))}>{t('common.view')}</button></div>
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
