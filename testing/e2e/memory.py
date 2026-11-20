# Memory: add a fact in Settings, get a suggestion under a chat answer, edit it first and
# remember it, then see both in Settings (light and dark, phone).
import asyncio, os
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/memory/"
os.makedirs(S, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()

async def idle(pg):
    await pg.wait_for_timeout(700)
    await pg.wait_for_function("() => !document.querySelector('#chat-stop') || document.querySelector('#chat-stop').classList.contains('hidden')", timeout=60000)
    await pg.wait_for_timeout(500)

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
        errs = []
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1280, "height": 860}, color_scheme="light", device_scale_factor=2)
        pg = await ctx.new_page()
        pg.on("pageerror", lambda e: errs.append(str(e)))
        await pg.goto(U + "/")
        await pg.fill("#pw", pw); await pg.click("button[type=submit]")
        await pg.wait_for_selector("#side-tasks"); await pg.wait_for_timeout(1000)
        await pg.goto(U + "/#/settings"); await pg.wait_for_timeout(800)
        await pg.click("#set-nav a[href='#set-mem']"); await pg.wait_for_timeout(600)
        await pg.fill("#mem-new", "The home lab uses Gitea for CI mirrors.")
        await pg.click("#mem-add"); await pg.wait_for_selector(".mem-row")
        # A chat where the owner states a lasting preference.
        await pg.goto(U + "/#/chat"); await pg.wait_for_selector(".mcard")
        await pg.fill("#chat-text", "I always use Go 1.26 and plain JavaScript. How do I read a file line by line?")
        await pg.click("#chat-send")
        await pg.wait_for_selector(".mem-sug", timeout=60000); await idle(pg)
        assert "Prefers Go 1.26 and plain JavaScript" in await pg.inner_text(".mem-sug"), await pg.inner_text(".mem-sug")
        await pg.screenshot(path=S + "suggestion-light.png")
        await pg.click("[data-mem-editfirst]")
        await pg.fill(".mem-sug-input", "Prefers Go 1.26 and plain JavaScript, no frameworks.")
        await pg.click("[data-mem-keepedit]")
        await pg.wait_for_selector(".mem-sug", state="detached")
        # A second answer offers nothing new, and the card stays gone after a reload.
        await pg.reload(); await pg.wait_for_timeout(1500)
        assert await pg.locator(".mem-sug").count() == 0
        for scheme in ["light", "dark"]:
            await pg.emulate_media(color_scheme=scheme)
            await pg.goto(U + "/#/settings"); await pg.wait_for_timeout(800)
            await pg.click("#set-nav a[href='#set-mem']"); await pg.wait_for_timeout(700)
            rows = await pg.locator(".mem-row").all_inner_texts()
            assert len(rows) == 2 and "no frameworks" in rows[1] and "added by hand" in rows[0], rows
            await pg.locator("#set-mem").screenshot(path=S + "settings-%s.png" % scheme)
        # Edit and forget.
        await pg.click("[data-mem-edit] >> nth=0"); await pg.fill("#mem-edit-text", "The home lab uses Gitea.")
        await pg.click("[data-mem-save]"); await pg.wait_for_selector(".mem-row:has-text('uses Gitea.')")
        await pg.set_viewport_size({"width": 390, "height": 800}); await pg.wait_for_timeout(500)
        await pg.locator("#set-mem").screenshot(path=S + "settings-phone-dark.png")
        await b.close()
        assert not errs, errs
        print("ok", S)

asyncio.run(main())
