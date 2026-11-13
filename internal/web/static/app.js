'use strict';
// HomeBackup dashboard. Talks to the same API as the apps (/api/v1) with a
// device token kept in this browser. No build step, no dependencies.

const API = '/api/v1';
const $ = (sel, root = document) => root.querySelector(sel);

// ---------- tiny DOM helper ----------
function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (v == null || v === false) continue;
      if (k === 'class') el.className = v;
      else if (k === 'style') el.style.cssText = v;
      else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
      else if (v === true) el.setAttribute(k, '');
      else el.setAttribute(k, v);
    }
  }
  for (const c of children.flat(Infinity)) {
    if (c == null || c === false) continue;
    el.append(c.nodeType ? c : document.createTextNode(String(c)));
  }
  return el;
}

// ---------- session ----------
const session = {
  get token() { try { return localStorage.getItem('hb.token'); } catch { return null; } },
  get deviceId() { try { return localStorage.getItem('hb.deviceId'); } catch { return null; } },
  set(token, deviceId) {
    try {
      localStorage.setItem('hb.token', token);
      if (deviceId) localStorage.setItem('hb.deviceId', deviceId); else localStorage.removeItem('hb.deviceId');
    } catch {}
  },
  clear() { try { localStorage.removeItem('hb.token'); localStorage.removeItem('hb.deviceId'); } catch {} },
};

class ApiError extends Error {
  constructor(status, code, message) { super(message); this.status = status; this.code = code; }
}

async function api(method, path, body, { raw = false, headers = {} } = {}) {
  const hdr = { ...headers };
  if (session.token) hdr.Authorization = 'Bearer ' + session.token;
  let payload = body;
  if (body !== undefined && body !== null && !(body instanceof ArrayBuffer) && !(body instanceof Blob) && !ArrayBuffer.isView(body)) {
    hdr['Content-Type'] = 'application/json';
    payload = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch(API + path, { method, headers: hdr, body: payload });
  } catch {
    setOffline(true);
    throw new ApiError(0, 'network', "Can't reach the server. Is it turned on and on this network?");
  }
  setOffline(false);
  if (res.status === 401 && path !== '/pair') {
    session.clear();
    showSignIn('This browser is no longer paired with the server. Pair it again to continue.');
    throw new ApiError(401, 'unauthorized', 'not paired');
  }
  if (!res.ok) {
    let msg = res.statusText, code = '';
    try { const j = await res.json(); if (j && j.error) { msg = j.error.message; code = j.error.code; } } catch {}
    throw new ApiError(res.status, code, msg);
  }
  if (raw) return res;
  if (res.status === 204) return null;
  const ct = res.headers.get('content-type') || '';
  return ct.includes('json') ? res.json() : res;
}

function friendly(e) {
  if (!(e instanceof ApiError)) return (e && e.message) || String(e);
  switch (e.status) {
    case 0: return e.message;
    case 400: return e.message || 'The server did not accept that request.';
    case 401: return 'That code is wrong or expired. Codes last 10 minutes; get a new one and try again.';
    case 403: return 'This backup is read-only. Only My Files can be changed.';
    case 404: return "The server doesn't have that anymore.";
    case 409: return e.message || 'Something with that name already exists.';
    case 413: return 'That file is too large for one upload.';
    case 422: return 'The upload was damaged in transit and was discarded. Try again.';
    case 429: return 'Too many attempts. Wait a few seconds and try again.';
    default: return e.message ? e.message : 'The server answered with error ' + e.status + '.';
  }
}

