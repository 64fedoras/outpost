"""Lab stage "inspect": read-only checks of features that were never tried on real machines.

Imported by testing/lab_smoke.py when OUTPOST_LAB_STAGES contains "inspect". It changes nothing on the
cluster. It reuses the helpers (call, say, fail) of the smoke module, which is passed in as `lab`.

Checks, per online node:
  versions   every node runs the controller's version (a node on another version fails a run;
             nodes that are mid-update are reported, not failed)
  pick-best  GET /api/nodes/{id}/load-options returns a ranked best pick; on a GPU node that pick
             must not be marked "no" (does not fit) and must be the only rank 1
  peer port  the node's model-sharing port (Settings > Hello "peer") accepts a TCP connection
             from the runner. This is the Windows firewall rule (port 8444) and any other node's
             firewall. Skip nodes the runner cannot route to with OUTPOST_LAB_SKIP_PEER=name,name
  macOS      a Mac node is online and its node log has no "blocked by the Local Network setting"
             line (the LaunchDaemon change). The Apple Silicon / Metal path is reported as untested
             when no arm64 Mac is in the cluster
  Windows    a Windows node is online, its log is readable, and the log shows no service errors
  update     "Check for updates" against the configured update repository answers (info only when
             no repository is set)

Nothing is installed, loaded, restarted or deleted.
"""
import os
import socket
import re

LNP_MARK = "blocking Outpost from the local network"


def _csv(name):
    return {s.strip() for s in os.environ.get(name, "").split(",") if s.strip()}


def _host(addr):
    # node address as the controller sees it: "192.0.2.5:51234" or "[fe80::1]:51234"
    if not addr:
        return ""
    if addr.startswith("["):
        return addr[1:addr.index("]")]
    return addr.rsplit(":", 1)[0]


def check_versions(lab, st, nodes):
    want = st.get("version")
    for n in nodes:
        name, h = n.get("name"), n.get("hello") or {}
        have = h.get("agent_version")
        up = (n.get("update") or {}).get("state")
        if n.get("update_to") or up in ("downloading", "installing", "restarting"):
            lab.say("info", f"{name}: update in progress ({up}); version not compared")
        elif have != want:
            lab.fail(f"{name}: runs {have}, controller runs {want}")
        else:
            lab.say("ok", f"{name}: version {have}")


def check_pick_best(lab, nodes):
    for n in nodes:
        name, nid, h = n.get("name"), n.get("id"), n.get("hello") or {}
        code, res = lab.call("GET", f"/api/nodes/{nid}/load-options")
        if code != 200 or not isinstance(res, dict):
            lab.fail(f"{name}: load-options returned {code}: {res}")
            continue
        opts = res.get("options") or []
        if not res.get("build_ok", True):
            lab.say("info", f"{name}: no llama.cpp build for this node yet, ranking skipped")
            continue
        if not opts:
            lab.say("warn", f"{name}: no model to pick from (empty library and catalog?)")
            continue
        top = [o for o in opts if o.get("rank") == 1]
        if len(top) != 1:
            lab.fail(f"{name}: expected exactly one best pick, got {len(top)}")
            continue
        best = top[0]
        gpus = h.get("gpus") or []
        vram = sum(g.get("vram_bytes", 0) for g in gpus)
        if best.get("fit") == "no":
            lab.fail(f"{name}: best pick {best.get('name')} is marked as not fitting")
        elif vram and best.get("fit") == "ram" and not best.get("unified"):
            lab.fail(f"{name}: has {vram >> 30} GiB of GPU memory but the best pick "
                     f"{best.get('name')} would run on the CPU")
        else:
            gpu = f", GPU {gpus[0].get('name')}" if gpus else ""
            lab.say("ok", f"{name}: best pick {best.get('name')} ({best.get('fit')}{gpu}); "
                          f"{best.get('speed_note') or 'no speed note'}")
        if h.get("loaded_model") and not any(o.get("evicts") or o.get("on_node") for o in opts):
            lab.say("info", f"{name}: a model is loaded; no option mentions unloading it")


def check_peer_ports(lab, nodes):
    skip = _csv("OUTPOST_LAB_SKIP_PEER")
    for n in nodes:
        name, h = n.get("name"), n.get("hello") or {}
        port = (h.get("peer") or {}).get("port")
        if not port:
            lab.say("info", f"{name}: serves no model files (no peer port)")
            continue
        if name in skip:
            lab.say("info", f"{name}: peer port check skipped (OUTPOST_LAB_SKIP_PEER)")
            continue
        host = _host(n.get("addr"))
        if not host:
            continue
        try:
            with socket.create_connection((host, int(port)), timeout=8):
                pass
            lab.say("ok", f"{name}: model-sharing port {port} reachable from the runner")
        except OSError as e:
            lab.fail(f"{name}: model-sharing port {port} not reachable from the runner ({e}). "
                     f"A node firewall or another subnet; add the name to OUTPOST_LAB_SKIP_PEER "
                     f"if the runner is not meant to reach it")


def _log(lab, n, source="node", lines=300):
    code, res = lab.call("GET", f"/api/nodes/{n.get('id')}/logs?source={source}&lines={lines}")
    if code != 200 or not isinstance(res, dict):
        return None
    return res.get("text") or ""


def check_macs(lab, nodes):
    macs = [n for n in nodes if (n.get("hello") or {}).get("os") == "darwin"]
    if not macs:
        lab.say("info", "no online Mac node; macOS checks skipped")
        return
    for n in macs:
        name, h = n.get("name"), n.get("hello") or {}
        text = _log(lab, n)
        if text is None:
            lab.fail(f"{name}: could not read its node log")
        elif LNP_MARK in text:
            lab.fail(f"{name}: node log says macOS is blocking the local network; "
                     "see System Settings > Privacy & Security > Local Network")
        else:
            lab.say("ok", f"{name}: online ({h.get('arch')}), no Local Network block in the log")
    if not any((n.get("hello") or {}).get("arch") == "arm64" for n in macs):
        lab.say("warn", "no Apple Silicon Mac online: arm64, Metal and unified memory stay untested")


def check_windows(lab, nodes):
    wins = [n for n in nodes if (n.get("hello") or {}).get("os") == "windows"]
    if not wins:
        lab.say("info", "no online Windows node; Windows checks skipped")
        return
    for n in wins:
        name = n.get("name")
        text = _log(lab, n)
        if text is None:
            lab.fail(f"{name}: could not read its node log")
            continue
        errs = [l for l in text.splitlines() if re.search(r"\bservice: .*(error|failed|cannot)", l, re.I)]
        if errs:
            lab.fail(f"{name}: service errors in node log: {errs[-1][:200]}")
        else:
            lab.say("ok", f"{name}: online, node log readable, no service errors")
    lab.say("warn", "Windows service survival across reboot/sign-out and Defender cannot be checked here")


def check_update_repo(lab):
    code, res = lab.call("POST", "/api/github-updates/check", timeout=60)
    if code == 200:
        lab.say("ok", f"update check answered (latest: {(res or {}).get('latest')})")
    elif code in (400, 502):
        lab.say("info", f"update check not available: {(res or {}).get('error', code)}")
    else:
        lab.fail(f"update check returned {code}: {res}")


def run(lab, st):
    nodes = [n for n in (st.get("nodes") or []) if n.get("online")]
    check_versions(lab, st, nodes)
    check_pick_best(lab, nodes)
    check_peer_ports(lab, nodes)
    check_macs(lab, nodes)
    check_windows(lab, nodes)
    check_update_repo(lab)
