# New 3D model page: the fake model writes OpenSCAD (one typo first, fixed in round 2),
# the node renders it with the real OpenSCAD (needs `openscad` on PATH), the viewer
# shows it, a parameter re-renders without the model, and "Ask for changes" edits it.
import asyncio, os
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/cad/"
os.makedirs(S, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()

async def login(pg):
    await pg.goto(U + "/")
    await pg.fill("#pw", pw); await pg.click("button[type=submit]")
    await pg.wait_for_selector("#tile-nodes, #side-tasks"); await pg.wait_for_timeout(1500)

async def done(pg, text):
    await pg.wait_for_function("t => (document.getElementById('cad-r-meta')||{}).textContent.includes(t) && !document.querySelector('#cad-busy .gen-card')", arg=text, timeout=90000)
    await pg.wait_for_timeout(900)

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None, args=["--use-gl=swiftshader", "--enable-unsafe-swiftshader"])
        errs = []
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1440, "height": 1000}, color_scheme="light", device_scale_factor=1)
        pg = await ctx.new_page()
        pg.on("pageerror", lambda e: errs.append(str(e)))
        pg.on("console", lambda m: errs.append(m.text) if m.type == "error" else None)
        await login(pg)
        await pg.goto(U + "/#/cad"); await pg.wait_for_selector("#cad-optline:has-text('Renders on')", timeout=60000)
        await pg.screenshot(path=S + "empty-light.png")
        await pg.fill("#cad-prompt", "A phone stand, 70 mm wide base, 65 degree backrest, 12 mm slot, M3 hole (with a typo first)")
        await pg.click("#cad-go")
        await pg.wait_for_selector("#cad-busy .gen-card", timeout=10000)
        await pg.screenshot(path=S + "working-light.png")
        await done(pg, "70.0 ×")
        log = await pg.locator("#cad-log").text_content()
        assert "OpenSCAD failed" in log and "Round 2" in log, log
        assert "cube([w, 80, 8]);" in await pg.locator("#cad-code").inner_text()
        assert await pg.locator("#cad-params [data-param]").count() == 4
        # The viewer drew something: some pixels are not transparent.
        drawn = await pg.evaluate("""() => { const c = document.getElementById('cad-canvas'); const g = c.getContext('webgl');
            const px = new Uint8Array(4 * 64 * 64); g.readPixels(c.width / 2 - 32, c.height / 2 - 32, 64, 64, g.RGBA, g.UNSIGNED_BYTE, px);
            let n = 0; for (let i = 3; i < px.length; i += 4) if (px[i] > 0) n++; return n; }""")
        await pg.screenshot(path=S + "result-light.png")
        assert drawn > 100, f"viewer drew {drawn} pixels"
        await pg.click("#cad-wire"); await pg.wait_for_timeout(400)
        await pg.screenshot(path=S + "wire-light.png")
        await pg.click("#cad-wire")
        # A parameter render: no model call.
        await pg.fill("#cad-params [data-param=w]", "120")
        await pg.click("[data-cad=params]")
        await done(pg, "120.0 ×")
        assert await pg.locator("[data-cad=params-reset]").count() == 1
        # Ask for changes: the model sees the file with w = 120 and answers with w = 100.
        await pg.fill("#cad-ask-text", "make the base wider")
        await pg.click("#cad-ask-go")
        await done(pg, "100.0 ×")
        assert "Asked: make the base wider" in await pg.locator("#cad-log").text_content()
        prompts = open(E + "prompts.log").read()
        assert "w = 120; // [40:200]" in prompts, "the change request should carry the owner's values"
        r = await pg.request.get(U + "/api/cad")
        mid = (await r.json())["models"][0]["id"]
        r = await pg.request.get(U + f"/api/cad/{mid}/model.stl?download=1")
        assert r.ok and r.headers["content-disposition"].endswith('.stl"'), r.headers
        await ctx.close()
        for scheme in ["dark", "light"]:
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1440, "height": 1100}, color_scheme=scheme, device_scale_factor=2)
            pg = await ctx.new_page(); await login(pg)
            await pg.goto(U + "/#/cad"); await pg.wait_for_selector("#cad-r-meta:has-text('100.0')"); await pg.wait_for_timeout(1500)
            await pg.screenshot(path=S + f"desktop-{scheme}.png", full_page=True)
            await ctx.close()
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 390, "height": 844}, color_scheme="dark", is_mobile=True, has_touch=True, device_scale_factor=2)
        pg = await ctx.new_page(); await login(pg)
        await pg.goto(U + "/#/cad"); await pg.wait_for_selector("#cad-r-meta:has-text('100.0')"); await pg.wait_for_timeout(1500)
        assert await pg.evaluate("document.documentElement.scrollWidth <= window.innerWidth + 1"), "no sideways scroll on a phone"
        await pg.screenshot(path=S + "phone-dark.png", full_page=True)
        await ctx.close()
        await b.close()
        bad = [e for e in errs if "favicon" not in e and "status of 401" not in e]  # 401: the sign-in page, before login
        assert not bad, bad
        print("cad e2e ok")

asyncio.run(main())
