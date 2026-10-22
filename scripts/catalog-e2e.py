#!/usr/bin/env python3
"""End-to-end check of a release against a running Module Manager.

Adds a GitHub repository to the catalog of a ZimaOS host, asserts that the
catalog picks the release image, shows the expected version as installed and
current, then removes the repository again (unless it was there before).

    python3 scripts/catalog-e2e.py --url http://<zimaos-host> --user <name> --password-file <file>
    python3 scripts/catalog-e2e.py --selftest

Options: --repo (default chicohaager/modmanager), --expect (default: version in
cmd/modmanager/main.go), --keep (leave the repository in the catalog).
The password is read from a file and never printed; API answers are printed
field by field from a fixed list.
"""

from __future__ import annotations

import argparse
import json
import pathlib
import re
import sys
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent
SHOWN = ("id", "title", "version", "asset", "installer", "installed", "installed_version",
         "state", "protected", "error", "note")


def source_version() -> str:
    m = re.search(r'^\s*version\s*=\s*"([^"]+)"', (ROOT / "cmd/modmanager/main.go").read_text(), re.MULTILINE)
    if not m:
        raise SystemExit("version not found in cmd/modmanager/main.go")
    return m.group(1)


def problems(entry: dict | None, expect: str) -> list[str]:
    """What is wrong with the catalog entry of the release; empty when it is right."""
    if entry is None:
        return ["repository not in the catalog after adding it"]
    out = []
    if entry.get("error"):
        out.append(f"error: {entry['error']}")
    if entry.get("version") != "v" + expect:
        out.append(f"latest release {entry.get('version')!r}, expected 'v{expect}'")
    asset = entry.get("asset") or ""
    if not re.fullmatch(r"[A-Za-z0-9_.-]+[-_](amd64|x86_64|x86-64|arm64|aarch64)\.raw", asset):
        out.append(f"image {asset!r} is not an architecture-specific .raw")
    if entry.get("installer") != "zpkg":
        out.append(f"installer {entry.get('installer')!r}, expected 'zpkg'")
    if entry.get("installed") is not True or entry.get("installed_version") != expect:
        out.append(f"installed {entry.get('installed')!r} version {entry.get('installed_version')!r}, expected {expect}")
    if entry.get("state") != "current":
        out.append(f"state {entry.get('state')!r}, expected 'current'")
    return out


def selftest() -> bool:
    good = {"id": "gh:o/r", "version": "v1.2.3", "asset": "r-amd64.raw", "installer": "zpkg",
            "installed": True, "installed_version": "1.2.3", "state": "current"}
    cases = [
        (good, 0),
        (None, 1),
        (dict(good, state="outdated"), 1),
        (dict(good, version="v1.2.4"), 1),
        (dict(good, asset="r.raw"), 1),  # an arch-less image would be picked on any CPU
        (dict(good, installed_version="1.2.2"), 1),
        (dict(good, installed=False), 1),
        (dict(good, error="no_checksum"), 1),
    ]
    for entry, want in cases:
        got = len(problems(entry, "1.2.3"))
        if (got > 0) != (want > 0):
            print(f"✗ selftest: {entry!r} gave {got} problem(s), expected {'some' if want else 'none'}")
            return False
    return True


class Api:
    def __init__(self, url: str):
        self.url, self.token = url.rstrip("/"), None

    def call(self, method: str, path: str, body: dict | None = None) -> tuple[int, dict]:
        headers = {"Content-Type": "application/json"}
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.url + path, data=data, method=method, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=120) as r:
                return r.status, json.loads(r.read() or b"{}")
        except urllib.error.HTTPError as e:
            try:
                err = json.loads(e.read() or b"{}")
            except ValueError:
                err = {}
            return e.code, {"error": err.get("error") or err.get("message") or "?"}

    def login(self, user: str, password: str) -> None:
        st, d = self.call("POST", "/v1/users/login", {"username": user, "password": password})
        self.token = ((d.get("data") or {}).get("token") or {}).get("access_token")
        if st != 200 or not self.token:
            raise SystemExit(f"✗ login failed: HTTP {st} {d.get('error', '')}")

    def entries(self) -> list[dict]:
        st, d = self.call("GET", "/modmanager/catalog")
        if st != 200:
            raise SystemExit(f"✗ GET /modmanager/catalog: HTTP {st} {d.get('error', '')}")
        return d.get("entries") or []


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--url")
    ap.add_argument("--user")
    ap.add_argument("--password-file")
    ap.add_argument("--repo", default="chicohaager/modmanager")
    ap.add_argument("--expect")
    ap.add_argument("--keep", action="store_true")
    ap.add_argument("--selftest", action="store_true")
    a = ap.parse_args()

    if not selftest():
        return 2
    if a.selftest:
        print("✓ selftest: a correct entry passes, 7 wrong ones fail")
        return 0
    if not (a.url and a.user and a.password_file):
        ap.error("--url, --user and --password-file are required")
    expect = a.expect or source_version()
    eid = "gh:" + a.repo

    api = Api(a.url)
    api.login(a.user, pathlib.Path(a.password_file).read_text().strip())
    before = api.entries()
    was_there = any(e.get("id") == eid for e in before)
    print(f"catalog before: {len(before)} entries, {a.repo} {'already listed' if was_there else 'not listed'}")

    if not was_there:
        st, d = api.call("POST", "/modmanager/catalog/repos", {"repo": a.repo})
        if st != 200:
            print(f"✗ add {a.repo}: HTTP {st} {d.get('error', '')}")
            return 1
    try:
        entry = next((e for e in api.entries() if e.get("id") == eid), None)
        if entry is not None:
            print("entry:", json.dumps({k: entry[k] for k in SHOWN if k in entry}, ensure_ascii=False))
        found = problems(entry, expect)
    finally:
        if not was_there and not a.keep:
            st, d = api.call("POST", "/modmanager/catalog/repos/remove", {"repo": a.repo})
            after = api.entries()
            print(f"removed {a.repo}: HTTP {st}; catalog after: {len(after)} entries (before: {len(before)})")
            if st != 200 or len(after) != len(before):
                print("✗ catalog not back to its previous state")
                return 1

    if found:
        print(f"✗ {a.repo} v{expect}:")
        for p in found:
            print("  -", p)
        return 1
    print(f"✓ {a.repo} v{expect}: release image picked, installed version current")
    return 0


if __name__ == "__main__":
    sys.exit(main())
