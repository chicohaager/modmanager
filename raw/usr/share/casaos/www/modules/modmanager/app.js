'use strict';

// The gateway forwards module calls with the path intact; the backend
// listens under /modmanager.
const API_BASE = '/modmanager';
const MODULES_API = '/v2/mod_management/modules';

const $ = (sel) => document.querySelector(sel);

function safeGet(k) { try { return localStorage.getItem(k); } catch { return null; } }
function safeSet(k, v) { try { localStorage.setItem(k, v); } catch { /* private window */ } }

function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

class ApiError extends Error {
  constructor(status, code, message) { super(message); this.status = status; this.code = code; }
}

// Same session handling as the other Lintux modules: the shell keeps the
// token in localStorage; a 401 goes through POST /v1/users/refresh once.
async function api(path, opts = {}, retried = false) {
  const headers = { Accept: 'application/json', ...(opts.headers || {}) };
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json';
  const token = safeGet('access_token');
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = await fetch(path, { ...opts, headers, body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined });
  if (res.status === 401 && !retried && await refreshSession()) return api(path, opts, true);
  const isJson = (res.headers.get('content-type') || '').includes('application/json');
  const data = isJson ? await res.json().catch(() => ({})) : await res.text();
  if (!res.ok) throw new ApiError(res.status, (data && data.code) || 'http', (data && (data.error || data.message)) || `HTTP ${res.status}`);
  return data;
}

let refreshing = null;
function refreshSession() {
  if (refreshing) return refreshing;
  refreshing = (async () => {
    const rt = safeGet('refresh_token');
    if (!rt) return false;
    try {
      const res = await fetch('/v1/users/refresh', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ refresh_token: rt }) });
      const body = res.ok ? await res.json() : null;
      const d = body && body.data;
      if (!d || !d.access_token) return false;
      safeSet('access_token', d.access_token);
      if (d.refresh_token) safeSet('refresh_token', d.refresh_token);
      if (d.expires_at !== undefined) safeSet('expires_at', String(d.expires_at));
      return true;
    } catch {
      return false;
    } finally {
      setTimeout(() => { refreshing = null; }, 0);
    }
  })();
  return refreshing;
}

function setStatus(msg, kind) {
  const el = $('#status');
  el.textContent = msg || '';
  el.className = kind || '';
}

function describe(err) {
  if (err instanceof ApiError && err.status === 401) return 'Session expired — reload the ZimaOS page and sign in again.';
  return err.message || String(err);
}

// ---------- theme ----------

function applyTheme(t) {
  if (t === 'light' || t === 'dark') document.documentElement.setAttribute('data-theme', t);
  else document.documentElement.removeAttribute('data-theme');
}

function initTheme() {
  const sel = $('#theme-select');
  const stored = safeGet('modmanager.theme');
  sel.value = stored === 'light' || stored === 'dark' ? stored : 'auto';
  sel.addEventListener('change', () => {
    safeSet('modmanager.theme', sel.value);
    applyTheme(sel.value);
  });
}

// ---------- built-in apps ----------

const KIND_LABEL = { sysext: 'system extension', base: 'system image', system: 'system tile' };
let apps = [];
let busy = false;

function fmtBytes(n) {
  if (!n) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.min(u.length - 1, Math.floor(Math.log(n) / Math.log(1024)));
  return `${(n / 1024 ** i).toFixed(i ? 1 : 0)} ${u[i]}`;
}

function iconHTML(a) {
  const letter = esc((a.title || a.name || '?').charAt(0).toUpperCase());
  // A hidden sysext is unmerged, so its icon file is gone with it.
  if (!a.icon || (a.hidden && a.kind === 'sysext')) return `<span class="app-icon">${letter}</span>`;
  return `<img class="app-icon" src="${esc(a.icon)}" alt="" data-letter="${letter}">`;
}

function stateHTML(a) {
  if (a.blocked && !a.hidden) return '<span class="pill pill-warn">Locked</span>';
  return a.hidden ? '<span class="pill pill-off">Hidden</span>' : '<span class="pill pill-ok">Visible</span>';
}

