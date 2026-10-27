# Changelog

Releases: https://github.com/chicohaager/modmanager/releases. Every release is amd64 only and tested on a ZimaBoard 2 with ZimaOS 1.8.0.

## 0.3.3 — unreleased

- Catalog: **Restart needed** on a module whose service still runs the program its image replaced — `zpkg install --force` does not restart services (measured on ZimaOS 1.8.0). Module Manager compares each service of the module (its unit files in the image, templates with their running instances) with the program file its `ExecStart` names, so this works for every module, also ones released before; modules built with lintux-modkit v0.1.3 also report it on their health route (`"binary":"replaced"`). The button **Restart (reinstall)** reinstalls the release through `zpkg remove` + `zpkg install`, which restarts the services.
- Built against lintux-modkit v0.1.3.

## 0.3.2 — 2026-10-26

- Prometheus Exporter in the catalog (`chicohaager/zimaos-node-exporter-sysext` v1.0.0: `node_exporter` and `smartctl_exporter` on the host for Prometheus and Grafana, installed version from `/node-exporter/health`).
- `build.sh` stops when the README header names another version than the code.
- Built against lintux-modkit v0.1.2 (image modes 755/644 whatever the umask, so the release tag rebuilds to the same bytes; `tools/check-release.sh`).

## 0.3.1 — 2026-10-26

Security and robustness fixes from a full code and security review.

- **State moves to `/DATA/.modmanager`** (0700, root only). Up to 0.3.0 it was `/DATA/AppData/modmanager`, and `/DATA/AppData` is writable by everyone on ZimaOS 1.8 (0777 without the sticky bit; the share of `/DATA` is writable over SMB): another user could swap a download for their own image between the checksum and `zpkg install`, or plant a symlink root writes through. Module Manager refuses to start when the state directory is a symlink, belongs to someone else or sits in a directory others can write to. The first start copies the added repositories, the language, the saved manifests of hidden apps and the `user.db` backups over (plain root-owned files only); the old directory stays.
- **Session tokens are checked against the user service's own keys** (lintux-modkit v0.1.1): the JWKS address comes from `user-service.url`, which only root writes, not from the gateway's route table — a process without root can change that table without login (measured on ZimaOS 1.8.0).
- A failed module update installs the previous version again (it used to leave the module removed).
- An arm64 host no longer takes the arch-less `name.raw` of a release that also ships `name-amd64.raw`: lintux-modkit's build writes it as a copy of the amd64 image.
- Self-update: a failed `zpkg remove` stops before anything is installed over the running version, and a failed way back ends with `result rollback-failed` in the log instead of claiming a rollback.
- A catalog request cut short (closed tab, timeout) no longer leaves "GitHub not reachable" in the cache for 10 minutes.
- The dashboard card stays when GitHub does not answer the update check (offline, rate limit), and the check comes back after an hour instead of 12.
- An install no longer waits for a slow ZimaOS message bus (up to 5 s).
- The local build refuses a lintux-modkit checkout other than the pinned one (`lintux-modkit.ref`, the same tag CI checks out); govulncheck is pinned in CI.

## 0.3.0 — 2026-10-26

- French UI: every text also in French (menu: Français; auto picks it when the ZimaOS user's language, or without one the browser's, is French), the dashboard card too; the i18n check now fails when any language lacks a text or the menu lacks a language.
- Pangolin in the catalog (`chicohaager/zimaos-pangolin-sysext` v1.0.0: the Pangolin CLI's site tunnel and the CLI in the ZimaOS terminal, installed version from `/pangolin/health`).
- CI builds against lintux-modkit v0.1.0 (tagged), not its moving `main`.
- CI on every push and pull request: gofmt, go vet, `node --check` on the frontend, `go test -race`, govulncheck (advisory), build for amd64 and arm64 (arm64 built, not tested on hardware).
- README: screenshots of the three tabs; this changelog.
- German UI: every text in English and German, language auto (from the ZimaOS user's setting) or chosen in the header; error codes of the API and of catalog entries are translated, the installed-modules table shows the modules' German titles; a check fails the build when a text or an error code has no translation.
- Module Manager updates itself: an **Update to v…** button next to the version when a newer release exists; download, checksum and collision check as for any catalog module, the swap runs in a transient unit and rolls back to the previous image if the install fails (tested on ZimaOS 1.8.0: 0.2.4 → 0.2.5 in 8 s; an image zpkg refuses rolled back to the previous one in 7 s).
- Module updates on the dashboard: a check two minutes after the start, after every install from the catalog, and then every 12 hours (hourly while updates wait, so one installed elsewhere drops off) finds installed catalog modules with a newer release (and a newer Module Manager); while there are any, a ZimaOS card on the dashboard names them, with a button that opens the catalog. ZimaOS does not store such cards, so the card is sent again every minute; it is withdrawn once nothing waits. The card speaks the language the page last showed (en/de).
- Catalog: when GitHub's API rate limit is reached (60 calls per hour without login), the newest release is read from github.com's release pages instead — they do not count against that limit (measured 2026-10-23). Only if those fail too, the last good answer (or the rate-limit message) is shown.

## 0.2.5 — 2026-10-23

- Four new modules in the catalog, installable from it: Admin Tools, WireGuard, Nebula and restic Backup (`chicohaager/zimaos-*-sysext`), each with its installed version read from its health route.
- An image whose files exist on the host already is refused (HTTP 409 with the list): a module overlays `/usr`, so such a file would replace ZimaOS' own for every service. On an update the files of the installed version do not count.
- Catalog cards: badges stack and a long repository name wraps (it ran 59 px over the card in a 1280 px window).
- README: install from the release with the rename to `modmanager.raw` — `zpkg` takes the module name from the file name and refuses `modmanager-amd64.raw` ("module not pass validate").

## 0.2.4 — 2026-10-22

- Modules with their own installer (ZFW, mergerfs + SnapRAID, Tailscale): the hint says to paste the command into the ZimaOS terminal instead of an SSH session.

## 0.2.3 — 2026-10-22

First public release.

- Hide or restore the built-in apps: PeerDrop and ZVM are masked as system extensions, Photos' manifest is shadowed with `ui.show: false` and its services are masked, the Backup tile (experimental, ZimaOS 1.8.0 only) gets a deny entry in `user.db`, which is backed up before every write.
- Restore brings an app back as ZimaOS shipped it. Delete data (hidden apps only) is refused when photo or video files are found and asks you to type the app's name.
- Catalog of GitHub sysext modules, installed through `zpkg` only when the release ships a sha256 checksum and the download matches it.
