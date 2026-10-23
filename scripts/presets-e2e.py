#!/usr/bin/env python3
"""End-to-end check of the Lintux sysext presets against a running Module Manager.

For every preset given as <repo>=<version>, asserts that the catalog lists it
with that latest release, an architecture-specific image, zpkg as installer
and — the module being installed on the host — the same installed version
read from its health route, state "current". Read-only: adds and removes nothing.

    python3 scripts/presets-e2e.py --url http://<zimaos-host> --user <name> --password-file <file> \\
        chicohaager/zimaos-admintools-sysext=1.1.0 chicohaager/zimaos-restic-sysext=1.0.0 …

The checks are the ones catalog-e2e.py applies to the Module Manager's own
release (problems()); the password is read from a file and never printed.
"""

from __future__ import annotations

import argparse
import importlib.util
import pathlib
import sys

HERE = pathlib.Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("catalog_e2e", HERE / "catalog-e2e.py")
e2e = importlib.util.module_from_spec(spec)
spec.loader.exec_module(e2e)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--url", required=True)
    ap.add_argument("--user", required=True)
    ap.add_argument("--password-file", required=True)
    ap.add_argument("expect", nargs="+", help="<owner/repo>=<version>")
    a = ap.parse_args()

    api = e2e.Api(a.url)
    api.login(a.user, pathlib.Path(a.password_file).read_text().strip())
    entries = {e.get("id"): e for e in api.entries()}
    bad = 0
    for item in a.expect:
        repo, _, version = item.partition("=")
        entry = entries.get("gh:" + repo)
        found = e2e.problems(entry, version)
        shown = {k: entry.get(k) for k in e2e.SHOWN} if entry else {}
        if found:
            bad += 1
            print(f"✗ {repo}: " + "; ".join(found))
        else:
            print(f"✓ {repo}: v{version} picked ({shown.get('asset')}), installed {shown.get('installed_version')}, current")
    print(f"{len(a.expect) - bad} of {len(a.expect)} presets right")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
