<script lang="ts">
  import Icon from '$lib/components/Icon.svelte';
  import ProviderIcon from '$lib/components/ProviderIcon.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import { app } from '$lib/state.svelte';
  import { t } from '$lib/i18n';
  import { providers, providerName } from '$lib/mock';

  const tierA = providers.filter((p) => p.tier === 'A');
  const tierBC = providers.filter((p) => p.tier !== 'A');
</script>

<Modal variant="sheet" labelledby="add-sheet-title" onclose={() => app.closeOverlay()}>
  <div class="sheet">
    <div class="sheet-head">
      <div class="grow">
        <div class="sheet-title" id="add-sheet-title">{t('sheet.addTitle')}</div>
        <div class="sheet-sub">{t('sheet.addSub')}</div>
      </div>
      <button class="icon-button" aria-label={t('common.close')} onclick={() => app.closeOverlay()}>
        <Icon name="close" size={20} />
      </button>
    </div>

    <div class="sheet-body">
      <div class="section-label">{t('sheet.oneClick')}</div>
      <div class="provider-grid">
        {#each tierA as p (p.id)}
          <button class="provider-card" onclick={() => app.go('clouds/connect', { provider: p.id })}>
            {#if p.recommended}<span class="rec">{t('onboarding.recommended')}</span>{/if}
            <span class="ptile {p.id}"><ProviderIcon provider={p.id} size={24} /></span>
            <span>{providerName(p, app.lang)}</span>
          </button>
        {/each}
      </div>

      <div class="section-label">{t('sheet.manual')}</div>
      <div>
        {#each tierBC as p (p.id)}
          <button class="tier-row" onclick={() => app.go('clouds/connect', { provider: p.id })}>
            <span class="ptile {p.id}"><ProviderIcon provider={p.id} size={20} /></span>
            <span class="tr-n">{providerName(p, app.lang)}</span>
            <span class="chip">{t('sheet.advanced')}</span>
            <Icon name="chevron-right" size={16} />
          </button>
        {/each}
      </div>

      <div class="privacy" style="margin-top:22px">
        <Icon name="lock" size={17} /><div>{t('onboarding.trust')}</div>
      </div>
    </div>

    <div class="sheet-foot">
      <!-- svelte-ignore a11y_autofocus -->
      <button class="btn text" autofocus onclick={() => app.closeOverlay()}>{t('common.cancel')}</button>
    </div>
  </div>
</Modal>