function cardHTML(a) {
  const svc = a.services.map((s) => {
    const cls = s.masked ? 'pill-off' : s.active === 'active' ? 'pill-ok' : 'pill-off';
    const txt = s.masked ? 'masked' : (s.active || 'unknown');
    return `<span class="pill ${cls}" title="${esc(s.unit)}">${esc(s.unit.replace(/\.service$/, ''))}: ${esc(txt)}</span>`;
  }).join('');
  const data = a.data && a.data.exists
    ? `<span class="pill pill-accent" title="${esc(a.data.path)}">data ${esc(fmtBytes(a.data.bytes))}</span>` : '';
  const exp = a.experimental
    ? '<span class="pill pill-warn" title="Writes into the ZimaOS user database (internal, may change with any ZimaOS update). A backup is taken before every change.">experimental</span>' : '';
  const blocked = a.blocked ? `<div class="app-sub">${esc(a.blocked)}</div>` : '';
  let actions;
  if (a.hidden) {
    actions = `<button class="btn-primary btn-small" data-act="restore" data-id="${esc(a.id)}">Restore</button>`;
    if (a.data && a.data.exists) actions += `<button class="btn-danger btn-small" data-act="purge" data-id="${esc(a.id)}">Delete data</button>`;
  } else {
    actions = `<button class="btn-small" data-act="hide" data-id="${esc(a.id)}"${a.blocked ? ' disabled' : ''}>Hide</button>`;
  }
  return `<div class="card${a.hidden ? ' is-hidden' : ''}">
    <div class="app-head">${iconHTML(a)}
      <div class="app-text"><div class="app-title">${esc(a.title)}</div><div class="app-sub mono">${esc(a.name)} · ${esc(KIND_LABEL[a.kind] || a.kind)}</div></div>
      ${stateHTML(a)}
    </div>
    ${blocked}
    <div class="app-meta">${exp}${svc}${data}</div>
    <div class="app-actions">${actions}</div>
  </div>`;
}

function renderApps() {
  const el = $('#apps');
  if (!apps.length) {
    el.innerHTML = '<div class="loading">No built-in apps found.</div>';
    return;
  }
  el.innerHTML = apps.map(cardHTML).join('');
  el.querySelectorAll('img.app-icon').forEach((img) => {
    img.addEventListener('error', () => {
      const s = document.createElement('span');
      s.className = 'app-icon';
      s.textContent = img.dataset.letter || '?';
      img.replaceWith(s);
    }, { once: true });
  });
  el.querySelectorAll('button[data-act]').forEach((b) => {
    b.disabled = b.disabled || busy;
  });
}

async function loadApps() {
  const data = await api(`${API_BASE}/apps`);
  apps = data.apps || [];
  renderApps();
}

async function act(action, id, confirm) {
  const a = apps.find((x) => x.id === id);
  const name = a ? a.title : id;
  busy = true;
  renderApps();
  setStatus(`${action === 'hide' ? 'Hiding' : action === 'restore' ? 'Restoring' : 'Deleting data of'} ${name} — ZimaOS re-merges its system extensions, this takes a few seconds…`);
  try {
    const data = await api(`${API_BASE}/apps/${action}`, { method: 'POST', body: { id, confirm } });
    apps = data.apps || [];
    const done = { hide: 'hidden', restore: 'restored', purge: 'data deleted' }[action];
    setStatus(`${name}: ${done}. Reload the ZimaOS dashboard to see the change.`, 'ok');
  } catch (err) {
    setStatus(`${name}: ${describe(err)}`, 'err');
    try { await loadApps(); } catch { /* keep the message above */ }
  } finally {
    busy = false;
    renderApps();
  }
}

// ---------- purge dialog ----------

let purgeID = null;

function openPurge(id) {
  const a = apps.find((x) => x.id === id);
  if (!a || !a.data) return;
  purgeID = id;
  $('#purge-text').textContent = `This permanently deletes ${a.data.path} (${fmtBytes(a.data.bytes)}): the app's index, settings and models. Your own files elsewhere are not touched; the deletion is refused if a photo or video is found inside.`;
  $('#purge-name').textContent = a.name;
  $('#purge-input').value = '';
  $('#purge-ok').disabled = true;
  $('#purge-modal').hidden = false;
  $('#purge-input').focus();
}

