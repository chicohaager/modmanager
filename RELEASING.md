# Releasing

0. **lintux-modkit.** `lintux-modkit.ref` names the tag and commit CI checks out; `build.sh` refuses a sibling `../lintux-modkit` on another commit or with local changes. A new lintux-modkit release: tag it there, put `<tag> <commit>` into `lintux-modkit.ref`, commit. (`MODKIT_DEV=1 sh build.sh` builds against any checkout, with a warning — never for a release.)

1. **Version and build.** Set `version` in `cmd/modmanager/main.go`, then build and commit everything the build changed (it writes the version into `index.html`):

   ```sh
   sh build.sh amd64
   git commit -am "<version>"
   git status --porcelain     # must be empty: the image is built from the work tree, so it matches this commit
   ```

   Install `dist/modmanager-amd64.raw` on a ZimaOS host and test it there — copy it as `/tmp/modmanager.raw`: `zpkg` takes the module name from the file name and refuses `modmanager-amd64.raw` (`module not pass validate`).

2. **Release.** Tag the tested commit and upload only architecture-specific images with their checksums:

   ```sh
   gh release create v<version> --target <commit> dist/modmanager-amd64.raw dist/modmanager-amd64.raw.sha256
   ```

   Do not upload the arch-less `dist/modmanager.raw`: when a release has no image for the host's CPU, the catalog takes an arch-less one (`internal/catalog/catalog.go`, `pick`), so an arm64 host would install the amd64 image. Add `dist/modmanager-arm64.raw` (+ `.sha256`) only after it was tested on arm64.

3. **Check that the release comes back from its tag** (same bytes, built with the lintux-modkit commit stamped in the image):

   ```sh
   ASSET_FILE=dist/modmanager-amd64.raw ../lintux-modkit/tools/check-release.sh . <commit> modmanager   # before uploading
   ../lintux-modkit/tools/check-release.sh . v<version> modmanager                                      # after the release
   ```

   It must end with `reproduced: …`. On 2026-10-26 a rebuild from the commit gave other bytes than the tested image, because `../lintux-modkit` had moved on.

4. **Check the release** against the host that runs it:

   ```sh
   python3 scripts/catalog-e2e.py --url http://<zimaos-host> --user <name> --password-file <file>
   ```

   It must end with `✓ … release image picked, installed version current` and leave the catalog as it was.

5. **Run the README install path as written**, line by line, in the ZimaOS terminal of a host, then check `/modmanager/health` for the new version. The catalog check above does not cover it: on 22.10.2026 the README told users to install the downloaded file without renaming it, and `zpkg` refused it.

   On a host that already runs the Module Manager, end with the README's "Update by hand" line instead of the first-install line: the download, checksum and rename lines are the same, and `zpkg install --force` refuses a wrongly named file with the same `module not pass validate` (ZIMAOS-KNOWLEDGE §7.4.1). It costs one `systemd-sysext refresh`; `sudo zpkg remove modmanager` followed by the first install costs two. Every refresh keeps one loop device per extension until the next reboot (on 15.11.2026 remove + install took the test host from 186 to 227). Run the first-install line itself only on a host without the module, or right before a planned reboot.
