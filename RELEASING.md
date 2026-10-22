# Releasing

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

3. **Check the release** against the host that runs it:

   ```sh
   python3 scripts/catalog-e2e.py --url http://<zimaos-host> --user <name> --password-file <file>
   ```

   It must end with `✓ … release image picked, installed version current` and leave the catalog as it was.

4. **Run the README install path as written**, line by line, in the ZimaOS terminal of a host (after `sudo zpkg remove modmanager`), then check `/modmanager/health` for the new version. The catalog check above does not cover it: on 22.10.2026 the README told users to install the downloaded file without renaming it, and `zpkg` refused it.
