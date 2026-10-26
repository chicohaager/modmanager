#!/bin/sh
# Cases for modkit-check.sh against a throwaway lintux-modkit repository.
set -u
cd "$(dirname "$0")/.."
T=$(mktemp -d); trap 'rm -rf "${T:?}"' EXIT
git init -q "$T/m" && git -C "$T/m" -c user.name=t -c user.email=t@example.invalid commit -q --allow-empty -m a
A=$(git -C "$T/m" rev-parse HEAD)
git -C "$T/m" -c user.name=t -c user.email=t@example.invalid commit -q --allow-empty -m b
B=$(git -C "$T/m" rev-parse HEAD)
cp lintux-modkit.ref "$T/ref.keep"
F=0
check() { # name, want rc, env, text the output must contain
  out=$(env $3 sh scripts/modkit-check.sh "$T/m" 2>&1); rc=$?
  case "$rc:$out" in "$2:"*"$4"*) echo "ok   $1 (rc=$rc)" ;; *) echo "FAIL $1: rc=$rc, want $2 and \"$4\": $out"; F=$((F + 1)) ;; esac
}
echo "vX $B" > lintux-modkit.ref; check "sibling on the pinned commit" 0 "" "lintux-modkit vX"
echo "vX $A" > lintux-modkit.ref; check "sibling on another commit" 1 "" "refused: lintux-modkit at"
check "another commit with MODKIT_DEV=1" 0 "MODKIT_DEV=1" "WARNING"
echo "vX $B" > lintux-modkit.ref; touch "$T/m/stray"; check "pinned commit with local changes" 1 "" "with local changes"
cp "$T/ref.keep" lintux-modkit.ref
[ $F = 0 ] && echo "✓ modkit-check: 4 cases" || echo "✗ modkit-check: $F failed"
exit $F
