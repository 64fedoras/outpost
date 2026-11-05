"""Soak test for the e2e rig: many tasks, with the node and the controller
killed and restarted at random while they run. Every task must still finish.

    sh testing/e2e/run.sh up
    python3 testing/e2e/soak.py            # SOAK_TASKS=12 SOAK_SEED=7 to change

Uses only the HTTP API (no browser). A Claude-planned task does not resume on
its own after a controller restart (a restart never spends money by itself),
so the script presses Resume the way a user would.
"""
import http.cookiejar, json, os, random, signal, ssl, subprocess, sys, time, urllib.request

E = os.environ.get("OUTPOST_E2E_DIR", os.path.join(os.path.dirname(__file__), "run"))
U = "https://127.0.0.1:18460"
N = int(os.environ.get("SOAK_TASKS", "8"))
rnd = random.Random(int(os.environ.get("SOAK_SEED", "1")))
ctx = ssl.create_default_context(); ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE
jar = http.cookiejar.CookieJar()
op = urllib.request.build_opener(urllib.request.HTTPSHandler(context=ctx), urllib.request.HTTPCookieProcessor(jar))


def api(path, body=None, method=None):
    data = None if body is None else json.dumps(body).encode()
    r = urllib.request.Request(U + path, data=data, method=method or ("POST" if body is not None else "GET"),
                               headers={"Content-Type": "application/json"})
    with op.open(r, timeout=30) as resp:
        return json.loads(resp.read() or b"null")


def wait_up(timeout=60):
    for _ in range(timeout * 2):
        try:
            api("/api/login", {"password": open(os.path.join(E, "pw.txt")).read().strip()})
            return
        except Exception:
            time.sleep(0.5)
    sys.exit("controller did not come back")


def pids(role):
    out = []
    for p in os.listdir("/proc"):
        if not p.isdigit():
            continue
        try:
            cmd = open(f"/proc/{p}/cmdline", "rb").read().split(b"\0")
        except OSError:
            continue
        if len(cmd) > 1 and cmd[0].endswith(b"/outpost") or (cmd and cmd[0] == b"./outpost"):
            if len(cmd) > 1 and cmd[1] == role.encode():
                out.append(int(p))
    return out


def restart(role):
    for p in pids(role):
        os.kill(p, signal.SIGTERM)
    for _ in range(60):
        if not pids(role):
            break
        time.sleep(0.25)
    if role == "controller":
        args = ["./outpost", "controller", "--listen", "127.0.0.1:18460", "--advertise", U, "--data", os.path.join(E, "ctl")]
        log = "ctl.log"
    else:
        args = ["./outpost", "node", "--data", os.path.join(E, "node")]
        log = "node.log"
    subprocess.Popen(args, cwd=E, stdout=open(os.path.join(E, log), "a"), stderr=subprocess.STDOUT,
                     start_new_session=True, env=dict(os.environ, OUTPOST_ANTHROPIC_URL="http://127.0.0.1:18700"))
    if role == "controller":
        wait_up()


def status(tid):
    return api(f"/api/tasks/{tid}")["status"]


def wait_status(tid, want, timeout=240):
    end = time.time() + timeout
    while time.time() < end:
        try:
            st = status(tid)
        except Exception:
            time.sleep(1); continue
        if st in want:
            return st
        if st == "interrupted":
            api(f"/api/tasks/{tid}/resume", {})
        time.sleep(1)
    return None


def main():
    wait_up()
    failed = []
    for i in range(N):
        t = api("/api/tasks", {"prompt": f"Build a small word counter CLI, run {i}.", "planner": "claude", "cap_usd": 2})
        tid = t["id"]
        if wait_status(tid, {"awaiting_approval", "awaiting_answers"}) == "awaiting_answers":
            api(f"/api/tasks/{tid}/answers", {"answers": {}, "note": "use your defaults"})
            wait_status(tid, {"awaiting_approval"})
        api(f"/api/tasks/{tid}/approve", {})
        time.sleep(rnd.uniform(0.2, 2.5))
        chaos = rnd.choice(["none", "node", "controller", "both"])
        if chaos in ("node", "both"):
            restart("node")
        if chaos in ("controller", "both"):
            restart("controller")
        st = wait_status(tid, {"done", "failed", "cancelled"})
        print(f"task {i + 1}/{N} {tid}: chaos={chaos} -> {st}", flush=True)
        if st != "done":
            failed.append((tid, chaos, st))
    if failed:
        sys.exit(f"{len(failed)} task(s) did not finish: {failed}")
    print(f"soak passed: {N} tasks")


main()
