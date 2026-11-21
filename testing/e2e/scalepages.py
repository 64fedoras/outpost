# Updates and Models pages with many nodes: grouped update table, holders list, summarised fit badges, ⋯ menus.
# Run after `sh testing/e2e/run.sh up`. N=25 by default.
import asyncio, os, sys
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from manynodes import many
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/scalepages/"
os.makedirs(S, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
N = int(os.environ.get("N", "25"))
pw = open(E + "pw.txt").read().strip()

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
            await ctx.route("**/api/state/events*", lambda r: r.abort()); await ctx.route("**/api/state", route)
            pg = await ctx.new_page()
            pg.on("pageerror", lambda e: errs.append(str(e)))
            pg.on("console", lambda m: errs.append(m.text) if m.type == "error" and "ERR_FAILED" not in m.text and "401" not in m.text else None)
            await pg.goto(U + "/"); await pg.fill("#pw", pw); await pg.click("button[type=submit]"); await pg.wait_for_selector("#side-tasks"); await pg.wait_for_timeout(1500)
            # small cluster keeps the flat table and the full library row
            await pg.goto(U + "/#/updates"); await pg.wait_for_selector("#upd-table tbody tr"); await pg.wait_for_timeout(500)
            assert await pg.locator(".upd-g").count() == 0, "no groups for a small cluster"
            count["n"] = N
            await pg.evaluate("refresh()"); await pg.wait_for_selector(".upd-g"); await pg.wait_for_timeout(500)
            groups = await pg.locator(".upd-g").count()
            assert groups >= 2, f"online platforms and offline nodes are separate groups: {groups}"
            rows = await pg.locator("#upd-table tbody tr").count()
            assert rows < N, f"collapsed groups keep the table short: {rows} rows for {N} nodes"
            await pg.screenshot(path=S + f"updates-{scheme}.png", full_page=True)
            first = pg.locator(".upd-g").first
            assert await first.get_attribute("aria-expanded") == "true"
            await first.click(); await pg.wait_for_timeout(200)
            assert await pg.locator(".upd-g").first.get_attribute("aria-expanded") == "false", "clicking a group row collapses it"
            await pg.locator(".upd-g").first.click(); await pg.wait_for_timeout(200)
            # models
            await pg.goto(U + "/#/models"); await pg.wait_for_selector("#models-list tbody tr"); await pg.wait_for_timeout(800)
            assert await pg.locator("#models-list details.holders").count() == 1, "many holders fold into a count"
            assert "nodes" in await pg.locator("#models-list details.holders summary").inner_text()
            await pg.locator("#models-list details.holders summary").click(); await pg.wait_for_timeout(200)
            assert await pg.locator("#models-list .holders-list").is_visible()
            await pg.screenshot(path=S + f"models-{scheme}.png")
            await pg.mouse.click(5, 5); await pg.wait_for_timeout(200)
            assert not await pg.locator("#models-list details.holders[open]").count(), "clicking elsewhere closes the list"
            assert await pg.locator("[data-page=models] > p.hint.clamp").count() >= 2
            await ctx.close()
        await b.close()
        assert not errs, errs
        print("scalepages: ok", N, "nodes")
asyncio.run(main())
