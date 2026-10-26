#!/bin/sh
# The release is built against the lintux-modkit commit CI checks out (lintux-modkit.ref:
# "<tag> <commit>"): build.sh refuses a sibling checkout on another commit or with local
# changes, so a local build cannot carry other modkit code than the tested one.
#   sh scripts/modkit-check.sh [<modkit dir>]     MODKIT_DEV=1 allows it, with a warning (not for a release)
set -eu
cd "$(dirname "$0")/.."
M="${1:-../lintux-modkit}"
read -r TAG COMMIT < lintux-modkit.ref
HEAD=$(git -C "$M" rev-parse HEAD)
DIRTY=$(git -C "$M" status --porcelain)
if [ "$HEAD" = "$COMMIT" ] && [ -z "$DIRTY" ]; then
  echo "lintux-modkit $TAG ($COMMIT)"
  exit 0
fi
CHANGES=""
[ -z "$DIRTY" ] || CHANGES=" with local changes"
MSG="lintux-modkit at $HEAD$CHANGES, not $TAG ($COMMIT) from lintux-modkit.ref"
if [ "${MODKIT_DEV:-}" = 1 ]; then
  echo "WARNING: $MSG — development build (MODKIT_DEV=1), not for a release" >&2
  exit 0
fi
echo "refused: $MSG; check out $TAG in $M, or set MODKIT_DEV=1 for a development build" >&2
exit 1
