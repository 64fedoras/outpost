# Research mode: pick Research in the chat, watch the lanes (waiting, working, done) while
# sub-questions run, get a report with numbered sources, open the lane record and the report
# panel; a second question stopped mid-way marks its lanes stopped (light, dark, phone).
# Search goes to a DuckDuckGo stand-in in mock_claude.py (OUTPOST_TEST_DDG, set by run.sh).
import asyncio, os
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/research/"
os.makedirs(S, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()

async def idle(pg):
    await pg.wait_for_timeout(700)
    await pg.wait_for_function("() => !document.querySelector('#chat-stop') || document.querySelector('#chat-stop').classList.contains('hidden')", timeout=180000)
    await pg.wait_for_timeout(500)

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
        errs = []
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1280, "height": 900}, color_scheme="light", device_scale_factor=2)
        pg = await ctx.new_page()
        pg.on("pageerror", lambda e: errs.append(str(e)))
        await pg.goto(U + "/")
        await pg.fill("#pw", pw); await pg.click("button[type=submit]")
        await pg.wait_for_selector("#side-tasks"); await pg.wait_for_timeout(1000)
        await pg.goto(U + "/#/chat"); await pg.wait_for_selector(".mcard")
        await pg.click(".mcard[data-model=research]")
        assert await pg.input_value("#chat-node") == "research"
        await pg.screenshot(path=S + "pick-light.png")
        await pg.fill("#chat-text", "How much power does a Raspberry Pi 5 use?")
        await pg.click("#chat-send")
        # Lanes appear; with one node they run one after another.
        await pg.wait_for_selector("#chat-live .rs-lane.rs-working", timeout=60000)
        await pg.wait_for_selector("#chat-live .rs-lane.rs-done", timeout=60000)
        await pg.screenshot(path=S + "lanes-light.png")
        assert await pg.locator("#chat-live .rs-lane").count() == 3
        await idle(pg)
        ans = await pg.inner_text(".chat-answer .chat-text")
        assert "Short answer" in ans and "[1] https://example.org/" in ans, ans
        sub = await pg.inner_text(".chat-answer .sub")
        assert "Research draft: 3 of 3 sub-questions answered on 1 node(s), 3 search(es)" in sub, sub
        await pg.click(".rs-rec summary")
        await pg.click(".rs-rec .rs-lane >> nth=0 >> summary")
        assert "example.org" in await pg.inner_text(".rs-rec .rs-find >> nth=0")
        await pg.screenshot(path=S + "report-light.png")
        await pg.click("[data-rs-open]")
        await pg.wait_for_selector("#pv-panel:not(.hidden)")
        assert (await pg.inner_text("#pv-name")).endswith("-report.md")
        await pg.wait_for_timeout(800)
        await pg.screenshot(path=S + "report-panel-light.png")
        await pg.click("#pv-x")
        # Stop a second research question while its lanes run.
        await pg.fill("#chat-text", "Is a Raspberry Pi 5 good as an Outpost node?")
        await pg.click("#chat-send")
        await pg.wait_for_selector("#chat-live .rs-lane.rs-working", timeout=60000)
        await pg.click("#chat-stop"); await idle(pg)
        last = pg.locator(".chat-answer").last
        assert "Stopped" in await last.inner_text()
        await last.locator(".rs-rec summary").click()
        st = await last.locator(".rs-lane.rs-stopped").count()
        assert st >= 1, await last.inner_html()
        await pg.emulate_media(color_scheme="dark"); await pg.wait_for_timeout(500)
        await pg.screenshot(path=S + "chat-dark.png")
        await pg.set_viewport_size({"width": 390, "height": 844}); await pg.wait_for_timeout(600)
        await pg.screenshot(path=S + "chat-phone-dark.png", full_page=False)
        await b.close()
        assert not errs, errs
        print("ok", S)

asyncio.run(main())
