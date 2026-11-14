<script lang="ts">
  import Icon from '$lib/components/Icon.svelte';
  import Ring from '$lib/components/Ring.svelte';
  import Spark from '$lib/components/Spark.svelte';
  import { app } from '$lib/state.svelte';
  import { t, fmtBytes, fmtDuration, fmtTime } from '$lib/i18n';
  import { accounts, history, jobs, speedSeries, accountName } from '$lib/mock';

  const j = jobs.active;
  const p = jobs.paused;
  const paused = $derived(app.paused);
  const split = $derived(app.splitView);
</script>

<div class="scroll">
  <div class="page activity">
    <div class="page-head">
      <div>
        <h1 class="page-title">{t('activity.title')}</h1>
        <p class="page-sub">{t('activity.sub')}</p>
      </div>
    </div>

    <div class="tabs" role="tablist" {...({ focusgroup: 'tablist nomemory' } as any)} aria-label={t('activity.title')}>
      <button type="button" class="tab" role="tab" id="tab-active" aria-selected="true" {...({ focusgroupstart: '' } as any)}>
        {t('activity.tabActive')}<span class="count">{paused ? '2' : '1'}</span>
      </button>
      <button type="button" class="tab" role="tab" id="tab-history" aria-selected="false"
        onclick={() => app.notify(t('activity.history'))}>
        {t('activity.tabHistory')}
      </button>
    </div>

    <div role="tabpanel" aria-labelledby="tab-active" tabindex="0">
      <article class="job-card">
        <Ring pct={j.bytes / j.total} size={56} />
        <div class="job-main">
          <div class="job-title">
            {t('activity.backupTitle', {
              source: (app.lang === 'en' ? j.sourceEn : j.source) ?? '',
              account: (app.lang === 'en' ? j.accountEn : j.account) ?? ''
            })}
          </div>
          <div class="job-meta">
            <span class="nums">{t('activity.bytes', { done: fmtBytes(j.bytes), total: fmtBytes(j.total) })}</span>
            <span>·</span>
            <span>{t('activity.eta', { time: fmtDuration(j.eta) })}</span>
            {#if split}
              <span class="chip" style="height:22px;padding:0 8px">
                <Icon name="sync" size={14} />{t('activity.splitChip', { n: j.split.total })}
              </span>
            {/if}
          </div>
          {#if split}
            <div class="job-split">
              <span class="truncate">{app.lang === 'en' ? 'Family video 2024.mp4' : '家庭影片_2024.mp4'}</span>
              <span class="bar thin"><i style={`width:${(j.split.done / j.split.total) * 100}%`}></i></span>
              <span class="nums">{t('activity.splitParts', { done: j.split.done, total: j.split.total })}</span>
            </div>
          {/if}
        </div>
        <div style="display:flex;align-items:center;gap:14px">
          <span class="status info">{(j.speed / 1024 / 1024).toFixed(1)} MB/s</span>
          <Spark series={speedSeries} />
          <div class="job-actions">
            <button class="btn tonal sm" onclick={() => app.notify(t('progress.pauseHint'))}>
              <Icon name="pause" size={16} /><span>{t('activity.pause')}</span>
            </button>
            <button class="btn text sm" onclick={() => app.notify(t('activity.cancelled'))}>
              {t('common.cancelAction')}
            </button>
          </div>
        </div>
      </article>

      {#if paused}
        <article class="job-card">
          <Ring pct={p.bytes / p.total} size={56} />
          <div class="job-main">
            <div class="job-title">
              {t('activity.backupTitle', {
                source: (app.lang === 'en' ? p.sourceEn : p.source) ?? '',
                account: (app.lang === 'en' ? p.accountEn : p.account) ?? ''
              })}
            </div>
            <div class="job-meta">
              <span class="status neutral"><Icon name="pause" size={14} />{t('activity.paused')}</span>
              <span class="nums">{t('activity.bytes', { done: fmtBytes(p.bytes), total: fmtBytes(p.total) })}</span>
            </div>
            <div class="job-paused-banner">
              <Icon name="info" size={17} />{t('activity.pausedHint')}
            </div>
          </div>
          <div class="job-actions">
            <button class="btn tonal sm" onclick={() => app.notify(t('activity.resume'))}>
              <Icon name="play-arrow" size={16} /><span>{t('activity.resume')}</span>
            </button>
          </div>
        </article>
      {/if}

      <div class="history-section">
        <div class="section-head">
          <h3 class="section-title">{t('activity.history')}</h3>
          <button class="btn text sm" onclick={() => app.notify(t('common.viewAll'))}>{t('common.viewAll')}</button>
        </div>
        <div>
          {#each history as h (h.id)}
            <div class="history-row">
              <span class="hi" class:ok={h.state === 'done'} class:err={h.state === 'failed'} class:cancel={h.state === 'cancelled'}>
                <Icon name={h.state === 'failed' ? 'error' : h.state === 'cancelled' ? 'remove' : 'check'} size={18} />
              </span>
              <div class="hm">
                <div class="ht">
                  {h.kind === 'restore'
                    ? t('activity.restoreOf', { name: h.name ?? '' })
                    : t('activity.backupTitle', {
                        source: (app.lang === 'en' ? h.sourceEn : h.source) ?? '',
                        account: accountName(accounts[0], app.lang)
                      })}
                </div>
                <div class="hs">
                  {#if h.state === 'failed'}
                    {t('activity.failedTitle', { n: h.failed ?? 0 })} · {fmtTime(h.time)}
                  {:else}
                    {fmtTime(h.time)} · {fmtBytes(h.size)} · {h.kind === 'restore'
                      ? t('activity.completed')
                      : t('files.count', { n: (h.files ?? 0).toLocaleString('en-US') })}
                  {/if}
                </div>
              </div>
              {#if h.state === 'failed'}
                <button class="btn text sm" onclick={() => app.notify(t('activity.retry'))}>{t('activity.retry')}</button>
              {:else}
                <button class="btn text sm" onclick={() => app.notify(t('common.view'))}>{t('common.view')}</button>
              {/if}
            </div>
          {/each}
        </div>
      </div>
    </div>
  </div>
</div>