function closePurge() {
  $('#purge-modal').hidden = true;
  purgeID = null;
}

// ---------- installed modules ----------

const BUILTIN = new Set(['icewhale_files', 'icewhale_peerdrop', 'icewhale_photos', 'zimaos_zvm', 'doconverter', 'zimanet']);

async function loadModules() {
  const data = await api(MODULES_API);
  const list = Array.isArray(data.data) ? data.data : [];
  list.sort((x, y) => String(x.name).localeCompare(String(y.name)));
  const rows = list.map((m) => {
    const t = (m.ui && m.ui.title && (m.ui.title.en_us || Object.values(m.ui.title)[0])) || '';
    const svcs = (m.services || []).map((s) => esc(s.name)).join(', ') || '<span class="muted">—</span>';
    const when = m.created_at ? new Date(m.created_at * 1000).toLocaleDateString() : '—';
    const src = BUILTIN.has(m.name) ? '<span class="pill pill-off">ZimaOS</span>' : '<span class="pill pill-accent">added</span>';
    return `<tr><td class="mono">${esc(m.name)}</td><td>${esc(t)}</td><td class="mono">${svcs}</td><td>${esc(when)}</td><td>${src}</td></tr>`;
  });
  $('#mods').innerHTML = rows.join('') || '<tr><td colspan="5" class="loading">No modules reported.</td></tr>';
}

// ---------- catalog ----------

let catalog = { entries: [], repos: [] };
let armed = null; // name whose uninstall waits for the second click

function stateBadge(e) {
  if (!e.installed) return e.installer === 'own' ? '<span class="pill pill-off">Own installer</span>' : '<span class="pill pill-off">Not installed</span>';
  const iv = e.installed_version;
  const rel = esc(e.version || 'release');
  if (iv) {
    // The version comes from the running module and is compared semver-style.
    if (e.state === 'current') return `<span class="pill pill-ok">Up to date · ${esc(iv)}</span>`;
    if (e.state === 'update') return `<span class="pill pill-warn">Update ${esc(iv)} → ${rel}</span>`;
    if (e.state === 'newer') return `<span class="pill pill-accent" title="A development build newer than the latest release.">${esc(iv)} · newer than ${rel}</span>`;
  }
  if (e.state === 'current') return '<span class="pill pill-ok">Up to date</span>';
  // "update" only says the installed image is not byte-identical with the
  // latest release — it may be older or a newer development build.
  if (e.state === 'update') return `<span class="pill pill-warn" title="The installed image is not the release image; it may be older or a development build.">Differs from ${esc(e.version || 'release')}</span>`;
  return '<span class="pill pill-ok">Installed</span>';
}

