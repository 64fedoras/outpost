#!/bin/sh
# Re-vendors third-party code from local clones (this build machine has no Go
# module proxy). Usage: sh scripts/vendor_sync.sh /path/systray /path/dbus /path/x-sys
set -e
cd "$(dirname "$0")/.."
cp go.mod go.mod.orig
cat >> go.mod <<EOT

replace (
	fyne.io/systray => $1
	github.com/godbus/dbus/v5 => $2
	golang.org/x/sys => $3
)
EOT
go mod vendor
mv go.mod.orig go.mod
python3 - <<'PY'
import re
m=open('vendor/modules.txt').read()
m=re.sub(r' => /\S+','',m)
m='\n'.join(l for l in m.split('\n') if not re.fullmatch(r'# \S+',l))
open('vendor/modules.txt','w').write(m)
PY
echo vendored
