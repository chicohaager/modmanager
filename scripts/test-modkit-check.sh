#!/bin/sh
# scripts/modkit-check.sh against throwaway git checkouts: a module that replaces lintux-modkit with a
# sibling directory, the scripts' directory given separately, as build.sh does with LINTUX_MODKIT.
#   sh scripts/test-modkit-check.sh      (POSIX sh: CI runs it with sh; with [[ ]] under sh every case passed, 17.11.2026)
set -eu
cd "$(dirname "$0")/.."
CHECK="$PWD/scripts/modkit-check.sh"
T=$(mktemp -d)
trap 'rm -rf "${T:?}"' EXIT
export GOFLAGS='' GOWORK=off GOTOOLCHAIN=local GIT_CONFIG_GLOBAL=/dev/null
ok=0

modkit() { # a lintux-modkit checkout with one commit; prints that commit
  mkdir -p "$1"
  printf 'module github.com/chicohaager/lintux-modkit\n\ngo 1.22\n' > "$1/go.mod"
  git -C "$1" init -q
  git -C "$1" add go.mod
  git -C "$1" -c user.name=t -c user.email=t@example.invalid commit -qm "$2"
  git -C "$1" rev-parse HEAD
}
module() { # a module dir next to $2 (the replace target), pinned to commit $3
  mkdir -p "$1/scripts"
  cp "$CHECK" "$1/scripts/"
  printf 'module example.invalid/m\n\ngo 1.22\n\nrequire github.com/chicohaager/lintux-modkit v0.0.0\n\nreplace github.com/chicohaager/lintux-modkit => %s\n' "$2" > "$1/go.mod"
  echo "v9.9.9 $3" > "$1/lintux-modkit.ref"
}
expect() { # expect <rc> <text in output> <case> -- <command>
  want=$1 text=$2 name=$3 rc=0
  shift 4
  out=$("$@" 2>&1) || rc=$?
  case "$out" in *"$text"*) found=1 ;; *) found=0 ;; esac
  if [ "$rc" != "$want" ] || [ "$found" != 1 ]; then
    echo "FAIL $name: rc=$rc (want $want), output: $out"
    exit 1
  fi
  ok=$((ok + 1))
}

GOOD=$(modkit "$T/good" good)
OLD=$(modkit "$T/old" old)
module "$T/m1" "$T/good" "$GOOD"
expect 0 "lintux-modkit v9.9.9 ($GOOD)" "scripts and Go on the pinned commit" -- sh "$T/m1/scripts/modkit-check.sh" "$T/good"
expect 1 "$T/old at $OLD" "scripts on another commit" -- sh "$T/m1/scripts/modkit-check.sh" "$T/old"
module "$T/m2" "$T/old" "$GOOD"
expect 1 "$T/old at $OLD" "scripts right, Go compiles another commit" -- sh "$T/m2/scripts/modkit-check.sh" "$T/good"
mkdir "$T/w"
printf 'go 1.22\n\nuse %s\n\nreplace github.com/chicohaager/lintux-modkit => %s\n' "$T/m2" "$T/good" > "$T/w/go.work"
expect 0 "Go $T/good" "a go.work moves Go to the pinned commit" -- env GOWORK="$T/w/go.work" sh "$T/m2/scripts/modkit-check.sh" "$T/good"
echo change >> "$T/good/go.mod"
expect 1 "$T/good with local changes" "local changes" -- sh "$T/m1/scripts/modkit-check.sh" "$T/good"
git -C "$T/good" checkout -q go.mod
out=$(cd "$T/m2" && sh scripts/modkit-check.sh ../old 2>&1) || true
n=$(printf '%s\n' "$out" | grep -o " at $OLD" | wc -l)
[ "$n" = 1 ] || { echo "FAIL the same directory twice (relative, absolute) is one finding: $n in $out"; exit 1; }
ok=$((ok + 1))
mkdir "$T/plain"
expect 1 "$T/plain at no git checkout" "scripts dir without git" -- sh "$T/m1/scripts/modkit-check.sh" "$T/plain"
expect 0 "lintux-modkit v9.9.9 ($GOOD)" "without an argument the scripts dir is LINTUX_MODKIT (build-pinned.sh)" -- env LINTUX_MODKIT="$T/good" sh "$T/m1/scripts/modkit-check.sh"
expect 0 "WARNING: lintux-modkit not v9.9.9" "MODKIT_DEV=1 lets it through, loudly" -- env MODKIT_DEV=1 sh "$T/m2/scripts/modkit-check.sh" "$T/good"
echo "test-modkit-check: $ok cases ok"
