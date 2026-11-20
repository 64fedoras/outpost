# Chat screenshots: paste an image into the Chat composer, see the warning when no
# Vision model is loaded, then (argument "vision", after the rig's model is marked
# as having an mmproj file) send it and see the thumbnail and the answer.
#   python3 testing/e2e/chatimages.py novision   # the rig as `run.sh up` leaves it
#   python3 testing/e2e/chatimages.py vision     # after adding mmproj-F16.gguf to tiny-test's hf_files and restarting the controller
import asyncio, base64, os, sys, struct, zlib
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/chatimages/"
os.makedirs(S, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()
MODE = sys.argv[1] if len(sys.argv) > 1 else "novision"

def png(w=64, h=40):
    raw = b"".join(b"\x00" + bytes([30, 35, 48]) * w for _ in range(h))
    chunk = lambda t, d: struct.pack(">I", len(d)) + t + d + struct.pack(">I", zlib.crc32(t + d) & 0xffffffff)
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0)) + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b"")

PASTE = """async (b64) => {
  const bin = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
  const dt = new DataTransfer(); dt.items.add(new File([bin], 'shot.png', { type: 'image/png' }));
  document.getElementById('chat-text').dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
}"""

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
        errs = []
        for scheme, vp in [("dark", {"width": 1440, "height": 900}), ("light", {"width": 1440, "height": 900}), ("phone", {"width": 390, "height": 844})]:
            ctx = await b.new_context(ignore_https_errors=True, viewport=vp, color_scheme="light" if scheme == "light" else "dark", device_scale_factor=2 if scheme == "phone" else 1)
            pg = await ctx.new_page()
            pg.on("pageerror", lambda e: errs.append(str(e)))
            await pg.goto(U + "/")
            await pg.fill("#pw", pw); await pg.click("button[type=submit]")
            await pg.wait_for_selector("#tile-nodes, #side-tasks"); await pg.wait_for_timeout(2500)
            await pg.goto(U + "/#/chat"); await pg.wait_for_selector(".mcard"); await pg.wait_for_timeout(800)
            await pg.evaluate(PASTE, base64.b64encode(png()).decode())
            await pg.wait_for_selector("#chat-imgs .img-chip")
            warn = await pg.locator("#chat-img-warn").is_visible()
            if MODE == "novision":
                assert warn, "warning should show with a text-only model"
                assert "Vision model" in await pg.inner_text("#chat-img-warn")
                await pg.fill("#chat-text", "What is wrong here?")
                await pg.screenshot(path=S + f"novision-{scheme}.png")
                await pg.click("#chat-send"); await pg.wait_for_timeout(800)
                assert "Vision model" in await pg.inner_text("#toast"), "the controller should refuse it with the same reason"
                assert await pg.locator("#chat-imgs .img-chip").count() == 1, "a refused image stays in the composer"
            else:
                assert not warn, "no warning with a vision model loaded"
                await pg.fill("#chat-text", "What is wrong here?")
                await pg.screenshot(path=S + f"compose-{scheme}.png")
                await pg.click("#chat-send")
                await pg.wait_for_selector(".bubble-imgs img", timeout=30000)
                await pg.wait_for_selector(".chat-answer .chat-text >> text=1 image(s)", timeout=60000); await pg.wait_for_timeout(800)  # the node got the image
                assert await pg.locator("#chat-imgs .img-chip").count() == 0, "composer clears after sending"
                ok = await pg.evaluate("async () => { const i = document.querySelector('.bubble-imgs img'); await i.decode(); return i.naturalWidth; }")
                assert ok == 64, f"thumbnail served from the controller: {ok}"
                await pg.screenshot(path=S + f"answer-{scheme}.png")
            await ctx.close()
        await b.close()
        assert not errs, errs
        print("chatimages", MODE, "ok")

asyncio.run(main())
