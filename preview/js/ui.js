/* =========================================================================
   Cloudnest — tiny HTML builders + brand marks
   ========================================================================= */
window.CB = window.CB || {};

CB.esc = function (s) {
  return String(s === undefined || s === null ? '' : s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
};

CB.attr = function (obj) {
  var out = '';
  for (var k in obj) {
    if (!Object.prototype.hasOwnProperty.call(obj, k)) continue;
    var v = obj[k];
    if (v === undefined || v === null || v === false) continue;
    var name = (k.indexOf('data-') === 0 || k.indexOf('aria-') === 0) ? k : 'data-' + k;
    out += ' ' + name + (v === true ? '' : '="' + CB.esc(v) + '"');
  }
  return out;
};

/* --- brand marks -------------------------------------------------------- */
CB.brand = {
  google:
    '<path fill="#4285F4" d="M8.4 3h7.2l7.1 12.2h-7.2z"/>' +
    '<path fill="#34A853" d="M8.4 3L1.3 15.2l3.6 6.3L12 9.3z"/>' +
    '<path fill="#FBBC04" d="M4.9 21.5h13.9l3.6-6.3H8.5z"/>',
  onedrive:
    '<path fill="#0364B8" d="M9.6 7.4a5.2 5.2 0 0 1 8.7 1.9 4.2 4.2 0 0 1-.4 8.4H7.1A4.6 4.6 0 0 1 6 8.6a5.2 5.2 0 0 1 3.6-1.2z"/>' +
    '<path fill="#28A8EA" d="M6 8.6a4.6 4.6 0 0 0-1 9.1h3.3a4.6 4.6 0 0 1 1-9.1z" opacity=".9"/>',
  dropbox:
    '<g fill="#0061FF"><path d="M6 2l6 3.8L6 9.6 0 5.8z"/><path d="M18 2l6 3.8-6 3.8-6-3.8z"/>' +
    '<path d="M6 10l6 3.8L6 17.6 0 13.8z"/><path d="M18 10l6 3.8-6 3.8-6-3.8z"/>' +
    '<path d="M6 18.4l6 3.8 6-3.8-6-3.6z"/></g>',
  box:
    '<path fill="#0061D5" d="M12 2.2l9.4 5.4v8.8L12 21.8 2.6 16.4V7.6z"/>' +
    '<path fill="#2488FF" d="M12 2.2l9.4 5.4L12 13 2.6 7.6z"/>' +
    '<path fill="#0057C8" d="M12 13v8.8L2.6 16.4V7.6z" opacity=".9"/>',
  mega:
    '<rect x="2" y="2" width="20" height="20" rx="5" fill="#D9272E"/>' +
    '<path fill="#fff" d="M6.2 17.2V6.8h2.4L12 12l3.4-5.2h2.4v10.4h-2.2v-6.6L12 15.8l-3.6-5.2v6.6z"/>',
  pcloud:
    '<path fill="#21A0DF" d="M7.4 18.6a4.7 4.7 0 0 1-.6-9.35 5.8 5.8 0 0 1 11.1 1.4 4 4 0 0 1-.5 7.95z"/>' +
    '<circle cx="12" cy="12.6" r="2.1" fill="#fff"/>',
  aws:
    '<path fill="#FF9900" d="M4.5 6h15l-1.4 12.2a2 2 0 0 1-2 1.8H7.9a2 2 0 0 1-2-1.8z"/>' +
    '<path fill="#fff" d="M6.1 8h11.8l-.2 2H6.3z" opacity=".9"/>' +
    '<path fill="none" stroke="#232F3E" stroke-width="1.4" stroke-linecap="round" d="M6 20.4c3.6 1.9 8.4 1.9 12 0"/>',
  webdav:
    '<g fill="none" stroke="currentColor" stroke-width="1.7"><circle cx="12" cy="12" r="8.6"/>' +
    '<path d="M3.4 12h17.2M12 3.4c2.6 2.4 2.6 14.8 0 17.2M12 3.4c-2.6 2.4-2.6 14.8 0 17.2"/></g>'
};

CB.providerIcon = function (provider, size) {
  var body = CB.brand[provider] || CB.brand.webdav;
  var s = size || 26;
  return '<svg class="provider-icon" width="' + s + '" height="' + s +
    '" viewBox="0 0 24 24" aria-hidden="true" focusable="false">' + body + '</svg>';
};

CB.providerTile = function (provider, name, size) {
  return '<span class="ptile ' + CB.esc(provider) + '">' +
    CB.providerIcon(provider, size || 24) + '</span>';
};

/* --- atoms -------------------------------------------------------------- */
CB.btn = function (opts) {
  var cls = 'btn ' + (opts.variant || 'text');
  if (opts.size === 'sm') cls += ' sm';
  if (opts.class) cls += ' ' + opts.class;
  var dis = opts.disabled ? ' disabled' : '';
  return '<button class="' + cls + '"' + CB.attr(opts.data) + (opts.autofocus ? ' autofocus' : '') + dis + '>' +
    (opts.icon ? CB.icon(opts.icon, 18) : '') +
    (opts.label ? '<span>' + (opts.rawLabel ? opts.label : CB.esc(opts.label)) + '</span>' : '') +
    '</button>';
};

CB.iconBtn = function (name, label, data, size) {
  return '<button class="icon-button" aria-label="' + CB.esc(label) + '"' +
    CB.attr(data || {}) + '>' + CB.icon(name, size || 20) + '</button>';
};

CB.status = function (kind, label, dot) {
  return '<span class="status ' + kind + '">' +
    (dot ? '<span class="dot"></span>' : '') + CB.esc(label) + '</span>';
};

CB.chip = function (opts) {
  var cls = 'chip' + (opts.selected ? ' selected' : '') + (opts.interactive ? ' interactive' : '') +
    (opts.class ? ' ' + opts.class : '');
  return '<button class="' + cls + '"' + CB.attr(opts.data || {}) + '>' +
    (opts.icon ? CB.icon(opts.icon, 16) : '') +
    (opts.label ? '<span>' + CB.esc(opts.label) + '</span>' : '') +
    '</button>';
};

CB.ring = function (pct, size, label) {
  size = size || 56;
  var stroke = size >= 100 ? 8 : 5;
  var r = (size - stroke) / 2;
  var c = 2 * Math.PI * r;
  var off = c * (1 - Math.max(0, Math.min(1, pct)));
  var inner = label === false ? '' :
    '<span class="ring-text">' + (label || ('<span class="pct">' + Math.round(pct * 100) + '%</span>')) + '</span>';
  return '<span class="ring" style="width:' + size + 'px;height:' + size + 'px">' +
    '<svg width="' + size + '" height="' + size + '" aria-hidden="true">' +
    '<circle class="track" cx="' + size / 2 + '" cy="' + size / 2 + '" r="' + r + '" stroke-width="' + stroke + '"/>' +
    '<circle class="value" cx="' + size / 2 + '" cy="' + size / 2 + '" r="' + r + '" stroke-width="' + stroke +
    '" stroke-dasharray="' + c.toFixed(2) + '" stroke-dashoffset="' + off.toFixed(2) + '"/>' +
    '</svg>' + inner + '</span>';
};

CB.bar = function (pct, cls) {
  return '<span class="bar ' + (cls || '') + '"><i style="width:' + Math.round(pct * 100) + '%"></i></span>';
};

CB.spark = function (series, w, h) {
  w = w || 132; h = h || 34;
  var step = w / (series.length - 1);
  var pts = series.map(function (v, i) { return (i * step).toFixed(1) + ',' + (h - v * (h - 4) - 2).toFixed(1); });
  var line = 'M' + pts.join(' L');
  var area = line + ' L' + w + ',' + h + ' L0,' + h + ' Z';
  return '<svg class="spark" width="' + w + '" height="' + h + '" viewBox="0 0 ' + w + ' ' + h + '" aria-hidden="true">' +
    '<path class="area" d="' + area + '"/><path class="line" d="' + line + '"/></svg>';
};

CB.fileIcon = function (entry) {
  if (entry.dir) return '<span class="i">' + CB.icon('folder', 18) + '</span>';
  var map = {
    img: ['image', 'icon-img'],
    vid: ['movie', 'icon-vid'],
    aud: ['music-note', 'icon-aud'],
    pdf: ['picture-as-pdf', 'icon-doc'],
    doc: ['description', 'icon-doc'],
    zip: ['archive', 'icon-zip']
  };
  var m = map[entry.kind] || ['description', 'icon-zip'];
  return '<span class="i ' + m[1] + '">' + CB.icon(m[0], 18) + '</span>';
};

CB.avatar = function (initials) {
  return '<span class="ptile" style="background:var(--m3c-secondary-container);color:var(--m3c-on-secondary-container);font:600 14px/1 var(--font-sans)">' +
    CB.esc(initials) + '</span>';
};

CB.accountName = function (a) {
  return CB.lang === 'en' ? a.nameEn : a.name;
};

CB.timeText = function (d) { return CB.fmtTime(d); };
