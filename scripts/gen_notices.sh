#!/bin/sh
# Regenerates THIRD_PARTY_NOTICES.txt from vendor/ and bundled/. Run after `go mod vendor`.
set -e
cd "$(dirname "$0")/.."
out=THIRD_PARTY_NOTICES.txt
{
cat <<'H'
Outpost third-party notices
===========================

Outpost itself is licensed under the GNU Affero General Public License v3.0 (see LICENSE).
Outpost includes, or downloads and runs, the components below. Each is under its own licence,
and each licence is compatible with the AGPL-3.0. This is not legal advice.

H
for m in $(grep '^# ' vendor/modules.txt | awk '{print $2 "@" $3}'); do
  mod=${m%@*}; ver=${m#*@}
  echo "-------------------------------------------------------------------------------"
  echo "Go module $mod $ver (vendored in vendor/, compiled into the binary)"
  echo "-------------------------------------------------------------------------------"
  echo
  for f in vendor/$mod/LICENSE vendor/$mod/LICENSE.txt vendor/$mod/PATENTS; do
    [ -f "$f" ] || continue
    echo "[$(basename "$f")]"; echo; cat "$f"; echo
  done
done
cat <<'H'
-------------------------------------------------------------------------------
libgomp.so.1 (GCC OpenMP runtime; bundled in Linux x86-64 builds, unmodified)
-------------------------------------------------------------------------------

License: GPL-3.0-or-later WITH GCC-exception-3.1 (GCC Runtime Library Exception).
Taken unmodified from Debian's libgomp1 package. Corresponding source code: the GCC source
package, obtainable with `apt source gcc` on Debian/Ubuntu, or from https://gcc.gnu.org/ and
https://sources.debian.org/src/gcc-14/ . Outpost writes this library to disk only on machines
that lack it, and only llama.cpp loads it. The Debian copyright file follows.

H
cat bundled/LICENSE-libgomp.txt
echo
cat <<'H'
-------------------------------------------------------------------------------
llama.cpp (MIT; downloaded and run by nodes, not part of the Outpost binary)
-------------------------------------------------------------------------------

Outpost downloads official llama.cpp release builds from https://github.com/ggml-org/llama.cpp
and runs them as separate programs (llama-server, rpc-server). Source: that repository.

H
cat bundled/LICENSE-llama.cpp.txt
} > "$out.new"
mv "$out.new" "$out"
