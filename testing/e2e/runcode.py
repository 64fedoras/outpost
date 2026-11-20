# Run code in chat: turn on "Run code" for a new chat, the model runs a program that fails,
# fixes it, and the run cards show code, output, a chart and a CSV download (light, dark, phone).
import asyncio, os
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/runcode/"
os.makedirs(S, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()

async def idle(pg):
    await pg.wait_for_timeout(700)
    await pg.wait_for_function("() => !document.querySelector('#chat-stop') || document.querySelector('#chat-stop').classList.contains('hidden')", timeout=120000)
    await pg.wait_for_timeout(500)

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
        errs = []
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1280, "height": 900}, color_scheme="light", device_scale_factor=2)
        pg = await ctx.new_page()
        pg.on("pageerror", lambda e: errs.append(str(e)))
        await pg.goto(U + "/")
        await pg.fill("#pw", pw); await pg.click("button[type=submit]")
        await pg.wait_for_selector("#side-tasks"); await pg.wait_for_timeout(1000)
        await pg.goto(U + "/#/chat"); await pg.wait_for_selector(".mcard")
        await pg.click("#chat-run")
        assert await pg.get_attribute("#chat-run", "aria-pressed") == "true"
        await pg.fill("#chat-text", "Sum these numbers and chart them: 3 1 4 1 5 9 2 6")
        await pg.click("#chat-send")
        await pg.wait_for_selector(".run-card >> nth=1", timeout=120000); await idle(pg)
        heads = await pg.locator(".run-card summary").all_inner_texts()
        assert "Ran Python on" in heads[0] and "exit 1" in heads[0] and "exit 0" in heads[1] and "2 files" in heads[1], heads
        ans = await pg.inner_text(".chat-answer .chat-text")
        assert "sum 31 max 9" in ans, ans
        await pg.wait_for_selector(".run-files img")
        ok = await pg.evaluate("() => { const i = document.querySelector('.run-files img'); return i.complete && i.naturalWidth > 0; }")
        assert ok, "chart did not load"
        await pg.click(".run-card >> nth=0 >> summary")
        assert "NameError" in await pg.inner_text(".run-card >> nth=0 >> .run-out")
        r = await pg.request.get(U + await pg.get_attribute(".run-dl a:has-text('numbers.csv')", "href"))
        assert r.ok and "9" in await r.text(), await r.text()
        assert await pg.get_attribute("#chat-run", "aria-pressed") == "true"  # kept on the new chat
        await pg.screenshot(path=S + "chat-light.png")
        await pg.emulate_media(color_scheme="dark"); await pg.wait_for_timeout(500)
        await pg.screenshot(path=S + "chat-dark.png")
        await pg.set_viewport_size({"width": 390, "height": 844}); await pg.wait_for_timeout(600)
        await pg.screenshot(path=S + "chat-phone-dark.png")
        # Turning it off sticks after a reload.
        await pg.set_viewport_size({"width": 1280, "height": 900})
        await pg.click("#chat-run"); await pg.wait_for_timeout(500)
        await pg.reload(); await pg.wait_for_selector(".run-card")
        assert await pg.get_attribute("#chat-run", "aria-pressed") == "false"
        await b.close()
        assert not errs, errs
        print("ok", S)

asyncio.run(main())
