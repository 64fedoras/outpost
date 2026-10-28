#!/bin/sh
# End-to-end rig: real controller + real node on 127.0.0.1, with a stub Claude API
# and a fake llama-server, so tasks run without a model or an API key.
#
#   sh testing/e2e/run.sh up      build, seed, start everything, join one node, load the fake model
#   sh testing/e2e/run.sh down    stop it all
#   sh testing/e2e/run.sh pw      print the admin password
#
# State lives in testing/e2e/run/ (git-ignored). UI: https://127.0.0.1:18460
set -e
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
RUN=$HERE/run
U=https://127.0.0.1:18460
export OUTPOST_E2E_DIR=$RUN
export OUTPOST_ANTHROPIC_URL=http://127.0.0.1:18700

case "${1:-up}" in
down)
  sh "$HERE/stop_all.sh"
  for p in $(pgrep -x python3); do
    tr '\0' ' ' < /proc/$p/cmdline 2>/dev/null | grep -q mock_claude.py && kill "$p"
  done
  exit 0 ;;
pw) cat "$RUN/pw.txt"; exit 0 ;;
esac

sh "$HERE/stop_all.sh" 2>/dev/null || true
rm -rf "$RUN"; mkdir -p "$RUN/ctl" "$RUN/node"
(cd "$ROOT" && CGO_ENABLED=0 go build -tags e2e -o "$RUN/outpost" .)
cp -r "$HERE/fixtures/ctl-seed/." "$RUN/ctl/"
# fake_llama_server.py is the source of the stub llama.cpp archive: repack it so the two never drift apart
RT=$RUN/ctl/runtime/linux-x64-cpu; ARC=llama-b99999-bin-ubuntu-x64.tar.gz
mkdir -p "$RUN/pack/llama-fake"; cp "$HERE/fake_llama_server.py" "$RUN/pack/llama-fake/llama-server"; chmod +x "$RUN/pack/llama-fake/llama-server"
tar czf "$RT/$ARC" -C "$RUN/pack" llama-fake
python3 - "$RT" "$ARC" <<'PY'
import hashlib, json, sys
d, arc = sys.argv[1], sys.argv[2]
b = open(d + "/" + arc, "rb").read(); m = json.load(open(d + "/runtime.json"))
m["sha256"], m["bytes"] = hashlib.sha256(b).hexdigest(), len(b)
json.dump(m, open(d + "/runtime.json", "w"))
PY

python3 "$HERE/mock_claude.py" 18700 >"$RUN/claude.log" 2>&1 &
cd "$RUN"
./outpost controller --listen 127.0.0.1:18460 --advertise "$U" --data "$RUN/ctl" >ctl.log 2>&1 &
for i in $(seq 1 30); do grep -q "no admin password yet" ctl.log 2>/dev/null && break; sleep 0.5; done
# first-run setup: the controller starts without a password; choose one through the setup endpoint
PW=e2e-test-password
echo "$PW" > pw.txt

C="curl -sk -b $RUN/jar -c $RUN/jar -H content-type:application/json"
$C -X POST $U/api/setup -d "{\"password\":\"$PW\"}" >/dev/null
$C -X POST $U/api/settings -d '{"anthropic_key":"sk-test-key","default_planner":"claude","default_cap_usd":2}' >/dev/null
TOK=$($C -X POST $U/api/tokens -d '{}' | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')

# the fake server rides inside the stub llama.cpp archive in fixtures/ctl-seed/runtime
./outpost node --join "$TOK" --data "$RUN/node" >node.log 2>&1 &
echo "waiting for the node to come online..."
for i in $(seq 1 60); do
  $C $U/api/state | grep -q '"online":true' && break; sleep 1
done
ID=$($C $U/api/state | python3 -c 'import json,sys;print(json.load(sys.stdin)["nodes"][0]["id"])')
$C -X POST $U/api/nodes/$ID/settings -d '{"exec_enabled":true}' >/dev/null
$C -X POST $U/api/nodes/$ID/load -d '{"name":"tiny-test","ctx":2048}' >/dev/null   # also installs the stub runtime
grep -q "model tiny-test loaded" node.log 2>/dev/null || for i in $(seq 1 30); do grep -q "model tiny-test loaded" node.log && break; sleep 1; done
echo "controller: $U   password: $PW   (pw.txt)"
echo "try: python3 testing/e2e/flow.py dark   (OUTPOST_E2E_DIR=$RUN)"
