# Settings → Storage: chat parts in the bar, Chats by size, clearing code files, deleting a chat. Desktop dark/light and phone.
import asyncio, json, os, ssl, urllib.request, http.cookiejar
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/storagechats/"
os.makedirs(S, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()
op = urllib.request.build_opener(urllib.request.HTTPSHandler(context=ssl._create_unverified_context()), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(path, body=None, method=None):
    r = urllib.request.Request(U + path, json.dumps(body).encode() if body is not None else None, {"Content-Type": "application/json"}, method=method)
    return json.load(op.open(r))
call("/api/login", {"password": pw})

# Two chats with files on disk: one with big code files and an image, one small.
ids = [call("/api/chats", {"text": t})["id"] for t in ("Plot the sales data", "Hello")]
import time
for _ in range(120):   # the fake model must have finished answering: a chat that is answering is not cleared
    if not any(c["busy"] for c in call("/api/chats") if c["id"] in ids): break
    time.sleep(1)
d = E + "ctl/chats/"
for sub, name, size in [("-runs", "1-plot.png", 3_000_000), ("-images", "a.png", 800_000)]:
    os.makedirs(d + ids[0] + sub, exist_ok=True)
    open(d + ids[0] + sub + "/" + name, "wb").write(b"\0" * size)
call("/api/storage?refresh=1")

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
        errs = []
        for scheme, vp in [("dark", (1440, 900)), ("light", (1440, 900)), ("light", (390, 800))]:
            tag = f"{scheme}-{vp[0]}"
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": vp[0], "height": vp[1]}, color_scheme=scheme)
            pg = await ctx.new_page()
            pg.on("pageerror", lambda e: errs.append(str(e)))
            pg.on("console", lambda m: errs.append(m.text) if m.type == "error" and "401" not in m.text else None)
            await pg.goto(U + "/"); await pg.fill("#pw", pw); await pg.click("button[type=submit]")
            await pg.wait_for_selector("#tile-nodes, #side-tasks"); await pg.wait_for_timeout(1500)
            await pg.goto(U + "/#/settings"); await pg.wait_for_selector("#storage-card", state="attached")
            await pg.evaluate("document.querySelector('[data-sec=maint]').click()")
            await pg.wait_for_selector("#st-legend span"); await pg.wait_for_timeout(500)
            legend = await pg.inner_text("#st-legend")
            assert "Chat code files" in legend and "Chat images" in legend, legend
            await pg.click("#st-chats-btn"); await pg.wait_for_selector(".st-chat-row")
            first = pg.locator(".st-chat-row").first
            assert "Plot the sales data" in await first.inner_text()
            assert "code files" in await first.inner_text()
            await pg.locator("#storage-card").scroll_into_view_if_needed()
            await pg.screenshot(path=S + f"storage-{tag}.png")
            await ctx.close()
        # delete the second chat from the list
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1440, "height": 900})
        pg = await ctx.new_page()
        await pg.goto(U + "/"); await pg.fill("#pw", pw); await pg.click("button[type=submit]")
        await pg.wait_for_selector("#tile-nodes, #side-tasks"); await pg.goto(U + "/#/settings"); await pg.wait_for_selector("#storage-card", state="attached")
        await pg.evaluate("document.querySelector('[data-sec=maint]').click()"); await pg.click("#st-chats-btn"); await pg.wait_for_selector(".st-chat-row")
        await pg.click("[data-st-clear]")
        await pg.locator("[data-st-clear]").wait_for(state="detached", timeout=10000)
        assert not os.path.exists(d + ids[0] + "-runs") and os.path.exists(d + ids[0] + "-images/a.png")
        await pg.screenshot(path=S + "cleared-dark-1440.png")
        n = await pg.locator(".st-chat-row").count()
        await pg.locator("[data-st-del]").last.click()
        await pg.click("#ask-yes")
        for _ in range(40):
            if await pg.locator(".st-chat-row").count() == n - 1: break
            await pg.wait_for_timeout(250)
        else: raise AssertionError("chat row not removed")
        assert not errs, errs
        await b.close()
    print("storagechats ok")
asyncio.run(main())
