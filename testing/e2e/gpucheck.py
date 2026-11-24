# GPU speed check line in a node's details: slower (Mac Pro numbers), faster, skipped, and Re-check.
# The rig's node has no GPU, so its hello is patched in the browser with one GPU and a check
# result; the Re-check POST is intercepted. Run after `sh testing/e2e/run.sh up`.
import asyncio, os
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/gpucheck/"
os.makedirs(S, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()

CASES = {
    "slower": {"key": "k", "model": "qwen2.5-coder-3b-q4km", "gpu_tps": 3.88, "cpu_tps": 22.46, "slower": True},
    "faster": {"key": "k", "model": "qwen2.5-coder-3b-q4km", "gpu_tps": 96.4, "cpu_tps": 21.3},
    "skipped": {"key": "k", "skipped": "no model on this node is small enough to fit its GPU; it is checked after a smaller model is stored here"},
}

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
        errs = []
        runs = [("slower", "light", (1280, 800)), ("slower", "dark", (1280, 800)), ("faster", "light", (1280, 800)),
                ("skipped", "dark", (1280, 800)), ("slower", "dark", (390, 844))]
        for case, scheme, size in runs:
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": size[0], "height": size[1]}, color_scheme=scheme)
            posts = []
            def mk(case):
              async def state(r):
                resp = await r.fetch()
                if resp.status != 200:
                    await r.fulfill(response=resp); return
                st = await resp.json()
                h = st["nodes"][0].setdefault("hello", {})
                h["gpus"] = [{"vendor": "amd", "name": "AMD FirePro D700", "vram_bytes": 6 << 30, "backend": "metal"}]
                h["gpu_check"] = CASES[case]
                await r.fulfill(response=resp, json=st)
              return state
            state = mk(case)
            async def recheck(r):
                posts.append(r.request.url)
                await r.fulfill(status=202, json={"ok": True})
            await ctx.route("**/api/state/events*", lambda r: r.abort())
            await ctx.route("**/api/state", state)
            await ctx.route("**/api/nodes/*/gpu-recheck", recheck)
            pg = await ctx.new_page()
            pg.on("pageerror", lambda e: errs.append(str(e)))
            await pg.goto(U + "/"); await pg.fill("#pw", pw); await pg.click("button[type=submit]")
            await pg.wait_for_selector("#side-tasks"); await pg.wait_for_timeout(800)
            await pg.goto(U + "/#/nodes"); await pg.wait_for_timeout(800)
            await pg.locator("button[data-act=node-detail]").first.click(); await pg.wait_for_selector("#node-drawer:not(.hidden)")
            await pg.wait_for_timeout(500)
            want = {"slower": "GPU slower than CPU (3.9 vs 22 tok/s)", "faster": "GPU 96 tok/s, CPU 21 tok/s",
                    "skipped": "GPU speed not checked"}[case]
            line = pg.locator("#node-drawer .sub", has_text=want)
            assert await line.count() == 1, (case, await pg.locator("#node-drawer").inner_text())
            await line.scroll_into_view_if_needed()
            name = f"{case}-{scheme}" + ("-phone" if size[0] < 500 else "")
            await pg.screenshot(path=S + name + ".png")
            if case == "slower" and scheme == "light":
                await line.locator("[data-act=gpu-recheck]").click(); await pg.wait_for_timeout(400)
                assert posts, "Re-check did not post"
            await ctx.close()
        await b.close()
        assert not errs, errs
        print("gpucheck ok; shots in", S)

asyncio.run(main())
