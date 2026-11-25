# Nodes page with many nodes: filter bar, summary strip, update chips, compact list rows, details panel,
# recent jobs cap. The rig has one real node; the browser copies it to simulate N (the controller is not changed).
# Run after `sh testing/e2e/run.sh up` (the stub model and node must be online). N=25 by default.
import asyncio, os, re, sys
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/nodesscale/"
os.makedirs(S, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
N = int(os.environ.get("N", "25"))
pw = open(E + "pw.txt").read().strip()
from manynodes import many

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
            # one node: no filter bar, tiles as before in a strip, two-line list rows; no Grid view or view switch
            await pg.goto(U + "/#/nodes"); await pg.wait_for_selector(".nv-row"); await pg.wait_for_timeout(800)
            assert await pg.locator("#node-filter").is_hidden(), "filter bar must stay hidden for a small cluster"
            assert await pg.locator(".strip > div").count() == 6
            assert await pg.locator(".nv-box.dense").count() == 0
            assert await pg.locator("[data-node-view=grid]").count() == 0 and await pg.locator(".gc").count() == 0, "the Grid view is gone"
            assert await pg.locator("#node-view-toggle").is_hidden(), "no view switch unless the Detailed list is turned on"
            # many nodes: compact one-line rows grouped into Needs attention / Healthy / Offline
            count["n"] = N
            await pg.evaluate("refresh()"); await pg.wait_for_selector(".nv-box.dense"); await pg.wait_for_timeout(1000)
            assert await pg.locator("#node-filter").is_visible(), "filter bar shows at N nodes"
            rows = ".nv-row.dn:not(.nv-head)"
            assert await pg.locator(rows).count() == N, "every node is shown once"
            grps = await pg.locator(".nv-grp").all_inner_texts()
            assert any(g.lower().startswith("offline") for g in grps), grps
            h = await pg.locator(rows).first.evaluate("e => e.getBoundingClientRect().height")
            assert h <= 40, f"compact list rows are one line: {h}"
            assert await pg.locator(rows + " .upd-chip").count() > 0, "update chips by node names"
            await pg.click("[data-chip=update]"); await pg.wait_for_timeout(300)
            assert 0 < await pg.locator(rows).count() < N, "Can update filters to nodes that can update"
            await pg.click("[data-chip=all]"); await pg.wait_for_timeout(200)
            await pg.screenshot(path=S + f"list-{scheme}.png", full_page=True)
            # search
            await pg.fill("#nf-q", "lab")
            await pg.wait_for_timeout(300)
            names = await pg.locator(rows + " > b.nv-ell").all_inner_texts()
            assert names and all("lab" in x.lower() for x in names), names
            assert await pg.locator("#nf-q").evaluate("e => e === document.activeElement"), "typing keeps focus"
            assert "Showing" in await pg.locator(".nf-count").inner_text()
            await pg.fill("#nf-q", "zzzz"); await pg.wait_for_timeout(300)
            assert "No nodes match" in await pg.locator("#nodes").inner_text()
            await pg.click("[data-act=node-clear]"); await pg.wait_for_timeout(300)
            assert await pg.locator("#nf-q").input_value() == "", "clear filters empties the box"
            # chips
            await pg.click("[data-chip=offline]"); await pg.wait_for_timeout(300)
            grps = await pg.locator(".nv-grp").all_inner_texts()
            assert len(grps) == 1 and grps[0].lower().startswith("offline"), grps
            await pg.click("[data-chip=all]")
            await pg.select_option("#nf-sort", "name"); await pg.wait_for_timeout(300)
            await pg.select_option("#nf-sort", "status")
            # details panel opens from a row, steps with next, closes with Esc
            await pg.locator(rows).first.click(); await pg.wait_for_selector("#node-drawer:not(.hidden)")
            first = await pg.locator("#node-drawer .nd-title .node-name").inner_text()
            await pg.click("#node-drawer [aria-label='Next node']"); await pg.wait_for_timeout(200)
            assert await pg.locator("#node-drawer .nd-title .node-name").inner_text() != first, "next node"
            await pg.keyboard.press("Escape"); await pg.wait_for_timeout(200)
            assert await pg.locator("#node-drawer").is_hidden()
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
        await pg.goto(U + "/#/nodes"); await pg.wait_for_selector(".nv-box.dense"); await pg.wait_for_timeout(800)
        assert await pg.evaluate("document.documentElement.scrollWidth <= window.innerWidth + 1"), "no sideways page scroll on the phone"
        await pg.screenshot(path=S + "list-phone.png")
        await ctx.close(); await b.close()
        assert not errs, errs
        print("nodesscale: ok", N, "nodes")
asyncio.run(main())
