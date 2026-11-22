# Settings autosave: cards made of switches and dropdowns (planner defaults, running tasks, free disk) have no Save
# button and save a moment after a change. Shots go to run/shots/usability5/.
import asyncio, os
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
U = "https://127.0.0.1:18460"
S = E + "shots/usability5/"; os.makedirs(S, exist_ok=True)
GET = "() => fetch('/api/settings').then(r => r.json())"

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or None, args=["--no-sandbox"])
        for scheme, w, h in (("light", 1360, 860), ("dark", 1360, 860), ("phone", 390, 844)):
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": w, "height": h}, color_scheme="light" if scheme == "light" else "dark")
            pg = await ctx.new_page(); errs = []
            pg.on("pageerror", lambda e: errs.append(str(e)))
            await pg.goto(U + "/"); await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
            await pg.wait_for_selector("#side", state="attached")
            await pg.goto(U + "/#/settings"); await pg.wait_for_selector("#set-agent", state="attached"); await pg.wait_for_timeout(600)
            for sid in ("set-save", "set-run-save", "set-autofree-save"):
                assert await pg.locator("#" + sid).is_hidden(), sid + " should be hidden"
            before = await pg.evaluate(GET)
            # two quick changes in different cards, then check both reached the server and the form kept them
            await pg.locator("#set-agent").scroll_into_view_if_needed()
            await pg.screenshot(path=S + f"settings-{scheme}.png")
            await pg.evaluate("() => { const a = document.getElementById('set-agent'); a.checked = !a.checked; a.dispatchEvent(new Event('change', {bubbles: true})); const f = document.getElementById('set-autofree'); f.checked = !f.checked; f.dispatchEvent(new Event('change', {bubbles: true})); }")
            await pg.wait_for_timeout(1500)
            after = await pg.evaluate(GET)
            assert after["agent_mode"] != before["agent_mode"], "agent mode not saved"
            assert after["auto_free"] != before["auto_free"], "auto free not saved"
            assert await pg.evaluate("() => document.getElementById('set-agent').checked") == after["agent_mode"], "form redrawn wrongly"
            # reorder the Auto planners: saved too
            first = await pg.evaluate("() => document.querySelector('#set-autoorder [data-ao-row]').dataset.aoRow")
            await pg.click("#set-autoorder [data-ao-row] >> nth=0 >> [data-ao-move='1']")
            await pg.wait_for_timeout(1200)
            order = (await pg.evaluate(GET))["auto_order"]
            assert order[0] != first or len(order) < 2, order
            # put everything back
            await pg.evaluate("() => { const a = document.getElementById('set-agent'); a.checked = !a.checked; a.dispatchEvent(new Event('change', {bubbles: true})); const f = document.getElementById('set-autofree'); f.checked = !f.checked; f.dispatchEvent(new Event('change', {bubbles: true})); }")
            await pg.wait_for_timeout(1000)
            assert not errs, errs
            await ctx.close()
        await b.close()
    print("usability5 ok")
asyncio.run(main())
