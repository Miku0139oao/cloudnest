<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import Icon from '$lib/components/Icon.svelte';
  import ProviderIcon from '$lib/components/ProviderIcon.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import { app } from '$lib/state.svelte';
  import { t } from '$lib/i18n';
  import { providers, providerName } from '$lib/mock';
  import { backend, isWails } from '$lib/backend';
  import { refreshAccounts } from '$lib/data.svelte';

  const provider = $derived(
    providers.find((p) => p.id === (app.params.provider ?? 'google')) ?? providers[0]
  );

  let phase = $state<'starting' | 'waiting' | 'done' | 'failed'>('starting');
  let authUrl = $state('');
  let error = $state('');
  let session = '';
  let timer: ReturnType<typeof setInterval> | undefined;
  let watch: ReturnType<typeof setInterval> | undefined;
  // Remote names present before this flow started. A new one means rclone has
  // already saved the token, even if the poll has not caught up yet.
  let known = new Set<string>();
  let seeded = false;

  const waiting = $derived(phase === 'waiting');

  async function seedKnown(): Promise<void> {
    const res = await backend.listAccounts();
    if (res?.items) {
      known = new Set(res.items.map((a) => a.id));
      seeded = true;
    }
  }

  async function accountAppeared(): Promise<boolean> {
    if (!seeded) return false;
    const res = await backend.listAccounts();
    if (!res?.items) return false;
    const fresh = res.items.find((a) => !known.has(a.id));
    if (!fresh) return false;
    known.add(fresh.id);
    return true;
  }

  async function succeed(): Promise<void> {
    if (phase === 'done') return;
    phase = 'done';
    clearInterval(timer);
    clearInterval(watch);
    await refreshAccounts();
    setTimeout(() => app.go('clouds'), 1000);
  }

  async function start(): Promise<void> {
    if (!isWails) {
      phase = 'waiting';
      return;
    }
    phase = 'starting';
    error = '';
    await seedKnown();
    const res = await backend.beginConnect(provider.id, '');
    if (!res) {
      phase = 'failed';
      error = 'backend unavailable';
      return;
    }
    if (res.err) {
      phase = 'failed';
      error = res.err.detail || res.err.message;
      return;
    }
    session = res.id;
    startPolling();
    poll();
  }

  // A retry must resume the original request: the remote name and the OAuth
  // client (the user's own, when the flow came from the client-ID dialog).
  // Starting a fresh sign-in here used to create a second account on rclone's
  // shared client instead.
  async function retry(): Promise<void> {
    if (!isWails) {
      phase = 'waiting';
      return;
    }
    if (!session) {
      await start();
      return;
    }
    phase = 'starting';
    error = '';
    const res = await backend.retryConnect(session);
    if (!res) {
      phase = 'failed';
      error = 'backend unavailable';
      return;
    }
    if (res.err) {
      phase = 'failed';
      error = res.err.detail || res.err.message;
      return;
    }
    session = res.id;
    // The failure which showed the Retry button already cleared the intervals;
    // without re-arming them the retried sign-in is never observed and the
    // sheet hangs on "waiting". The previous attempt's URL must not be offered
    // again either.
    authUrl = '';
    startPolling();
    poll();
  }

  async function poll(): Promise<void> {
    if (!session || phase === 'done') return;
    const st = await backend.connectState(session);
    if (!st) return;
    authUrl = st.authUrl ?? '';
    if (st.state === 'waiting' || st.state === 'awaiting_url' || st.state === 'preparing') {
      phase = 'waiting';
    } else if (st.state === 'done') {
      await succeed();
    } else if (st.state === 'failed') {
      phase = 'failed';
      error = st.err?.detail || st.err?.message || 'sign-in failed';
      clearInterval(timer);
      clearInterval(watch);
    } else if (st.state === 'cancelled') {
      phase = 'failed';
      error = '';
      clearInterval(timer);
      clearInterval(watch);
    }
  }

  // (Re)arms both observation loops. A failed poll clears them, so a retry has
  // to start them again rather than rely on the mount-time intervals.
  function startPolling(): void {
    if (timer) clearInterval(timer);
    if (watch) clearInterval(watch);
    timer = setInterval(poll, 700);
    // The provider's token is saved as soon as rclone exits; watching the
    // account list makes the sheet finish even if a poll is missed.
    watch = setInterval(async () => {
      if (phase === 'waiting' && (await accountAppeared())) await succeed();
    }, 1200);
  }

  onMount(() => {
    if (app.params.session) {
      session = app.params.session;
      phase = 'waiting';
      seedKnown().then(poll);
    } else {
      start();
    }
    startPolling();
  });

  onDestroy(() => {
    if (timer) clearInterval(timer);
    if (watch) clearInterval(watch);
    if (session && phase !== 'done' && isWails) backend.cancelConnect(session);
  });

  function cancel(): void {
    if (session && isWails) backend.cancelConnect(session);
    app.closeOverlay();
  }
