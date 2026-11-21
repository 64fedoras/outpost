# Settings shows one section at a time; the phone task header folds Files, Activity and Delete into a ⋯ menu.
# Run after `sh testing/e2e/run.sh up` (needs at least one task, e.g. after flow.py).
import asyncio, os
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/settingsphone/"
os.makedirs(S, exist_ok=True)
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()

async def login(pg):
    await pg.goto(U + "/"); await pg.fill("#pw", pw); await pg.click("button[type=submit]"); await pg.wait_for_selector("#side-tasks"); await pg.wait_for_timeout(1200)

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
        errs = []
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1440, "height": 900}, color_scheme="dark")
        pg = await ctx.new_page()
        pg.on("pageerror", lambda e: errs.append(str(e)))
        pg.on("console", lambda m: errs.append(m.text) if m.type == "error" and "ERR_FAILED" not in m.text and "401" not in m.text else None)
        await login(pg)
        await pg.goto(U + "/#/settings"); await pg.wait_for_selector("#set-nav a"); await pg.wait_for_timeout(600)
        vis = await pg.evaluate("[...document.querySelectorAll('section.set-sec')].filter(s=>!s.classList.contains('hidden')).length")
        assert vis == 1, f"one settings section at a time, saw {vis}"
        h1 = await pg.evaluate("document.documentElement.scrollHeight")
        links = await pg.locator("#set-nav a").count()
        assert links >= 4
        await pg.locator("#set-nav a").nth(2).click(); await pg.wait_for_timeout(300)
        vis2 = await pg.evaluate("[...document.querySelectorAll('section.set-sec')].filter(s=>!s.classList.contains('hidden')).map(s=>s.id)")
        assert len(vis2) == 1, vis2
        await pg.screenshot(path=S + "settings-dark.png")
        await pg.reload(); await pg.wait_for_selector("#set-nav a"); await pg.wait_for_timeout(600)
        vis3 = await pg.evaluate("[...document.querySelectorAll('section.set-sec')].filter(s=>!s.classList.contains('hidden')).map(s=>s.id)")
        assert vis3 == vis2, f"the section is remembered: {vis2} vs {vis3}"
        print("settings page height:", h1)
        # phone task header
        ph = await b.new_context(ignore_https_errors=True, viewport={"width": 390, "height": 844}, color_scheme="dark", device_scale_factor=2)
        pp = await ph.new_page()
        pp.on("pageerror", lambda e: errs.append(str(e)))
        await login(pp)
        await pp.click("#side-open"); await pp.wait_for_timeout(300)
        if await pp.locator(".side-task").count() == 0:
            print("no task in the rig: skipping the phone header check"); await b.close(); assert not errs, errs; return
        await pp.click(".side-task >> nth=0"); await pp.wait_for_selector(".task-bar-tools"); await pp.wait_for_timeout(1200)
        assert await pp.locator(".task-bar-tools .tb-full:visible").count() == 0, "Files/Activity/Delete buttons are folded away on a phone"
        assert await pp.locator(".task-bar-tools .tb-more").is_visible()
        await pp.locator(".tb-more summary").click(); await pp.wait_for_timeout(200)
        assert await pp.locator(".tb-more .menu-list").is_visible()
        await pp.screenshot(path=S + "task-phone-menu.png")
        await pp.locator(".tb-more .menu-list button >> text=Activity").click(); await pp.wait_for_timeout(300)
        assert not await pp.locator(".tb-more[open]").count(), "choosing an item closes the menu"
        sw = await pp.evaluate("document.documentElement.scrollWidth")
        assert sw <= 390, f"no sideways scroll on the phone: {sw}"
        await pp.screenshot(path=S + "task-phone.png")
        # desktop keeps the full buttons
        await pg.click(".side-task >> nth=0"); await pg.wait_for_timeout(1000)
        assert not await pg.locator(".tb-more").is_visible(), "desktop has no ⋯ menu"
        await b.close()
        assert not errs, errs
        print("settingsphone: ok")
asyncio.run(main())
