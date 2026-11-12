# Chat answers are drawn as markdown (web/markdown.js): headings, lists, a table, code with a
# Copy button, links; HTML in an answer stays text and nothing breaks the page's CSP.
# Screenshots: after-* is the page as it is; before-* shows the same answer as plain text, the
# way it was drawn before markdown.js (made here by swapping md() for the old escape-only code).
import asyncio, os
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/chatmd/"
os.makedirs(S, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()
SCALE = int(os.environ.get("SHOT_SCALE", "1"))
OLD = "window.md = (t) => t ? '<p class=\"md-plain\">' + esc(t) + '</p>' : ''; if (chatCur) { $('chat-msgs')._h = null; chatRender(); }"

async def login(pg):
    await pg.goto(U + "/")
    await pg.fill("#pw", pw); await pg.click("button[type=submit]")
    await pg.wait_for_selector("#tile-nodes, #side-tasks"); await pg.wait_for_timeout(2500)

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
        errs = []
        chat = None
        for scheme in ["dark", "light"]:
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1440, "height": 900}, color_scheme=scheme, device_scale_factor=SCALE)
            await ctx.grant_permissions(["clipboard-read", "clipboard-write"])
            pg = await ctx.new_page()
            pg.on("pageerror", lambda e: errs.append(str(e)))
            pg.on("console", lambda m: errs.append(m.text) if m.type == "error" else None)
            await login(pg)
            if not chat:
                await pg.goto(U + "/#/chat"); await pg.wait_for_selector(".mcard")
                await pg.fill("#chat-text", "markdown sample: what should I do with onions that are about to go bad")
                await pg.keyboard.press("Control+Enter")
                await pg.wait_for_selector("#chat-msgs > .chat-answer .chat-text.md", timeout=60000); await pg.wait_for_timeout(800)
                chat = pg.url
            else:
                await pg.goto(chat); await pg.wait_for_selector(".chat-answer .chat-text.md"); await pg.wait_for_timeout(800)
            a = pg.locator("#chat-msgs > .chat-answer .chat-text.md").first
            assert await a.locator("h3").count() == 2, "two headings"
            assert await a.locator("h3 strong").first.inner_text() == "Quick Fixes (Same Day/Next Day)"
            assert await a.locator("ul > li").count() >= 4 and await a.locator("ol > li").count() == 2, "lists"
            assert await a.locator("ol li ul li").count() == 2, "nested list"
            assert await a.locator("hr").count() == 1
            assert await a.locator("table tbody tr").count() == 3 and await a.locator("th").count() == 3, "table"
            assert await a.locator("blockquote").count() == 1
            assert await a.locator("li > code").first.inner_text() == "vinegar"
            code = await a.locator(".md-code pre").inner_text()
            assert 'print(f"{i}: <b>{o}</b> & caramelize")' in code and code.startswith("onions = "), code
            link = a.locator("a[href='https://example.com/onion-jam']")
            assert await link.get_attribute("rel") == "noopener noreferrer nofollow" and await link.get_attribute("target") == "_blank"
            text = await a.inner_text()
            assert "###" not in text and "**" not in text and "```" not in text, "no raw markdown left"
            # HTML in the answer is text, never a tag; a javascript: link is not a link
            assert await a.locator("script, img, iframe").count() == 0
            assert await a.locator("a").count() == 1, "only the https link is a link"
            assert "<script>window.mdTestHacked = 1</script>" in text
            assert await pg.evaluate("window.mdTestHacked") is None
            # your own message stays as typed
            assert await pg.locator(".bubble").first.inner_text() == "markdown sample: what should I do with onions that are about to go bad"
            await a.locator(".md-copy").click(); await pg.wait_for_timeout(200)
            assert (await pg.evaluate("navigator.clipboard.readText()")).startswith('onions = ["red"'), "Copy puts the code on the clipboard"
            await pg.wait_for_timeout(1600)
            await pg.screenshot(path=S + f"after-desktop-{scheme}.png")
            await pg.evaluate("document.querySelector('#chat-msgs .md-table').scrollIntoView({block: 'start'})"); await pg.wait_for_timeout(200)
            await pg.screenshot(path=S + f"after-desktop-{scheme}-code.png")
            await pg.evaluate(OLD); await pg.evaluate("$('chat-msgs').scrollTop = 0"); await pg.wait_for_timeout(200)
            await pg.screenshot(path=S + f"before-desktop-{scheme}.png")
            await ctx.close()
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 390, "height": 844}, color_scheme="dark", is_mobile=True, has_touch=True, device_scale_factor=2)
        pg = await ctx.new_page(); pg.on("pageerror", lambda e: errs.append(str(e)))
        pg.on("console", lambda m: errs.append(m.text) if m.type == "error" else None)
        await login(pg)
        await pg.goto(chat); await pg.wait_for_selector(".chat-answer .chat-text.md"); await pg.wait_for_timeout(800)
        # a wide table or a long code line scrolls inside its own box; the page never scrolls sideways
        assert await pg.evaluate("document.documentElement.scrollWidth <= window.innerWidth"), "phone: no sideways page scroll"
        assert await pg.evaluate("(() => { const m = document.getElementById('chat-msgs'); return m.scrollWidth <= m.clientWidth + 1; })()"), "phone: the answer fits the column"
        await pg.evaluate("$('chat-msgs').scrollTop = 0"); await pg.wait_for_timeout(200)
        await pg.screenshot(path=S + "after-phone.png")
        await pg.evaluate("document.querySelector('#chat-msgs .md-table').scrollIntoView({block: 'start'})"); await pg.wait_for_timeout(200)
        await pg.screenshot(path=S + "after-phone-code.png")
        await pg.evaluate(OLD); await pg.evaluate("$('chat-msgs').scrollTop = 0"); await pg.wait_for_timeout(200)
        await pg.screenshot(path=S + "before-phone.png")
        await b.close()
        errs = [e for e in errs if "status of 401" not in e]  # the sign-in page, before login
        assert not errs, f"console errors (a CSP report would show here): {errs}"
        print("chat markdown: ok, console errors: none")
asyncio.run(main())
