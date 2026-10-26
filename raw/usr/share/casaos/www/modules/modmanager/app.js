'use strict';
// Texts come from i18n.js (t, th, applyStaticTexts, initLang), loaded before this file.

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

// detail: what the server said after the part the code already names (the file list of a
// collision, the cause of a failure) — technical detail stays in the server's words.
function detailOf(code, message) {
  const m = String(message || '');
  if (code === 'collision') return m.split('not installed: ').pop();
  const i = m.indexOf(': ');
  return i >= 0 && code !== 'failed' && code !== 'internal' ? m.slice(i + 2) : m;
}

// A text for an error code of the API or of a catalog entry; unknown codes keep the server's message.
function codeText(code, message) {
  const key = `code.${code}`;
  if (code && TEXTS.en[key]) return t(key, { detail: detailOf(code, message) });
  return message || code;
}

function describe(err) {
  if (err instanceof ApiError && err.status === 401) return t('err.session');
  if (err instanceof ApiError) return codeText(err.code, err.message);
  return err.message || String(err);
}

// ---------- theme and language ----------

function applyTheme(th_) {
  if (th_ === 'light' || th_ === 'dark') document.documentElement.setAttribute('data-theme', th_);
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

// The backend has no user token for the ZimaOS language: the page tells it, so the
// dashboard card about module updates speaks the same language. Failure changes nothing here.
function reportLang() {
  api(`${API_BASE}/ui-lang`, { method: 'POST', body: { lang: LANG } }).catch(() => {});
}

function initLangSelect() {
  const sel = $('#lang-select');
  const stored = safeGet('modmanager.lang');
  sel.value = stored === 'en' || stored === 'de' ? stored : 'auto';
  sel.addEventListener('change', async () => {
    safeSet('modmanager.lang', sel.value);
    await initLang();
    reportLang();
    applyStaticTexts();
    renderApps();
    renderCatalog();
    loadModules().catch(() => {});
    if (selfTarget) $('#self-update-btn').textContent = t('self.update', { v: selfTarget });
  });
}

// ---------- built-in apps ----------

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
  if (a.blocked && !a.hidden) return `<span class="pill pill-warn">${esc(t('pill.locked'))}</span>`;
  return a.hidden ? `<span class="pill pill-off">${esc(t('pill.hidden'))}</span>` : `<span class="pill pill-ok">${esc(t('pill.visible'))}</span>`;
}

// The reason an app cannot be hidden: by code, the server's words as the tooltip.
function blockedText(a) {
  return a.blocked_code && TEXTS.en[`blocked.${a.blocked_code}`] ? t(`blocked.${a.blocked_code}`) : a.blocked;
}

