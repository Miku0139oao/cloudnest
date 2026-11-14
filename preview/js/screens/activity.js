/* =========================================================================
   Activity
   ========================================================================= */
(function () {
  CB.screens = CB.screens || {};

  CB.screens.activity = function (ctx) {
    var d = CB.data;
    var split = ctx.flags.has('split');
    var paused = ctx.flags.has('paused');
    var empty = ctx.flags.has('empty');
    var j = d.jobs.active;
    var p = d.jobs.paused;

    var pct = j.bytes / j.total;

    var activeCard = '<article class="job-card">' +
      CB.ring(pct, 56) +
      '<div class="job-main">' +
      '<div class="job-title">' + CB.esc(CB.t('activity.backupTitle', {
        source: CB.lang === 'en' ? j.sourceEn : j.source,
        account: CB.lang === 'en' ? j.accountEn : j.account
      })) + '</div>' +
      '<div class="job-meta">' +
      '<span class="nums">' + CB.esc(CB.t('activity.bytes', {
        done: CB.fmtBytes(j.bytes), total: CB.fmtBytes(j.total)
      })) + '</span><span>·</span>' +
      '<span>' + CB.esc(CB.t('activity.eta', { time: CB.fmtDuration(j.eta) })) + '</span>' +
      (split
        ? '<span class="chip" style="height:22px;padding:0 8px">' + CB.icon('sync', 14) +
          CB.esc(CB.t('activity.splitChip', { n: j.split.total })) + '</span>'
        : '') +
      '</div>' +
      (split
        ? '<div class="job-split"><span class="truncate">' +
          CB.esc(CB.lang === 'en' ? 'Family video 2024.mp4' : '家庭影片_2024.mp4') + '</span>' +
          CB.bar(j.split.done / j.split.total, 'thin') +
          '<span class="nums">' + CB.esc(CB.t('activity.splitParts', { done: j.split.done, total: j.split.total })) + '</span></div>'
        : '') +
      '</div>' +
      '<div style="display:flex;align-items:center;gap:14px">' +
      '<span class="status info">' + Math.round(j.speed / 1024 / 1024 * 10) / 10 + ' MB/s</span>' +
      CB.spark(d.speedSeries, 132, 34) +
      '<div class="job-actions">' +
      CB.btn({ label: CB.t('activity.pause'), variant: 'tonal', icon: 'pause', size: 'sm', data: { action: 'noop' } }) +
      CB.btn({ label: CB.t('common.cancelAction'), variant: 'text', size: 'sm', data: { action: 'noop' } }) +
      '</div></div>' +
      '</article>';

    var pausedCard = '<article class="job-card">' +
      CB.ring(p.bytes / p.total, 56) +
      '<div class="job-main">' +
      '<div class="job-title">' + CB.esc(CB.t('activity.backupTitle', {
        source: CB.lang === 'en' ? p.sourceEn : p.source,
        account: CB.lang === 'en' ? p.accountEn : p.account
      })) + '</div>' +
      '<div class="job-meta"><span class="status neutral">' + CB.icon('pause', 14) + CB.esc(CB.t('activity.paused')) + '</span>' +
      '<span class="nums">' + CB.esc(CB.t('activity.bytes', {
        done: CB.fmtBytes(p.bytes), total: CB.fmtBytes(p.total)
      })) + '</span></div>' +
      '<div class="job-paused-banner">' + CB.icon('info', 17) + CB.esc(CB.t('activity.pausedHint')) + '</div>' +
      '</div>' +
      '<div class="job-actions">' +
      CB.btn({ label: CB.t('activity.resume'), variant: 'tonal', icon: 'play-arrow', size: 'sm', data: { action: 'noop' } }) +
      '</div></article>';

    var history = d.history.map(function (h) {
      var failed = h.state === 'failed';
      var cls = failed ? 'err' : (h.state === 'cancelled' ? 'cancel' : 'ok');
      var icon = failed ? 'error' : (h.state === 'cancelled' ? 'remove' : 'check');
      var title = h.kind === 'restore'
        ? CB.t('activity.restoreOf', { name: h.name })
        : CB.t('activity.backupTitle', {
            source: CB.lang === 'en' ? h.sourceEn : h.source,
            account: CB.lang === 'en' ? d.accounts[0].nameEn : d.accounts[0].name
          });
      var sub;
      if (failed) {
        sub = CB.t('activity.failedTitle', { n: h.failed }) + ' · ' + CB.fmtTime(h.time);
      } else {
        sub = CB.fmtTime(h.time) + ' · ' + CB.fmtBytes(h.size) + ' · ' +
          (h.kind === 'restore'
            ? CB.t('activity.completed')
            : CB.t('files.count', { n: h.files.toLocaleString('en-US') }));
      }
      return '<div class="history-row">' +
        '<span class="hi ' + cls + '">' + CB.icon(icon, 18) + '</span>' +
        '<div class="hm"><div class="ht">' + CB.esc(title) + '</div>' +
        '<div class="hs">' + CB.esc(sub) + '</div></div>' +
        (failed
          ? '<button class="btn text sm" data-action="retry">' + CB.esc(CB.t('activity.retry')) + '</button>'
          : '<button class="btn text sm" data-action="noop">' + CB.esc(CB.t('common.view')) + '</button>') +
        '</div>';
    }).join('');

    var body = empty
      ? '<div class="empty activity-empty"><span class="art">' + CB.icon('check-circle', 44) + '</span>' +
        '<div class="e-title">' + CB.esc(CB.t('activity.emptyTitle')) + '</div>' +
        '<div class="e-body">' + CB.esc(CB.t('activity.emptyBody')) + '</div>' +
        CB.btn({ label: CB.t('activity.startBackup'), variant: 'filled', icon: 'cloud-upload', data: { action: 'nav', view: 'wizard/1' } }) +
        '</div>'
      : activeCard + (paused ? pausedCard : '') +
        '<div class="history-section"><div class="section-head"><h3 class="section-title">' +
        CB.esc(CB.t('activity.history')) + '</h3>' +
        '<button class="btn text sm" data-action="noop">' + CB.esc(CB.t('common.viewAll')) + '</button></div>' +
        '<div>' + history + '</div></div>';

    return '<div class="scroll"><div class="page activity">' +
      '<div class="page-head"><div><h1 class="page-title">' + CB.esc(CB.t('activity.title')) + '</h1>' +
      '<p class="page-sub">' + CB.esc(CB.t('activity.sub')) + '</p></div></div>' +
      '<div class="tabs" role="tablist" focusgroup="tablist nomemory" aria-label="' + CB.esc(CB.t('activity.title')) + '">' +
      '<button type="button" class="tab" role="tab" id="tab-active" aria-selected="true" focusgroupstart>' + CB.esc(CB.t('activity.tabActive')) +
      '<span class="count">' + (paused ? '2' : '1') + '</span></button>' +
      '<button type="button" class="tab" role="tab" id="tab-history" aria-selected="false" data-action="noop">' + CB.esc(CB.t('activity.tabHistory')) + '</button>' +
      '</div>' +
      '<div role="tabpanel" aria-labelledby="tab-active" tabindex="0">' + body + '</div>' +
      '</div></div>';
  };
})();
