#!/bin/sh
# Build the modmanager sysext for ZimaOS.
#   sh build.sh [amd64|arm64]      (default: amd64)
# Output in dist/: modmanager-<arch>.raw (+ .sha256), for amd64 also modmanager.raw.
set -eu
cd "$(dirname "$0")"
ARCH="${1:-amd64}"
VERSION=$(sed -n 's/^\s*version\s*=\s*"\(.*\)".*/\1/p' cmd/modmanager/main.go)
[ -n "$VERSION" ] || { echo "version not found in cmd/modmanager/main.go" >&2; exit 1; }
WEB=raw/usr/share/casaos/www/modules/modmanager
echo "modmanager v$VERSION linux/$ARCH"

go vet ./...
go test -count=1 ./...
node --check "$WEB/app.js"

mkdir -p raw/usr/bin
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -ldflags="-s -w" -o raw/usr/bin/modmanager ./cmd/modmanager/

# Cache-bust the frontend with the version and keep the shown version in step.
sed -i -E "s/(styles\.css|app\.js)\?v=[0-9A-Za-z.-]+/\1?v=$VERSION/g; s/(id=\"app-version\">)v[0-9A-Za-z.-]+/\1v$VERSION/" "$WEB/index.html"

sh ../lintux-modkit/sysext/build-sysext.sh modmanager raw "$ARCH" dist "$VERSION"
