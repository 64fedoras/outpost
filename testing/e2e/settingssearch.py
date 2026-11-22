# Settings search: filters cards across sections, ranks the best match first, opens folded help, restores the
# section on clear, "/" focuses the box, API tokens are their own card in Security. Run after `sh testing/e2e/run.sh up`.
import asyncio, os
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/settingssearch/"
os.makedirs(S, exist_ok=True)
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()

async def login(pg):
    await pg.goto(U + "/"); await pg.fill("#pw", pw); await pg.click("button[type=submit]"); await pg.wait_for_selector("#side-tasks"); await pg.wait_for_timeout(1200)

async def visible_cards(pg):
    return await pg.evaluate("[...document.querySelectorAll('.set-main .card')].filter(c=>c.offsetParent!==null).map(c=>c.previousElementSibling&&c.previousElementSibling.classList.contains('section-head')?c.previousElementSibling.textContent.replace(/^.*›\\s*/,'').trim():'?')")

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
        errs = []
        for scheme, vw, vh, tag in (("light", 1440, 900, "desktop-light"), ("dark", 1440, 900, "desktop-dark"), ("dark", 390, 844, "phone")):
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": vw, "height": vh}, color_scheme=scheme, device_scale_factor=2)
            pg = await ctx.new_page()
            pg.on("pageerror", lambda e: errs.append(str(e)))
            await login(pg)
            await pg.goto(U + "/#/settings"); await pg.wait_for_selector("#set-q"); await pg.wait_for_timeout(800)
            await pg.locator("#set-nav a[data-sec=pics]").click(); await pg.wait_for_timeout(200)
            open_before = await pg.evaluate("[...document.querySelectorAll('section.set-sec')].filter(s=>!s.classList.contains('hidden')).map(s=>s.id)")
            assert open_before == ["set-pics"], open_before
            # "/" focuses the box, but not while typing in a field
            await pg.locator("body").click(position={"x": 5, "y": 5}); await pg.keyboard.press("/")
            if vw > 800: assert await pg.evaluate("document.activeElement.id") == "set-q", "/ focuses the search"
            # API tokens: found by "api token", first, in Security, as its own card
            await pg.fill("#set-q", "api token"); await pg.wait_for_timeout(300)
            first = await pg.evaluate("[...document.querySelectorAll('.set-main .section-head')].filter(h=>h.offsetParent!==null).sort((a,b)=>a.getBoundingClientRect().top-b.getBoundingClientRect().top)[0].textContent.trim()")
            assert first == "API tokens" and await pg.evaluate("document.querySelector('#tok-name').closest('.set-sec').id") == "set-sec", first
            assert await pg.locator("#tok-name").is_visible(), "token box is shown in results"
            await pg.screenshot(path=S + tag + "-apitoken.png")
            # word that matches nothing
            await pg.fill("#set-q", "kubernetes"); await pg.wait_for_timeout(200)
            assert await pg.locator("#set-none").is_visible()
            assert len(await visible_cards(pg)) == 0
            # match inside a folded hint opens it
            await pg.fill("#set-q", "duckduckgo"); await pg.wait_for_timeout(300)
            assert await pg.locator(".set-main .clamp.open").count() >= 1, "folded help opens on a match"
            await pg.screenshot(path=S + tag + "-hint.png")
            # Esc clears and restores the section that was open
            await pg.keyboard.press("Escape"); await pg.wait_for_timeout(200)
            assert await pg.input_value("#set-q") == ""
            after = await pg.evaluate("[...document.querySelectorAll('section.set-sec')].filter(s=>!s.classList.contains('hidden')).map(s=>s.id)")
            assert after == open_before, after
            assert await pg.evaluate("document.querySelectorAll('.set-main .clamp.open').length") == 0, "folded help folds back"
            # the Connect a tool tab opens for a tab name
            await pg.fill("#set-q", "ollama"); await pg.wait_for_timeout(300)
            assert "ollama" in (await pg.evaluate("document.querySelector('#ct-tabs .on').textContent")).lower()
            # picking a section on the left leaves the search
            await pg.locator("#set-nav a[data-sec=ui]").click(); await pg.wait_for_timeout(200)
            assert await pg.input_value("#set-q") == ""
            # the pointer in API keys goes to Security
            await pg.locator("#set-nav a[data-sec=keys]").click(); await pg.wait_for_timeout(200)
            await pg.locator("a[data-goto=sec]").click(); await pg.wait_for_timeout(200)
            assert await pg.locator("#set-sec").is_visible() and await pg.locator("#tok-name").is_visible()
            if vw < 800:
                assert await pg.evaluate("document.documentElement.scrollWidth") <= vw, "no sideways scroll"
            await ctx.close()
        await b.close()
        assert not errs, errs
        print("ok")
asyncio.run(main())
