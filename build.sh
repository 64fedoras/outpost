#!/bin/sh
# Builds Outpost for every platform into DIR (default ../dist/outpost-<version>).
# Windows gets version info + a manifest (winres/) and keeps its symbol table:
# stripped, metadata-free Go executables are a common antivirus false positive.
set -e
cd "$(dirname "$0")"
V=$(sed -n 's/^const Version = "\(.*\)"/\1/p' proto.go)
D=${1:-../dist/outpost-$V}
sh winres/gen.sh
mkdir -p "$D/bin"
for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
  o=${t%/*}; a=${t#*/}; ext=""; flags="-s -w"
  if [ "$o" = windows ]; then ext=.exe; flags=""; fi
  CGO_ENABLED=0 GOOS=$o GOARCH=$a go build -trimpath -ldflags="$flags" -o "$D/bin/outpost-$o-$a$ext" .
done
sh winres/sign.sh "$D/bin/outpost-windows-amd64.exe"
echo "built Outpost $V into $D"
