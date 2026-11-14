<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '$lib/components/Icon.svelte';
  import ProviderIcon from '$lib/components/ProviderIcon.svelte';
  import { app } from '$lib/state.svelte';
  import { t, fmtBytes, fmtTime } from '$lib/i18n';
  import { accountName } from '$lib/mock';
  import { data, refreshUsage, refreshJobs } from '$lib/data.svelte';
  import { backend, isWails } from '$lib/backend';

  onMount(() => {
    // Quota is nice-to-have; never let it contend with a file listing.
    const timer = setTimeout(() => {
      if (!data.cloudLoading) refreshUsage();
    }, 2500);
    return () => clearTimeout(timer);
  });

  const n = $derived(data.accounts.length);

  // rclone skips files that already arrived, so retrying a failed backup only
  // transfers the remainder.
  async function retry(id: string): Promise<void> {
    if (!isWails) {
      app.go('wizard/1');
      return;
    }
    const res = await backend.resumeJob(id);
    if (!res || res.err) {
      app.notify(t('activity.retryGone'));
      return;
    }
    await refreshJobs();
    app.go('activity');
  }
</script>

<div class="scroll">
  <div class="page home">
    <div class="home-head">
      <div class="greeting">
        <h1>{t('home.greeting')}</h1>
        <p>{n ? t('home.subtitle.connected', { n }) : t('home.subtitle.empty')}</p>
      </div>
    </div>

    <section class="hero">
      <h2>{t('home.hero.title')}</h2>
      <p>{t('home.hero.sub')}</p>
      <button class="btn filled" onclick={() => app.go('wizard/1')}>
        <Icon name="cloud-upload" size={18} /><span>{t('home.hero.cta')}</span>
      </button>
      <svg class="hero-art" viewBox="0 0 300 160" aria-hidden="true">
        <circle cx="236" cy="42" r="30" fill="#ffffff" opacity=".28" />
        <circle cx="196" cy="126" r="16" fill="#ffffff" opacity=".22" />
        <circle cx="92" cy="34" r="8" fill="#ffffff" opacity=".35" />
        <g transform="rotate(-9 118 82)">
          <rect x="64" y="30" width="72" height="96" rx="16" fill="#ffffff" opacity=".45" />
        </g>
        <g transform="rotate(8 196 80)">
          <rect x="168" y="22" width="84" height="110" rx="18" fill="#ffffff" opacity=".72" />
        </g>
        <rect x="118" y="34" width="80" height="94" rx="18" fill="#7114FF" />
        <path d="M158 104V68l-13 13-6.5-6.5L158 55l19.5 19.5-6.5 6.5L158 68v36z" fill="#ffffff" />
        <rect x="132" y="110" width="52" height="6" rx="3" fill="#ffffff" opacity=".55" />
      </svg>
    </section>

    <div class="quick-grid">
      <button class="card tonal quick-card" onclick={() => app.go('files')}>
        <span class="qq"><Icon name="cloud-download" size={22} /></span>
        <span class="grow qtext">
          <span class="qt">{t('home.quick.download')}</span>
          <span class="qs">{t('home.quick.downloadSub')}</span>
        </span>
        <span class="go"><Icon name="chevron-right" size={20} /></span>
      </button>
      <button class="card tonal quick-card" onclick={() => app.go('clouds')}>
        <span class="qq"><Icon name="add-circle" size={22} /></span>
        <span class="grow qtext">
          <span class="qt">{t('home.quick.accounts')}</span>
          <span class="qs">{t('home.quick.accountsSub')}</span>
        </span>
        <span class="go"><Icon name="chevron-right" size={20} /></span>
      </button>
    </div>

    <div class="home-grid">
      <section>
        <div class="section-head"><h3 class="section-title">{t('home.clouds.title')}</h3></div>
        <div class="cloud-strip">
          {#each data.accounts as a (a.id)}
            {@const pct = a.total > 0 ? Math.round((a.used / a.total) * 100) : 0}
            <button class="card cloud-mini interactive" style="text-align:left;width:100%;font:inherit;color:inherit" onclick={() => app.go('files')}>
              <div class="top">
                <span class="ptile {a.provider}">
                  <ProviderIcon provider={a.provider} size={20} />
                </span>
                <div class="grow">
                  <div class="nm">{accountName(a, app.lang)}</div>
                  <div class="st">{t('clouds.connected')}</div>
                </div>
              </div>
              <div class="usage">
                <span>{a.total > 0 ? t('home.used', { used: fmtBytes(a.used), total: fmtBytes(a.total) }) : t('clouds.quotaUnknown')}</span>
                <span class="nums">{a.total > 0 ? pct + '%' : ''}</span>
              </div>
              <span class="bar"><i style={`width:${pct}%`}></i></span>
            </button>
          {/each}
          <button class="card outlined cloud-mini" style="align-items:center;justify-content:center;gap:8px" onclick={() => app.go('clouds/add')}>
            <span style="width:40px;height:40px;border-radius:12px;background:var(--m3c-primary-container);color:var(--m3c-on-primary-container);display:grid;place-items:center">
              <Icon name="add" size={22} />
            </span>
            <span class="nm" style="font:var(--type-label-l)">{t('clouds.add')}</span>
          </button>
        </div>
      </section>

      <section>
        <div class="section-head">
          <h3 class="section-title">{t('home.recent.title')}</h3>
          <button class="btn text sm" onclick={() => app.go('activity')}>{t('common.viewAll')}</button>
        </div>
        <div class="card recent" style="padding:6px">
          {#if data.history.length === 0}
            <div class="empty" style="padding:26px 16px 22px">
              <div class="e-title">{t('activity.noHistory')}</div>
              <div class="e-body">{t('home.recent.empty')}</div>
            </div>
          {/if}
          {#each data.history.slice(0, 3) as h (h.id)}
            <div class="recent-row" role="button" tabindex="0" onclick={() => app.go('activity')}
              onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); app.go('activity'); } }}>
              <span class="st-icon {h.state === 'failed' ? 'err' : 'ok'}">
                <Icon name={h.state === 'failed' ? 'error' : 'check'} size={18} />
              </span>
              <div class="rmain">
                <div class="rt">
                  {h.kind === 'restore'
                    ? t('activity.restoreOf', { name: h.name ?? '' })
                    : t(h.state === 'failed' ? 'home.recent.failed' : 'home.recent.done', {
                        source: (app.lang === 'en' ? h.sourceEn : h.source) ?? ''
                      })}
                </div>
                <div class="rs">
                  {fmtTime(h.time)} · {t('home.recent.files', {
                    files: h.kind === 'restore' ? 1 : (h.files ?? 0).toLocaleString('en-US'),
                    size: fmtBytes(h.size)
                  })}
                </div>
              </div>
              {#if h.state === 'failed'}
                <button class="btn text sm" onclick={(e) => { e.stopPropagation(); retry(h.id); }}>
                  {t('common.retry')}
                </button>
              {/if}
            </div>
          {/each}
        </div>
      </section>
    </div>
  </div>
</div>
