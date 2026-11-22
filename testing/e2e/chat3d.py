# Chat to 3D: a clear 3D request opens the 3D card on Send (not the chat model), the mesh note and
# look-up boxes, "Answer in chat instead" (and that chat stays in chat), a 3D-printing question answered
# in chat with "Make this in 3D", and Make it with the measurement look-up shown on the 3D page.
# Needs `openscad` on PATH for the last part. Run after `sh testing/e2e/run.sh up`.
import asyncio, os, sys
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/chat3d/"
os.makedirs(S, exist_ok=True)
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()
MARK = "modify this 3d model https://makerworld.com/en/models/123456 for a GL.iNet GL-AR750"
QUESTION = "What infill should I use to 3D print a rack mount for a small router?"

async def login(pg):
    await pg.goto(U + "/"); await pg.fill("#pw", pw); await pg.click("button[type=submit]")
    await pg.wait_for_selector("#side-tasks"); await pg.wait_for_timeout(800)

async def new_chat(pg):
    await pg.goto(U + "/#/chat"); await pg.wait_for_selector("#chat-plus"); await pg.wait_for_timeout(500)

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None, args=["--use-gl=swiftshader", "--enable-unsafe-swiftshader"])
        errs = []
        for name, vp, scheme in [("desktop-light", {"width": 1440, "height": 900}, "light"), ("desktop-dark", {"width": 1440, "height": 900}, "dark"), ("phone", {"width": 390, "height": 844}, "dark")]:
            ctx = await b.new_context(ignore_https_errors=True, viewport=vp, color_scheme=scheme, device_scale_factor=2)
            pg = await ctx.new_page()
            pg.on("pageerror", lambda e: errs.append(str(e)))
            pg.on("console", lambda m: errs.append(m.text) if m.type == "error" and "ERR_FAILED" not in m.text and "401" not in m.text else None)
            await login(pg); await new_chat(pg)
            # 1. clear request: Send opens the 3D card, nothing is sent to chat
            await pg.fill("#chat-text", MARK); await pg.click("#chat-send")
            await pg.wait_for_selector(".skill-card [data-sk=chat]")
            assert await pg.input_value("#sk-prompt") == MARK
            assert "#/chat/" not in pg.url, "no chat was started"
            card = await pg.locator(".skill-card").inner_text()
            assert "can't edit a mesh" in card, card
            assert await pg.is_checked("#sk-links") and await pg.is_checked("#sk-lookup")
            await pg.screenshot(path=S + f"routed-{name}.png")
            if name != "desktop-light":
                await ctx.close(); continue
            # 2. Answer in chat instead: goes to chat, and the next clear request in that chat stays there
            await pg.click("[data-sk=chat]")
            await pg.wait_for_url("**/#/chat/*"); await pg.wait_for_selector(".chat-answer [data-sk-make]", timeout=60000)
            assert await pg.locator(".skill-card").count() == 0
            await pg.fill("#chat-text", "make a 3d printable rack mount for it"); await pg.click("#chat-send"); await pg.wait_for_timeout(800)
            assert await pg.locator(".skill-card").count() == 0, "this chat stays in chat"
            for _ in range(120):
                if await pg.locator(".chat-answer [data-sk-make]").count() >= 2: break
                await pg.wait_for_timeout(500)
            assert await pg.locator(".chat-answer [data-sk-make]").count() >= 2, "the second answer went to chat"
            # 3. a question stays in chat and gets Make this in 3D
            await new_chat(pg)
            await pg.fill("#chat-text", QUESTION); await pg.click("#chat-send")
            await pg.wait_for_url("**/#/chat/*"); await pg.wait_for_selector(".chat-answer [data-sk-make]", timeout=60000)
            await pg.screenshot(path=S + "question-answer.png")
            await pg.click(".chat-answer [data-sk-make]"); await pg.wait_for_selector(".skill-card #sk-prompt")
            assert await pg.input_value("#sk-prompt") == QUESTION
            assert await pg.locator("[data-sk=chat]").count() == 0, "not routed, so no Answer in chat instead"
            assert not await pg.is_checked("#sk-lookup"), "no device named"
            await pg.click("[data-sk=cancel]")
            # 4. plain question with no 3D cue and code with STL never route
            await new_chat(pg)
            await pg.fill("#chat-text", "make this C++ loop use the STL vector instead"); await pg.click("#chat-send")
            await pg.wait_for_url("**/#/chat/*"); await pg.wait_for_timeout(500)
            assert await pg.locator(".skill-card").count() == 0
            # 5. Make it with the look-up: measurements show on the 3D page
            await new_chat(pg)
            await pg.fill("#chat-text", "design a 10 inch rack mount for a GL.iNet GL-AR750, 2 mm walls"); await pg.click("#chat-send")
            await pg.wait_for_selector(".skill-card [data-sk=go]")
            assert await pg.is_checked("#sk-lookup"), "a named device turns the look-up on"
            if await pg.locator(".skill-card [data-sk=go][disabled]").count():
                await pg.screenshot(path=S + "no-openscad.png")
                print("no node renders (openscad missing?); skipping Make it", file=sys.stderr)
            else:
                await pg.click("[data-sk=go]"); await pg.wait_for_selector(".skill-card >> text=Started")
                await pg.goto(U + "/#/cad"); await pg.wait_for_selector(".cad-refs", timeout=90000)
                refs = await pg.locator(".cad-refs").inner_text()
                assert "GL-AR750 body" in refs and "100 × 68 × 24 mm" in refs, refs
                await pg.wait_for_selector("#cad-dl-stl:not(.hidden)", timeout=90000)
                await pg.screenshot(path=S + "cad-refs-light.png")
            await ctx.close()
        await b.close()
        bad = [e for e in errs if "favicon" not in e]
        assert not bad, bad
        print("chat3d ok")

asyncio.run(main())
