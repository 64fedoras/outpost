#!/bin/sh
# Builds rsrc_windows_amd64.syso (version info + manifest) for the Windows
# binary. Go links any .syso file in the package automatically. Run after
# changing Version in proto.go:  sh winres/gen.sh
set -e
cd "$(dirname "$0")/.."
V=$(sed -n 's/^const Version = "\(.*\)"/\1/p' proto.go)
IFS=. read MA MI PA <<EOT
$V
EOT
T=$(mktemp -d)
sed "s/@V@/$V/g; s/@MA@/$MA/g; s/@MI@/$MI/g; s/@PA@/$PA/g" winres/outpost.rc.in > $T/outpost.rc
cp winres/outpost.manifest winres/outpost.ico $T/
(cd $T && llvm-rc /nologo /fo outpost.res outpost.rc)
llvm-cvtres /machine:x64 /out:rsrc_windows_amd64.syso $T/outpost.res >/dev/null
rm -rf $T
echo "rsrc_windows_amd64.syso: Outpost $V"
