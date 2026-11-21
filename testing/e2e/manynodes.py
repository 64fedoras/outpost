# Shared by the "many nodes" e2e scripts: the rig has one real node, so the browser copies it to simulate N
# (the controller is not changed). Use it from a Playwright route on /api/state, and abort /api/state/events.
import json
NAMES = ["gpu-desk", "win-gamer", "bigram", "mac-mini", "test-vm", "lab", "rack", "nuc", "pi", "studio"]

def many(st, n):
    """Return the /api/state document with n nodes (and n*3 recent jobs, at most 120)."""
    if not st.get("nodes") or n <= 1: return st
    base = st["nodes"][0]; out = []
    for i in range(n):
        x = json.loads(json.dumps(base)); x["id"] = base["id"] + (f"x{i}" if i else ""); x["name"] = f"{NAMES[i % 10]}-{i:02d}" if i else base["name"]
        if i:
            x["hello"]["hostname"] = x["name"]
            if i % 7 == 3: x["online"] = False; x["last_seen"] = "2026-11-19T10:00:00Z"
            if i % 4 == 1:
                x["hello"]["gpus"] = [{"name": "AMD Radeon RX 9070 XT", "vram_bytes": 16e9, "backend": "vulkan"}]; x["heartbeat"]["vram_used_bytes"] = [8e9]; x["heartbeat"]["gpu_util_pct"] = [40]
            if i % 5 == 2: x["hello"]["loaded_model"] = ""; x["heartbeat"]["loaded_model"] = ""
            if i % 3 == 0: x["labels"] = ["rack"]
            if i % 2 == 0 and x.get("online", True): x["update_to"] = "9.9.9"
            x["heartbeat"]["cpu_pct"] = (i * 13) % 100
        out.append(x)
    st["nodes"] = out
    if n > 1: st["labels"] = ["rack"]
    j = st.get("jobs") or [{"id": "j0", "status": "succeeded", "command": "Word Counter · tests", "created": "2026-11-20T01:00:00Z"}]
    if j: st["jobs"] = [dict(j[k % len(j)], id=f"{j[k % len(j)]['id']}-{k}", node_id=out[k % n]["id"], node_name=out[k % n]["name"]) for k in range(min(n * 3, 120))]
    return st

