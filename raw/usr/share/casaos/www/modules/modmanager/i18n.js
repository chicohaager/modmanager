'use strict';
// Texts of the UI in English and German. The language follows the ZimaOS user's setting
// (GET /v1/users/current/custom/system → data.lang, e.g. "de_DE", measured on 1.8.0) unless the
// language menu says otherwise. Every key must exist in both languages (scripts/i18n-check.mjs).
// {name} placeholders are filled by t(); th() escapes them for HTML and keeps the markup of the text.

const TEXTS = {
  en: {
    'header.subtitle': 'Hide or restore the apps ZimaOS ships built in, and see every installed module.',
    'theme.auto': 'Theme: auto', 'theme.light': 'Theme: light', 'theme.dark': 'Theme: dark',
    'lang.auto': 'Language: auto', 'lang.label': 'Language',
    'btn.refresh': 'Refresh',
    'tab.builtin': 'Built-in apps', 'tab.catalog': 'Catalog', 'tab.installed': 'Installed modules',
    'hint.builtin': '<b>Hide</b> takes an app off the dashboard and stops its services. Nothing is deleted: <b>Restore</b> brings it back exactly as ZimaOS shipped it. ZimaOS keeps the apps in its read-only system image, so they can be hidden but not removed. Data stays until you choose <b>Delete data</b> for a hidden app.',
    'hint.catalog': 'Install modules from GitHub releases. A module runs <b>as root</b> on this host. A GitHub image is installed only when its release ships a sha256 checksum and the download matches it &mdash; that proves the file <b>arrived intact</b>, not that its author can be trusted: the checksum comes from the same release. Modules keep their data in <span class="mono">/DATA/AppData</span> when removed.',
    'repo.label': 'Add a GitHub module', 'repo.placeholder': 'owner/repo or https://github.com/owner/repo', 'repo.add': 'Add',
    'repo.rule': 'The latest release must contain a sysext image (<span class="mono">name-amd64.raw</span> or <span class="mono">name.raw</span>) and its checksum.',
    'repo.trust': 'Add only repositories whose author you trust. An installed module runs as root and can read and change everything on this ZimaOS host; the checksum does not check that.',
    'section.github': 'GitHub modules', 'loading': 'Loading…',
    'hint.installed': 'Every module ZimaOS currently has merged, as reported by its module management.',
    'th.module': 'Module', 'th.title': 'Title', 'th.services': 'Services', 'th.installed': 'Installed', 'th.source': 'Source',
    'purge.title': 'Delete data', 'purge.type1': 'Type', 'purge.type2': 'to confirm.', 'purge.cancel': 'Cancel', 'purge.ok': 'Delete data',
    'purge.text': 'This permanently deletes {path} ({size}): the app\'s index, settings and models. Your own files elsewhere are not touched; the deletion is refused if a photo or video is found inside.',
    'err.session': 'Session expired — reload the ZimaOS page and sign in again.',
    'kind.sysext': 'system extension', 'kind.base': 'system image', 'kind.system': 'system tile',
    'pill.locked': 'Locked', 'pill.hidden': 'Hidden', 'pill.visible': 'Visible',
    'svc.masked': 'masked', 'svc.unknown': 'unknown', 'pill.data': 'data {size}',
    'svc.active': 'active', 'svc.inactive': 'inactive', 'svc.failed': 'failed', 'svc.activating': 'starting',
    'pill.experimental': 'experimental', 'pill.experimental.title': 'Writes into the ZimaOS user database (internal, may change with any ZimaOS update). A backup is taken before every change.',
    'btn.restore': 'Restore', 'btn.purge': 'Delete data', 'btn.hide': 'Hide',
    'apps.none': 'No built-in apps found.',
    'act.hide': 'Hiding {name} — ZimaOS re-merges its system extensions, this takes a few seconds…',
    'act.restore': 'Restoring {name} — ZimaOS re-merges its system extensions, this takes a few seconds…',
    'act.purge': 'Deleting data of {name} — this takes a few seconds…',
    'done.hide': '{name}: hidden. Reload the ZimaOS dashboard to see the change.',
    'done.restore': '{name}: restored. Reload the ZimaOS dashboard to see the change.',
    'done.purge': '{name}: data deleted.',
    'blocked.dependency': 'Files and search depend on it',
    'blocked.schema': 'The ZimaOS user database has changed; hiding is switched off for safety.',
    'blocked.version': 'Not verified on this ZimaOS version; hiding is switched off.',
    'mods.added': 'added', 'mods.none': 'No modules reported.',
    'badge.own': 'Own installer', 'badge.notInstalled': 'Not installed', 'badge.current': 'Up to date · {v}', 'badge.currentPlain': 'Up to date',
    'badge.update': 'Update {iv} → {rel}', 'badge.newer': '{iv} · newer than {rel}', 'badge.newer.title': 'A development build newer than the latest release.',
    'badge.differs': 'Differs from {rel}', 'badge.differs.title': 'The installed image is not the release image; it may be older or a development build.',
    'badge.installed': 'Installed', 'word.release': 'release', 'badge.thirdParty': 'third-party',
    'card.rootNote': '<span class="pill pill-root">root</span> Must run as root: open the ZimaOS terminal, log in with your ZimaOS user and paste the command — <code>sudo</code> asks for your ZimaOS password.',
    'btn.copy': 'Copy', 'card.ownInstaller': 'Needs its own installer (it sets up more than the image).',
    'btn.install': 'Install', 'btn.updateTo': 'Update to {rel}', 'btn.installRel': 'Install {rel}', 'btn.reinstall': 'Reinstall',
    'btn.uninstall': 'Uninstall', 'btn.uninstallAgain': 'Click again to uninstall',
    'btn.removeFromList': 'Remove from list', 'btn.removeFromList.title': 'Remove from this list (an installed module stays installed)',
    'card.trust': 'Third-party repository you added: the checksum only proves the download is intact, not that the author is trustworthy. The module would run as root.',
    'cat.none': 'No GitHub modules.', 'cat.done': '{what}: done.',
    'cat.stale': 'Release info from {time} — GitHub rate limit, newer releases not checked.',
    'what.adding': 'Adding {repo}', 'what.installing': 'Installing {title} (download, checksum, zpkg)', 'what.uninstalling': 'Uninstalling {title}', 'what.removing': 'Removing {repo} from the list',
    'copy.ok': 'Command copied.', 'copy.fail': 'Copy failed — select the command and copy it by hand.',
    'self.update': 'Update to {v}', 'self.again': 'Click again: update to {v}', 'self.checking': 'Downloading and checking {v}…',
    'self.updating': 'Updating to {v} — Module Manager restarts; this page reloads when it answers again…',
    'self.failed': 'The update to {v} failed; {old} is installed again. Log: /DATA/AppData/modmanager/self-update/log',
    'self.timeout': 'No answer from Module Manager after 3 minutes. Log: /DATA/AppData/modmanager/self-update/log',
    // codes of the API's errors (cmd/modmanager/handlers.go) and of catalog entries (error_code)
    'code.bad_repo': 'Not a GitHub repository (owner/repo or https://github.com/owner/repo).',
    'code.unknown_entry': 'Unknown catalog entry.',
    'code.no_image': 'The latest release has no sysext image (.raw) for this CPU.',
    'code.no_checksum': 'The release ships no sha256 checksum; it cannot be installed from here.',
    'code.checksum_mismatch': 'Checksum mismatch; not installed.',
    'code.own_installer': 'This module needs its own installer; copy its command into the ZimaOS terminal.',
    'code.protected': 'This module is managed by ZimaOS or by Module Manager itself and cannot be changed here.',
    'code.collision': 'Refused: the image would replace files ZimaOS already has: {detail}',
    'code.not_newer': 'The latest release is not newer than this Module Manager.',
    'code.not_installed': 'This module is not installed.',
    'code.rate_limit': 'GitHub rate limit reached (60 requests per hour without login); try again later.',
    'code.no_release': 'No such repository, or it has no release.',
    'code.unreachable': 'GitHub is not reachable.',
    'code.missing_id': 'No app given.', 'code.unknown_app': 'Unknown app.', 'code.blocked': 'This app cannot be hidden here: {detail}',
    'code.not_hidden': 'The app is not hidden.', 'code.media_found': 'Refused: photos or videos were found in the data folder: {detail}',
    'code.confirm_mismatch': 'The typed name does not match.',
    'code.failed': 'Failed: {detail}', 'code.internal': 'Internal error: {detail}',
  },
  de: {
    'header.subtitle': 'Eingebaute Apps von ZimaOS ausblenden oder wiederherstellen und alle installierten Module sehen.',
    'theme.auto': 'Design: automatisch', 'theme.light': 'Design: hell', 'theme.dark': 'Design: dunkel',
    'lang.auto': 'Sprache: automatisch', 'lang.label': 'Sprache',
    'btn.refresh': 'Aktualisieren',
    'tab.builtin': 'Eingebaute Apps', 'tab.catalog': 'Katalog', 'tab.installed': 'Installierte Module',
    'hint.builtin': '<b>Ausblenden</b> nimmt eine App vom Dashboard und stoppt ihre Dienste. Gelöscht wird nichts: <b>Wiederherstellen</b> bringt sie genau so zurück, wie ZimaOS sie ausliefert. ZimaOS hält die Apps in seinem schreibgeschützten Systemabbild, darum lassen sie sich ausblenden, aber nicht entfernen. Daten bleiben, bis Sie bei einer ausgeblendeten App <b>Daten löschen</b> wählen.',
    'hint.catalog': 'Module aus GitHub-Releases installieren. Ein Modul läuft auf diesem Host <b>als root</b>. Ein GitHub-Image wird nur installiert, wenn sein Release eine sha256-Prüfsumme mitliefert und der Download dazu passt &mdash; das belegt, dass die Datei <b>unversehrt angekommen</b> ist, nicht, dass man ihrem Autor trauen kann: Die Prüfsumme stammt aus demselben Release. Beim Entfernen bleiben die Daten eines Moduls in <span class="mono">/DATA/AppData</span>.',
    'repo.label': 'GitHub-Modul hinzufügen', 'repo.placeholder': 'besitzer/repo oder https://github.com/besitzer/repo', 'repo.add': 'Hinzufügen',
    'repo.rule': 'Das neueste Release muss ein sysext-Image (<span class="mono">name-amd64.raw</span> oder <span class="mono">name.raw</span>) und seine Prüfsumme enthalten.',
    'repo.trust': 'Fügen Sie nur Repositories hinzu, deren Autor Sie vertrauen. Ein installiertes Modul läuft als root und kann auf diesem ZimaOS-Host alles lesen und ändern; das prüft die Prüfsumme nicht.',
    'section.github': 'GitHub-Module', 'loading': 'Lädt…',
    'hint.installed': 'Alle Module, die ZimaOS gerade eingebunden hat, so wie seine Modulverwaltung sie meldet.',
    'th.module': 'Modul', 'th.title': 'Titel', 'th.services': 'Dienste', 'th.installed': 'Installiert', 'th.source': 'Herkunft',
    'purge.title': 'Daten löschen', 'purge.type1': 'Zum Bestätigen', 'purge.type2': 'eintippen.', 'purge.cancel': 'Abbrechen', 'purge.ok': 'Daten löschen',
    'purge.text': 'Das löscht {path} ({size}) endgültig: Index, Einstellungen und Modelle der App. Ihre eigenen Dateien anderswo bleiben unberührt; das Löschen wird verweigert, wenn darin ein Foto oder Video gefunden wird.',
    'err.session': 'Sitzung abgelaufen — laden Sie die ZimaOS-Seite neu und melden Sie sich wieder an.',
    'kind.sysext': 'Systemerweiterung', 'kind.base': 'Systemabbild', 'kind.system': 'Systemkachel',
    'pill.locked': 'Gesperrt', 'pill.hidden': 'Ausgeblendet', 'pill.visible': 'Sichtbar',
    'svc.masked': 'maskiert', 'svc.unknown': 'unbekannt', 'pill.data': 'Daten {size}',
    'svc.active': 'aktiv', 'svc.inactive': 'inaktiv', 'svc.failed': 'fehlgeschlagen', 'svc.activating': 'startet',
    'pill.experimental': 'experimentell', 'pill.experimental.title': 'Schreibt in die Benutzerdatenbank von ZimaOS (intern, kann sich mit jedem ZimaOS-Update ändern). Vor jeder Änderung wird eine Sicherung angelegt.',
    'btn.restore': 'Wiederherstellen', 'btn.purge': 'Daten löschen', 'btn.hide': 'Ausblenden',
    'apps.none': 'Keine eingebauten Apps gefunden.',
    'act.hide': '{name} wird ausgeblendet — ZimaOS bindet seine Systemerweiterungen neu ein, das dauert ein paar Sekunden…',
    'act.restore': '{name} wird wiederhergestellt — ZimaOS bindet seine Systemerweiterungen neu ein, das dauert ein paar Sekunden…',
    'act.purge': 'Daten von {name} werden gelöscht — das dauert ein paar Sekunden…',
    'done.hide': '{name}: ausgeblendet. Laden Sie das ZimaOS-Dashboard neu, um die Änderung zu sehen.',
    'done.restore': '{name}: wiederhergestellt. Laden Sie das ZimaOS-Dashboard neu, um die Änderung zu sehen.',
    'done.purge': '{name}: Daten gelöscht.',
    'blocked.dependency': 'Dateien und Suche hängen davon ab',
    'blocked.schema': 'Die Benutzerdatenbank von ZimaOS hat sich geändert; Ausblenden ist sicherheitshalber abgeschaltet.',
    'blocked.version': 'Auf dieser ZimaOS-Version nicht geprüft; Ausblenden ist abgeschaltet.',
    'mods.added': 'hinzugefügt', 'mods.none': 'Keine Module gemeldet.',
    'badge.own': 'Eigener Installer', 'badge.notInstalled': 'Nicht installiert', 'badge.current': 'Aktuell · {v}', 'badge.currentPlain': 'Aktuell',
    'badge.update': 'Update {iv} → {rel}', 'badge.newer': '{iv} · neuer als {rel}', 'badge.newer.title': 'Ein Entwicklungsstand, neuer als das neueste Release.',
    'badge.differs': 'Weicht ab von {rel}', 'badge.differs.title': 'Das installierte Image ist nicht das Release-Image; es kann älter oder ein Entwicklungsstand sein.',
    'badge.installed': 'Installiert', 'word.release': 'Release', 'badge.thirdParty': 'Drittanbieter',
    'card.rootNote': '<span class="pill pill-root">root</span> Muss als root laufen: Öffnen Sie das ZimaOS-Terminal, melden Sie sich mit Ihrem ZimaOS-Benutzer an und fügen Sie den Befehl ein — <code>sudo</code> fragt nach Ihrem ZimaOS-Passwort.',
    'btn.copy': 'Kopieren', 'card.ownInstaller': 'Braucht einen eigenen Installer (er richtet mehr ein als das Image).',
    'btn.install': 'Installieren', 'btn.updateTo': 'Update auf {rel}', 'btn.installRel': '{rel} installieren', 'btn.reinstall': 'Neu installieren',
    'btn.uninstall': 'Deinstallieren', 'btn.uninstallAgain': 'Zum Deinstallieren nochmals klicken',
    'btn.removeFromList': 'Aus der Liste entfernen', 'btn.removeFromList.title': 'Aus dieser Liste entfernen (ein installiertes Modul bleibt installiert)',
    'card.trust': 'Von Ihnen hinzugefügtes Drittanbieter-Repository: Die Prüfsumme belegt nur, dass der Download unversehrt ist, nicht, dass der Autor vertrauenswürdig ist. Das Modul liefe als root.',
    'cat.none': 'Keine GitHub-Module.', 'cat.done': '{what}: erledigt.',
    'cat.stale': 'Release-Stand vom {time} — GitHub-Limit erreicht, neuere Releases nicht geprüft.',
    'what.adding': '{repo} wird hinzugefügt', 'what.installing': '{title} wird installiert (Download, Prüfsumme, zpkg)', 'what.uninstalling': '{title} wird deinstalliert', 'what.removing': '{repo} wird aus der Liste entfernt',
    'copy.ok': 'Befehl kopiert.', 'copy.fail': 'Kopieren fehlgeschlagen — markieren Sie den Befehl und kopieren Sie ihn von Hand.',
    'self.update': 'Update auf {v}', 'self.again': 'Nochmals klicken: Update auf {v}', 'self.checking': '{v} wird geladen und geprüft…',
    'self.updating': 'Update auf {v} — Module Manager startet neu; die Seite lädt neu, sobald er wieder antwortet…',
    'self.failed': 'Das Update auf {v} ist fehlgeschlagen; {old} ist wieder installiert. Protokoll: /DATA/AppData/modmanager/self-update/log',
    'self.timeout': 'Keine Antwort von Module Manager nach 3 Minuten. Protokoll: /DATA/AppData/modmanager/self-update/log',
    'code.bad_repo': 'Kein GitHub-Repository (besitzer/repo oder https://github.com/besitzer/repo).',
    'code.unknown_entry': 'Unbekannter Katalogeintrag.',
    'code.no_image': 'Das neueste Release hat kein sysext-Image (.raw) für diese CPU.',
    'code.no_checksum': 'Das Release liefert keine sha256-Prüfsumme; von hier aus ist es nicht installierbar.',
    'code.checksum_mismatch': 'Prüfsumme passt nicht; nicht installiert.',
    'code.own_installer': 'Dieses Modul braucht seinen eigenen Installer; kopieren Sie seinen Befehl ins ZimaOS-Terminal.',
    'code.protected': 'Dieses Modul verwaltet ZimaOS oder Module Manager selbst; hier lässt es sich nicht ändern.',
    'code.collision': 'Abgelehnt: Das Image würde Dateien ersetzen, die ZimaOS schon hat: {detail}',
    'code.not_newer': 'Das neueste Release ist nicht neuer als dieser Module Manager.',
    'code.not_installed': 'Dieses Modul ist nicht installiert.',
    'code.rate_limit': 'GitHub-Limit erreicht (60 Anfragen pro Stunde ohne Anmeldung); später noch einmal versuchen.',
    'code.no_release': 'Dieses Repository gibt es nicht, oder es hat kein Release.',
    'code.unreachable': 'GitHub ist nicht erreichbar.',
    'code.missing_id': 'Keine App angegeben.', 'code.unknown_app': 'Unbekannte App.', 'code.blocked': 'Diese App lässt sich hier nicht ausblenden: {detail}',
    'code.not_hidden': 'Die App ist nicht ausgeblendet.', 'code.media_found': 'Abgelehnt: Im Datenordner wurden Fotos oder Videos gefunden: {detail}',
    'code.confirm_mismatch': 'Der eingegebene Name passt nicht.',
    'code.failed': 'Fehlgeschlagen: {detail}', 'code.internal': 'Interner Fehler: {detail}',
  },
};

