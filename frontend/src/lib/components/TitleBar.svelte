<script lang="ts">
  import { Window } from '@wailsio/runtime';
  import Icon from './Icon.svelte';
  import { app, baseFor } from '$lib/state.svelte';
  import { t } from '$lib/i18n';

  function contextLabel(): string {
    const r = app.route;
    if (r.startsWith('wizard/')) return t('wizard.title');
    if (r === 'clouds/add') return t('sheet.addTitle');
    if (r === 'clouds/connect' || r === 'clouds/oauth') return t('clouds.add');
    if (r === 'files/restore') return t('restore.title', { name: '家庭影片_2024.mp4' });
    const base = baseFor(r);
    const map: Record<string, string> = {
      home: 'nav.home', clouds: 'nav.clouds', files: 'nav.files',
      activity: 'nav.activity', settings: 'nav.settings', onboarding: 'onboarding.title'
    };
    return t(map[base] ?? 'nav.home');
  }

  function win(action: 'min' | 'max' | 'close'): void {
    try {
      if (action === 'min') Window.Minimise();
      else if (action === 'max') Window.ToggleMaximise();
      else Window.Close();
    } catch {
      /* running in a plain browser during UI development */
    }
  }
</script>

<header class="titlebar">
  <div class="brand">
    <span class="appmark"><Icon name="cloud" size={15} /></span>
    <span class="brandname">雲栖</span>
    <span class="brandname-en">Cloudnest</span>
  </div>
  <div class="titlebar-context">
    <span class="sep"></span>
    <span>{contextLabel()}</span>
  </div>
  <div class="titlebar-spacer"></div>

  <div class="titlebar-actions">
    <div class="lang" role="group" aria-label="Language">
      <button
        aria-pressed={app.lang === 'zh-Hant'}
        onclick={() => app.setLanguage('zh-Hant')}>中</button
      >
      <button aria-pressed={app.lang === 'en'} onclick={() => app.setLanguage('en')}>EN</button>
    </div>
    <button
      class="iconbtn"
      aria-label={app.resolvedTheme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}
      onclick={() => app.toggleTheme()}
    >
      <Icon name={app.resolvedTheme === 'dark' ? 'light-mode' : 'dark-mode'} size={18} />
    </button>
    <div class="wincontrols">
      <button aria-label="Minimize" onclick={() => win('min')}>
        <svg class="i" width="16" height="16" viewBox="0 0 16 16"
          ><line x1="3" y1="8" x2="13" y2="8" stroke="currentColor" stroke-width="1" /></svg
        >
      </button>
      <button aria-label="Maximize" onclick={() => win('max')}>
        <svg class="i" width="16" height="16" viewBox="0 0 16 16" fill="none"
          ><rect x="3.5" y="3.5" width="9" height="9" rx="1" stroke="currentColor" stroke-width="1"
          /></svg
        >
      </button>
      <button class="close" aria-label="Close" onclick={() => win('close')}>
        <svg class="i" width="16" height="16" viewBox="0 0 16 16"
          ><path
            d="M4 4l8 8M12 4l-8 8"
            stroke="currentColor"
            stroke-width="1.1"
            stroke-linecap="round"
          /></svg
        >
      </button>
    </div>
  </div>
</header>
