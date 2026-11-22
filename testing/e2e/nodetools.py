# "No node has Chromium/OpenSCAD" notices on the Nodes page: recommended node, Install button, Not now.
# The controller state is patched in the browser (the rig's node already decides what it has), and the
# install POST is intercepted, so nothing is downloaded. Run after `sh testing/e2e/run.sh up`.
import asyncio, json, os, sys
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/nodetools/"
os.makedirs(S, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
        errs = []
        for name, scheme, size in [("desktop-light", "light", (1280, 800)), ("desktop-dark", "dark", (1280, 800)), ("phone", "dark", (390, 844))]:
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": size[0], "height": size[1]}, color_scheme=scheme)
            posts = []
            async def state(r):
                resp = await r.fetch()
                if resp.status != 200:
                    await r.fulfill(response=resp); return
                st = await resp.json()
                nid = st["nodes"][0]["id"]
                st["tool_recs"] = [
                    {"tool": "chromium", "label": "Chromium", "why": "Screenshots and visual review of web tasks need Chromium.", "size": "about 170 MB",
                     "nodes": [{"id": nid, "name": "Node01"}, {"id": "other", "name": "Node03"}]},
                    {"tool": "openscad", "label": "OpenSCAD", "why": "The New 3D model page needs OpenSCAD to render files.", "size": "about 60 MB", "nodes": [], "reason": "No online exec node can take it (Linux or Windows, 64-bit Intel/AMD)."}]
                await r.fulfill(response=resp, json=st)
            async def toolchain(r):
                if r.request.method == "POST":
                    posts.append(r.request.post_data_json)
                    await r.fulfill(status=202, json={"ok": True})
                else:
                    await r.continue_()
            await ctx.route("**/api/state/events*", lambda r: r.abort())
            await ctx.route("**/api/state", state)
            await ctx.route("**/api/nodes/*/toolchain", toolchain)
            pg = await ctx.new_page()
            pg.on("pageerror", lambda e: errs.append(str(e)))
            await pg.goto(U + "/"); await pg.fill("#pw", pw); await pg.click("button[type=submit]")
            await pg.wait_for_selector("#side-tasks"); await pg.wait_for_timeout(1000)
            await pg.goto(U + "/#/nodes"); await pg.wait_for_selector(".upd-banner"); await pg.wait_for_timeout(600)
            assert await pg.locator(".upd-banner", has_text="No node has Chromium").count() == 1
            assert await pg.locator(".upd-banner", has_text="No online exec node").count() == 1
            assert "(recommended)" in await pg.locator("[data-rec-node=chromium] option").first.inner_text()
            await pg.screenshot(path=S + name + ".png")
            if name == "desktop-light":
                await pg.click("[data-act=rec-install][data-tool=chromium]"); await pg.wait_for_timeout(500)
                assert posts and posts[0]["tool"] == "chromium", posts
                await pg.click("[data-act=rec-dismiss][data-tool=chromium]"); await pg.wait_for_timeout(300)
                assert await pg.locator(".upd-banner", has_text="No node has Chromium").count() == 0
            await ctx.close()
        await b.close()
        assert not errs, errs
        print("nodetools ok; shots in", S)

asyncio.run(main())
