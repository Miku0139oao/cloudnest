/* =========================================================================
   Files (dual pane) + restore dialog
   ========================================================================= */
(function () {
  CB.screens = CB.screens || {};
  CB.overlays = CB.overlays || {};

  function splitChip(entry) {
    if (!entry.split) return '';
    var map = {
      ready: ['ready', 'verified', 'files.split.ready'],
      broken: ['broken', 'error', 'files.split.broken'],
      incomplete: ['incomplete', 'schedule', 'files.split.incomplete'],
      foreign: ['incomplete', 'info', 'files.split.foreign']
    };
    var m = map[entry.split] || map.ready;
    return '<span class="chip split-chip ' + m[0] + '" title="' + CB.esc(CB.t(m[2])) + '">' +
      CB.icon(m[1], 14) + CB.esc(CB.t(m[2])) + '</span>';
  }

  function rows(entries, pane, selected) {
    return entries.map(function (e) {
      var sel = selected[e.id];
      var kind = e.split ? 'split' : (e.dir ? 'folder' : 'file');
      return '<tr class="' + (sel ? 'selected' : '') + '" data-action="select-row" data-id="' + e.id + '" data-pane="' + pane + '" data-kind="' + kind + '">' +
        '<td class="c-check"><span class="cb"><input type="checkbox"' + (sel ? ' checked' : '') +
        ' aria-label="' + CB.esc(e.name) + '"></span></td>' +
        '<td><div class="file-name-cell">' + CB.fileIcon(e) +
        '<span class="cname">' + CB.esc(e.name) + '</span>' + splitChip(e) + '</div></td>' +
        '<td class="c-size nums">' + (e.dir ? '—' : CB.fmtBytes(e.size)) + '</td>' +
        '<td class="c-time">' + CB.esc(CB.fmtTime(e.mod)) + '</td>' +
        '</tr>';
    }).join('');
  }

  function paneHeadLocal() {
    return '<div class="pane-head">' +
      '<span class="pane-title" style="display:flex;align-items:center;gap:7px">' +
      CB.icon('computer', 18) + CB.esc(CB.t('files.computer')) + '</span>' +
      '<span style="color:var(--m3c-outline)">/</span>' +
      '<span class="crumb" data-action="noop">' + CB.icon('folder-open', 15) +
      CB.esc(CB.lang === 'en' ? 'Downloads' : '下載') + '</span>' +
      '<span class="grow"></span>' +
      '<button class="icon-button" aria-label="更多" data-action="noop" style="width:32px;height:32px">' + CB.icon('more-vert', 18) + '</button>' +
      '</div>';
  }

  function paneToolbarLocal() {
    var quick = [['desktop', 'files.root.desktop'], ['documents', 'files.root.documents'],
      ['pictures', 'files.root.pictures'], ['downloads', 'files.root.downloads'],
      ['music', 'files.root.music']].map(function (r) {
      return CB.chip({
        label: CB.t(r[1]),
        selected: r[0] === 'downloads',
        interactive: true,
        data: { action: 'nav', view: 'files' }
      });
    }).join('');

    return '<div class="pane-toolbar">' +
      CB.btn({ label: CB.t('files.upload'), variant: 'tonal', icon: 'cloud-upload', disabled: true, size: 'sm' }) +
      quick +
      '<span class="spacer"></span>' +
      '<button class="icon-button" aria-label="' + CB.esc(CB.t('files.newFolder')) + '" data-action="noop">' + CB.icon('create-new-folder', 20) + '</button>' +
      '</div>';
  }

  function paneHeadCloud() {
    var a = CB.data.accounts[0];
    return '<div class="pane-head">' +
      '<button class="crumb" data-action="noop" style="height:28px;background:var(--m3c-surface-container-high)">' +
      CB.providerTile(a.provider, CB.accountName(a), 16) +
      '<span>' + CB.esc(CB.accountName(a)) + '</span>' + CB.icon('expand-more', 16) + '</button>' +
      '<span class="crumb-sep">' + CB.icon('chevron-right', 16) + '</span>' +
      '<span class="crumb" data-action="noop">' + CB.icon('folder', 15) + CB.esc(CB.t('files.folder.backup')) + '</span>' +
      '<span class="crumb-sep">' + CB.icon('chevron-right', 15) + '</span>' +
      '<span class="crumb" data-action="noop">' + CB.icon('folder-open', 15) + CB.esc(CB.lang === 'en' ? 'Downloads' : '下載') + '</span>' +
      '<span class="grow"></span>' +
      '<button class="icon-button" aria-label="更多" data-action="noop" style="width:32px;height:32px">' + CB.icon('more-vert', 18) + '</button>' +
      '</div>';
  }

  function paneToolbarCloud(hasSelection, hasSplit) {
    return '<div class="pane-toolbar">' +
      CB.btn({
        label: hasSplit ? CB.t('files.restore') : CB.t('files.download'),
        variant: 'filled',
        icon: hasSplit ? 'history' : 'cloud-download',
        size: 'sm',
        disabled: !hasSelection,
        data: { action: hasSplit ? 'open-restore' : 'noop' }
      }) +
      '<span class="spacer"></span>' +
      '<button class="icon-button" aria-label="' + CB.esc(CB.t('files.newFolder')) + '" data-action="noop">' + CB.icon('create-new-folder', 20) + '</button>' +
      '<button class="icon-button" aria-label="' + CB.esc(CB.t('files.delete')) + '" data-action="noop">' + CB.icon('delete', 20) + '</button>' +
      '</div>';
  }

  CB.screens.files = function (ctx) {
    var d = CB.data;
    var selected = {};
    CB.state.selCloud.forEach(function (id) { selected[id] = true; });
    var selectedEntries = d.cloudFiles.filter(function (e) { return selected[e.id]; });
    var hasSplit = selectedEntries.some(function (e) { return !!e.split; });
    var selBytes = selectedEntries.reduce(function (s, e) { return s + (e.size || 0); }, 0);
    var selLabel = selectedEntries.length
      ? CB.t('files.selected', {
          n: selectedEntries.length,
          size: CB.fmtBytes(selBytes)
        })
      : CB.t('files.items', { n: '1,284' });

    var drag = ctx.flags.has('dragover');

    return '<div class="files">' +
      '<div class="files-head">' +
      '<span class="files-title">' + CB.esc(CB.t('nav.files')) + '</span>' +
      '<span class="hint">' + CB.esc(CB.t('files.dragTip')) + '</span>' +
      '<span class="grow"></span>' +
      '<div class="search">' + CB.icon('search', 18) +
      '<input type="text" placeholder="' + CB.esc(CB.t('files.search')) + '" aria-label="' + CB.esc(CB.t('files.search')) + '"></div>' +
      '<button class="icon-button" aria-label="' + CB.esc(CB.t('common.view')) + '" data-action="noop">' + CB.icon('table-chart', 20) + '</button>' +
      '</div>' +

      '<div class="files-main">' +
      '<section class="pane pane-drop" id="drop-local">' +
      paneHeadLocal() + paneToolbarLocal() +
      '<div class="pane-table" tabindex="0" role="region" aria-label="' + CB.esc(CB.t('files.computer') + ' · ' + (CB.lang === 'en' ? 'Downloads' : '下載')) + '">' +
      '<table class="table"><colgroup><col style="width:40px"><col><col style="width:92px"><col style="width:148px"></colgroup>' +
      '<thead><tr><th class="c-check" scope="col"><span class="sr-only">' + CB.esc(CB.t('files.select')) + '</span></th><th scope="col">' + CB.esc(CB.t('files.name')) + '</th>' +
      '<th class="c-size" scope="col">' + CB.esc(CB.t('files.size')) + '</th><th class="c-time" scope="col">' + CB.esc(CB.t('files.modified')) + '</th></tr></thead>' +
      '<tbody>' + rows(d.localFiles, 'local', {}) + '</tbody></table>' +
      '</div></section>' +

      '<div class="splitter" role="separator" aria-label="resize"></div>' +

      '<section class="pane pane-drop" id="drop-cloud">' +
      paneHeadCloud() + paneToolbarCloud(selectedEntries.length > 0, hasSplit) +
      '<div class="pane-table" tabindex="0" role="region" aria-label="' + CB.esc(CB.accountName(CB.data.accounts[0]) + ' · ' + CB.t('files.folder.backup')) + '">' +
      '<table class="table"><colgroup><col style="width:40px"><col><col style="width:92px"><col style="width:148px"></colgroup>' +
      '<thead><tr><th class="c-check" scope="col"><span class="sr-only">' + CB.esc(CB.t('files.select')) + '</span></th><th scope="col">' + CB.esc(CB.t('files.name')) + '</th>' +
      '<th class="c-size" scope="col">' + CB.esc(CB.t('files.size')) + '</th><th class="c-time" scope="col">' + CB.esc(CB.t('files.modified')) + '</th></tr></thead>' +
      '<tbody>' + rows(d.cloudFiles, 'cloud', selected) + '</tbody></table>' +
      '</div>' +
      (drag ? '<div class="drop-overlay">' + CB.icon('cloud-upload', 48) +
        '<div class="dt">' + CB.esc(CB.t('files.dragHint', { where: CB.t('files.folder.backup') + ' / ' + (CB.lang === 'en' ? 'Downloads' : '下載') })) + '</div>' +
        '<div class="ds">' + CB.esc(CB.t('files.uploadStarted', { n: 3 })) + '</div></div>' : '') +
      '</section>' +
      '</div>' +

      '<div class="statusbar"><span class="sb-left">' + CB.icon('info', 15) +
      '<span>' + CB.esc(selLabel) + '</span></span>' +
      '<span class="nums">' + CB.esc(CB.t('files.items', { n: '1,284' })) + ' · ' + CB.esc(CB.fmtBytes(12.4 * 1024 * 1024 * 1024)) + '</span></div>' +
      '</div>';
  };

  CB.overlays.restore = function (ctx) {
    var r = CB.data.restore;
    var conflict = ctx.flags.has('conflict');
    var done = !conflict;

    return '<dialog class="dlg" closedby="any" aria-labelledby="restore-title">' +
      '<div class="dialog restore-dialog">' +
      '<div class="dialog-head" style="padding-bottom:2px">' +
      '<div class="grow"><div class="dialog-title" id="restore-title">' + CB.esc(CB.t('restore.title', { name: r.name })) + '</div></div>' +
      '<button class="icon-button" aria-label="' + CB.esc(CB.t('common.close')) + '" data-action="close-overlay">' + CB.icon('close', 20) + '</button>' +
      '</div>' +
      '<div class="dialog-body">' +
      '<div class="restore-file">' +
      '<span class="rtile">' + CB.icon('movie', 28) + '</span>' +
      '<div class="grow"><div class="rn">' + CB.esc(r.name) + '</div>' +
      '<div class="rs">' + CB.esc(CB.t('restore.sub', { size: CB.fmtBytes(r.size), parts: r.parts })) + '</div>' +
      '<div class="rs">' + CB.esc(r.remotePath + ' · ' + CB.t('clouds.connected')) + '</div></div>' +
      CB.status('ok', CB.t('files.split.ready'), false) +
      '</div>' +

      '<div class="restore-where">' + CB.esc(CB.t('restore.where')) + '</div>' +
      '<div class="restore-dest">' +
      CB.chip({ label: CB.t('restore.destName'), icon: 'folder', selected: true, interactive: true, data: { action: 'noop' } }) +
      CB.chip({ label: CB.t('files.root.pictures'), icon: 'image', interactive: true, data: { action: 'noop' } }) +
      CB.chip({ label: CB.t('files.root.desktop'), icon: 'computer', interactive: true, data: { action: 'noop' } }) +
      CB.chip({ label: CB.t('restore.other'), icon: 'folder-open', interactive: true, data: { action: 'noop' } }) +
      '</div>' +
      '<div class="rs" style="margin-top:6px;color:var(--m3c-on-surface-variant)">' +
      CB.esc(CB.t('restore.free', { free: CB.fmtBytes(r.free) })) + '</div>' +

      (done
        ? '<div class="restore-progress">' + CB.bar(1) + '</div>' +
          '<div class="restore-done">' + CB.icon('check-circle', 22) +
          '<div><div class="dt">' + CB.esc(CB.t('restore.done')) + '</div>' +
          '<div class="db">' + CB.esc(CB.t('restore.doneBody', { name: r.name, dest: CB.t('restore.destName') })) + '</div></div></div>'
        : '<div class="restore-progress" style="padding-top:18px">' +
          '<div class="hint" style="margin-bottom:6px">' + CB.esc(CB.t('restore.conflict', { name: r.name })) + '</div>' +
          '<div class="mode selected" style="margin-bottom:8px"><span class="radio"></span>' +
          '<div><div class="mt">' + CB.esc(CB.t('restore.keepBoth', { new: '家庭影片_2024 (1).mp4' })) + '</div></div></div>' +
          '<div class="mode"><span class="radio"></span><div><div class="mt">' + CB.esc(CB.t('restore.replace')) + '</div></div></div>' +
          '</div>') +
      '</div>' +
      '<div class="dialog-foot">' +
      (done
        ? CB.btn({ label: CB.t('common.view') + ' ' + CB.t('restore.destName'), variant: 'text', icon: 'folder-open', data: { action: 'noop' } }) +
          '<div class="grow"></div>' +
          CB.btn({ label: CB.t('common.done'), variant: 'filled', autofocus: true, data: { action: 'close-overlay' } })
        : CB.btn({ label: CB.t('common.cancel'), variant: 'text', autofocus: true, data: { action: 'close-overlay' } }) +
          '<div class="grow"></div>' +
          CB.btn({ label: CB.t('restore.start', { size: CB.fmtBytes(r.size) }), variant: 'filled', data: { action: 'noop' } })) +
      '</div>' +
      '</div></dialog>';
  };
})();
