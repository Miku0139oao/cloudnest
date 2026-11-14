<script lang="ts">
  import Icon from '$lib/components/Icon.svelte';
  import ProviderIcon from '$lib/components/ProviderIcon.svelte';
  import { app } from '$lib/state.svelte';
  import { t } from '$lib/i18n';
  import { providers } from '$lib/mock';

  const top = providers.slice(0, 4);
  const mega = providers.find((p) => p.id === 'mega');
</script>

<div class="onboarding">
  <div class="onboard-card">
    <div class="onboard-mark"><Icon name="cloud" size={34} /></div>
    <h1>{t('onboarding.title')}</h1>
    <p class="lede">{t('onboarding.sub')}</p>

    <div class="provider-row">
      {#each [...top, ...(mega ? [mega] : [])] as p (p.id)}
        <button
          class="provider-tile"
          class:recommended={p.recommended}
          onclick={() => app.go('clouds/connect', { provider: p.id })}
        >
          {#if p.recommended}<span class="rec">{t('onboarding.recommended')}</span>{/if}
          <span class="ptile {p.id}"><ProviderIcon provider={p.id} size={21} /></span>
          <span>{app.lang === 'en' ? p.nameEn : p.name}</span>
        </button>
      {/each}
    </div>

    <div class="onboard-actions">
      <button class="btn filled" onclick={() => app.go('clouds/connect', { provider: 'google' })}>
        <Icon name="cloud-upload" size={18} /><span>{t('onboarding.connect')}</span>
      </button>
      <button class="btn text" onclick={() => app.go('clouds/add')}>{t('onboarding.other')}</button>
      <span class="trust"><Icon name="lock" size={15} />{t('onboarding.trust')}</span>
    </div>

    <div style="margin-top:22px;font:var(--type-body-s);color:var(--m3c-on-surface-variant)">
      {t('onboarding.signup')} <button class="linklike" onclick={() => app.notify(t('onboarding.signupLink'))}>{t('onboarding.signupLink')}</button>
    </div>
  </div>
</div>