function catalogCardHTML(e) {
  const sub = [e.name, e.version].filter(Boolean).map(esc).join(' · ');
  const repoLink = e.repo ? `<a class="app-sub mono" href="https://github.com/${esc(e.repo)}" target="_blank" rel="noopener noreferrer">${esc(e.repo)}</a>` : '';
  const err = e.error ? `<div class="app-err">${esc(e.error)}</div>` : '';
  const note = e.note ? `<div class="app-sub">${esc(e.note)}</div>` : '';
  let body = '';
  let actions = '';
  if (e.installer === 'own' && e.command) {
    body = `<div class="root-note"><span class="pill pill-root">root</span> Must run as root: open the ZimaOS terminal, log in with your ZimaOS user and paste the command — <code>sudo</code> asks for your ZimaOS password.</div>
      <div class="cmd"><code>${esc(e.command)}</code><button class="btn-small" data-copy="${esc(e.command)}">Copy</button></div>
      <div class="app-sub">Needs its own installer (it sets up more than the image).</div>`;
  } else if (e.installer === 'zpkg') {
    if (!e.error && !e.protected) {
      const rel = esc(e.version || 'release');
      if (!e.installed) actions += `<button class="btn-primary btn-small" data-cat="install" data-id="${esc(e.id)}">Install</button>`;
      else if (e.state === 'update') actions += `<button class="btn-primary btn-small" data-cat="install" data-id="${esc(e.id)}">${e.installed_version ? 'Update to' : 'Install'} ${rel}</button>`;
      // "newer": no button — it would downgrade a development build to the release.
      else if (e.state !== 'newer') actions += `<button class="btn-small" data-cat="install" data-id="${esc(e.id)}">Reinstall</button>`;
    }
    if (e.installed && !e.protected) {
      const label = armed === e.name ? 'Click again to uninstall' : 'Uninstall';
      actions += `<button class="btn-danger btn-small" data-cat="uninstall" data-name="${esc(e.name)}">${label}</button>`;
    }
  }
  if (e.source === 'repo') actions += `<button class="btn-small" data-cat="remove-repo" data-repo="${esc(e.repo)}" title="Remove from this list (an installed module stays installed)">Remove from list</button>`;
  return `<div class="card">
    <div class="app-head"><span class="app-icon">${esc((e.title || e.name || '?').charAt(0).toUpperCase())}</span>
      <div class="app-text"><div class="app-title">${esc(e.title || e.name)}</div><div class="app-sub mono">${sub}</div>${repoLink}</div>
      <div class="app-badges">${e.source === 'repo' ? '<span class="pill pill-warn">third-party</span>' : ''}${e.error && !e.installed ? '' : stateBadge(e)}</div>
    </div>
    ${e.source === 'repo' ? '<div class="trust-note">Third-party repository you added: the checksum only proves the download is intact, not that the author is trustworthy. The module would run as root.</div>' : ''}
    ${note}${err}${body}
    <div class="app-actions">${actions}</div>
  </div>`;
}

function renderCatalog() {
  // Presets and repositories the user added share one section.
  $('#cat-github').innerHTML = catalog.entries.map(catalogCardHTML).join('') || '<div class="empty">No GitHub modules.</div>';
  document.querySelectorAll('#tab-catalog button[data-cat], #repo-form button').forEach((b) => { b.disabled = busy; });
}

async function loadCatalog() {
  const data = await api(`${API_BASE}/catalog`);
  catalog = { entries: data.entries || [], repos: data.repos || [] };
  renderCatalog();
}

async function catalogAction(path, body, what) {
  busy = true;
  renderCatalog();
  setStatus(`${what}…`);
  try {
    const data = await api(`${API_BASE}/catalog/${path}`, { method: 'POST', body });
    catalog = { entries: data.entries || [], repos: data.repos || [] };
    setStatus(`${what}: done.`, 'ok');
    loadModules().catch(() => {});
    return true;
  } catch (err) {
    setStatus(`${what}: ${describe(err)}`, 'err');
    return false;
  } finally {
    busy = false;
    armed = null;
    renderCatalog();
  }
}

// ---------- self-update ----------

let selfTarget = null; // newer release tag, when there is one
let selfArmed = false; // the update waits for a second click

async function loadSelf() {
  const s = await api(`${API_BASE}/self`);
  const b = $('#self-update-btn');
  selfTarget = s.update ? s.latest : null;
  b.hidden = !selfTarget;
  if (selfTarget) b.textContent = `Update to ${selfTarget}`;
}

// The backend downloads and checks the release, then a transient unit swaps the module:
// Module Manager is gone for a few seconds. Wait until /health answers with the new version.
async function selfUpdate() {
  const b = $('#self-update-btn');
  if (!selfTarget) return;
  if (!selfArmed) {
    selfArmed = true;
    b.textContent = `Click again: update to ${selfTarget}`;
    setTimeout(() => { selfArmed = false; if (selfTarget) b.textContent = `Update to ${selfTarget}`; }, 4000);
    return;
  }
  selfArmed = false;
  b.disabled = true;
  setStatus(`Downloading and checking ${selfTarget}…`);
  try {
    const r = await api(`${API_BASE}/self/update`, { method: 'POST', body: {} });
    setStatus(`Updating to ${r.to} — Module Manager restarts; this page reloads when it answers again…`);
    const want = r.to.replace(/^v/, '');
    let away = false;
    const until = Date.now() + 180000;
    while (Date.now() < until) {
      await new Promise((ok) => setTimeout(ok, 3000));
      let h = null;
      try { h = await (await fetch(`${API_BASE}/health`, { cache: 'no-store' })).json(); } catch { away = true; continue; }
      if (h && h.version === want) { location.reload(); return; }
      if (h && away) {
        setStatus(`The update to ${r.to} failed; ${h.version} is installed again. Log: /DATA/AppData/modmanager/self-update/log`, 'err');
        return;
      }
    }
    setStatus(`No answer from Module Manager after 3 minutes. Log: /DATA/AppData/modmanager/self-update/log`, 'err');
  } catch (e) {
    setStatus(describe(e), 'err');
  } finally {
    b.disabled = false;
  }
}