let LANG = 'en';

function t(key, vars = {}) {
  const s = (TEXTS[LANG] && TEXTS[LANG][key]) ?? TEXTS.en[key] ?? key;
  return s.replace(/\{(\w+)\}/g, (_, k) => (vars[k] ?? `{${k}}`));
}

// th: for innerHTML — the text's own markup stays, the values are escaped.
function th(key, vars = {}) {
  const safe = {};
  for (const [k, v] of Object.entries(vars)) safe[k] = String(v ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  return t(key, safe);
}

// The static texts of index.html: data-i18n (text), data-i18n-html (markup), data-i18n-placeholder.
function applyStaticTexts() {
  document.documentElement.lang = LANG;
  document.querySelectorAll('[data-i18n]').forEach((el) => { el.textContent = t(el.dataset.i18n); });
  document.querySelectorAll('[data-i18n-html]').forEach((el) => { el.innerHTML = t(el.dataset.i18nHtml); });
  document.querySelectorAll('[data-i18n-placeholder]').forEach((el) => { el.placeholder = t(el.dataset.i18nPlaceholder); });
}

// The language: the stored choice, else the ZimaOS user's setting, else the browser's.
async function initLang() {
  let choice = null;
  try { choice = localStorage.getItem('modmanager.lang'); } catch { /* private window */ }
  if (choice === 'en' || choice === 'de') {
    LANG = choice;
    return;
  }
  let sys = '';
  try {
    const token = localStorage.getItem('access_token');
    const res = await fetch('/v1/users/current/custom/system', { headers: token ? { Authorization: `Bearer ${token}` } : {} });
    if (res.ok) sys = String(((await res.json()).data || {}).lang || '');
  } catch { /* no ZimaOS setting: the browser decides */ }
  LANG = (sys || navigator.language || 'en').toLowerCase().startsWith('de') ? 'de' : 'en';
}
