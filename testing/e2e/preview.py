# Live preview: an HTML answer and a Mermaid answer show as cards; Open shows them in the
# sandboxed panel (scripts run, network and cookies blocked), Code, versions, Save to project.
import asyncio, os
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/preview/"
os.makedirs(S, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()

PAGE = """Here you go:
```html
<!doctype html>
<html><head><title>Pomodoro timer</title>
<style>body{font:20px system-ui;display:grid;place-items:center;height:90vh;margin:0} b{font-size:64px}</style></head>
<body><div><b id=t>25:00</b><p id=net>checking…</p><p id=ck></p></div>
<script>
document.getElementById('t').textContent = '24:59';
fetch('/api/state').then(() => net.textContent = 'NETWORK OPEN', () => net.textContent = 'network blocked');
try { document.cookie; ck.textContent = 'COOKIES READABLE'; } catch (e) { ck.textContent = 'cookies blocked'; }
</script></body></html>
```"""
DIAGRAM = """```mermaid
graph LR
  A[Owner] --> B(Controller)
  B --> C[Node 1]
  B --> D[Node 2]
```"""

async def idle(pg):
    await pg.wait_for_timeout(700)
    await pg.wait_for_function("() => !document.querySelector('#chat-stop') || document.querySelector('#chat-stop').classList.contains('hidden')", timeout=60000)
    await pg.wait_for_timeout(500)

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
        errs = []
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1440, "height": 900}, color_scheme="light", device_scale_factor=2)
        pg = await ctx.new_page()
        pg.on("pageerror", lambda e: errs.append(str(e)))
        await pg.goto(U + "/")
        await pg.fill("#pw", pw); await pg.click("button[type=submit]")
        await pg.wait_for_selector("#side-tasks"); await pg.wait_for_timeout(1000)
        await pg.evaluate("() => post('/api/projects', {name: 'Timer site'})")
        await pg.goto(U + "/#/chat"); await pg.wait_for_selector(".mcard")
        await pg.fill("#chat-text", PAGE); await pg.click("#chat-send")
        await pg.wait_for_selector(".pv-card", timeout=60000); await idle(pg)
        await pg.fill("#chat-text", PAGE.replace("25:00", "50:00")); await pg.click("#chat-send"); await idle(pg)
        cards = await pg.locator(".pv-card").all_inner_texts()
        assert len(cards) == 2 and "pomodoro-timer.html" in cards[0] and "version 2" in cards[1], cards
        await pg.click(".pv-card >> nth=1 >> button")
        fr = pg.frame_locator("#pv-frame")
        await fr.locator("#net").filter(has_text="blocked").wait_for(timeout=10000)
        assert await fr.locator("#t").inner_text() == "24:59", "scripts run"
        assert await fr.locator("#ck").inner_text() == "cookies blocked"
        assert "Version 2 of 2" in await pg.inner_text("#pv-ver")
        await pg.screenshot(path=S + "html-light.png")
        await pg.click("#pv-tab-code"); await pg.wait_for_selector("#pv-code:not(.hidden)")
        assert "50:00" in await pg.inner_text("#pv-code")
        await pg.select_option("#pv-ver", "0")
        assert "25:00" in await pg.inner_text("#pv-code")
        await pg.click("#pv-save"); await pg.fill("#pv-save-path", "index.html"); await pg.click("#pv-save-go")
        await pg.wait_for_selector("#toast:has-text('index.html in')")
        await pg.click("#pv-x")
        # Mermaid, dark.
        await pg.emulate_media(color_scheme="dark")
        await pg.fill("#chat-text", DIAGRAM); await pg.click("#chat-send"); await idle(pg)
        await pg.click(".pv-card:has-text('diagram.mmd') >> button")
        await pg.frame_locator("#pv-frame").locator("svg").first.wait_for(timeout=20000)
        await pg.wait_for_timeout(500)
        await pg.screenshot(path=S + "mermaid-dark.png")
        await pg.click("#pv-x")
        # Phone width.
        await pg.set_viewport_size({"width": 390, "height": 800})
        await pg.click(".pv-card >> nth=0 >> button"); await pg.wait_for_timeout(1500)
        await pg.screenshot(path=S + "phone-dark.png")
        await b.close()
        assert not errs, errs
        print("ok", S)

asyncio.run(main())