// ---------- wiring ----------

async function refreshAll() {
  setStatus('Loading…');
  const results = await Promise.allSettled([loadApps(), loadModules(), loadCatalog()]);
  const failed = results.filter((r) => r.status === 'rejected');
  if (failed.length) setStatus(failed.map((r) => describe(r.reason)).join(' · '), 'err');
  else setStatus('');
}

document.addEventListener('DOMContentLoaded', () => {
  initTheme();
  document.querySelectorAll('.tab-btn').forEach((b) => b.addEventListener('click', () => {
    document.querySelectorAll('.tab-btn').forEach((x) => x.classList.toggle('active', x === b));
    document.querySelectorAll('.tab-panel').forEach((p) => p.classList.toggle('active', p.id === `tab-${b.dataset.tab}`));
  }));
  $('#apps').addEventListener('click', (e) => {
    const b = e.target.closest('button[data-act]');
    if (!b || b.disabled) return;
    if (b.dataset.act === 'purge') openPurge(b.dataset.id);
    else act(b.dataset.act, b.dataset.id);
  });
  $('#purge-input').addEventListener('input', () => {
    const a = apps.find((x) => x.id === purgeID);
    $('#purge-ok').disabled = !a || $('#purge-input').value !== a.name;
  });
  $('#purge-cancel').addEventListener('click', closePurge);
  $('#purge-ok').addEventListener('click', () => {
    const id = purgeID;
    const confirm = $('#purge-input').value;
    closePurge();
    act('purge', id, confirm);
  });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && !$('#purge-modal').hidden) closePurge(); });
  $('#refresh-btn').addEventListener('click', refreshAll);
  $('#self-update-btn').addEventListener('click', selfUpdate);
  loadSelf().catch(() => {}); // no answer about updates is not an error of this page
  $('#repo-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const repo = $('#repo-input').value.trim();
    if (!repo) return;
    if (await catalogAction('repos', { repo }, `Adding ${repo}`)) $('#repo-input').value = '';
  });
  $('#tab-catalog').addEventListener('click', async (e) => {
    const copy = e.target.closest('button[data-copy]');
    if (copy) {
      try { await navigator.clipboard.writeText(copy.dataset.copy); setStatus('Command copied.', 'ok'); } catch { setStatus('Copy failed — select the command and copy it by hand.', 'err'); }
      return;
    }
    const b = e.target.closest('button[data-cat]');
    if (!b || b.disabled) return;
    const entry = catalog.entries.find((x) => x.id === b.dataset.id || x.name === b.dataset.name);
    const title = entry ? (entry.title || entry.name) : (b.dataset.repo || '');
    if (b.dataset.cat === 'install') {
      catalogAction('install', { id: b.dataset.id }, `Installing ${title} (download, checksum, zpkg)`);
    } else if (b.dataset.cat === 'uninstall') {
      if (armed !== b.dataset.name) {
        armed = b.dataset.name;
        renderCatalog();
        setTimeout(() => { if (armed === b.dataset.name) { armed = null; renderCatalog(); } }, 5000);
        return;
      }
      catalogAction('uninstall', { name: b.dataset.name }, `Uninstalling ${title}`);
    } else if (b.dataset.cat === 'remove-repo') {
      catalogAction('repos/remove', { repo: b.dataset.repo }, `Removing ${b.dataset.repo} from the list`);
    }
  });
  refreshAll();
});
