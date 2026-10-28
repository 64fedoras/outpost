#!/bin/sh
# Makes a free self-signed code-signing certificate for your own machines.
# Writes outpost-signing.pfx (for OUTPOST_SIGN_PFX) and outpost-signing.cer;
# import the .cer into "Trusted Publishers" and "Trusted Root" on each Windows
# machine (certlm.msc). This stops "unknown publisher" prompts on those
# machines only; it does not help against Defender's machine-learning detections.
set -e
pass="${1:?usage: sh winres/selfsign.sh PFX_PASSWORD}"
openssl req -x509 -newkey rsa:3072 -nodes -days 1825 -subj "/CN=Outpost (self-signed)" \
  -addext "keyUsage=digitalSignature" -addext "extendedKeyUsage=codeSigning" \
  -keyout outpost-signing.key -out outpost-signing.cer
openssl pkcs12 -export -inkey outpost-signing.key -in outpost-signing.cer -out outpost-signing.pfx -passout "pass:$pass"
rm outpost-signing.key
echo "wrote outpost-signing.pfx and outpost-signing.cer (keep the .pfx private)"
