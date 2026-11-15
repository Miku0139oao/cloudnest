<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import Icon from '$lib/components/Icon.svelte';
  import Ring from '$lib/components/Ring.svelte';
  import Spark from '$lib/components/Spark.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import { app } from '$lib/state.svelte';
  import { t, fmtBytes, fmtDuration, fmtTime } from '$lib/i18n';
  import { accountName, type HistoryItem } from '$lib/mock';
  import { data, refreshJobs, refreshHistory, resumePending } from '$lib/data.svelte';
  import { backend, isWails } from '$lib/backend';

  let tab = $state<'active' | 'history'>('active');
  let timer: ReturnType<typeof setInterval> | undefined;
  let detail = $state<HistoryItem | null>(null);
  // Rows whose Retry / card action is awaiting the backend. A double-click
  // would reach ResumeJob twice; its duplicate-start guard then reads as a
  // false failure. One entry per id: a second row resolving must not clear the
  // guard (and re-enable the button) of a row still in flight.
  let retrying = $state<string[]>([]);
  let acting = $state<string[]>([]);

  const active = $derived(data.jobs.filter((j) => j.state === 'running' || j.state === 'paused'));
  const accountLabel = $derived(data.accounts[0] ? accountName(data.accounts[0], app.lang) : '');

  function pct(j: { bytes: number; totalBytes: number }): number {
    return j.totalBytes > 0 ? j.bytes / j.totalBytes : 0;
  }

  function titleFor(j: { kind: string; title?: string }): string {
    const name = j.title ?? '';
    if (j.kind === 'upload') return t('activity.jobUpload', { name });
    if (j.kind === 'download') return t('activity.jobDownload', { name });
    if (j.kind === 'restore') return t('activity.restoreOf', { name });
    return t('activity.backupTitle', { source: name, account: accountLabel });
  }

  // The scheduler records synthetic failed entries with no job behind them:
  // "schedule-blocked-<unix>" when a paused backup holds the point, and
  // "schedule-<unix>" when a scheduled start kept failing. Neither can be
  // resumed, so show the explanation and let the user view it instead of
  // retrying something that does not exist.
  function isSyntheticSchedule(h: HistoryItem): boolean {
    return h.id.startsWith('schedule-');
  }

  function isScheduleBlocked(h: HistoryItem): boolean {
    return h.id.startsWith('schedule-blocked-');
  }

  // recordRestartFailure also writes a synthetic record, with no resumable job
  // behind it, so it must get the same treatment as the schedule records.
  function isRestartRecord(h: HistoryItem): boolean {
    return h.id.startsWith('restart-');
  }

  function isSyntheticRecord(h: HistoryItem): boolean {
    return isSyntheticSchedule(h) || isRestartRecord(h);
  }

  async function pause(id: string): Promise<void> {
    if (acting.includes(id)) return;
    acting = [...acting, id];
    try {
      await backend.pauseJob(id);
      await refreshJobs();
    } finally {
      acting = acting.filter((x) => x !== id);
    }
  }

  async function resume(id: string): Promise<void> {
    if (acting.includes(id)) return;
    acting = [...acting, id];
    try {
      const res = await backend.resumeJob(id);
      // ResumeJob refuses with a reason (still winding down, copy cannot be
      // resumed, …); show it instead of silently leaving the card paused.
      if (res?.err) {
        app.notify(res.err.detail || res.err.message || t('activity.resumeFailed'));
      }
      await refreshJobs();
    } finally {
      acting = acting.filter((x) => x !== id);
    }
  }

  async function cancel(id: string): Promise<void> {
    if (acting.includes(id)) return;
    acting = [...acting, id];
    try {
      await backend.cancelJob(id);
      await refreshJobs();
      refreshHistory();
    } finally {
      acting = acting.filter((x) => x !== id);
    }
  }

  // Retry re-runs the stored job spec. rclone only sends what is missing, so a
  // failed backup picks up where it stopped. An interrupted run is retryable
  // too: one whose request survived in the journal is restarted from that
  // stored request, while a job still in memory is resumed by id.
  // A History row whose job is live again (for example a just-retried by-id
  // resume) must not offer Retry: the backend would refuse with "still running"
  // and show it as a false failure.
  function live(id: string): boolean {
    return data.jobs.some((j) => j.id === id && (j.state === 'running' || j.state === 'paused'));
  }

  async function retry(rec: HistoryItem): Promise<void> {
    if (retrying.includes(rec.id)) return;
    retrying = [...retrying, rec.id];
    try {
      if (!isWails) {
        app.go('wizard/1');
        return;
      }
      if (data.pendingResume.some((p) => p.id === rec.id)) {
        if (!(await resumePending(rec.id))) {
          app.notify(t('activity.resumeFailed'));
          return;
        }
        tab = 'active';
        await refreshJobs();
        app.notify(t('activity.retryStarted'));
        return;
      }
      const res = await backend.resumeJob(rec.id);
      if (!res || res.err) {
        app.notify(res?.err?.detail || res?.err?.message || t('activity.retryGone'));
        return;
      }
      tab = 'active';
      await refreshJobs();
      await refreshHistory();
      app.notify(t('activity.retryStarted'));
    } finally {
      retrying = retrying.filter((x) => x !== rec.id);
    }
  }

  // A job finishing leaves data.jobs, so poll History too: its failed row must
  // stop offering Retry once the retried run has settled.
  function jobSignature(): string {
    return data.jobs.map((j) => `${j.id}:${j.state}`).join('|');
  }
  let jobSig = '';
  onMount(() => {
    refreshJobs().then(() => {
      jobSig = jobSignature();
    });
    refreshHistory();
    timer = setInterval(async () => {
      await refreshJobs();
      const sig = jobSignature();
      if (sig !== jobSig) {
        jobSig = sig;
        refreshHistory();
      }
    }, 1200);
  });

  onDestroy(() => {
    if (timer) clearInterval(timer);
  });
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
      <button type="button" class="tab" role="tab" id="tab-active" aria-selected={tab === 'active'}
        {...({ focusgroupstart: '' } as any)} onclick={() => (tab = 'active')}>
        {t('activity.tabActive')}<span class="count">{active.length}</span>
      </button>
      <button type="button" class="tab" role="tab" id="tab-history" aria-selected={tab === 'history'}
        onclick={() => { tab = 'history'; refreshHistory(); }}>
        {t('activity.tabHistory')}<span class="count">{data.history.length}</span>
      </button>
    </div>

    {#if tab === 'active'}
      <div role="tabpanel" aria-labelledby="tab-active" tabindex="0">
        {#if active.length === 0}
          <div class="empty activity-empty">
            <span class="art"><Icon name="check-circle" size={44} /></span>
            <div class="e-title">{t('activity.emptyTitle')}</div>
            <div class="e-body">{t('activity.emptyBody')}</div>
            <button class="btn filled" onclick={() => app.go('wizard/1')}>
              <Icon name="cloud-upload" size={18} /><span>{t('activity.startBackup')}</span>
            </button>
          </div>
        {:else}
          {#each active as j (j.id)}
            <article class="job-card">
              <Ring pct={pct(j)} size={56} />
              <div class="job-main">
                <div class="job-title">{titleFor(j)}</div>
                <div class="job-meta">
                  <span class="nums">{t('activity.bytes', { done: fmtBytes(j.bytes), total: fmtBytes(j.totalBytes) })}</span>
                  {#if j.state === 'running'}
                    <span>·</span>
                    <span>{j.eta ? t('activity.eta', { time: fmtDuration(j.eta) }) : t('activity.etaSoon')}</span>
                    {#if j.speed > 0}
                      <span class="status info">{(j.speed / 1024 / 1024).toFixed(1)} MB/s</span>
                    {/if}
                  {:else}
                    <span class="status neutral"><Icon name="pause" size={14} />{t('activity.paused')}</span>
                  {/if}
                </div>
                {#if j.state === 'paused'}
                  <div class="job-paused-banner">
                    <Icon name="info" size={17} />{t('activity.pausedHint')}
                  </div>
                {:else if j.current}
                  <div class="job-split">
                    <span class="truncate">{j.current}</span>
                  </div>
                {/if}
              </div>
              <div style="display:flex;align-items:center;gap:14px">
                {#if j.state === 'running' && j.speed > 0}
                  <Spark series={data.jobs.length ? [0.5, 0.55, 0.45, 0.62, 0.58, 0.7, 0.66, 0.72, 0.68, 0.74] : []} />
                {/if}
                <div class="job-actions">
                  {#if j.state === 'running'}
                    <button class="btn tonal sm" onclick={() => pause(j.id)} disabled={acting.includes(j.id)}>
                      <Icon name="pause" size={16} /><span>{t('activity.pause')}</span>
                    </button>
                  {:else}
                    <button class="btn tonal sm" onclick={() => resume(j.id)} disabled={acting.includes(j.id)}>
                      <Icon name="play-arrow" size={16} /><span>{t('activity.resume')}</span>
                    </button>
                  {/if}
                  <button class="btn text sm" onclick={() => cancel(j.id)} disabled={acting.includes(j.id)}>{t('common.cancelAction')}</button>
                </div>
              </div>
            </article>
          {/each}
        {/if}
      </div>
    {:else}
      <div role="tabpanel" aria-labelledby="tab-history" tabindex="0">
        {#if data.history.length === 0}
          <div class="empty activity-empty">
            <span class="art"><Icon name="history" size={44} /></span>
            <div class="e-title">{t('activity.noHistory')}</div>
            <div class="e-body">{t('activity.emptyBody')}</div>
          </div>
        {:else}
          {#each data.history as h (h.id)}
            <div class="history-row">
              <span class="hi" class:ok={h.state === 'done'} class:err={h.state === 'failed'}
                class:cancel={h.state === 'cancelled'} class:warn={h.state === 'interrupted'}>
                <Icon name={h.state === 'interrupted'
                  ? 'warning'
                  : h.state === 'failed'
                    ? 'error'
                    : h.state === 'cancelled'
                      ? 'remove'
                      : 'check'} size={18} />
              </span>
              <div class="hm">
                <div class="ht">
                  {#if isScheduleBlocked(h)}
                    {t('activity.scheduleBlockedTitle')}
                  {:else if isSyntheticSchedule(h)}
                    {t('activity.scheduleFailedTitle')}
                  {:else if isRestartRecord(h)}
                    {t('activity.restartFailedTitle')}
                  {:else if h.kind === 'restore'}
                    {t('activity.restoreOf', { name: h.name ?? h.source ?? '' })}
                  {:else}
                    {t('activity.backupTitle', { source: h.source ?? '', account: accountLabel })}
                  {/if}
                </div>
                {#if isSyntheticRecord(h)}
                  <div class="hs">
                    {isScheduleBlocked(h)
                      ? t('activity.scheduleBlockedHint')
                      : isRestartRecord(h)
                        ? t('activity.restartFailedHint')
                        : t('activity.scheduleFailedHint')}
                  </div>
                  {#if h.error}
                    <div class="hs history-error">{h.error}</div>
                  {/if}
                {:else}
                  <div class="hs">
                    {#if h.state === 'interrupted'}
                      {t('activity.interrupted')} · {fmtTime(h.time)}
                    {:else if h.state === 'failed'}
                      {h.failed ? t('activity.failedTitle', { n: h.failed }) : t('activity.failedGeneric')} · {fmtTime(h.time)}
                    {:else if h.state === 'cancelled'}
                      {t('activity.cancelled')} · {fmtTime(h.time)}
                    {:else}
                      {fmtTime(h.time)} · {fmtBytes(h.size)} · {t('files.count', { n: (h.files ?? 0).toLocaleString('en-US') })}
                    {/if}
                  </div>
                  {#if h.state === 'interrupted' && h.error}
                    <div class="hs history-error">{h.error}</div>
                  {/if}
                {/if}
              </div>
              {#if (h.state === 'failed' || h.state === 'interrupted') && !isSyntheticRecord(h) && !live(h.id)}
                <button class="btn text sm" onclick={() => retry(h)} disabled={retrying.includes(h.id)}>{t('activity.retry')}</button>
              {:else}
                <button class="btn text sm" onclick={() => (detail = h)}>{t('common.view')}</button>
              {/if}
            </div>
          {/each}
        {/if}
      </div>
    {/if}
  </div>
</div>

{#if detail}
  <Modal labelledby="hist-title" onclose={() => (detail = null)}>
    <div class="dialog" style="width: 520px">
      <div class="dialog-head" style="padding-bottom: 2px">
        <div class="grow">
          <div class="dialog-title" id="hist-title">
            {isScheduleBlocked(detail)
              ? t('activity.scheduleBlockedTitle')
              : isSyntheticSchedule(detail)
                ? t('activity.scheduleFailedTitle')
                : isRestartRecord(detail)
                  ? t('activity.restartFailedTitle')
                  : (detail.name || detail.source)}
          </div>
        </div>
        <button class="icon-button" aria-label={t('common.close')} onclick={() => (detail = null)}>
          <Icon name="close" size={20} />
        </button>
      </div>
      <div class="dialog-body">
        <div class="detail-list">
          <div><span>{t('activity.detail.kind')}</span><b>{isRestartRecord(detail) ? t('activity.detail.settings') : detail.kind === 'restore' ? t('activity.detail.restore') : t('activity.detail.backup')}</b></div>
          <div><span>{t('activity.detail.state')}</span><b>{t(`activity.detail.${detail.state}`)}</b></div>
          <div><span>{t('activity.detail.time')}</span><b>{fmtTime(detail.time)}</b></div>
          <div><span>{t('activity.detail.size')}</span><b>{fmtBytes(detail.size)}</b></div>
          <div><span>{t('activity.detail.files')}</span><b>{(detail.files ?? 0).toLocaleString('en-US')}</b></div>
          {#if detail.failed}
            <div><span>{t('activity.detail.failedCount')}</span><b>{detail.failed.toLocaleString('en-US')}</b></div>
          {/if}
        </div>
        {#if detail.error}
          <div class="tech">
            <div class="tech-title">{t('activity.detail.tech')}</div>
            <pre>{detail.error}</pre>
          </div>
        {/if}
      </div>
      <div class="dialog-foot">
        <div class="grow"></div>
        <button class="btn filled" onclick={() => (detail = null)}>{t('common.close')}</button>
      </div>
    </div>
  </Modal>
{/if}
