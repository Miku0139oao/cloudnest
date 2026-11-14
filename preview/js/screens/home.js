/* =========================================================================
   Home
   ========================================================================= */
(function () {
  CB.screens = CB.screens || {};

  function heroArt() {
    return '' +
      '<svg class="hero-art" viewBox="0 0 300 160" aria-hidden="true">' +
      '<circle cx="236" cy="42" r="30" fill="#ffffff" opacity=".28"/>' +
      '<circle cx="196" cy="126" r="16" fill="#ffffff" opacity=".22"/>' +
      '<circle cx="92" cy="34" r="8" fill="#ffffff" opacity=".35"/>' +
      '<g transform="rotate(-9 118 82)">' +
      '<rect x="64" y="30" width="72" height="96" rx="16" fill="#ffffff" opacity=".45"/>' +
      '</g>' +
      '<g transform="rotate(8 196 80)">' +
      '<rect x="168" y="22" width="84" height="110" rx="18" fill="#ffffff" opacity=".72"/>' +
      '</g>' +
      '<rect x="118" y="34" width="80" height="94" rx="18" fill="#7114FF"/>' +
      '<path d="M158 104V68l-13 13-6.5-6.5L158 55l19.5 19.5-6.5 6.5L158 68v36z" fill="#ffffff"/>' +
      '<rect x="132" y="110" width="52" height="6" rx="3" fill="#ffffff" opacity=".55"/>' +
      '</svg>';
  }

  CB.screens.home = function (ctx) {
    var d = CB.data;
    var accounts = d.accounts;
    var n = accounts.length;

    var clouds = accounts.map(function (a) {
      var used = a.used, total = a.total;
      var pct = Math.max(0, Math.min(1, used / total));
      var low = pct > 0.9;
      return '<article class="card card-mini cloud-mini interactive" data-action="nav" data-view="files" data-account="' + a.id + '">' +
        '<div class="top">' + CB.providerTile(a.provider, CB.accountName(a), 20) +
        '<div class="grow"><div class="nm">' + CB.esc(CB.accountName(a)) + '</div>' +
        '<div class="st">' + (a.status === 'connected' ? CB.t('clouds.connected') : CB.t('clouds.relogin')) + '</div></div>' +
        (low ? '<span class="status warn"><span class="dot"></span>' + CB.esc(CB.t('home.spaceFull')) + '</span>' : '') +
        '</div>' +
        '<div class="usage"><span>' + CB.esc(CB.t('home.used', { used: CB.fmtBytes(used), total: CB.fmtBytes(total) })) + '</span>' +
        '<span class="nums">' + Math.round(pct * 100) + '%</span></div>' +
        CB.bar(pct) +
        '</article>';
    }).join('');

    var addTile = '<button class="card outlined cloud-mini" style="align-items:center;justify-content:center;gap:8px;cursor:pointer" data-action="nav" data-view="clouds/add">' +
      '<span style="width:40px;height:40px;border-radius:12px;background:var(--m3c-primary-container);color:var(--m3c-on-primary-container);display:grid;place-items:center">' +
      CB.icon('add', 22) + '</span>' +
      '<span class="nm" style="font:var(--type-label-l)">' + CB.esc(CB.t('clouds.add')) + '</span>' +
      '</button>';

    var recent = d.history.slice(0, 3).map(function (h) {
      var failed = h.state === 'failed';
      var icon = failed ? 'error' : 'check';
      var cls = failed ? 'err' : 'ok';
      var titleKey = failed ? 'home.recent.failed' : 'home.recent.done';
      var title = h.kind === 'restore'
        ? CB.t('activity.restoreOf', { name: h.name })
        : CB.t(titleKey, { source: CB.lang === 'en' ? h.sourceEn : h.source });
      var sub = CB.fmtTime(h.time) + ' · ' + CB.t('home.recent.files', {
        files: h.kind === 'restore' ? 1 : h.files.toLocaleString('en-US'),
        size: CB.fmtBytes(h.size)
      });
      return '<div class="recent-row" data-action="nav" data-view="activity">' +
        '<span class="st-icon ' + cls + '">' + CB.icon(icon, 18) + '</span>' +
        '<div class="rmain"><div class="rt">' + CB.esc(title) + '</div>' +
        '<div class="rs">' + CB.esc(sub) + '</div></div>' +
        (failed ? '<button class="btn text sm" data-action="retry">' + CB.esc(CB.t('common.retry')) + '</button>' : '') +
        '</div>';
    }).join('');

    return '' +
      '<div class="scroll"><div class="page home">' +
      '<div class="home-head"><div class="greeting">' +
      '<h1>' + CB.esc(CB.t('home.greeting')) + '</h1>' +
      '<p>' + CB.esc(n ? CB.t('home.subtitle.connected', { n: n }) : CB.t('home.subtitle.empty')) + '</p>' +
      '</div></div>' +

      '<section class="hero">' +
      '<h2>' + CB.esc(CB.t('home.hero.title')) + '</h2>' +
      '<p>' + CB.esc(CB.t('home.hero.sub')) + '</p>' +
      CB.btn({ label: CB.t('home.hero.cta'), variant: 'filled', icon: 'cloud-upload', data: { action: 'nav', view: 'wizard/1' } }) +
      heroArt() +
      '</section>' +

      '<div class="quick-grid">' +
      '<button class="card tonal quick-card" data-action="nav" data-view="files" data-focus="cloud">' +
      '<span class="qq">' + CB.icon('cloud-download', 22) + '</span>' +
      '<span class="grow" style="text-align:left"><span class="qt">' + CB.esc(CB.t('home.quick.download')) + '</span><br>' +
      '<span class="qs">' + CB.esc(CB.t('home.quick.downloadSub')) + '</span></span>' +
      '<span class="go">' + CB.icon('chevron-right', 20) + '</span></button>' +
      '<button class="card tonal quick-card" data-action="nav" data-view="clouds">' +
      '<span class="qq">' + CB.icon('add-circle', 22) + '</span>' +
      '<span class="grow" style="text-align:left"><span class="qt">' + CB.esc(CB.t('home.quick.accounts')) + '</span><br>' +
      '<span class="qs">' + CB.esc(CB.t('home.quick.accountsSub')) + '</span></span>' +
      '<span class="go">' + CB.icon('chevron-right', 20) + '</span></button>' +
      '</div>' +

      '<div class="home-grid">' +
      '<section><div class="section-head"><h3 class="section-title">' + CB.esc(CB.t('home.clouds.title')) + '</h3></div>' +
      '<div class="cloud-strip">' + clouds + addTile + '</div></section>' +

      '<section><div class="section-head"><h3 class="section-title">' + CB.esc(CB.t('home.recent.title')) + '</h3>' +
      '<button class="btn text sm" data-action="nav" data-view="activity">' + CB.esc(CB.t('common.viewAll')) + '</button></div>' +
      '<div class="card recent" style="padding:6px">' + recent + '</div></section>' +
      '</div>' +

      '</div></div>';
  };

  /* --- Onboarding ------------------------------------------------------- */
  CB.screens.onboarding = function () {
    var tiles = [
      { id: 'google', name: 'Google 雲端硬碟', nameEn: 'Google Drive', rec: true },
      { id: 'onedrive', name: 'OneDrive', nameEn: 'OneDrive' },
      { id: 'dropbox', name: 'Dropbox', nameEn: 'Dropbox' },
      { id: 'box', name: 'Box', nameEn: 'Box' },
      { id: 'mega', name: 'MEGA', nameEn: 'MEGA' }
    ].map(function (p) {
      return '<button class="provider-tile' + (p.rec ? ' recommended' : '') + '" data-action="connect" data-provider="' + p.id + '">' +
        (p.rec ? '<span class="rec">' + CB.esc(CB.t('onboarding.recommended')) + '</span>' : '') +
        '<span class="ptile ' + p.id + '">' + CB.providerIcon(p.id, 21) + '</span>' +
        '<span>' + CB.esc(CB.lang === 'en' ? p.nameEn : p.name) + '</span></button>';
    }).join('');

    return '' +
      '<div class="onboarding">' +
      '<div class="onboard-card">' +
      '<div class="onboard-mark">' + CB.icon('cloud', 34) + '</div>' +
      '<h1>' + CB.esc(CB.t('onboarding.title')) + '</h1>' +
      '<p class="lede">' + CB.esc(CB.t('onboarding.sub')) + '</p>' +
      '<div class="provider-row">' + tiles + '</div>' +
      '<div class="onboard-actions">' +
      CB.btn({ label: CB.t('onboarding.connect'), variant: 'filled', icon: 'cloud-upload', data: { action: 'connect', provider: 'google' } }) +
      '<button class="btn text" data-action="nav" data-view="clouds/add">' + CB.esc(CB.t('onboarding.other')) + '</button>' +
      '<span class="trust">' + CB.icon('lock', 15) + CB.esc(CB.t('onboarding.trust')) + '</span>' +
      '</div>' +
      '<div style="margin-top:22px;font:var(--type-body-s);color:var(--m3c-on-surface-variant)">' +
      CB.esc(CB.t('onboarding.signup')) + ' <a href="#" data-action="signup">' + CB.esc(CB.t('onboarding.signupLink')) + '</a>' +
      '</div>' +
      '</div></div>';
  };
})();
