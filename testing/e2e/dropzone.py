# Drag-and-drop anywhere on the Updates page (needs `run.sh up` first; set OUTPOST_E2E_DIR).
import asyncio, os
from playwright.async_api import async_playwright
E = os.environ.get("OUTPOST_E2E_DIR", ".") + "/"
U = "https://127.0.0.1:18460"

async def main():
    async with async_playwright() as p:
        b = await p.chromium.launch(executable_path=os.environ.get("CHROMIUM_PATH") or None)
        pg = await b.new_page(ignore_https_errors=True, viewport={"width": 1360, "height": 900})
        errors = []; pg.on("pageerror", lambda e: errors.append(str(e)))
        await pg.goto(U + "/"); await pg.fill("#pw", open(E + "pw.txt").read().strip()); await pg.click("button[type=submit]")
        await pg.wait_for_selector("#nt-prompt")
        # off the Updates page: no overlay
        await pg.evaluate("""() => { const dt = new DataTransfer(); dt.items.add(new File(['x'], 'a')); document.body.dispatchEvent(new DragEvent('dragenter', {bubbles:true, cancelable:true, dataTransfer: dt})); }""")
        assert await pg.locator("#drop-overlay").is_hidden(), "overlay shown off Updates page"
        await pg.goto(U + "/#/updates"); await pg.wait_for_selector("#drop")
        # text drag ignored
        await pg.evaluate("""() => { const dt = new DataTransfer(); dt.setData('text/plain', 'hi'); document.querySelector('h2').dispatchEvent(new DragEvent('dragenter', {bubbles:true, cancelable:true, dataTransfer: dt})); }""")
        assert await pg.locator("#drop-overlay").is_hidden(), "overlay shown for text drag"
        # file drag over a far-from-dropbox element shows overlay
        await pg.evaluate("""() => { const dt = new DataTransfer(); dt.items.add(new File(['x'], 'a')); window._dt = dt; document.querySelector('.section-head').dispatchEvent(new DragEvent('dragenter', {bubbles:true, cancelable:true, dataTransfer: dt})); }""")
        assert await pg.locator("#drop-overlay").is_visible(), "overlay not shown"
        await pg.screenshot(path=E + "dropzone-overlay.png")
        # drop two files (one valid binary, one junk) on a non-drop-box element
        import base64
        real = base64.b64encode(open(E + "outpost", "rb").read()).decode()
        await pg.evaluate("""async (b64) => {
          const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
          const dt = new DataTransfer();
          dt.items.add(new File([bytes], 'outpost-linux-amd64'));
          dt.items.add(new File(['junk'], 'outpost-bogus'));
          const e = new DragEvent('drop', {bubbles:true, cancelable:true, dataTransfer: dt});
          document.querySelector('.section-head').dispatchEvent(e);
          window._prevented = e.defaultPrevented;
        }""", real)
        assert await pg.evaluate("window._prevented"), "drop not default-prevented"
        assert await pg.locator("#drop-overlay").is_hidden(), "overlay stays after drop"
        await pg.locator("#releases", has_text="linux-amd64").wait_for(timeout=60000)  # no string eval: the CSP forbids it
        print("releases:", (await pg.inner_text("#releases")).replace("\n", " | ")[:200])
        assert not errors, errors
        print("OK")
        await b.close()
asyncio.run(main())
