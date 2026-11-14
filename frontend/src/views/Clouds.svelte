<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '$lib/components/Icon.svelte';
  import ProviderIcon from '$lib/components/ProviderIcon.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import { app } from '$lib/state.svelte';
  import { t, fmtBytes, fmtTime } from '$lib/i18n';
  import { accountName, type Account } from '$lib/mock';
  import { data, refreshAccounts, refreshUsage } from '$lib/data.svelte';
  import { backend } from '$lib/backend';

  onMount(() => {
    const timer = setTimeout(() => refreshUsage(), 1500);
    return () => clearTimeout(timer);
  });

  let removeFor = $state<Account | null>(null);
  let removing = $state(false);

  function closeMenu(id: string): void {
    const el = document.getElementById(`menu-${id}`);
    if (el && 'hidePopover' in el) (el as HTMLElement).hidePopover();
  }

  async function confirmRemove(): Promise<void> {
    if (!removeFor || removing) return;
    removing = true;
    await backend.removeAccount(removeFor.id);
    await refreshAccounts();
    removing = false;
    removeFor = null;
    app.notify(t('clouds.removed'));
  }

  async function relogin(a: Account): Promise<void> {
    const res = await backend.beginConnect(a.provider, a.name);
    if (res?.id) {
      app.go('clouds/oauth', { provider: a.provider, session: res.id });
    } else {
      app.notify(res?.err?.message ?? t('sheet.signInFailed'));
    }
  }
</script>

<div class="scroll">
  <div class="page clouds">
    <div class="page-head">
      <div>
        <h1 class="page-title">{t('clouds.title')}</h1>
        <p class="page-sub">{t('clouds.sub')}</p>
      </div>
      <button class="btn tonal" onclick={() => app.go('clouds/add')}>
        <Icon name="add" size={18} /><span>{t('clouds.add')}</span>
      </button>
    </div>

    <div class="cloud-grid">
      {#each data.accounts as a (a.id)}
        <article class="card cloud-card">
          <div class="cc-top">
            <span class="ptile {a.provider}"><ProviderIcon provider={a.provider} size={24} /></span>
            <div class="grow">
              <div class="cc-name">{accountName(a, app.lang)}</div>
              <div class="cc-sub">{a.email || ' '}</div>
            </div>
            <div style="display:flex;align-items:center;gap:8px">
              <span class="status" class:ok={a.status === 'connected'} class:err={a.status !== 'connected'}>
                <span class="dot"></span>
                {a.status === 'connected' ? t('clouds.connected') : t('clouds.relogin')}
              </span>
              <button
                class="icon-button"
                aria-label="More"
                style={`width:32px;height:32px;anchor-name:--acct-${a.id}`}
                popovertarget={`menu-${a.id}`}
              >
                <Icon name="more-vert" size={18} />
              </button>
            </div>
          </div>

          <div class="usage-line">
            <span>{a.total > 0 ? t('clouds.used', { used: fmtBytes(a.used), total: fmtBytes(a.total) }) : t('clouds.quotaUnknown')}</span>
            <span class="nums">{a.total > 0 ? Math.round((a.used / a.total) * 100) + '%' : ''}</span>
          </div>
          <span class="bar"><i style={`width:${a.total > 0 ? Math.round((a.used / a.total) * 100) : 0}%`}></i></span>
          <div class="cc-foot">
            <span>{a.lastBackup ? t('clouds.lastBackup', { time: fmtTime(a.lastBackup) }) : t('clouds.neverBackedUp')}</span>
            <button class="btn text sm" onclick={() => app.go('files')}>{t('clouds.browse')}</button>
          </div>

          <div id={`menu-${a.id}`} popover class="account-menu" style={`position-anchor:--acct-${a.id}`}>
            <button type="button" onclick={() => { closeMenu(a.id); app.go('files'); }}>
              <Icon name="folder-open" size={18} />{t('clouds.browse')}
            </button>
            <button type="button" onclick={() => { closeMenu(a.id); relogin(a); }}>
              <Icon name="key" size={18} />{t('clouds.reloginAction')}
            </button>
            <hr class="divider" />
            <button type="button" class="danger" onclick={() => { closeMenu(a.id); removeFor = a; }}>
              <Icon name="delete" size={18} />{t('clouds.remove')}
            </button>
          </div>
        </article>
      {/each}

      <button class="card outlined cloud-card"
        style="align-items:center;justify-content:center;gap:12px;min-height:182px"
        onclick={() => app.go('clouds/add')}>
        <span style="width:52px;height:52px;border-radius:16px;background:var(--m3c-primary-container);color:var(--m3c-on-primary-container);display:grid;place-items:center">
          <Icon name="add" size={26} />
        </span>
        <div style="text-align:center">
          <div class="cc-name">{t('clouds.add')}</div>
          <div class="cc-sub">{t('sheet.addSub')}</div>
        </div>
      </button>
    </div>
  </div>
</div>

{#if removeFor}
  <Modal labelledby="remove-title" onclose={() => (removeFor = null)}>
    <div class="dialog" style="width: 520px">
      <div class="dialog-head" style="padding-bottom:2px">
        <div class="grow">
          <div class="dialog-title" id="remove-title">
            {t('clouds.removeTitle', { name: removeFor.name })}
          </div>
        </div>
        <button class="icon-button" aria-label={t('common.close')} onclick={() => (removeFor = null)}>
          <Icon name="close" size={20} />
        </button>
      </div>
      <div class="dialog-body">
        <p style="margin:4px 0 8px;color:var(--m3c-on-surface-variant)">
          {t('clouds.removeBody')}
        </p>
      </div>
      <div class="dialog-foot">
        <!-- svelte-ignore a11y_autofocus -->
        <button class="btn text" autofocus onclick={() => (removeFor = null)}>{t('common.cancel')}</button>
        <div class="grow"></div>
        <button class="btn danger-filled" disabled={removing} onclick={confirmRemove}>
          <Icon name="delete" size={18} /><span>{t('clouds.remove')}</span>
        </button>
      </div>
    </div>
  </Modal>
{/if}
