# Resizable side panels: drag the left edge of the project's Threads panel and of the
# chats list, keyboard on the focused handle, double-click to reset, the width kept
# after a reload, and no handle where the panel is an overlay (narrow screens, phone).
# Pass "before" to only take screenshots (for a build without the handle).
# Screenshots go to $OUTPOST_E2E_DIR/shots/panelresize/.
import asyncio, os, sys
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
OUT = E + "shots/panelresize/"
os.makedirs(OUT, exist_ok=True)
U = "https://127.0.0.1:18460"
BEFORE = "before" in sys.argv[1:]
POST = "(a) => fetch(a[0], {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(a[1] || {})}).then(r => r.json())"

async def login(pg):
    await pg.goto(U + "/"); await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
    await pg.wait_for_selector("#nt-prompt")

async def pw(pg, sel):
    return await pg.evaluate(f"document.querySelector('{sel}').getBoundingClientRect().width")

async def drag(pg, handle, dx):
    box = await handle.bounding_box()
    x, y = box["x"] + box["width"] / 2, box["y"] + 300
    await pg.mouse.move(x, y); await pg.mouse.down()
    for i in range(1, 11):
        await pg.mouse.move(x + dx * i / 10, y)
    await pg.mouse.up()

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or None, args=["--no-sandbox"])
        errors, first = [], None
        for scheme in ("dark", "light"):
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1440, "height": 860}, color_scheme=scheme, device_scale_factor=2)
            pg = await ctx.new_page()
            pg.on("pageerror", lambda e: errors.append(str(e)))
            await login(pg)
            pid = (await pg.evaluate(POST, ["/api/projects", {"name": "Dice roller " + scheme}]))["id"]
            first = first or pid
            await pg.evaluate(POST, ["/api/tasks", {"prompt": "Write a dice roller CLI in Python with a pytest test", "project": pid}])
            sid = (await pg.evaluate(POST, [f"/api/projects/{pid}/stash", {}]))["id"]
            for name, body in (("dice-rules.md", "# Rules\nRoll NdM, add modifiers."), ("api-notes.txt", "The CLI takes 2d6+1 style arguments.")):
                await pg.evaluate("(a) => fetch(a[0], {method: 'POST', body: a[1]}).then(r => r.json())", [f"/api/stash/{sid}/resources?name={name}", body])
            await pg.goto(U + "/#/projects/" + pid)
            await pg.wait_for_selector("#pj-name")
            if not await pg.locator("#pj-wrap.panel-open").count():
                await pg.click("#pj-threads-btn")
            await pg.wait_for_selector(".pj-row")
            await pg.wait_for_timeout(400)
            tag = "before" if BEFORE else "after"
            if BEFORE:
                await pg.screenshot(path=OUT + f"{tag}-{scheme}.png")
                await ctx.close(); continue
            await pg.wait_for_selector("#pj-panel .pj-acard.k-resource")
            assert await pg.locator("#pj-panel .pj-acard.k-resource").count() == 2
            # sections fold and stay folded after a reload
            await pg.click("#pj-panel [data-act=proj-fold][data-key=waiting]")
            assert not await pg.locator("#pj-panel .pj-sec.waiting .pj-row").count()
            await pg.reload(); await pg.wait_for_selector("#pj-panel .pj-sec.waiting")
            assert not await pg.locator("#pj-panel .pj-sec.waiting .pj-row").count()
            await pg.click("#pj-panel [data-act=proj-fold][data-key=waiting]")
            await pg.wait_for_selector("#pj-panel .pj-sec.waiting .pj-row")
            await pg.screenshot(path=OUT + f"after-{scheme}.png")
            h = pg.locator("#pj-panel .side-resize")
            assert await h.count() == 1
            assert abs(await pw(pg, "#pj-panel") - 380) < 2, await pw(pg, "#pj-panel")
            # drag left widens
            await drag(pg, h, -220)
            w = await pw(pg, "#pj-panel")
            assert 590 < w < 610, w
            await h.hover()
            await pg.screenshot(path=OUT + f"after-wide-{scheme}.png")
            # clamped: the main column keeps 420 px, the panel at most 760
            await drag(pg, h, -900)
            top = min(760, await pw(pg, "#pj-wrap") - 420)
            assert abs(await pw(pg, "#pj-panel") - top) < 2, (await pw(pg, "#pj-panel"), top)
            await drag(pg, h, 900)
            assert abs(await pw(pg, "#pj-panel") - 280) < 2, await pw(pg, "#pj-panel")
            await pg.mouse.move(700, 400)
            await pg.screenshot(path=OUT + f"after-narrow-{scheme}.png")
            # keyboard: arrows on the focused handle, Enter resets
            await h.focus()
            await pg.keyboard.press("ArrowLeft"); await pg.keyboard.press("Shift+ArrowLeft")
            assert abs(await pw(pg, "#pj-panel") - (280 + 16 + 64)) < 2
            assert await h.get_attribute("aria-valuenow") == "360"
            await pg.keyboard.press("Enter")
            assert abs(await pw(pg, "#pj-panel") - 380) < 2
            # remembered after a reload; double-click resets
            await drag(pg, h, -120)
            await pg.reload(); await pg.wait_for_selector(".pj-row")
            assert abs(await pw(pg, "#pj-panel") - 500) < 2, await pw(pg, "#pj-panel")
            await pg.locator("#pj-panel .side-resize").dblclick()
            assert abs(await pw(pg, "#pj-panel") - 380) < 2
            assert await pg.evaluate("localStorage.getItem('outpost.pjPanelW')") is None
            # chats list on the Chat page uses the same handle
            await pg.goto(U + "/#/chat")
            await pg.wait_for_selector("#chat-panel-btn")
            if await pg.locator("#chat-wrap.closed").count():
                await pg.click("#chat-panel-btn")
            ch = pg.locator("#chat-panel .side-resize")
            await ch.wait_for()
            assert abs(await pw(pg, "#chat-panel") - 300) < 2
            await drag(pg, ch, -150)
            assert abs(await pw(pg, "#chat-panel") - 450) < 2, await pw(pg, "#chat-panel")
            await ch.hover()
            await pg.screenshot(path=OUT + f"after-chat-{scheme}.png")
            await ch.dblclick()
            # narrower than 1100 px the Threads panel is an overlay: no handle, fixed width
            await pg.set_viewport_size({"width": 1000, "height": 800})
            await pg.goto(U + "/#/projects/" + pid); await pg.wait_for_selector("#pj-name")
            if not await pg.locator("#pj-wrap.panel-open").count():
                await pg.click("#pj-threads-btn")
            await pg.wait_for_selector(".pj-row")
            assert not await pg.locator("#pj-panel .side-resize").is_visible()
            assert abs(await pw(pg, "#pj-panel") - 420) < 2
            await ctx.close()
        # phone: unchanged full-screen panel, no handle
        ctx = await b.new_context(ignore_https_errors=True, **p.devices["iPhone 13"], color_scheme="dark")
        pg = await ctx.new_page()
        await login(pg)
        pid = first
        await pg.goto(U + "/#/projects/" + pid); await pg.wait_for_selector("#pj-name")
        await pg.click("#pj-threads-btn"); await pg.wait_for_selector("#pj-panel .pj-row")
        await pg.wait_for_timeout(300)
        await pg.screenshot(path=OUT + ("before" if BEFORE else "after") + "-phone.png")
        if not BEFORE:
            assert not await pg.locator("#pj-panel .side-resize").is_visible()
        await ctx.close()
        await b.close()
        assert not errors, errors
        print("panelresize: ok" + (" (before shots only)" if BEFORE else ""))

asyncio.run(main())
