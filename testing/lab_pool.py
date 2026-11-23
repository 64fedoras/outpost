"""Lab stage "pool": memory pooling across two real nodes, through the controller API.

This stage CHANGES the cluster: it turns memory pooling on, loads a model on one node (the main
node) with a second node (the helper) lending its memory, and unloads both nodes' own models while
it runs. It runs only when OUTPOST_LAB_STAGES contains "pool"; the Gitea workflow offers it only on a
manual "Run workflow", and lab_smoke.py refuses it on a scheduled run.

llama.cpp's RPC protocol has no authentication or encryption: run this on a trusted LAN only.

Environment:
  OUTPOST_LAB_POOL_MAIN    name of the main node (required)
  OUTPOST_LAB_POOL_HELPER  name of the helper node (required)
  OUTPOST_LAB_POOL_MODEL   library model to load pooled (required, must be "ready" in the library).
                           Ideally one that fits main + helper together but not main alone.
  OUTPOST_LAB_POOL_TIMEOUT seconds the pooled load may take (default 1800: weights go over the LAN)

Steps, following /testing/README.md "Lab checks" and the pooling test plan:
  1. while pooling is off (if it is), a pooled load is refused
  2. pooling is switched on
  3. the model is loaded on main with the helper lending: main reports it loaded and pooled with
     the helper, the helper reports lending to main and an RPC address
  4. from the runner (not the main node) a connection to the helper's RPC port is closed at once
     by the gatekeeper (only the main node's address is allowed)
  5. one short chat answer from the pooled model, with the time it took
  6. pooling is switched off again: main unloads, the helper stops lending, the port closes
Afterwards the pooling setting is put back as it was and each node's own model (if it had one)
is loaded again with its last load settings, even when a step failed.
"""
import os
import socket
import time

POLL = float(os.environ.get("OUTPOST_LAB_POLL", "5"))


def _state(lab):
    code, st = lab.call("GET", "/api/state")
    return st if code == 200 and isinstance(st, dict) else {}


def _find(st, name):
    for n in st.get("nodes") or []:
        if (n.get("name") or "").lower() == name.lower():
            return n
    return None


def _loaded(n):
    hb = (n or {}).get("heartbeat") or {}
    return hb.get("loaded_model") or ((n or {}).get("hello") or {}).get("loaded_model") or ""


def _wait(lab, what, test, timeout):
    start = time.time()
    while time.time() - start < timeout:
        st = _state(lab)
        got = test(st)
        if got == "error":
            return None
        if got:
            lab.say("ok", f"{what} after {int(time.time() - start)} s")
            return got
        time.sleep(POLL)
    lab.fail(f"{what}: not seen within {timeout} s")
    return None


def _hostport(ep):
    if not ep or ":" not in ep:
        return None, None
    host, port = ep.rsplit(":", 1)
    return host.strip("[]"), int(port) if port.isdigit() else None


def _gatekeeper(lab, host, port):
    """The helper must close a connection from anywhere but the main node."""
    try:
        s = socket.create_connection((host, port), timeout=8)
    except ConnectionRefusedError:
        lab.say("ok", f"helper RPC port {port} refuses the runner outright")
        return
    except OSError as e:
        lab.say("warn", f"helper RPC port {port} not reachable from the runner ({e}); gatekeeper not tested")
        return
    s.settimeout(5)
    try:
        data = s.recv(16)
        if data == b"":
            lab.say("ok", f"helper gatekeeper closed the runner's connection to port {port}")
        else:
            lab.fail(f"helper RPC port {port} answered the runner with data: the allow-list let it in")
    except socket.timeout:
        lab.fail(f"helper RPC port {port} kept the runner's connection open: the allow-list let it in")
    except OSError:
        lab.say("ok", f"helper gatekeeper reset the runner's connection to port {port}")
    finally:
        s.close()


def _port_closed(host, port):
    try:
        with socket.create_connection((host, port), timeout=5):
            return False
    except OSError:
        return True


def _set_pooling(lab, on):
    code, res = lab.call("POST", "/api/settings", {"pooling_enabled": on})
    if code != 200:
        lab.fail(f"turning pooling {'on' if on else 'off'} returned {code}: {res}")
        return False
    return True


def _reload(lab, n, prefs, label, had):
    if not had:
        return
    if not prefs or not prefs.get("name"):
        lab.say("warn", f"{label}: had {had} loaded but no saved load settings; load it again by hand")
        return
    body = {k: prefs.get(k) for k in ("name", "ctx", "gpu_layers", "threads", "cache_type",
                                      "flash_attn", "draft", "parallel", "cpu_moe") if prefs.get(k)}
    code, res = lab.call("POST", f"/api/nodes/{n['id']}/load", body)
    if code != 200:
        lab.fail(f"{label}: could not load its own model {prefs['name']} again ({code}: {res})")
        return
    def serving(st):
        hb = (_find(st, n["name"]) or {}).get("heartbeat") or {}
        if hb.get("llama_error"):
            lab.fail(f"{label}: own model {prefs['name']} failed to start: {hb['llama_error'][:300]}")
            return "error"
        return _loaded(_find(st, n["name"])) == prefs["name"] and hb.get("inference_ready")

    # Loaded in the heartbeat is not enough (it shows before llama-server is up, and
    # a failed start comes later): it must be ready, and still ready 20 s on.
    if not _wait(lab, f"{label}: own model {prefs['name']} ready again", serving, 600):
        return
    time.sleep(20)
    if serving(_state(lab)):
        lab.say("ok", f"{label}: own model {prefs['name']} still ready after 20 s")
    else:
        lab.fail(f"{label}: own model {prefs['name']} stopped being ready within 20 s of loading")