function cardHTML(a) {
  const svc = a.services.map((s) => {
    const cls = s.masked ? 'pill-off' : s.active === 'active' ? 'pill-ok' : 'pill-off';
    const state = s.active && TEXTS.en[`svc.${s.active}`] ? t(`svc.${s.active}`) : s.active; // systemd's word when unknown
    const txt = s.masked ? t('svc.masked') : (state || t('svc.unknown'));
    return `<span class="pill ${cls}" title="${esc(s.unit)}">${esc(s.unit.replace(/\.service$/, ''))}: ${esc(txt)}</span>`;
  }).join('');
  const data = a.data && a.data.exists
    ? `<span class="pill pill-accent" title="${esc(a.data.path)}">${esc(t('pill.data', { size: fmtBytes(a.data.bytes) }))}</span>` : '';
  const exp = a.experimental
    ? `<span class="pill pill-warn" title="${esc(t('pill.experimental.title'))}">${esc(t('pill.experimental'))}</span>` : '';
  const blocked = a.blocked ? `<div class="app-sub" title="${esc(a.blocked)}">${esc(blockedText(a))}</div>` : '';
  let actions;
  if (a.hidden) {
    actions = `<button class="btn-primary btn-small" data-act="restore" data-id="${esc(a.id)}">${esc(t('btn.restore'))}</button>`;
    if (a.data && a.data.exists) actions += `<button class="btn-danger btn-small" data-act="purge" data-id="${esc(a.id)}">${esc(t('btn.purge'))}</button>`;
  } else {
    actions = `<button class="btn-small" data-act="hide" data-id="${esc(a.id)}"${a.blocked ? ' disabled' : ''}>${esc(t('btn.hide'))}</button>`;
  }
  return `<div class="card${a.hidden ? ' is-hidden' : ''}">
    <div class="app-head">${iconHTML(a)}
      <div class="app-text"><div class="app-title">${esc(a.title)}</div><div class="app-sub mono">${esc(a.name)} · ${esc(TEXTS.en[`kind.${a.kind}`] ? t(`kind.${a.kind}`) : a.kind)}</div></div>
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
    el.innerHTML = `<div class="loading">${esc(t('apps.none'))}</div>`;
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
  setStatus(t(`act.${action}`, { name }));
  try {
    const data = await api(`${API_BASE}/apps/${action}`, { method: 'POST', body: { id, confirm } });
    apps = data.apps || [];
    setStatus(t(`done.${action}`, { name }), 'ok');
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
  $('#purge-text').textContent = t('purge.text', { path: a.data.path, size: fmtBytes(a.data.bytes) });
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
    const titles = (m.ui && m.ui.title) || {};
    const ti = titles[LANG === 'de' ? 'de_de' : 'en_us'] || titles.en_us || Object.values(titles)[0] || '';
    const svcs = (m.services || []).map((s) => esc(s.name)).join(', ') || '<span class="muted">—</span>';
    const when = m.created_at ? new Date(m.created_at * 1000).toLocaleDateString(LANG === 'de' ? 'de-DE' : undefined) : '—';
    const src = BUILTIN.has(m.name) ? '<span class="pill pill-off">ZimaOS</span>' : `<span class="pill pill-accent">${esc(t('mods.added'))}</span>`;
    return `<tr><td class="mono">${esc(m.name)}</td><td>${esc(ti)}</td><td class="mono">${svcs}</td><td>${esc(when)}</td><td>${src}</td></tr>`;
  });
  $('#mods').innerHTML = rows.join('') || `<tr><td colspan="5" class="loading">${esc(t('mods.none'))}</td></tr>`;
}

// ---------- catalog ----------

let catalog = { entries: [], repos: [] };
let armed = null; // name whose uninstall waits for the second click

function pill(cls, text, title) {
  return `<span class="pill ${cls}"${title ? ` title="${esc(title)}"` : ''}>${esc(text)}</span>`;
}

function stateBadge(e) {
  if (!e.installed) return pill('pill-off', e.installer === 'own' ? t('badge.own') : t('badge.notInstalled'));
  const iv = e.installed_version;
  const rel = e.version || t('word.release');
  if (iv) {
    // The version comes from the running module and is compared semver-style.
    if (e.state === 'current') return pill('pill-ok', t('badge.current', { v: iv }));
    if (e.state === 'update') return pill('pill-warn', t('badge.update', { iv, rel }));
    if (e.state === 'newer') return pill('pill-accent', t('badge.newer', { iv, rel }), t('badge.newer.title'));
  }
  if (e.state === 'current') return pill('pill-ok', t('badge.currentPlain'));
  // "update" only says the installed image is not byte-identical with the
  // latest release — it may be older or a newer development build.
  if (e.state === 'update') return pill('pill-warn', t('badge.differs', { rel }), t('badge.differs.title'));
  return pill('pill-ok', t('badge.installed'));
}

function entryNote(e) {
  if (e.stale_at) return t('cat.stale', { time: new Date(e.stale_at).toLocaleString(LANG === 'de' ? 'de-DE' : undefined) });
  return e.note || '';
}

function catalogCardHTML(e) {
  const sub = [e.name, e.version].filter(Boolean).map(esc).join(' · ');
  const repoLink = e.repo ? `<a class="app-sub mono" href="https://github.com/${esc(e.repo)}" target="_blank" rel="noopener noreferrer">${esc(e.repo)}</a>` : '';
  const err = e.error ? `<div class="app-err" title="${esc(e.error)}">${esc(codeText(e.error_code, e.error))}</div>` : '';
  const noteText = entryNote(e);
  const note = noteText ? `<div class="app-sub">${esc(noteText)}</div>` : '';
  let body = '';
  let actions = '';
  if (e.installer === 'own' && e.command) {
    body = `<div class="root-note">${t('card.rootNote')}</div>
      <div class="cmd"><code>${esc(e.command)}</code><button class="btn-small" data-copy="${esc(e.command)}">${esc(t('btn.copy'))}</button></div>
      <div class="app-sub">${esc(t('card.ownInstaller'))}</div>`;
  } else if (e.installer === 'zpkg') {
    if (!e.error && !e.protected) {
      const rel = e.version || t('word.release');
      if (!e.installed) actions += `<button class="btn-primary btn-small" data-cat="install" data-id="${esc(e.id)}">${esc(t('btn.install'))}</button>`;
      else if (e.state === 'update') actions += `<button class="btn-primary btn-small" data-cat="install" data-id="${esc(e.id)}">${esc(e.installed_version ? t('btn.updateTo', { rel }) : t('btn.installRel', { rel }))}</button>`;
      // "newer": no button — it would downgrade a development build to the release.
      else if (e.state !== 'newer') actions += `<button class="btn-small" data-cat="install" data-id="${esc(e.id)}">${esc(t('btn.reinstall'))}</button>`;
    }
    if (e.installed && !e.protected) {
      const label = armed === e.name ? t('btn.uninstallAgain') : t('btn.uninstall');
      actions += `<button class="btn-danger btn-small" data-cat="uninstall" data-name="${esc(e.name)}">${esc(label)}</button>`;
    }
  }
  if (e.source === 'repo') actions += `<button class="btn-small" data-cat="remove-repo" data-repo="${esc(e.repo)}" title="${esc(t('btn.removeFromList.title'))}">${esc(t('btn.removeFromList'))}</button>`;
  return `<div class="card">
    <div class="app-head"><span class="app-icon">${esc((e.title || e.name || '?').charAt(0).toUpperCase())}</span>
      <div class="app-text"><div class="app-title">${esc(e.title || e.name)}</div><div class="app-sub mono">${sub}</div>${repoLink}</div>
      <div class="app-badges">${e.source === 'repo' ? pill('pill-warn', t('badge.thirdParty')) : ''}${e.error && !e.installed ? '' : stateBadge(e)}</div>
    </div>
    ${e.source === 'repo' ? `<div class="trust-note">${esc(t('card.trust'))}</div>` : ''}
    ${note}${err}${body}
    <div class="app-actions">${actions}</div>
  </div>`;
}

function renderCatalog() {
  // Presets and repositories the user added share one section.
  $('#cat-github').innerHTML = catalog.entries.map(catalogCardHTML).join('') || `<div class="empty">${esc(t('cat.none'))}</div>`;
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
    setStatus(t('cat.done', { what }), 'ok');
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
  if (selfTarget) b.textContent = t('self.update', { v: selfTarget });
}

// The backend downloads and checks the release, then a transient unit swaps the module:
// Module Manager is gone for a few seconds. Wait until /health answers with the new version.
async function selfUpdate() {
  const b = $('#self-update-btn');
  if (!selfTarget) return;
  if (!selfArmed) {
    selfArmed = true;
    b.textContent = t('self.again', { v: selfTarget });
    setTimeout(() => { selfArmed = false; if (selfTarget) b.textContent = t('self.update', { v: selfTarget }); }, 4000);
    return;
  }
  selfArmed = false;
  b.disabled = true;
  setStatus(t('self.checking', { v: selfTarget }));
  try {
    const r = await api(`${API_BASE}/self/update`, { method: 'POST', body: {} });
    setStatus(t('self.updating', { v: r.to }));
    const want = r.to.replace(/^v/, '');
    let away = false;
    const until = Date.now() + 180000;
    while (Date.now() < until) {
      await new Promise((ok) => setTimeout(ok, 3000));
      let h = null;
      try { h = await (await fetch(`${API_BASE}/health`, { cache: 'no-store' })).json(); } catch { away = true; continue; }
      if (h && h.version === want) { location.reload(); return; }
      if (h && away) {
        setStatus(t('self.failed', { v: r.to, old: h.version }), 'err');
        return;
      }
    }
    setStatus(t('self.timeout'), 'err');
  } catch (e) {
    setStatus(describe(e), 'err');
  } finally {
    b.disabled = false;
  }
}

// ---------- wiring ----------

async function refreshAll() {
  setStatus(t('loading'));
  const results = await Promise.allSettled([loadApps(), loadModules(), loadCatalog()]);
  const failed = results.filter((r) => r.status === 'rejected');
  if (failed.length) setStatus(failed.map((r) => describe(r.reason)).join(' · '), 'err');
  else setStatus('');
}

document.addEventListener('DOMContentLoaded', async () => {
  initTheme();
  await initLang();
  reportLang();
  applyStaticTexts();
  initLangSelect();
  document.querySelectorAll('.tab-btn').forEach((b) => b.addEventListener('click', () => {
    document.querySelectorAll('.tab-btn').forEach((x) => x.classList.toggle('active', x === b));
    document.querySelectorAll('.tab-panel').forEach((p) => p.classList.toggle('active', p.id === `tab-${b.dataset.tab}`));
  }));
  // #catalog (the button on the dashboard card) opens the catalog tab.
  const tab = document.querySelector(`.tab-btn[data-tab="${location.hash.slice(1)}"]`);
  if (tab) tab.click();
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
    if (await catalogAction('repos', { repo }, t('what.adding', { repo }))) $('#repo-input').value = '';
  });
  $('#tab-catalog').addEventListener('click', async (e) => {
    const copy = e.target.closest('button[data-copy]');
    if (copy) {
      try { await navigator.clipboard.writeText(copy.dataset.copy); setStatus(t('copy.ok'), 'ok'); } catch { setStatus(t('copy.fail'), 'err'); }
      return;
    }
    const b = e.target.closest('button[data-cat]');
    if (!b || b.disabled) return;
    const entry = catalog.entries.find((x) => x.id === b.dataset.id || x.name === b.dataset.name);
    const title = entry ? (entry.title || entry.name) : (b.dataset.repo || '');
    if (b.dataset.cat === 'install') {
      catalogAction('install', { id: b.dataset.id }, t('what.installing', { title }));
    } else if (b.dataset.cat === 'uninstall') {
      if (armed !== b.dataset.name) {
        armed = b.dataset.name;
        renderCatalog();
        setTimeout(() => { if (armed === b.dataset.name) { armed = null; renderCatalog(); } }, 5000);
        return;
      }
      catalogAction('uninstall', { name: b.dataset.name }, t('what.uninstalling', { title }));
    } else if (b.dataset.cat === 'remove-repo') {
      catalogAction('repos/remove', { repo: b.dataset.repo }, t('what.removing', { repo: b.dataset.repo }));
    }
  });
  refreshAll();
});
