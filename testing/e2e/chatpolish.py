# Chat polish: export (Markdown, text), personas, Compare two nodes, and the local usage tile on Spending.
# The rig has one node, so this script joins a second real node (the same binary, its own data dir) for Compare.
import asyncio, json, os, subprocess, time, urllib.request, ssl
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/chatpolish/"
os.makedirs(S, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
pw = open(E + "pw.txt").read().strip()
CTX = ssl._create_unverified_context()

def call(path, body=None, jar=[None]):
    import http.cookiejar, urllib.request
    if jar[0] is None:
        jar[0] = urllib.request.build_opener(urllib.request.HTTPSHandler(context=CTX), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        req = urllib.request.Request(U + "/api/login", json.dumps({"password": pw}).encode(), {"Content-Type": "application/json"})
        jar[0].open(req).read()
    req = urllib.request.Request(U + path, json.dumps(body).encode() if body is not None else None, {"Content-Type": "application/json"})
    return json.load(jar[0].open(req))

def second_node():
    d = E + "node2"
    joined = os.path.isdir(d) and os.listdir(d)   # a second run reuses the node joined by the first
    os.makedirs(d, exist_ok=True)
    args = [E + "outpost", "node", "--data", d] + ([] if joined else ["--join", call("/api/tokens", {})["token"]])
    log = open(E + "node2.log", "w")
    proc = subprocess.Popen(args, stdout=log, stderr=log, cwd=E, start_new_session=True)
    def online(): return [n for n in call("/api/state")["nodes"] if n["online"]]
    for _ in range(60):
        if len(online()) >= 2: break
        time.sleep(1)
    else: raise SystemExit("second node never came online")
    for n in online():
        if not n["heartbeat"].get("loaded_model"): call(f"/api/nodes/{n['id']}/load", {"name": "tiny-test", "ctx": 2048})
    for _ in range(40):
        if sum(1 for n in online() if n["heartbeat"].get("inference_ready")) >= 2: break
        time.sleep(1)
    return proc

async def login(pg):
    await pg.goto(U + "/")
    await pg.fill("#pw", pw); await pg.click("button[type=submit]")
    await pg.wait_for_selector("#tile-nodes, #side-tasks"); await pg.wait_for_timeout(2500)

async def main():
    proc = second_node()
    try:
        async with async_playwright() as p:
            b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
            errs = []
            for scheme, vp in [("dark", (1440, 900)), ("light", (1440, 900)), ("light", (390, 800))]:
                tag = f"{scheme}-{vp[0]}"
                ctx = await b.new_context(ignore_https_errors=True, viewport={"width": vp[0], "height": vp[1]}, color_scheme=scheme, accept_downloads=True)
                pg = await ctx.new_page()
                pg.on("pageerror", lambda e: errs.append(str(e)))
                pg.on("console", lambda m: errs.append(m.text) if m.type == "error" and "401" not in m.text else None)
                await login(pg)
                await pg.goto(U + "/#/chat"); await pg.wait_for_selector(".mcard"); await pg.wait_for_timeout(600)
                await pg.fill("#chat-text", "Say hello in one short sentence.")
                await pg.keyboard.press("Control+Enter")
                await pg.wait_for_selector(".chat-answer .chat-text", timeout=60000); await pg.wait_for_timeout(1500)

                # export
                assert await pg.locator("#chat-export-btn").is_visible(), "Export shows once the chat has messages"
                await pg.click("#chat-export-btn")
                await pg.screenshot(path=S + f"export-{tag}.png")
                async with pg.expect_download() as dl:
                    await pg.click("#exp-md")
                path = await (await dl.value).path()
                md = open(path).read()
                assert md.startswith("# ") and "## You" in md and "## Assistant" in md and "Say hello" in md, md
                await pg.click("#chat-export-btn")
                async with pg.expect_download() as dl:
                    await pg.click("#exp-txt")
                txt = open(await (await dl.value).path()).read()
                assert "You" in txt and "Say hello" in txt
                # print view builds and is only visible while printing
                await pg.evaluate("window.print = () => {}")
                await pg.click("#chat-export-btn"); await pg.click("#exp-pdf"); await pg.wait_for_timeout(300)
                assert "Say hello" in await pg.inner_text("#chat-print")
                await pg.evaluate("window.dispatchEvent(new Event('afterprint'))")
                assert await pg.evaluate("!document.body.classList.contains('printing')")

                # personas
                await pg.click("#chat-instr-btn")
                await pg.fill("#chat-instr-text", "Answer like a pirate.")
                await pg.click("#chat-persona-save")
                await pg.fill("#chat-persona-name", f"Pirate {tag}")
                await pg.click("#chat-persona-ok"); await pg.wait_for_timeout(700)
                assert await pg.locator("#chat-persona option", has_text=f"Pirate {tag}").count() == 1
                await pg.fill("#chat-instr-text", "")
                await pg.select_option("#chat-persona", label=f"Pirate {tag}")
                assert await pg.input_value("#chat-instr-text") == "Answer like a pirate."
                await pg.screenshot(path=S + f"personas-{tag}.png")
                pg.once("dialog", lambda d: d.accept())
                await pg.click("#chat-persona-del")
                for sel in ["#confirm-ok", ".confirm-ok", "text=Delete >> nth=-1"]:
                    if await pg.locator(sel).first.is_visible(): await pg.locator(sel).first.click(); break
                await pg.wait_for_timeout(700)
                await pg.keyboard.press("Escape")
                await pg.evaluate("document.querySelectorAll('.overlay').forEach(o => o.classList.add('hidden'))")

                # compare
                await pg.wait_for_function("!document.getElementById('chat-cmp-btn').classList.contains('hidden')", timeout=15000)
                await pg.click("#chat-cmp-btn")
                assert await pg.locator("#cmp-a option").count() == 2
                assert await pg.input_value("#cmp-a") != await pg.input_value("#cmp-b")
                await pg.fill("#cmp-prompt", "Name one colour.")
                await pg.screenshot(path=S + f"compare-pick-{tag}.png")
                await pg.click("#cmp-run")
                await pg.wait_for_selector(".cmp-col .cmp-text", timeout=60000)
                await pg.wait_for_function("document.querySelectorAll('.cmp-col.done').length === 2", timeout=60000)
                assert await pg.locator("#cmp-run").is_enabled()
                await pg.screenshot(path=S + f"compare-done-{tag}.png")
                await pg.click("#modal-compare [data-close]")
                await ctx.close()

            # Spending tile
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1440, "height": 900}, color_scheme="dark")
            pg = await ctx.new_page(); await login(pg)
            await pg.goto(U + "/#/spending"); await pg.wait_for_selector("#sp-tiles .tile"); await pg.wait_for_timeout(800)
            t = await pg.inner_text("#sp-tiles")
            assert "Local models (free)" in t, t
            assert "answer" in t, "the chat and compare answers should be counted: " + t
            await pg.screenshot(path=S + "spending-dark-1440.png")
            assert not errs, errs
            await b.close()
    finally:
        import signal; os.killpg(proc.pid, signal.SIGTERM)
    print("chatpolish ok")

asyncio.run(main())
