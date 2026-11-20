# Project conversation reads files: the planner (a node) lists, searches and reads,
# the answer shows chips and file:line links that open the read-only viewer.
import asyncio, json, os, ssl, urllib.request, http.cookiejar
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E + "shots/projectreads/"
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
    node = [n for n in api("/api/state")["nodes"] if n.get("online")][0]["id"]
    p = api("/api/projects", {"name": "Word counter"})
    api("/api/projects/" + p["id"], {"planner": "node:" + node})
    repo = None
    for root, dirs, files in os.walk(E + "ctl"):
        if root.endswith(p["id"]):
            repo = os.path.join(root, "repo"); break
    os.makedirs(repo + "/wordcount", exist_ok=True)
    open(repo + "/wordcount/core.py", "w").write("def count_words(text):\n    counts = {}\n    for w in text.lower().split():\n        counts[w] = counts.get(w, 0) + 1\n    return counts\n")
    open(repo + "/wordcount/cli.py", "w").write("import sys\nfrom .core import count_words\n\ndef main(argv):\n    print(count_words(' '.join(argv)))\n")
    async with async_playwright() as pwr:
        b = await pwr.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
        errs = []
        for scheme in ["light", "dark"]:
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1280, "height": 860}, color_scheme=scheme, device_scale_factor=2)
            pg = await ctx.new_page()
            pg.on("pageerror", lambda e: errs.append(str(e)))
            pg.on("console", lambda m: errs.append(m.text) if m.type == "error" else None)
            await pg.goto(U + "/")
            await pg.fill("#pw", pw); await pg.click("button[type=submit]")
            await pg.wait_for_selector("#side-tasks"); await pg.wait_for_timeout(1000)
            await pg.goto(U + "/#/projects/" + p["id"]); await pg.wait_for_timeout(1200)
            if scheme == "light":
                await pg.fill("#pj-text", "Where is the counting done, and does it ignore case?")
                await pg.click("#pj-go")
            await pg.wait_for_selector(".pj-answer-text", timeout=60000); await pg.wait_for_timeout(800)
            chips = await pg.locator(".pj-read").all_inner_texts()
            assert chips == ["Listed the project", 'Searched "count_words"', "Read wordcount/core.py 1-5"], chips
            assert await pg.locator(".pj-ref").count() == 2, "core.py:1 and cli.py are links"
            await pg.screenshot(path=S + "answer-%s.png" % scheme)
            await pg.click(".pj-ref >> nth=0"); await pg.wait_for_selector("#pjf-body .pj-hl")
            assert "count_words" in await pg.inner_text("#pjf-body")
            await pg.screenshot(path=S + "viewer-%s.png" % scheme)
            await ctx.close()
        await b.close()
        errs = [e for e in errs if "401" not in e]
        assert not errs, errs
        print("ok", S)

asyncio.run(main())
