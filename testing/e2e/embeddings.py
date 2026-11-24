# /v1/embeddings through the real controller and a real node running the stub llama-server, plus the Models page.
import asyncio, base64, json, os, ssl, struct, urllib.request, http.cookiejar
E_ = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
S = E_ + "shots/embeddings/"
os.makedirs(S, exist_ok=True)
from playwright.async_api import async_playwright
U = "https://127.0.0.1:18460"
pw = open(E_ + "pw.txt").read().strip()
op = urllib.request.build_opener(urllib.request.HTTPSHandler(context=ssl._create_unverified_context()), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
def call(path, body=None, method=None, ok=True):
    r = urllib.request.Request(U + path, json.dumps(body).encode() if body is not None else None, {"Content-Type": "application/json"}, method=method)
    try: return json.load(op.open(r))
    except urllib.error.HTTPError as e:
        if ok: raise
        return {"status": e.code, **json.loads(e.read() or b"{}")}
call("/api/login", {"password": pw})
tok = call("/api/apitokens", {"name": "embed test", "scope": "chat"})["token"]

def v1(body):
    r = urllib.request.Request(U + "/v1/embeddings", json.dumps(body).encode(), {"Content-Type": "application/json", "Authorization": "Bearer " + tok})
    return json.load(urllib.request.build_opener(urllib.request.HTTPSHandler(context=ssl._create_unverified_context())).open(r, timeout=120))
def cos(a, b): return sum(x * y for x, y in zip(a, b))

# the first call pushes the model to the node and starts the embedding server
r = v1({"input": ["the cat sat on the mat", "a cat on a mat", "quarterly tax filing deadline"]})
d = [x["embedding"] for x in r["data"]]
assert r["model"] == "nomic-embed-text-v1.5-q8" and len(d) == 3 and len(d[0]) == 64, r["model"]
assert abs(cos(d[0], d[0]) - 1) < 1e-4, "vectors are unit length"
assert cos(d[0], d[1]) > cos(d[0], d[2]) + 0.3, "similar texts are closer"
assert r["usage"]["prompt_tokens"] > 0
b = v1({"input": "hello world", "encoding_format": "base64"})["data"][0]["embedding"]
assert len(struct.unpack("<64f", base64.b64decode(b))) == 64
models = call("/v1/models", None) if False else None
req = urllib.request.Request(U + "/v1/models", headers={"Authorization": "Bearer " + tok})
ms = json.load(urllib.request.build_opener(urllib.request.HTTPSHandler(context=ssl._create_unverified_context())).open(req))["data"]
assert any(m["id"] == "nomic-embed-text-v1.5-q8" and m.get("capabilities") == ["embedding"] for m in ms), ms
# the embedding model is not offered as a chat model
state = call("/api/state")
assert state["nodes"][0]["heartbeat"]["loaded_model"] == "tiny-test"

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or os.environ.get("PW_CHROMIUM") or None)
        errs = []
        for scheme, vp in [("dark", (1440, 900)), ("light", (390, 800))]:
            ctx = await b.new_context(ignore_https_errors=True, viewport={"width": vp[0], "height": vp[1]}, color_scheme=scheme)
            pg = await ctx.new_page()
            pg.on("pageerror", lambda e: errs.append(str(e)))
            pg.on("console", lambda m: errs.append(m.text) if m.type == "error" and "401" not in m.text else None)
            await pg.goto(U + "/"); await pg.fill("#pw", pw); await pg.click("button[type=submit]")
            await pg.wait_for_selector("#tile-nodes, #side-tasks"); await pg.wait_for_timeout(1500)
            await pg.goto(U + "/#/models"); await pg.wait_for_selector("text=Nomic Embed Text"); await pg.wait_for_timeout(800)
            row = pg.locator("tr", has_text="Nomic Embed Text v1.5 (test stub)").first
            assert "Embedding" in await row.inner_text()
            assert "Load on" not in await row.inner_text(), "an embedding model has no Load button"
            cat = pg.locator(".cat-row", has_text="Qwen3 Embedding").first
            await cat.scroll_into_view_if_needed()
            assert "Embedding" in await cat.inner_text() and "Add to library" in await cat.inner_text()
            assert "Add and load" not in await cat.inner_text()
            await pg.screenshot(path=S + f"models-{scheme}-{vp[0]}.png")
            await ctx.close()
        assert not errs, errs
        await b.close()
    print("embeddings ok")
asyncio.run(main())
