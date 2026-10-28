#!/bin/sh
# Optionally Authenticode-signs the Windows exe. Does nothing (and never fails
# the build) unless a signing method is configured:
#   OUTPOST_SIGN_PFX=cert.pfx OUTPOST_SIGN_PASS=...   uses osslsigncode
#   OUTPOST_SIGN_CMD='jsign ... "$1"'                 any command; $1 is the exe
#     (for Azure Artifact Signing use a tool that supports it, such as jsign)
# Timestamp server: OUTPOST_SIGN_TS (default http://timestamp.digicert.com).
set -e
exe="$1"
if [ -n "$OUTPOST_SIGN_CMD" ]; then
  sh -c "$OUTPOST_SIGN_CMD" sign "$exe"
  echo "signed $exe with OUTPOST_SIGN_CMD"
elif [ -n "$OUTPOST_SIGN_PFX" ]; then
  command -v osslsigncode >/dev/null || { echo "osslsigncode not found; leaving $exe unsigned" >&2; exit 0; }
  osslsigncode sign -pkcs12 "$OUTPOST_SIGN_PFX" -pass "$OUTPOST_SIGN_PASS" -n Outpost \
    -ts "${OUTPOST_SIGN_TS:-http://timestamp.digicert.com}" -in "$exe" -out "$exe.signed"
  mv "$exe.signed" "$exe"
  echo "signed $exe with $OUTPOST_SIGN_PFX"
else
  echo "Windows exe left unsigned (set OUTPOST_SIGN_PFX or OUTPOST_SIGN_CMD to sign it)"
fi