</script>

<Modal variant="sheet" labelledby="connect-title" onclose={cancel}>
  <div class="sheet">
    <div class="sheet-head">
      <div class="grow"></div>
      <button class="icon-button" aria-label={t('common.close')} onclick={cancel}>
        <Icon name="close" size={20} />
      </button>
    </div>

    <div class="sheet-body">
      <div class="connect-hero">
        <span class="ptile {provider.id}"><ProviderIcon provider={provider.id} size={36} /></span>
        <h2 id="connect-title">{t('sheet.connectTitle', { name: providerName(provider, app.lang) })}</h2>
        <p>{t('sheet.connectBody')}</p>
      </div>

      <div class="oauth-steps">
        <div class="oauth-step" class:done={waiting || phase === 'done'} class:current={!waiting && phase !== 'done'}>
          <span class="n">{#if waiting || phase === 'done'}<Icon name="check" size={14} />{:else}1{/if}</span>
          <div><div class="ot">{t('sheet.oauth1')}</div></div>
        </div>
        <div class="oauth-step" class:current={waiting} class:pending={!waiting}>
          <span class="n">2</span>
          <div><div class="ot">{t('sheet.oauth2')}</div></div>
        </div>
        <div class="oauth-step pending">
          <span class="n">3</span>
          <div><div class="ot">{t('sheet.oauth3')}</div></div>
        </div>
      </div>

      {#if waiting}
        <div class="oauth-status">
          <Icon name="sync" size={18} />{t('sheet.waiting')}
          <span class="bar indeterminate"><i></i></span>
        </div>
      {/if}

      {#if phase === 'done'}
        <div class="restore-done" style="margin-top:16px">
          <Icon name="check-circle" size={22} />
          <div>
            <div class="dt">{t('sheet.connected')}</div>
            <div class="db">{t('sheet.connectedBody', { name: providerName(provider, app.lang) })}</div>
          </div>
        </div>
      {:else if phase === 'failed'}
        <div class="banner" style="margin-top:16px">
          <Icon name="error" size={20} />
          <div>
            <div class="b-title">{t('sheet.signInFailed')}</div>
            <div>{error}</div>
          </div>
        </div>
      {:else}
        <div class="banner info" style="margin-top:14px;align-items:center">
          <Icon name="lock" size={18} /><div>{t('sheet.connectTrust')}</div>
        </div>
      {/if}
    </div>

    <div class="sheet-foot" style="flex-direction:column;align-items:stretch;gap:8px">
      {#if phase === 'failed'}
        <button class="btn filled" onclick={retry}>{t('common.retry')}</button>
      {:else if phase !== 'done'}
        <button class="btn filled" disabled={!authUrl}
          onclick={() => authUrl && backend.openExternal(authUrl)}>
          <Icon name="open-in-new" size={18} /><span>{t('sheet.reopen')}</span>
        </button>
      {/if}
      <button class="btn text" onclick={cancel}>{t('common.cancel')}</button>
    </div>
  </div>
</Modal>
