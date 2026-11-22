# Monthly limit asked once when the first cloud key is saved, and schedule times shown on the browser's clock too.
# The key prompt is opened through the page's own function (the rig already has a key). Shots: run/shots/usability4/.
import asyncio, os
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
U = "https://127.0.0.1:18460"
S = E + "shots/usability4/"; os.makedirs(S, exist_ok=True)

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or None, args=["--no-sandbox"])
        for scheme, w, h in (("light", 1360, 860), ("dark", 1360, 860), ("phone", 390, 844)):
            # a browser clock 5 hours behind the controller's, so the schedule line has something to say
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": w, "height": h}, color_scheme="light" if scheme == "light" else "dark", timezone_id="America/Chicago")
            pg = await ctx.new_page(); errs = []
            pg.on("pageerror", lambda e: errs.append(str(e)))
            await pg.goto(U + "/"); await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
            await pg.wait_for_selector("#side", state="attached")
            await pg.goto(U + "/#/settings"); await pg.wait_for_selector("#set-key", state="attached"); await pg.wait_for_timeout(600)
            await pg.evaluate("() => { settings.monthly_cap_usd = 0; settings.monthly_cap_asked = false; askMonthlyCap(); }")
            await pg.wait_for_selector("#modal-ask:not(.hidden)")
            assert (await pg.input_value("#ask-input")) == "25"
            assert (await pg.inner_text("#ask-no")).strip() == "No limit"
            await pg.screenshot(path=S + f"cap-prompt-{scheme}.png")
            await pg.click("#ask-yes")
            await pg.wait_for_timeout(600)
            cap = await pg.evaluate("() => fetch('/api/spending').then(r => r.json()).then(r => r.cap_usd)")
            assert cap == 25, cap
            # answered once: no second prompt
            await pg.evaluate("() => { settings.monthly_cap_usd = 0; askMonthlyCap(); }")
            await pg.wait_for_timeout(300)
            assert await pg.locator("#modal-ask:not(.hidden)").count() == 0, "asked twice"
            await pg.evaluate("() => fetch('/api/spending/cap', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({cap_usd: 0})})")
            await pg.goto(U + "/#/scheduled"); await pg.wait_for_selector("#sc-tz"); await pg.wait_for_timeout(500)
            tz = await pg.inner_text("#sc-tz")
            assert "this browser is on UTC" in tz, tz
            await pg.screenshot(path=S + f"scheduled-{scheme}.png")
            assert not errs, errs
            await ctx.close()
        await b.close()
    print("usability4 ok")
asyncio.run(main())
