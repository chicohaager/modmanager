#!/bin/sh
# The release is built against the lintux-modkit commit of lintux-modkit.ref ("<tag> <commit>").
# lintux-modkit reaches the build twice: the sysext scripts from <modkit dir> (LINTUX_MODKIT), the Go
# packages from the directory go.mod's replace or a go.work names — LINTUX_MODKIT does not move those,
# so the check asks go list which one it compiles. Both must be that commit without local changes,
# else a local build carries other modkit code than the tested one.
#   sh scripts/modkit-check.sh [<modkit dir>]     (default: $LINTUX_MODKIT, else ../lintux-modkit)
#       MODKIT_DEV=1 allows it, with a warning (not for a release)
set -eu
cd "$(dirname "$0")/.."
M="${1:-${LINTUX_MODKIT:-../lintux-modkit}}"   # LINTUX_MODKIT as in the module repos' build.sh
read -r TAG COMMIT < lintux-modkit.ref
GO_DIR=$(go list -m -f '{{.Dir}}' github.com/chicohaager/lintux-modkit)
FAULT=""
SEEN=""
for d in "$M" "$GO_DIR"; do
  real=$(cd "$d" 2>/dev/null && pwd -P) || real=$d
  [ "$real" != "$SEEN" ] || continue   # the scripts' directory is often the one Go compiles
  SEEN=$real
  HEAD=$(git -C "$d" rev-parse HEAD 2>/dev/null) || HEAD="no git checkout"
  if [ "$HEAD" != "$COMMIT" ]; then
    FAULT="$FAULT, $d at $HEAD"
  elif [ -n "$(git -C "$d" status --porcelain)" ]; then
    FAULT="$FAULT, $d with local changes"
  fi
done
if [ -z "$FAULT" ]; then
  echo "lintux-modkit $TAG ($COMMIT): scripts $M, Go $GO_DIR"
  exit 0
fi
MSG="lintux-modkit not $TAG ($COMMIT) from lintux-modkit.ref:${FAULT#,} (scripts: $M, Go: $GO_DIR)"
if [ "${MODKIT_DEV:-}" = 1 ]; then
  echo "WARNING: $MSG — development build (MODKIT_DEV=1), not for a release" >&2
  exit 0
fi
echo "refused: $MSG; check out $TAG there, or set MODKIT_DEV=1 for a development build" >&2
exit 1
