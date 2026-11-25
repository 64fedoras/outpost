# Project research: with "Let chats search the web" on, the project planner (a node) looks an
# unknown name up (a "Searched the web" chip), then starts research; the lanes show live in the
# conversation, the report lands under them and is saved to the project's Stash (light, dark, phone).
# Search goes to the DuckDuckGo stand-in in mock_claude.py (OUTPOST_TEST_DDG, set by run.sh).
import asyncio, json, os, ssl, urllib.request, http.cookiejar
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/projresearch/"
os.makedirs(S, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()
ctxs = ssl.create_default_context(); ctxs.check_hostname = False; ctxs.verify_mode = ssl.CERT_NONE
jar = http.cookiejar.CookieJar()
op = urllib.request.build_opener(urllib.request.HTTPSHandler(context=ctxs), urllib.request.HTTPCookieProcessor(jar))

def api(path, body=None, method=None):
    data = json.dumps(body).encode() if body is not None else None
    r = urllib.request.Request(U + path, data=data, method=method or ("POST" if data else "GET"), headers={"content-type": "application/json"})
    return json.loads(op.open(r).read() or b"null")

async def main():
    api("/api/login", {"password": pw})
    before = api("/api/settings")
    api("/api/settings", {"chat_web_search": True, "chat_search_with": "local"})
    node = [n for n in api("/api/state")["nodes"] if n.get("online")][0]["id"]
    p = api("/api/projects", {"name": "Agent replacement"})
    api("/api/projects/" + p["id"], {"planner": "node:" + node})
    try:
        async with async_playwright() as pwr:
            b = await pwr.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
            errs = []
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1280, "height": 900}, color_scheme="light", device_scale_factor=2)
            pg = await ctx.new_page()
            pg.on("pageerror", lambda e: errs.append(str(e)))
            pg.on("console", lambda m: errs.append(m.text) if m.type == "error" and "status of 401" not in m.text else None)
            await pg.goto(U + "/")
            await pg.fill("#pw", pw); await pg.click("button[type=submit]")
            await pg.wait_for_selector("#side-tasks"); await pg.wait_for_timeout(800)
            await pg.goto(U + "/#/projects/" + p["id"]); await pg.wait_for_selector("#pj-text")
            await pg.fill("#pj-text", "build a replacement to hermes ai")
            await pg.keyboard.press("Enter")
            # The lookup chip, then the live lanes with a Stop button.
            await pg.wait_for_selector(".pj-read >> text=Searched the web for", timeout=60000)
            await pg.wait_for_selector(".pj-answer .rs-lane.rs-working", timeout=60000)
            assert "How does the Hermes AI agent framework work" in await pg.inner_text(".pj-rs-q")
            assert await pg.locator(".pj-answer .rs-h [data-act=proj-chat-stop]").count() == 1
            await pg.wait_for_selector(".pj-answer .rs-lane.rs-done", timeout=60000)
            await pg.screenshot(path=S + "lanes-light.png")
            await pg.wait_for_selector(".pj-rs-saved", timeout=180000)
            await pg.wait_for_timeout(600)
            saved = await pg.inner_text(".pj-rs-saved")
            assert "Research - How does the Hermes AI agent framework work" in saved, saved
            text = await pg.inner_text(".pj-answer >> nth=-1")
            assert "Short answer" in text and "example.org" in text, text
            await pg.click(".pj-answer .rs-rec summary")
            await pg.screenshot(path=S + "report-light.png", full_page=False)
            st = api("/api/projects/" + p["id"] + "/stash", {})
            res = api("/api/stash/" + st["id"])
            names = [r["name"] for r in res.get("resources", [])]
            assert any(n.startswith("Research - How does the Hermes") for n in names), names
            # The Stash link opens the project's stash.
            await pg.click(".pj-rs-saved a")
            await pg.wait_for_function("() => location.hash.startsWith('#/stash/')", timeout=10000)
            for scheme, vp, name in [("dark", {"width": 1280, "height": 900}, "report-dark.png"), ("light", {"width": 390, "height": 844}, "phone-light.png")]:
                c2 = await b.new_context(ignore_https_errors=True, viewport=vp, color_scheme=scheme, device_scale_factor=2)
                for ck in await ctx.cookies(): await c2.add_cookies([ck])
                p2 = await c2.new_page()
                p2.on("pageerror", lambda e: errs.append(str(e)))
                await p2.goto(U + "/#/projects/" + p["id"]); await p2.wait_for_selector(".pj-rs-saved")
                await p2.click(".pj-answer .rs-rec summary"); await p2.wait_for_timeout(500)
                await p2.locator(".pj-rs-q").scroll_into_view_if_needed()
                await p2.screenshot(path=S + name)
                await c2.close()
            assert not errs, errs
            await b.close()
    finally:
        api("/api/settings", {"chat_web_search": bool(before.get("chat_web_search")), "chat_search_with": before.get("chat_search_with", "")})
    print("projresearch ok")

asyncio.run(main())
