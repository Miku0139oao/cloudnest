<script lang="ts">
  import { onMount } from 'svelte';
  import { boot, refreshEngine, data } from '$lib/data.svelte';
  import TitleBar from '$lib/components/TitleBar.svelte';
  import NavRail from '$lib/components/NavRail.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import { t } from '$lib/i18n';
  import { backend, isWails } from '$lib/backend';
  import { Events } from '@wailsio/runtime';
  import Home from './views/Home.svelte';
  import Onboarding from './views/Onboarding.svelte';
  import Clouds from './views/Clouds.svelte';
  import Files from './views/Files.svelte';
  import Activity from './views/Activity.svelte';
  import Settings from './views/Settings.svelte';
  import WizardOverlay from './views/WizardOverlay.svelte';
  import AddProviderSheet from './views/AddProviderSheet.svelte';
  import ConnectSheet from './views/ConnectSheet.svelte';
  import RestoreDialog from './views/RestoreDialog.svelte';
  import { app, baseFor, overlayFor } from '$lib/state.svelte';

  const base = $derived(baseFor(app.route));
  const overlay = $derived(overlayFor(app.route));
  // Set when the window close was interrupted because transfers are running.
  let closeAsk = $state(false);

  onMount(() => {
    boot();
    const timer = setInterval(() => {
      if (!data.ready) refreshEngine();
    }, 1500);
    const off = isWails
      ? Events.On('cloudnest:close-requested', () => (closeAsk = true))
      : undefined;
    return () => {
      clearInterval(timer);
      off?.();
    };
  });
</script>

<div class="app">
  <TitleBar />
  <div class="body">
    {#if base !== 'onboarding'}
      <NavRail />
    {/if}
    <main class="content">
      {#if base === 'onboarding'}
        <Onboarding />
      {:else if base === 'clouds'}
        <Clouds />
      {:else if base === 'files'}
        <Files />
      {:else if base === 'activity'}
        <Activity />
      {:else if base === 'settings'}
        <Settings />
      {:else}
        <Home />
      {/if}

      {#if overlay === 'wizard'}
        <WizardOverlay />
      {:else if overlay === 'add-provider'}
        <AddProviderSheet />
      {:else if overlay === 'connect'}
        <ConnectSheet />
      {:else if overlay === 'restore'}
        <RestoreDialog />
      {/if}

      {#if app.toast}
        <div class="snackbar">
          <span>{app.toast}</span>
          <div class="grow"></div>
          <button class="s-action" onclick={() => (app.toast = null)}>OK</button>
        </div>
      {/if}
    </main>
  </div>
</div>

{#if closeAsk}
  <Modal labelledby="close-title" onclose={() => (closeAsk = false)}>
    <div class="dialog" style="width: 480px">
      <div class="dialog-head" style="padding-bottom: 2px">
        <div class="grow">
          <div class="dialog-title" id="close-title">{t('progress.closeTitle')}</div>
        </div>
      </div>
      <div class="dialog-body">
        <p style="margin: 4px 0 8px; color: var(--m3c-on-surface-variant)">
          {t('progress.closeBody')}
        </p>
      </div>
      <div class="dialog-foot">
        <!-- svelte-ignore a11y_autofocus -->
        <button class="btn text" autofocus onclick={() => (closeAsk = false)}>
          {t('progress.keepGoing')}
        </button>
        <div class="grow"></div>
        <button class="btn danger-filled" onclick={() => backend.confirmClose()}>
          {t('progress.stop')}
        </button>
      </div>
    </div>
  </Modal>
{/if}
