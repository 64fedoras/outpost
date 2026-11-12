# Chat page layout: model cards on a new chat, docked composer, chats panel, phone sheet.
import asyncio, os
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/chatpage/"
os.makedirs(S, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()

async def login(pg):
    await pg.goto(U + "/")
    await pg.fill("#pw", pw); await pg.click("button[type=submit]")
    await pg.wait_for_selector("#tile-nodes, #side-tasks"); await pg.wait_for_timeout(2500)

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("PW_CHROMIUM") or None)
        errs = []
        for scheme in ["dark", "light"]:
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1440, "height": 900}, color_scheme=scheme, device_scale_factor=1)
            pg = await ctx.new_page()
            pg.on("pageerror", lambda e: errs.append(str(e)))
            pg.on("console", lambda m: errs.append(m.text) if m.type == "error" else None)
            await login(pg)
            await pg.goto(U + "/#/chat"); await pg.wait_for_selector(".mcard"); await pg.wait_for_timeout(800)
            assert await pg.locator(".mcard").count() >= 2, "expected Auto plus a loaded model card"
            assert await pg.locator(".mcard.sel").count() == 1
            await pg.screenshot(path=S + f"empty-{scheme}.png")
            await pg.locator(".mcard").nth(1).click()
            assert await pg.locator("#chat-node").input_value() != "auto", "card click should set the model"
            await pg.fill("#chat-text", "Say hello in one short sentence.")
            await pg.keyboard.press("Control+Enter")
            await pg.wait_for_selector(".chat-answer .chat-text", timeout=60000); await pg.wait_for_timeout(1200)
            assert await pg.locator(".mcard").count() == 0, "cards leave once a chat is open"
            await pg.screenshot(path=S + f"active-{scheme}.png")
            await pg.click("#chat-panel-btn")
            assert await pg.locator(".chat-panel").is_hidden(), "panel toggles closed"
            await pg.screenshot(path=S + f"active-nopanel-{scheme}.png")
            await ctx.close()
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 390, "height": 844}, color_scheme="dark", is_mobile=True, has_touch=True, device_scale_factor=2)
        pg = await ctx.new_page(); pg.on("pageerror", lambda e: errs.append(str(e)))
        await login(pg)
        await pg.goto(U + "/#/chat"); await pg.wait_for_selector(".mcard"); await pg.wait_for_timeout(600)
        await pg.screenshot(path=S + "phone-empty.png")
        assert await pg.locator(".chat-panel").is_hidden()
        await pg.click("#chat-panel-btn"); await pg.wait_for_timeout(300)
        assert await pg.locator(".chat-panel").is_visible(), "phone: Chats opens a sheet"
        await pg.screenshot(path=S + "phone-chats.png")
        await pg.click(".chat-list a >> nth=0"); await pg.wait_for_timeout(1200)
        assert await pg.locator(".chat-panel").is_hidden(), "picking a chat closes the sheet"
        await pg.screenshot(path=S + "phone-active.png")
        print("console errors:", errs or "none")
        await b.close()
asyncio.run(main())
