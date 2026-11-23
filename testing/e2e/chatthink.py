# Chat: thinking behind a fold, the green working bar, and answers carried on after the length limit.
import asyncio, os
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/chatthink/"
os.makedirs(S, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()

async def ask(pg, text):
    await pg.fill("#chat-text", text)
    await pg.keyboard.press("Control+Enter")

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
        errs = []
        for scheme, width in [("light", 1200), ("dark", 1200), ("light", 390)]:
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": width, "height": 800}, color_scheme=scheme, device_scale_factor=1)
            pg = await ctx.new_page()
            pg.on("pageerror", lambda e: errs.append(str(e)))
            await pg.goto(U + "/"); await pg.fill("#pw", pw); await pg.click("button[type=submit]")
            await pg.wait_for_selector("#tile-nodes, #side-tasks"); await pg.wait_for_timeout(2500)
            await pg.goto(U + "/#/chat"); await pg.wait_for_selector(".mcard")
            await pg.locator(".mcard").nth(0).click()
            tag = f"{scheme}-{width}"
            await ask(pg, "think test: what is the answer?")
            await pg.wait_for_selector(".working", timeout=30000)
            await pg.wait_for_selector("#chat-live .think", timeout=30000)
            assert await pg.locator("#chat-live .think[open]").count() == 0, "thinking starts folded"
            assert "Thinking" in await pg.locator("#chat-live .working").inner_text()
            await pg.screenshot(path=S + f"thinking-{tag}.png")
            await pg.click("#chat-live .think > summary")  # open it while it is still thinking
            await pg.wait_for_timeout(1300)
            assert await pg.locator("#chat-live .think[open]").count() == 1, "an opened fold stays open as text arrives"
            await pg.screenshot(path=S + f"thinking-open-{tag}.png")
            await pg.wait_for_selector(".chat-answer .chat-text:has-text('The answer is 42')", timeout=60000)
            await pg.wait_for_function("!document.querySelector('#chat-live .working')", timeout=30000)
            assert await pg.locator(".chat-msgs .think").count() == 1
            assert "Thought for" in await pg.locator(".chat-msgs .think > summary").first.inner_text()
            assert await pg.locator(".chat-answer .think[open]").count() == 1, "the fold opened while thinking stays open on the saved answer"
            await pg.reload(); await pg.wait_for_selector(".chat-answer .think")
            assert await pg.locator(".chat-answer .think[open]").count() == 0, "saved thinking is folded again"
            await pg.click(".chat-answer .think > summary")
            assert "step by step" in await pg.locator(".chat-answer .think-body").first.inner_text()
            await pg.screenshot(path=S + f"done-open-{tag}.png")
            if scheme == "light" and width == 1200:
                await ask(pg, "length test please")
                await pg.wait_for_selector(".chat-answer:has-text('carries on to the end')", timeout=60000)
                await pg.wait_for_function("!document.querySelector('#chat-live .working')", timeout=30000)
                body = await pg.locator(".chat-msgs").inner_text()
                assert "cut off at the length limit" not in body, body
                assert "Carried on automatically 1 time" in body, body
                assert await pg.locator("[data-continue]").count() == 0, "no Continue button once it carried on"
            await ctx.close()
        await b.close()
        assert not errs, errs
        print("chatthink ok")
asyncio.run(main())