def run(lab, st):
    main_name = os.environ.get("OUTPOST_LAB_POOL_MAIN", "").strip()
    helper_name = os.environ.get("OUTPOST_LAB_POOL_HELPER", "").strip()
    model = os.environ.get("OUTPOST_LAB_POOL_MODEL", "").strip()
    timeout = int(os.environ.get("OUTPOST_LAB_POOL_TIMEOUT", "1800") or 1800)
    if not (main_name and helper_name and model):
        lab.fail("stage pool needs OUTPOST_LAB_POOL_MAIN, OUTPOST_LAB_POOL_HELPER and OUTPOST_LAB_POOL_MODEL")
        return
    st = _state(lab)
    main, helper = _find(st, main_name), _find(st, helper_name)
    for label, n in (("main", main), ("helper", helper)):
        if not n:
            lab.fail(f"{label} node not found")
            return
        if not n.get("online") or n.get("running_jobs"):
            lab.fail(f"{n['name']}: offline or busy; pick idle online nodes")
            return
        if n.get("lending_to") or n.get("pool_names"):
            lab.fail(f"{n['name']}: already in a pool; dissolve it first")
            return
    code, lib = lab.call("GET", "/api/models")
    rec = next((m for m in ((lib or {}).get("models") or []) if m.get("name") == model), None)
    if not rec or rec.get("status") != "ready":
        lab.fail(f"model '{model}' is not ready in the library")
        return
    code, settings = lab.call("GET", "/api/settings")
    was_on = bool((settings or {}).get("pooling_enabled"))
    before = {
        "main": (main, main.get("load_prefs"), _loaded(main)),
        "helper": (helper, helper.get("load_prefs"), _loaded(helper)),
    }
    gb = (rec.get("bytes_total") or 0) / 2**30
    lab.say("info", f"pool: {model} ({gb:.1f} GiB) on {main['name']} with {helper['name']} lending; "
                    f"pooling was {'on' if was_on else 'off'}; own models: "
                    f"{_loaded(main) or 'none'} / {_loaded(helper) or 'none'}")
    ep_host = ep_port = None
    try:
        if not was_on:
            code, res = lab.call("POST", f"/api/nodes/{main['id']}/load", {"name": model, "pool": [helper["id"]]})
            if code == 409:
                lab.say("ok", "pooled load refused while pooling is off")
            else:
                lab.fail(f"pooled load while pooling is off returned {code}, expected 409")
                return
            if not _set_pooling(lab, True):
                return
        code, res = lab.call("POST", f"/api/nodes/{main['id']}/load", {"name": model, "pool": [helper["id"]]})
        if code != 200:
            lab.fail(f"pooled load returned {code}: {(res or {}).get('error', res)}")
            return
        started = time.time()

        def ready(st):
            m, h = _find(st, main["name"]), _find(st, helper["name"])
            hb = (m or {}).get("heartbeat") or {}
            if (m or {}).get("heartbeat", {}).get("llama_error"):
                lab.fail(f"{main['name']}: {hb.get('llama_error')}")
                return "error"
            if _loaded(m) == model and hb.get("inference_ready") and helper["name"] in (m.get("pool_names") or []) \
                    and (h or {}).get("lending_to") == main["name"]:
                return h
            return None

        h = _wait(lab, f"{main['name']} serving {model} pooled with {helper['name']}", ready, timeout)
        if not h:
            return
        load_s = int(time.time() - started)
        ep_host, ep_port = _hostport((h.get("heartbeat") or {}).get("rpc_endpoint"))
        if ep_port:
            lab.say("ok", f"{helper['name']} lends memory on port {ep_port}")
            _gatekeeper(lab, ep_host, ep_port)
        else:
            lab.fail(f"{helper['name']} is lending but reports no RPC address")
        t0 = time.time()
        code, res = lab.call("POST", "/v1/chat/completions", {
            "model": model, "max_tokens": 32, "temperature": 0,
            "messages": [{"role": "user", "content": "Reply with the single word: ok"}]}, timeout=600)
        if code == 200:
            try:
                text = res["choices"][0]["message"]["content"].strip()
            except (KeyError, IndexError, TypeError, AttributeError):
                text = ""
            toks = ((res or {}).get("usage") or {}).get("completion_tokens") or 0
            dt = time.time() - t0
            if text:
                lab.say("ok", f"pooled model answered {text[:40]!r} in {dt:.1f} s "
                              f"({toks / dt:.1f} tok/s incl. prompt); load took {load_s} s")
            else:
                lab.fail("pooled model answered with empty text")
        else:
            lab.fail(f"chat with the pooled model returned {code}: {res}")
    finally:
        # Step 6 and the clean-up: switching pooling off dissolves every pool.
        if _set_pooling(lab, False):
            _wait(lab, "pool dissolved (main unloaded, helper stopped lending)",
                  lambda st: (not (_find(st, helper["name"]) or {}).get("lending_to")
                              and not ((_find(st, helper["name"]) or {}).get("heartbeat") or {}).get("rpc_endpoint")
                              and _loaded(_find(st, main["name"])) != model) or None, 180)
            if ep_port:
                time.sleep(3)
                if _port_closed(ep_host, ep_port):
                    lab.say("ok", f"helper RPC port {ep_port} closed")
                else:
                    lab.fail(f"helper RPC port {ep_port} still open after the pool ended")
        if was_on:
            _set_pooling(lab, True)
        lab.say("info", f"pooling setting put back to {'on' if was_on else 'off'}")
        for label in ("main", "helper"):
            n, prefs, had = before[label]
            if not prefs or prefs.get("name") != had:
                prefs = {"name": had}  # no saved settings for that model: the node's defaults
            _reload(lab, n, prefs, n["name"], had)
