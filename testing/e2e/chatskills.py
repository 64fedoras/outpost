# Chat skills: slim sidebar (no New image / New 3D model), the ＋ menu, skill chip, suggestion, confirm card,
# "More settings…" prefill, and the Run code switch in the menu. Run after `sh testing/e2e/run.sh up`.
import asyncio, os
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/chatskills/"
os.makedirs(S, exist_ok=True)
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
        errs = []
        for name, vp in [("desktop", {"width": 1440, "height": 900}), ("phone", {"width": 390, "height": 844})]:
            ctx = await b.new_context(ignore_https_errors=True, viewport=vp, color_scheme="dark", device_scale_factor=2 if name == "phone" else 1)
            pg = await ctx.new_page()
            pg.on("pageerror", lambda e: errs.append(str(e)))
            pg.on("console", lambda m: errs.append(m.text) if m.type == "error" and "ERR_FAILED" not in m.text and "401" not in m.text else None)
            await pg.goto(U + "/"); await pg.fill("#pw", pw); await pg.click("button[type=submit]"); await pg.wait_for_selector("#side-tasks"); await pg.wait_for_timeout(1000)
            if name == "phone": await pg.click("#side-open"); await pg.wait_for_timeout(300)
            side = await pg.locator("#side").inner_text()
            assert "New image" not in side and "New 3D model" not in side, "the sidebar no longer has the skill pages"
            assert await pg.locator(".new-task").count() == 1, "one boxed New task"
            for t in ["Chat", "Projects", "Scheduled"]: assert t in side, t
            await pg.screenshot(path=S + f"sidebar-{name}.png")
            if name == "phone": await pg.click("#side-close"); await pg.wait_for_timeout(200)
            await pg.goto(U + "/#/chat"); await pg.wait_for_selector("#chat-plus"); await pg.wait_for_timeout(600)
            # menu
            await pg.click("#chat-plus"); await pg.wait_for_selector("#chat-plus-menu:not(.hidden)")
            assert await pg.locator("#chat-plus-menu [data-sk-menu]").count() == 5
            await pg.screenshot(path=S + f"menu-{name}.png")
            await pg.keyboard.press("Escape"); assert await pg.locator("#chat-plus-menu.hidden").count() == 1
            # run code from the menu shows a chip
            await pg.click("#chat-plus"); await pg.click("[data-sk-menu=run]"); await pg.wait_for_timeout(300)
            assert await pg.locator("[data-sk-chip=run]").count() == 1, "Run code on shows a chip"
            await pg.click("[data-sk-chip=run]"); await pg.wait_for_timeout(300)
            assert await pg.locator("[data-sk-chip=run]").count() == 0
            # image skill: chip, confirm card, no chat message sent
            await pg.click("#chat-plus"); await pg.click("[data-sk-menu=image]"); await pg.wait_for_timeout(200)
            assert await pg.locator("[data-sk-chip=skill]").count() == 1
            await pg.fill("#chat-text", "a red fox in the snow"); await pg.press("#chat-text", "Enter")
            await pg.wait_for_selector(".skill-card #sk-prompt")
            assert await pg.input_value("#sk-prompt") == "a red fox in the snow"
            assert await pg.input_value("#chat-text") == "", "the text moved into the card"
            assert "#/chat/" not in pg.url, "no chat was started"
            await pg.screenshot(path=S + f"card-image-{name}.png")
            await pg.fill("#sk-prompt", "a red fox in the snow, watercolour")
            await pg.click("[data-sk=more]"); await pg.wait_for_selector("#img-prompt", state="visible"); await pg.wait_for_timeout(300)
            assert await pg.input_value("#img-prompt") == "a red fox in the snow, watercolour", "More settings keeps the prompt"
            # suggestion for a picture request, dismiss, then card from the suggestion
            await pg.goto(U + "/#/chat"); await pg.wait_for_selector("#chat-plus"); await pg.wait_for_timeout(400)
            await pg.fill("#chat-text", "please draw a picture of a lighthouse")
            await pg.wait_for_selector("#chat-suggest:not(.hidden)")
            await pg.click("[data-sk-sug=no]"); await pg.wait_for_timeout(100)
            assert await pg.locator("#chat-suggest.hidden").count() == 1
            await pg.fill("#chat-text", "please draw an image of a lighthouse at dusk")
            await pg.click("[data-sk-sug=image]"); await pg.wait_for_selector(".skill-card #sk-prompt")
            assert "lighthouse at dusk" in await pg.input_value("#sk-prompt")
            await pg.click("[data-sk=cancel]"); assert await pg.locator(".skill-card").count() == 0
            # 3D card
            await pg.click("#chat-plus"); await pg.click("[data-sk-menu=cad]")
            await pg.fill("#chat-text", "a 40 mm cube with a 10 mm hole"); await pg.click("#chat-send")
            await pg.wait_for_selector(".skill-card #sk-prompt"); await pg.wait_for_timeout(500)
            await pg.screenshot(path=S + f"card-cad-{name}.png")
            if await pg.locator("[data-sk=go]").is_enabled():
                await pg.click("[data-sk=go]"); await pg.wait_for_selector(".skill-card a[href='#/cad']", timeout=15000)
            else:
                assert "render" in await pg.locator(".skill-card").inner_text(), "says why Make it is off"
                await pg.click("[data-sk=cancel]")
            # a normal message still goes to the chat
            await pg.fill("#chat-text", "Say hello in one short sentence."); await pg.click("#chat-send")
            await pg.wait_for_url("**/#/chat/*", timeout=15000)
            await ctx.close()
        await b.close()
        assert not errs, errs
        print("chatskills: ok")
asyncio.run(main())
