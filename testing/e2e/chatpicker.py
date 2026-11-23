# Chat picker with many nodes: one card per model ("on N nodes · N free"), "Pick a specific node…", and a chat sent to
# "this model, any free node". Run after `sh testing/e2e/run.sh up`. N=25 by default.
import asyncio, os, sys, json
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from manynodes import many
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/chatpicker/"
os.makedirs(S, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
N = int(os.environ.get("N", "25"))
pw = open(E + "pw.txt").read().strip()

def shaped(st):
    st = many(st, N)
    for i, n in enumerate(st["nodes"]):
        if i and i % 3 == 0 and n.get("heartbeat"): n["heartbeat"]["loaded_model"] = "Other-Model-9B"
        if n.get("heartbeat"): n["heartbeat"]["inference_ready"] = True
    return st

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
        errs = []
        for name, vp in [("desktop", {"width": 1440, "height": 900}), ("phone", {"width": 390, "height": 844})]:
            ctx = await b.new_context(ignore_https_errors=True, viewport=vp, color_scheme="dark")
            async def route(r):
                resp = await r.fetch(); await r.fulfill(response=resp, json=shaped(await resp.json()))
            sim = {"on": False}
            async def route2(r):
                if sim["on"]: await route(r)
                else: await r.continue_()
            await ctx.route("**/api/state/events*", lambda r: r.abort()); await ctx.route("**/api/state", route2)
            pg = await ctx.new_page()
            pg.on("pageerror", lambda e: errs.append(str(e)))
            pg.on("console", lambda m: errs.append(m.text) if m.type == "error" and "ERR_FAILED" not in m.text and "401" not in m.text else None)
            await pg.goto(U + "/"); await pg.fill("#pw", pw); await pg.click("button[type=submit]"); await pg.wait_for_selector("#side-tasks"); await pg.wait_for_timeout(1200)
            sim["on"] = True
            await pg.goto(U + "/#/chat"); await pg.wait_for_selector(".mcard"); await pg.evaluate("refresh()"); await pg.wait_for_timeout(800)
            if name == "phone" and await pg.locator("#chat-pick-more").count():
                await pg.click("#chat-pick-more"); await pg.wait_for_timeout(200)
            assert await pg.locator(".mcard").count() == 2, "only Auto and Research are cards"
            assert await pg.locator(".mpick-n").count() == 0, "the model list starts closed"
            await pg.screenshot(path=S + f"picker-{name}.png")
            await pg.locator("[data-specific]").first.click(); await pg.wait_for_timeout(200)
            assert await pg.locator(".mpick-row").count() >= 2, "the list shows each model"
            assert await pg.locator(".mpick-n", has_text="Any free node").count() >= 1, "a model on several nodes offers any free node"
            await pg.screenshot(path=S + f"picker-{name}-nodes.png")
            await pg.locator(".mpick-n", has_text="Any free node").first.click(); await pg.wait_for_timeout(200)
            assert (await pg.locator("#chat-node").input_value()).startswith("model:"), "the any-free-node chip means any free node with that model"
            await ctx.close()
        # the real controller accepts a model: choice
        ctx = await b.new_context(ignore_https_errors=True); pg = await ctx.new_page()
        await pg.goto(U + "/"); await pg.fill("#pw", pw); await pg.click("button[type=submit]"); await pg.wait_for_selector("#side-tasks")
        st = await pg.evaluate("fetch('/api/state').then(r=>r.json())")
        model = st["nodes"][0]["heartbeat"]["loaded_model"]
        r = await pg.evaluate("(m)=>fetch('/api/chats',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({text:'Say hi.',node:'model:'+m})}).then(r=>r.status)", model)
        assert r == 200, r
        await b.close()
        assert not errs, errs
        print("chatpicker: ok", N, "nodes")
asyncio.run(main())
