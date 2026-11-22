# Settings, Security: an API token's access is one explicit choice (Chat only by default, Full access on purpose),
# and full-access tokens carry a "full access" label in the list.
# Run after `sh testing/e2e/run.sh up`.
import asyncio, os
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/tokenscope/"
os.makedirs(S, exist_ok=True)
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()

async def open_security(b, scheme, w, h):
    ctx = await b.new_context(ignore_https_errors=True, viewport={"width": w, "height": h}, color_scheme=scheme, device_scale_factor=2)
    pg = await ctx.new_page()
    errs = []
    pg.on("pageerror", lambda e: errs.append(str(e)))
    await pg.goto(U + "/"); await pg.fill("#pw", pw); await pg.click("button[type=submit]"); await pg.wait_for_selector("#side-tasks"); await pg.wait_for_timeout(800)
    await pg.goto(U + "/#/settings"); await pg.wait_for_selector("#set-nav a")
    await pg.locator("#set-nav a", has_text="Security").first.click(); await pg.wait_for_selector("#tok-name", state="visible")
    await pg.wait_for_timeout(500)
    return ctx, pg, errs

async def create(pg, name, scope):
    await pg.fill("#tok-name", name)
    if scope: await pg.check(f"input[name=tok-scope][value={scope}]")
    await pg.click("#tok-create"); await pg.wait_for_selector("#tok-new:not(.hidden)"); await pg.wait_for_timeout(400)

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
        ctx, pg, errs = await open_security(b, "light", 1440, 1000)
        assert await pg.locator("#tok-chat, #tok-mcp, #tok-img").count() == 0, "the old checkboxes are gone"
        assert await pg.locator("input[name=tok-scope]").count() == 4
        assert await pg.locator("input[name=tok-scope][value=chat]").is_checked(), "Chat only is the default"
        await create(pg, "e2e default", None)
        await create(pg, "e2e full", "full")
        assert await pg.locator("input[name=tok-scope][value=chat]").is_checked(), "the choice resets to Chat only"
        await create(pg, "e2e mcp", "mcp")
        await create(pg, "e2e pics", "images")
        toks = await pg.evaluate("fetch('/api/apitokens').then(r=>r.json())")
        toks = toks.get("tokens", toks) if isinstance(toks, dict) else toks
        got = {t["name"]: t.get("scope", "") for t in toks}
        assert got.get("e2e default") == "chat", got
        assert got.get("e2e full", "x") == "", got
        assert got.get("e2e mcp") == "mcp", got
        assert got.get("e2e pics") == "images", got
        row = pg.locator("#tokens-list .rowline", has_text="e2e full")
        assert await row.locator(".pill", has_text="full access").count() == 1, "full-access tokens are labelled"
        assert await pg.locator("#tokens-list .rowline", has_text="e2e default").locator(".pill", has_text="full access").count() == 0
        card = pg.locator("#tok-name").locator("xpath=ancestor::div[contains(@class,'card')]")
        await card.screenshot(path=S + "light.png")
        await ctx.close()
        for scheme, w, h, name in [("dark", 1440, 1000, "dark.png"), ("dark", 390, 844, "phone.png")]:
            c2, p2, e2 = await open_security(b, scheme, w, h)
            if w < 500:
                sw = await p2.evaluate("document.documentElement.scrollWidth")
                assert sw <= w, f"no sideways scroll on the phone: {sw}"
            await p2.locator("#tok-name").locator("xpath=ancestor::div[contains(@class,'card')]").screenshot(path=S + name)
            errs += e2
            await c2.close()
        # clean up
        c3, p3, _ = await open_security(b, "light", 1200, 900)
        for t in toks:
            if t["name"].startswith("e2e "):
                await p3.evaluate("id=>fetch('/api/apitokens/'+id,{method:'DELETE'})", t["id"])
        await c3.close()
        await b.close()
        assert not errs, errs
        print("tokenscope: ok")

asyncio.run(main())
