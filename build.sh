#!/bin/sh
# Builds Outpost. Works with Linux, macOS (bash 3.2 / BSD tools) and any POSIX sh.
#
#   sh build.sh [TARGET] [DIR]
#
# TARGET is `all` (default: the five release platforms), `current` (this machine), or one of
# linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64 (linux/amd64 also works).
# DIR defaults to ../dist/outpost-<version>; the binaries go in DIR/bin. If the first argument
# is not a target name it is taken as DIR, so `sh build.sh /some/dir` still builds everything.
#
# OUTPOST_VERSION=1.2.3 stamps that version into the binaries (CI does this for every release).
# proto.go and the Windows resource file are restored afterwards, so a build leaves the checkout
# clean. Without it the binaries report the base `const Version` in proto.go.
#
# Needs only Go (the version in go.mod) and, for the Windows version info, optionally llvm-rc +
# llvm-cvtres. Dependencies are vendored; nothing is downloaded.
# Windows gets version info + a manifest (winres/) and keeps its symbol table:
# stripped, metadata-free Go executables are a common antivirus false positive.
set -e
cd "$(dirname "$0")"

ALL="linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64"
die() { echo "build.sh: $*" >&2; exit 1; }

# --- Go present and new enough? -------------------------------------------------------------
command -v go >/dev/null 2>&1 || die "Go is not installed or not on PATH. Install Go $(sed -n 's/^go \([0-9.]*\).*/\1/p' go.mod) or newer from https://go.dev/dl/"
need=$(sed -n 's/^go \([0-9][0-9.]*\).*/\1/p' go.mod | head -n 1)
have=$(go version | sed -n 's/^go version go\([0-9][0-9.]*\).*/\1/p')
[ -n "$have" ] || die "cannot read the Go version from: $(go version)"
# Compare numerically, component by component (1.24.7 vs 1.24 vs 1.9).
older() { # older A B: true when version A < version B
  awk -v a="$1" -v b="$2" 'BEGIN{n=split(a,x,".");m=split(b,y,".");if(m>n)n=m
    for(i=1;i<=n;i++){p=x[i]+0;q=y[i]+0;if(p<q)exit 0;if(p>q)exit 1}exit 1}'
}
if older "$have" "$need"; then
  die "Go $have is too old: this source needs Go $need or newer (see https://go.dev/dl/)"
fi
# Never let Go fetch a toolchain or modules: everything needed is local.
export GOTOOLCHAIN=local GOFLAGS=-mod=vendor GOPROXY=off CGO_ENABLED=0

# --- arguments ------------------------------------------------------------------------------
TARGET=all
case "$1" in
  all|current|host) TARGET=$1; [ "$TARGET" = host ] && TARGET=current; shift ;;
  linux[-/]*|darwin[-/]*|windows[-/]*)
    t=$(printf '%s' "$1" | tr / -)
    case " $ALL " in *" $t "*) TARGET=$t; shift ;; *) die "unknown target: $1 (use all, current, or one of: $ALL)" ;; esac ;;
esac
if [ "$TARGET" = current ]; then
  TARGET=$(go env GOOS)-$(go env GOARCH)
  case " $ALL " in *" $TARGET "*) ;; *) die "this machine ($TARGET) is not a supported Outpost platform; supported: $ALL" ;; esac
fi
[ "$TARGET" = all ] && TARGETS=$ALL || TARGETS=$TARGET

# --- version stamping -----------------------------------------------------------------------
restore() {
  cp "$SAVE/proto.go" proto.go
  cp "$SAVE/syso" rsrc_windows_amd64.syso
  rm -rf "$SAVE"
}
# proto.go and the committed Windows resource file are put back on exit, whatever the build did to them.
SAVE=$(mktemp -d "${TMPDIR:-/tmp}/outpost-build.XXXXXX")
trap restore EXIT
cp proto.go "$SAVE/proto.go"
cp rsrc_windows_amd64.syso "$SAVE/syso"
if [ -n "$OUTPOST_VERSION" ]; then
  case "$OUTPOST_VERSION" in
    [0-9]*.[0-9]*.[0-9]*) ;;
    *) die "OUTPOST_VERSION must look like 1.2.3, got: $OUTPOST_VERSION" ;;
  esac
  sed "s/^const Version = \".*\"/const Version = \"$OUTPOST_VERSION\"/" "$SAVE/proto.go" > proto.go
fi
V=$(sed -n 's/^const Version = "\(.*\)"/\1/p' proto.go)
D=${1:-../dist/outpost-$V}

# --- Windows version info (needs llvm-rc and llvm-cvtres; otherwise the committed file is used)
case " $TARGETS " in
  *windows-amd64*)
    if command -v llvm-rc >/dev/null 2>&1 && command -v llvm-cvtres >/dev/null 2>&1; then
      sh winres/gen.sh
    else
      echo "note: llvm-rc/llvm-cvtres not found; using the committed rsrc_windows_amd64.syso (its version info may differ from $V)" >&2
    fi ;;
esac

# --- build ----------------------------------------------------------------------------------
sha256() { # sha256 FILE -> "<hash>  <name>"
  if command -v sha256sum >/dev/null 2>&1; then (cd "$(dirname "$1")" && sha256sum "$(basename "$1")")
  elif command -v shasum >/dev/null 2>&1; then (cd "$(dirname "$1")" && shasum -a 256 "$(basename "$1")")
  else (cd "$(dirname "$1")" && printf '%s  %s\n' "$(openssl dgst -sha256 "$(basename "$1")" | sed 's/^.*= //')" "$(basename "$1")"); fi
}
mkdir -p "$D/bin"
OUT=""
for t in $TARGETS; do
  o=${t%-*}; a=${t#*-}; ext=""; flags="-s -w -buildid="
  if [ "$o" = windows ]; then ext=.exe; flags="-buildid="; fi
  echo "building $o/$a ..."
  GOOS=$o GOARCH=$a go build -trimpath -ldflags="$flags" -o "$D/bin/outpost-$o-$a$ext" .
  OUT="$OUT $D/bin/outpost-$o-$a$ext"
done
case " $TARGETS " in *windows-amd64*) sh winres/sign.sh "$D/bin/outpost-windows-amd64.exe" ;; esac
echo "built Outpost $V into $D/bin"
echo "SHA-256:"
for f in $OUT; do sha256 "$f"; done
