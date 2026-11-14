<script lang="ts">
  import Icon from './Icon.svelte';
  import { app, baseFor } from '$lib/state.svelte';
  import { t } from '$lib/i18n';

  const items = [
    { id: 'home', key: 'nav.home', icon: 'home' },
    { id: 'clouds', key: 'nav.clouds', icon: 'cloud' },
    { id: 'files', key: 'nav.files', icon: 'folder' },
    { id: 'activity', key: 'nav.activity', icon: 'sync' }
  ];

  const active = $derived(baseFor(app.route));
</script>

<nav class="rail" aria-label="Main">
  {#each items as item (item.id)}
    <button
      class="rail-item"
      aria-current={active === item.id ? 'page' : undefined}
      onclick={() => app.go(item.id)}
    >
      <span class="indicator"><Icon name={item.icon} size={22} /></span>
      <span class="label">{t(item.key)}</span>
      {#if item.id === 'activity'}<span class="badge">2</span>{/if}
    </button>
  {/each}
  <div class="rail-spacer"></div>
  <div class="rail-sep"></div>
  <button
    class="rail-item"
    aria-current={active === 'settings' ? 'page' : undefined}
    onclick={() => app.go('settings')}
  >
    <span class="indicator"><Icon name="settings" size={22} /></span>
    <span class="label">{t('nav.settings')}</span>
  </button>
</nav>
