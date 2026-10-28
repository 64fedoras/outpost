#!/bin/sh
# Prints the version the next build gets. Usage: next-version.sh [proto.go]
# Base = `const Version` in proto.go (a human sets the minor/major there, e.g. 0.9.0 or 1.0.0).
# Latest = highest v<X.Y.Z> git tag. Result = the higher of Base and Latest with its patch + 1,
# so merges after v0.8.0 give 0.8.1, 0.8.2, ... and a deliberate bump to 0.9.0 wins at once.
# After patch 19 the minor rolls on its own (0.8.19 -> 0.9.0 -> ... -> 0.9.19 -> 0.10.0); majors are only ever set by hand.
set -e
P=${1:-proto.go}
BASE=$(sed -n 's/^const Version = "\(.*\)"/\1/p' "$P")
LAST=$(git tag -l 'v[0-9]*.[0-9]*.[0-9]*' | sed 's/^v//' | sort -t. -k1,1n -k2,2n -k3,3n | tail -n 1)
if [ -n "$LAST" ]; then
  IFS=. read MA MI PA <<EOT
$LAST
EOT
  if [ "$PA" -ge 19 ]; then NEXT="$MA.$((MI + 1)).0"; else NEXT="$MA.$MI.$((PA + 1))"; fi
  HI=$(printf '%s\n%s\n' "$BASE" "$NEXT" | sort -t. -k1,1n -k2,2n -k3,3n | tail -n 1)
else
  HI=$BASE
fi
echo "$HI"
