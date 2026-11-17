#!/usr/bin/env python3
"""Browser check: after an install from the catalog the page says to reload the ZimaOS dashboard.

ZimaOS 1.8.0 does not show a newly installed module's tile in an open dashboard (measured
2026-11-17: no tile after 120 s, the tile after a reload). The page served from raw/ runs in
Chromium; every API call is answered here (page.route), nothing reaches a host.

    ~/.venvs/playwright/bin/python3 scripts/ui-install-hint.py      exit 0 = all cases ok
"""
import json
import sys
from pathlib import Path

from playwright.sync_api import sync_playwright

WEB = Path(__file__).resolve().parent.parent / "raw/usr/share/casaos/www/modules/modmanager"
BASE = "http://modmanager.test/modules/modmanager/"
TYPES = {".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml"}


def entry(installed):
    return {"id": "gh:t/notes", "name": "notes", "title": "Notes", "version": "v1.0.0", "installer": "zpkg",
            "installed": installed, "installed_version": "1.0.0" if installed else "", "state": "current" if installed else "",
            "asset": "notes-amd64.raw"}


def run(lang, install_fails):
    state = {"installed": False, "job": 0}

    def handle(route):
        url, method = route.request.url, route.request.method
        path = url.split("modmanager.test", 1)[1].split("?")[0]
        if path.startswith("/modules/modmanager/"):
            f = WEB / path.removeprefix("/modules/modmanager/")
            return route.fulfill(status=200, body=f.read_bytes(), headers={"Content-Type": TYPES.get(f.suffix, "application/octet-stream")})
        if path == "/modmanager/catalog/install" and method == "POST":
            if install_fails:
                return route.fulfill(status=502, json={"code": "download", "error": "release asset not reachable"})
            state["installed"], state["job"] = True, state["job"] + 1
            return route.fulfill(json={"entries": [entry(True)], "repos": []})
        if path == "/modmanager/catalog":
            return route.fulfill(json={"entries": [entry(state["installed"])], "repos": []})
        if path == "/modmanager/progress":
            return route.fulfill(json={"id": state["job"], "running": False, "ok": True, "lines": []})
        return route.fulfill(json={})  # apps, modules, loops, self, language reports: empty answers

    with sync_playwright() as p:
        b = p.chromium.launch()
        page = b.new_page(locale=lang)
        page.add_init_script(f"localStorage.setItem('lang', {json.dumps(lang)})")
        page.route("http://modmanager.test/**", handle)
        page.goto(BASE + "index.html#catalog")
        page.wait_for_selector('button[data-cat="install"]', timeout=10000)
        with page.expect_response("**/modmanager/catalog/install"):
            page.locator('button[data-cat="install"]').first.click()
        page.wait_for_timeout(1500)  # termEnd polls once more after the answer, then the page renders
        status = page.locator("#status").inner_text()
        cls = page.locator("#status").get_attribute("class") or ""
        b.close()
    return status, cls


WANT = {"en": "Reload the ZimaOS dashboard", "de": "ZimaOS-Dashboard neu", "fr": "rechargez le tableau de bord ZimaOS"}
fails = 0
for lang, text in WANT.items():
    status, cls = run(lang, False)
    ok = text.lower() in status.lower() and "Notes" in status and cls == "ok"
    print(("ok  " if ok else "FAIL"), f"{lang}: install done says to reload the dashboard: {status!r} ({cls})")
    fails += not ok
status, cls = run("en", True)
ok = "reload" not in status.lower() and cls == "err"
print(("ok  " if ok else "FAIL"), f"en: a failed install shows the error, no reload hint: {status!r} ({cls})")
fails += not ok
print(f"{fails} failure(s)")
sys.exit(1 if fails else 0)
