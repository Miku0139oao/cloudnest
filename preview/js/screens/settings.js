/* =========================================================================
   Settings
   ========================================================================= */
(function () {
  CB.screens = CB.screens || {};

  function seg(items, activeIndex) {
    return '<div class="segmented">' + items.map(function (it, i) {
      return '<button type="button" aria-pressed="' + (i === activeIndex ? 'true' : 'false') + '"' +
        CB.attr(it.data || {}) + '>' + CB.esc(it.label) + '</button>';
    }).join('') + '</div>';
  }

  function sw(on, action) {
    return '<input type="checkbox" class="switch" role="switch"' + (on ? ' checked' : '') +
      CB.attr({ 'data-action': action || 'noop' }) + '>';
  }

  function row(title, sub, control, cls) {
    return '<div class="set-row ' + (cls || '') + '">' +
      '<div class="sl"><div class="st">' + CB.esc(title) + '</div>' +
      (sub ? '<div class="sd">' + CB.esc(sub) + '</div>' : '') + '</div>' +
      '<div class="sc">' + control + '</div></div>';
  }

  CB.screens.settings = function (ctx) {
    var advanced = ctx.flags.has('advanced');
    var theme = ctx.theme;
    var appLang = CB.lang;
    var langIndex = appLang === 'en' ? 1 : 0;
    var themeIndex = theme === 'dark' ? 2 : (theme === 'light' ? 1 : 0);

    var nav = [
      { key: 'settings.general', icon: 'settings' },
      { key: 'settings.backup', icon: 'cloud-upload' },
      { key: 'settings.advanced', icon: 'shield' },
      { key: 'settings.about', icon: 'info' }
    ];
    var activeNav = advanced ? 2 : 0;

    var general =
      '<section class="set-section"><h2>' + CB.esc(CB.t('settings.general')) + '</h2><div class="set-card">' +
      row(CB.t('settings.language'), CB.t('settings.languageSub'),
        seg([{ label: '繁體中文', data: { action: 'set-lang', lang: 'zh-Hant' } },
             { label: 'English', data: { action: 'set-lang', lang: 'en' } }], langIndex)) +
      row(CB.t('settings.appearance'), CB.t('settings.appearanceSub'),
        seg([{ label: CB.t('settings.system'), data: { action: 'noop' } },
             { label: CB.t('settings.light'), data: { action: 'set-theme', theme: 'light' } },
             { label: CB.t('settings.dark'), data: { action: 'set-theme', theme: 'dark' } }], themeIndex)) +
      '</div></section>';

    var backup =
      '<section class="set-section"><h2>' + CB.esc(CB.t('settings.backup')) + '</h2><div class="set-card">' +
      row(CB.t('settings.transfers'), CB.t('settings.transfersSub'),
        '<div class="stepper"><button aria-label="minus" data-action="noop">' + CB.icon('remove', 18) + '</button>' +
        '<span class="value">4</span>' +
        '<button aria-label="plus" data-action="noop">' + CB.icon('add', 18) + '</button></div>') +
      row(CB.t('settings.speed'), CB.t('settings.speedSub'),
        '<button class="textfield" style="height:36px;min-width:132px;justify-content:space-between;border-radius:999px;background:var(--m3c-surface-container-high)" data-action="noop">' +
        '<span>' + CB.esc(CB.t('common.unlimited')) + '</span>' + CB.icon('expand-more', 18) + '</button>') +
      row(CB.t('settings.retry'), CB.t('settings.retrySub'), sw(true)) +
      row(CB.t('settings.fast'), CB.t('settings.fastSub', { size: '256 MB' }), sw(true, 'toggle-fast-setting')) +
      row(CB.t('settings.parts'), CB.t('settings.partsSub'), '') +
      '</div></section>';

    var adv =
      '<section class="set-section"><h2>' + CB.esc(CB.t('settings.advanced')) + '</h2>' +
      (advanced
        ? '<div class="advanced-warning">' + CB.icon('warning', 18) +
          '<div>' + CB.esc(CB.t('settings.advancedWarn')) + '</div></div>' +
          '<div class="set-card">' +
          row(CB.t('settings.engine'), CB.t('settings.engineSub'),
            '<div class="engine-line"><span class="dot"></span><span class="nums">v1.70.3</span>' +
            '<button class="btn text sm" data-action="noop">' + CB.esc(CB.t('common.view')) + '</button></div>') +
          row(CB.t('settings.proxy'), CB.t('settings.proxySub'), sw(false)) +
          row(CB.t('settings.verbose'), CB.t('settings.verboseSub'), sw(false)) +
          '<div class="set-row danger"><div class="sl"><div class="st">' + CB.esc(CB.t('settings.reset')) + '</div>' +
          '<div class="sd">' + CB.esc(CB.t('settings.resetSub')) + '</div></div>' +
          '<div class="sc"><button class="btn text-danger sm" data-action="noop">' +
          CB.esc(CB.t('settings.resetAction')) + '</button></div></div>' +
          '</div>'
        : '<div class="set-card">' + row(CB.t('settings.engine'), CB.t('settings.engineSub'),
            '<div class="engine-line"><span class="dot"></span><span class="nums">v1.70.3</span></div>') +
          '</div>') +
      '</section>';

    var about =
      '<section class="set-section"><h2>' + CB.esc(CB.t('settings.about')) + '</h2><div class="set-card">' +
      row(CB.t('settings.version', { v: '1.0.0' }), 'Cloudnest / 雲栖',
        '<button class="btn text sm" data-action="noop">' + CB.esc(CB.t('settings.checkUpdate')) + '</button>') +
      row(CB.t('settings.license'), '', '<button class="btn text sm" data-action="noop">' +
        CB.esc(CB.t('common.view')) + '</button>') +
      '<div style="padding:14px 18px"><div class="privacy">' + CB.icon('shield', 17) +
      '<div>' + CB.esc(CB.t('settings.privacy')) + '</div></div></div>' +
      '</div></section>';

    return '<div class="settings">' +
      '<nav class="settings-nav"><h2 class="nv-title">' + CB.esc(CB.t('settings.title')) + '</h2>' +
      nav.map(function (n, i) {
        return '<button class="' + (i === activeNav ? 'active' : '') + '" data-action="noop">' +
          CB.icon(n.icon, 18) + CB.esc(CB.t(n.key)) + '</button>';
      }).join('') +
      '</nav>' +
      '<div class="settings-main"><div class="settings-wrap">' +
      (advanced ? adv + about : general + backup + adv + about) +
      '</div></div></div>';
  };
})();
