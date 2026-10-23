# Changelog

Releases: https://github.com/chicohaager/modmanager/releases. Every release is amd64 only and tested on a ZimaBoard 2 with ZimaOS 1.8.0.

## Unreleased

- CI on every push and pull request: gofmt, go vet, `node --check` on the frontend, `go test -race`, govulncheck (advisory), build for amd64 and arm64 (arm64 built, not tested on hardware).
- README: screenshots of the three tabs; this changelog.
- Module Manager updates itself: an **Update to v…** button next to the version when a newer release exists; download, checksum and collision check as for any catalog module, the swap runs in a transient unit and rolls back to the previous image if the install fails (tested on ZimaOS 1.8.0: 0.2.4 → 0.2.5 in 8 s).
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
