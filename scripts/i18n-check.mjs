// Every text of the UI exists in every language of TEXTS — English, German, French (run by build.sh and CI):
//   1. every language has the same keys as TEXTS.en, and none of them is missing;
//   2. every key app.js asks for with t()/th() and every data-i18n* key of index.html exists;
//   3. every error code the Go backend can send (WriteError codes, catalog errorCode, sysapps
//      blocked codes) has a text — read from the Go sources, so a new code fails here;
//   4. every visible text of index.html carries a data-i18n* key (product names excepted).
// Exit 1 with the list of what is missing.
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

const WEB = 'raw/usr/share/casaos/www/modules/modmanager';
const read = (p) => readFileSync(p, 'utf8');
const ctx = {};
vm.createContext(ctx);
vm.runInContext(`${read(`${WEB}/i18n.js`)}\nthis.TEXTS = TEXTS;`, ctx);
const LANGS = ['en', 'de', 'fr'];
const { en } = ctx.TEXTS;
const missing = [];
for (const l of LANGS) if (!ctx.TEXTS[l]) missing.push(`${l}: the whole language (TEXTS.${l})`);
for (const l of Object.keys(ctx.TEXTS)) if (!LANGS.includes(l)) missing.push(`${l}: not in LANGS of this check`);
const need = (key, where) => {
  for (const l of LANGS) if (ctx.TEXTS[l] && !(key in ctx.TEXTS[l])) missing.push(`${l}: ${key} (${where})`);
};

for (const l of LANGS.slice(1)) {
  const d = ctx.TEXTS[l] || {};
  for (const k of Object.keys(en)) if (ctx.TEXTS[l] && !(k in d)) missing.push(`${l}: ${k} (only in en)`);
  for (const k of Object.keys(d)) if (!(k in en)) missing.push(`en: ${k} (only in ${l})`);
}

const app = read(`${WEB}/app.js`);
for (const m of app.matchAll(/\bth?\('([\w.]+)'/g)) need(m[1], 'app.js');
for (const prefix of ['act', 'done']) for (const a of ['hide', 'restore', 'purge']) need(`${prefix}.${a}`, `app.js t(\`${prefix}.\${action}\`)`);
for (const k of ['sysext', 'base', 'system']) need(`kind.${k}`, 'app.js kind');

const html = read(`${WEB}/index.html`);
for (const m of html.matchAll(/data-i18n(?:-html|-placeholder)?="([\w.]+)"/g)) need(m[1], 'index.html');
for (const l of LANGS) if (!html.includes(`<option value="${l}">`)) missing.push(`index.html: no <option value="${l}"> in the language menu`);

const goCodes = new Set();
for (const f of ['cmd/modmanager/handlers.go']) for (const m of read(f).matchAll(/WriteError\(w, [^,]+, "(\w+)"/g)) goCodes.add(m[1]);
const cat = read('internal/catalog/catalog.go');
const fn = cat.slice(cat.indexOf('func errorCode('), cat.indexOf('// ParseRepo accepts'));
for (const m of fn.matchAll(/return "(\w+)"/g)) goCodes.add(m[1]);
for (const m of cat.matchAll(/e\.ErrorCode = [^\n]*"(\w+)"\n/g)) goCodes.add(m[1]);
for (const c of goCodes) need(`code.${c}`, 'Go error code');
const sys = read('internal/sysapps/sysapps.go');
for (const m of sys.matchAll(/BlockedCode = [^\n]*"(\w+)"/g)) need(`blocked.${m[1]}`, 'sysapps blocked code');
for (const m of sys.matchAll(/return "(\w+)"\n/g)) need(`blocked.${m[1]}`, 'sysapps blockedCode()');

// 4: text between tags in the body must sit in an element with a data-i18n* key
const body = html.slice(html.indexOf('<body'));
const allowed = new Set(['Module Manager', 'English', 'Deutsch', 'Français', 'Loading…', 'Loading&hellip;', 'root', '/DATA/AppData', 'name-amd64.raw', 'name.raw', 'sudo']);
for (const m of body.matchAll(/<(\w+)([^>]*)>([^<>]*[A-Za-z]{3}[^<>]*)</g)) {
  const text = m[3].trim();
  if (!text || allowed.has(text) || /^v\d/.test(text)) continue;
  if (!/data-i18n/.test(m[2])) {
    const before = body.slice(0, m.index);
    const open = before.lastIndexOf('data-i18n-html=');
    const close = before.lastIndexOf('</p>') > before.lastIndexOf('</div>') ? before.lastIndexOf('</p>') : before.lastIndexOf('</div>');
    if (open < 0 || open < close) missing.push(`index.html: "${text.slice(0, 50)}" has no data-i18n key`);
  }
}

console.log(`i18n-check: ${LANGS.map((l) => `${Object.keys(ctx.TEXTS[l] || {}).length} keys in ${l}`).join(', ')}, ${goCodes.size} Go error codes`);
if (missing.length) {
  console.log(missing.map((x) => `  missing ${x}`).join('\n'));
  process.exit(1);
}
