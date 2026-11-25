# Project Artifacts as folders and files: a finished task with build outputs shows a
# "Result files" card and one card per output; each opens the task's file browser in the
# project pane (folders, a Build outputs heading, a text output read in place, a binary
# one named as binary), instead of downloading a zip. Then uploads: files and a folder
# go up, each type opens in its viewer (picture, CSV table, PDF text, STL 3D, audio,
# text), a file and a folder are deleted, and a build output is deleted.
# Shots in $OUTPOST_E2E_DIR/shots/projfiles/.
import asyncio, base64, io, os, struct, wave
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
OUT = E + "shots/projfiles/"
os.makedirs(OUT, exist_ok=True)
U = "https://127.0.0.1:18460"
POST = "(a) => fetch(a[0], {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(a[1] || {})}).then(r => r.json())"
GET = "(u) => fetch(u).then(r => r.json())"

async def until(pg, expr, timeout=15000):
    # page.wait_for_function evaluates a string, which the UI's CSP forbids; poll instead
    for _ in range(timeout // 100):
        if await pg.evaluate(f"() => !!({expr})"):
            return
        await pg.wait_for_timeout(100)
    raise AssertionError("timed out: " + expr)

def pdf(text):
    b = io.BytesIO(); offs = []
    def obj(x):
        offs.append(b.tell()); b.write(f"{len(offs)} 0 obj\n{x}\nendobj\n".encode("latin-1"))
    b.write(b"%PDF-1.4\n")
    obj("<< /Type /Catalog /Pages 2 0 R >>")
    obj("<< /Type /Pages /Kids [4 0 R] /Count 1 >>")
    obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
    obj("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents 5 0 R >>")
    c = f"BT /F1 12 Tf 72 720 Td ({text}) Tj ET"
    obj(f"<< /Length {len(c)} >>\nstream\n{c}\nendstream")
    x = b.tell()
    b.write(f"xref\n0 {len(offs) + 1}\n0000000000 65535 f \n".encode())
    for o in offs: b.write(f"{o:010d} 00000 n \n".encode())
    b.write(f"trailer\n<< /Size {len(offs) + 1} /Root 1 0 R >>\nstartxref\n{x}\n%%EOF\n".encode())
    return b.getvalue()

def wav():
    b = io.BytesIO(); w = wave.open(b, "wb"); w.setnchannels(1); w.setsampwidth(2); w.setframerate(8000)
    w.writeframes(b"".join(struct.pack("<h", int(8000 * ((i // 20) % 2 * 2 - 1))) for i in range(8000))); w.close()
    return b.getvalue()

PNG = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
STL = "solid t\n" + "".join(f"facet normal 0 0 1\nouter loop\nvertex {a}\nvertex {b}\nvertex {c}\nendloop\nendfacet\n" for a, b, c in [
    ("0 0 0", "1 0 0", "0 1 0"), ("0 0 0", "0 1 0", "0 0 1"), ("0 0 0", "0 0 1", "1 0 0"), ("1 0 0", "0 1 0", "0 0 1")]) + "endsolid t\n"
UPLOAD = "(a) => { const fd = new FormData(); for (const [n, b64] of a[1]) fd.append('files', new Blob([Uint8Array.from(atob(b64), (c) => c.charCodeAt(0))]), n); return fetch(a[0], {method: 'POST', body: fd}).then(r => r.json()); }"

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("OUTPOST_CHROMIUM") or None, args=["--no-sandbox"])
        errors = []
        ctx = await b.new_context(ignore_https_errors=True, viewport={"width": 1600, "height": 900})
        pg = await ctx.new_page()
        pg.on("pageerror", lambda e: errors.append(str(e)))
        await pg.goto(U + "/"); await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
        await pg.wait_for_selector("#nt-prompt")
        pid = (await pg.evaluate(POST, ["/api/projects", {"name": "Files as folders"}]))["id"]
        tid = (await pg.evaluate(POST, ["/api/tasks", {"prompt": "Write a word counter and build artifacts", "project": pid}]))["id"]
        for _ in range(300):
            t = await pg.evaluate(GET, "/api/tasks/" + tid)
            if t["status"] in ("awaiting_approval", "awaiting_answers"):
                break
            await pg.wait_for_timeout(500)
        await pg.evaluate(POST, ["/api/tasks/" + tid + "/approve", {}])
        for _ in range(600):
            t = await pg.evaluate(GET, "/api/tasks/" + tid)
            if t["status"] in ("done", "failed"):
                break
            await pg.wait_for_timeout(500)
        assert t["status"] == "done", t["status"]
        assert any(a["path"] == "dist/info.txt" for a in t.get("artifacts") or []), t.get("artifacts")

        await pg.goto(U + "/#/projects/" + pid)
        await pg.wait_for_selector("#pj-name")
        if not await pg.locator("#pj-wrap.panel-open").count():
            await pg.click("#pj-threads-btn")
        if await pg.locator("#pj-tab-context.on").count():
            await pg.click("#pj-tab-threads")
        await pg.wait_for_selector("#pj-threads .pj-acard.k-result")
        assert "Result files" in await pg.inner_text("#pj-threads .pj-acard.k-result")
        assert ".zip" not in await pg.inner_text("#pj-threads .pj-arts")

        # the result card opens the task in the pane with its file browser
        await pg.click("#pj-threads .pj-acard.k-result")
        await until(pg, f"location.hash === '#/projects/{pid}/tasks/{tid}'")
        await pg.wait_for_selector("#modal-files:not(.hidden) #tv-files .fb-row")
        tree = await pg.inner_text("#tv-files")
        assert "Build outputs" in tree and "info.txt" in tree and "app.bin" in tree, tree
        assert await pg.locator("#tv-files a[data-art]").count() == 2
        assert await pg.locator("#tv-files .fb-tick").count() >= 1
        await pg.click("#tv-files a[data-art][data-path='dist/info.txt']")
        await until(pg, "document.getElementById('fb-body').textContent.includes('v1')")
        assert "Build output" in await pg.inner_text("#fb-title")
        assert (await pg.get_attribute("#fb-dl", "href")).endswith("/artifact?path=dist%2Finfo.txt")
        await pg.screenshot(path=OUT + "browser.png")
        await pg.click("#tv-files a[data-art][data-path='dist/app.bin']")
        await until(pg, "document.getElementById('fb-body').textContent.includes('binary file')")
        await pg.click("#modal-files [data-close]")

        # a build output card opens that output straight away
        await pg.click("#pj-pane-back")
        await pg.wait_for_selector("#pj-threads .pj-acard.k-build")
        await pg.click("#pj-threads .pj-acard.k-build[data-path='dist/info.txt']")
        await pg.wait_for_selector("#modal-files:not(.hidden)")
        await until(pg, "document.getElementById('fb-body').textContent.includes('v1')")
        await pg.click("#modal-files [data-close]")

        # uploads: two files through the dialog's Upload files button, a folder by its paths
        ud = E + "shots/projfiles/up/"; os.makedirs(ud, exist_ok=True)
        open(ud + "table.csv", "w").write('name,count\n"Smith, J",3\nLee,5\n')
        open(ud + "pixel.png", "wb").write(PNG)
        await pg.click("#pj-pane-back")
        await pg.click("#pj-threads [data-act=proj-art-up]")
        await pg.wait_for_selector("#modal-files:not(.hidden) #fb-up-f:not(.hidden)")
        assert await pg.inner_text("#files-title") == "Artifacts"
        assert not await pg.locator("#files-zip").is_visible()
        await pg.set_input_files("#fb-up-files", [ud + "table.csv", ud + "pixel.png"])
        await pg.wait_for_selector("#tv-files a[data-path='table.csv']")
        b64 = lambda x: base64.b64encode(x if isinstance(x, bytes) else x.encode()).decode()
        r = await pg.evaluate(UPLOAD, ["/api/projects/" + pid + "/artifacts", [["kit/doc/spec.pdf", b64(pdf("Hello from the spec"))], ["kit/part.stl", b64(STL)], ["kit/beep.wav", b64(wav())], ["kit/readme.md", b64("# Kit\n")]]])
        assert len(r["saved"]) == 4, r
        await pg.click("#modal-files [data-close]")
        await pg.evaluate("pjFetch(true).then(renderProjectPage)")
        await pg.wait_for_selector("#pj-threads .pj-acard.k-folder")
        assert "kit/" in await pg.inner_text("#pj-threads .pj-acard.k-folder")
        await pg.click("#pj-threads .pj-acard.k-upload[data-path='table.csv']")
        await pg.wait_for_selector("#fb-media:not(.hidden) .fb-table td")
        cells = await pg.locator("#fb-media .fb-table td").all_inner_texts()
        assert cells[:2] == ["Smith, J", "3"], cells
        await pg.screenshot(path=OUT + "csv.png")
        await pg.click("#tv-files a[data-path='pixel.png']")
        await pg.wait_for_selector("#fb-media:not(.hidden) img.fb-img")
        await until(pg, "document.querySelector('#fb-media img').naturalWidth === 1")
        await pg.click("#tv-files a[data-path='kit/doc/spec.pdf']")
        await until(pg, "document.getElementById('fb-body').textContent.includes('Hello from the spec')")
        assert await pg.locator("#fb-open").is_visible()
        await pg.click("#tv-files a[data-path='kit/part.stl']")
        await pg.wait_for_selector("#fb-media:not(.hidden) canvas.fb-3d, #fb-media:not(.hidden) p")
        await pg.screenshot(path=OUT + "stl.png")
        await pg.click("#tv-files a[data-path='kit/beep.wav']")
        await pg.wait_for_selector("#fb-media:not(.hidden) audio")
        await until(pg, "document.querySelector('#fb-media audio').readyState >= 1", 10000)
        await pg.click("#tv-files a[data-path='kit/readme.md']")
        await until(pg, "document.getElementById('fb-body').textContent.includes('# Kit')")
        assert await pg.locator("#fb-preview").is_visible()

        # delete one file from the viewer, then a whole folder from the tree
        await pg.click("#tv-files a[data-path='pixel.png']")
        await pg.wait_for_selector("#fb-media img")
        await pg.click("#fb-del")
        await pg.click("#modal-ask #ask-yes")
        await until(pg, "!document.querySelector(\"#tv-files a[data-path='pixel.png']\")")
        await pg.hover("#tv-files .fb-dir:has-text('kit/')")
        await pg.click("#tv-files [data-act=fb-del][data-path='kit']")
        assert "4 files" in await pg.inner_text("#modal-ask")
        await pg.click("#modal-ask #ask-yes")
        await until(pg, "!document.querySelector(\"#tv-files a[data-path^='kit/']\")")
        left = await pg.evaluate(GET, "/api/projects/" + pid + "/artifacts")
        assert [f["path"] for f in left["files"]] == ["table.csv"], left
        await pg.click("#modal-files [data-close]")

        # a build output can be deleted from the task's file browser
        await pg.evaluate("pjFetch(true).then(renderProjectPage)")
        await pg.wait_for_selector("#pj-threads .pj-acard.k-result")
        await pg.click("#pj-threads .pj-acard.k-result")
        await pg.wait_for_selector("#modal-files:not(.hidden) #tv-files a[data-art]")
        await pg.click("#tv-files a[data-art][data-path='dist/app.bin']")
        await pg.click("#fb-del")
        await pg.click("#modal-ask #ask-yes")
        await until(pg, "!document.querySelector(\"#tv-files a[data-art][data-path='dist/app.bin']\")")
        t = await pg.evaluate(GET, "/api/tasks/" + tid)
        assert [a["path"] for a in t["artifacts"]] == ["dist/info.txt"], t["artifacts"]
        print("page errors:", errors)
        assert not errors
        await b.close()
asyncio.run(main())
