# Update everything: one primary button, per-machine buttons behind "Choose individually…", one Check button,
# uploads folded away, and the sidebar badge agrees with the page. Update states are drawn by changing the page's
# own state copy (the rig has nothing to update). Shots go to run/shots/usability3/.
import asyncio, os
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
U = "https://127.0.0.1:18460"
S = E + "shots/usability3/"; os.makedirs(S, exist_ok=True)
FAKE = "() => { state.nodes = state.nodes.map(n => Object.assign({}, n, {update_to: '9.9.9'})); state.controller_update_to = '9.9.9'; renderBadges(); renderUpdates(); }"

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or None, args=["--no-sandbox"])
        for scheme, w, h in (("light", 1360, 860), ("dark", 1360, 860), ("phone", 390, 844)):
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": w, "height": h}, color_scheme="light" if scheme == "light" else "dark")
            pg = await ctx.new_page(); errs = []
            pg.on("pageerror", lambda e: errs.append(str(e)))
            await pg.goto(U + "/"); await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
            await pg.wait_for_selector("#side", state="attached")
            await pg.goto(U + "/#/updates"); await pg.wait_for_selector("#upd-table tbody tr")
            await pg.wait_for_timeout(500)
            assert (await pg.inner_text("#btn-upd-all")).strip() == "Everything is up to date"
            assert await pg.locator("#btn-gh-install").count() == 0, "one check button"
            assert (await pg.inner_text("#btn-gh-check")).strip() == "Check for updates"
            assert await pg.locator("#upl").evaluate("e => !e.open"), "uploads folded"
            await pg.evaluate(FAKE)
            assert (await pg.inner_text("#btn-upd-all")).strip() == "Update everything"
            assert await pg.locator("#upd-table [data-act=node-update], #upd-table [data-act=ctl-update], #upd-table [data-act=upd-group-go]").count() == 0, "no per-machine buttons yet"
            n = int(await pg.evaluate("() => updatesAvailable()"))
            badge = (await pg.inner_text("#badge-updates")).strip()
            assert badge == str(n), (badge, n)
            await pg.screenshot(path=S + f"everything-{scheme}.png")
            await pg.click("#btn-upd-indiv")
            await pg.evaluate(FAKE)
            assert await pg.locator("#upd-table [data-act=node-update], #upd-table [data-act=ctl-update], #upd-table [data-act=upd-group-go]").count() >= 1, "per-machine buttons shown on request"
            await pg.screenshot(path=S + f"individually-{scheme}.png")
            await pg.click("#btn-upd-indiv")
            assert not errs, errs
            await ctx.close()
        await b.close()
    print("usability3 ok")
asyncio.run(main())
