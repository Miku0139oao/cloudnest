/* =========================================================================
   Backup wizard (overlay dialog)
   ========================================================================= */
(function () {
  CB.overlays = CB.overlays || {};

  var QUICK = [
    { id: 'desktop', key: 'files.root.desktop', icon: 'computer' },
    { id: 'documents', key: 'files.root.documents', icon: 'description' },
    { id: 'pictures', key: 'files.root.pictures', icon: 'image' },
    { id: 'downloads', key: 'files.root.downloads', icon: 'download' },
    { id: 'music', key: 'files.root.music', icon: 'music-note' }
  ];

  function steps(active) {
    var labels = ['wizard.step1', 'wizard.step2', 'wizard.step3'];
    var out = '';
    for (var i = 0; i < 3; i++) {
      var n = i + 1;
      var cls = n < active ? 'done' : (n === active ? 'active' : '');
      out += '<span class="step-dot ' + cls + '"><span class="n">' +
        (n < active ? CB.icon('check', 13) : n) + '</span>' + CB.esc(CB.t(labels[i])) + '</span>';
      if (i < 2) out += '<span class="step-line"></span>';
    }
    return '<div class="wizard-steps">' + out + '</div>';
  }

  function step1(ctx) {
    var src = CB.data.backupSource;
    var name = CB.lang === 'en' ? src.nameEn : src.name;
    var scanning = ctx.flags.has('scanning');

    var chosen = CB.state.sourceChoice || 'folder';
    return '<div class="wizard-body">' +
      '<div><h2 class="step-title">' + CB.esc(CB.t('wizard.s1.title')) + '</h2>' +
      '<p class="step-sub">' + CB.esc(CB.t('wizard.s1.sub')) + '</p></div>' +

      '<div class="option-pair">' +
      '<label class="option big' + (chosen === 'folder' ? ' selected' : '') + '" data-action="wizard-choice" data-choice="folder">' +
      '<input type="radio" name="wiz-source" value="folder"' + (chosen === 'folder' ? ' checked' : '') + '>' + CB.icon('folder', 26) +
      '<span class="t">' + CB.esc(CB.t('wizard.s1.folder')) + '</span>' +
      '<span class="d">' + CB.esc(CB.t('wizard.s1.folderSub')) + '</span></label>' +
      '<label class="option big' + (chosen === 'files' ? ' selected' : '') + '" data-action="wizard-choice" data-choice="files">' +
      '<input type="radio" name="wiz-source" value="files"' + (chosen === 'files' ? ' checked' : '') + '>' + CB.icon('description', 26) +
      '<span class="t">' + CB.esc(CB.t('wizard.s1.files')) + '</span>' +
      '<span class="d">' + CB.esc(CB.t('wizard.s1.filesSub')) + '</span></label>' +
      '</div>' +

      '<div class="quick-locations"><span class="lbl">' + CB.esc(CB.t('wizard.s1.quick')) + '</span>' +
      QUICK.map(function (q) {
        return CB.chip({
          label: CB.t(q.key),
          icon: q.icon,
          selected: q.id === 'downloads',
          interactive: true,
          data: { action: 'quick-location', location: q.id }
        });
      }).join('') + '</div>' +

      '<div class="source-summary">' +
      CB.icon('folder', 20) +
      '<div class="sc"><div class="sn">' + CB.esc(name) + '</div>' +
      '<div class="ss">' + (scanning
        ? CB.esc(CB.t('wizard.s1.scanning'))
        : CB.esc(CB.t('wizard.s1.count', {
            files: src.files.toLocaleString('en-US'),
            size: CB.fmtBytes(src.bytes)
          }))) + '</div></div>' +
      '<button class="btn text sm" data-action="nav" data-view="wizard/1">' + CB.esc(CB.t('common.change')) + '</button>' +
      '</div>' +
      '</div>';
  }

  function step2(ctx) {
    var d = CB.data;
    var accounts = d.accounts.map(function (a, i) {
      var free = a.total - a.used;
      var sel = a.id === (CB.state.account || d.accounts[0].id);
      return '<label class="account-row' + (sel ? ' selected' : '') + '" data-action="choose-account" data-account="' + a.id + '">' +
        '<input type="radio" name="wiz-account" value="' + a.id + '"' + (sel ? ' checked' : '') + '>' +
        CB.providerTile(a.provider, CB.accountName(a), 22) +
        '<div class="am"><div class="an">' + CB.esc(CB.accountName(a)) + '</div>' +
        '<div class="as">' + CB.esc(CB.t('clouds.used', { used: CB.fmtBytes(a.used), total: CB.fmtBytes(a.total) })) + '</div></div>' +
        '<div class="as nums" style="color:var(--m3c-on-surface-variant)">' + CB.esc(CB.fmtBytes(free)) + ' free</div>' +
        '</label>';
    }).join('');

    return '<div class="wizard-body">' +
      '<div><h2 class="step-title">' + CB.esc(CB.t('wizard.s2.title')) + '</h2>' +
      '<p class="step-sub">' + CB.esc(CB.t('wizard.s2.sub')) + '</p></div>' +
      '<div class="account-list">' + accounts + '</div>' +
      '<div class="dest-block"><span class="hint">' + CB.esc(CB.t('wizard.s2.dest')) + '</span>' +
      '<div class="breadcrumb">' +
      '<span class="crumb">' + CB.icon('folder', 15) + CB.esc(CB.t('files.folder.backup')) + '</span>' +
      '<span class="crumb-sep">' + CB.icon('chevron-right', 16) + '</span>' +
      '<span class="crumb">' + CB.icon('folder-open', 15) + CB.esc(CB.lang === 'en' ? CB.data.backupSource.nameEn : CB.data.backupSource.name) + '</span>' +
      '</div>' +
      '<button class="btn text sm" data-action="nav" data-view="wizard/2">' + CB.esc(CB.t('common.change')) + '</button></div>' +
      '<div class="banner info" style="align-items:center">' + CB.icon('history', 20) +
      '<div>' + CB.esc(CB.t('wizard.s2.hint', { files: (1240).toLocaleString('en-US') })) + '</div></div>' +
      '<button class="btn text sm" style="align-self:flex-start" data-action="nav" data-view="clouds/add">' +
      CB.icon('add', 16) + CB.esc(CB.t('wizard.s2.connect')) + '</button>' +
      '</div>';
  }

  function step3(ctx) {
    var d = CB.data;
    var src = d.backupSource;
    var source = CB.lang === 'en' ? src.nameEn : src.name;
    var mirror = ctx.flags.has('mirror');
    var advanced = ctx.flags.has('advanced') || CB.state.advancedOpen;
    var checked = !!CB.state.mirrorChecked;
    var fast = CB.state.fastFiles !== false;

    var summary = CB.t('wizard.s3.summary', {
      source: source,
      files: src.files.toLocaleString('en-US'),
      size: CB.fmtBytes(src.bytes),
      account: CB.accountName(d.accounts[0]),
      dest: CB.t('files.folder.backup') + ' / ' + source
    });

    return '<div class="wizard-body">' +
      '<div class="summary-box">' + CB.esc(summary) + '</div>' +

      '<div class="mode-list">' +
      '<label class="mode' + (!mirror ? ' selected' : '') + '" data-action="wizard-mode" data-mode="add">' +
      '<input type="radio" name="wiz-mode" value="add"' + (!mirror ? ' checked' : '') + '>' +
      '<div><div class="mt">' + CB.esc(CB.t('wizard.s3.modeAdd')) + '</div>' +
      '<div class="md">' + CB.esc(CB.t('wizard.s3.modeAddSub')) + '</div></div></label>' +
      '<label class="mode' + (mirror ? ' selected' : '') + '" data-action="wizard-mode" data-mode="mirror">' +
      '<input type="radio" name="wiz-mode" value="mirror"' + (mirror ? ' checked' : '') + '>' +
      '<div><div class="mt">' + CB.esc(CB.t('wizard.s3.modeMirror')) + '</div>' +
      '<div class="md">' + CB.esc(CB.t('wizard.s3.modeMirrorSub')) + '</div></div></label>' +
      '</div>' +

      (mirror ? '<div class="mirror-warn">' + CB.icon('warning', 20) +
        '<div class="grow"><div class="wt">' + CB.esc(CB.t('wizard.s3.warnTitle', {
          files: '37', size: CB.fmtBytes(412 * 1024 * 1024)
        })) + '</div>' +
        '<button class="btn text sm" style="padding-left:0;color:inherit" data-action="show-deletions">' +
        CB.esc(CB.t('wizard.s3.warnView')) + '</button>' +
        '<label class="checkline">' +
        '<input type="checkbox" data-action="toggle-mirror-confirm"' + (checked ? ' checked' : '') + '>' +
        '<span>' + CB.esc(CB.t('wizard.s3.confirm')) + '</span></label></div></div>' : '') +

      '<button class="advanced-toggle' + (advanced ? ' open' : '') + '" data-action="toggle-advanced">' +
      CB.icon('tune', 18) + CB.esc(CB.t('wizard.s3.advanced')) + CB.icon('expand-more', 18) + '</button>' +

      (advanced ? '<div class="advanced-panel">' +
        '<div class="adv-row"><div class="al"><div class="at">' + CB.esc(CB.t('wizard.s3.fast')) + '</div>' +
        '<div class="ad">' + CB.esc(CB.t('wizard.s3.fastSub')) + '</div>' +
        '<div class="ad fast-count">' + CB.esc(CB.t('wizard.s3.fastCount', { n: 3, parts: 12 })) + '</div></div>' +
        '<input type="checkbox" class="switch" role="switch" data-action="toggle-fast"' + (fast ? ' checked' : '') + '></div>' +
        '<hr class="divider">' +
        '<div class="adv-row"><div class="al"><div class="at">' + CB.esc(CB.t('wizard.adv.excludes')) + '</div>' +
        '<div class="ad">' + CB.esc(CB.t('wizard.adv.excludesSub')) + '</div></div>' +
        '<button class="btn text sm" data-action="noop">' + CB.esc(CB.t('common.change')) + '</button></div>' +
        '<div class="adv-row"><div class="al"><div class="at">' + CB.esc(CB.t('wizard.adv.speed')) + '</div></div>' +
        '<span class="hint">' + CB.esc(CB.t('common.unlimited')) + '</span></div>' +
        '<div class="adv-row"><div class="al"><div class="at">' + CB.esc(CB.t('wizard.adv.parts')) + '</div>' +
        '<div class="ad">' + CB.esc(CB.t('wizard.adv.partsSub')) + '</div></div>' +
        '<span class="hint">' + CB.esc(CB.t('common.auto') || '自動（2–16 份）') + '</span></div>' +
        '</div>' : '') +
      '</div>';
  }

  function progress(ctx) {
    var j = CB.data.jobs.active;
    var pct = j.bytes / j.total;
    var mins = Math.round(j.eta / 60);
    return '<div class="wizard-body" style="min-height:auto">' +
      '<div class="progress-view">' +
      '<div class="big-ring">' + CB.ring(pct, 128) + '</div>' +
      '<div class="progress-line-1">' + CB.esc(CB.t('progress.bytes', {
        done: CB.fmtBytes(j.bytes), total: CB.fmtBytes(j.total)
      })) + '</div>' +
      '<div class="progress-line-2">' + CB.esc(CB.t('progress.eta', { time: CB.fmtDuration(j.eta) })) + '</div>' +
      '<div class="progress-file">' + CB.icon('cloud-upload', 16) +
      '<span class="name truncate">' + CB.esc(CB.t('progress.current', { name: j.current })) + '</span>' +
      '<span class="status info">' + Math.round(j.speed / 1024 / 1024 * 10) / 10 + ' MB/s</span></div>' +
      '</div></div>';
  }

  CB.overlays.wizard = function (ctx) {
    var m = /^wizard\/(\d)/.exec(ctx.route);
    var step = m ? parseInt(m[1], 10) : 1;
    var isProgress = ctx.route === 'wizard/progress';

    var body, foot;
    if (isProgress) {
      body = progress(ctx);
      foot = '<div class="progress-actions">' +
        CB.btn({ label: CB.t('progress.pause'), variant: 'tonal', icon: 'pause', autofocus: true, data: { action: 'noop' } }) +
        CB.btn({ label: CB.t('common.cancel'), variant: 'text', data: { action: 'close-overlay' } }) +
        '</div>';
      foot = '<div class="grow"></div>' + foot;
    } else {
      body = step === 1 ? step1(ctx) : step === 2 ? step2(ctx) : step3(ctx);
      var mirror = ctx.flags.has('mirror');
      var disabled = step === 3 && mirror && !CB.state.mirrorChecked;
      foot = (step > 1
        ? CB.btn({ label: CB.t('common.back'), variant: 'text', autofocus: disabled, data: { action: 'wizard-back' } })
        : '') +
        '<div class="grow"></div>' +
        (step < 3
          ? CB.btn({ label: CB.t('common.next'), variant: 'filled', autofocus: true, data: { action: 'wizard-next' } })
          : CB.btn({
              label: CB.t('wizard.s3.start'),
              variant: 'filled',
              icon: 'cloud-upload',
              autofocus: !disabled,
              disabled: disabled,
              data: { action: 'wizard-start' }
            }));
    }

    return '<dialog class="dlg" closedby="any" aria-labelledby="wiz-title">' +
      '<div class="dialog wizard">' +
      '<div class="dialog-head">' +
      '<div class="grow"><div class="dialog-title" id="wiz-title">' + CB.esc(isProgress ? CB.t('progress.backingUp') : CB.t('wizard.title')) + '</div>' +
      (isProgress ? '' : '<div style="margin-top:8px">' + steps(step) + '</div>') + '</div>' +
      '<button class="icon-button" aria-label="' + CB.esc(CB.t('common.close')) + '" data-action="close-overlay">' + CB.icon('close', 20) + '</button>' +
      '</div>' +
      '<div class="dialog-body">' + body + '</div>' +
      '<div class="dialog-foot">' + foot + '</div>' +
      '</div></dialog>';
  };
})();
