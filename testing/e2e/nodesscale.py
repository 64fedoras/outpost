# Nodes page with many nodes: filter bar, summary strip, update banner, compact grid and list, offline strip,
# recent jobs cap. The rig has one real node; the browser copies it to simulate N (the controller is not changed).
# Run after `sh testing/e2e/run.sh up` (the stub model and node must be online). N=25 by default.
import asyncio, os, json, re
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/nodesscale/"
os.makedirs(S, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
N = int(os.environ.get("N", "25"))
pw = open(E + "pw.txt").read().strip()
NAMES = ["gpu-desk", "win-gamer", "bigram", "mac-mini", "test-vm", "lab", "rack", "nuc", "pi", "studio"]

def many(st, n):
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

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
        errs = []
        for scheme in ["dark", "light"]:
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1440, "height": 900}, color_scheme=scheme)
            count = {"n": 1}
            async def route(r):
                resp = await r.fetch(); st = await resp.json()
                await r.fulfill(response=resp, json=many(st, count["n"]))
            await ctx.route("**/api/state/events*", lambda r: r.abort())
            await ctx.route("**/api/state", route)
            pg = await ctx.new_page()
            pg.on("pageerror", lambda e: errs.append(str(e)))
            pg.on("console", lambda m: errs.append(m.text) if m.type == "error" and "ERR_FAILED" not in m.text and "401" not in m.text else None)
            await pg.goto(U + "/"); await pg.fill("#pw", pw); await pg.click("button[type=submit]")
            await pg.wait_for_selector("#side-tasks"); await pg.wait_for_timeout(1500)
            # one node: no filter bar, tiles as before in a strip, full cards
            await pg.goto(U + "/#/nodes"); await pg.wait_for_selector(".gc"); await pg.wait_for_timeout(800)
            assert await pg.locator("#node-filter").is_hidden(), "filter bar must stay hidden for a small cluster"
            assert await pg.locator(".strip > div").count() == 6
            assert await pg.locator(".gc.dense").count() == 0
            # many nodes
            count["n"] = N
            await pg.evaluate("refresh()"); await pg.wait_for_selector(".gc.dense"); await pg.wait_for_timeout(1000)
            assert await pg.locator("#node-filter").is_visible(), "filter bar shows at N nodes"
            total = await pg.locator(".gc").count() + await pg.locator(".off-chip").count()
            assert total == N, f"every node is shown once: {total} != {N}"
            assert await pg.locator(".off-chip").count() > 0, "offline nodes collapse into the strip"
            assert await pg.locator(".gc .gc-sparks").count() == 0
            ub = await pg.locator(".upd-banner").count()
            assert ub == 1, "one update banner for many outdated nodes"
            assert await pg.locator(".gc .nv-note").filter(has_text="is available").count() == 0, "no per-card update notes while the banner shows"
            await pg.click(".upd-banner [data-chip=update]"); await pg.wait_for_timeout(300)
            assert 0 < await pg.locator(".gc").count() < N, "Show them filters to nodes that can update"
            await pg.click("[data-chip=all]"); await pg.wait_for_timeout(200)
            await pg.screenshot(path=S + f"grid-{scheme}.png", full_page=True)
            # search
            await pg.fill("#nf-q", "lab")
            await pg.wait_for_timeout(300)
            names = await pg.locator(".gc .node-name").all_inner_texts()
            assert names and all("lab" in x.lower() for x in names), names
            assert await pg.locator("#nf-q").evaluate("e => e === document.activeElement"), "typing keeps focus"
            assert "Showing" in await pg.locator(".nf-count").inner_text()
            await pg.fill("#nf-q", "zzzz"); await pg.wait_for_timeout(300)
            assert "No nodes match" in await pg.locator("#nodes").inner_text()
            await pg.click("[data-act=node-clear]"); await pg.wait_for_timeout(300)
            assert await pg.locator("#nf-q").input_value() == "", "clear filters empties the box"
            # chips
            await pg.click("[data-chip=offline]"); await pg.wait_for_timeout(300)
            assert await pg.locator(".gc").count() == 0 and await pg.locator(".off-chip").count() > 0
            await pg.click("[data-chip=gpu]"); await pg.wait_for_timeout(300)
            assert await pg.locator(".gc .nv-pill").count() == await pg.locator(".gc").count() > 0
            await pg.click("[data-chip=all]")
            await pg.select_option("#nf-sort", "name"); await pg.wait_for_timeout(300)
            names = await pg.locator(".gc .node-name").all_inner_texts()
            assert names == sorted(names, key=lambda s: [int(t) if t.isdigit() else t for t in re.split(r"(\d+)", s.lower())]), "sort by name"
            await pg.select_option("#nf-sort", "status")
            # card details panel still opens from a dense card
            await pg.locator(".gc button[data-act=node-detail]").first.click(); await pg.wait_for_selector("#node-drawer:not(.hidden)")
            await pg.keyboard.press("Escape"); await pg.wait_for_timeout(200)
            # list view
            await pg.click("[data-node-view=list]"); await pg.wait_for_selector(".nv-box.dense"); await pg.wait_for_timeout(500)
            assert await pg.locator(".nv-row.dn:not(.nv-head)").count() == N
            h = await pg.locator(".nv-row.dn:not(.nv-head)").first.evaluate("e => e.getBoundingClientRect().height")
            assert h <= 40, f"compact list rows are one line: {h}"
            await pg.screenshot(path=S + f"list-{scheme}.png", full_page=True)
            await pg.locator(".nv-row.dn:not(.nv-head)").first.click(); await pg.wait_for_selector(".nv-exp"); await pg.locator(".nv-row.dn:not(.nv-head)").first.click()
            await pg.click("[data-node-view=grid]")
            # jobs
            rows = await pg.locator("#jobs tbody tr").count()
            assert rows == 8, f"jobs start at 8 rows: {rows}"
            await pg.click("[data-act=jobs-more]"); await pg.wait_for_timeout(200)
            assert await pg.locator("#jobs tbody tr").count() == 32
            await pg.select_option("#jobs-status", "failed"); await pg.wait_for_timeout(200)
            assert "No jobs match" in await pg.locator("#jobs").inner_text() or await pg.locator("#jobs .pill.st-failed").count() > 0
            await pg.select_option("#jobs-status", "")
            await ctx.close()
        # phone
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 390, "height": 844}, color_scheme="dark", is_mobile=True, has_touch=True, device_scale_factor=2)
        async def route2(r):
            resp = await r.fetch(); st = await resp.json(); await r.fulfill(response=resp, json=many(st, N))
        await ctx.route("**/api/state/events*", lambda r: r.abort()); await ctx.route("**/api/state", route2)
        pg = await ctx.new_page(); pg.on("pageerror", lambda e: errs.append(str(e)))
        await pg.goto(U + "/"); await pg.fill("#pw", pw); await pg.click("button[type=submit]"); await pg.wait_for_selector("#side-tasks", state="attached")
        await pg.goto(U + "/#/nodes"); await pg.wait_for_selector(".gc.dense"); await pg.wait_for_timeout(800)
        assert await pg.evaluate("document.documentElement.scrollWidth <= window.innerWidth + 1"), "no sideways page scroll on the phone"
        await pg.screenshot(path=S + "grid-phone.png")
        await ctx.close(); await b.close()
        assert not errs, errs
        print("nodesscale: ok", N, "nodes; banner shown:", bool(ub))
asyncio.run(main())
