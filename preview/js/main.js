/* =========================================================================
   Router + shell renderer + interaction
   ========================================================================= */
(function () {
  var NAV = [
    { id: 'home', key: 'nav.home', icon: 'home' },
    { id: 'clouds', key: 'nav.clouds', icon: 'cloud' },
    { id: 'files', key: 'nav.files', icon: 'folder' },
    { id: 'activity', key: 'nav.activity', icon: 'sync' }
  ];

  CB.state = {
    lang: 'zh-Hant',
    themeSetting: 'system',
    mirrorMode: null,
    advancedOpen: false,
    mirrorChecked: false,
    fastFiles: true,
    sourceChoice: 'folder',
    account: 'gdrive',
    selCloud: new Set(),
    paused: false,
    drag: false,
    toast: null
  };

  CB.parseHash = function () {
    var raw = location.hash.replace(/^#/, '');
    var qi = raw.indexOf('?');
    var route = (qi >= 0 ? raw.slice(0, qi) : raw) || 'home';
    var qs = qi >= 0 ? raw.slice(qi + 1) : '';
    var params = {};
    qs.split('&').forEach(function (pair) {
      if (!pair) return;
      var i = pair.indexOf('=');
      var k = decodeURIComponent(i >= 0 ? pair.slice(0, i) : pair);
      var v = i >= 0 ? decodeURIComponent(pair.slice(i + 1)) : '';
      params[k] = v;
    });
    var flags = new Set((params.state || '').split(',').filter(Boolean));
    return { route: route, params: params, flags: flags };
  };

  CB.baseFor = function (route) {
    if (route.indexOf('wizard/') === 0) return 'home';
    if (route.indexOf('clouds/') === 0) return 'clouds';
    if (route === 'files/restore') return 'files';
    return route;
  };

  CB.overlayFor = function (route) {
    if (route.indexOf('wizard/') === 0) return 'wizard';
    if (route === 'clouds/add') return 'addProvider';
    if (route === 'clouds/connect' || route === 'clouds/oauth') return 'connect';
    if (route === 'files/restore') return 'restore';
    return null;
  };

  CB.writeHash = function (route, extraParams) {
    var q = [];
    q.push('lang=' + encodeURIComponent(CB.state.lang));
    q.push('theme=' + encodeURIComponent(CB.state.themeSetting));
    var st = [];
    if (route === 'activity' && CB.state.paused) st.push('paused');
    if (route === 'activity' && CB.state.splitView) st.push('split');
    if (route === 'files' && CB.state.selCloud.size) st.push('selected');
    if (route === 'files' && CB.state.drag) st.push('dragover');
    if (route.indexOf('wizard/') === 0) {
      if (CB.state.mirrorMode) st.push('mirror');
      if (CB.state.advancedOpen) st.push('advanced');
    }
    if (route === 'settings' && CB.state.advancedOpen) st.push('advanced');
    if (st.length) q.push('state=' + encodeURIComponent(st.join(',')));
    if (CB.state.staticMode) q.push('static=1');
    if (extraParams) {
      Object.keys(extraParams).forEach(function (k) {
        q.push(encodeURIComponent(k) + '=' + encodeURIComponent(extraParams[k]));
      });
    }
    location.hash = '#' + route + '?' + q.join('&');
  };

  CB.go = function (route, extra) { CB.writeHash(route, extra); };

  function resolveTheme() {
    if (CB.state.themeSetting === 'dark') return 'dark';
    if (CB.state.themeSetting === 'light') return 'light';
    return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }

  function titlebarContext(route) {
    if (route.indexOf('wizard/') === 0) return CB.t('wizard.title');
    if (route === 'clouds/add') return CB.t('sheet.addTitle');
    if (route === 'clouds/connect' || route === 'clouds/oauth') return CB.t('clouds.add');
    if (route === 'files/restore') return CB.t('restore.title', { name: CB.data.restore.name });
    var base = CB.baseFor(route);
    var map = { home: 'nav.home', clouds: 'nav.clouds', files: 'nav.files', activity: 'nav.activity', settings: 'nav.settings', onboarding: 'onboarding.title' };
    return CB.t(map[base] || 'nav.home');
  }

  function titlebar(route) {
    return '<header class="titlebar">' +
      '<div class="brand"><span class="appmark">' + CB.icon('cloud', 15) + '</span>' +
      '<span class="brandname">雲栖</span><span class="brandname-en">Cloudnest</span></div>' +
      '<div class="titlebar-context"><span class="sep"></span><span>' + CB.esc(titlebarContext(route)) + '</span></div>' +
      '<div class="titlebar-spacer"></div>' +
      '<div class="titlebar-actions">' +
      '<div class="lang" role="group" aria-label="Language">' +
      '<button aria-pressed="' + (CB.lang === 'zh-Hant' ? 'true' : 'false') + '" data-action="set-lang" data-lang="zh-Hant">中</button>' +
      '<button aria-pressed="' + (CB.lang === 'en' ? 'true' : 'false') + '" data-action="set-lang" data-lang="en">EN</button>' +
      '</div>' +
      '<button class="iconbtn" aria-label="' + CB.esc(resolveTheme() === 'dark' ? 'Light' : 'Dark') + '" data-action="toggle-theme">' +
      CB.icon(resolveTheme() === 'dark' ? 'light-mode' : 'dark-mode', 18) + '</button>' +
      '<div class="wincontrols">' +
      '<button aria-label="Minimize" data-action="noop"><svg class="i" width="16" height="16" viewBox="0 0 16 16"><line x1="3" y1="8" x2="13" y2="8" stroke="currentColor" stroke-width="1"/></svg></button>' +
      '<button aria-label="Maximize" data-action="noop"><svg class="i" width="16" height="16" viewBox="0 0 16 16" fill="none"><rect x="3.5" y="3.5" width="9" height="9" rx="1" stroke="currentColor" stroke-width="1"/></svg></button>' +
      '<button class="close" aria-label="Close" data-action="noop"><svg class="i" width="16" height="16" viewBox="0 0 16 16"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.1" stroke-linecap="round"/></svg></button>' +
      '</div></div></header>';
  }

  function rail(route) {
    var base = CB.baseFor(route);
    var items = NAV.map(function (n) {
      return '<button class="rail-item" ' + (base === n.id ? 'aria-current="page"' : '') +
        ' data-action="nav" data-view="' + n.id + '">' +
        '<span class="indicator">' + CB.icon(n.icon, 22) + '</span>' +
        '<span class="label">' + CB.esc(CB.t(n.key)) + '</span>' +
        (n.id === 'activity' ? '<span class="badge">2</span>' : '') +
        '</button>';
    }).join('');
    return '<nav class="rail" aria-label="Main">' + items +
      '<div class="rail-spacer"></div><div class="rail-sep"></div>' +
      '<button class="rail-item" ' + (base === 'settings' ? 'aria-current="page"' : '') +
      ' data-action="nav" data-view="settings">' +
      '<span class="indicator">' + CB.icon('settings', 22) + '</span>' +
      '<span class="label">' + CB.esc(CB.t('nav.settings')) + '</span></button>' +
      '</nav>';
  }

  CB.render = function () {
    var ctx = CB.parseHash();
    ctx.theme = resolveTheme();
    ctx.lang = CB.state.lang;

    if (ctx.route === 'wizard/3') {
      if (CB.state.mirrorMode !== null) {
        if (CB.state.mirrorMode) ctx.flags.add('mirror'); else ctx.flags.delete('mirror');
      }
      if (CB.state.advancedOpen) ctx.flags.add('advanced'); else ctx.flags.delete('advanced');
    }
    if (ctx.route === 'files') {
      if (CB.state.selCloud.size) ctx.flags.add('selected');
      if (CB.state.drag) ctx.flags.add('dragover');
    }
    if (ctx.route === 'activity') {
      if (CB.state.paused) ctx.flags.add('paused');
      if (CB.state.splitView) ctx.flags.add('split');
    }

    var base = CB.baseFor(ctx.route);
    var screenFn = CB.screens[base];
    var screenHTML = screenFn ? screenFn(ctx) : CB.screens.home(ctx);

    var overlay = CB.overlayFor(ctx.route);
    var overlayHTML = '';
    if (overlay && CB.overlays[overlay]) {
      var octx = Object.assign({}, ctx);
      octx.route = ctx.route;
      octx.params = Object.assign({}, ctx.params);
      overlayHTML = CB.overlays[overlay](octx);
    }

    var toast = CB.state.toast
      ? '<div class="snackbar">' + CB.esc(CB.state.toast) +
        '<div class="grow"></div><button class="s-action" data-action="clear-toast">OK</button></div>'
      : '';

    var showRail = base !== 'onboarding';

    document.getElementById('app').innerHTML =
      '<div class="app">' + titlebar(ctx.route) +
      '<div class="body">' + (showRail ? rail(ctx.route) : '') +
      '<main class="content">' + screenHTML + overlayHTML + toast + '</main>' +
      '</div></div>';

    openModals();
  };

  /* Native <dialog> gives us the top layer, focus containment, Esc and
     platform close requests. On any dismissal we return to the base view. */
  function openModals() {
    var modals = document.querySelectorAll('dialog.dlg');
    for (var i = 0; i < modals.length; i++) {
      var d = modals[i];
      if (!d.open) {
        try { d.showModal(); } catch (e) { /* already open / detached */ }
      }
      d.addEventListener('close', function () {
        // Ignore closes caused by our own re-render replacing the node.
        if (!this.isConnected) return;
        var cur = CB.parseHash();
        CB.go(CB.baseFor(cur.route));
      });
    }
  }

  CB.applyChrome = function () {
    document.documentElement.setAttribute('data-theme', resolveTheme());
    if (CB.state.staticMode) document.documentElement.setAttribute('data-static', '1');
    else document.documentElement.removeAttribute('data-static');
  };

  CB.boot = function () {
    var ctx = CB.parseHash();
    CB.state.lang = ctx.params.lang === 'en' ? 'en' : 'zh-Hant';
    CB.state.themeSetting = ctx.params.theme || 'system';
    CB.state.staticMode = ctx.params.static === '1';
    CB.state.mirrorMode = ctx.flags.has('mirror') ? true : (ctx.route === 'wizard/3' ? false : null);
    CB.state.advancedOpen = ctx.flags.has('advanced');
    CB.state.paused = ctx.flags.has('paused');
    CB.state.splitView = ctx.flags.has('split');
    CB.state.drag = ctx.flags.has('dragover');
    CB.state.selCloud = new Set(ctx.flags.has('selected') ? CB.data.selectedCloud : []);
    if (ctx.params.paused === '1') CB.state.paused = true;
    if (ctx.flags.has('mirror')) CB.state.mirrorChecked = false;
    CB.state.fastFiles = !ctx.flags.has('slow');
    CB.state.booted = true;

    CB.setLang(CB.state.lang);
    CB.applyChrome();
    CB.render();
  };

  /* --- interactions ----------------------------------------------------- */
  function wizardStep(route) {
    var m = /^wizard\/(\d)/.exec(route);
    return m ? parseInt(m[1], 10) : 1;
  }

  function onAction(e) {
    var el = e.target.closest('[data-action]');
    if (!el) return;
    var a = el.getAttribute('data-action');

    if (a === 'scrim-close') {
      if (e.target === el) {
        var ctx0 = CB.parseHash();
        CB.go(CB.baseFor(ctx0.route));
      }
      return;
    }
    if (a === 'noop') { e.preventDefault(); return; }

    var ctx = CB.parseHash();

    switch (a) {
      case 'nav':
        e.preventDefault();
        CB.go(el.getAttribute('data-view'));
        break;
      case 'set-lang':
        CB.state.lang = el.getAttribute('data-lang');
        CB.setLang(CB.state.lang);
        CB.applyChrome();
        CB.go(ctx.route);
        break;
      case 'toggle-theme': {
        var next = resolveTheme() === 'dark' ? 'light' : 'dark';
        CB.state.themeSetting = next;
        CB.applyChrome();
        CB.go(ctx.route);
        break;
      }
      case 'set-theme':
        CB.state.themeSetting = el.getAttribute('data-theme');
        CB.applyChrome();
        CB.go(ctx.route);
        break;
      case 'close-overlay': {
        var dlg = el.closest('dialog');
        if (dlg && dlg.open) dlg.close();
        else CB.go(CB.baseFor(ctx.route));
        break;
      }
      case 'wizard-next': {
        var step = wizardStep(ctx.route);
        CB.go('wizard/' + Math.min(3, step + 1));
        break;
      }
      case 'wizard-back': {
        var s2 = wizardStep(ctx.route);
        CB.go('wizard/' + Math.max(1, s2 - 1));
        break;
      }
      case 'wizard-start':
        CB.go('wizard/progress');
        break;
      case 'wizard-mode':
        CB.state.mirrorMode = el.getAttribute('data-mode') === 'mirror';
        CB.go(ctx.route);
        break;
      case 'wizard-choice':
        CB.state.sourceChoice = el.getAttribute('data-choice');
        CB.render();
        break;
      case 'choose-account':
        CB.state.account = el.getAttribute('data-account');
        CB.render();
        break;
      case 'toggle-mirror-confirm':
        CB.state.mirrorChecked = !CB.state.mirrorChecked;
        CB.render();
        break;
      case 'toggle-advanced':
        CB.state.advancedOpen = !CB.state.advancedOpen;
        CB.go(ctx.route);
        break;
      case 'toggle-fast':
      case 'toggle-fast-setting':
        CB.state.fastFiles = !CB.state.fastFiles;
        CB.render();
        break;
      case 'select-row': {
        var id = el.getAttribute('data-id');
        var pane = el.getAttribute('data-pane');
        if (pane === 'cloud') {
          if (CB.state.selCloud.has(id)) CB.state.selCloud.delete(id);
          else CB.state.selCloud.add(id);
          CB.render();
        }
        break;
      }
      case 'open-restore':
        CB.go('files/restore');
        break;
      case 'connect':
        CB.go('clouds/connect', { provider: el.getAttribute('data-provider') || 'google' });
        break;
      case 'open-browser':
      case 'reopen':
        CB.go('clouds/oauth', { provider: ctx.params.provider || 'google' });
        break;
      case 'copy-link':
        CB.toast(CB.t('sheet.copyLink') + ' ✓');
        break;
      case 'retry':
        CB.toast(CB.t('common.retry') + '…');
        break;
      case 'clear-toast':
        CB.state.toast = null;
        CB.render();
        break;
      case 'signup':
        e.preventDefault();
        break;
      default:
        break;
    }
  }

  CB.toast = function (msg) {
    CB.state.toast = msg;
    CB.render();
    setTimeout(function () {
      CB.state.toast = null;
      if (CB.state.booted) CB.render();
    }, 2600);
  };

  window.addEventListener('hashchange', function () {
    var ctx = CB.parseHash();
    if (ctx.params.lang) {
      CB.state.lang = ctx.params.lang === 'en' ? 'en' : 'zh-Hant';
      CB.setLang(CB.state.lang);
    }
    if (ctx.params.theme) CB.state.themeSetting = ctx.params.theme;
    if (ctx.params.static === '1') CB.state.staticMode = true;
    CB.applyChrome();
    CB.render();
  });

  window.addEventListener('DOMContentLoaded', function () {
    document.addEventListener('click', onAction);
    CB.boot();

    // System preference can change at any time; follow it when not pinned.
    if (window.matchMedia) {
      var mq = window.matchMedia('(prefers-color-scheme: dark)');
      var onScheme = function () {
        if (CB.state.themeSetting === 'system') {
          CB.applyChrome();
          CB.render();
        }
      };
      if (mq.addEventListener) mq.addEventListener('change', onScheme);
      else if (mq.addListener) mq.addListener(onScheme);
    }

    window.__preview = {
      go: function (h) { location.hash = h; },
      setLang: function (l) { CB.state.lang = l; CB.setLang(l); CB.applyChrome(); CB.render(); },
      setTheme: function (t) { CB.state.themeSetting = t; CB.applyChrome(); CB.render(); },
      render: function () { CB.render(); }
    };
  });
})();
