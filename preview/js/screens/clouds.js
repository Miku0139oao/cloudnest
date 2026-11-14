/* =========================================================================
   Cloud accounts + add-provider sheet + OAuth waiting
   ========================================================================= */
(function () {
  CB.screens = CB.screens || {};
  CB.overlays = CB.overlays || {};

  function pname(p) { return CB.lang === 'en' ? p.nameEn : p.name; }

  CB.screens.clouds = function (ctx) {
    var d = CB.data;
    var cards = d.accounts.map(function (a) {
      var pct = Math.max(0, Math.min(1, a.used / a.total));
      var ok = a.status === 'connected';
      return '<article class="card cloud-card">' +
        '<div class="cc-top">' + CB.providerTile(a.provider, CB.accountName(a), 24) +
        '<div class="grow"><div class="cc-name">' + CB.esc(CB.accountName(a)) + '</div>' +
        '<div class="cc-sub">' + CB.esc(a.email) + '</div></div>' +
        '<div class="cc-actions" style="display:flex;align-items:center;gap:8px">' +
        CB.status(ok ? 'ok' : 'err', ok ? CB.t('clouds.connected') : CB.t('clouds.relogin'), true) +
        '<button class="icon-button" aria-label="更多" data-action="noop" style="width:32px;height:32px">' + CB.icon('more-vert', 18) + '</button>' +
        '</div></div>' +
        '<div class="usage-line"><span>' + CB.esc(CB.t('clouds.used', {
          used: CB.fmtBytes(a.used), total: CB.fmtBytes(a.total)
        })) + '</span><span class="nums">' + Math.round(pct * 100) + '%</span></div>' +
        CB.bar(pct) +
        '<div class="cc-foot"><span>' + CB.esc(CB.t('clouds.lastBackup', { time: CB.fmtTime(a.lastBackup) })) + '</span>' +
        '<button class="btn text sm" data-action="nav" data-view="files" data-account="' + a.id + '">' +
        CB.esc(CB.t('clouds.browse')) + '</button></div>' +
        '</article>';
    }).join('');

    var addCard = '<button class="card outlined cloud-card" style="align-items:center;justify-content:center;gap:12px;min-height:182px" data-action="nav" data-view="clouds/add">' +
      '<span class="qq" style="width:52px;height:52px;border-radius:16px;background:var(--m3c-primary-container);color:var(--m3c-on-primary-container);display:grid;place-items:center">' +
      CB.icon('add', 26) + '</span>' +
      '<div style="text-align:center"><div class="cc-name">' + CB.esc(CB.t('clouds.add')) + '</div>' +
      '<div class="cc-sub">' + CB.esc(CB.t('sheet.addSub')) + '</div></div></button>';

    return '<div class="scroll"><div class="page clouds">' +
      '<div class="page-head"><div><h1 class="page-title">' + CB.esc(CB.t('clouds.title')) + '</h1>' +
      '<p class="page-sub">' + CB.esc(CB.t('clouds.sub')) + '</p></div>' +
      CB.btn({ label: CB.t('clouds.add'), variant: 'tonal', icon: 'add', data: { action: 'nav', view: 'clouds/add' } }) +
      '</div>' +
      '<div class="cloud-grid">' + cards + addCard + '</div>' +
      '</div></div>';
  };

  CB.screens['clouds/add'] = CB.screens.clouds;

  CB.overlays.addProvider = function (ctx) {
    var tierA = CB.data.providers.filter(function (p) { return p.tier === 'A'; });
    var tierBC = CB.data.providers.filter(function (p) { return p.tier !== 'A'; });
    var advanced = ctx.flags.has('advanced');

    return '<dialog class="dlg sheet-dialog" closedby="any" aria-labelledby="add-sheet-title">' +
      '<div class="sheet">' +
      '<div class="sheet-head"><div class="grow">' +
      '<div class="sheet-title" id="add-sheet-title">' + CB.esc(CB.t('sheet.addTitle')) + '</div>' +
      '<div class="sheet-sub">' + CB.esc(CB.t('sheet.addSub')) + '</div></div>' +
      '<button class="icon-button" aria-label="' + CB.esc(CB.t('common.close')) + '" data-action="close-overlay">' + CB.icon('close', 20) + '</button>' +
      '</div>' +
      '<div class="sheet-body">' +
      '<div class="section-label">' + CB.esc(CB.t('sheet.oneClick')) + '</div>' +
      '<div class="provider-grid">' + tierA.map(function (p) {
        return '<button class="provider-card" data-action="connect" data-provider="' + p.id + '">' +
          (p.recommended ? '<span class="rec">' + CB.esc(CB.t('onboarding.recommended')) + '</span>' : '') +
          '<span class="ptile ' + p.id + '">' + CB.providerIcon(p.id, 24) + '</span>' +
          '<span>' + CB.esc(pname(p)) + '</span></button>';
      }).join('') + '</div>' +

      '<div class="section-label">' + CB.esc(CB.t('sheet.manual')) + '</div>' +
      '<div>' + tierBC.map(function (p) {
        return '<div class="tier-row" data-action="connect" data-provider="' + p.id + '">' +
          CB.providerTile(p.id, pname(p), 20) +
          '<span class="tr-n">' + CB.esc(pname(p)) + '</span>' +
          '<span class="chip">' + CB.esc(CB.t('sheet.advanced')) + '</span>' +
          CB.icon('chevron-right', 16) + '</div>';
      }).join('') + '</div>' +

      (advanced ? '<div class="banner warn" style="margin-top:18px">' + CB.icon('info', 20) +
        '<div>' + CB.esc(CB.t('sheet.warn')) + '</div></div>' : '') +
      '<div class="privacy" style="margin-top:22px">' + CB.icon('lock', 17) +
      '<div>' + CB.esc(CB.t('onboarding.trust')) + '</div></div>' +
      '</div>' +
      '<div class="sheet-foot">' +
      CB.btn({ label: CB.t('common.cancel'), variant: 'text', autofocus: true, data: { action: 'close-overlay' } }) +
      '</div></div></dialog>';
  };

  CB.overlays.connect = function (ctx) {
    var provider = ctx.params.provider || 'google';
    var p = CB.data.providers.filter(function (x) { return x.id === provider; })[0] || CB.data.providers[0];
    var waiting = ctx.route === 'clouds/oauth';

    return '<dialog class="dlg sheet-dialog" closedby="any" aria-labelledby="connect-title">' +
      '<div class="sheet">' +
      '<div class="sheet-head"><div class="grow"></div>' +
      '<button class="icon-button" aria-label="' + CB.esc(CB.t('common.close')) + '" data-action="close-overlay">' + CB.icon('close', 20) + '</button>' +
      '</div>' +
      '<div class="sheet-body">' +
      '<div class="connect-hero">' +
      '<span class="ptile ' + p.id + '">' + CB.providerIcon(p.id, 36) + '</span>' +
      '<h2 id="connect-title">' + CB.esc(CB.t('sheet.connectTitle', { name: pname(p) })) + '</h2>' +
      '<p>' + CB.esc(CB.t('sheet.connectBody')) + '</p>' +
      '</div>' +

      '<div class="oauth-steps">' +
      '<div class="oauth-step ' + (waiting ? 'done' : 'current') + '"><span class="n">' + (waiting ? CB.icon('check', 14) : '1') + '</span>' +
      '<div><div class="ot">' + CB.esc(CB.t('sheet.oauth1')) + '</div></div></div>' +
      '<div class="oauth-step ' + (waiting ? 'current' : 'pending') + '"><span class="n">2</span>' +
      '<div><div class="ot">' + CB.esc(CB.t('sheet.oauth2')) + '</div></div></div>' +
      '<div class="oauth-step pending"><span class="n">3</span>' +
      '<div><div class="ot">' + CB.esc(CB.t('sheet.oauth3')) + '</div></div></div>' +
      '</div>' +

      (waiting ? '<div class="oauth-status">' + CB.icon('sync', 18) + CB.esc(CB.t('sheet.waiting')) +
        '<span class="bar indeterminate"><i></i></span></div>' : '') +

      '<div class="banner info" style="margin-top:14px;align-items:center">' + CB.icon('lock', 18) +
      '<div>' + CB.esc(CB.t('sheet.connectTrust')) + '</div></div>' +
      '</div>' +
      '<div class="sheet-foot" style="flex-direction:column;align-items:stretch;gap:8px">' +
      CB.btn({ label: CB.t('sheet.connectCta'), variant: 'filled', icon: 'open-in-new', autofocus: true, data: { action: waiting ? 'reopen' : 'open-browser' } }) +
      (waiting ? '<button class="btn text" data-action="copy-link">' + CB.esc(CB.t('sheet.copyLink')) + '</button>' : '') +
      '<button class="btn text" data-action="close-overlay">' + CB.esc(CB.t('common.cancel')) + '</button>' +
      '</div></div></dialog>';
  };
})();
