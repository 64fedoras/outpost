# Chat essentials: edit and resend, regenerate, version arrows, branch, instructions, chats search.
import asyncio, os
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/chatversions/"
os.makedirs(S, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()

async def login(pg):
    await pg.goto(U + "/")
    await pg.fill("#pw", pw); await pg.click("button[type=submit]")
    await pg.wait_for_selector("#tile-nodes, #side-tasks"); await pg.wait_for_timeout(1500)

async def idle(pg):
    await pg.wait_for_timeout(700)
    await pg.wait_for_function("() => !document.querySelector('#chat-stop') || document.querySelector('#chat-stop').classList.contains('hidden')", timeout=60000)
    await pg.wait_for_timeout(600)

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
        errs = []
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1280, "height": 860}, color_scheme="light", device_scale_factor=2)
        pg = await ctx.new_page()
        pg.on("pageerror", lambda e: errs.append(str(e)))
        pg.on("console", lambda m: errs.append(m.text) if m.type == "error" else None)
        await login(pg)
        await pg.goto(U + "/#/chat"); await pg.wait_for_selector(".mcard")
        # Instructions on a new chat, before the first message.
        await pg.click("#chat-instr-btn"); await pg.fill("#chat-instr-text", "Answer in Go. Short answers, code first.")
        await pg.click("#chat-instr-save"); await pg.wait_for_timeout(300)
        await pg.fill("#chat-text", "How do I parse an RFC 3339 time and keep the zone?")
        await pg.click("#chat-send")
        await pg.wait_for_selector(".chat-answer .chat-text", timeout=60000); await idle(pg)
        assert await pg.locator(".chat-instr").count() == 1, "instructions strip"
        # Edit the first message.
        await pg.click("[data-edit]"); await pg.fill("#chat-edit-text", "How do I parse RFC 3339 dates in Go?")
        await pg.click("[data-edit-send]"); await idle(pg)
        assert "1 / 2" not in await pg.inner_text("#chat-msgs") and "2 / 2" in await pg.inner_text("#chat-msgs"), "edit makes version 2 of 2"
        # Regenerate the answer.
        await pg.click("[data-regen]"); await idle(pg)
        assert await pg.locator(".chat-answer .ver").count() == 1, "answer has versions"
        await pg.screenshot(path=S + "versions-light.png")
        # Back to the first version of the question.
        await pg.click(".bubble-col [aria-label='Previous version']"); await idle(pg)
        assert "RFC 3339 time and keep the zone" in await pg.inner_text("#chat-msgs"), "first version back"
        # Chats search by message text.
        await pg.fill("#chat-find", "zone"); await pg.wait_for_timeout(900)
        assert await pg.locator(".chat-list a").count() >= 1, "search finds the chat"
        await pg.screenshot(path=S + "search-light.png")
        await pg.fill("#chat-find", ""); await pg.wait_for_timeout(500)
        # Branch.
        before = pg.url
        await pg.click("[data-branch] >> nth=0"); await pg.wait_for_timeout(1200)
        assert pg.url != before, "branch opens a new chat"
        await pg.evaluate("document.documentElement.dataset.theme='dark'")
        await pg.goto(before); await pg.wait_for_selector(".chat-acts"); await pg.wait_for_timeout(800)
        await pg.screenshot(path=S + "versions-dark.png")
        await pg.click("#chat-instr-btn"); await pg.wait_for_timeout(300)
        await pg.screenshot(path=S + "instructions-dark.png")
        await ctx.close()
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 390, "height": 844}, is_mobile=True, has_touch=True, device_scale_factor=2)
        pg = await ctx.new_page(); pg.on("pageerror", lambda e: errs.append(str(e)))
        await login(pg)
        await pg.goto(before); await pg.wait_for_selector(".chat-acts"); await pg.wait_for_timeout(800)
        await pg.screenshot(path=S + "phone.png")
        print("console errors:", errs or "none")
        await b.close()
asyncio.run(main())
