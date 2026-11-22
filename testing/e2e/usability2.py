# First-run checklist on New task: live from state, each step has its button, the card goes away once a node has a
# model ready. The rig has a ready node, so the earlier stages are drawn by changing the page's own state copy.
# Shots go to run/shots/usability2/.
import asyncio, os
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
U = "https://127.0.0.1:18460"
S = E + "shots/usability2/"; os.makedirs(S, exist_ok=True)

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or None, args=["--no-sandbox"])
        for scheme, w, h in (("light", 1360, 860), ("dark", 1360, 860), ("phone", 390, 844)):
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": w, "height": h}, color_scheme="light" if scheme == "light" else "dark")
            pg = await ctx.new_page(); errs = []
            pg.on("pageerror", lambda e: errs.append(str(e)))
            await pg.goto(U + "/"); await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
            await pg.wait_for_selector("#side", state="attached")
            await pg.goto(U + "/#/"); await pg.wait_for_selector("#nt-prompt")
            await pg.wait_for_timeout(800)
            assert await pg.locator("#home-setup .setup").count() == 0, "no card when a node has a model ready"
            # a node that is online with nothing loaded
            await pg.evaluate("() => { window.__n = state.nodes; state.nodes = state.nodes.map(n => Object.assign({}, n, {heartbeat: Object.assign({}, n.heartbeat, {loaded_model: '', inference_ready: false})})); renderNew(); }")
            assert await pg.locator("#home-setup [data-act=best]").count() == 1, "pick-a-model button"
            assert await pg.locator("#home-setup [data-act=setup-add]").count() == 0
            await pg.screenshot(path=S + f"no-model-{scheme}.png")
            await pg.evaluate("() => document.querySelector('#home-setup [data-act=best]').click()")
            await pg.wait_for_selector("#modal-best:not(.hidden)", timeout=5000)
            await pg.evaluate("() => document.querySelectorAll('.overlay').forEach(o => o.classList.add('hidden'))")
            # no nodes at all
            await pg.evaluate("() => { state.nodes = []; renderNew(); }")
            assert await pg.locator("#home-setup [data-act=setup-add]").count() == 1, "add-a-node button"
            await pg.screenshot(path=S + f"no-node-{scheme}.png")
            await pg.evaluate("() => { state.nodes = []; renderNew(); document.querySelector('#home-setup [data-act=setup-add]').click(); }")
            await pg.wait_for_selector("#modal-add:not(.hidden)", timeout=5000)
            await pg.evaluate("() => document.querySelectorAll('.overlay').forEach(o => o.classList.add('hidden'))")
            await pg.evaluate("() => { state.nodes = window.__n; renderNew(); }")
            assert await pg.locator("#home-setup .setup").count() == 0, "card gone again"
            assert not errs, errs
            await ctx.close()
        await b.close()
    print("usability2 ok")
asyncio.run(main())
