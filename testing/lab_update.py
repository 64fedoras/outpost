"""Lab stage "update": update, roll back and update again ONE node, through the controller API.

This stage CHANGES the cluster: the chosen node restarts three times. It runs only when
OUTPOST_LAB_STAGES contains "update" and OUTPOST_LAB_UPDATE_NODE names the node; the Gitea
workflow offers it only on a manual "Run workflow", never on the nightly schedule.

What it does, on the node named in OUTPOST_LAB_UPDATE_NODE:
  0. checks the node is online and idle and that the controller holds a release for its platform
     (upload it in the Updates tab, or let the update repository fetch it) at or above the node's version
  1. if the node is older than that release: update it (Update button)
  2. roll it back to the version before (Rollback button)
  3. update it again to the release
After each step the node must come back online on the expected version within
OUTPOST_LAB_UPDATE_TIMEOUT seconds (default 600), still list every model file it had, keep its
node settings (name, exec, max jobs, sandbox, idle unload, labels, storage limit, hours) and, if a
model was loaded, load it again on its own within 5 minutes.

A rollback makes the controller hold auto-install at the version rolled back from. The stage puts
the hold back to what it was before, even when a step fails. The node ends on the release version.
It never touches the controller's own version or any other node.
"""
import os
import time

SETTINGS = ("name", "exec_enabled", "max_jobs_override", "sandbox", "idle_unload", "labels",
            "storage_limit_gb", "storage_full", "hours", "yield_gpu")


def _node(lab, name):
    code, st = lab.call("GET", "/api/state")
    if code != 200 or not isinstance(st, dict):
        return None, None
    for n in st.get("nodes") or []:
        if n.get("name") == name:
            return n, st
    return None, st


def _release(st, n):
    h = n.get("hello") or {}
    plat = f"{h.get('os')}-{h.get('arch')}"
    for r in st.get("releases") or []:
        if r.get("platform") == plat:
            return plat, r.get("version")
    return plat, None


def _snapshot(n):
    h = n.get("hello") or {}
    hb = n.get("heartbeat") or {}
    return {
        "version": h.get("agent_version"),
        "models": {m.get("name") for m in h.get("models") or [] if m.get("name")},
        "loaded": hb.get("loaded_model") or h.get("loaded_model") or "",
        "settings": {k: n.get(k) for k in SETTINGS},
    }


def _wait_version(lab, name, want, timeout):
    start = time.time()
    seen_offline = False
    while time.time() - start < timeout:
        n, _ = _node(lab, name)
        if n:
            up = n.get("update") or {}
            if up.get("state") == "failed" and up.get("version") == want:
                lab.fail(f"{name}: switching to {want} failed: {up.get('error') or 'no reason given'}")
                return None
            if not n.get("online"):
                seen_offline = True
            elif (n.get("hello") or {}).get("agent_version") == want:
                lab.say("ok", f"{name}: back online on {want} after {int(time.time() - start)} s"
                              + ("" if seen_offline else " (no offline moment was seen)"))
                return n
        time.sleep(float(os.environ.get("OUTPOST_LAB_POLL", "5")))
    n, _ = _node(lab, name)
    have = (n or {}).get("hello", {}).get("agent_version")
    lab.fail(f"{name}: not online on {want} within {timeout} s (now {have}, "
             f"{'online' if (n or {}).get('online') else 'offline'})")
    return None


def _check_kept(lab, name, before, n):
    now = _snapshot(n)
    lost = before["models"] - now["models"]
    if lost:
        lab.fail(f"{name}: model files missing after the switch: {', '.join(sorted(lost))}")
    else:
        lab.say("ok", f"{name}: all {len(before['models'])} model file(s) still listed")
    changed = [k for k in SETTINGS if before["settings"].get(k) != now["settings"].get(k)]
    if changed:
        lab.fail(f"{name}: node settings changed: {', '.join(changed)}")
    else:
        lab.say("ok", f"{name}: node settings unchanged")
    if not before["loaded"]:
        return
    deadline = time.time() + 300
    while time.time() < deadline:
        n2, _ = _node(lab, name)
        if n2 and _snapshot(n2)["loaded"] == before["loaded"]:
            lab.say("ok", f"{name}: model {before['loaded']} loaded again")
            return
        time.sleep(10)
    lab.fail(f"{name}: model {before['loaded']} was not loaded again within 5 minutes")


def _step(lab, name, nid, action, want, before, timeout):
    lab.say("info", f"{name}: {action} to {want}")
    code, res = lab.call("POST", f"/api/nodes/{nid}/{action}")
    if code != 200:
        lab.fail(f"{name}: {action} returned {code}: {(res or {}).get('error', res)}")
        return None
    n = _wait_version(lab, name, want, timeout)
    if n:
        _check_kept(lab, name, before, n)
    return n


def run(lab, st):
    name = os.environ.get("OUTPOST_LAB_UPDATE_NODE", "").strip()
    if not name:
        lab.fail("stage update needs OUTPOST_LAB_UPDATE_NODE (the name of a spare node)")
        return
    timeout = int(os.environ.get("OUTPOST_LAB_UPDATE_TIMEOUT", "600") or 600)
    n, st = _node(lab, name)
    if not n:
        lab.fail(f"node '{name}' not found")
        return
    if not n.get("online"):
        lab.fail(f"{name}: offline, nothing to update")
        return
    if n.get("running_jobs") or (n.get("update") or {}).get("state") in ("sent", "downloading", "installing"):
        lab.fail(f"{name}: busy (running jobs or an update in progress); try when it is idle")
        return
    plat, rel = _release(st, n)
    before = _snapshot(n)
    if not rel:
        lab.fail(f"{name}: the controller holds no release for {plat}; upload one in the Updates tab "
                 "or let the update repository fetch it")
        return
    lab.say("info", f"{name}: on {before['version']}, release for {plat} is {rel}, "
                    f"{len(before['models'])} model file(s), loaded: {before['loaded'] or 'none'}")
    hold = (st.get("github_updates") or {}).get("hold", "")
    order = _cmp(before["version"] or "0", rel)
    if order > 0:
        lab.fail(f"{name}: runs {before['version']}, newer than the release {rel}; nothing to test")
        return
    try:
        if order < 0:
            n = _step(lab, name, n["id"], "update", rel, before, timeout)
            if not n:
                return
        back = (n.get("hello") or {}).get("rollback_to")
        if not back:
            lab.fail(f"{name}: no older version installed to roll back to; update it once from an older "
                     "release first (the node keeps the previous copy)")
            return
        if not _step(lab, name, n["id"], "rollback", back, before, timeout):
            return
        _step(lab, name, n["id"], "update", rel, before, timeout)
    finally:
        _, st2 = _node(lab, name)
        now_hold = ((st2 or {}).get("github_updates") or {}).get("hold", "")
        if now_hold != hold:
            code, res = lab.call("POST", "/api/github-updates/hold", {"version": hold})
            if code == 200:
                lab.say("ok", f"update hold put back to {hold or 'none'} (the rollback had set {now_hold})")
            else:
                lab.fail(f"could not put the update hold back to {hold or 'none'} ({code}: {res}); "
                         f"auto-install skips {now_hold} and older until a newer release or a manual install")


def _cmp(a, b):
    def parts(v):
        out = []
        for p in v.lstrip("v").split("-")[0].split("."):
            out.append(int(p) if p.isdigit() else 0)
        return out + [0] * (4 - len(out))
    pa, pb = parts(a), parts(b)
    return (pa > pb) - (pa < pb)