// ---------- formatting ----------
function fmtBytes(n) {
  n = Number(n) || 0;
  const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let i = 0;
  while (n >= 1000 && i < u.length - 1) { n /= 1000; i++; }
  return (i === 0 ? n : n.toFixed(n < 10 ? 2 : n < 100 ? 1 : 0)) + ' ' + u[i];
}
function fmtAgo(d) {
  if (!d) return '';
  const t = d instanceof Date ? d : new Date(d);
  const s = Math.round((Date.now() - t.getTime()) / 1000);
  if (isNaN(s)) return '';
  if (s < 45) return 'just now';
  if (s < 90) return 'a minute ago';
  const m = Math.round(s / 60);
  if (m < 60) return m + ' min ago';
  const hr = Math.round(m / 60);
  if (hr < 36) return hr + (hr === 1 ? ' hour ago' : ' hours ago');
  const days = Math.round(hr / 24);
  if (days < 14) return days + ' days ago';
  return t.toLocaleDateString();
}
function fmtDate(d) {
  if (!d) return '';
  const t = d instanceof Date ? d : new Date(d);
  return isNaN(t) ? '' : t.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
}
function shortKey(fp) {
  if (!fp) return '';
  const hex = fp.toUpperCase().slice(0, 16);
  return hex.match(/.{1,4}/g).join(' ');
}
function sourceName(s) {
  switch (s.type) {
    case 'drive': return 'My Files';
    case 'gdrive': return 'Google Drive backup';
    case 'gphotos-takeout': return 'Google Photos backup';
    case 'device-upload': return 'Device photos (' + s.id + ')';
    case 'icloud-local': return 'iCloud backup';
    default: return s.id;
  }
}
function sourceBlurb(s) {
  switch (s.type) {
    case 'drive': return 'Your own files. Edit them in Finder, the Files app, or here.';
    case 'gdrive': return 'A read-only copy of your Google Drive, refreshed on a schedule.';
    case 'gphotos-takeout': return 'Photos imported from Google Takeout archives placed in the inbox folder.';
    case 'device-upload': return 'Photos and videos backed up from your phone.';
    default: return '';
  }
}
function platformLabel(p) {
  return { ios: 'iPhone / iPad', macos: 'Mac', web: 'Web browser', cli: 'Command line' }[p] || p;
}
function platformIcon(p) {
  return { ios: '📱', macos: '💻', web: '🌐', cli: '⌨️' }[p] || '•';
}
function browserName() {
  const ua = navigator.userAgent;
  let b = 'Browser';
  if (/Edg\//.test(ua)) b = 'Edge';
  else if (/Chrome\//.test(ua)) b = 'Chrome';
  else if (/Firefox\//.test(ua)) b = 'Firefox';
  else if (/Safari\//.test(ua)) b = 'Safari';
  let os = '';
  if (/iPhone|iPad/.test(ua)) os = 'iPhone';
  else if (/Mac OS X/.test(ua)) os = 'Mac';
  else if (/Windows/.test(ua)) os = 'Windows';
  else if (/Android/.test(ua)) os = 'Android';
  else if (/Linux/.test(ua)) os = 'Linux';
  return os ? b + ' on ' + os : b;
}
function fileIcon(f) {
  if (f.isDir) return '📁';
  if (f.media === 'photo') return '🖼️';
  if (f.media === 'video') return '🎬';
  const ext = (f.name.split('.').pop() || '').toLowerCase();
  if (['pdf'].includes(ext)) return '📕';
  if (['doc', 'docx', 'pages', 'txt', 'md', 'rtf'].includes(ext)) return '📝';
  if (['xls', 'xlsx', 'numbers', 'csv'].includes(ext)) return '📊';
  if (['zip', 'tar', 'gz', 'dmg', '7z'].includes(ext)) return '🗜️';
  if (['mp3', 'm4a', 'wav', 'flac', 'aac'].includes(ext)) return '🎵';
  return '📄';
}

// ---------- UI plumbing ----------
let toastTimer;
function toast(msg, kind) {
  const t = $('#toast');
  t.textContent = msg;
  t.className = 'toast' + (kind ? ' ' + kind : '');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.add('hidden'), kind === 'err' ? 6000 : 3500);
}
function setOffline(off) { $('#offline').classList.toggle('hidden', !off); }

// Modal helpers resolve their promise BEFORE closeModal(), because closing
// runs onClose, which resolves the same promise with the cancel value.
let modalCleanup = null;
function openModal(content, { wide = false, onClose } = {}) {
  closeModal();
  const card = $('#modal-card');
  card.className = 'modal-card' + (wide ? ' wide' : '');
  card.replaceChildren(content);
  $('#modal').classList.remove('hidden');
  modalCleanup = onClose || null;
}
function closeModal() {
  $('#modal').classList.add('hidden');
  $('#modal-card').replaceChildren();
  if (modalCleanup) { try { modalCleanup(); } catch {} }
  modalCleanup = null;
}
$('#modal').addEventListener('click', e => { if (e.target === $('#modal')) closeModal(); });
document.addEventListener('keydown', e => { if (e.key === 'Escape') closeModal(); });

function confirmBox(title, text, okLabel = 'OK', danger = false) {
  return new Promise(resolve => {
    openModal(h('div', null,
      h('h2', null, title),
      h('p', null, text),
      h('div', { class: 'modal-actions' },
        h('button', { onclick: () => { resolve(false); closeModal(); } }, 'Cancel'),
        h('button', { class: danger ? 'danger' : 'primary', onclick: () => { resolve(true); closeModal(); } }, okLabel))),
      { onClose: () => resolve(false) });
  });
}
function promptBox(title, label, value = '', okLabel = 'OK') {
  return new Promise(resolve => {
    const input = h('input', { value, autofocus: true });
    const form = h('form', { class: 'stack', onsubmit: e => { e.preventDefault(); resolve(input.value.trim()); closeModal(); } },
      h('h2', null, title),
      h('label', null, label),
      input,
      h('div', { class: 'modal-actions' },
        h('button', { type: 'button', onclick: () => { resolve(null); closeModal(); } }, 'Cancel'),
        h('button', { type: 'submit', class: 'primary' }, okLabel)));
    openModal(form, { onClose: () => resolve(null) });
    setTimeout(() => { input.focus(); input.select(); }, 0);
  });
}

// ---------- sign in ----------
let health = null;
async function loadHealth() {
  try {
    const res = await fetch(API + '/health');
    health = await res.json();
    setOffline(false);
  } catch {
    health = null;
    setOffline(true);
  }
  return health;
}

async function showSignIn(message) {
  $('#shell').classList.add('hidden');
  $('#signin').classList.remove('hidden');
  const msg = $('#signin-msg');
  msg.textContent = message || '';
  msg.classList.toggle('hidden', !message);
  $('#signin-host').textContent = location.hostname;
  await loadHealth();
  $('#signin-key').textContent = health
    ? (health.certFingerprint ? shortKey(health.certFingerprint) : 'none (plain HTTP, not encrypted)')
    : 'server unreachable';
}

$('#pair-form').addEventListener('submit', async e => {
  e.preventDefault();
  const err = $('#pair-error');
  err.classList.add('hidden');
  const code = $('#pair-code').value.replace(/\D/g, '');
  if (code.length !== 6) { err.textContent = 'Enter the 6-digit code.'; err.classList.remove('hidden'); return; }
  const btn = e.target.querySelector('button[type=submit]');
  btn.disabled = true;
  try {
    const r = await api('POST', '/pair', { code, deviceName: browserName(), platform: 'web' });
    if (health && health.certFingerprint && r.certFingerprint && r.certFingerprint !== health.certFingerprint) {
      throw new Error("The server's key changed while pairing. Do not use this server.");
    }
    session.set(r.token, r.deviceId);
    $('#pair-code').value = '';
    await start();
  } catch (ex) {
    err.textContent = friendly(ex);
    err.classList.remove('hidden');
  } finally {
    btn.disabled = false;
  }
});

$('#token-form').addEventListener('submit', async e => {
  e.preventDefault();
  const tok = $('#admin-token').value.trim();
  if (!tok) return;
  session.set(tok, null);
  try {
    await api('GET', '/status');
    $('#admin-token').value = '';
    await start();
  } catch (ex) {
    session.clear();
    const err = $('#pair-error');
    err.textContent = ex.status === 401 ? 'That token was not accepted.' : friendly(ex);
    err.classList.remove('hidden');
  }
});

// ---------- shell + router ----------
let status = null; // last /status
const routes = { overview: renderOverview, files: renderFiles, photos: renderPhotos, devices: renderDevices, activity: renderActivity, settings: renderSettings };
let liveTimer = null;
function stopLive() { if (liveTimer) { clearInterval(liveTimer); liveTimer = null; } }

async function start() {
  $('#signin').classList.add('hidden');
  $('#shell').classList.remove('hidden');
  await loadHealth();
  try { status = await api('GET', '/status'); } catch (e) { if (e.status === 401) return; }
  $('#nav-host').textContent = (status && status.hostname) || location.hostname;
  $('#nav-version').textContent = status ? 'homebackupd ' + status.version : '';
  route();
}

function route() {
  if (!session.token) return;
  const hash = location.hash.replace(/^#\/?/, '') || 'overview';
  const [name, qs] = hash.split('?');
  const params = new URLSearchParams(qs || '');
  const fn = routes[name] || renderOverview;
  document.querySelectorAll('.sidebar a').forEach(a => a.classList.toggle('active', a.dataset.nav === (routes[name] ? name : 'overview')));
  stopLive();
  closeModal();
  const main = $('#main');
  main.replaceChildren(h('p', { class: 'muted' }, 'Loading…'));
  fn(params, main).catch(e => {
    if (e.status === 401) return;
    main.replaceChildren(pageHead(name[0].toUpperCase() + name.slice(1)), h('div', { class: 'error' }, friendly(e)));
  });
}
window.addEventListener('hashchange', route);

function pageHead(title, ...actions) {
  return h('div', { class: 'page-head' }, h('h1', null, title), h('div', { class: 'row' }, actions));
}
function nav(name, params) {
  const qs = params ? '?' + new URLSearchParams(params).toString() : '';
  location.hash = '#/' + name + qs;
}

// ---------- overview ----------
async function renderOverview(params, main) {
  const draw = async () => {
    const [st, devices] = await Promise.all([api('GET', '/status'), api('GET', '/devices').catch(() => [])]);
    status = st;
    $('#nav-host').textContent = st.hostname || location.hostname;
    $('#nav-version').textContent = 'homebackupd ' + st.version;
    const disk = st.disk;
    const frac = disk && disk.total ? disk.used / disk.total : 0;
    const files = st.sources.reduce((a, s) => a + (s.files || 0), 0);
    const bytes = st.sources.reduce((a, s) => a + (s.bytes || 0), 0);
    const problems = st.sources.filter(s => s.lastError);

    const storage = h('div', { class: 'card' },
      h('h3', null, 'Storage'),
      disk ? [
        h('div', { class: 'stat' }, fmtBytes(disk.free), h('span', { class: 'muted', style: 'font-size:15px;font-weight:400' }, ' free of ' + fmtBytes(disk.total))),
        h('div', { class: 'bar' + (frac > 0.9 ? ' full' : '') }, h('div', { style: 'width:' + Math.min(100, frac * 100).toFixed(1) + '%' })),
        h('div', { class: 'hint' }, fmtBytes(disk.used) + ' used on the backup drive · ' + files.toLocaleString() + ' files in ' + fmtBytes(bytes) + ' backed up'),
        frac > 0.9 ? h('p', { class: 'error', style: 'margin-top:8px' }, 'The backup drive is almost full. Free up space or move the root to a bigger drive.') : null,
      ] : h('p', { class: 'muted' }, 'No disk information.'));

    const server = h('div', { class: 'card' },
      h('h3', null, 'Server'),
      h('dl', { class: 'kv' },
        h('dt', null, 'Name'), h('dd', null, st.hostname || location.hostname),
        h('dt', null, 'Address'), h('dd', { class: 'mono' }, location.origin),
        h('dt', null, 'Server key'), h('dd', { class: 'mono' }, health && health.certFingerprint ? shortKey(health.certFingerprint) : 'none (plain HTTP)'),
        h('dt', null, 'Version'), h('dd', null, st.version),
        h('dt', null, 'Running since'), h('dd', null, fmtDate(st.started) + ' (' + fmtAgo(st.started).replace(' ago', '') + ')'),
        h('dt', null, 'Devices'), h('dd', null, h('a', { href: '#/devices' }, devices.length + ' paired'))));

    const sources = h('div', { class: 'card wide' },
      h('div', { class: 'row between' }, h('h3', null, 'Backups'), h('a', { href: '#/files', class: 'small' }, 'Browse files →')),
      st.sources.length === 0 ? h('div', { class: 'empty' }, 'No sources configured. Add them to homebackup.json on the server.') :
      h('table', null,
        h('thead', null, h('tr', null, h('th', null, 'Source'), h('th', { class: 'num' }, 'Files'), h('th', { class: 'num' }, 'Size'), h('th', null, 'Last sync'), h('th', null, 'Status'), h('th', null))),
        h('tbody', null, st.sources.map(s => h('tr', null,
          h('td', null, h('div', null, h('b', null, sourceName(s))), h('div', { class: 'hint' }, sourceBlurb(s))),
          h('td', { class: 'num' }, (s.files || 0).toLocaleString()),
          h('td', { class: 'num' }, fmtBytes(s.bytes || 0)),
          h('td', null, s.lastSync ? h('span', { title: fmtDate(s.lastSync) }, fmtAgo(s.lastSync)) : h('span', { class: 'muted' }, s.type === 'drive' || s.type === 'device-upload' ? '—' : 'never')),
          h('td', null, sourceBadge(s)),
          h('td', { class: 'actions' }, (s.type === 'gdrive' || s.type === 'gphotos-takeout') ? h('button', { class: 'small', disabled: s.running, onclick: async () => {
            try { await api('POST', '/sources/' + encodeURIComponent(s.id) + '/sync'); toast('Sync started for ' + sourceName(s)); setTimeout(draw, 1500); }
            catch (e) { toast(friendly(e), 'err'); }
          } }, 'Sync now') : null))))));

    const next = nextSteps(st, devices);
    main.replaceChildren(
      pageHead('Overview', h('button', { class: 'small', onclick: draw }, 'Refresh')),
      problems.length ? h('div', { class: 'error', style: 'margin-bottom:14px' },
        h('b', null, problems.length === 1 ? 'One backup has a problem. ' : problems.length + ' backups have problems. '),
        problems.map(s => h('div', null, sourceName(s) + ': ' + s.lastError))) : null,
      h('div', { class: 'cards' }, storage, server, sources, next));
  };
  await draw();
  liveTimer = setInterval(() => draw().catch(() => {}), 20000);
}

function sourceBadge(s) {
  if (s.running) return h('span', { class: 'badge busy' }, 'Syncing…');
  if (s.lastError) return h('span', { class: 'badge err', title: s.lastError }, 'Error');
  if (s.type === 'drive') return h('span', { class: 'badge ok' }, 'Ready');
  if (s.type === 'device-upload') return h('span', { class: 'badge ok' }, s.files ? 'Receiving' : 'Waiting for photos');
  if (!s.lastSync) return h('span', { class: 'badge' }, 'Not synced yet');
  return h('span', { class: 'badge ok' }, 'Up to date');
}

function nextSteps(st, devices) {
  const items = [];
  const real = devices.filter(d => d.platform !== 'web');
  items.push(step(true, 'Server is running', 'Version ' + st.version + ' on ' + (st.hostname || location.hostname) + '.'));
  items.push(step(real.length > 0, real.length ? real.length + (real.length === 1 ? ' device paired' : ' devices paired') : 'Pair your Mac and iPhone',
    real.length ? 'Add more under Devices.' : 'Devices → Add a device shows a code to enter in the HomeBackup app.', real.length ? null : () => nav('devices')));
  for (const s of st.sources) {
    if (s.type === 'gphotos-takeout' && !s.files) {
      items.push(step(false, 'Import your Google Photos', 'Download your photos from takeout.google.com and drop the zip files into ' + ((s.options && s.options.inbox) || 'the inbox folder') + ' on the server. They are imported on the next sync.'));
    }
    if (s.type === 'gdrive' && !s.lastSync) {
      items.push(step(false, 'Sign in to Google Drive', 'Run homebackupd -config <file> auth ' + s.id + ' on the server once.'));
    }
    if (s.type === 'drive' && !s.files) {
      items.push(step(false, 'Put something in My Files', 'Open My Files in Finder or the Files app and drop a file in. It shows up here and is kept with history.', () => nav('files', { source: s.id })));
    }
    if (s.type === 'device-upload' && !s.files) {
      items.push(step(false, 'Turn on photo backup on your iPhone', 'In the HomeBackup app, Settings → Back up this device\'s photos.'));
    }
  }
  if (items.every(i => i.done)) items.push(step(true, 'All set', 'Everything is paired and syncing.'));
  return h('div', { class: 'card wide' }, h('h3', null, 'Getting started'),
    h('ul', { class: 'checklist' }, items.map(i => h('li', null,
      h('span', { class: 'mark ' + (i.done ? 'ok' : 'muted') }, i.done ? '✓' : '○'),
      h('div', { class: 'grow' }, h('div', null, h('b', null, i.title)), h('div', { class: 'hint' }, i.text)),
      i.action ? h('button', { class: 'small', onclick: i.action }, 'Open') : null))));
}
function step(done, title, text, action) { return { done, title, text, action }; }

// ---------- files ----------
async function sourcesList() {
  if (!status) status = await api('GET', '/status');
  return status.sources;
}

async function renderFiles(params, main) {
  const sources = await sourcesList();
  if (!sources.length) { main.replaceChildren(pageHead('Files'), h('div', { class: 'empty' }, 'No sources configured.')); return; }
  const srcId = params.get('source') || (sources.find(s => s.type === 'drive') || sources[0]).id;
  const src = sources.find(s => s.id === srcId) || sources[0];
  const path = params.get('path') || '';
  const trash = params.get('trash') === '1';
  const writable = src.type === 'drive';

  const tabs = h('div', { class: 'tabs' }, sources.map(s => h('button', { class: s.id === src.id ? 'active' : '', onclick: () => nav('files', { source: s.id }) }, sourceName(s))));
  const crumbs = h('div', { class: 'crumbs' });
  const parts = path ? path.split('/') : [];
  crumbs.append(h('a', { href: '#', onclick: e => { e.preventDefault(); nav('files', { source: src.id }); } }, sourceName(src)));
  parts.forEach((p, i) => {
    crumbs.append(h('span', { class: 'sep' }, '›'));
    const sub = parts.slice(0, i + 1).join('/');
    crumbs.append(i === parts.length - 1 ? h('b', null, p) : h('a', { href: '#', onclick: e => { e.preventDefault(); nav('files', { source: src.id, path: sub }); } }, p));
  });

  const body = h('div');
  const fileInput = h('input', { type: 'file', multiple: true, style: 'display:none', onchange: () => uploadFiles(src, path, [...fileInput.files], body).then(load) });
  const toolbar = h('div', { class: 'toolbar' },
    trash ? h('button', { class: 'small', onclick: () => nav('files', { source: src.id }) }, '← Back to files') :
      h('button', { class: 'small', onclick: () => nav('files', { source: src.id, trash: '1' }) }, 'Trash' + (src.trashedFiles ? ' (' + src.trashedFiles + ')' : '')),
    writable && !trash ? [
      h('button', { class: 'small', onclick: async () => {
        const name = await promptBox('New folder', 'Folder name', '', 'Create');
        if (!name) return;
        try { await api('POST', '/folders', { source: src.id, path: path ? path + '/' + name : name }); toast('Folder created'); load(); }
        catch (e) { toast(friendly(e), 'err'); }
      } }, 'New folder'),
      h('button', { class: 'small', onclick: () => fileInput.click() }, 'Upload…'),
      fileInput,
    ] : null,
    h('span', { class: 'grow' }),
    h('span', { class: 'hint' }, writable ? 'Changes here show up in Finder and the Files app.' : 'Read-only copy. Change the original in ' + (src.type === 'gdrive' ? 'Google Drive' : src.type === 'gphotos-takeout' ? 'Google Photos' : 'the app') + '.'));

  main.replaceChildren(pageHead('Files'), tabs, crumbs, toolbar, body);

  async function load() {
    body.replaceChildren(h('p', { class: 'muted' }, 'Loading…'));
    if (trash) return loadTrash();
    const r = await api('GET', '/files?source=' + encodeURIComponent(src.id) + '&path=' + encodeURIComponent(path));
    const items = (r.items || []).sort((a, b) => (b.isDir - a.isDir) || a.name.localeCompare(b.name, undefined, { numeric: true }));
    if (!items.length) {
      body.replaceChildren(h('div', { class: 'empty' }, writable ? 'This folder is empty. Drop files here from Finder, or use Upload.' : 'Nothing here yet.'));
      return;
    }
    const media = items.filter(f => f.media).length;
    if (media > 0 && media >= items.filter(f => !f.isDir).length * 0.5) {
      body.replaceChildren(gridView(src, items, writable, load));
    } else {
      body.replaceChildren(listView(src, items, writable, load));
    }
  }
  async function loadTrash() {
    const items = await api('GET', '/trash?source=' + encodeURIComponent(src.id));
    const keep = status && status.retention ? status.retention.trashDays : null;
    const note = h('p', { class: 'hint' }, keep ? 'Deleted files are kept for ' + keep + ' days, then removed for good.' : '');
    if (!items.length) { body.replaceChildren(note, h('div', { class: 'empty' }, 'The trash is empty.')); return; }
    body.replaceChildren(note, h('table', null,
      h('thead', null, h('tr', null, h('th', null, 'Name'), h('th', null, 'Was in'), h('th', { class: 'num' }, 'Size'), h('th', null, 'Deleted'), h('th', null))),
      h('tbody', null, items.map(f => h('tr', null,
        h('td', null, h('div', { class: 'name-cell' }, h('span', { class: 'icon' }, fileIcon(f)), h('span', { class: 'name' }, f.name))),
        h('td', { class: 'muted' }, f.path.includes('/') ? f.path.slice(0, f.path.lastIndexOf('/')) : '(top level)'),
        h('td', { class: 'num' }, f.isDir ? '' : fmtBytes(f.size)),
        h('td', { title: fmtDate(f.deletedAt) }, fmtAgo(f.deletedAt)),
        h('td', { class: 'actions' }, h('button', { class: 'small', onclick: async () => {
          try { await api('POST', '/files/' + encodeURIComponent(f.id) + '/restore', {}); toast('Restored ' + f.name); load(); }
          catch (e) {
            if (e.status === 409) {
              const p = await promptBox('Restore to a new name', 'A file already exists at the original place. Restore as:', f.path, 'Restore');
              if (p) { try { await api('POST', '/files/' + encodeURIComponent(f.id) + '/restore', { path: p }); toast('Restored'); load(); } catch (e2) { toast(friendly(e2), 'err'); } }
            } else toast(friendly(e), 'err');
          }
        } }, 'Restore')))))));
  }
  await load();
}

function listView(src, items, writable, reload) {
  return h('table', null,
    h('thead', null, h('tr', null, h('th', null, 'Name'), h('th', { class: 'num' }, 'Size'), h('th', null, 'Modified'), h('th', null))),
    h('tbody', null, items.map(f => {
      const open = () => f.isDir ? nav('files', { source: src.id, path: f.path }) : preview(f);
      return h('tr', { class: 'clickable' },
        h('td', { onclick: open }, h('div', { class: 'name-cell' }, h('span', { class: 'icon' }, fileIcon(f)), h('span', { class: 'name' }, f.name))),
        h('td', { class: 'num', onclick: open }, f.isDir ? '' : fmtBytes(f.size)),
        h('td', { onclick: open, title: fmtDate(f.modified) }, f.isDir ? '' : fmtAgo(f.modified)),
        h('td', { class: 'actions' }, fileActions(src, f, writable, reload)));
    })));
}

function gridView(src, items, writable, reload) {
  const folders = items.filter(f => f.isDir);
  const files = items.filter(f => !f.isDir);
  const wrap = h('div');
  if (folders.length) wrap.append(h('div', { class: 'chips' }, folders.map(f => h('button', { class: 'small', onclick: () => nav('files', { source: src.id, path: f.path }) }, '📁 ' + f.name))));
  const grid = h('div', { class: 'grid' }, files.map(f => {
    const img = f.media ? h('img', { alt: f.name, loading: 'lazy' }) : null;
    const tile = h('div', { class: 'tile', onclick: () => preview(f, { src, writable, reload, siblings: files }) },
      h('div', { class: 'thumb' }, img || fileIcon(f)),
      h('div', { class: 'cap', title: f.name }, f.name));
    if (img) lazyThumb(img, f.id, 'small');
    return tile;
  }));
  wrap.append(grid);
  return wrap;
}

const thumbObserver = new IntersectionObserver(entries => {
  for (const e of entries) {
    if (!e.isIntersecting) continue;
    thumbObserver.unobserve(e.target);
    const { id, size } = e.target.dataset;
    loadThumb(e.target, id, size);
  }
}, { rootMargin: '300px' });
function lazyThumb(img, id, size) { img.dataset.id = id; img.dataset.size = size; thumbObserver.observe(img); }
async function loadThumb(img, id, size) {
  try {
    const res = await api('GET', '/files/' + encodeURIComponent(id) + '/thumbnail?size=' + size, undefined, { raw: true });
    const blob = await res.blob();
    img.src = URL.createObjectURL(blob);
  } catch {
    img.replaceWith(h('span', null, '🖼️'));
  }
}

function fileActions(src, f, writable, reload) {
  const acts = [];
  if (!f.isDir) {
    acts.push(h('button', { class: 'small', onclick: e => { e.stopPropagation(); download(f); } }, 'Download'));
    acts.push(h('button', { class: 'small', onclick: e => { e.stopPropagation(); showVersions(f, reload); } }, 'Versions'));
  }
  if (writable && f.id) {
    acts.push(h('button', { class: 'small', onclick: async e => {
      e.stopPropagation();
      const name = await promptBox('Rename', 'New name', f.name, 'Rename');
      if (!name || name === f.name) return;
      const dir = f.path.includes('/') ? f.path.slice(0, f.path.lastIndexOf('/') + 1) : '';
      try { await api('PATCH', '/files/' + encodeURIComponent(f.id), { path: dir + name }); toast('Renamed'); reload(); }
      catch (ex) { toast(friendly(ex), 'err'); }
    } }, 'Rename'));
    acts.push(h('button', { class: 'small danger', onclick: async e => {
      e.stopPropagation();
      if (!await confirmBox('Move to trash?', (f.isDir ? 'The folder ' : 'The file ') + f.name + ' moves to the trash, where it is kept for a while and can be restored.', 'Move to trash', true)) return;
      try { await api('DELETE', '/files/' + encodeURIComponent(f.id)); toast('Moved to trash'); reload(); }
      catch (ex) { toast(friendly(ex), 'err'); }
    } }, 'Delete'));
  }
  return acts;
}

async function download(f, version) {
  toast('Downloading ' + f.name + '…');
  try {
    const q = version ? '?version=' + encodeURIComponent(version) : '';
    const res = await api('GET', '/files/' + encodeURIComponent(f.id) + '/content' + q, undefined, { raw: true });
    const blob = await res.blob();
    const url = URL.createObjectURL(blob);
    const a = h('a', { href: url, download: f.name });
    document.body.append(a); a.click(); a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 60000);
  } catch (e) { toast(friendly(e), 'err'); }
}

function preview(f, ctx = {}) {
  const info = h('dl', { class: 'kv' },
    h('dt', null, 'Size'), h('dd', null, fmtBytes(f.size)),
    h('dt', null, 'Modified'), h('dd', null, fmtDate(f.modified)),
    h('dt', null, 'Path'), h('dd', { class: 'mono small' }, f.path),
    f.sha256 ? [h('dt', null, 'SHA-256'), h('dd', { class: 'mono small' }, f.sha256.slice(0, 16) + '…')] : null);
  const box = h('div', { class: 'lightbox' });
  if (f.media) {
    const img = h('img', { alt: f.name });
    box.append(img);
    loadThumb(img, f.id, 'large');
  } else {
    box.append(h('div', { class: 'empty', style: 'font-size:48px' }, fileIcon(f)));
  }
  openModal(h('div', null,
    h('h2', { style: 'overflow-wrap:anywhere' }, f.name),
    box,
    h('div', { style: 'margin-top:12px' }, info),
    h('div', { class: 'modal-actions' },
      h('button', { onclick: () => showVersions(f, ctx.reload) }, 'Versions'),
      h('button', { class: 'primary', onclick: () => download(f) }, 'Download'),
      h('button', { onclick: closeModal }, 'Close'))), { wide: !!f.media });
}

async function showVersions(f, reload) {
  let versions;
  try { versions = await api('GET', '/files/' + encodeURIComponent(f.id) + '/versions'); }
  catch (e) { toast(friendly(e), 'err'); return; }
  const keep = status && status.retention;
  openModal(h('div', null,
    h('h2', null, 'Versions of ' + f.name),
    h('p', { class: 'hint' }, keep ? 'Previous versions are kept for ' + keep.versionDays + ' days (at most ' + keep.maxVersions + ' per file). The current version is always kept.' : ''),
    h('table', null,
      h('thead', null, h('tr', null, h('th', null, 'Saved'), h('th', { class: 'num' }, 'Size'), h('th', null))),
      h('tbody', null,
        h('tr', null, h('td', null, h('b', null, 'Current'), h('span', { class: 'muted' }, ' · ' + fmtDate(f.modified))), h('td', { class: 'num' }, fmtBytes(f.size)), h('td', { class: 'actions' }, h('button', { class: 'small', onclick: () => download(f) }, 'Download'))),
        versions.length ? versions.map(v => h('tr', null,
          h('td', { title: fmtDate(v.created) }, fmtAgo(v.created), h('span', { class: 'muted small' }, ' · edited ' + fmtDate(v.modified))),
          h('td', { class: 'num' }, fmtBytes(v.size)),
          h('td', { class: 'actions' },
            h('button', { class: 'small', onclick: () => download(f, v.id) }, 'Download'),
            h('button', { class: 'small', onclick: async () => {
              if (!await confirmBox('Restore this version?', 'The current content is kept as a new version, so nothing is lost.', 'Restore')) return;
              try { await api('POST', '/files/' + encodeURIComponent(f.id) + '/restore', { version: v.id }); toast('Version restored'); closeModal(); if (reload) reload(); }
              catch (e) { toast(friendly(e), 'err'); }
            } }, 'Restore')))) :
          h('tr', null, h('td', { colspan: 3, class: 'muted' }, 'No previous versions yet. Each edit keeps the version before it.')))),
    h('div', { class: 'modal-actions' }, h('button', { onclick: closeModal }, 'Close'))), { wide: true });
}

async function uploadFiles(src, dir, files, body) {
  if (!files.length) return;
  if (!(window.crypto && crypto.subtle)) { toast('Uploads need a secure (HTTPS) connection.', 'err'); return; }
  const prog = h('div', { class: 'progress' });
  body.prepend(prog);
  let n = 0;
  for (const file of files) {
    n++;
    prog.textContent = 'Uploading ' + file.name + ' (' + n + ' of ' + files.length + ')…';
    try {
      await uploadOne(src, dir, file, pct => { prog.textContent = 'Uploading ' + file.name + ' (' + n + ' of ' + files.length + ') ' + pct + '%'; });
    } catch (e) {
      toast('Could not upload ' + file.name + ': ' + friendly(e), 'err');
    }
  }
  prog.remove();
  toast(files.length === 1 ? 'Uploaded ' + files[0].name : 'Uploaded ' + files.length + ' files');
}

async function uploadOne(src, dir, file, onProgress) {
  const buf = await file.arrayBuffer();
  const digest = await crypto.subtle.digest('SHA-256', buf);
  const sha = [...new Uint8Array(digest)].map(b => b.toString(16).padStart(2, '0')).join('');
  const path = dir ? dir + '/' + file.name : file.name;
  const start = await api('POST', '/uploads', { source: src.id, path, size: file.size, sha256: sha, created: new Date(file.lastModified).toISOString() });
  if (start.status === 'exists') return;
  const id = start.uploadId;
  let offset = start.offset || 0;
  const CHUNK = 8 * 1024 * 1024;
  while (offset < file.size) {
    const chunk = buf.slice(offset, Math.min(offset + CHUNK, file.size));
    await api('PATCH', '/uploads/' + encodeURIComponent(id), chunk, { raw: true, headers: { 'Upload-Offset': String(offset), 'Content-Type': 'application/offset+octet-stream' } });
    offset += chunk.byteLength;
    onProgress(Math.round(offset / file.size * 100));
  }
  await api('POST', '/uploads/' + encodeURIComponent(id) + '/complete');
}

// ---------- photos ----------
async function renderPhotos(params, main) {
  const sources = (await sourcesList()).filter(s => s.type === 'device-upload' || s.type === 'gphotos-takeout' || s.type === 'icloud-local');
  if (!sources.length) {
    main.replaceChildren(pageHead('Photos'), h('div', { class: 'empty' }, 'No photo sources yet. Add a device-upload source for your iPhone, or a gphotos-takeout source for Google Photos.'));
    return;
  }
  const src = sources.find(s => s.id === params.get('source')) || sources[0];
  const path = params.get('path') || '';
  const tabs = h('div', { class: 'tabs' }, sources.map(s => h('button', { class: s.id === src.id ? 'active' : '', onclick: () => nav('photos', { source: s.id }) }, sourceName(s))));
  const parts = path ? path.split('/') : [];
  const crumbs = h('div', { class: 'crumbs' }, h('a', { href: '#', onclick: e => { e.preventDefault(); nav('photos', { source: src.id }); } }, 'All'));
  parts.forEach((p, i) => {
    crumbs.append(h('span', { class: 'sep' }, '›'));
    const sub = parts.slice(0, i + 1).join('/');
    crumbs.append(i === parts.length - 1 ? h('b', null, p) : h('a', { href: '#', onclick: e => { e.preventDefault(); nav('photos', { source: src.id, path: sub }); } }, p));
  });
  const body = h('div', null, h('p', { class: 'muted' }, 'Loading…'));
  main.replaceChildren(pageHead('Photos', h('span', { class: 'hint' }, (src.files || 0).toLocaleString() + ' items · ' + fmtBytes(src.bytes || 0))), tabs, crumbs, body);
  const r = await api('GET', '/files?source=' + encodeURIComponent(src.id) + '&path=' + encodeURIComponent(path));
  const items = (r.items || []);
  const folders = items.filter(f => f.isDir).sort((a, b) => b.name.localeCompare(a.name, undefined, { numeric: true }));
  const media = items.filter(f => !f.isDir).sort((a, b) => new Date(b.modified) - new Date(a.modified));
  body.replaceChildren();
  if (!items.length) {
    body.append(h('div', { class: 'empty' }, src.type === 'device-upload' ? 'No photos yet. Turn on backup in the HomeBackup app on your iPhone.' : 'No photos yet. Put your Takeout zip files in ' + ((src.options && src.options.inbox) || 'the inbox folder') + ' and sync.'));
    return;
  }
  if (folders.length) body.append(h('div', { class: 'chips' }, folders.map(f => h('button', { onclick: () => nav('photos', { source: src.id, path: f.path }) }, f.name))));
  if (media.length) body.append(h('div', { class: 'grid' }, media.map(f => {
    const img = f.media ? h('img', { alt: f.name, loading: 'lazy' }) : null;
    const tile = h('div', { class: 'tile', onclick: () => preview(f) }, h('div', { class: 'thumb' }, img || fileIcon(f)), h('div', { class: 'cap', title: f.name }, f.name));
    if (img) lazyThumb(img, f.id, 'small');
    return tile;
  })));
}

// ---------- devices ----------
async function renderDevices(params, main) {
  const draw = async () => {
    const devices = await api('GET', '/devices');
    const me = session.deviceId;
    main.replaceChildren(
      pageHead('Devices', h('button', { class: 'primary', onclick: addDevice }, 'Add a device')),
      h('p', { class: 'hint' }, 'Every paired phone, Mac, and browser has its own key. Revoke one and it is signed out immediately.'),
      h('div', { class: 'card' }, devices.length === 0 ? h('div', { class: 'empty' }, 'No devices paired yet.') :
        h('table', null,
          h('thead', null, h('tr', null, h('th', null, 'Device'), h('th', null, 'Type'), h('th', null, 'Paired'), h('th', null, 'Last seen'), h('th', null))),
          h('tbody', null, devices.map(d => h('tr', null,
            h('td', null, h('div', { class: 'name-cell' }, h('span', { class: 'icon' }, platformIcon(d.platform)), h('span', { class: 'name' }, h('b', null, d.name), d.id === me ? h('span', { class: 'badge', style: 'margin-left:8px' }, 'this browser') : null))),
            h('td', null, platformLabel(d.platform)),
            h('td', { title: fmtDate(d.created) }, fmtAgo(d.created)),
            h('td', { title: fmtDate(d.lastSeen) }, d.lastSeen ? fmtAgo(d.lastSeen) : h('span', { class: 'muted' }, 'never')),
            h('td', { class: 'actions' }, h('button', { class: 'small danger', onclick: async () => {
              if (!await confirmBox('Revoke ' + d.name + '?', 'That device is signed out right away and must pair again to reconnect. Its backed-up files stay on the server.', 'Revoke', true)) return;
              try { await api('DELETE', '/devices/' + encodeURIComponent(d.id)); toast('Revoked ' + d.name); if (d.id === me) { session.clear(); showSignIn('This browser was revoked.'); } else draw(); }
              catch (e) { toast(friendly(e), 'err'); }
            } }, 'Revoke'))))))));
  };
  await draw();
}

async function addDevice() {
  const codeEl = h('div', { class: 'code-big' }, '…');
  const left = h('div', { class: 'hint', style: 'text-align:center' });
  const err = h('div', { class: 'error hidden' });
  let timer = null, expires = null;
  const tick = () => {
    if (!expires) return;
    const s = Math.max(0, Math.round((expires - Date.now()) / 1000));
    left.textContent = s > 0 ? 'Valid for ' + Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0') : 'This code expired. Get a new one.';
  };
  const fetchCode = async () => {
    err.classList.add('hidden');
    try {
      const r = await api('POST', '/pair/codes');
      codeEl.textContent = r.code.slice(0, 3) + ' ' + r.code.slice(3);
      expires = new Date(r.expiresAt).getTime();
      tick();
    } catch (e) {
      codeEl.textContent = '— — —';
      err.textContent = e.status === 404 ? 'This server is too old to issue codes here. Update homebackupd, or run homebackupd pair on the server.' : friendly(e);
      err.classList.remove('hidden');
    }
  };
  const key = health && health.certFingerprint ? shortKey(health.certFingerprint) : null;
  openModal(h('div', null,
    h('h2', null, 'Add a device'),
    codeEl, left, err,
    key ? h('div', { class: 'keybox' }, h('div', { class: 'label' }, 'Server key'), h('div', { class: 'mono big' }, key), h('div', { class: 'hint' }, 'The new device must show this same key before you enter the code.')) : null,
    h('ol', { class: 'steps' },
      h('li', null, h('b', null, 'iPhone or iPad: '), 'open the HomeBackup app, choose this server, check the key, and enter the code.'),
      h('li', null, h('b', null, 'Mac: '), 'install HomeBackup, click its menu bar icon, then ', h('i', null, 'Connect to your home server…')),
      h('li', null, h('b', null, 'Another browser: '), 'open ', h('span', { class: 'mono' }, location.origin), ' and choose ', h('i', null, 'Pair this browser'), '.')),
    h('div', { class: 'modal-actions' },
      h('button', { onclick: fetchCode }, 'New code'),
      h('button', { class: 'primary', onclick: closeModal }, 'Done'))),
    { onClose: () => clearInterval(timer) });
  timer = setInterval(tick, 1000);
  await fetchCode();
}

// ---------- activity (log) ----------
function parseLogLine(line) {
  // slog text: time=... level=INFO msg="..." k=v ...  or Go's log package: 2026/11/13 00:19:25 message
  const out = { time: '', level: '', msg: '', attrs: [] };
  const m = line.match(/^(\d{4}\/\d{2}\/\d{2} \d{2}:\d{2}:\d{2}) (.*)$/);
  if (m && !line.startsWith('time=')) {
    out.time = m[1].slice(11); out.level = line.includes('error') ? 'WARN' : 'INFO'; out.msg = m[2];
    return out;
  }
  const re = /([A-Za-z_.]+)=("(?:[^"\\]|\\.)*"|\S*)/g;
  let k;
  while ((k = re.exec(line))) {
    let v = k[2];
    if (v.startsWith('"')) { try { v = JSON.parse(v); } catch { v = v.slice(1, -1); } }
    if (k[1] === 'time') out.time = v.replace(/^\d{4}-\d{2}-\d{2}T/, '').replace(/\.\d+/, '').replace(/[+-]\d{2}:\d{2}$/, '');
    else if (k[1] === 'level') out.level = v;
    else if (k[1] === 'msg') out.msg = v;
    else out.attrs.push(k[1] + '=' + v);
  }
  if (!out.msg && !out.attrs.length) out.msg = line;
  return out;
}

async function renderActivity(params, main) {
  const filter = h('input', { placeholder: 'Filter…', style: 'max-width:260px' });
  const hideHttp = h('input', { type: 'checkbox', checked: true });
  const live = h('input', { type: 'checkbox', checked: true });
  const box = h('div', { class: 'log' });
  let lines = [];
  const draw = () => {
    const q = filter.value.toLowerCase();
    const rows = [];
    for (const line of lines) {
      const p = parseLogLine(line);
      if (hideHttp.checked && p.msg === 'http' && !p.attrs.some(a => /^status=[45]/.test(a))) continue;
      if (q && !line.toLowerCase().includes(q)) continue;
      rows.push(h('div', { class: 'line' }, h('span', { class: 't' }, p.time), h('span', { class: 'lvl ' + p.level }, p.level), h('span', null, p.msg, p.attrs.length ? h('span', { class: 'attrs' }, '  ' + p.attrs.join(' ')) : null)));
    }
    const atBottom = box.scrollTop + box.clientHeight >= box.scrollHeight - 20;
    box.replaceChildren(...(rows.length ? rows : [h('div', { class: 'empty' }, lines.length ? 'Nothing matches.' : 'No log lines yet.')]));
    if (atBottom) box.scrollTop = box.scrollHeight;
  };
  const load = async () => { const r = await api('GET', '/log?n=500'); lines = r.lines || []; draw(); };
  filter.addEventListener('input', draw);
  hideHttp.addEventListener('change', draw);
  main.replaceChildren(
    pageHead('Activity', h('label', { class: 'row' }, live, 'Live'), h('label', { class: 'row' }, hideHttp, 'Hide routine requests'), filter, h('button', { class: 'small', onclick: load }, 'Refresh')),
    h('p', { class: 'hint' }, 'The most recent server log lines. The full log is on the server (macOS: ~/Library/Logs/homebackupd.log; Linux: journalctl -u homebackupd).'),
    box);
  await load();
  box.scrollTop = box.scrollHeight;
  liveTimer = setInterval(() => { if (live.checked) load().catch(() => {}); }, 5000);
}

// ---------- settings ----------
async function renderSettings(params, main) {
  const st = await api('GET', '/status');
  status = st;
  const ret = st.retention || {};
  const copyBtn = (text) => h('button', { class: 'small', onclick: () => { navigator.clipboard && navigator.clipboard.writeText(text).then(() => toast('Copied')); } }, 'Copy');
  main.replaceChildren(
    pageHead('Settings'),
    h('div', { class: 'cards' },
      h('div', { class: 'card' },
        h('h3', null, 'This server'),
        h('dl', { class: 'kv' },
          h('dt', null, 'Listens on'), h('dd', { class: 'mono' }, st.listen || '', st.tls ? ' (HTTPS)' : ' (plain HTTP)'),
          h('dt', null, 'Discovery'), h('dd', null, st.bonjour ? 'On: apps find this server by name on the home network' : 'Off: enter the address by hand in the apps'),
          h('dt', null, 'Backup root'), h('dd', { class: 'mono' }, st.root),
          h('dt', null, 'Server key'), h('dd', { class: 'row' }, h('span', { class: 'mono' }, health && health.certFingerprint ? shortKey(health.certFingerprint) : 'none'), health && health.certFingerprint ? copyBtn(shortKey(health.certFingerprint)) : null),
          h('dt', null, 'Version'), h('dd', null, st.version))),
      h('div', { class: 'card' },
        h('h3', null, 'History'),
        h('dl', { class: 'kv' },
          h('dt', null, 'Trash'), h('dd', null, 'Deleted files are kept for ' + (ret.trashDays ?? '?') + ' days'),
          h('dt', null, 'Versions'), h('dd', null, 'Previous versions are kept for ' + (ret.versionDays ?? '?') + ' days, at most ' + (ret.maxVersions ?? '?') + ' per file')),
        h('p', { class: 'hint', style: 'margin-top:10px' }, 'Change these under "retention" in homebackup.json on the server.')),
      h('div', { class: 'card wide' },
        h('h3', null, 'Backup sources'),
        h('table', null,
          h('thead', null, h('tr', null, h('th', null, 'Name'), h('th', null, 'ID'), h('th', null, 'Type'), h('th', null, 'Schedule'), h('th', null, 'Options'))),
          h('tbody', null, st.sources.map(s => h('tr', null,
            h('td', null, sourceName(s)), h('td', { class: 'mono' }, s.id), h('td', { class: 'mono' }, s.type),
            h('td', null, s.schedule ? 'every ' + s.schedule.replace(/0m0s$|0s$/, '') : (s.type === 'drive' || s.type === 'device-upload' ? 'as changes arrive' : 'manual')),
            h('td', { class: 'mono small' }, s.options ? Object.entries(s.options).map(([k, v]) => k + ': ' + v).join(', ') : ''))))),
        h('p', { class: 'hint', style: 'margin-top:10px' }, 'Sources live in homebackup.json on the server. After editing it, restart homebackupd: on macOS ', h('code', null, 'launchctl kickstart -k gui/$(id -u)/com.homebackup.homebackupd'), ', on Linux ', h('code', null, 'sudo systemctl restart homebackupd'), '.')),
      h('div', { class: 'card wide' },
        h('h3', null, 'This browser'),
        h('p', null, 'This browser is paired as a device of its own. Signing out removes its key from the server.'),
        h('div', { class: 'row' },
          h('button', { class: 'danger', onclick: async () => {
            if (!await confirmBox('Sign out of this browser?', 'You will need a new pairing code to come back.', 'Sign out', true)) return;
            const me = session.deviceId;
            if (me) { try { await api('DELETE', '/devices/' + encodeURIComponent(me)); } catch {} }
            session.clear();
            showSignIn('Signed out.');
          } }, 'Sign out'),
          h('a', { class: 'btn', href: 'https://github.com/LeoHChen/homebackup#readme', target: '_blank', rel: 'noopener' }, 'Documentation')))));
}

// ---------- boot ----------
(async function boot() {
  if (session.token) await start(); else await showSignIn();
})();
