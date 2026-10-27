#!/bin/sh
# Stops test processes by executable name, never by matching command text
# (which would also match the shell running this script).
for p in $(pgrep -x outpost) $(pgrep -x llama-server); do kill "$p" 2>/dev/null; done
for p in $(pgrep -x python3); do
  if tr '\0' ' ' < /proc/$p/cmdline 2>/dev/null | grep -q "llama-fake/llama-server"; then kill "$p"; fi
done
sleep 1
